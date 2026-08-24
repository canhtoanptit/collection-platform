package kafka

import (
	"slices"
	"strings"

	"github.com/twmb/franz-go/pkg/kgo"
)

// DLQ headers (A§27). A dead-lettered record carries every header it arrived
// with, plus these four, so a replay knows where the record came from and an
// operator knows why it stopped — without opening the payload, which may hold
// personal data.
const (
	// HeaderOriginTopic is the topic the record was consumed from. The replay
	// endpoint re-produces to it (D§49).
	HeaderOriginTopic = "x-origin-topic"
	// HeaderOriginPartition is the partition the record came from, as a decimal
	// string.
	HeaderOriginPartition = "x-origin-partition"
	// HeaderOriginOffset is the offset the record had in that partition, as a
	// decimal string. Together with the topic and partition it identifies the
	// record exactly, which is what makes "did we already replay this" answerable.
	HeaderOriginOffset = "x-origin-offset"
	// HeaderError is why the record was dead-lettered: a schema violation, or
	// the last error the handler returned. It is a diagnostic string, not a
	// stable code — alerts key on the DLQ topic's depth, not on this.
	HeaderError = "x-error"
)

// originHeaders are the headers deadLetter sets itself. A record replayed out of
// the DLQ and dead-lettered again must report where it came from *this* time, so
// these are overwritten rather than duplicated.
var originHeaders = []string{
	HeaderOriginTopic, HeaderOriginPartition, HeaderOriginOffset, HeaderError,
}

// headerMap flattens record headers for otelkit, which speaks map[string]string.
//
// Kafka headers are a list and may repeat a key; the last occurrence wins, which
// matches how an HTTP-shaped propagation header is read. Keys are lowercased
// because another runtime's client may not agree on case and
// otelkit.ContextFromKafkaHeaders matches case-insensitively anyway.
func headerMap(headers []kgo.RecordHeader) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for _, h := range headers {
		out[strings.ToLower(h.Key)] = string(h.Value)
	}
	return out
}

// toRecordHeaders renders a header map onto the wire, sorted by key.
//
// Sorted, because a Kafka record's header order is preserved verbatim: leaving it
// to Go's randomised map iteration would make two publications of the same
// logical message byte-different, and the outbox's content hashes and the golden
// vectors in contracts/ both depend on that not happening.
func toRecordHeaders(headers map[string]string) []kgo.RecordHeader {
	if len(headers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	out := make([]kgo.RecordHeader, 0, len(keys))
	for _, k := range keys {
		out = append(out, kgo.RecordHeader{Key: k, Value: []byte(headers[k])})
	}
	return out
}

// preserveHeaders copies a record's headers, dropping the origin headers a
// previous dead-lettering may have added so the current ones are unambiguous.
func preserveHeaders(headers []kgo.RecordHeader) []kgo.RecordHeader {
	out := make([]kgo.RecordHeader, 0, len(headers)+len(originHeaders))
	for _, h := range headers {
		if slices.Contains(originHeaders, strings.ToLower(h.Key)) {
			continue
		}
		out = append(out, h)
	}
	return out
}
