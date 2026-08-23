package events_test

import (
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/canhtoanptit/collection-platform/platform/clock"
	"github.com/canhtoanptit/collection-platform/platform/events"
)

func TestMarshalCanonicalSortsKeysAtEveryDepth(t *testing.T) {
	env, err := events.New(
		"CaseCreated", 1, "case-service", "Case", "01M0KK4P3G0MQSQ3A1X2PMA6VX",
		json.RawMessage(`{"zeta":1,"alpha":{"nested":[{"b":2,"a":1}],"beta":true},"mid":null}`),
		events.WithEventID("01M0MEKD80M9S346Q3D25VT4F5"),
		events.WithCorrelationID("01M0MEKBHXV37E3S3E28JT97KB"),
		events.WithCausationID("01M0MEKCV46CQ643DZVMXXQKFB"),
		events.WithOccurredAt(time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := events.MarshalCanonical(env)
	if err != nil {
		t.Fatalf("MarshalCanonical: %v", err)
	}

	want := `{` +
		`"aggregateId":"01M0KK4P3G0MQSQ3A1X2PMA6VX",` +
		`"aggregateType":"Case",` +
		`"causationId":"01M0MEKCV46CQ643DZVMXXQKFB",` +
		`"correlationId":"01M0MEKBHXV37E3S3E28JT97KB",` +
		`"eventId":"01M0MEKD80M9S346Q3D25VT4F5",` +
		`"eventType":"CaseCreated",` +
		`"eventVersion":1,` +
		`"occurredAt":"2026-08-22T10:00:00Z",` +
		`"payload":{"alpha":{"beta":true,"nested":[{"a":1,"b":2}]},"mid":null,"zeta":1},` +
		`"producer":"case-service"` +
		`}`
	if string(got) != want {
		t.Errorf("canonical form mismatch\ngot:  %s\nwant: %s", got, want)
	}
}

// TestMarshalCanonicalIsStableAcrossPayloadKeyOrder is the property the function
// exists for: the same fact hashes to the same digest however its payload was
// spelled.
func TestMarshalCanonicalIsStableAcrossPayloadKeyOrder(t *testing.T) {
	spellings := []string{
		`{"caseId":"01M0KK4P3G0MQSQ3A1X2PMA6VX","status":"OPEN","priority":2}`,
		`{"priority":2,"caseId":"01M0KK4P3G0MQSQ3A1X2PMA6VX","status":"OPEN"}`,
		`{"status":"OPEN","priority":2,"caseId":"01M0KK4P3G0MQSQ3A1X2PMA6VX"}`,
	}

	var first [32]byte
	for i, payload := range spellings {
		env, err := events.New(
			"CaseCreated", 1, "case-service", "Case", "01M0KK4P3G0MQSQ3A1X2PMA6VX",
			json.RawMessage(payload),
			events.WithEventID("01M0MEKD80M9S346Q3D25VT4F5"),
			events.WithCorrelationID("01M0MEKBHXV37E3S3E28JT97KB"),
			events.WithClock(clock.Fixed(time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC))),
		)
		if err != nil {
			t.Fatalf("New(%d): %v", i, err)
		}
		canonical, err := events.MarshalCanonical(env)
		if err != nil {
			t.Fatalf("MarshalCanonical(%d): %v", i, err)
		}
		digest := sha256.Sum256(canonical)
		if i == 0 {
			first = digest
			continue
		}
		if digest != first {
			t.Errorf("payload spelling %d hashes differently:\n%s", i, canonical)
		}
	}
}

// TestMarshalCanonicalRepeatsItself catches any dependence on Go map iteration
// order, which is randomised per range statement.
func TestMarshalCanonicalRepeatsItself(t *testing.T) {
	env := newCaseCreated(t,
		events.WithEventID("01M0MEKD80M9S346Q3D25VT4F5"),
		events.WithCorrelationID("01M0MEKBHXV37E3S3E28JT97KB"),
		events.WithClock(clock.Fixed(time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC))),
	)

	want, err := events.MarshalCanonical(env)
	if err != nil {
		t.Fatalf("MarshalCanonical: %v", err)
	}
	for i := range 50 {
		got, err := events.MarshalCanonical(env)
		if err != nil {
			t.Fatalf("MarshalCanonical (run %d): %v", i, err)
		}
		if string(got) != string(want) {
			t.Fatalf("run %d differs:\ngot:  %s\nwant: %s", i, got, want)
		}
	}
}

func TestMarshalCanonicalPreservesValues(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{"integers keep their literal", `{"amountMinor":50000}`, `{"amountMinor":50000}`},
		{"large int64 is not floated", `{"amountMinor":9007199254740993}`, `{"amountMinor":9007199254740993}`},
		{"trailing zeros are preserved", `{"rate":1.50}`, `{"rate":1.50}`},
		{"negative numbers", `{"adjustmentMinor":-2500}`, `{"adjustmentMinor":-2500}`},
		{"exponents keep their spelling", `{"tiny":1e-7}`, `{"tiny":1e-7}`},
		{"booleans and null", `{"a":true,"b":false,"c":null}`, `{"a":true,"b":false,"c":null}`},
		{"empty object", `{}`, `{}`},
		{"empty array", `{"items":[]}`, `{"items":[]}`},
		{"array order is significant", `{"items":[3,1,2]}`, `{"items":[3,1,2]}`},
		{"unicode strings", `{"name":"Ærø Ünïcøde"}`, `{"name":"Ærø Ünïcøde"}`},
		{"escapes stay escaped", `{"note":"line\nbreak\t\"quoted\""}`, `{"note":"line\nbreak\t\"quoted\""}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, err := events.New(
				"CaseCreated", 1, "case-service", "Case", "A1", json.RawMessage(tc.payload),
				events.WithEventID("01M0MEKD80M9S346Q3D25VT4F5"),
				events.WithCorrelationID("01M0MEKBHXV37E3S3E28JT97KB"),
				events.WithOccurredAt(time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)),
			)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			canonical, err := events.MarshalCanonical(env)
			if err != nil {
				t.Fatalf("MarshalCanonical: %v", err)
			}

			var doc struct {
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(canonical, &doc); err != nil {
				t.Fatalf("canonical output is not valid JSON (%s): %v", canonical, err)
			}
			if string(doc.Payload) != tc.want {
				t.Errorf("payload = %s, want %s", doc.Payload, tc.want)
			}
		})
	}
}

// TestMarshalCanonicalDiffersWhenTheFactDiffers is the counterpart property: the
// canonical form must not collapse two different events onto one hash.
func TestMarshalCanonicalDiffersWhenTheFactDiffers(t *testing.T) {
	base := newCaseCreated(t,
		events.WithEventID("01M0MEKD80M9S346Q3D25VT4F5"),
		events.WithCorrelationID("01M0MEKBHXV37E3S3E28JT97KB"),
		events.WithClock(clock.Fixed(time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC))),
	)
	baseline, err := events.MarshalCanonical(base)
	if err != nil {
		t.Fatalf("MarshalCanonical: %v", err)
	}

	mutations := map[string]func(events.Envelope) events.Envelope{
		"different eventId":     func(e events.Envelope) events.Envelope { e.EventID = "01M0MEKD80M9S346Q3D25VT4F6"; return e },
		"different occurredAt":  func(e events.Envelope) events.Envelope { e.OccurredAt = e.OccurredAt.Add(time.Second); return e },
		"different aggregateId": func(e events.Envelope) events.Envelope { e.AggregateID = "01M0KK4P3G0MQSQ3A1X2PMA6VY"; return e },
		"different payload":     func(e events.Envelope) events.Envelope { e.Payload = json.RawMessage(`{"caseId":"X"}`); return e },
		"causation id added": func(e events.Envelope) events.Envelope {
			e.CausationID = "01M0MEKCV46CQ643DZVMXXQKFB"
			return e
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			got, err := events.MarshalCanonical(mutate(base))
			if err != nil {
				t.Fatalf("MarshalCanonical: %v", err)
			}
			if string(got) == string(baseline) {
				t.Errorf("canonical form unchanged by %s", name)
			}
		})
	}
}

func TestMarshalCanonicalRejectsAnUnmarshallablePayload(t *testing.T) {
	// Envelope.Payload is raw JSON, so the only way to reach the error path is
	// to hand-build an envelope holding something that is not JSON — which is
	// exactly what a corrupted outbox row looks like.
	_, err := events.MarshalCanonical(events.Envelope{
		EventID:   "01M0MEKD80M9S346Q3D25VT4F5",
		EventType: "CaseCreated",
		Payload:   json.RawMessage(`{"caseId":`),
	})
	if err == nil {
		t.Fatal("MarshalCanonical accepted a corrupt payload")
	}
}
