package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	// schemaBaseURL is the $id prefix of every contract schema. It is a naming
	// authority, not a reachable address: the loader resolves it against the
	// supplied fs.FS and refuses everything else, so validation can never make
	// a network call (contracts/README §2).
	schemaBaseURL = "https://contracts.collections.internal/"

	// envelopeSchemaPath is the normative A§24 envelope schema.
	envelopeSchemaPath = "schemas/envelope/EventEnvelope.v1.json"

	// eventSchemaRoot is the directory holding one payload schema per
	// (eventType, eventVersion): schemas/events/<context>/<Type>.v<N>.json.
	eventSchemaRoot = "schemas/events"

	schemaSuffix = ".json"
	versionMark  = ".v"
)

// ErrUnknownEvent reports that the registry holds no payload schema for an
// event's (eventType, eventVersion) pair.
//
// Consumers must treat it as "not mine" rather than "broken": contracts/README
// §13 requires them to ignore unknown event types and unknown versions so a new
// event can be rolled out before every consumer knows about it. Check it with
// errors.Is before routing an event to the DLQ.
var ErrUnknownEvent = errors.New("unknown event type or version")

// ErrSchemaViolation reports that a document failed its JSON Schema. The
// wrapped error carries the offending instance locations.
var ErrSchemaViolation = errors.New("schema violation")

// Key identifies one payload contract: an event type at a major version. The
// meaning of a Key never changes — a breaking payload change is a new Version
// (D§29).
type Key struct {
	EventType string
	Version   int
}

// String renders the key the way the contracts name it: CaseCreated.v1.
func (k Key) String() string { return k.EventType + versionMark + strconv.Itoa(k.Version) }

// Registry is the compiled set of contract schemas an event is validated
// against: the envelope plus one payload schema per Key. It is built once at
// startup from the embedded contracts FS and is immutable afterwards, so it is
// safe for concurrent use by every producer and consumer in a service.
type Registry struct {
	envelope *jsonschema.Schema
	payloads map[Key]*jsonschema.Schema
}

// NewRegistry compiles the envelope schema and every payload schema under
// schemas/events/ from fsys — in practice contracts.FS.
//
// It fails when the envelope schema is absent, when a schema does not compile,
// when a file under schemas/events/ is not named <EventType>.v<N>.json, or when
// two contexts claim the same (eventType, version): the pair is how a consumer
// finds a payload schema from an envelope alone, so an ambiguous pair would make
// validation depend on directory walk order.
func NewRegistry(fsys fs.FS) (*Registry, error) {
	if fsys == nil {
		return nil, errors.New("compiling the event registry: no contracts FS supplied")
	}

	c := jsonschema.NewCompiler()
	c.UseLoader(embeddedLoader{fsys: fsys})
	// Assert `format: date-time` instead of merely annotating it, so an
	// occurredAt that is not a real timestamp fails validation.
	c.AssertFormat()

	envelope, err := c.Compile(schemaBaseURL + envelopeSchemaPath)
	if err != nil {
		return nil, fmt.Errorf("compiling the event envelope schema %s: %w", envelopeSchemaPath, err)
	}

	payloads := make(map[Key]*jsonschema.Schema)
	origin := make(map[Key]string)
	walkErr := fs.WalkDir(fsys, eventSchemaRoot, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir(), !strings.HasSuffix(p, schemaSuffix):
			return nil
		}

		key, err := keyFromSchemaPath(p)
		if err != nil {
			return err
		}
		if first, dup := origin[key]; dup {
			return fmt.Errorf(
				"%s is declared twice: %s and %s — (eventType, eventVersion) must locate exactly one payload schema",
				key, first, p)
		}
		sch, err := c.Compile(schemaBaseURL + p)
		if err != nil {
			return fmt.Errorf("compiling payload schema %s: %w", p, err)
		}
		payloads[key], origin[key] = sch, p
		return nil
	})
	// A missing schemas/events directory is the same failure as an empty one —
	// the "none found" message below says so more usefully than an open error.
	if walkErr != nil && !errors.Is(walkErr, fs.ErrNotExist) {
		return nil, fmt.Errorf("loading payload schemas from %s: %w", eventSchemaRoot, walkErr)
	}
	if len(payloads) == 0 {
		return nil, fmt.Errorf("loading payload schemas from %s: none found — the contracts FS is empty or misrooted", eventSchemaRoot)
	}
	return &Registry{envelope: envelope, payloads: payloads}, nil
}

// keyFromSchemaPath derives the (eventType, version) key from a payload schema
// path: schemas/events/case/CaseCreated.v1.json -> CaseCreated v1.
func keyFromSchemaPath(p string) (Key, error) {
	base := strings.TrimSuffix(path.Base(p), schemaSuffix)

	i := strings.LastIndex(base, versionMark)
	if i <= 0 {
		return Key{}, fmt.Errorf("%s: payload schemas are named <EventType>.v<N>%s", p, schemaSuffix)
	}
	version, err := strconv.Atoi(base[i+len(versionMark):])
	if err != nil {
		return Key{}, fmt.Errorf("%s: version in the file name is not an integer: %w", p, err)
	}
	if version < 1 {
		return Key{}, fmt.Errorf("%s: version %d is below 1", p, version)
	}
	return Key{EventType: base[:i], Version: version}, nil
}

// embeddedLoader resolves canonical contract URLs against an fs.FS so
// cross-file $refs (every payload schema $refs the envelope's ULID definition)
// work with no network access. Any other URL is refused.
type embeddedLoader struct{ fsys fs.FS }

// Load implements jsonschema.URLLoader.
func (l embeddedLoader) Load(rawURL string) (any, error) {
	rel, ok := strings.CutPrefix(rawURL, schemaBaseURL)
	if !ok {
		return nil, fmt.Errorf(
			"refusing to load %q: contract schemas are self-contained, only %s* URLs resolve",
			rawURL, schemaBaseURL)
	}
	f, err := l.fsys.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("%q is not in the contracts FS at %q: %w", rawURL, rel, err)
	}
	defer func() { _ = f.Close() }()
	return jsonschema.UnmarshalJSON(f)
}

// Has reports whether the registry holds a payload schema for this event type
// and version. Use it to decide whether an event is one this service knows
// about before doing any work on it.
func (r *Registry) Has(eventType string, version int) bool {
	_, ok := r.payloads[Key{EventType: eventType, Version: version}]
	return ok
}

// Keys returns every (eventType, version) pair the registry knows, sorted by
// type then version. Useful for startup logging and for tests that assert a
// service can validate everything it subscribes to.
func (r *Registry) Keys() []Key {
	keys := make([]Key, 0, len(r.payloads))
	for k := range r.payloads {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b Key) int {
		if c := strings.Compare(a.EventType, b.EventType); c != 0 {
			return c
		}
		return a.Version - b.Version
	})
	return keys
}

// Validate checks env against the envelope schema and its payload against the
// schema for (eventType, eventVersion).
//
// This is the gate outbox.Enqueue runs before a row is written, so an invalid
// event can never reach the broker. An unknown (type, version) is
// ErrUnknownEvent; a document that fails a schema is ErrSchemaViolation.
func (r *Registry) Validate(env Envelope) error {
	doc, err := envelopeDocument(env)
	if err != nil {
		return err
	}
	return r.validateDocument(doc, env.EventType, env.EventVersion)
}

// ValidateJSON checks a raw envelope document — a Kafka message value, an outbox
// row, a DLQ record — against the envelope schema and its payload schema.
//
// Prefer it over Validate on anything that arrived from outside the process:
// decoding into Envelope silently drops unknown top-level fields, so only the
// raw document proves the envelope carries exactly the ten A§24 fields.
func (r *Registry) ValidateJSON(doc []byte) error {
	parsed, err := decodeDocument(doc)
	if err != nil {
		return err
	}
	obj, ok := parsed.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: an event envelope must be a JSON object, got %s", ErrSchemaViolation, describeJSON(doc))
	}

	eventType, _ := obj["eventType"].(string)
	version, versionOK := jsonInt(obj["eventVersion"])
	if !versionOK {
		// The envelope schema reports the real problem (missing or non-integer
		// eventVersion) with a proper instance location.
		if err := r.envelope.Validate(parsed); err != nil {
			return fmt.Errorf("%w: envelope: %w", ErrSchemaViolation, err)
		}
		return fmt.Errorf("%w: eventVersion is missing or not an integer", ErrSchemaViolation)
	}
	return r.validateDocument(parsed, eventType, version)
}

// Decode validates a raw envelope document and then decodes it. It is the one
// entry point a consumer needs: nothing reaches business code that has not
// satisfied both schemas.
func (r *Registry) Decode(doc []byte) (Envelope, error) {
	if err := r.ValidateJSON(doc); err != nil {
		return Envelope{}, err
	}
	var env Envelope
	if err := json.Unmarshal(doc, &env); err != nil {
		return Envelope{}, fmt.Errorf("decoding a schema-valid envelope: %w", err)
	}
	return env, nil
}

// validateDocument runs the two-stage check over an already-parsed document.
func (r *Registry) validateDocument(doc any, eventType string, version int) error {
	if err := r.envelope.Validate(doc); err != nil {
		return fmt.Errorf("%w: %s v%d against %s: %w", ErrSchemaViolation, eventType, version, envelopeSchemaPath, err)
	}

	key := Key{EventType: eventType, Version: version}
	payloadSchema, ok := r.payloads[key]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownEvent, key)
	}

	obj, ok := doc.(map[string]any)
	if !ok {
		return fmt.Errorf("%w: an event envelope must be a JSON object", ErrSchemaViolation)
	}
	payload, ok := obj["payload"]
	if !ok {
		return fmt.Errorf("%w: %s carries no payload", ErrSchemaViolation, key)
	}
	if err := payloadSchema.Validate(payload); err != nil {
		return fmt.Errorf("%w: %s payload: %w", ErrSchemaViolation, key, err)
	}
	return nil
}

// envelopeDocument renders env the way it appears on the wire and re-parses it,
// so the validator sees exactly the document a consumer would (json.Number for
// every number, RFC3339 strings for every timestamp).
func envelopeDocument(env Envelope) (any, error) {
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshalling %s for validation: %w", env.Key(), err)
	}
	return decodeDocument(raw)
}

// decodeDocument parses one JSON document, preserving numeric literals and
// refusing trailing content — a message value with a second document appended
// is malformed, not merely odd.
func decodeDocument(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: not valid JSON: %w", ErrSchemaViolation, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: trailing content after the JSON document", ErrSchemaViolation)
	}
	return doc, nil
}

// jsonInt reads an integer from a decoded JSON value, accepting the json.Number
// that UseNumber produces as well as a plain float64 or int.
func jsonInt(v any) (int, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := strconv.Atoi(n.String())
		return i, err == nil
	case float64:
		i := int(n)
		return i, float64(i) == n
	case int:
		return n, true
	default:
		return 0, false
	}
}
