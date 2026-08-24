//go:build integration

// Integration tests for platform/kafka against a real broker (Redpanda via
// testcontainers). Gated behind the `integration` build tag — the same
// convention makefiles/service.mk uses — so a plain `go test ./...`, and
// therefore `make test-all`, needs no Docker:
//
//	go -C platform test ./kafka/... -tags integration -count=1 -timeout 20m
//
// One container serves every test in this file; isolation comes from a topic
// triple and a consumer group named after the test. Starting Redpanda takes
// several seconds and none of these tests care about broker state.
package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/redpanda"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/trace"

	"github.com/canhtoanptit/collection-platform/contracts"
	"github.com/canhtoanptit/collection-platform/platform/events"
	"github.com/canhtoanptit/collection-platform/platform/httpkit"
	"github.com/canhtoanptit/collection-platform/platform/ids"
	"github.com/canhtoanptit/collection-platform/platform/otelkit"
)

const (
	// redpandaImage is pinned. Redpanda is Kafka-API compatible and is what
	// ADR-0004 names for tests; the cluster runs MSK, and the one-dev-cluster
	// smoke test per service wave is what covers that drift (plan risk 8).
	redpandaImage = "docker.redpanda.com/redpandadata/redpanda:v25.2.4"

	// startTimeout covers an image pull on a cold cache.
	startTimeout = 5 * time.Minute
	// settleTimeout bounds "the consumer should have processed this by now".
	settleTimeout = 90 * time.Second
	// shutdownTimeout bounds a graceful Run return.
	shutdownTimeout = 60 * time.Second

	// testEventType is a real contract event, so the registry validates the
	// payload for real rather than against a fixture schema written to pass.
	testEventType    = "DelinquencyChanged"
	testEventVersion = 1
	testProducer     = "delinquency-service"
	testAggregate    = "Account"
)

var (
	// testBroker is the seed broker of the shared container, set by TestMain.
	testBroker string
	// testCorrelationID is the correlation id every published fixture carries.
	testCorrelationID = ids.NewULID()
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	container, err := redpanda.Run(ctx, redpandaImage,
		// Redpanda refuses produce requests once the data volume has less than
		// storage_min_free_bytes free — 5 GiB by default. A developer laptop or a
		// CI runner whose Docker VM is fuller than that would fail these tests
		// with "records have timed out", nowhere near the real cause. These tests
		// write a few hundred small records, so 32 MiB is a generous floor.
		redpanda.WithBootstrapConfig("storage_min_free_bytes", 32*1024*1024),
	)
	if err != nil {
		cancel()
		log.Fatalf("starting %s (is Docker running?): %v", redpandaImage, err)
	}
	if testBroker, err = container.KafkaSeedBroker(ctx); err != nil {
		cancel()
		log.Fatalf("reading the Redpanda seed broker: %v", err)
	}
	cancel()

	// The real service wiring: propagators plus a sampling tracer provider, so
	// the trace and correlation assertions below exercise what runs in a pod
	// rather than a test-only propagator.
	shutdownTelemetry, err := otelkit.Init(context.Background(),
		otelkit.ServiceInfo{Name: "platform-kafka-test", Env: "test"})
	if err != nil {
		log.Fatalf("initialising telemetry: %v", err)
	}

	code := m.Run()

	if err := shutdownTelemetry(context.Background()); err != nil {
		log.Printf("shutting telemetry down: %v", err)
	}
	if err := testcontainers.TerminateContainer(container); err != nil {
		log.Printf("terminating the Redpanda container: %v", err)
	}
	os.Exit(code)
}

// TestIntegrationOrderedDeliveryPerKey is the A§26 guarantee: everything about
// one aggregate arrives in the order it was published, whatever else is going on.
//
// One "focus" key gets 120 records published in sequence while nine other keys
// are published to concurrently, so the topic is busy and the partitions are
// shared. The handler must see the focus key's records in strictly ascending
// order, and every other key's too.
func TestIntegrationOrderedDeliveryPerKey(t *testing.T) {
	const (
		focusRecords = 120
		otherKeys    = 9
		otherRecords = 4
	)
	topics := newTopics(t, 3)

	focusKey := ids.NewULID()
	others := make([]string, otherKeys)
	for i := range others {
		others[i] = ids.NewULID()
	}

	var (
		mu    sync.Mutex
		seen  = map[string][]int{}
		total = make(chan struct{}, focusRecords+otherKeys*otherRecords)
	)
	consumer := startConsumer(t, topics.consumerConfig(func(_ context.Context, env events.Envelope) error {
		sequence, err := sequenceOf(env)
		if err != nil {
			return err
		}
		mu.Lock()
		seen[env.AggregateID] = append(seen[env.AggregateID], sequence)
		mu.Unlock()
		total <- struct{}{}
		return nil
	}))

	publisher := newPublisher(t)
	var wg sync.WaitGroup
	wg.Add(1 + otherKeys)
	go func() {
		defer wg.Done()
		publishSequence(t, publisher, topics.topic, focusKey, focusRecords)
	}()
	for _, key := range others {
		go func() {
			defer wg.Done()
			publishSequence(t, publisher, topics.topic, key, otherRecords)
		}()
	}
	wg.Wait()

	awaitCount(t, total, focusRecords+otherKeys*otherRecords, "records handled")
	consumer.stop(t)

	mu.Lock()
	defer mu.Unlock()

	focus := seen[focusKey]
	if len(focus) != focusRecords {
		t.Fatalf("the focus key was handled %d times, want %d", len(focus), focusRecords)
	}
	for key, sequences := range seen {
		for i := 1; i < len(sequences); i++ {
			if sequences[i] <= sequences[i-1] {
				t.Fatalf("key %s handled out of order at index %d: %v", key, i, sequences)
			}
		}
	}
	if len(seen) != otherKeys+1 {
		t.Fatalf("handled %d distinct keys, want %d", len(seen), otherKeys+1)
	}
}

// TestIntegrationPartitionsProceedConcurrently is the other half of the ordering
// contract, and the half a naive "handle everything one at a time" implementation
// would silently pass the previous test with: sequential *within* a partition,
// concurrent *across* partitions.
//
// The handler holds for a beat and records the peak number of concurrent
// invocations. Anything less than two means one slow partition would stall every
// other one.
func TestIntegrationPartitionsProceedConcurrently(t *testing.T) {
	const (
		keys           = 20
		perKey         = 2
		handlerHold    = 150 * time.Millisecond
		wantConcurrent = 2
	)
	topics := newTopics(t, 4)

	// Publish everything before the consumer starts, so the first poll holds
	// records from several partitions at once.
	publisher := newPublisher(t)
	for range keys {
		publishSequence(t, publisher, topics.topic, ids.NewULID(), perKey)
	}

	var (
		mu       sync.Mutex
		current  int
		peak     int
		sequence = map[string][]int{}
		handled  = make(chan struct{}, keys*perKey)
	)
	consumer := startConsumer(t, topics.consumerConfig(func(_ context.Context, env events.Envelope) error {
		got, err := sequenceOf(env)
		if err != nil {
			return err
		}
		mu.Lock()
		current++
		peak = max(peak, current)
		sequence[env.AggregateID] = append(sequence[env.AggregateID], got)
		mu.Unlock()

		time.Sleep(handlerHold)

		mu.Lock()
		current--
		mu.Unlock()
		handled <- struct{}{}
		return nil
	}))

	awaitCount(t, handled, keys*perKey, "records handled")
	consumer.stop(t)

	mu.Lock()
	defer mu.Unlock()
	if peak < wantConcurrent {
		t.Errorf("peak concurrent handler invocations = %d, want at least %d — partitions are being handled one after another",
			peak, wantConcurrent)
	}
	for key, got := range sequence {
		for i := 1; i < len(got); i++ {
			if got[i] <= got[i-1] {
				t.Errorf("key %s handled out of order: %v", key, got)
			}
		}
	}
}

// TestIntegrationPoisonMessageIsDeadLettered is the A§27 acceptance: a handler
// that keeps failing costs exactly MaxRetries+1 attempts, the record lands on the
// DLQ byte-identical with the origin headers, and the partition carries on.
func TestIntegrationPoisonMessageIsDeadLettered(t *testing.T) {
	// One partition, so the poison record and its successor share a partition
	// and their offsets are exactly 0 and 1.
	topics := newTopics(t, 1)

	key := ids.NewULID()
	poison := newEnvelope(t, key, 1)
	good := newEnvelope(t, key, 2)

	var (
		attempts atomic.Int64
		handled  = make(chan int, 4)
	)
	consumer := startConsumer(t, topics.consumerConfig(func(_ context.Context, env events.Envelope) error {
		sequence, err := sequenceOf(env)
		if err != nil {
			return err
		}
		if sequence == 1 {
			attempts.Add(1)
			return fmt.Errorf("this handler always fails for sequence %d", sequence)
		}
		handled <- sequence
		return nil
	}))

	publisher := newPublisher(t)
	publish(t, publisher, topics.topic, key, poison.raw)
	publish(t, publisher, topics.topic, key, good.raw)

	// The successor being handled is the proof that the partition was not
	// blocked: it can only happen after the poison record was settled.
	select {
	case sequence := <-handled:
		if sequence != 2 {
			t.Fatalf("handled sequence %d, want the record after the poison one", sequence)
		}
	case <-time.After(settleTimeout):
		t.Fatal("the record after the poison one was never handled — the partition is blocked")
	}

	dead := readDLQ(t, topics.dlqTopic, 1)
	consumer.stop(t)

	if got := attempts.Load(); got != int64(defaultMaxRetries+1) {
		t.Errorf("handler attempts = %d, want %d (one try plus %d retries)",
			got, defaultMaxRetries+1, defaultMaxRetries)
	}

	record := dead[0]
	if string(record.Value) != string(poison.raw) {
		t.Errorf("the dead-lettered value is not byte-identical to what was published:\n got %s\nwant %s",
			record.Value, poison.raw)
	}
	if string(record.Key) != key {
		t.Errorf("the dead-lettered key = %q, want %q — a replay would repartition the record", record.Key, key)
	}

	headers := headerMap(record.Headers)
	assertHeaders(t, headers, map[string]string{
		HeaderOriginTopic:     topics.topic,
		HeaderOriginPartition: "0",
		HeaderOriginOffset:    "0",
	})
	if headers[HeaderError] == "" {
		t.Error("the DLQ record carries no x-error — nobody can tell why it is there")
	}
	// The propagation headers the publisher injected must survive, or the DLQ
	// record cannot be joined to the flow that produced it (A§97).
	for _, name := range []string{otelkit.HeaderTraceparent, otelkit.HeaderCorrelationID} {
		if headers[name] == "" {
			t.Errorf("the DLQ record lost the %s header", name)
		}
	}
}

// TestIntegrationMalformedEnvelopeIsDeadLetteredWithoutRetries: no number of
// retries makes an invalid document valid, so retrying one is pure latency on a
// partition that has real work waiting.
func TestIntegrationMalformedEnvelopeIsDeadLetteredWithoutRetries(t *testing.T) {
	topics := newTopics(t, 1)

	key := ids.NewULID()
	good := newEnvelope(t, key, 7)

	tests := []struct {
		name  string
		value []byte
	}{
		{"not JSON at all", []byte("{this is not json")},
		{"a JSON array instead of an envelope", []byte(`[{"eventType":"DelinquencyChanged"}]`)},
		{"an envelope missing every required field", []byte(`{"eventType":"DelinquencyChanged","eventVersion":1}`)},
		{
			name: "an eleventh top-level field the frozen envelope schema forbids",
			value: mutateEnvelope(t, newEnvelope(t, key, 99).raw, func(doc map[string]any) {
				doc["tenantId"] = "T1"
			}),
		},
	}

	var (
		attempts atomic.Int64
		handled  = make(chan struct{}, len(tests))
	)
	consumer := startConsumer(t, topics.consumerConfig(func(_ context.Context, env events.Envelope) error {
		attempts.Add(1)
		if seq, err := sequenceOf(env); err == nil && seq == 7 {
			handled <- struct{}{}
		}
		return nil
	}))

	publisher := newPublisher(t)
	for _, tc := range tests {
		publish(t, publisher, topics.topic, key, tc.value)
	}
	// A valid record last: it proves the consumer kept going.
	publish(t, publisher, topics.topic, key, good.raw)

	select {
	case <-handled:
	case <-time.After(settleTimeout):
		t.Fatal("the valid record after the malformed ones was never handled")
	}

	dead := readDLQ(t, topics.dlqTopic, len(tests))
	consumer.stop(t)

	if got := attempts.Load(); got != 1 {
		t.Errorf("the handler was invoked %d times, want 1 (only the valid record) — a malformed envelope must never reach it",
			got)
	}
	if len(dead) != len(tests) {
		t.Fatalf("the DLQ holds %d records, want %d", len(dead), len(tests))
	}
	for i, record := range dead {
		if string(record.Value) != string(tests[i].value) {
			t.Errorf("%s: the DLQ value was rewritten:\n got %s\nwant %s",
				tests[i].name, record.Value, tests[i].value)
		}
		if headerMap(record.Headers)[HeaderError] == "" {
			t.Errorf("%s: no x-error header", tests[i].name)
		}
	}
}

// TestIntegrationUnknownEventTypeIsSkipped is contracts/README §13: a consumer
// must ignore an event type or version it does not know, so a new event can be
// rolled out before every consumer understands it. Dead-lettering it would make
// every new event type an incident for every already-deployed consumer.
func TestIntegrationUnknownEventTypeIsSkipped(t *testing.T) {
	topics := newTopics(t, 1)

	key := ids.NewULID()
	// Structurally valid envelopes with no schema in the registry: an event type
	// nobody has heard of, and a future major version of one that exists.
	unknownType := envelopeBytes(t, mustEnvelope(t, "AccountTeleported", 1, key, 1))
	unknownVersion := envelopeBytes(t, mustEnvelope(t, testEventType, 99, key, 2))
	good := newEnvelope(t, key, 3)

	handled := make(chan int, 4)
	consumer := startConsumer(t, topics.consumerConfig(func(_ context.Context, env events.Envelope) error {
		sequence, err := sequenceOf(env)
		if err != nil {
			return err
		}
		handled <- sequence
		return nil
	}))

	publisher := newPublisher(t)
	publish(t, publisher, topics.topic, key, unknownType)
	publish(t, publisher, topics.topic, key, unknownVersion)
	publish(t, publisher, topics.topic, key, good.raw)

	select {
	case sequence := <-handled:
		if sequence != 3 {
			t.Fatalf("the handler saw sequence %d; the unknown events should never reach it", sequence)
		}
	case <-time.After(settleTimeout):
		t.Fatal("the known event was never handled")
	}
	consumer.stop(t)

	if extra := len(handled); extra != 0 {
		t.Errorf("the handler was invoked %d extra times for unknown event types", extra)
	}
	// The DLQ must be empty. Waiting for the known record to be handled first
	// means anything the consumer would have dead-lettered is already there.
	if dead := drainDLQ(t, topics.dlqTopic); len(dead) != 0 {
		t.Fatalf("%d unknown-event records were dead-lettered; they must be skipped", len(dead))
	}
}

// TestIntegrationPropagationReachesTheHandler is the A§97 chain across the broker:
// the handler's context must carry the publisher's trace, the flow's correlation
// id and a causation id set to this event's own eventId — so anything the handler
// emits records what caused it without the handler doing anything.
func TestIntegrationPropagationReachesTheHandler(t *testing.T) {
	topics := newTopics(t, 1)

	key := ids.NewULID()
	envelope := newEnvelope(t, key, 5)
	correlationID := ids.NewULID()

	type observed struct {
		correlation string
		causation   string
		traceID     string
		eventID     string
	}
	got := make(chan observed, 1)
	consumer := startConsumer(t, topics.consumerConfig(func(ctx context.Context, env events.Envelope) error {
		got <- observed{
			correlation: httpkit.CorrelationIDFrom(ctx),
			causation:   otelkit.CausationIDFrom(ctx),
			traceID:     trace.SpanContextFromContext(ctx).TraceID().String(),
			eventID:     env.EventID,
		}
		return nil
	}))

	// Publish from inside a span, with a correlation id in the context, exactly
	// as the outbox relay does while handling a request.
	publishCtx, span := newTracer().Start(
		httpkit.ContextWithCorrelationID(context.Background(), correlationID),
		"test.publish")
	wantTraceID := span.SpanContext().TraceID().String()

	publisher := newPublisher(t)
	if err := publisher.Publish(publishCtx, topics.topic, key, envelope.raw, nil); err != nil {
		span.End()
		t.Fatalf("Publish: %v", err)
	}
	span.End()

	var seen observed
	select {
	case seen = <-got:
	case <-time.After(settleTimeout):
		t.Fatal("the record was never handled")
	}
	consumer.stop(t)

	tests := []struct {
		field string
		got   string
		want  string
	}{
		{"correlation id", seen.correlation, correlationID},
		{"causation id", seen.causation, seen.eventID},
		{"trace id", seen.traceID, wantTraceID},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s in the handler context = %q, want %q", tc.field, tc.got, tc.want)
		}
	}
	if seen.eventID != envelope.env.EventID {
		t.Errorf("the handler saw eventId %q, want %q", seen.eventID, envelope.env.EventID)
	}
}

// TestIntegrationGracefulShutdownCommitsProcessedOffsets is the shutdown
// contract: cancelling the context drains and commits the batch in flight, so a
// rolling deploy does not reprocess what the old pod already did.
//
// A small MaxPollRecords makes the batch boundary observable — the consumer is
// cancelled part way through the stream, and the second consumer in the same
// group must pick up from where the first committed.
func TestIntegrationGracefulShutdownCommitsProcessedOffsets(t *testing.T) {
	const (
		published = 40
		batchSize = 5
		// Two whole batches, with plenty of stream left over so the cancel
		// cannot race the consumer to the end of the topic.
		stopAfter = 10
	)
	topics := newTopics(t, 1)

	key := ids.NewULID()
	publisher := newPublisher(t)
	publishSequence(t, publisher, topics.topic, key, published)

	// collector records what a consumer handled, and signals once it has seen
	// enough. Both consumers use one, so "who handled what" is one data
	// structure rather than two conventions.
	firstPass := newCollector(stopAfter)
	cfg := topics.consumerConfig(firstPass.handle)
	cfg.MaxPollRecords = batchSize

	consumer := startConsumer(t, cfg)
	firstPass.await(t, "the first consumer never reached the shutdown point")
	consumer.stop(t)

	handledByFirst := firstPass.seen()
	if len(handledByFirst) < stopAfter {
		t.Fatalf("the first consumer handled %d records, want at least %d", len(handledByFirst), stopAfter)
	}
	if len(handledByFirst) >= published {
		t.Fatalf("the first consumer drained the whole topic (%d records); the restart would prove nothing",
			len(handledByFirst))
	}

	// Same group, so the second consumer resumes from the committed offset.
	secondPass := newCollector(published - len(handledByFirst))
	second := topics.consumerConfig(secondPass.handle)
	second.MaxPollRecords = batchSize

	restarted := startConsumer(t, second)
	secondPass.await(t, "the restarted consumer did not receive the rest of the stream")
	restarted.stop(t)

	// A record the first consumer handled turning up again means its offset was
	// not committed before shutdown.
	handledAgain := secondPass.seen()
	byFirst := map[int]bool{}
	for _, sequence := range handledByFirst {
		byFirst[sequence] = true
	}
	for _, sequence := range handledAgain {
		if byFirst[sequence] {
			t.Errorf("sequence %d was reprocessed after the restart — the shutdown did not commit it", sequence)
		}
	}
	if total := len(handledByFirst) + len(handledAgain); total != published {
		t.Errorf("%d records handled in total, want %d — the shutdown lost or duplicated work",
			total, published)
	}
}

// collector accumulates the sequence numbers a handler saw and closes a channel
// once it has seen `want` of them.
type collector struct {
	want int

	mu    sync.Mutex
	order []int

	once sync.Once
	done chan struct{}
}

func newCollector(want int) *collector {
	return &collector{want: want, done: make(chan struct{})}
}

func (c *collector) handle(_ context.Context, env events.Envelope) error {
	sequence, err := sequenceOf(env)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.order = append(c.order, sequence)
	reached := len(c.order)
	c.mu.Unlock()

	if reached >= c.want {
		c.once.Do(func() { close(c.done) })
	}
	return nil
}

func (c *collector) await(t *testing.T, message string) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(settleTimeout):
		c.mu.Lock()
		got := len(c.order)
		c.mu.Unlock()
		t.Fatalf("%s (%d of %d after %v)", message, got, c.want, settleTimeout)
	}
}

func (c *collector) seen() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.order...)
}

// TestIntegrationUnsettledRecordIsNotCommitted is the "never drop" guarantee.
// If the DLQ produce itself fails, the record has reached none of the three
// settled states, so committing past it would lose an event outright. Run must
// return the error instead and let the restart replay it.
//
// The failure is produced by pointing DLQTopic at a topic that does not exist,
// which is what a misconfigured deployment looks like: auto-create is off
// (ADR-0004), so the produce fails rather than inventing a topic.
func TestIntegrationUnsettledRecordIsNotCommitted(t *testing.T) {
	topics := newTopics(t, 1)

	key := ids.NewULID()
	poison := newEnvelope(t, key, 1)

	cfg := topics.consumerConfig(func(_ context.Context, env events.Envelope) error {
		return fmt.Errorf("this handler always fails")
	})
	cfg.DLQTopic = "collections.dlq.does-not-exist." + strings.ToLower(t.Name())
	cfg.MaxRetries = 0
	// Short, so the test costs seconds rather than the 30s production default.
	cfg.DeliveryTimeout = 3 * time.Second

	consumer, err := NewConsumer(cfg)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	t.Cleanup(consumer.Close)

	publisher := newPublisher(t)
	publish(t, publisher, topics.topic, key, poison.raw)

	runErr := make(chan error, 1)
	go func() { runErr <- consumer.Run(context.Background()) }()

	select {
	case err := <-runErr:
		if err == nil {
			t.Fatal("Run returned nil after failing to dead-letter a record — the offset would be committed and the event lost")
		}
		for _, want := range []string{"dead-lettering", topics.topic} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the error does not mention %q: %v", want, err)
			}
		}
	case <-time.After(settleTimeout):
		t.Fatal("Run did not return after the DLQ produce failed")
	}

	// Nothing was committed, so a fresh member of the same group sees the record
	// again. That is the replay the guarantee promises.
	redelivered := newCollector(1)
	replay := topics.consumerConfig(redelivered.handle)
	restarted := startConsumer(t, replay)
	redelivered.await(t, "the unsettled record was not redelivered — its offset was committed")
	restarted.stop(t)
}

// TestIntegrationPublishReportsBrokerRejection: a publish that the brokers never
// acknowledge must be an error, not a silent success or an unbounded wait. The
// outbox relay marks a row published only after this returns nil, so a
// fire-and-forget failure here would read as data loss.
func TestIntegrationPublishReportsBrokerRejection(t *testing.T) {
	publisher, err := NewPublisher(Config{
		Brokers:         []string{testBroker},
		ClientID:        "platform-kafka-test",
		Linger:          time.Nanosecond,
		DeliveryTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	t.Cleanup(publisher.Close)

	// A topic nobody declared. Auto-create is off, so this can never succeed.
	err = publisher.Publish(context.Background(),
		"collections.undeclared."+strings.ToLower(t.Name()), ids.NewULID(),
		[]byte(`{"eventId":"01M0KK4P3G0MQSQ3A1X2PMA6VX"}`), nil)
	if err == nil {
		t.Fatal("Publish returned nil for a topic that does not exist")
	}
	if !strings.Contains(err.Error(), "publishing to") {
		t.Errorf("the error does not name the failing publish: %v", err)
	}
}

// TestIntegrationReadyCheckPassesOnceTheGroupIsJoined is the positive half of the
// readiness probe. The negative half (no broker, no group) is a unit test; this
// is the half that would keep a healthy pod out of the Service if it were wrong.
func TestIntegrationReadyCheckPassesOnceTheGroupIsJoined(t *testing.T) {
	topics := newTopics(t, 1)

	key := ids.NewULID()
	publisher := newPublisher(t)
	publish(t, publisher, topics.topic, key, newEnvelope(t, key, 1).raw)

	handled := newCollector(1)
	consumer, err := NewConsumer(topics.consumerConfig(handled.handle))
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	t.Cleanup(consumer.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()

	// Handling a record proves the group is joined and assigned.
	handled.await(t, "the consumer never handled the record")

	if err := consumer.ReadyCheck().Probe(ctx); err != nil {
		t.Errorf("a joined, consuming member reports not ready: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned an error: %v", err)
		}
	case <-time.After(shutdownTimeout):
		t.Error("Run did not return after cancellation")
	}
}

// --- fixtures and helpers ---------------------------------------------------

// topicSet is one test's isolated topic pair and consumer group.
type topicSet struct {
	t        *testing.T
	topic    string
	dlqTopic string
	group    string
	registry *events.Registry
}

// newTopics creates the test's topic and its DLQ with an explicit partition
// count. Topics are created rather than auto-created: production has auto-create
// off (ADR-0004), and a one-partition topic conjured by a typo would make an
// ordering test pass for the wrong reason.
func newTopics(t *testing.T, partitions int32) topicSet {
	t.Helper()

	name := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "."))
	set := topicSet{
		t:        t,
		topic:    "collections." + name,
		dlqTopic: "collections.dlq." + name,
		group:    name,
		registry: testRegistry(t),
	}

	client, err := kgo.NewClient(kgo.SeedBrokers(testBroker))
	if err != nil {
		t.Fatalf("connecting to create topics: %v", err)
	}
	defer client.Close()

	admin := kadm.NewClient(client)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	created, err := admin.CreateTopics(ctx, partitions, 1, nil, set.topic, set.dlqTopic)
	if err != nil {
		t.Fatalf("creating topics: %v", err)
	}
	for _, response := range created {
		if response.Err != nil {
			t.Fatalf("creating topic %s: %v", response.Topic, response.Err)
		}
	}

	// CreateTopics returns as soon as the controller has accepted the creation;
	// a leader for every partition arrives shortly afterwards. Producing before
	// then gets UNKNOWN_TOPIC_OR_PARTITION or NOT_LEADER, which franz-go retries
	// — so the symptom is a slow test rather than a failing one, and the cause is
	// invisible. Wait for the metadata instead.
	awaitTopics(ctx, t, admin, partitions, set.topic, set.dlqTopic)
	return set
}

// awaitTopics blocks until every named topic reports the expected number of
// partitions, each with a leader.
func awaitTopics(ctx context.Context, t *testing.T, admin *kadm.Client, partitions int32, topics ...string) {
	t.Helper()

	for {
		details, err := admin.ListTopics(ctx, topics...)
		if err == nil && topicsReady(details, partitions, topics) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("topics %v were not ready within the deadline (last error: %v)", topics, err)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func topicsReady(details kadm.TopicDetails, partitions int32, topics []string) bool {
	for _, topic := range topics {
		detail, ok := details[topic]
		if !ok || detail.Err != nil || int32(len(detail.Partitions)) != partitions {
			return false
		}
		for _, partition := range detail.Partitions {
			if partition.Err != nil || partition.Leader < 0 {
				return false
			}
		}
	}
	return true
}

// consumerConfig is the configuration under test: default retries and backoff
// except for the backoff itself, which is shortened so a retry table does not
// cost seconds of wall time.
func (s topicSet) consumerConfig(handler Handler) ConsumerConfig {
	return ConsumerConfig{
		Brokers:    []string{testBroker},
		Group:      s.group,
		Topics:     []string{s.topic},
		Registry:   s.registry,
		Handler:    handler,
		DLQTopic:   s.dlqTopic,
		Backoff:    10 * time.Millisecond,
		MaxBackoff: 50 * time.Millisecond,
	}
}

func testRegistry(t *testing.T) *events.Registry {
	t.Helper()
	registry, err := events.NewRegistry(contracts.FS)
	if err != nil {
		t.Fatalf("compiling the event registry: %v", err)
	}
	return registry
}

// runner owns a Consumer running in its own goroutine.
type runner struct {
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
}

func startConsumer(t *testing.T, cfg ConsumerConfig) *runner {
	t.Helper()

	consumer, err := NewConsumer(cfg)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	r := &runner{cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- consumer.Run(ctx) }()
	t.Cleanup(func() { r.stop(t) })
	return r
}

// stop cancels the consumer and waits for Run to return, asserting it returned
// cleanly. It is idempotent so a test can stop explicitly and still be cleaned up.
func (r *runner) stop(t *testing.T) {
	t.Helper()
	r.once.Do(func() {
		r.cancel()
		select {
		case err := <-r.done:
			if err != nil {
				t.Errorf("Run returned an error: %v", err)
			}
		case <-time.After(shutdownTimeout):
			t.Errorf("Run did not return within %v of cancellation", shutdownTimeout)
		}
	})
}

func newPublisher(t *testing.T) *FranzPublisher {
	t.Helper()
	publisher, err := NewPublisher(Config{
		Brokers:  []string{testBroker},
		ClientID: "platform-kafka-test",
		// No linger: these tests publish one record at a time and wait, so
		// batching would only add latency.
		Linger: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	t.Cleanup(publisher.Close)
	return publisher
}

// fixture is an envelope and the exact bytes that were published, so a DLQ
// assertion can compare against what went on the wire.
type fixture struct {
	env events.Envelope
	raw []byte
}

func newEnvelope(t *testing.T, accountID string, sequence int) fixture {
	t.Helper()
	env := mustEnvelope(t, testEventType, testEventVersion, accountID, sequence)
	return fixture{env: env, raw: envelopeBytes(t, env)}
}

// mustEnvelope builds a DelinquencyChanged envelope whose dpd carries the test's
// sequence number. dpd is used rather than an added field because the payload
// schema sets additionalProperties: false — a test fixture must satisfy the
// frozen contract, not a relaxed copy of it.
func mustEnvelope(t *testing.T, eventType string, version int, accountID string, sequence int) events.Envelope {
	t.Helper()

	env, err := events.New(eventType, version, testProducer, testAggregate, accountID,
		map[string]any{
			"accountId":          accountID,
			"customerId":         ids.NewULID(),
			"dpd":                sequence,
			"previousBucket":     nil,
			"newBucket":          "DPD_31_60",
			"status":             "DELINQUENT",
			"overdueAmountMinor": 50000,
			"currency":           "EUR",
		})
	if err != nil {
		t.Fatalf("building a %s v%d envelope: %v", eventType, version, err)
	}
	return env
}

func envelopeBytes(t *testing.T, env events.Envelope) []byte {
	t.Helper()
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshalling an envelope: %v", err)
	}
	return raw
}

// sequenceOf reads the sequence number back out of the payload.
func sequenceOf(env events.Envelope) (int, error) {
	var payload struct {
		DPD int `json:"dpd"`
	}
	if err := env.UnmarshalPayload(&payload); err != nil {
		return 0, err
	}
	return payload.DPD, nil
}

// mutateEnvelope re-encodes an envelope document after an arbitrary edit, which
// is how a document the frozen schema forbids is produced without giving the
// Envelope struct an eleventh field.
func mutateEnvelope(t *testing.T, raw []byte, edit func(map[string]any)) []byte {
	t.Helper()

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("re-parsing an envelope to mutate it: %v", err)
	}
	edit(doc)

	mutated, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("re-encoding a mutated envelope: %v", err)
	}
	return mutated
}

// publish sends one record with a correlation id in the context, because that is
// the only state a publish ever happens in on this platform: the id enters at the
// HTTP edge or is minted by the job that started the flow, and is propagated
// unchanged (A§97).
func publish(t *testing.T, publisher *FranzPublisher, topic, key string, value []byte) {
	t.Helper()

	ctx := httpkit.ContextWithCorrelationID(context.Background(), testCorrelationID)
	if err := publisher.Publish(ctx, topic, key, value, nil); err != nil {
		t.Fatalf("publishing to %s: %v", topic, err)
	}
}

// publishSequence publishes n records for one key, in order, one at a time —
// which is what the outbox relay does for a key (ordered per key).
func publishSequence(t *testing.T, publisher *FranzPublisher, topic, key string, n int) {
	t.Helper()
	for sequence := range n {
		publish(t, publisher, topic, key, newEnvelope(t, key, sequence).raw)
	}
}

// readDLQ waits for want records on the DLQ topic and returns them in offset
// order.
func readDLQ(t *testing.T, topic string, want int) []*kgo.Record {
	t.Helper()

	client, err := kgo.NewClient(
		kgo.SeedBrokers(testBroker),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatalf("connecting to read the DLQ: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
	defer cancel()

	var records []*kgo.Record
	for len(records) < want {
		fetches := client.PollRecords(ctx, want-len(records))
		if err := fetches.Err(); err != nil {
			t.Fatalf("reading %s: got %d of %d records: %v", topic, len(records), want, err)
		}
		records = append(records, fetches.Records()...)
	}
	return records
}

// drainDLQ reads whatever is on the DLQ right now, without waiting for more. It
// answers "is the DLQ empty" — a question only worth asking once the consumer has
// demonstrably moved past the records under test.
func drainDLQ(t *testing.T, topic string) []*kgo.Record {
	t.Helper()

	client, err := kgo.NewClient(
		kgo.SeedBrokers(testBroker),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatalf("connecting to read the DLQ: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fetches := client.PollRecords(ctx, 100)
	return fetches.Records()
}

// awaitCount waits for n signals on ch.
func awaitCount[T any](t *testing.T, ch <-chan T, n int, what string) {
	t.Helper()

	deadline := time.After(settleTimeout)
	for got := range n {
		select {
		case <-ch:
		case <-deadline:
			t.Fatalf("timed out after %v with %d of %d %s", settleTimeout, got, n, what)
		}
	}
}

func assertHeaders(t *testing.T, got map[string]string, want map[string]string) {
	t.Helper()
	for name, value := range want {
		if got[name] != value {
			t.Errorf("header %s = %q, want %q", name, got[name], value)
		}
	}
}
