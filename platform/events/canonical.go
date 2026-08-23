package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

// MarshalCanonical renders env as canonical JSON: every object's keys sorted
// lexicographically, at every depth, including inside the payload, with no
// insignificant whitespace.
//
// It exists so a hash of an event is stable. Go's map iteration order is
// randomised and a payload that arrived as JSON keeps its author's key order, so
// hashing the ordinary encoding would make the same fact hash differently on
// every process — useless for outbox deduplication, content hashes on published
// strategy documents, and golden fixtures.
//
// Numbers are preserved verbatim rather than renormalised (RFC 8785 would
// reformat them through a float64, which is lossy above 2^53 — and money on this
// platform is int64 minor units). Two documents that differ only in numeric
// spelling therefore hash differently; both would have to come from the same
// producer for that to matter, and producers go through New.
//
// The output is a hashing input, not a wire format: publish json.Marshal(env).
func MarshalCanonical(env Envelope) ([]byte, error) {
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshalling %s: %w", env.Key(), err)
	}
	doc, err := decodeDocument(raw)
	if err != nil {
		return nil, fmt.Errorf("re-parsing %s for canonicalisation: %w", env.Key(), err)
	}

	var buf bytes.Buffer
	buf.Grow(len(raw))
	if err := writeCanonical(&buf, doc); err != nil {
		return nil, fmt.Errorf("canonicalising %s: %w", env.Key(), err)
	}
	return buf.Bytes(), nil
}

// writeCanonical emits v in canonical form. Every JSON kind a decoded document
// can hold is handled explicitly; anything else is a programming error in the
// decoder, not user input.
func writeCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case map[string]any:
		buf.WriteByte('{')
		for i, k := range slices.Sorted(maps.Keys(t)) {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonicalString(buf, k); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeCanonical(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil

	case []any:
		buf.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil

	case json.Number:
		buf.WriteString(t.String())
		return nil

	case string:
		return writeCanonicalString(buf, t)

	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
		return nil

	case nil:
		buf.WriteString("null")
		return nil

	default:
		return fmt.Errorf("unexpected %T in a decoded JSON document", v)
	}
}

// writeCanonicalString writes s as a JSON string using encoding/json's escaping,
// which is deterministic for a given input — the only property canonical form
// needs.
func writeCanonicalString(buf *bytes.Buffer, s string) error {
	encoded, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("encoding string: %w", err)
	}
	buf.Write(encoded)
	return nil
}
