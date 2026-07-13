package sim

import (
	"context"
	"testing"
	"time"
)

// needs_catchup_test.go — LLM-393. A restart must resume the village where it
// left off, never accumulate a "catch-up" need shock. runNeedsTickIteration
// applies at most a single hour: a gap of more than one hour (real downtime, or
// a boot onto a stale checkpoint) stamps the boundary and moves on with no
// increment; a normal single-hour roll still ticks +NeedsTickAmount.

// catchupTestWorld builds a running world with one stateful vendor at mid
// needs and the needs-tick knobs set. Cancel stops the world goroutine.
func catchupTestWorld(t *testing.T) (*World, context.CancelFunc) {
	t.Helper()
	w := NewWorld(Repository{})
	w.Settings = WorldSettings{
		Location:        time.UTC,
		NeedsTickAmount: 1,
		NeedThresholds:  DefaultNeedThresholds(),
	}
	w.Actors["vendor"] = &Actor{
		ID:       "vendor",
		Kind:     KindNPCStateful,
		LLMAgent: "salem-vendor",
		Needs:    map[NeedKey]int{"hunger": 5, "thirst": 5, "tiredness": 5},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

func readVendorNeed(t *testing.T, w *World, key NeedKey) int {
	t.Helper()
	v, err := w.Send(Command{Fn: func(world *World) (any, error) {
		return world.Actors["vendor"].Needs[key], nil
	}})
	if err != nil {
		t.Fatalf("read need %s: %v", key, err)
	}
	return v.(int)
}

func setNeedsStamp(t *testing.T, w *World, at time.Time) {
	t.Helper()
	if _, err := w.Send(Command{Fn: func(world *World) (any, error) {
		world.Environment.LastNeedsTickAt = at
		return nil, nil
	}}); err != nil {
		t.Fatalf("set stamp: %v", err)
	}
}

func readNeedsStamp(t *testing.T, w *World) time.Time {
	t.Helper()
	v, err := w.Send(Command{Fn: func(world *World) (any, error) {
		return world.Environment.LastNeedsTickAt, nil
	}})
	if err != nil {
		t.Fatalf("read stamp: %v", err)
	}
	return v.(time.Time)
}

// TestNeedsTick_NoCatchupOnLongGap: an 18-hour-stale stamp (the LLM-392
// scenario) applies NO increment — every need is untouched — and the stamp
// advances to the current hour boundary so normal ticking resumes.
func TestNeedsTick_NoCatchupOnLongGap(t *testing.T) {
	w, cancel := catchupTestWorld(t)
	defer cancel()

	now := time.Date(1692, 9, 22, 15, 30, 0, 0, time.UTC)
	setNeedsStamp(t, w, now.Truncate(time.Hour).Add(-18*time.Hour))

	runNeedsTickIteration(context.Background(), w, now)

	for _, key := range []NeedKey{"hunger", "thirst", "tiredness"} {
		if got := readVendorNeed(t, w, key); got != 5 {
			t.Errorf("%s = %d, want 5 (no catch-up shock on an 18h gap)", key, got)
		}
	}
	if got := readNeedsStamp(t, w); !got.Equal(now.Truncate(time.Hour)) {
		t.Errorf("stamp = %v, want %v (boundary advanced, ready to resume)", got, now.Truncate(time.Hour))
	}
}

// TestNeedsTick_NormalHourStillTicks: a single-hour roll is NOT a gap — the
// increment still applies (+NeedsTickAmount), so steady-state accrual is
// unchanged by the no-catch-up guard.
func TestNeedsTick_NormalHourStillTicks(t *testing.T) {
	w, cancel := catchupTestWorld(t)
	defer cancel()

	now := time.Date(1692, 9, 22, 15, 30, 0, 0, time.UTC)
	setNeedsStamp(t, w, now.Truncate(time.Hour).Add(-1*time.Hour))

	runNeedsTickIteration(context.Background(), w, now)

	if got := readVendorNeed(t, w, "hunger"); got != 6 {
		t.Errorf("hunger = %d, want 6 (5 + 1, normal hourly tick preserved)", got)
	}
}
