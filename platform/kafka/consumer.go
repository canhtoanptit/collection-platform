package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/canhtoanptit/collection-platform/platform/events"
	"github.com/canhtoanptit/collection-platform/platform/health"
	"github.com/canhtoanptit/collection-platform/platform/httpkit"
	"github.com/canhtoanptit/collection-platform/platform/otelkit"
)

// Consumer defaults.
const (
	// defaultMaxRetries is the A§27 retry budget: the handler is called up to
	// four times in total (one attempt plus three retries) before the record is
	// dead-lettered. Enough to ride out a failover or a brief dependency
	// outage; not enough to hold a partition for minutes.
	defaultMaxRetries = 3
	// defaultBackoff is the first retry delay; each retry doubles it.
	defaultBackoff = 100 * time.Millisecond
	// defaultMaxBackoff caps the doubling.
	defaultMaxBackoff = 5 * time.Second
	// defaultMaxPollRecords bounds one batch. Smaller batches mean more frequent
	// commits, so less duplicate work after a crash; larger ones amortise the
	// commit. 100 is a compromise, and it is the unit of the drain budget below.
	defaultMaxPollRecords = 100
	// defaultDrainTimeout is the budget for finishing one polled batch. It is
	// also the worst-case graceful-shutdown latency, because a batch that is
	// already in flight when ctx is cancelled is allowed to finish (see Run).
	// Keep it below the group's rebalance timeout.
	defaultDrainTimeout = 30 * time.Second
	// commitTimeout bounds the offset commit that closes each batch. It is not
	// configurable: a commit is one small request to the group coordinator, and
	// the only interesting failure is "the coordinator is gone", which this
	// detects quickly and reports.
	commitTimeout = 10 * time.Second
)

// Handler processes one validated event.
//
// It receives an envelope that has already passed both schemas, in a context
// carrying the producer's trace, the flow's correlation id and a causation id set
// to this event's eventId — so an event the handler emits through the outbox
// records what caused it without the handler doing anything.
//
// The contract for the return value is short and load-bearing:
//
//   - nil means the record is settled. The offset is committed.
//   - a non-nil error means "try again": the consumer retries with backoff and
//     then dead-letters. Do not return an error for a message this service does
//     not care about — return nil, or let the registry skip it.
//   - the handler must be idempotent. Delivery is at-least-once, so it will
//     sometimes see an event twice; platform/inbox.Dedupe in the same
//     transaction as the side effects is how that is made harmless (D§3.5).
type Handler func(ctx context.Context, env events.Envelope) error

// ConsumerConfig configures a [Consumer]. The connection fields mirror [Config]
// and bind the same environment variables, so a service that consumes and
// publishes configures its broker connection once.
type ConsumerConfig struct {
	// Brokers is the bootstrap broker list.
	Brokers []string `env:"KAFKA_BROKERS" envSeparator:","`
	// TLS dials over TLS. Implied by SASLIAMRegion.
	TLS bool `env:"KAFKA_TLS"`
	// SASLIAMRegion enables MSK IAM authentication. See Config.SASLIAMRegion.
	SASLIAMRegion string `env:"KAFKA_SASL_IAM_REGION"`

	// Group is the consumer group id. It is the service name: a group per
	// service, so scaling a Deployment shares the partitions instead of
	// duplicating the work, and so the inbox's `consumer` column has one value
	// per service (LIB-C).
	Group string `env:"KAFKA_CONSUMER_GROUP"`
	// Topics is the subscription. Domain services subscribe to
	// `collections.<context>` and the canonical `ingestion.*` topics only — raw
	// CDC and raw webhook topics are internal to ingestion/ (CLAUDE.md §5).
	Topics []string `env:"KAFKA_TOPICS" envSeparator:","`

	// Registry validates every message before the handler sees it. Required:
	// a consumer without one would hand unvalidated JSON to business code, and
	// ADR-0004's guarantee is that every message is validated at runtime.
	Registry *events.Registry

	// Handler processes each validated event. Required.
	Handler Handler

	// DLQTopic is where records that cannot be handled go: `collections.dlq.<service>`
	// per A§27. Required — a consumer with nowhere to put a poison message would
	// have to choose between dropping it and blocking its partition forever, and
	// CLAUDE.md §5 forbids both.
	DLQTopic string `env:"KAFKA_DLQ_TOPIC"`

	// MaxRetries is how many times a failing handler is retried before the
	// record is dead-lettered. Zero means the default (3); negative means no
	// retries at all, which is what a handler with no transient failure modes
	// wants.
	MaxRetries int `env:"KAFKA_MAX_RETRIES" envDefault:"3"`
	// Backoff is the delay before the first retry; it doubles for each
	// subsequent one, up to MaxBackoff.
	Backoff time.Duration `env:"KAFKA_RETRY_BACKOFF" envDefault:"100ms"`
	// MaxBackoff caps the exponential growth.
	MaxBackoff time.Duration `env:"KAFKA_RETRY_MAX_BACKOFF" envDefault:"5s"`

	// MaxPollRecords bounds one polled batch. See defaultMaxPollRecords.
	MaxPollRecords int `env:"KAFKA_MAX_POLL_RECORDS" envDefault:"100"`
	// DrainTimeout is the budget for one batch, and so the worst-case graceful
	// shutdown latency. See defaultDrainTimeout.
	DrainTimeout time.Duration `env:"KAFKA_DRAIN_TIMEOUT" envDefault:"30s"`

	// DeliveryTimeout bounds the DLQ produce. See Config.DeliveryTimeout — an
	// unbounded produce here would mean a record that can be neither handled nor
	// dead-lettered blocks its partition silently, which is the one outcome A§27
	// rules out.
	DeliveryTimeout time.Duration `env:"KAFKA_DELIVERY_TIMEOUT" envDefault:"30s"`

	// ClientID identifies the process to the brokers. Defaults to Group.
	ClientID string `env:"KAFKA_CLIENT_ID"`
}

// Consumer is a consumer group that runs [Handler] over validated envelopes with
// the A§27 retry and DLQ semantics. Build it with [NewConsumer] and drive it with
// [Consumer.Run].
type Consumer struct {
	client  *kgo.Client
	cfg     ConsumerConfig
	metrics *metrics
	tracer  trace.Tracer

	closeOnce sync.Once
}

// NewConsumer validates the configuration and builds the client. It does not
// join the group and does not dial — [Consumer.Run] does both.
//
// The same client both consumes and produces: the DLQ record is produced with
// acks=all and the idempotent producer, over the connection and credentials the
// consumer already has. A second client for four bytes of dead letters would be
// a second thing to configure and a second thing to get wrong.
func NewConsumer(cfg ConsumerConfig) (*Consumer, error) {
	cfg, err := cfg.validated()
	if err != nil {
		return nil, fmt.Errorf("creating the Kafka consumer: %w", err)
	}

	connCfg := Config{
		Brokers:         cfg.Brokers,
		TLS:             cfg.TLS,
		SASLIAMRegion:   cfg.SASLIAMRegion,
		ClientID:        cfg.ClientID,
		DeliveryTimeout: cfg.DeliveryTimeout,
	}
	opts, err := connCfg.connectionOpts()
	if err != nil {
		return nil, fmt.Errorf("creating the Kafka consumer: %w", err)
	}
	opts = append(opts, connCfg.producerOpts()...)
	opts = append(opts,
		kgo.ConsumerGroup(cfg.Group),
		kgo.ConsumeTopics(cfg.Topics...),
		// Offsets are committed by Run after each batch is settled. Automatic
		// commits would commit records the handler has not seen yet, which is
		// exactly the at-most-once behaviour this platform refuses.
		kgo.DisableAutoCommit(),
		// A rebalance between polling a batch and committing it would hand a
		// partition to another member while this one is still working on it.
		// Blocking rebalances until AllowRebalance keeps handling and ownership
		// aligned; the cost is that a rebalance waits for the current batch,
		// which is what DrainTimeout bounds.
		kgo.BlockRebalanceOnPoll(),
		// A group with no committed offset starts at the beginning of the topic.
		// Events are the replay mechanism (D§49) and consumers are idempotent,
		// so reading history is the useful default; "latest" would silently skip
		// everything published before a new consumer was deployed.
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)

	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("creating the Kafka consumer: %w", err)
	}
	m, err := newMetrics()
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("creating the Kafka consumer: %w", err)
	}

	return &Consumer{client: client, cfg: cfg, metrics: m, tracer: newTracer()}, nil
}

// validated returns a copy with defaults applied, or an error naming every
// problem at once — the same fail-complete rule platform/config follows.
func (c ConsumerConfig) validated() (ConsumerConfig, error) {
	var errs []error

	c.Brokers = nonEmpty(c.Brokers)
	if len(c.Brokers) == 0 {
		errs = append(errs, errors.New("no brokers configured (KAFKA_BROKERS)"))
	}
	if c.Group == "" {
		errs = append(errs, errors.New("no consumer group configured (KAFKA_CONSUMER_GROUP)"))
	}
	c.Topics = nonEmpty(c.Topics)
	if len(c.Topics) == 0 {
		errs = append(errs, errors.New("no topics to subscribe to (KAFKA_TOPICS)"))
	}
	if c.Registry == nil {
		errs = append(errs, errors.New("no event registry — every message must be validated (ADR-0004)"))
	}
	if c.Handler == nil {
		errs = append(errs, errors.New("no handler"))
	}
	if c.DLQTopic == "" {
		errs = append(errs, errors.New(
			"no DLQ topic configured (KAFKA_DLQ_TOPIC) — a poison message must not block a partition or be dropped (A§27)"))
	}
	for _, topic := range c.Topics {
		if topic == c.DLQTopic {
			errs = append(errs, fmt.Errorf(
				"topic %s is also the DLQ topic — a consumer that reads its own dead letters loops forever", topic))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return ConsumerConfig{}, err
	}

	if c.MaxRetries == 0 {
		c.MaxRetries = defaultMaxRetries
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	if c.Backoff <= 0 {
		c.Backoff = defaultBackoff
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = defaultMaxBackoff
	}
	if c.MaxBackoff < c.Backoff {
		c.MaxBackoff = c.Backoff
	}
	if c.MaxPollRecords <= 0 {
		c.MaxPollRecords = defaultMaxPollRecords
	}
	if c.DrainTimeout <= 0 {
		c.DrainTimeout = defaultDrainTimeout
	}
	if c.DeliveryTimeout <= 0 {
		c.DeliveryTimeout = defaultDeliveryTimeout
	}
	if c.ClientID == "" {
		c.ClientID = c.Group
	}
	return c, nil
}

// Run joins the group and consumes until ctx is cancelled or the client is
// closed. It returns nil on a clean shutdown.
//
// The loop is: poll a batch, handle every partition in it (each partition's
// records sequentially, partitions concurrently), commit what was settled, allow
// a rebalance, repeat.
//
// # Shutdown
//
// Cancelling ctx stops the loop, but not mid-batch. A batch already polled is
// handled to completion with a context detached from ctx (bounded by
// DrainTimeout) and then committed. That detachment is deliberate: if handling
// inherited the cancellation, shutting a pod down would make every in-flight
// handler fail its context, exhaust its retries and dead-letter records that
// were never actually poison. Kubernetes' terminationGracePeriodSeconds must
// therefore exceed DrainTimeout, or the drain is killed and the batch is
// redelivered instead (which is safe, just wasteful).
//
// # Errors
//
// Run returns an error only when a record could be neither handled nor
// dead-lettered — the DLQ produce itself failed. Nothing past that record is
// committed, so restarting redelivers it. Every other failure is accounted for
// in the record's own outcome.
func (c *Consumer) Run(ctx context.Context) error {
	// CloseAllowingRebalance commits nothing by itself — that has already
	// happened per batch — but it does leave the group cleanly, so the remaining
	// members rebalance immediately instead of after the session timeout.
	defer c.closeClient()

	for {
		fetches := c.client.PollRecords(ctx, c.cfg.MaxPollRecords)
		// BlockRebalanceOnPoll blocks rebalances from the moment a poll returns
		// records until AllowRebalance, so every iteration must reach it —
		// including the ones that return early. Close would otherwise wait for a
		// rebalance this member is still holding.
		if fetches.IsClientClosed() {
			return nil
		}
		if shuttingDown(ctx, fetches) {
			c.client.AllowRebalance()
			return nil
		}
		c.logFetchErrors(ctx, fetches)

		// One budget for the whole batch, detached from ctx so a shutdown
		// drains rather than poisons. See the Shutdown section above.
		workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.DrainTimeout)
		err := c.handleBatch(workCtx, fetches)
		cancel()

		c.client.AllowRebalance()
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
	}
}

// handleBatch handles every partition in the batch and commits what was settled.
func (c *Consumer) handleBatch(ctx context.Context, fetches kgo.Fetches) error {
	type outcome struct {
		// settled is the last record whose fate was decided, so its offset (+1)
		// is safe to commit. Keeping the last one rather than all of them keeps
		// this O(partitions) instead of O(records).
		settled *kgo.Record
		err     error
	}

	var (
		mu       sync.Mutex
		outcomes []outcome
		wg       sync.WaitGroup
	)

	fetches.EachPartition(func(p kgo.FetchTopicPartition) {
		wg.Add(1)
		go func() {
			defer wg.Done()

			var got outcome
			// Sequential within the partition, in offset order: this is the
			// per-key ordering guarantee (A§26). The loop stops at the first
			// unsettled record so the partition's offsets stay contiguous.
			for _, record := range p.Records {
				if err := c.handleRecord(ctx, record); err != nil {
					got.err = err
					break
				}
				got.settled = record
			}

			mu.Lock()
			outcomes = append(outcomes, got)
			mu.Unlock()
		}()
	})
	wg.Wait()

	commit := make([]*kgo.Record, 0, len(outcomes))
	var errs []error
	for _, got := range outcomes {
		if got.settled != nil {
			commit = append(commit, got.settled)
		}
		if got.err != nil {
			errs = append(errs, got.err)
		}
	}

	if len(commit) > 0 {
		// The commit gets its own budget, detached from the batch's. A batch that
		// used its whole DrainTimeout would otherwise commit on an expired
		// context and redo work it had already finished — which is exactly the
		// duplicate the graceful-shutdown guarantee exists to avoid.
		commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), commitTimeout)
		err := c.client.CommitRecords(commitCtx, commit...)
		cancel()
		if err != nil {
			// A failed commit is not a lost message: the records were handled,
			// and the next poll after the rebalance redelivers them. Say so
			// loudly, then carry on rather than tearing the consumer down.
			otelkit.Logger(ctx).ErrorContext(ctx, "committing consumed offsets failed; records will be redelivered",
				slog.String("group", c.cfg.Group), slog.Any("error", err))
		}
	}
	return errors.Join(errs...)
}

// handleRecord takes one record from arrival to a settled outcome.
//
// It returns an error only when the record could not be settled at all, which
// means the DLQ produce failed. Every other outcome — handled, skipped,
// dead-lettered — is a nil return, because the record's fate is decided and its
// offset may be committed.
func (c *Consumer) handleRecord(ctx context.Context, record *kgo.Record) error {
	headers := headerMap(record.Headers)

	// Continue the producer's trace and restore the correlation id, before any
	// log line about this record exists (A§97).
	ctx = otelkit.ContextFromKafkaHeaders(ctx, headers)
	ctx, span := c.tracer.Start(ctx, "kafka.consume "+record.Topic,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.source.name", record.Topic),
			attribute.Int64("messaging.kafka.message.offset", record.Offset),
			attribute.Int("messaging.kafka.destination.partition", int(record.Partition)),
			attribute.String("messaging.kafka.message.key", string(record.Key)),
		))
	defer span.End()

	c.metrics.recordConsumed(ctx, c.cfg.Group, record.Topic)

	// Decode is ValidateJSON followed by an unmarshal. Validating the *raw*
	// bytes is the only way to prove the envelope carries exactly the ten A§24
	// fields — decoding into the struct would silently drop an eleventh — and
	// calling ValidateJSON separately first would run the JSON Schema twice on
	// every message for no extra guarantee (ADR-0004 flags that cost).
	env, err := c.cfg.Registry.Decode(record.Value)
	if err != nil {
		if errors.Is(err, events.ErrUnknownEvent) {
			// Not an error: contracts/README §13 requires an unknown event type
			// or version to be ignored, so a new event can be rolled out before
			// every consumer knows about it. Dead-lettering here would turn
			// every new event type into an incident for every old consumer.
			c.metrics.recordSkipped(ctx, c.cfg.Group, record.Topic)
			span.SetAttributes(attribute.String("colx.outcome", "skipped"))
			otelkit.Logger(ctx).InfoContext(ctx, "skipping an event type this consumer does not know",
				slog.String("topic", record.Topic),
				slog.Int64("offset", record.Offset),
				slog.String("reason", err.Error()))
			return nil
		}
		// Malformed: no number of retries makes an invalid document valid, so it
		// goes straight to the DLQ.
		span.SetAttributes(attribute.String("colx.outcome", "dead-lettered"))
		span.SetStatus(codes.Error, "schema violation")
		return c.deadLetter(ctx, record, reasonSchema, err)
	}

	// The event being handled is the cause of anything the handler emits (A§24).
	ctx = otelkit.ContextWithCausationID(ctx, env.EventID)
	// A producer that set no correlation header still carries the id in the
	// envelope; use it rather than letting the chain restart here.
	if httpkit.CorrelationIDFrom(ctx) == "" && env.CorrelationID != "" {
		ctx = httpkit.ContextWithCorrelationID(ctx, env.CorrelationID)
	}
	span.SetAttributes(
		attribute.String("colx.event_type", env.EventType),
		attribute.Int("colx.event_version", env.EventVersion),
	)

	if err := c.invokeHandler(ctx, record, env); err != nil {
		span.SetAttributes(attribute.String("colx.outcome", "dead-lettered"))
		span.SetStatus(codes.Error, "handler failed")
		return c.deadLetter(ctx, record, reasonHandler, err)
	}
	span.SetAttributes(attribute.String("colx.outcome", "handled"))
	return nil
}

// invokeHandler calls the handler, retrying with exponential backoff up to
// MaxRetries. It returns the last error if every attempt failed.
func (c *Consumer) invokeHandler(ctx context.Context, record *kgo.Record, env events.Envelope) error {
	backoff := c.cfg.Backoff
	var lastErr error

	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			c.metrics.recordRetry(ctx, c.cfg.Group, record.Topic)
			if err := sleep(ctx, backoff); err != nil {
				// The batch budget ran out mid-backoff. Report the handler's
				// error, not the context's: the handler is why we are here, and
				// the record is about to be dead-lettered with a cause an
				// operator can act on.
				return lastErr
			}
			if backoff *= 2; backoff > c.cfg.MaxBackoff {
				backoff = c.cfg.MaxBackoff
			}
		}

		started := time.Now()
		lastErr = c.cfg.Handler(ctx, env)
		c.metrics.recordHandleDuration(ctx, c.cfg.Group, record.Topic, time.Since(started))
		if lastErr == nil {
			return nil
		}
		otelkit.Logger(ctx).WarnContext(ctx, "event handler failed",
			slog.String("event_type", env.EventType),
			slog.String("event_id", env.EventID),
			slog.Int("attempt", attempt+1),
			slog.Int("attempts_allowed", c.cfg.MaxRetries+1),
			slog.Any("error", lastErr))
	}
	return lastErr
}

// deadLetter produces the record, byte for byte, to the DLQ topic with the A§27
// origin headers, and reports whether that succeeded.
//
// The value is the original bytes and the key is the original key, so a replay
// re-produces exactly what was consumed (D§49). A failure to produce is returned
// rather than logged: committing past a record nobody has is the one way this
// package could lose an event.
func (c *Consumer) deadLetter(ctx context.Context, record *kgo.Record, reason string, cause error) error {
	headers := preserveHeaders(record.Headers)
	headers = append(headers,
		kgo.RecordHeader{Key: HeaderOriginTopic, Value: []byte(record.Topic)},
		kgo.RecordHeader{Key: HeaderOriginPartition, Value: []byte(strconv.FormatInt(int64(record.Partition), 10))},
		kgo.RecordHeader{Key: HeaderOriginOffset, Value: []byte(strconv.FormatInt(record.Offset, 10))},
		kgo.RecordHeader{Key: HeaderError, Value: []byte(cause.Error())},
	)

	dlqRecord := &kgo.Record{
		Topic:   c.cfg.DLQTopic,
		Key:     record.Key,
		Value:   record.Value,
		Headers: headers,
	}

	if err := c.client.ProduceSync(ctx, dlqRecord).FirstErr(); err != nil {
		c.metrics.recordUnsettled(ctx, c.cfg.Group, record.Topic)
		c.metrics.recordPublishFailed(ctx, c.cfg.DLQTopic)
		return fmt.Errorf(
			"dead-lettering %s partition %d offset %d to %s (cause: %v): %w",
			record.Topic, record.Partition, record.Offset, c.cfg.DLQTopic, cause, err)
	}

	c.metrics.recordDeadLettered(ctx, c.cfg.Group, record.Topic, reason)
	c.metrics.recordPublished(ctx, c.cfg.DLQTopic)
	otelkit.Logger(ctx).ErrorContext(ctx, "record dead-lettered",
		slog.String("origin_topic", record.Topic),
		slog.Int("origin_partition", int(record.Partition)),
		slog.Int64("origin_offset", record.Offset),
		slog.String("dlq_topic", c.cfg.DLQTopic),
		slog.String("reason", reason),
		slog.Any("error", cause))
	return nil
}

// logFetchErrors reports broker-side fetch problems. franz-go retries them
// itself, so these are informational — but a partition that keeps erroring is
// invisible without them.
func (c *Consumer) logFetchErrors(ctx context.Context, fetches kgo.Fetches) {
	fetches.EachError(func(topic string, partition int32, err error) {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		otelkit.Logger(ctx).WarnContext(ctx, "fetching from Kafka failed; the client will retry",
			slog.String("topic", topic),
			slog.Int("partition", int(partition)),
			slog.Any("error", err))
	})
}

// Close leaves the group and shuts the client down. Run does this on return, so
// a caller only needs it when it built a Consumer it never ran.
func (c *Consumer) Close() { c.closeClient() }

func (c *Consumer) closeClient() {
	c.closeOnce.Do(c.client.CloseAllowingRebalance)
}

// ReadyCheck reports the consumer as a readiness dependency: the brokers must be
// reachable and this member must have joined the group.
//
// Membership matters as much as reachability. A member that is connected but has
// not joined — a rebalance that keeps failing, an authorisation the broker
// refuses — is not consuming anything, and reporting it ready hides a consumer
// that will never process a message.
func (c *Consumer) ReadyCheck() health.Check {
	return health.Check{
		Name: "kafka-consumer",
		Probe: func(ctx context.Context) error {
			if err := c.client.Ping(ctx); err != nil {
				return fmt.Errorf("pinging the Kafka brokers: %w", err)
			}
			// franz-go reports generation -1 until the first successful join.
			if _, generation := c.client.GroupMetadata(); generation < 0 {
				return fmt.Errorf("consumer group %s has not been joined yet", c.cfg.Group)
			}
			return nil
		},
	}
}

// shuttingDown reports whether the poll ended because ctx was cancelled with
// nothing to show for it. franz-go surfaces a cancelled poll as a fetch error
// rather than an empty result, and a poll can return both records *and* the
// cancellation — those records still have to be handled and committed, which is
// what the drain is.
func shuttingDown(ctx context.Context, fetches kgo.Fetches) bool {
	return ctx.Err() != nil && fetches.NumRecords() == 0
}

// sleep waits for d, or returns ctx's error if that happens first.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
