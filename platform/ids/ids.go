// Package ids mints and validates the platform's identifiers.
//
// Every platform-generated identifier is a ULID (ADR-0016): 26 characters of
// Crockford base32, lexicographically sortable by generation time, stored in
// TEXT columns. Operational entities carry a self-describing prefix so an id in
// an alert or a log line says what it is without a lookup (docs/conventions.md
// §1): FIL_ file registry entry, JOB_ ingestion job/run, REC_ reconciliation
// run, COR_ correlation id.
//
// Identifiers that travel on the A§24 event envelope — eventId, correlationId,
// causationId — are **bare ULIDs, no prefix** (contracts/README §6, and the
// envelope schema enforces `^[0-9A-HJKMNP-TV-Z]{26}$`). NewCorrelationID exists
// for the *operational records* that store a correlation id as a first-class
// entity (the file registry's `correlation_id` column); HTTP and Kafka
// correlation ids are bare ULIDs minted by platform/httpkit.
//
// Generation is monotonic within the process: two ids minted in the same
// millisecond still sort in call order. Across processes ULIDs are only ordered
// to millisecond precision — never rely on them as a global sequence.
package ids

import (
	cryptorand "crypto/rand"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// Prefixes for operational identifiers (docs/conventions.md §1, ADR-0016).
// Domain aggregate ids (caseId, accountId, decisionId, …) are unprefixed.
const (
	// FilePrefix marks a file registry entry — one ingested file.
	FilePrefix = "FIL_"
	// JobPrefix marks an ingestion job or run.
	JobPrefix = "JOB_"
	// ReconciliationPrefix marks a reconciliation run.
	ReconciliationPrefix = "REC_"
	// CorrelationPrefix marks a correlation id held as an operational record.
	// It never appears on an event envelope (see the package doc).
	CorrelationPrefix = "COR_"
)

const (
	// ulidLen is the length of a canonical ULID string.
	ulidLen = 26
	// maxULIDMillis is the largest instant a ULID's 48-bit timestamp can hold
	// (2 August 10889). Beyond it the encoding overflows.
	maxULIDMillis = uint64(1)<<48 - 1
)

// The process-wide monotonic generator. The mutex covers the timestamp sample,
// the last-seen millisecond and the entropy reader together — reading the clock
// outside the lock is exactly how a concurrent generator emits ids out of order,
// because two goroutines can sample different milliseconds and then reach the
// entropy source in the opposite order.
//
// Only NewULID uses it: feeding the monotonic reader an arbitrary timestamp
// would reset its state and break the ordering of every later NewULID call.
var (
	mintMu    sync.Mutex
	entropy   = ulid.Monotonic(cryptorand.Reader, 0)
	lastMilli uint64
)

// NewULID returns a new ULID for the current wall-clock instant. Ids minted in
// this process are strictly increasing, even from concurrent goroutines and even
// within a single millisecond.
//
// Wall-clock time is deliberate: a ULID's first 48 bits *are* its creation
// timestamp, so it cannot come from an injected clock without making ids from a
// mocked test clock collide with production ordering. Domain and application
// code still take time from platform/clock; only identifier minting reads the
// system clock. Tests that need a deterministic timestamp use NewULIDAt.
func NewULID() string {
	mintMu.Lock()
	defer mintMu.Unlock()

	// A wall clock that steps backwards (NTP correction, VM migration) would
	// otherwise emit an id that sorts before its predecessor: hold the previous
	// millisecond until real time catches up.
	ms := max(ulid.Timestamp(time.Now()), lastMilli)
	lastMilli = ms

	if id, err := ulid.New(ms, entropy); err == nil {
		return id.String()
	}
	// The monotonic reader overflows only if more than 2^80 ids are minted
	// inside one millisecond. Recoverable: fall back to independent entropy,
	// which loses ordering within that millisecond but never fails (crypto/rand
	// does not return errors on supported platforms).
	return ulid.MustNew(ms, cryptorand.Reader).String()
}

// NewULIDAt returns a new ULID whose 48-bit timestamp is t, for backfills and
// tests that need a deterministic generation instant.
//
// It draws independent entropy rather than the process monotonic source, so
// minting an id for a historical instant cannot disturb the ordering of
// NewULID. Ids minted for the same t are therefore unordered among themselves.
// Instants outside the ULID range (before the Unix epoch, after the year 10889)
// are clamped into it rather than failing.
func NewULIDAt(t time.Time) string {
	var ms uint64
	switch utc := t.UTC(); {
	case utc.Unix() < 0:
		ms = 0
	default:
		if ms = ulid.Timestamp(utc); ms > maxULIDMillis {
			ms = maxULIDMillis
		}
	}
	return ulid.MustNew(ms, cryptorand.Reader).String()
}

// IsULID reports whether s is a canonical bare ULID: exactly 26 Crockford
// base32 characters, uppercase, excluding I, L, O and U. It is the Go twin of
// the envelope schema's ULID pattern, so anything IsULID accepts is a legal
// eventId, correlationId or causationId.
func IsULID(s string) bool {
	if len(s) != ulidLen {
		return false
	}
	for i := range len(s) {
		if !isCrockford(s[i]) {
			return false
		}
	}
	// A ULID's first character encodes the top 3 bits of the 48-bit timestamp:
	// anything above '7' overflows and cannot be parsed back.
	return s[0] <= '7'
}

// isCrockford reports whether c is a canonical uppercase Crockford base32
// digit. I, L, O and U are excluded to avoid transcription errors.
func isCrockford(c byte) bool {
	switch {
	case c >= '0' && c <= '9':
		return true
	case c >= 'A' && c <= 'Z':
		return c != 'I' && c != 'L' && c != 'O' && c != 'U'
	default:
		return false
	}
}

// NewFileID returns a new FIL_-prefixed file registry id.
func NewFileID() string { return FilePrefix + NewULID() }

// NewJobID returns a new JOB_-prefixed ingestion job id.
func NewJobID() string { return JobPrefix + NewULID() }

// NewReconciliationID returns a new REC_-prefixed reconciliation run id.
func NewReconciliationID() string { return ReconciliationPrefix + NewULID() }

// NewCorrelationID returns a new COR_-prefixed correlation id for operational
// records. Do not put it on an event envelope or an X-Correlation-Id header —
// those carry bare ULIDs (see the package doc).
func NewCorrelationID() string { return CorrelationPrefix + NewULID() }

// IsFileID reports whether s is a well-formed FIL_<ULID>.
func IsFileID(s string) bool { return IsPrefixedULID(FilePrefix, s) }

// IsJobID reports whether s is a well-formed JOB_<ULID>.
func IsJobID(s string) bool { return IsPrefixedULID(JobPrefix, s) }

// IsReconciliationID reports whether s is a well-formed REC_<ULID>.
func IsReconciliationID(s string) bool { return IsPrefixedULID(ReconciliationPrefix, s) }

// IsCorrelationID reports whether s is a well-formed COR_<ULID>.
func IsCorrelationID(s string) bool { return IsPrefixedULID(CorrelationPrefix, s) }

// IsPrefixedULID reports whether s is exactly prefix followed by a canonical
// ULID. A prefix pasted into the wrong field fails here rather than at the
// database or the schema boundary.
func IsPrefixedULID(prefix, s string) bool {
	rest, ok := strings.CutPrefix(s, prefix)
	return ok && IsULID(rest)
}

// Strip removes a known operational prefix from s and reports whether one was
// present, so callers can compare the bare ULID part of two differently
// prefixed ids. An unprefixed ULID is returned unchanged with false.
func Strip(s string) (string, bool) {
	for _, p := range [...]string{FilePrefix, JobPrefix, ReconciliationPrefix, CorrelationPrefix} {
		if rest, ok := strings.CutPrefix(s, p); ok {
			return rest, true
		}
	}
	return s, false
}
