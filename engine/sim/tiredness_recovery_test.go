package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildRecoveryTestWorld seeds one actor and sets the recovery rate, then
// starts the world goroutine. rateX100 of 0 disables recovery.
func buildRecoveryTestWorld(t *testing.T, rateX100 int, actor *sim.Actor) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{actor.ID: actor})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.TirednessRecoveryPerMinuteX100 = rateX100
		return nil, nil
	}}); err != nil {
		cancel()
		t.Fatalf("set rate: %v", err)
	}
	return w, cancel
}

// setCursor stamps an actor's recovery cursor through the world goroutine.
func setCursor(t *testing.T, w *sim.World, id sim.ActorID, at *time.Time) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[id].LastTirednessRecoveryAt = at
		return nil, nil
	}}); err != nil {
		t.Fatalf("set cursor: %v", err)
	}
}

func getTiredness(t *testing.T, w *sim.World, id sim.ActorID) int {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[id].Needs["tiredness"], nil
	}})
	if err != nil {
		t.Fatalf("get tiredness: %v", err)
	}
	return v.(int)
}

func getCursor(t *testing.T, w *sim.World, id sim.ActorID) *time.Time {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[id].LastTirednessRecoveryAt, nil
	}})
	if err != nil {
		t.Fatalf("get cursor: %v", err)
	}
	return v.(*time.Time)
}

func restingActor(id sim.ActorID, tiredness int) *sim.Actor {
	return &sim.Actor{
		ID:       id,
		LLMAgent: string(id) + "-agent",
		Needs:    map[sim.NeedKey]int{"tiredness": tiredness, "hunger": 5, "thirst": 5},
	}
}

// TestRecoverTirednessBasic: 30 min asleep at 0.1/min → 3 units recovered,
// cursor advances by exactly 30 min (3 / 0.1).
func TestRecoverTirednessBasic(t *testing.T) {
	now := time.Now().UTC()
	a := restingActor("sleeper", 20)
	sleepEnd := now.Add(time.Hour)
	a.SleepingUntil = &sleepEnd
	w, cancel := buildRecoveryTestWorld(t, 10, a)
	defer cancel()

	cursorStart := now.Add(-30 * time.Minute)
	setCursor(t, w, "sleeper", &cursorStart)

	res, err := w.Send(sim.RecoverTiredness(now))
	if err != nil {
		t.Fatalf("RecoverTiredness: %v", err)
	}
	if n := res.(int); n != 1 {
		t.Fatalf("recovered count = %d, want 1", n)
	}
	if got := getTiredness(t, w, "sleeper"); got != 17 {
		t.Errorf("tiredness = %d, want 17 (20 - 3)", got)
	}
	if c := getCursor(t, w, "sleeper"); c == nil || !c.Equal(now) {
		t.Errorf("cursor = %v, want %v (advanced by 30 min)", c, now)
	}
}

// TestRecoverTirednessFractionalCarry: 5 min @ 0.1/min = 0.5 unit → nothing
// credited, cursor unchanged so the fraction carries into the next pass.
func TestRecoverTirednessFractionalCarry(t *testing.T) {
	now := time.Now().UTC()
	a := restingActor("napper", 20)
	end := now.Add(time.Hour)
	a.BreakUntil = &end
	w, cancel := buildRecoveryTestWorld(t, 10, a)
	defer cancel()

	cursorStart := now.Add(-5 * time.Minute)
	setCursor(t, w, "napper", &cursorStart)

	res, _ := w.Send(sim.RecoverTiredness(now))
	if n := res.(int); n != 0 {
		t.Fatalf("recovered count = %d, want 0 (sub-unit)", n)
	}
	if got := getTiredness(t, w, "napper"); got != 20 {
		t.Errorf("tiredness = %d, want 20 (unchanged)", got)
	}
	if c := getCursor(t, w, "napper"); c == nil || !c.Equal(cursorStart) {
		t.Errorf("cursor = %v, want %v (unchanged — fraction carries)", c, cursorStart)
	}
}

// TestRecoverTirednessClampsAtZero: more recovery than tiredness floors at 0.
func TestRecoverTirednessClampsAtZero(t *testing.T) {
	now := time.Now().UTC()
	a := restingActor("rested", 2)
	end := now.Add(time.Hour)
	a.SleepingUntil = &end
	w, cancel := buildRecoveryTestWorld(t, 10, a)
	defer cancel()

	cursorStart := now.Add(-60 * time.Minute) // 6 units at 0.1/min
	setCursor(t, w, "rested", &cursorStart)

	w.Send(sim.RecoverTiredness(now))
	if got := getTiredness(t, w, "rested"); got != 0 {
		t.Errorf("tiredness = %d, want 0 (clamped, 2 - 6)", got)
	}
}

// TestRecoverTirednessWindowEndClamp: recovery only credits up to the window
// end, not all the way to `now`. Sleep ended 10 min ago; cursor 30 min back →
// 20 min of credit (2 units), not 30 min (3 units).
func TestRecoverTirednessWindowEndClamp(t *testing.T) {
	now := time.Now().UTC()
	a := restingActor("waking", 20)
	endedAgo := now.Add(-10 * time.Minute)
	a.SleepingUntil = &endedAgo
	w, cancel := buildRecoveryTestWorld(t, 10, a)
	defer cancel()

	cursorStart := now.Add(-30 * time.Minute)
	setCursor(t, w, "waking", &cursorStart)

	w.Send(sim.RecoverTiredness(now))
	if got := getTiredness(t, w, "waking"); got != 18 {
		t.Errorf("tiredness = %d, want 18 (20 - 2, credit clamped to window end)", got)
	}
}

// TestRecoverTirednessNotRestingClearsCursor: an actor with no open window
// has its cursor dropped so a future window can't credit a stale gap.
func TestRecoverTirednessNotRestingClearsCursor(t *testing.T) {
	now := time.Now().UTC()
	a := restingActor("awake", 20) // no BreakUntil / SleepingUntil
	w, cancel := buildRecoveryTestWorld(t, 10, a)
	defer cancel()

	stale := now.Add(-3 * time.Hour)
	setCursor(t, w, "awake", &stale)

	w.Send(sim.RecoverTiredness(now))
	if got := getTiredness(t, w, "awake"); got != 20 {
		t.Errorf("tiredness = %d, want 20 (not resting, no recovery)", got)
	}
	if c := getCursor(t, w, "awake"); c != nil {
		t.Errorf("cursor = %v, want nil (cleared when not resting)", c)
	}
}

// TestRecoverTirednessLazyInit: a resting actor with no cursor gets one set
// to `now` and is credited nothing on the first pass.
func TestRecoverTirednessLazyInit(t *testing.T) {
	now := time.Now().UTC()
	a := restingActor("fresh", 20)
	end := now.Add(time.Hour)
	a.SleepingUntil = &end
	w, cancel := buildRecoveryTestWorld(t, 10, a)
	defer cancel()
	// cursor left nil by seed

	res, _ := w.Send(sim.RecoverTiredness(now))
	if n := res.(int); n != 0 {
		t.Fatalf("recovered = %d, want 0 (first pass just inits cursor)", n)
	}
	if got := getTiredness(t, w, "fresh"); got != 20 {
		t.Errorf("tiredness = %d, want 20 (unchanged on init pass)", got)
	}
	if c := getCursor(t, w, "fresh"); c == nil || !c.Equal(now) {
		t.Errorf("cursor = %v, want %v (lazy-init to now)", c, now)
	}
}

// TestRecoverTirednessDisabled: rate 0 is a no-op even for a resting actor.
func TestRecoverTirednessDisabled(t *testing.T) {
	now := time.Now().UTC()
	a := restingActor("sleeper", 20)
	end := now.Add(time.Hour)
	a.SleepingUntil = &end
	w, cancel := buildRecoveryTestWorld(t, 0, a) // disabled
	defer cancel()

	cursorStart := now.Add(-60 * time.Minute)
	setCursor(t, w, "sleeper", &cursorStart)

	res, _ := w.Send(sim.RecoverTiredness(now))
	if n := res.(int); n != 0 {
		t.Fatalf("recovered = %d, want 0 (disabled)", n)
	}
	if got := getTiredness(t, w, "sleeper"); got != 20 {
		t.Errorf("tiredness = %d, want 20 (recovery disabled)", got)
	}
}

// TestRecoverTirednessExpiredWindowClearsCursor: a window whose end is in the
// past still credits the final unit up to its end, then drops the cursor so a
// later window can't be credited from the stale boundary.
func TestRecoverTirednessExpiredWindowClearsCursor(t *testing.T) {
	now := time.Now().UTC()
	a := restingActor("woken", 20)
	endedAgo := now.Add(-1 * time.Minute) // window ended 1 min ago
	a.SleepingUntil = &endedAgo
	w, cancel := buildRecoveryTestWorld(t, 10, a)
	defer cancel()

	cursorStart := now.Add(-11 * time.Minute) // 10 min of rest up to end
	setCursor(t, w, "woken", &cursorStart)

	res, _ := w.Send(sim.RecoverTiredness(now))
	if n := res.(int); n != 1 {
		t.Fatalf("recovered = %d, want 1 (final unit up to window end)", n)
	}
	if got := getTiredness(t, w, "woken"); got != 19 {
		t.Errorf("tiredness = %d, want 19 (20 - 1 final unit)", got)
	}
	if c := getCursor(t, w, "woken"); c != nil {
		t.Errorf("cursor = %v, want nil (cleared after expired window)", c)
	}
}

// TestRecoverTirednessNoOverCreditOnReopen is the regression for the review
// finding: an expired window must not let a later window credit the whole gap.
// After the cursor is cleared by the expired window, reopening a window without
// re-stamping the cursor lazy-inits from `now` and credits nothing.
func TestRecoverTirednessNoOverCreditOnReopen(t *testing.T) {
	now := time.Now().UTC()
	a := restingActor("relapse", 20)
	oldEnd := now.Add(-5 * time.Minute)
	a.SleepingUntil = &oldEnd
	w, cancel := buildRecoveryTestWorld(t, 10, a)
	defer cancel()

	// Cursor already at the old window's end (fully credited), window still set.
	setCursor(t, w, "relapse", &oldEnd)

	// Pass 1: expired window, nothing left to credit, cursor cleared.
	w.Send(sim.RecoverTiredness(now))
	if c := getCursor(t, w, "relapse"); c != nil {
		t.Fatalf("cursor = %v, want nil after expired window", c)
	}

	// Reopen a fresh window WITHOUT stamping the cursor (the dangerous case).
	newEnd := now.Add(time.Hour)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["relapse"].SleepingUntil = &newEnd
		return nil, nil
	}}); err != nil {
		t.Fatalf("reopen window: %v", err)
	}

	// Pass 2: lazy-init only — must NOT credit the hours since oldEnd.
	res, _ := w.Send(sim.RecoverTiredness(now))
	if n := res.(int); n != 0 {
		t.Fatalf("recovered = %d, want 0 (lazy-init, no catch-up)", n)
	}
	if got := getTiredness(t, w, "relapse"); got != 20 {
		t.Errorf("tiredness = %d, want 20 (no over-credit on reopen)", got)
	}
}

// TestRecoverTirednessAlreadyZeroNotCounted: an already-rested actor advances
// its cursor but is NOT counted as recovered (the count means "tiredness
// dropped", matching the doc + log).
func TestRecoverTirednessAlreadyZeroNotCounted(t *testing.T) {
	now := time.Now().UTC()
	a := restingActor("fresh", 0)
	end := now.Add(time.Hour)
	a.SleepingUntil = &end
	w, cancel := buildRecoveryTestWorld(t, 10, a)
	defer cancel()

	cursorStart := now.Add(-60 * time.Minute) // 6 units would accrue
	setCursor(t, w, "fresh", &cursorStart)

	res, _ := w.Send(sim.RecoverTiredness(now))
	if n := res.(int); n != 0 {
		t.Fatalf("recovered = %d, want 0 (already at 0, nothing dropped)", n)
	}
	if got := getTiredness(t, w, "fresh"); got != 0 {
		t.Errorf("tiredness = %d, want 0", got)
	}
	if c := getCursor(t, w, "fresh"); c == nil || !c.Equal(now) {
		t.Errorf("cursor = %v, want %v (advanced even when already rested)", c, now)
	}
}
