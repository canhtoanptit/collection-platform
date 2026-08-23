package clock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/canhtoanptit/collection-platform/platform/clock"
)

// tokyo is a deliberately non-UTC zone: every constructor must normalise it
// away, because a local offset in a stored or serialized timestamp is rejected
// by the event envelope schema (ADR-0016).
var tokyo = time.FixedZone("UTC+9", 9*3600)

func TestEveryClockReportsUTC(t *testing.T) {
	t.Parallel()

	local := time.Date(2026, 8, 22, 19, 0, 0, 0, tokyo)

	tests := []struct {
		name  string
		clock clock.Clock
	}{
		{"System", clock.System()},
		{"Fixed from a local time", clock.Fixed(local)},
		{"Func returning a local time", clock.Func(func() time.Time { return local })},
		{"Mock started from a local time", clock.NewMock(local)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := tc.clock.Now()
			if got.Location() != time.UTC {
				t.Errorf("Now() location = %v, want UTC", got.Location())
			}
			if _, offset := got.Zone(); offset != 0 {
				t.Errorf("Now() zone offset = %d, want 0", offset)
			}
			if want := got.Format(time.RFC3339Nano); want[len(want)-1] != 'Z' {
				t.Errorf("Now() formats as %q, want a Z suffix", want)
			}
		})
	}
}

func TestFixedNeverMoves(t *testing.T) {
	t.Parallel()

	want := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	c := clock.Fixed(want)

	first := c.Now()
	time.Sleep(2 * time.Millisecond)
	second := c.Now()

	if !first.Equal(want) || !second.Equal(want) {
		t.Errorf("Fixed(%v) reported %v then %v", want, first, second)
	}
}

func TestFixedPreservesTheInstantAcrossZones(t *testing.T) {
	t.Parallel()

	instant := time.Date(2026, 8, 22, 19, 0, 0, 0, tokyo)
	got := clock.Fixed(instant).Now()

	if !got.Equal(instant) {
		t.Errorf("Fixed changed the instant: %v != %v", got, instant)
	}
	if want := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Fixed(19:00 UTC+9).Now() = %v, want %v", got, want)
	}
}

func TestSystemAdvances(t *testing.T) {
	t.Parallel()

	c := clock.System()
	before := c.Now()
	time.Sleep(2 * time.Millisecond)

	if after := c.Now(); !after.After(before) {
		t.Errorf("System() did not advance: %v then %v", before, after)
	}
}

func TestMockAdvance(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		steps []time.Duration
		want  time.Time
	}{
		{"no movement", nil, start},
		{"one hour", []time.Duration{time.Hour}, start.Add(time.Hour)},
		{"accumulates", []time.Duration{time.Hour, 30 * time.Minute}, start.Add(90 * time.Minute)},
		{"a whole day", []time.Duration{24 * time.Hour}, start.AddDate(0, 0, 1)},
		{"backwards for late-arriving data", []time.Duration{-time.Hour}, start.Add(-time.Hour)},
		{"zero is a no-op", []time.Duration{0}, start},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			m := clock.NewMock(start)
			var last time.Time
			for _, d := range tc.steps {
				last = m.Advance(d)
			}
			if got := m.Now(); !got.Equal(tc.want) {
				t.Errorf("Now() = %v, want %v", got, tc.want)
			}
			if len(tc.steps) > 0 && !last.Equal(tc.want) {
				t.Errorf("Advance returned %v, want %v", last, tc.want)
			}
		})
	}
}

func TestMockSet(t *testing.T) {
	t.Parallel()

	m := clock.NewMock(time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC))
	want := time.Date(2026, 9, 1, 2, 25, 1, 0, time.UTC)

	if got := m.Set(time.Date(2026, 9, 1, 11, 25, 1, 0, tokyo)); !got.Equal(want) {
		t.Errorf("Set returned %v, want %v", got, want)
	}
	if got := m.Now(); !got.Equal(want) || got.Location() != time.UTC {
		t.Errorf("Now() = %v (%v), want %v UTC", got, got.Location(), want)
	}
}

func TestMockIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	m := clock.NewMock(start)

	const advances = 500

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range advances {
			m.Advance(time.Second)
		}
	}()
	go func() {
		defer wg.Done()
		for range advances {
			if got := m.Now(); got.Before(start) {
				t.Errorf("Now() = %v, before the start %v", got, start)
				return
			}
		}
	}()
	wg.Wait()

	if want := start.Add(advances * time.Second); !m.Now().Equal(want) {
		t.Errorf("after %d concurrent advances Now() = %v, want %v", advances, m.Now(), want)
	}
}

// TestMockSatisfiesClock is a compile-time guard: *Mock must remain usable
// wherever a Clock is expected, so a test can swap it in without a wrapper.
func TestMockSatisfiesClock(t *testing.T) {
	t.Parallel()

	var c clock.Clock = clock.NewMock(time.Unix(0, 0))
	if got := c.Now(); !got.Equal(time.Unix(0, 0).UTC()) {
		t.Errorf("Now() = %v, want the Unix epoch", got)
	}
}
