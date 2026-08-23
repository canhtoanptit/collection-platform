// Package events builds and validates the A§24 event envelope.
//
// The envelope is the platform's only event wire format: exactly ten fields,
// nothing else (CLAUDE.md §5). It is the Kafka message value on
// `collections.<context>` and `ingestion.*` topics, the JSONB column in the
// transactional outbox, and the document replayed from the DLQ. No service
// constructs one by hand — New is the single constructor, and it fails rather
// than emit something the frozen envelope schema would reject.
//
// Validation is two-stage and mechanical: the whole document against
// `schemas/envelope/EventEnvelope.v1.json`, then `payload` against
// `schemas/events/<context>/<eventType>.v<eventVersion>.json`. The
// (eventType, eventVersion) pair locates the payload schema, which is why
// Registry refuses to load a contracts FS where two contexts claim the same
// pair. Everything resolves from the embedded contracts FS — nothing is ever
// fetched over the network.
package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/canhtoanptit/collection-platform/platform/clock"
	"github.com/canhtoanptit/collection-platform/platform/ids"
)

// ErrInvalidEnvelope reports that an envelope is structurally unusable — a
// missing field, a non-ULID identifier, a payload that is not a JSON object.
// New returns it (joined with one error per problem) before anything is
// published, so an invalid event cannot reach the outbox or the broker.
var ErrInvalidEnvelope = errors.New("invalid event envelope")

// occurredAtPrecision is the resolution of the envelope's occurredAt.
//
// Microseconds, not nanoseconds: `timestamptz` in Postgres and `TIMESTAMP_NTZ`
// in Snowflake both store microseconds, so an event that round-trips through
// the outbox, the broker and the warehouse compares equal to the one that was
// minted. RFC3339 with a Z suffix and at most 9 fractional digits is what the
// envelope schema permits; six is inside that and stays exactly reproducible.
const occurredAtPrecision = time.Microsecond

// Envelope is the A§24 event envelope — exactly these ten fields, in this
// order, with these JSON names. Adding a field here is a contract change: it
// ships as EventEnvelope.v2.json first (contracts/README §1).
type Envelope struct {
	// EventID identifies this event instance. Assigned once by the producer,
	// never reused; consumers dedupe on (consumer, eventId).
	EventID string `json:"eventId"`
	// EventType is the business event name in PascalCase, with no version
	// suffix — the version lives in EventVersion.
	EventType string `json:"eventType"`
	// EventVersion is the major version of the payload contract, from 1.
	EventVersion int `json:"eventVersion"`
	// OccurredAt is when the business fact happened — not when it was
	// published or consumed. Always UTC.
	OccurredAt time.Time `json:"occurredAt"`
	// Producer is the emitting service in kebab-case, matching its deployable
	// name. One producer per event type (A§7.2).
	Producer string `json:"producer"`
	// AggregateType is the PascalCase type of the aggregate the event is about.
	AggregateType string `json:"aggregateType"`
	// AggregateID identifies the aggregate instance. Normally a ULID; a
	// source-system identifier is permitted for externally keyed aggregates.
	AggregateID string `json:"aggregateId"`
	// CorrelationID ties every hop of one business interaction together
	// (A§97). A bare ULID, propagated unchanged — never re-minted mid-flow.
	CorrelationID string `json:"correlationId"`
	// CausationID is the direct cause of this event: the eventId being
	// handled, or the Idempotency-Key of the originating command. Omitted
	// entirely for the first event in a chain — never sent as null.
	CausationID string `json:"causationId,omitempty"`
	// Payload is the event-type-specific body, always a JSON object, validated
	// against its own schema by Registry.
	Payload json.RawMessage `json:"payload"`
}

// Option customises New. Defaults are applied first, so an option always wins.
type Option func(*builder)

// builder holds the parts of an envelope an Option may replace.
type builder struct {
	eventID       string
	occurredAt    time.Time
	correlationID string
	causationID   string
	clk           clock.Clock
	occurredSet   bool
}

// WithEventID sets the event id explicitly. Use it only when the id already
// exists — replaying a DLQ record, or re-emitting a fact whose identity was
// assigned upstream. A new fact gets a new id, which is the default.
func WithEventID(id string) Option {
	return func(b *builder) { b.eventID = id }
}

// WithCorrelationID continues an existing correlation id. Every handler that
// emits an event while processing an inbound request or event must pass the id
// it received: minting a fresh one breaks the A§97 chain.
func WithCorrelationID(id string) Option {
	return func(b *builder) { b.correlationID = id }
}

// WithCausationID records the direct cause of this event — the eventId being
// handled, or the Idempotency-Key of the command that produced it. Omit it only
// for the first event in a chain.
func WithCausationID(id string) Option {
	return func(b *builder) { b.causationID = id }
}

// WithOccurredAt sets the instant the business fact happened. Use it when the
// fact is older than the code handling it (a file's business date, a
// source-system payment timestamp); otherwise let WithClock supply it.
func WithOccurredAt(t time.Time) Option {
	return func(b *builder) {
		b.occurredAt = t
		b.occurredSet = true
	}
}

// WithClock supplies the clock New reads occurredAt from. This is the injection
// seam CLAUDE.md §3 requires: production wiring passes clock.System(), tests
// pass clock.Fixed or a clock.Mock and get byte-identical envelopes.
func WithClock(c clock.Clock) Option {
	return func(b *builder) { b.clk = c }
}

// New builds a validated envelope around payload.
//
// Defaults: a fresh ULID eventId; occurredAt = now (UTC, microseconds) from the
// injected clock; a fresh ULID correlationId when none is supplied — a new
// correlation id means "this flow starts here", so a handler continuing a flow
// must pass WithCorrelationID.
//
// The returned error joins every problem found (wrapping ErrInvalidEnvelope) so
// one call reports all of them. It is a *structural* check: New guarantees the
// envelope fields are well formed, Registry.Validate proves the document and
// its payload satisfy the frozen schemas.
func New(
	eventType string,
	version int,
	producer, aggregateType, aggregateID string,
	payload any,
	opts ...Option,
) (Envelope, error) {
	b := builder{clk: clock.System()}
	for _, opt := range opts {
		opt(&b)
	}
	if b.eventID == "" {
		b.eventID = ids.NewULID()
	}
	if b.correlationID == "" {
		b.correlationID = ids.NewULID()
	}
	if !b.occurredSet {
		b.occurredAt = b.clk.Now()
	}

	raw, payloadErr := marshalPayload(payload)

	env := Envelope{
		EventID:       b.eventID,
		EventType:     eventType,
		EventVersion:  version,
		OccurredAt:    b.occurredAt.UTC().Truncate(occurredAtPrecision),
		Producer:      producer,
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		CorrelationID: b.correlationID,
		CausationID:   b.causationID,
		Payload:       raw,
	}
	if err := errors.Join(payloadErr, env.check()); err != nil {
		return Envelope{}, fmt.Errorf("%w: %w", ErrInvalidEnvelope, err)
	}
	return env, nil
}

// marshalPayload renders payload as the envelope's JSON object. A payload that
// is not an object (a string, an array, nil) is refused here rather than at the
// schema boundary, because `payload` must stay extensible within a version.
func marshalPayload(payload any) (json.RawMessage, error) {
	if raw, ok := payload.(json.RawMessage); ok {
		payload = []byte(raw)
	}
	if raw, ok := payload.([]byte); ok {
		if !json.Valid(raw) {
			return nil, errors.New("payload: pre-encoded bytes are not valid JSON")
		}
		if err := requireObject(raw); err != nil {
			return nil, err
		}
		return json.RawMessage(bytes.Clone(raw)), nil
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	if err := requireObject(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// requireObject reports whether raw encodes a JSON object.
func requireObject(raw []byte) error {
	if trimmed := bytes.TrimLeft(raw, " \t\r\n"); len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf("payload must be a JSON object, got %s", describeJSON(raw))
	}
	return nil
}

// describeJSON names the JSON kind of raw for an error message, without
// echoing its content into a log line.
func describeJSON(raw []byte) string {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 {
		return "nothing"
	}
	switch trimmed[0] {
	case '[':
		return "an array"
	case '"':
		return "a string"
	case 'n':
		return "null"
	case 't', 'f':
		return "a boolean"
	default:
		return "a number"
	}
}

// check validates the envelope's structure, returning one joined error listing
// every problem.
func (e Envelope) check() error {
	var errs []error

	if !ids.IsULID(e.EventID) {
		errs = append(errs, fmt.Errorf("eventId %q is not a bare ULID", e.EventID))
	}
	if !ids.IsULID(e.CorrelationID) {
		errs = append(errs, fmt.Errorf("correlationId %q is not a bare ULID", e.CorrelationID))
	}
	if e.CausationID != "" && !ids.IsULID(e.CausationID) {
		errs = append(errs, fmt.Errorf("causationId %q is not a bare ULID", e.CausationID))
	}
	if e.EventType == "" {
		errs = append(errs, errors.New("eventType is empty"))
	}
	if e.EventVersion < 1 {
		errs = append(errs, fmt.Errorf("eventVersion %d is below 1", e.EventVersion))
	}
	if e.Producer == "" {
		errs = append(errs, errors.New("producer is empty"))
	}
	if e.AggregateType == "" {
		errs = append(errs, errors.New("aggregateType is empty"))
	}
	if e.AggregateID == "" {
		errs = append(errs, errors.New("aggregateId is empty"))
	}
	if e.OccurredAt.IsZero() {
		errs = append(errs, errors.New("occurredAt is the zero time"))
	}
	if len(e.Payload) == 0 {
		errs = append(errs, errors.New("payload is empty"))
	}
	return errors.Join(errs...)
}

// Key returns the (eventType, eventVersion) pair that locates the payload
// schema and the registry entry for this event.
func (e Envelope) Key() Key {
	return Key{EventType: e.EventType, Version: e.EventVersion}
}

// UnmarshalPayload decodes the payload into v. Validate the envelope first: a
// payload that has not been schema-checked may be missing fields v declares.
func (e Envelope) UnmarshalPayload(v any) error {
	if len(e.Payload) == 0 {
		return fmt.Errorf("decoding %s payload: envelope carries no payload", e.EventType)
	}
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return fmt.Errorf("decoding %s v%d payload: %w", e.EventType, e.EventVersion, err)
	}
	return nil
}

// String renders the envelope's identity for logs and error messages. It
// deliberately omits the payload, which may contain personal data.
func (e Envelope) String() string {
	return fmt.Sprintf("%s v%d %s/%s (eventId=%s correlationId=%s)",
		e.EventType, e.EventVersion, e.AggregateType, e.AggregateID, e.EventID, e.CorrelationID)
}
