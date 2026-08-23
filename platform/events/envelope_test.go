package events_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/canhtoanptit/collection-platform/platform/clock"
	"github.com/canhtoanptit/collection-platform/platform/events"
	"github.com/canhtoanptit/collection-platform/platform/ids"
)

// caseCreated is a payload that satisfies schemas/events/case/CaseCreated.v1.json,
// so a New + Validate pair proves the whole chain rather than just the struct.
type caseCreated struct {
	CaseID     string  `json:"caseId"`
	CustomerID string  `json:"customerId"`
	AccountID  string  `json:"accountId"`
	DebtID     *string `json:"debtId"`
	Status     string  `json:"status"`
	Priority   int     `json:"priority"`
}

func validPayload() caseCreated {
	return caseCreated{
		CaseID:     "01M0KK4P3G0MQSQ3A1X2PMA6VX",
		CustomerID: "01M0KK4K5RM5CNE9ZZQ52EJAC0",
		AccountID:  "01M0KK4M5029NKNFZXF7805T91",
		DebtID:     nil,
		Status:     "OPEN",
		Priority:   2,
	}
}

// newCaseCreated builds a CaseCreated envelope with the mandatory arguments
// filled in, so each test only states what it is exercising.
func newCaseCreated(t *testing.T, opts ...events.Option) events.Envelope {
	t.Helper()

	env, err := events.New(
		"CaseCreated", 1,
		"case-service", "Case", "01M0KK4P3G0MQSQ3A1X2PMA6VX",
		validPayload(),
		opts...,
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return env
}

func TestNewAppliesDefaults(t *testing.T) {
	fixed := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)

	env := newCaseCreated(t, events.WithClock(clock.Fixed(fixed)))

	if !ids.IsULID(env.EventID) {
		t.Errorf("eventId = %q, want a fresh bare ULID", env.EventID)
	}
	if !ids.IsULID(env.CorrelationID) {
		t.Errorf("correlationId = %q, want a fresh bare ULID", env.CorrelationID)
	}
	if env.CausationID != "" {
		t.Errorf("causationId = %q, want it omitted for the first event in a chain", env.CausationID)
	}
	if !env.OccurredAt.Equal(fixed) {
		t.Errorf("occurredAt = %v, want the injected clock's %v", env.OccurredAt, fixed)
	}
	if env.EventID == env.CorrelationID {
		t.Error("eventId and correlationId are the same value")
	}

	// Two calls must not share an event id: that would collapse inbox dedupe.
	other := newCaseCreated(t, events.WithClock(clock.Fixed(fixed)))
	if other.EventID == env.EventID {
		t.Error("two events were minted with the same eventId")
	}
	if other.CorrelationID == env.CorrelationID {
		t.Error("two independent flows were minted with the same correlationId")
	}
}

func TestNewOptions(t *testing.T) {
	const (
		eventID       = "01M0MEKD80M9S346Q3D25VT4F5"
		correlationID = "01M0MEKBHXV37E3S3E28JT97KB"
		causationID   = "01M0MEKCV46CQ643DZVMXXQKFB"
	)
	occurred := time.Date(2026, 9, 1, 2, 25, 1, 0, time.UTC)

	tests := []struct {
		name  string
		opts  []events.Option
		check func(*testing.T, events.Envelope)
	}{
		{
			name: "WithEventID replays an existing identity",
			opts: []events.Option{events.WithEventID(eventID)},
			check: func(t *testing.T, e events.Envelope) {
				if e.EventID != eventID {
					t.Errorf("eventId = %q, want %q", e.EventID, eventID)
				}
			},
		},
		{
			name: "WithCorrelationID continues a flow",
			opts: []events.Option{events.WithCorrelationID(correlationID)},
			check: func(t *testing.T, e events.Envelope) {
				if e.CorrelationID != correlationID {
					t.Errorf("correlationId = %q, want %q", e.CorrelationID, correlationID)
				}
			},
		},
		{
			name: "WithCausationID records the cause",
			opts: []events.Option{events.WithCausationID(causationID)},
			check: func(t *testing.T, e events.Envelope) {
				if e.CausationID != causationID {
					t.Errorf("causationId = %q, want %q", e.CausationID, causationID)
				}
			},
		},
		{
			name: "WithOccurredAt overrides the clock",
			opts: []events.Option{
				events.WithClock(clock.Fixed(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))),
				events.WithOccurredAt(occurred),
			},
			check: func(t *testing.T, e events.Envelope) {
				if !e.OccurredAt.Equal(occurred) {
					t.Errorf("occurredAt = %v, want %v", e.OccurredAt, occurred)
				}
			},
		},
		{
			name: "WithOccurredAt normalises a local offset to UTC",
			opts: []events.Option{
				events.WithOccurredAt(time.Date(2026, 9, 1, 11, 25, 1, 0, time.FixedZone("UTC+9", 9*3600))),
			},
			check: func(t *testing.T, e events.Envelope) {
				if !e.OccurredAt.Equal(occurred) {
					t.Errorf("occurredAt = %v, want %v", e.OccurredAt, occurred)
				}
				if e.OccurredAt.Location() != time.UTC {
					t.Errorf("occurredAt location = %v, want UTC", e.OccurredAt.Location())
				}
			},
		},
		{
			name: "occurredAt is truncated to microseconds",
			opts: []events.Option{
				events.WithOccurredAt(time.Date(2026, 9, 1, 2, 25, 1, 123456789, time.UTC)),
			},
			check: func(t *testing.T, e events.Envelope) {
				want := time.Date(2026, 9, 1, 2, 25, 1, 123456000, time.UTC)
				if !e.OccurredAt.Equal(want) {
					t.Errorf("occurredAt = %v, want %v (microsecond precision)", e.OccurredAt, want)
				}
			},
		},
		{
			name: "the last option wins",
			opts: []events.Option{
				events.WithCorrelationID("01M0MEKBHXV37E3S3E28JT97KB"),
				events.WithCorrelationID(correlationID),
			},
			check: func(t *testing.T, e events.Envelope) {
				if e.CorrelationID != correlationID {
					t.Errorf("correlationId = %q, want %q", e.CorrelationID, correlationID)
				}
			},
		},
		{
			name: "an empty option value falls back to the default",
			opts: []events.Option{events.WithCorrelationID(""), events.WithEventID("")},
			check: func(t *testing.T, e events.Envelope) {
				if !ids.IsULID(e.CorrelationID) || !ids.IsULID(e.EventID) {
					t.Errorf("empty option values did not fall back: eventId=%q correlationId=%q", e.EventID, e.CorrelationID)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, newCaseCreated(t, tc.opts...))
		})
	}
}

func TestNewProducesEnvelopesTheContractsAccept(t *testing.T) {
	r := contractRegistry(t)

	env := newCaseCreated(t,
		events.WithClock(clock.Fixed(time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC))),
		events.WithCausationID("01M0MEKCV46CQ643DZVMXXQKFB"),
	)

	if err := r.Validate(env); err != nil {
		t.Fatalf("Validate on a freshly built envelope: %v", err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if err := r.ValidateJSON(raw); err != nil {
		t.Fatalf("ValidateJSON on a freshly built envelope: %v", err)
	}
	// occurredAt must serialize as RFC3339 with a Z suffix, which the envelope
	// schema's UTC-only pattern requires.
	if got := string(raw); !strings.Contains(got, `"occurredAt":"2026-08-22T10:00:00Z"`) {
		t.Errorf("occurredAt is not serialized as RFC3339 UTC: %s", got)
	}
}

// TestNewOmitsCausationIDRatherThanSendingNull pins the A§24 rule that the first
// event in a chain omits the property entirely.
func TestNewOmitsCausationIDRatherThanSendingNull(t *testing.T) {
	raw, err := json.Marshal(newCaseCreated(t))
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if strings.Contains(string(raw), "causationId") {
		t.Errorf("causationId is present with no cause set: %s", raw)
	}
}

func TestNewRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		version   int
		producer  string
		aggType   string
		aggID     string
		payload   any
		opts      []events.Option
		wantIn    []string
	}{
		{
			name:    "everything missing at once is reported together",
			payload: nil,
			wantIn: []string{
				"eventType is empty", "eventVersion 0 is below 1", "producer is empty",
				"aggregateType is empty", "aggregateId is empty", "payload must be a JSON object",
			},
		},
		{
			name: "version zero", eventType: "CaseCreated", version: 0,
			producer: "case-service", aggType: "Case", aggID: "A1", payload: validPayload(),
			wantIn: []string{"eventVersion 0 is below 1"},
		},
		{
			name: "negative version", eventType: "CaseCreated", version: -1,
			producer: "case-service", aggType: "Case", aggID: "A1", payload: validPayload(),
			wantIn: []string{"eventVersion -1 is below 1"},
		},
		{
			name: "empty producer", eventType: "CaseCreated", version: 1,
			producer: "", aggType: "Case", aggID: "A1", payload: validPayload(),
			wantIn: []string{"producer is empty"},
		},
		{
			name: "empty aggregate id", eventType: "CaseCreated", version: 1,
			producer: "case-service", aggType: "Case", aggID: "", payload: validPayload(),
			wantIn: []string{"aggregateId is empty"},
		},
		{
			name: "payload is a string", eventType: "CaseCreated", version: 1,
			producer: "case-service", aggType: "Case", aggID: "A1", payload: "opaque",
			wantIn: []string{"payload must be a JSON object", "a string"},
		},
		{
			name: "payload is an array", eventType: "CaseCreated", version: 1,
			producer: "case-service", aggType: "Case", aggID: "A1", payload: []int{1, 2},
			wantIn: []string{"payload must be a JSON object", "an array"},
		},
		{
			name: "payload is a number", eventType: "CaseCreated", version: 1,
			producer: "case-service", aggType: "Case", aggID: "A1", payload: 42,
			wantIn: []string{"payload must be a JSON object", "a number"},
		},
		{
			name: "payload is nil", eventType: "CaseCreated", version: 1,
			producer: "case-service", aggType: "Case", aggID: "A1", payload: nil,
			wantIn: []string{"payload must be a JSON object", "null"},
		},
		{
			name: "payload cannot be marshalled", eventType: "CaseCreated", version: 1,
			producer: "case-service", aggType: "Case", aggID: "A1", payload: make(chan int),
			wantIn: []string{"payload"},
		},
		{
			name: "pre-encoded payload is not valid JSON", eventType: "CaseCreated", version: 1,
			producer: "case-service", aggType: "Case", aggID: "A1", payload: json.RawMessage(`{"caseId":`),
			wantIn: []string{"not valid JSON"},
		},
		{
			name: "explicit event id is not a ULID", eventType: "CaseCreated", version: 1,
			producer: "case-service", aggType: "Case", aggID: "A1", payload: validPayload(),
			opts:   []events.Option{events.WithEventID("EVT-1")},
			wantIn: []string{`eventId "EVT-1" is not a bare ULID`},
		},
		{
			name: "correlation id carries the COR_ operational prefix", eventType: "CaseCreated", version: 1,
			producer: "case-service", aggType: "Case", aggID: "A1", payload: validPayload(),
			opts:   []events.Option{events.WithCorrelationID("COR_01M0MEKBHXV37E3S3E28JT97KB")},
			wantIn: []string{"is not a bare ULID"},
		},
		{
			name: "causation id is not a ULID", eventType: "CaseCreated", version: 1,
			producer: "case-service", aggType: "Case", aggID: "A1", payload: validPayload(),
			opts:   []events.Option{events.WithCausationID("CMD123")},
			wantIn: []string{`causationId "CMD123" is not a bare ULID`},
		},
		{
			name: "occurredAt is the zero time", eventType: "CaseCreated", version: 1,
			producer: "case-service", aggType: "Case", aggID: "A1", payload: validPayload(),
			opts:   []events.Option{events.WithOccurredAt(time.Time{})},
			wantIn: []string{"occurredAt is the zero time"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, err := events.New(tc.eventType, tc.version, tc.producer, tc.aggType, tc.aggID, tc.payload, tc.opts...)
			if err == nil {
				t.Fatalf("New accepted invalid input and returned %s", env)
			}
			if !errors.Is(err, events.ErrInvalidEnvelope) {
				t.Errorf("error does not wrap ErrInvalidEnvelope: %v", err)
			}
			if !reflect.DeepEqual(env, events.Envelope{}) {
				t.Errorf("New returned a partially built envelope alongside the error: %+v", env)
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestNewAcceptsPreEncodedPayloads(t *testing.T) {
	tests := []struct {
		name    string
		payload any
	}{
		{"json.RawMessage", json.RawMessage(`{"caseId":"01M0KK4P3G0MQSQ3A1X2PMA6VX"}`)},
		{"raw bytes", []byte(`{"caseId":"01M0KK4P3G0MQSQ3A1X2PMA6VX"}`)},
		{"leading whitespace", json.RawMessage("  \n{\"caseId\":\"01M0KK4P3G0MQSQ3A1X2PMA6VX\"}")},
		{"empty object", json.RawMessage(`{}`)},
		{"a map", map[string]any{"caseId": "01M0KK4P3G0MQSQ3A1X2PMA6VX"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, err := events.New("CaseCreated", 1, "case-service", "Case", "A1", tc.payload)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if len(env.Payload) == 0 {
				t.Fatal("payload is empty")
			}
			if !json.Valid(env.Payload) {
				t.Fatalf("payload is not valid JSON: %s", env.Payload)
			}
		})
	}
}

// TestNewCopiesPreEncodedPayloads guards against a caller reusing (and then
// mutating) the buffer it handed in: the envelope must own its payload.
func TestNewCopiesPreEncodedPayloads(t *testing.T) {
	buf := []byte(`{"caseId":"01M0KK4P3G0MQSQ3A1X2PMA6VX"}`)

	env, err := events.New("CaseCreated", 1, "case-service", "Case", "A1", buf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	copy(buf, `{"caseId":"OVERWRITTEN______________"}`)

	if strings.Contains(string(env.Payload), "OVERWRITTEN") {
		t.Errorf("the envelope aliased the caller's buffer: %s", env.Payload)
	}
}

func TestUnmarshalPayload(t *testing.T) {
	env := newCaseCreated(t)

	var got caseCreated
	if err := env.UnmarshalPayload(&got); err != nil {
		t.Fatalf("UnmarshalPayload: %v", err)
	}
	if want := validPayload(); got != want {
		t.Errorf("payload = %+v, want %+v", got, want)
	}

	if err := (events.Envelope{}).UnmarshalPayload(&got); err == nil {
		t.Error("UnmarshalPayload on an empty envelope returned nil")
	}
	if err := env.UnmarshalPayload(&struct{ Priority string }{}); err == nil {
		t.Error("UnmarshalPayload into an incompatible type returned nil")
	}
}

func TestEnvelopeKeyAndString(t *testing.T) {
	env := newCaseCreated(t, events.WithEventID("01M0MEKD80M9S346Q3D25VT4F5"))

	if want := (events.Key{EventType: "CaseCreated", Version: 1}); env.Key() != want {
		t.Errorf("Key() = %v, want %v", env.Key(), want)
	}

	s := env.String()
	for _, want := range []string{"CaseCreated", "v1", "Case", "01M0MEKD80M9S346Q3D25VT4F5"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, want it to mention %q", s, want)
		}
	}
	// The payload may carry personal data, so it must never be in the log line.
	if strings.Contains(s, "01M0KK4K5RM5CNE9ZZQ52EJAC0") {
		t.Errorf("String() leaked payload content: %q", s)
	}
}
