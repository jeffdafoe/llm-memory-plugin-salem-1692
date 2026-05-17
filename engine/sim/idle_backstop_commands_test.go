package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// idle_backstop_commands_test.go — substrate tests for the idle-backstop
// scan Command. Drives sim.EvaluateIdleBackstop directly via Send so
// timing is deterministic; tests for the goroutine driver
// (cascade.RunIdleBackstop) live in the cascade package and exercise
// AfterFunc cadence + ctx-cancel separately.

// buildIdleBackstopWorld stands up a world with a configurable mix of
// actor kinds, runs it, and returns ready-to-test handles. The world
// goroutine is canceled by the returned cleanup.
func buildIdleBackstopWorld(t *testing.T, actors map[sim.ActorID]*sim.Actor) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(actors)
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// loadTimeOf reads World.LoadedAt — the cold-start anchor the idle-
// backstop sweep uses as the floor for effective-last-activity. Used
// to compute "now" values relative to the load moment without relying
// on time.Now() drift.
//
// The signature takes an actor id for symmetry with how the substrate
// tests historically read per-actor state, but LoadedAt is world-level;
// the id parameter is informational only.
func loadTimeOf(t *testing.T, w *sim.World, _ sim.ActorID) time.Time {
	t.Helper()
	var stamp time.Time
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		stamp = world.LoadedAt
		return nil, nil
	}}); err != nil {
		t.Fatalf("loadTimeOf: %v", err)
	}
	if stamp.IsZero() {
		t.Fatal("World.LoadedAt is zero — LoadWorld did not stamp it")
	}
	return stamp
}

// runEvaluate sends EvaluateIdleBackstop(now) and returns the telemetry.
func runEvaluate(t *testing.T, w *sim.World, now time.Time) sim.IdleBackstopTelemetry {
	t.Helper()
	v, err := w.Send(sim.EvaluateIdleBackstop(now))
	if err != nil {
		t.Fatalf("EvaluateIdleBackstop: %v", err)
	}
	tm, ok := v.(sim.IdleBackstopTelemetry)
	if !ok {
		t.Fatalf("EvaluateIdleBackstop returned %T, want IdleBackstopTelemetry", v)
	}
	return tm
}

// TestEvaluateIdleBackstop_StampsPastThreshold: with default threshold
// (30 min from reactor.go), an actor whose lastReactorTickAt is older
// than 30 min gets a fresh idle warrant stamped.
func TestEvaluateIdleBackstop_StampsPastThreshold(t *testing.T) {
	w, cancel := buildIdleBackstopWorld(t, map[sim.ActorID]*sim.Actor{
		"hannah": {ID: "hannah", Kind: sim.KindNPCShared},
	})
	defer cancel()

	loadAt := loadTimeOf(t, w, "hannah")
	now := loadAt.Add(31 * time.Minute) // 1 min past 30-min default threshold

	tm := runEvaluate(t, w, now)
	if tm.Stamped != 1 {
		t.Errorf("Stamped = %d, want 1; telemetry=%+v", tm.Stamped, tm)
	}

	inspectActor(t, w, "hannah", func(a *sim.Actor) {
		if a.WarrantedSince == nil {
			t.Fatal("hannah has no WarrantedSince after idle backstop")
		}
		if len(a.Warrants) != 1 {
			t.Fatalf("hannah has %d warrants, want 1", len(a.Warrants))
		}
		got := a.Warrants[0]
		if got.Kind() != sim.WarrantKindIdleBackstop {
			t.Errorf("warrant kind = %q, want %q", got.Kind(), sim.WarrantKindIdleBackstop)
		}
		if got.Force {
			t.Error("idle backstop warrant has Force=true, want false")
		}
		if got.SourceEventID != 0 {
			t.Errorf("idle backstop warrant has SourceEventID=%d, want 0 (not event-sourced)", got.SourceEventID)
		}
		r, ok := got.Reason.(sim.IdleBackstopWarrantReason)
		if !ok {
			t.Fatalf("warrant reason is %T, want IdleBackstopWarrantReason", got.Reason)
		}
		// QuietDuration = now - lastReactorTickAt = 31min.
		want := 31 * time.Minute
		if got, tol := r.QuietDuration, time.Second; got < want-tol || got > want+tol {
			t.Errorf("QuietDuration = %v, want %v (±%v)", got, want, tol)
		}
	})
}

// TestEvaluateIdleBackstop_SkipsRecentlyTicked: an actor with
// lastReactorTickAt within threshold is not warranted.
func TestEvaluateIdleBackstop_SkipsRecentlyTicked(t *testing.T) {
	w, cancel := buildIdleBackstopWorld(t, map[sim.ActorID]*sim.Actor{
		"hannah": {ID: "hannah", Kind: sim.KindNPCShared},
	})
	defer cancel()

	loadAt := loadTimeOf(t, w, "hannah")
	now := loadAt.Add(29 * time.Minute) // still inside the 30-min default

	tm := runEvaluate(t, w, now)
	if tm.Stamped != 0 {
		t.Errorf("Stamped = %d, want 0", tm.Stamped)
	}
	if tm.SkippedRecentlyTicked != 1 {
		t.Errorf("SkippedRecentlyTicked = %d, want 1; telemetry=%+v", tm.SkippedRecentlyTicked, tm)
	}

	inspectActor(t, w, "hannah", func(a *sim.Actor) {
		if a.WarrantedSince != nil {
			t.Errorf("hannah was warranted within threshold: WarrantedSince=%v", a.WarrantedSince)
		}
	})
}

// TestEvaluateIdleBackstop_SkipsPC: PCs don't tick via the reactor
// (player-driven); the scope gate excludes them.
func TestEvaluateIdleBackstop_SkipsPC(t *testing.T) {
	w, cancel := buildIdleBackstopWorld(t, map[sim.ActorID]*sim.Actor{
		"player": {ID: "player", Kind: sim.KindPC},
	})
	defer cancel()

	loadAt := loadTimeOf(t, w, "player")
	now := loadAt.Add(31 * time.Minute) // past threshold — would qualify if scope allowed

	tm := runEvaluate(t, w, now)
	if tm.Stamped != 0 {
		t.Errorf("Stamped = %d, want 0 (PC excluded by scope)", tm.Stamped)
	}
	if tm.SkippedScope != 1 {
		t.Errorf("SkippedScope = %d, want 1; telemetry=%+v", tm.SkippedScope, tm)
	}

	inspectActor(t, w, "player", func(a *sim.Actor) {
		if a.WarrantedSince != nil {
			t.Errorf("player was warranted: WarrantedSince=%v", a.WarrantedSince)
		}
	})
}

// TestEvaluateIdleBackstop_SkipsAlreadyWarranted: an actor with an open
// warrant cycle (WarrantedSince != nil) doesn't need engine-injected
// liveness; they already have a real reason coming.
func TestEvaluateIdleBackstop_SkipsAlreadyWarranted(t *testing.T) {
	w, cancel := buildIdleBackstopWorld(t, map[sim.ActorID]*sim.Actor{
		"hannah": {ID: "hannah", Kind: sim.KindNPCShared},
	})
	defer cancel()

	loadAt := loadTimeOf(t, w, "hannah")
	now := loadAt.Add(31 * time.Minute)

	// Stamp a non-idle warrant first so WarrantedSince is set.
	if _, err := w.Send(sim.StampWarrant("hannah", sim.WarrantMeta{
		TriggerActorID: "hannah",
		Reason:         sim.BasicWarrantReason{K: sim.WarrantKindNPCSpoke},
	}, now)); err != nil {
		t.Fatalf("seed stamp: %v", err)
	}

	tm := runEvaluate(t, w, now)
	if tm.Stamped != 0 {
		t.Errorf("Stamped = %d, want 0 (already warranted)", tm.Stamped)
	}
	if tm.SkippedWarranted != 1 {
		t.Errorf("SkippedWarranted = %d, want 1; telemetry=%+v", tm.SkippedWarranted, tm)
	}

	inspectActor(t, w, "hannah", func(a *sim.Actor) {
		if len(a.Warrants) != 1 {
			t.Errorf("hannah has %d warrants, want 1 (the seeded NPCSpoke)", len(a.Warrants))
		}
		for _, war := range a.Warrants {
			if war.Kind() == sim.WarrantKindIdleBackstop {
				t.Errorf("hannah got an idle backstop warrant despite being already warranted")
			}
		}
	})
}

// TestEvaluateIdleBackstop_SkipsTickInFlight: an actor mid-tick
// doesn't need a parallel idle warrant queued for the next attempt.
func TestEvaluateIdleBackstop_SkipsTickInFlight(t *testing.T) {
	w, cancel := buildIdleBackstopWorld(t, map[sim.ActorID]*sim.Actor{
		"hannah": {ID: "hannah", Kind: sim.KindNPCShared},
	})
	defer cancel()

	loadAt := loadTimeOf(t, w, "hannah")
	now := loadAt.Add(31 * time.Minute)

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hannah"].TickInFlight = true
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed TickInFlight: %v", err)
	}

	tm := runEvaluate(t, w, now)
	if tm.Stamped != 0 {
		t.Errorf("Stamped = %d, want 0 (TickInFlight)", tm.Stamped)
	}
	if tm.SkippedTickInFlight != 1 {
		t.Errorf("SkippedTickInFlight = %d, want 1; telemetry=%+v", tm.SkippedTickInFlight, tm)
	}

	inspectActor(t, w, "hannah", func(a *sim.Actor) {
		if a.WarrantedSince != nil {
			t.Errorf("hannah was warranted mid-tick: WarrantedSince=%v", a.WarrantedSince)
		}
	})
}

// TestEvaluateIdleBackstop_StampsStatefulAndShared: scope includes both
// KindNPCStateful and KindNPCShared.
func TestEvaluateIdleBackstop_StampsStatefulAndShared(t *testing.T) {
	w, cancel := buildIdleBackstopWorld(t, map[sim.ActorID]*sim.Actor{
		"stately": {ID: "stately", Kind: sim.KindNPCStateful},
		"sharedy": {ID: "sharedy", Kind: sim.KindNPCShared},
		"playery": {ID: "playery", Kind: sim.KindPC},
	})
	defer cancel()

	loadAt := loadTimeOf(t, w, "stately")
	now := loadAt.Add(31 * time.Minute)

	tm := runEvaluate(t, w, now)
	if tm.Stamped != 2 {
		t.Errorf("Stamped = %d, want 2 (stateful + shared); telemetry=%+v", tm.Stamped, tm)
	}
	if tm.SkippedScope != 1 {
		t.Errorf("SkippedScope = %d, want 1 (the PC); telemetry=%+v", tm.SkippedScope, tm)
	}

	for _, id := range []sim.ActorID{"stately", "sharedy"} {
		inspectActor(t, w, id, func(a *sim.Actor) {
			if a.WarrantedSince == nil {
				t.Errorf("%s has no idle warrant", id)
			}
		})
	}
	inspectActor(t, w, "playery", func(a *sim.Actor) {
		if a.WarrantedSince != nil {
			t.Errorf("playery (PC) got a warrant: %v", a.WarrantedSince)
		}
	})
}

// TestEvaluateIdleBackstop_RespectsConfiguredThreshold: a non-default
// IdleBackstopThreshold in WorldSettings takes effect.
func TestEvaluateIdleBackstop_RespectsConfiguredThreshold(t *testing.T) {
	w, cancel := buildIdleBackstopWorld(t, map[sim.ActorID]*sim.Actor{
		"hannah": {ID: "hannah", Kind: sim.KindNPCShared},
	})
	defer cancel()

	// Bump threshold to 1 hour.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.IdleBackstopThreshold = time.Hour
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	loadAt := loadTimeOf(t, w, "hannah")

	// 31 min past load — would qualify under default (30 min) but NOT
	// under configured 1 hour.
	tm := runEvaluate(t, w, loadAt.Add(31*time.Minute))
	if tm.Stamped != 0 {
		t.Errorf("Stamped = %d, want 0 (within configured threshold)", tm.Stamped)
	}

	// 61 min past load — past configured threshold.
	tm = runEvaluate(t, w, loadAt.Add(61*time.Minute))
	if tm.Stamped != 1 {
		t.Errorf("Stamped = %d, want 1 (past configured threshold); telemetry=%+v", tm.Stamped, tm)
	}
}

// TestEvaluateIdleBackstop_ColdStartNoStorm: post-LoadWorld first sweep
// with `now` close to the load moment does NOT stamp idle warrants on
// every actor — the cold-start seed in RecentReactorTicks is the whole
// point of resetReactorStateOnLoad seeding a single entry at LoadedAt.
func TestEvaluateIdleBackstop_ColdStartNoStorm(t *testing.T) {
	actors := map[sim.ActorID]*sim.Actor{}
	for _, id := range []sim.ActorID{"a", "b", "c", "d", "e"} {
		actors[id] = &sim.Actor{ID: id, Kind: sim.KindNPCShared}
	}
	w, cancel := buildIdleBackstopWorld(t, actors)
	defer cancel()

	loadAt := loadTimeOf(t, w, "a")
	// First sweep ~immediately after load: all actors look "freshly
	// ticked" because of the LoadWorld anchor. None should backstop.
	now := loadAt.Add(time.Second)

	tm := runEvaluate(t, w, now)
	if tm.Stamped != 0 {
		t.Errorf("cold-start storm: Stamped = %d on first sweep; telemetry=%+v", tm.Stamped, tm)
	}
	if tm.SkippedRecentlyTicked != len(actors) {
		t.Errorf("SkippedRecentlyTicked = %d, want %d (all actors anchored at load)", tm.SkippedRecentlyTicked, len(actors))
	}
}

// TestEvaluateIdleBackstop_TelemetryShape exercises the telemetry
// fields end-to-end against a mixed-scope world.
func TestEvaluateIdleBackstop_TelemetryShape(t *testing.T) {
	w, cancel := buildIdleBackstopWorld(t, map[sim.ActorID]*sim.Actor{
		"stamped": {ID: "stamped", Kind: sim.KindNPCShared},
		"recent":  {ID: "recent", Kind: sim.KindNPCStateful},
		"pc":      {ID: "pc", Kind: sim.KindPC},
		"flight":  {ID: "flight", Kind: sim.KindNPCShared},
		"warrant": {ID: "warrant", Kind: sim.KindNPCShared},
	})
	defer cancel()

	loadAt := loadTimeOf(t, w, "stamped")

	// Seed "flight" mid-tick, "warrant" with an open warrant, and tick
	// "recent" recently (push an entry close to `now` into a freshly
	// allocated ring — lastReactorTickAt's ok=true branch overrides
	// the World.LoadedAt floor).
	now := loadAt.Add(31 * time.Minute) // past threshold for "stamped"
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["flight"].TickInFlight = true
		// "warrant" gets an open warranted cycle
		t := now
		world.Actors["warrant"].WarrantedSince = &t
		world.Actors["warrant"].WarrantDueAt = &t
		world.Actors["warrant"].Warrants = []sim.WarrantMeta{
			{Reason: sim.BasicWarrantReason{K: sim.WarrantKindNPCSpoke}},
		}
		// "recent" has a fresh tick 1 min before `now` — within threshold.
		// LoadWorld leaves RecentReactorTicks nil; allocate here.
		ring := sim.NewRingBuffer[time.Time](8)
		ring.Push(now.Add(-1 * time.Minute))
		world.Actors["recent"].RecentReactorTicks = ring
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed mixed state: %v", err)
	}

	tm := runEvaluate(t, w, now)

	if tm.Stamped != 1 {
		t.Errorf("Stamped = %d, want 1 (only 'stamped' qualifies); telemetry=%+v", tm.Stamped, tm)
	}
	if tm.SkippedScope != 1 {
		t.Errorf("SkippedScope = %d, want 1 (pc)", tm.SkippedScope)
	}
	if tm.SkippedRecentlyTicked != 1 {
		t.Errorf("SkippedRecentlyTicked = %d, want 1 (recent)", tm.SkippedRecentlyTicked)
	}
	if tm.SkippedWarranted != 1 {
		t.Errorf("SkippedWarranted = %d, want 1 (warrant)", tm.SkippedWarranted)
	}
	if tm.SkippedTickInFlight != 1 {
		t.Errorf("SkippedTickInFlight = %d, want 1 (flight)", tm.SkippedTickInFlight)
	}
}
