package sim

import (
	"testing"
	"time"
)

// schedule_window_test.go — boundary math for mostRecentWindowBoundary,
// including the wrap-midnight case. Extracted with the helpers from the removed
// social scheduler's tests (LLM-150).

// scheduleWindowTestWorld is a bare world carrying only the timezone the
// boundary math reads.
func scheduleWindowTestWorld() *World {
	return &World{Settings: WorldSettings{Location: time.UTC}}
}

// at is a brief helper for a UTC instant on a fixed test day.
func at(hour, min int) time.Time {
	return time.Date(2026, 5, 22, hour, min, 0, 0, time.UTC)
}

func TestMostRecentWindowBoundary(t *testing.T) {
	w := scheduleWindowTestWorld()
	const start, end = 1080, 1320 // 18:00–22:00

	cases := []struct {
		name          string
		now           time.Time
		wantEnter     bool
		wantHour      int // expected boundary hour (UTC); -1 = expect today, see day check
		wantYesterday bool
	}{
		{"just after enter", at(18, 30), true, 18, false},
		{"just after leave", at(22, 30), false, 22, false},
		{"before today's enter -> yesterday's leave", at(9, 0), false, 22, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, isEnter, ok := mostRecentWindowBoundary(w, start, end, c.now)
			if !ok {
				t.Fatal("ok=false, want a boundary")
			}
			if isEnter != c.wantEnter {
				t.Errorf("isEnter = %v, want %v", isEnter, c.wantEnter)
			}
			if b.Hour() != c.wantHour {
				t.Errorf("boundary hour = %d, want %d (boundary=%v)", b.Hour(), c.wantHour, b)
			}
			if b.After(c.now) {
				t.Errorf("boundary %v is after now %v — must be at-or-before", b, c.now)
			}
			isYesterday := b.Day() != c.now.Day()
			if isYesterday != c.wantYesterday {
				t.Errorf("boundary yesterday = %v, want %v (boundary=%v now=%v)", isYesterday, c.wantYesterday, b, c.now)
			}
		})
	}
}

func TestMostRecentWindowBoundary_WrapMidnight(t *testing.T) {
	w := scheduleWindowTestWorld()
	const start, end = 1320, 120 // 22:00–02:00, wraps midnight

	// At 00:30, the most recent boundary is YESTERDAY's 22:00 enter (today's
	// 22:00 enter and 02:00 leave are both still in the future).
	b, isEnter, ok := mostRecentWindowBoundary(w, start, end, at(0, 30))
	if !ok || !isEnter {
		t.Fatalf("got (enter=%v, ok=%v), want enter=true ok=true", isEnter, ok)
	}
	if b.Hour() != 22 || b.Day() == 22 {
		t.Errorf("boundary = %v, want yesterday 22:00", b)
	}

	// At 02:30, the most recent boundary is today's 02:00 leave.
	b, isEnter, ok = mostRecentWindowBoundary(w, start, end, at(2, 30))
	if !ok || isEnter {
		t.Fatalf("got (enter=%v, ok=%v), want enter=false ok=true", isEnter, ok)
	}
	if b.Hour() != 2 || b.Day() != 22 {
		t.Errorf("boundary = %v, want today 02:00", b)
	}
}

func TestMostRecentWindowBoundary_EqualEndpointsEmpty(t *testing.T) {
	w := scheduleWindowTestWorld()
	if _, _, ok := mostRecentWindowBoundary(w, 600, 600, at(12, 0)); ok {
		t.Error("start==end should be an empty window (ok=false)")
	}
}
