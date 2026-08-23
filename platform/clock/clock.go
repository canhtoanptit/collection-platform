// Package clock is the single source of "now" for domain and application code.
//
// CLAUDE.md §3: time is UTC time.Time serialized RFC3339 with Z, and it comes
// from this package — never from time.Now() in domain or application code. That
// is what makes time-dependent behaviour (ageing, DPD buckets, promise due
// dates, SLA breaches) testable without sleeping and reproducible from a fixed
// input. Production wiring passes System(); tests pass Fixed or a Mock.
//
// Every Clock in this package returns UTC. A Clock that could hand back a
// wall-clock time in a local zone would let a local offset leak into a stored
// timestamp or an RFC3339 payload, which the event envelope schema rejects
// outright (ADR-0016).
package clock

import (
	"sync"
	"time"
)

// Clock reports the current instant. Implementations return UTC.
type Clock interface {
	// Now returns the current instant in UTC.
	Now() time.Time
}

// Func adapts a plain function to Clock. The returned time is normalised to
// UTC, so a Func that hands back a local time still satisfies the invariant.
type Func func() time.Time

// Now implements Clock.
func (f Func) Now() time.Time { return f().UTC() }

// System returns a Clock reading the operating system clock in UTC. It is the
// only Clock a production wiring should use.
func System() Clock { return Func(time.Now) }

// Fixed returns a Clock that always reports t (in UTC). Use it when a test
// needs one deterministic instant and never needs it to move.
func Fixed(t time.Time) Clock {
	utc := t.UTC()
	return Func(func() time.Time { return utc })
}

// Mock is a Clock a test drives by hand. It is safe for concurrent use, so a
// test can advance it while the code under test reads it from another
// goroutine.
type Mock struct {
	mu  sync.RWMutex
	now time.Time
}

// NewMock returns a Mock reporting start (in UTC) until it is advanced or set.
func NewMock(start time.Time) *Mock {
	return &Mock{now: start.UTC()}
}

// Now implements Clock.
func (m *Mock) Now() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.now
}

// Advance moves the clock forward by d and returns the new instant. A negative
// d moves it backwards, which is how a test exercises out-of-order or
// late-arriving data; nothing in this package assumes monotonicity.
func (m *Mock) Advance(d time.Duration) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(d)
	return m.now
}

// Set moves the clock to t (in UTC) and returns the new instant.
func (m *Mock) Set(t time.Time) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = t.UTC()
	return m.now
}
