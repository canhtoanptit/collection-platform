// Package contracts_test is the permanent self-validation harness for the
// contract artefacts in this module (CON-1). It is deliberately dependency-light
// (only santhosh-tekuri/jsonschema/v6, the same validator services use at
// runtime per plan §2) and hermetic: every schema reference is resolved from the
// embedded FS, never over the network.
//
// Three rules are enforced here, and every later contracts work package
// (CON-2/4/6, DEC-1) inherits them:
//
//  1. Compile rule — every schemas/**/*.json compiles as JSON Schema 2020-12,
//     and its $id is exactly "https://contracts.collections.internal/<path in
//     this module>". Cross-file $refs resolve through that $id, so a wrong $id
//     silently breaks every consumer: it is a test failure here.
//
//  2. Mirror rule — examples/<p>/<Name>.v<N>.example.json is validated against
//     schemas/<p>/<Name>.v<N>.json. An example with no mirrored schema fails
//     (orphan guard), as does any *.json under examples/ that is not named
//     *.example.json.
//
//  3. Event rule — examples/events/** are envelope-wrapped events: the whole
//     document is validated against the event envelope schema AND its `payload`
//     against the mirrored payload schema, with eventType/eventVersion required
//     to match the file name. Conversely every schemas/events/**/*.json must
//     ship an example (see TestEveryEventSchemaShipsAnExample).
package contracts_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/canhtoanptit/collection-platform/contracts"
)

const (
	// schemaBaseURL is the $id prefix of every schema in this module. It is a
	// naming authority, not a reachable address: embeddedLoader resolves it
	// against contracts.FS and refuses everything else.
	schemaBaseURL = "https://contracts.collections.internal/"

	// draft2020 is the only JSON Schema dialect this module uses.
	draft2020 = "https://json-schema.org/draft/2020-12/schema"

	// envelopeSchemaPath is the normative event envelope (A§24).
	envelopeSchemaPath = "schemas/envelope/EventEnvelope.v1.json"

	// envelopeExamplePath is the golden envelope document.
	envelopeExamplePath = "examples/envelope/EventEnvelope.v1.example.json"

	exampleSuffix = ".example.json"
	schemaSuffix  = ".json"

	eventsRoot         = "events"
	schemasDir         = "schemas"
	examplesDir        = "examples"
	schemaEventsPrefix = schemasDir + "/" + eventsRoot + "/"
	exampleEventPrefix = examplesDir + "/" + eventsRoot + "/"
)

// embeddedLoader resolves canonical contract URLs against the embedded FS so
// that cross-file $refs (later schemas will $ref the envelope) work without
// network access. Any other URL is refused: contracts must be self-contained.
type embeddedLoader struct{}

func (embeddedLoader) Load(rawURL string) (any, error) {
	rel, ok := strings.CutPrefix(rawURL, schemaBaseURL)
	if !ok {
		return nil, fmt.Errorf(
			"refusing to load %q: contracts are self-contained, only %s* URLs resolve",
			rawURL, schemaBaseURL)
	}
	f, err := contracts.FS.Open(rel)
	if err != nil {
		return nil, fmt.Errorf("%q is not in contracts.FS at %q: %w", rawURL, rel, err)
	}
	defer func() { _ = f.Close() }()
	return jsonschema.UnmarshalJSON(f)
}

// newCompiler returns a compiler wired to the embedded FS with format
// assertion on, so `format: date-time` is checked rather than annotated.
func newCompiler() *jsonschema.Compiler {
	c := jsonschema.NewCompiler()
	c.UseLoader(embeddedLoader{})
	c.AssertFormat()
	return c
}

func TestSchemasCompile(t *testing.T) {
	paths := walkJSON(t, schemasDir, schemaSuffix)
	if len(paths) == 0 {
		t.Fatalf("no schemas found under %s/ — the embedded FS is broken", schemasDir)
	}

	c := newCompiler()
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			doc := readObject(t, p)

			if got, want := doc["$id"], schemaBaseURL+p; got != want {
				t.Errorf("$id = %v, want %q\n"+
					"convention: $id is %s<path in the contracts module>; the loader and "+
					"every cross-file $ref resolve through it", got, want, schemaBaseURL)
			}
			if got := doc["$schema"]; got != draft2020 {
				t.Errorf("$schema = %v, want %q (2020-12 is the only dialect in this module)", got, draft2020)
			}
			if _, err := c.Compile(schemaBaseURL + p); err != nil {
				t.Fatalf("does not compile as JSON Schema 2020-12: %v", err)
			}
		})
	}
}

func TestExamplesValidateAgainstSchemas(t *testing.T) {
	for _, p := range walkJSON(t, examplesDir, schemaSuffix) {
		if !strings.HasSuffix(p, exampleSuffix) {
			t.Errorf("%s: every JSON file under %s/ must be named <Name>.v<N>%s (mirror rule)",
				p, examplesDir, exampleSuffix)
		}
	}

	examples := walkJSON(t, examplesDir, exampleSuffix)
	if len(examples) == 0 {
		t.Fatalf("no examples found under %s/ — at least the envelope example must exist", examplesDir)
	}

	c := newCompiler()
	for _, ex := range examples {
		t.Run(ex, func(t *testing.T) {
			schemaPath := mirroredSchemaPath(ex)
			if !existsInFS(schemaPath) {
				t.Fatalf("orphan example: no schema at %s\n"+
					"mirror rule: %s<p>/<Name>.v<N>%s -> %s/<p>/<Name>.v<N>%s",
					schemaPath, examplesDir+"/", exampleSuffix, schemasDir, schemaSuffix)
			}

			doc := readObject(t, ex)
			if !strings.HasPrefix(ex, exampleEventPrefix) {
				if err := mustCompile(t, c, schemaPath).Validate(doc); err != nil {
					t.Errorf("does not validate against %s:\n%v", schemaPath, err)
				}
				return
			}

			// Event rule: the example is an envelope-wrapped event.
			if err := mustCompile(t, c, envelopeSchemaPath).Validate(doc); err != nil {
				t.Errorf("does not validate against the event envelope %s:\n%v", envelopeSchemaPath, err)
			}
			assertEventNaming(t, ex, doc)

			payload, ok := doc["payload"]
			if !ok {
				t.Fatal("envelope-wrapped event example has no `payload` to validate")
			}
			if err := mustCompile(t, c, schemaPath).Validate(payload); err != nil {
				t.Errorf("`payload` does not validate against %s:\n%v", schemaPath, err)
			}
		})
	}
}

// TestEveryEventSchemaShipsAnExample is the reverse orphan guard: a payload
// schema with no golden example is a contract nobody has ever exercised, so it
// fails the build (plan CON-2 discipline).
func TestEveryEventSchemaShipsAnExample(t *testing.T) {
	schemas := walkJSON(t, schemaEventsPrefix, schemaSuffix)
	if len(schemas) == 0 {
		t.Logf("no event payload schemas yet — CON-2 adds %s<context>/<EventType>.v<N>%s",
			schemaEventsPrefix, schemaSuffix)
		return
	}
	for _, s := range schemas {
		want := examplesDir + "/" + strings.TrimSuffix(strings.TrimPrefix(s, schemasDir+"/"), schemaSuffix) + exampleSuffix
		if !existsInFS(want) {
			t.Errorf("event schema %s ships no example: create %s (envelope-wrapped, payload per this schema)", s, want)
		}
	}
}

// TestEnvelopeSchemaRejectsInvalidDocuments pins the normative envelope
// decisions (A§24): exactly ten fields, all required except causationId, ULID
// identifiers, UTC timestamps, kebab-case producer, PascalCase types.
func TestEnvelopeSchemaRejectsInvalidDocuments(t *testing.T) {
	sch := mustCompile(t, newCompiler(), envelopeSchemaPath)
	base := readObject(t, envelopeExamplePath)

	tests := []struct {
		name      string
		mutate    func(m map[string]any)
		wantValid bool
	}{
		{"golden example", func(map[string]any) {}, true},
		{"causationId omitted (first event in a chain)", func(m map[string]any) { delete(m, "causationId") }, true},
		{"payload empty object", func(m map[string]any) { m["payload"] = map[string]any{} }, true},

		{"causationId null instead of omitted", func(m map[string]any) { m["causationId"] = nil }, false},
		{"unknown field", func(m map[string]any) { m["tenantId"] = "T1" }, false},
		{"eventId missing", func(m map[string]any) { delete(m, "eventId") }, false},
		{"correlationId missing", func(m map[string]any) { delete(m, "correlationId") }, false},
		{"payload missing", func(m map[string]any) { delete(m, "payload") }, false},
		{"eventId not a ULID", func(m map[string]any) { m["eventId"] = "not-a-ulid" }, false},
		{"eventId uses excluded letter L", func(m map[string]any) { m["eventId"] = "01M0MEKD80M9S346Q3D25VT4FL" }, false},
		{"eventType not PascalCase", func(m map[string]any) { m["eventType"] = "case_created" }, false},
		{"eventType carries a version suffix", func(m map[string]any) { m["eventType"] = "CaseCreated.v1" }, false},
		{"eventVersion zero", func(m map[string]any) { m["eventVersion"] = 0 }, false},
		{"eventVersion as string", func(m map[string]any) { m["eventVersion"] = "1" }, false},
		{"occurredAt with a local offset", func(m map[string]any) { m["occurredAt"] = "2026-08-22T10:00:00+01:00" }, false},
		{"occurredAt not a timestamp", func(m map[string]any) { m["occurredAt"] = "22/08/2026" }, false},
		{"producer not kebab-case", func(m map[string]any) { m["producer"] = "CaseService" }, false},
		{"aggregateType not PascalCase", func(m map[string]any) { m["aggregateType"] = "case" }, false},
		{"aggregateId empty", func(m map[string]any) { m["aggregateId"] = "" }, false},
		{"payload not an object", func(m map[string]any) { m["payload"] = "opaque" }, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := make(map[string]any, len(base)+1)
			for k, v := range base {
				doc[k] = v
			}
			tc.mutate(doc)

			err := sch.Validate(reparse(t, doc))
			switch {
			case tc.wantValid && err != nil:
				t.Errorf("want valid, got: %v", err)
			case !tc.wantValid && err == nil:
				t.Error("want invalid, but the envelope schema accepted the document")
			}
		})
	}
}

// mirroredSchemaPath applies the mirror rule:
// examples/<p>/<Name>.v<N>.example.json -> schemas/<p>/<Name>.v<N>.json.
func mirroredSchemaPath(examplePath string) string {
	rel := strings.TrimPrefix(examplePath, examplesDir+"/")
	return schemasDir + "/" + strings.TrimSuffix(rel, exampleSuffix) + schemaSuffix
}

// assertEventNaming ties an event example's file name to its envelope fields:
// <EventType>.v<N>.example.json must carry eventType <EventType> and
// eventVersion <N>, which is what lets a consumer find a payload schema from an
// envelope alone.
func assertEventNaming(t *testing.T, examplePath string, doc map[string]any) {
	t.Helper()

	base := path.Base(strings.TrimSuffix(examplePath, exampleSuffix))
	eventType, version, found := strings.Cut(base, ".v")
	if !found {
		t.Fatalf("event example must be named <EventType>.v<N>%s, got %q", exampleSuffix, path.Base(examplePath))
	}
	if _, err := strconv.Atoi(version); err != nil {
		t.Fatalf("event example version in %q is not an integer: %v", path.Base(examplePath), err)
	}
	if got := fmt.Sprint(doc["eventType"]); got != eventType {
		t.Errorf("eventType = %q, want %q (from the file name)", got, eventType)
	}
	if got := fmt.Sprint(doc["eventVersion"]); got != version {
		t.Errorf("eventVersion = %q, want %q (from the file name)", got, version)
	}
}

// walkJSON lists, in lexical order, every file under root whose name ends in
// suffix. A root that does not exist yet is not an error: later work packages
// create schemas/events, schemas/ingestion, schemas/decisioning and so on.
func walkJSON(t *testing.T, root, suffix string) []string {
	t.Helper()

	var out []string
	err := fs.WalkDir(contracts.FS, strings.TrimSuffix(root, "/"), func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir(), !strings.HasSuffix(p, suffix):
			return nil
		default:
			out = append(out, p)
			return nil
		}
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

func mustCompile(t *testing.T, c *jsonschema.Compiler, schemaPath string) *jsonschema.Schema {
	t.Helper()

	sch, err := c.Compile(schemaBaseURL + schemaPath)
	if err != nil {
		t.Fatalf("compile %s: %v", schemaPath, err)
	}
	return sch
}

// readObject reads an embedded JSON document that must be an object.
func readObject(t *testing.T, p string) map[string]any {
	t.Helper()

	f, err := contracts.FS.Open(p)
	if err != nil {
		t.Fatalf("open %s: %v", p, err)
	}
	defer func() { _ = f.Close() }()

	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatalf("%s is not valid JSON: %v", p, err)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("%s must contain a JSON object, got %T", p, doc)
	}
	return obj
}

// reparse round-trips a mutated document through JSON so the validator sees
// exactly what it would see on the wire (json.Number for every number).
func reparse(t *testing.T, v any) any {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal test document: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("re-parse test document: %v", err)
	}
	return doc
}

func existsInFS(p string) bool {
	f, err := contracts.FS.Open(p)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
