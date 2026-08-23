package events_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/canhtoanptit/collection-platform/contracts"
	"github.com/canhtoanptit/collection-platform/platform/events"
)

// contractRegistry compiles the real contracts FS once per test binary: it is
// immutable after construction, so every test shares it.
func contractRegistry(t *testing.T) *events.Registry {
	t.Helper()

	registryOnce.Do(func() {
		sharedRegistry, sharedErr = events.NewRegistry(contracts.FS)
	})
	if sharedErr != nil {
		t.Fatalf("NewRegistry(contracts.FS): %v", sharedErr)
	}
	return sharedRegistry
}

var (
	registryOnce   sync.Once
	sharedRegistry *events.Registry
	sharedErr      error
)

// TestEveryContractExampleValidates is the acceptance test for LIB-1: every
// golden event example the contracts module ships must pass both stages of
// validation, through both entry points, and must survive a decode/re-encode
// round trip without losing a field.
func TestEveryContractExampleValidates(t *testing.T) {
	r := contractRegistry(t)

	examples := eventExamplePaths(t)
	if len(examples) == 0 {
		t.Fatal("no event examples found under contracts examples/events — the embedded FS is broken")
	}

	for _, p := range examples {
		t.Run(p, func(t *testing.T) {
			raw := readFile(t, p)

			// Stage 1+2 over the raw wire document.
			if err := r.ValidateJSON(raw); err != nil {
				t.Fatalf("ValidateJSON: %v", err)
			}

			env, err := r.Decode(raw)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}

			// The same document, validated through the typed envelope.
			if err := r.Validate(env); err != nil {
				t.Fatalf("Validate(decoded): %v", err)
			}

			if !r.Has(env.EventType, env.EventVersion) {
				t.Errorf("Has(%s, %d) = false for an example that validates", env.EventType, env.EventVersion)
			}
			if got := env.OccurredAt.Location().String(); got != "UTC" {
				t.Errorf("occurredAt decoded in %s, want UTC", got)
			}

			// Round trip: re-encoding the decoded envelope must reproduce the
			// document. Anything the ten fields fail to carry shows up here.
			reencoded, err := json.Marshal(env)
			if err != nil {
				t.Fatalf("re-marshalling the decoded envelope: %v", err)
			}
			if before, after := decode(t, raw), decode(t, reencoded); !reflect.DeepEqual(before, after) {
				t.Errorf("decode/encode round trip changed the document:\nbefore: %s\nafter:  %s", raw, reencoded)
			}
		})
	}
}

// TestEveryContractSchemaIsRegistered guards the other direction: a payload
// schema the registry failed to load would silently make every event of that
// type an ErrUnknownEvent at runtime.
func TestEveryContractSchemaIsRegistered(t *testing.T) {
	r := contractRegistry(t)

	var want int
	for _, p := range walk(t, "schemas/events") {
		if strings.HasSuffix(p, ".json") {
			want++
		}
	}
	if got := len(r.Keys()); got != want {
		t.Errorf("registry holds %d payload schemas, contracts ship %d", got, want)
	}
	if !r.Has("CaseCreated", 1) {
		t.Error("Has(CaseCreated, 1) = false")
	}
	for i, k := range r.Keys() {
		if k.EventType == "" || k.Version < 1 {
			t.Errorf("Keys()[%d] = %+v, want a named type at version >= 1", i, k)
		}
	}
	// Keys are sorted, which is what makes startup logs and test fixtures
	// comparable across runs.
	keys := r.Keys()
	for i := 1; i < len(keys); i++ {
		if keys[i-1].EventType > keys[i].EventType {
			t.Fatalf("Keys() is not sorted: %s before %s", keys[i-1], keys[i])
		}
	}
}

func TestValidateRejectsBadDocuments(t *testing.T) {
	r := contractRegistry(t)
	base := readFile(t, "examples/events/case/CaseCreated.v1.example.json")

	tests := []struct {
		name    string
		mutate  func(m map[string]any)
		wantErr error
	}{
		{
			name: "payload missing a required field",
			mutate: func(m map[string]any) {
				delete(m["payload"].(map[string]any), "status")
			},
			wantErr: events.ErrSchemaViolation,
		},
		{
			name: "payload with an unknown field (additionalProperties: false)",
			mutate: func(m map[string]any) {
				m["payload"].(map[string]any)["tenantId"] = "T1"
			},
			wantErr: events.ErrSchemaViolation,
		},
		{
			name: "payload field of the wrong type",
			mutate: func(m map[string]any) {
				m["payload"].(map[string]any)["priority"] = "high"
			},
			wantErr: events.ErrSchemaViolation,
		},
		{
			name: "payload enum value outside the state machine",
			mutate: func(m map[string]any) {
				m["payload"].(map[string]any)["status"] = "PENDING"
			},
			wantErr: events.ErrSchemaViolation,
		},
		{
			name:    "unknown event type",
			mutate:  func(m map[string]any) { m["eventType"] = "CaseTeleported" },
			wantErr: events.ErrUnknownEvent,
		},
		{
			name:    "known type at an unknown version",
			mutate:  func(m map[string]any) { m["eventVersion"] = 99 },
			wantErr: events.ErrUnknownEvent,
		},
		{
			name:    "envelope field missing",
			mutate:  func(m map[string]any) { delete(m, "correlationId") },
			wantErr: events.ErrSchemaViolation,
		},
		{
			name:    "envelope identifier is not a ULID",
			mutate:  func(m map[string]any) { m["eventId"] = "not-a-ulid" },
			wantErr: events.ErrSchemaViolation,
		},
		{
			name:    "occurredAt carries a local offset",
			mutate:  func(m map[string]any) { m["occurredAt"] = "2026-08-22T10:00:00+01:00" },
			wantErr: events.ErrSchemaViolation,
		},
		{
			name:    "causationId sent as null instead of omitted",
			mutate:  func(m map[string]any) { m["causationId"] = nil },
			wantErr: events.ErrSchemaViolation,
		},
		{
			name:    "unknown top-level field",
			mutate:  func(m map[string]any) { m["tenantId"] = "T1" },
			wantErr: events.ErrSchemaViolation,
		},
		{
			name:    "payload is not an object",
			mutate:  func(m map[string]any) { m["payload"] = "opaque" },
			wantErr: events.ErrSchemaViolation,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := decodeObject(t, base)
			tc.mutate(doc)

			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatalf("marshalling the mutated document: %v", err)
			}
			err = r.ValidateJSON(raw)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateJSON error = %v, want %v", err, tc.wantErr)
			}
			if _, err := r.Decode(raw); !errors.Is(err, tc.wantErr) {
				t.Errorf("Decode error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestValidateJSONCatchesWhatValidateCannot pins the reason ValidateJSON exists:
// decoding into the Go struct drops unknown top-level fields, so a document with
// an extra field passes Validate but must fail ValidateJSON.
func TestValidateJSONCatchesWhatValidateCannot(t *testing.T) {
	r := contractRegistry(t)

	doc := decodeObject(t, readFile(t, "examples/events/case/CaseCreated.v1.example.json"))
	doc["tenantId"] = "T1"
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	if err := r.ValidateJSON(raw); !errors.Is(err, events.ErrSchemaViolation) {
		t.Errorf("ValidateJSON accepted an extra top-level field: %v", err)
	}

	var env events.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}
	if err := r.Validate(env); err != nil {
		t.Errorf("Validate on the decoded envelope = %v, want nil (the extra field is dropped by the decoder)", err)
	}
}

// TestValidateRejectsTheEnvelopeIllustration proves the second validation stage
// really runs. The contracts ship examples/envelope/EventEnvelope.v1.example.json
// as an illustration of envelope *shape*: it is valid against the envelope
// schema, but its payload is a sketch rather than a real CaseCreated body, so
// full validation must reject it.
func TestValidateRejectsTheEnvelopeIllustration(t *testing.T) {
	r := contractRegistry(t)

	raw := readFile(t, "examples/envelope/EventEnvelope.v1.example.json")
	if err := r.ValidateJSON(raw); !errors.Is(err, events.ErrSchemaViolation) {
		t.Fatalf("ValidateJSON error = %v, want a payload schema violation", err)
	}
}

func TestValidateJSONRejectsMalformedInput(t *testing.T) {
	r := contractRegistry(t)

	tests := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"truncated", `{"eventId":"01M0KK5FG0RG0GVH3G6KY89V93"`},
		{"not JSON at all", `<xml/>`},
		{"a JSON array", `[{"eventType":"CaseCreated"}]`},
		{"a bare string", `"CaseCreated"`},
		{"two documents concatenated", `{"eventType":"CaseCreated"} {"eventType":"CaseCreated"}`},
		{"eventVersion as a string", `{"eventType":"CaseCreated","eventVersion":"1","payload":{}}`},
		{"eventVersion missing", `{"eventType":"CaseCreated","payload":{}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := r.ValidateJSON([]byte(tc.raw)); err == nil {
				t.Fatal("ValidateJSON accepted a malformed document")
			}
		})
	}
}

func TestNewRegistryRejectsBrokenContractFilesystems(t *testing.T) {
	const envelopePath = "schemas/envelope/EventEnvelope.v1.json"

	envelope := readFile(t, envelopePath)
	caseCreated := readFile(t, "schemas/events/case/CaseCreated.v1.json")

	tests := []struct {
		name    string
		fsys    fs.FS
		wantErr string
	}{
		{
			name:    "no FS at all",
			fsys:    nil,
			wantErr: "no contracts FS",
		},
		{
			name:    "empty FS",
			fsys:    fstest.MapFS{},
			wantErr: "compiling the event envelope schema",
		},
		{
			name: "envelope present but no payload schemas",
			fsys: fstest.MapFS{
				envelopePath: &fstest.MapFile{Data: envelope},
			},
			wantErr: "none found",
		},
		{
			name: "payload schema misnamed (no version)",
			fsys: fstest.MapFS{
				envelopePath:                           &fstest.MapFile{Data: envelope},
				"schemas/events/case/CaseCreated.json": &fstest.MapFile{Data: caseCreated},
			},
			wantErr: "named <EventType>.v<N>.json",
		},
		{
			name: "payload schema version is not an integer",
			fsys: fstest.MapFS{
				envelopePath: &fstest.MapFile{Data: envelope},
				"schemas/events/case/CaseCreated.vX.json": &fstest.MapFile{Data: caseCreated},
			},
			wantErr: "not an integer",
		},
		{
			name: "payload schema version is zero",
			fsys: fstest.MapFS{
				envelopePath: &fstest.MapFile{Data: envelope},
				"schemas/events/case/CaseCreated.v0.json": &fstest.MapFile{Data: caseCreated},
			},
			wantErr: "below 1",
		},
		{
			name: "the same event type declared in two contexts",
			fsys: fstest.MapFS{
				envelopePath: &fstest.MapFile{Data: envelope},
				"schemas/events/case/CaseCreated.v1.json":  &fstest.MapFile{Data: caseCreated},
				"schemas/events/legal/CaseCreated.v1.json": &fstest.MapFile{Data: caseCreated},
			},
			wantErr: "declared twice",
		},
		{
			name: "payload schema does not compile",
			fsys: fstest.MapFS{
				envelopePath: &fstest.MapFile{Data: envelope},
				"schemas/events/case/CaseCreated.v1.json": &fstest.MapFile{
					Data: []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":17}`),
				},
			},
			wantErr: "compiling payload schema",
		},
		{
			name: "payload schema refs something outside the contracts FS",
			fsys: fstest.MapFS{
				envelopePath: &fstest.MapFile{Data: envelope},
				"schemas/events/case/CaseCreated.v1.json": &fstest.MapFile{
					Data: []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$ref":"https://example.com/evil.json"}`),
				},
			},
			wantErr: "compiling payload schema",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := events.NewRegistry(tc.fsys)
			if err == nil {
				t.Fatal("NewRegistry accepted a broken contracts FS")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestNewRegistryRefusesNetworkReferences proves the loader is hermetic: a
// schema that $refs an off-FS URL fails to compile rather than reaching out.
func TestNewRegistryRefusesNetworkReferences(t *testing.T) {
	envelope := readFile(t, "schemas/envelope/EventEnvelope.v1.json")

	_, err := events.NewRegistry(fstest.MapFS{
		"schemas/envelope/EventEnvelope.v1.json": &fstest.MapFile{Data: envelope},
		"schemas/events/case/Case.v1.json": &fstest.MapFile{
			Data: []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$ref":"https://contracts.collections.internal/schemas/events/case/Missing.v1.json"}`),
		},
	})
	if err == nil {
		t.Fatal("NewRegistry resolved a $ref that is not in the FS")
	}
	if !strings.Contains(err.Error(), "not in the contracts FS") {
		t.Errorf("error = %q, want it to name the missing FS entry", err)
	}
}

func TestKeyString(t *testing.T) {
	tests := []struct {
		key  events.Key
		want string
	}{
		{events.Key{EventType: "CaseCreated", Version: 1}, "CaseCreated.v1"},
		{events.Key{EventType: "PaymentReceived", Version: 2}, "PaymentReceived.v2"},
		{events.Key{EventType: "DelinquencyChanged", Version: 10}, "DelinquencyChanged.v10"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.key.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------- helpers ----

func eventExamplePaths(t *testing.T) []string {
	t.Helper()

	var out []string
	for _, p := range walk(t, "examples/events") {
		if strings.HasSuffix(p, ".example.json") {
			out = append(out, p)
		}
	}
	return out
}

func walk(t *testing.T, root string) []string {
	t.Helper()

	var out []string
	err := fs.WalkDir(contracts.FS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s in contracts.FS: %v", root, err)
	}
	return out
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()

	b, err := fs.ReadFile(contracts.FS, p)
	if err != nil {
		t.Fatalf("reading %s from contracts.FS: %v", p, err)
	}
	return b
}

// decode parses a JSON document preserving numeric literals, so a round-trip
// comparison is not confused by float formatting.
func decode(t *testing.T, raw []byte) any {
	t.Helper()

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	return v
}

func decodeObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()

	obj, ok := decode(t, raw).(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON object, got %s", raw)
	}
	return obj
}
