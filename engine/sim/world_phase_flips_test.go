package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildPhaseTestWorld seeds a small fixture: two lamp objects, one already
// at the day-active state and one not; one tree with no phase-sensitive
// states; and one orphan object whose asset isn't in the catalog.
//
// Initial phase is seeded to night (matching the lamp states) via
// Environment.Seed — NO intermediate transition fires, so the fixture is
// deterministic with respect to async flip timing. Returns a running
// world plus a cancel func the test must defer.
func buildPhaseTestWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()

	// Seed environment with PhaseNight directly so the test's
	// transition-to-day exercises real flips against known initial states.
	// Carry the default settings shape from a freshly-constructed mem repo
	// so unrelated fields (NeedThresholds, etc.) stay sane.
	defaultRepo := mem.NewEnvironmentRepo()
	_, _, defaultSettings, _ := defaultRepo.Load(context.Background())
	handles.Environment.Seed(sim.WorldEnvironment{}, sim.PhaseNight, defaultSettings)
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"lamp-iron": {
			ID:           "lamp-iron",
			Name:         "Iron Lamp",
			Category:     "structure",
			DefaultState: "unlit",
			States: []sim.AssetState{
				{ID: 10, State: "unlit", Tags: []string{"day-active"}},
				{ID: 11, State: "lit", Tags: []string{"night-active"}},
			},
		},
		"tree-maple": {
			ID:           "tree-maple",
			Name:         "Maple Tree",
			Category:     "tree",
			DefaultState: "default",
			States: []sim.AssetState{
				{ID: 20, State: "default"},
			},
		},
		"torch-lamplighter": {
			ID:           "torch-lamplighter",
			Name:         "Lamplighter Torch",
			Category:     "structure",
			DefaultState: "unlit",
			States: []sim.AssetState{
				// Both lamplighter-target AND day-active → excluded when
				// excludeTag=TagLamplighterTarget.
				{ID: 30, State: "unlit", Tags: []string{"day-active", "lamplighter-target"}},
				{ID: 31, State: "lit", Tags: []string{"night-active", "lamplighter-target"}},
			},
		},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"lamp-A": {ID: "lamp-A", AssetID: "lamp-iron", CurrentState: "lit", X: 100, Y: 100},    // night state, day transition needs flip
		"lamp-B": {ID: "lamp-B", AssetID: "lamp-iron", CurrentState: "unlit", X: 200, Y: 200},  // already at day state, no flip
		"tree":   {ID: "tree", AssetID: "tree-maple", CurrentState: "default", X: 300, Y: 300}, // not phase-sensitive
		"torch":  {ID: "torch", AssetID: "torch-lamplighter", CurrentState: "lit", X: 400, Y: 400},
		"orphan": {ID: "orphan", AssetID: "missing-asset", CurrentState: "default", X: 500, Y: 500},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// TestDetermineTransitionFlipsDay covers the four classification branches
// that drive the day-active flip list.
func TestDetermineTransitionFlipsDay(t *testing.T) {
	w, cancel := buildPhaseTestWorld(t)
	defer cancel()

	res, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.DetermineTransitionFlips(world, sim.PhaseDay, ""), nil
		},
	})
	if err != nil {
		t.Fatalf("determine: %v", err)
	}
	flips := res.([]sim.PendingFlip)

	// Expected flips: lamp-A (currently "lit", target "unlit") and torch
	// (currently "lit", target "unlit"). lamp-B is already at target, tree
	// has no day-active state, orphan has no asset entry.
	got := flipIDs(flips)
	want := map[sim.VillageObjectID]string{
		"lamp-A": "unlit",
		"torch":  "unlit",
	}
	if len(got) != len(want) {
		t.Fatalf("flip count = %d, want %d (got %v)", len(got), len(want), got)
	}
	for id, wantState := range want {
		gotState, ok := got[id]
		if !ok {
			t.Errorf("missing flip for %q", id)
			continue
		}
		if gotState != wantState {
			t.Errorf("%q flip target = %q, want %q", id, gotState, wantState)
		}
	}
}

// TestDetermineTransitionFlipsExcludeTag covers the lamplighter-target
// carve-out (the legacy hasLamplighter branch).
func TestDetermineTransitionFlipsExcludeTag(t *testing.T) {
	w, cancel := buildPhaseTestWorld(t)
	defer cancel()

	res, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.DetermineTransitionFlips(world, sim.PhaseDay, sim.TagLamplighterTarget), nil
		},
	})
	flips := res.([]sim.PendingFlip)

	got := flipIDs(flips)
	// torch is excluded now; only lamp-A flips in the bulk pass.
	if len(got) != 1 {
		t.Fatalf("with excludeTag, flip count = %d, want 1 (got %v)", len(got), got)
	}
	if _, ok := got["lamp-A"]; !ok {
		t.Errorf("expected lamp-A in flips, got %v", got)
	}
	if _, ok := got["torch"]; ok {
		t.Errorf("torch should be excluded, got %v", got)
	}
}

// TestSetVillageObjectStateApplied covers the happy path.
func TestSetVillageObjectStateApplied(t *testing.T) {
	w, cancel := buildPhaseTestWorld(t)
	defer cancel()

	res, err := w.Send(sim.SetVillageObjectState("lamp-A", "unlit", 0))
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	sr := res.(sim.SetStateResult)
	if !sr.Applied || sr.Reason != "applied" {
		t.Errorf("result = %+v, want Applied=true Reason=applied", sr)
	}
	if w.Published().VillageObjects["lamp-A"].CurrentState != "unlit" {
		t.Errorf("state didn't change")
	}
}

// TestSetVillageObjectStateAlreadyAtTarget covers the no-op short-circuit.
func TestSetVillageObjectStateAlreadyAtTarget(t *testing.T) {
	w, cancel := buildPhaseTestWorld(t)
	defer cancel()

	res, _ := w.Send(sim.SetVillageObjectState("lamp-B", "unlit", 0))
	sr := res.(sim.SetStateResult)
	if sr.Applied || sr.Reason != "already_at_target" {
		t.Errorf("result = %+v, want Applied=false Reason=already_at_target", sr)
	}
}

// TestSetVillageObjectStateNotFound covers the missing-object branch.
func TestSetVillageObjectStateNotFound(t *testing.T) {
	w, cancel := buildPhaseTestWorld(t)
	defer cancel()

	res, _ := w.Send(sim.SetVillageObjectState("ghost", "unlit", 0))
	sr := res.(sim.SetStateResult)
	if sr.Applied || sr.Reason != "not_found" {
		t.Errorf("result = %+v, want Applied=false Reason=not_found", sr)
	}
}

// TestSetVillageObjectStateSuperseded covers the generation guard — the
// critical safety net protecting against rapid phase reversals.
func TestSetVillageObjectStateSuperseded(t *testing.T) {
	w, cancel := buildPhaseTestWorld(t)
	defer cancel()

	// Bump WorldEventGen to a non-zero value, then capture, then bump
	// again — gives us a "captured" gen that is genuinely stale relative
	// to "current" by the time we issue the SetVillageObjectState call.
	bump := func() {
		_, _ = w.Send(sim.Command{
			Fn: func(world *sim.World) (any, error) {
				world.WorldEventGen.Add(1)
				return nil, nil
			},
		})
	}
	bump()
	stale := w.WorldEventGen.Load()
	bump()

	res, _ := w.Send(sim.SetVillageObjectState("lamp-A", "unlit", stale))
	sr := res.(sim.SetStateResult)
	if sr.Applied || sr.Reason != "superseded" {
		t.Errorf("stale gen: result = %+v, want Applied=false Reason=superseded", sr)
	}
	// lamp-A still in its original "lit" state — supersede prevented the
	// stale flip from overwriting.
	if w.Published().VillageObjects["lamp-A"].CurrentState != "lit" {
		t.Errorf("supersede leaked through: lamp-A state = %q",
			w.Published().VillageObjects["lamp-A"].CurrentState)
	}
}

// TestApplyPhaseTransitionFiresFlips covers the end-to-end orchestration —
// ApplyPhaseTransition kicks off async flips via time.AfterFunc, the world
// goroutine executes them on its own thread, and the published state
// eventually catches up. Uses SpreadSeconds=0 so flips fire immediately.
//
// Lamplighter carve-out: ApplyPhaseTransition unconditionally excludes
// TagLamplighterTarget from the bulk flips, leaving torch for the
// lamplighter cascade slice. No lamplighter actor in this fixture, so
// torch stays at its old state.
func TestApplyPhaseTransitionFiresFlips(t *testing.T) {
	w, cancel := buildPhaseTestWorld(t)
	defer cancel()

	// Transition night → day. lamp-A flips "lit" → "unlit". torch is
	// the lamplighter-target carve-out and stays at "lit". lamp-B is
	// already at "unlit" (its day-state target).
	res, err := w.Send(sim.ApplyPhaseTransition(sim.PhaseDay))
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	tr := res.(sim.PhaseTransitionResult)
	if tr.ObjectsAffected != 1 {
		t.Errorf("ObjectsAffected = %d, want 1 (lamp-A only; torch carved out)", tr.ObjectsAffected)
	}
	if tr.Gen == 0 {
		t.Error("transition Gen = 0, expected non-zero")
	}

	// Eventually-consistent: poll up to 500ms for the async flip to land.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		snap := w.Published()
		if snap.VillageObjects["lamp-A"].CurrentState == "unlit" {
			// Sanity: torch (lamplighter-target carve-out) was NOT
			// flipped by the bulk pass.
			if torch := snap.VillageObjects["torch"].CurrentState; torch != "lit" {
				t.Errorf("torch (lamplighter-target) leaked into bulk flips: state = %q, want %q", torch, "lit")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	snap := w.Published()
	t.Fatalf("flips didn't land within deadline: lamp-A=%q",
		snap.VillageObjects["lamp-A"].CurrentState)
}

// TestApplyPhaseTransitionRedundantAlignsStragglers covers a redundant
// phase-change call (already at the target phase). From == To but the
// determine pass still walks objects and emits flips for any whose
// current_state doesn't match the phase target — the legacy "startup
// catch-up" mechanism: applyTransition with the current phase brings
// stragglers into compliance.
//
// In the fixture: phase=night, lamp-B is at "unlit" (the day-active
// state) — so a redundant night transition emits 1 flip to bring lamp-B
// into the night state ("lit").
func TestApplyPhaseTransitionRedundantAlignsStragglers(t *testing.T) {
	w, cancel := buildPhaseTestWorld(t)
	defer cancel()

	res, _ := w.Send(sim.ApplyPhaseTransition(sim.PhaseNight))
	tr := res.(sim.PhaseTransitionResult)
	if tr.From != sim.PhaseNight || tr.To != sim.PhaseNight {
		t.Errorf("redundant transition: From=%q To=%q, want both night", tr.From, tr.To)
	}
	if tr.ObjectsAffected != 1 {
		t.Errorf("redundant transition aligns 1 straggler (lamp-B): ObjectsAffected = %d, want 1",
			tr.ObjectsAffected)
	}
}

// flipIDs collapses a []PendingFlip into id → target_state for easy
// assertion regardless of ordering.
func flipIDs(flips []sim.PendingFlip) map[sim.VillageObjectID]string {
	out := make(map[sim.VillageObjectID]string, len(flips))
	for _, f := range flips {
		out[f.ObjectID] = f.NewState
	}
	return out
}
