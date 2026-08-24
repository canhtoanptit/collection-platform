// Package kafka is the platform's event-bus boundary: services import this
// package and never `kgo` (D§58, ADR-0004 — the broker stays replaceable
// because exactly one package knows which client is in use).
//
// It offers two things and deliberately nothing else:
//
//   - [Publisher] — a one-method interface plus a franz-go implementation with
//     acks=all and the idempotent producer on. Services do not use it directly:
//     they publish through platform/outbox in the same transaction as the state
//     change (CLAUDE.md §5), and the outbox relay is the only caller.
//   - [Consumer] — a consumer group with the A§27 delivery contract:
//     per-partition sequential handling, envelope validation before any business
//     code sees a message, retry with backoff, then the DLQ, and a commit only
//     after the message has been settled one way or another.
//
// # What the consumer guarantees, exactly
//
// Delivery is at-least-once and stays at-least-once: a duplicate is possible
// after a rebalance or a crash between the handler and the commit. Making the
// *business effect* exactly once is the caller's job, via
// platform/inbox.Dedupe in the same transaction as the side effects (D§3.5).
// This package will never hide a duplicate.
//
// Ordering is per partition, therefore per key, and nothing more. Records from
// one partition are handled one at a time, in offset order; different partitions
// proceed concurrently. A handler that needs global ordering does not exist on
// this platform (A§26).
//
// Every record reaches exactly one of these states before its offset is
// committed:
//
//	handled          the handler returned nil, on the first try or a retry
//	skipped          the (eventType, eventVersion) is not in the registry
//	dead-lettered    malformed, or the handler still failed after MaxRetries
//	unsettled        the DLQ produce itself failed — Run returns, nothing is
//	                 committed past that record, and the pod restarts into a
//	                 replay rather than dropping the message
//
// "Skipped" is a contract, not a shortcut: contracts/README §13 requires a
// consumer to ignore event types and versions it does not know, so a new event
// can be rolled out before every consumer understands it. Dead-lettering an
// unknown type would make every new event type an incident for every old
// consumer.
//
// # Headers
//
// A published message carries the W3C traceparent plus the A§97 correlation and
// causation ids, injected by platform/otelkit — the publisher adds them itself,
// so a message cannot be published without them. On the way in, the consumer
// restores the trace and correlation id from the headers and sets the causation
// id from the envelope's eventId, so an event a handler emits records what
// caused it without the handler having to remember.
package kafka

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl"
	kaws "github.com/twmb/franz-go/pkg/sasl/aws"
)

// Producer defaults. Every one is an env var on [Config].
const (
	// defaultLinger batches records for a few milliseconds before sending. It
	// trades a few ms of publish latency for far fewer requests; the outbox
	// relay publishes in batches anyway, so this mostly helps the DLQ path.
	defaultLinger = 5 * time.Millisecond
	// defaultMaxBufferedRecords bounds producer memory. When it is reached,
	// Publish blocks rather than dropping — an outbox relay that cannot publish
	// must slow down, not lose events.
	defaultMaxBufferedRecords = 10_000
	// defaultDeliveryTimeout bounds one Publish. franz-go's default is *no*
	// limit, which combined with auto-create being off (ADR-0004) means a
	// produce to an undeclared topic retries on UNKNOWN_TOPIC_OR_PARTITION
	// forever: the relay hangs with no error, no metric and no log line. A
	// bounded failure the relay retries on its next tick is strictly better than
	// a stall nobody can see.
	defaultDeliveryTimeout = 30 * time.Second
	// tlsMinVersion is the floor for any TLS dial. MSK offers 1.2 and 1.3.
	tlsMinVersion = tls.VersionTLS12
)

// Config is the connection configuration shared by the publisher and the
// consumer. A service embeds it, or builds it from config.Base.KafkaBrokers.
//
// Three deployment shapes are covered by the same struct:
//
//	local / e2e (redpanda)  Brokers only — plaintext, no auth
//	TLS without auth        TLS: true
//	MSK (dev and beyond)    SASLIAMRegion set — implies TLS and IAM SASL
type Config struct {
	// Brokers is the bootstrap broker list. Same variable as
	// config.Base.KafkaBrokers, so a service that embeds both gets one value in
	// two places rather than two sources of truth.
	Brokers []string `env:"KAFKA_BROKERS" envSeparator:","`

	// TLS dials brokers over TLS with the system root pool. It is implied by
	// SASLIAMRegion and only needs setting for a TLS listener without IAM auth.
	TLS bool `env:"KAFKA_TLS"`

	// SASLIAMRegion enables Amazon MSK IAM authentication (AWS_MSK_IAM) and
	// names the region whose credentials to sign with. Credentials come from the
	// default AWS chain — in the cluster that is the pod's IRSA role, never a
	// static key (ADR-0011: no AWS keys, OIDC federation only).
	//
	// Empty means no SASL, which is what local Redpanda and the e2e stack want.
	SASLIAMRegion string `env:"KAFKA_SASL_IAM_REGION"`

	// ClientID identifies this process in broker logs and quota accounting. Set
	// it to the service name; an unset client id makes a misbehaving client
	// unattributable.
	ClientID string `env:"KAFKA_CLIENT_ID"`

	// Linger is the producer batching delay. See defaultLinger.
	Linger time.Duration `env:"KAFKA_PRODUCER_LINGER" envDefault:"5ms"`

	// MaxBufferedRecords caps unflushed producer records. See
	// defaultMaxBufferedRecords.
	MaxBufferedRecords int `env:"KAFKA_MAX_BUFFERED_RECORDS" envDefault:"10000"`

	// DeliveryTimeout bounds one publish, including franz-go's internal retries.
	// See defaultDeliveryTimeout for why leaving it unbounded is not an option.
	DeliveryTimeout time.Duration `env:"KAFKA_DELIVERY_TIMEOUT" envDefault:"30s"`
}

// connectionOpts builds the client options every client shares: brokers,
// identity, transport security and logging.
//
// kgo.AllowAutoTopicCreation is conspicuously absent. Auto-create is off on every
// environment (ADR-0004) because a topic's partition count is a reviewed
// decision in deployment/kafka/topics.yaml; producing to a typo must be a loud
// error, not a silently created one-partition topic that reorders a key forever.
func (c Config) connectionOpts() ([]kgo.Opt, error) {
	brokers := nonEmpty(c.Brokers)
	if len(brokers) == 0 {
		return nil, errors.New("no Kafka brokers configured (KAFKA_BROKERS)")
	}

	opts := []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.WithLogger(slogLogger{}),
	}
	if c.ClientID != "" {
		opts = append(opts, kgo.ClientID(c.ClientID))
	}

	if c.SASLIAMRegion != "" {
		opts = append(opts, kgo.SASL(mskIAM(c.SASLIAMRegion)))
	}
	// IAM authentication signs a request that is only meaningful over TLS, and
	// MSK's IAM listener is TLS-only, so the region implies it. A caller cannot
	// accidentally ship SigV4 credentials over plaintext.
	if c.TLS || c.SASLIAMRegion != "" {
		opts = append(opts, kgo.DialTLSConfig(&tls.Config{MinVersion: tlsMinVersion}))
	}
	return opts, nil
}

// producerOpts is the durability configuration for anything that publishes: the
// relay's publisher and the consumer's DLQ producer both use it.
//
// acks=all plus the idempotent producer is the whole reason a published event can
// be trusted: acks=all means every in-sync replica holds the record before the
// broker acknowledges it, and idempotency means a produce franz-go retried after
// a network error cannot append the record twice. franz-go enables idempotency by
// default and refuses acks < all while it is on, so the two are stated together
// here — reading this file should not require knowing a library default.
func (c Config) producerOpts() []kgo.Opt {
	linger := c.Linger
	if linger <= 0 {
		linger = defaultLinger
	}
	buffered := c.MaxBufferedRecords
	if buffered <= 0 {
		buffered = defaultMaxBufferedRecords
	}
	delivery := c.DeliveryTimeout
	if delivery <= 0 {
		delivery = defaultDeliveryTimeout
	}
	return []kgo.Opt{
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerLinger(linger),
		kgo.MaxBufferedRecords(buffered),
		kgo.RecordDeliveryTimeout(delivery),
	}
}

// mskIAM builds the AWS_MSK_IAM mechanism.
func mskIAM(region string) sasl.Mechanism {
	creds := &iamCredentials{region: region}
	return kaws.ManagedStreamingIAM(creds.auth)
}

// iamCredentials resolves AWS credentials for MSK IAM, lazily and at most once
// per successful load.
//
// Lazily, because franz-go hands the auth callback a real context: there is no
// need to invent one at construction time (CLAUDE.md §3 — no context.Background
// outside main), and a process that never reaches a broker never touches the
// credential chain. At most once per success, because LoadDefaultConfig walks
// the environment, the shared config files, the container credential endpoint
// and IMDS; a failure is worth retrying on the next authentication, a success is
// not worth repeating. The resolved provider is already a credentials cache, so
// the per-connection Retrieve is a map lookup until the session token nears
// expiry.
type iamCredentials struct {
	region string

	mu       sync.Mutex
	retrieve func(context.Context) (kaws.Auth, error)
}

func (c *iamCredentials) auth(ctx context.Context) (kaws.Auth, error) {
	retrieve, err := c.provider(ctx)
	if err != nil {
		return kaws.Auth{}, err
	}
	return retrieve(ctx)
}

func (c *iamCredentials) provider(ctx context.Context) (func(context.Context) (kaws.Auth, error), error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.retrieve != nil {
		return c.retrieve, nil
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(c.region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS configuration for MSK IAM in %s: %w", c.region, err)
	}
	if cfg.Credentials == nil {
		return nil, fmt.Errorf(
			"no AWS credentials resolved for MSK IAM in %s — the pod needs an IRSA role", c.region)
	}

	c.retrieve = func(ctx context.Context) (kaws.Auth, error) {
		awsCreds, err := cfg.Credentials.Retrieve(ctx)
		if err != nil {
			return kaws.Auth{}, fmt.Errorf("retrieving AWS credentials for MSK IAM: %w", err)
		}
		return kaws.Auth{
			AccessKey:    awsCreds.AccessKeyID,
			SecretKey:    awsCreds.SecretAccessKey,
			SessionToken: awsCreds.SessionToken,
		}, nil
	}
	return c.retrieve, nil
}

// slogLogger bridges franz-go's logger onto slog, so client-level problems (a
// broker that stopped answering, a rebalance that keeps failing) appear in the
// same structured stream as everything else.
//
// The level is Warn: franz-go's Info includes every metadata refresh and every
// rebalance, which at this platform's volume is noise that would bury the lines
// an operator needs.
type slogLogger struct{}

func (slogLogger) Level() kgo.LogLevel { return kgo.LogLevelWarn }

func (slogLogger) Log(level kgo.LogLevel, msg string, keyvals ...any) {
	switch level {
	case kgo.LogLevelError:
		slog.Error("kafka client: "+msg, keyvals...)
	default:
		slog.Warn("kafka client: "+msg, keyvals...)
	}
}

// nonEmpty drops blank entries, so KAFKA_BROKERS="a:9092,,b:9092" or a trailing
// comma in a values file is a broker list of two rather than a dial to "".
func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
