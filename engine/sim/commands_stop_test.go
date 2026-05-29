package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// commands_stop_test.go — ZBBS-HOME-338. StopMove: a voluntary halt clears the
// MoveIntent and emits ActorMoveStopped{cancelled}; rejects when not walking.
// Reuses buildMoveTestWorld / moveIntentOf / eventRec from commands_move_test.go.

func TestStopMove_ClearsIntentAndEmitsCancelled(t *testing.T) {
	now := time.Now().UTC()
	w, cancel, rec := buildMoveTestWorld(t)
	defer cancel()

	// Put the walker in motion (no locomotion ticker runs in this harness, so
	// the MoveIntent persists until we stop it).
	dest := sim.NewPositionDestination(sim.Position{X: sim.PadX + 5, Y: sim.PadY + 5})
	if _, err := w.Send(sim.MoveActor("walker", dest, false, now)); err != nil {
		t.Fatalf("MoveActor rejected: %v", err)
	}
	if moveIntentOf(t, w, "walker") == nil {
		t.Fatal("precondition: walker should have a MoveIntent after MoveActor")
	}

	if _, err := w.Send(sim.StopMove("walker", now)); err != nil {
		t.Fatalf("StopMove rejected: %v", err)
	}

	if mi := moveIntentOf(t, w, "walker"); mi != nil {
		t.Errorf("StopMove left a MoveIntent: %+v", mi)
	}
	n := rec.countEvents(func(e sim.Event) bool {
		ms, ok := e.(*sim.ActorMoveStopped)
		return ok && ms.ActorID == "walker" && ms.Reason == sim.MoveStoppedCancelled
	})
	if n != 1 {
		t.Errorf("ActorMoveStopped{cancelled} count = %d, want 1", n)
	}
}

func TestStopMove_RejectsWhenNotWalking(t *testing.T) {
	now := time.Now().UTC()
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()

	// Walker is stationary (never issued a move).
	_, err := w.Send(sim.StopMove("walker", now))
	if err == nil {
		t.Fatal("StopMove on a stationary actor should reject")
	}
	if !strings.Contains(err.Error(), "not walking") {
		t.Errorf("rejection = %q, want it to mention 'not walking'", err.Error())
	}
}
