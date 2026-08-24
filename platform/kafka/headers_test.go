package kafka

import (
	"slices"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
)

func TestHeaderMap(t *testing.T) {
	tests := []struct {
		name    string
		headers []kgo.RecordHeader
		want    map[string]string
	}{
		{name: "no headers", headers: nil, want: nil},
		{
			name: "keys are lowercased for otelkit",
			headers: []kgo.RecordHeader{
				{Key: "Traceparent", Value: []byte("00-abc-def-01")},
				{Key: "X-Correlation-Id", Value: []byte("01M0KK4P3G0MQSQ3A1X2PMA6VX")},
			},
			want: map[string]string{
				"traceparent":      "00-abc-def-01",
				"x-correlation-id": "01M0KK4P3G0MQSQ3A1X2PMA6VX",
			},
		},
		{
			name: "a repeated key keeps the last value",
			headers: []kgo.RecordHeader{
				{Key: "x-error", Value: []byte("first")},
				{Key: "x-error", Value: []byte("second")},
			},
			want: map[string]string{"x-error": "second"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := headerMap(tc.headers)
			if len(got) != len(tc.want) {
				t.Fatalf("headerMap = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("header %q = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

// TestToRecordHeadersIsSorted matters more than it looks: a record's header
// order is preserved verbatim on the wire, so leaving it to Go's randomised map
// iteration would make two publications of the same logical message
// byte-different — and the outbox's content hashes depend on that not happening.
func TestToRecordHeadersIsSorted(t *testing.T) {
	headers := map[string]string{
		"x-correlation-id": "COR",
		"traceparent":      "TP",
		"x-causation-id":   "CAU",
		"baggage":          "BAG",
	}

	first := toRecordHeaders(headers)
	keys := make([]string, 0, len(first))
	for _, h := range first {
		keys = append(keys, h.Key)
	}
	if !slices.IsSorted(keys) {
		t.Fatalf("headers are not sorted: %v", keys)
	}

	// Run it again: a randomised map iteration would show up here, not above.
	for range 20 {
		again := toRecordHeaders(headers)
		for i := range again {
			if again[i].Key != first[i].Key || string(again[i].Value) != string(first[i].Value) {
				t.Fatalf("header order is unstable: %v then %v", first, again)
			}
		}
	}

	if toRecordHeaders(nil) != nil {
		t.Error("an empty map should produce no headers, not an empty slice")
	}
}

// TestPreserveHeaders covers the DLQ replay case: a record that comes back out
// of the DLQ, fails again and is dead-lettered again must report where it came
// from *this* time, not accumulate a history of origin headers.
func TestPreserveHeaders(t *testing.T) {
	original := []kgo.RecordHeader{
		{Key: "traceparent", Value: []byte("00-abc-def-01")},
		{Key: "x-correlation-id", Value: []byte("COR")},
		{Key: HeaderOriginTopic, Value: []byte("collections.delinquency")},
		{Key: "X-Origin-Offset", Value: []byte("41")},
		{Key: HeaderError, Value: []byte("an older failure")},
	}

	got := preserveHeaders(original)

	for _, h := range got {
		if slices.Contains(originHeaders, h.Key) || h.Key == "X-Origin-Offset" {
			t.Errorf("origin header %q survived; the new one would be ambiguous", h.Key)
		}
	}
	if len(got) != 2 {
		t.Fatalf("kept %d headers (%v), want the two propagation headers", len(got), got)
	}
}
