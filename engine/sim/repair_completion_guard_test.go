package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// repair_completion_guard_test.go — LLM-287. Pins the one guard left on the
// repair-completion path after the fix bound the Wear=0 reset to the object the
// window began at (act.ObjectID). The happy reset path is covered end to end in
// the handlers package (TestHiredWorkerRepair_*); this covers the negative arm.

// wearOf reads a village object's wear off the world goroutine.
func wearOf(t *testing.T, w *sim.World, objID sim.VillageObjectID) int {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.VillageObjects[objID].Wear, nil
	}})
	if err != nil {
		t.Fatalf("wearOf: %v", err)
	}
	return res.(int)
}

// TestRepairCompletion_SkipsNonBusinessObject: if the object a repair window
// began at is no longer a wearable business at completion — its owner or business
// tag cleared mid-window — the completion skips the Wear=0 reset (the
// !IsWearableStall guard) rather than mending a non-business. "oak" carries no
// business tag and no owner, so it is never a wearable stall; a repair window
// pointed at it must complete as a no-op with its wear untouched.
func TestRepairCompletion_SkipsNonBusinessObject(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()

	// Open a repair window pointed at the non-business "oak", bypassing
	// StartRepair's gate (which rejects a non-business) — the point under test is
	// the COMPLETION guard, not the start gate.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.VillageObjects["oak"].Wear = 500
		past := time.Now().UTC().Add(-time.Second)
		world.Actors["hannah"].SourceActivity = &sim.SourceActivity{
			Kind:      sim.SourceActivityRepair,
			ObjectID:  "oak",
			StartedAt: past,
			Until:     past,
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed repair window: %v", err)
	}

	if n := forceComplete(t, w); n != 1 {
		t.Fatalf("forceComplete landed %d activities, want 1 (the window must still be swept + cleared)", n)
	}

	if got := wearOf(t, w, "oak"); got != 500 {
		t.Errorf("oak wear after a repair completed on a non-business object: got %d, want 500 — the guard must skip the reset", got)
	}
	if liveActivity(t, w, "hannah") != nil {
		t.Errorf("repair window should be cleared after the sweep")
	}
}
