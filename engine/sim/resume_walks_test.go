package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// setResumeDestination seeds a checkpointed-walk destination on the named
// actor, standing in for what pg LoadAll does when actor.move_destination
// is non-NULL.
func setResumeDestination(t *testing.T, w *sim.World, actorID sim.ActorID, dest sim.MoveDestination) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[actorID].ResumeDestination = &dest
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed ResumeDestination: %v", err)
	}
}

// TestResumeCheckpointedWalks_ResumesWalk: a loaded actor with a
// checkpointed destination gets a live MoveIntent for that destination,
// the resume field clears, and the client walk frame fires.
func TestResumeCheckpointedWalks_ResumesWalk(t *testing.T) {
	w, cancel, rec := buildSetPositionTestWorld(t)
	defer cancel()
	target := sim.Position{X: sim.PadX + 5, Y: sim.PadY + 5}
	setResumeDestination(t, w, "walker", sim.NewPositionDestination(target))

	res, err := w.Send(sim.ResumeCheckpointedWalks(time.Now()))
	if err != nil {
		t.Fatalf("ResumeCheckpointedWalks: %v", err)
	}
	out := res.(sim.ResumeWalksResult)
	if out.Resumed != 1 || out.Dropped != 0 {
		t.Errorf("result = %+v, want 1 resumed / 0 dropped", out)
	}

	state, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["walker"]
		moving := a.MoveIntent != nil &&
			a.MoveIntent.Destination.Kind == sim.MoveDestinationPosition &&
			a.MoveIntent.Destination.Position != nil &&
			*a.MoveIntent.Destination.Position == target
		return []any{moving, a.ResumeDestination == nil}, nil
	}})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	v := state.([]any)
	if v[0] != true {
		t.Error("walker has no live MoveIntent for the checkpointed destination")
	}
	if v[1] != true {
		t.Error("ResumeDestination not cleared after resume")
	}
	if n := rec.countEvents(func(e sim.Event) bool {
		_, is := e.(*sim.ActorMoveStarted)
		return is
	}); n != 1 {
		t.Errorf("ActorMoveStarted count = %d, want 1", n)
	}
}

// TestResumeCheckpointedWalks_DropsInvalidDestination: a destination that
// no longer resolves (structure deleted during downtime) is dropped —
// logged, field cleared, no intent installed, world load unaffected.
func TestResumeCheckpointedWalks_DropsInvalidDestination(t *testing.T) {
	w, cancel, _ := buildSetPositionTestWorld(t)
	defer cancel()
	setResumeDestination(t, w, "walker", sim.NewStructureEnterDestination("razed-house"))

	res, err := w.Send(sim.ResumeCheckpointedWalks(time.Now()))
	if err != nil {
		t.Fatalf("ResumeCheckpointedWalks: %v", err)
	}
	out := res.(sim.ResumeWalksResult)
	if out.Resumed != 0 || out.Dropped != 1 {
		t.Errorf("result = %+v, want 0 resumed / 1 dropped", out)
	}

	state, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["walker"]
		return []any{a.MoveIntent == nil, a.ResumeDestination == nil}, nil
	}})
	v := state.([]any)
	if v[0] != true || v[1] != true {
		t.Errorf("after drop: intentCleared=%v resumeCleared=%v, want true/true", v[0], v[1])
	}
}

// TestResumeCheckpointedWalks_SkipsAlreadyMoving: under the mem repo the
// whole MoveIntent round-trips, so an actor can come back already walking.
// The sweep must not supersede that live walk — it just consumes the
// resume field.
func TestResumeCheckpointedWalks_SkipsAlreadyMoving(t *testing.T) {
	w, cancel, _ := buildSetPositionTestWorld(t)
	defer cancel()
	liveTarget := sim.Position{X: sim.PadX + 7, Y: sim.PadY + 7}
	if _, err := w.Send(sim.MoveActor("walker", sim.NewPositionDestination(liveTarget), false, time.Now())); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}
	setResumeDestination(t, w, "walker", sim.NewPositionDestination(sim.Position{X: sim.PadX + 20, Y: sim.PadY + 20}))

	res, err := w.Send(sim.ResumeCheckpointedWalks(time.Now()))
	if err != nil {
		t.Fatalf("ResumeCheckpointedWalks: %v", err)
	}
	out := res.(sim.ResumeWalksResult)
	if out.Resumed != 0 || out.Dropped != 0 {
		t.Errorf("result = %+v, want 0/0 (live walk untouched)", out)
	}

	state, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["walker"]
		keptLive := a.MoveIntent != nil &&
			a.MoveIntent.Destination.Position != nil &&
			*a.MoveIntent.Destination.Position == liveTarget
		return []any{keptLive, a.ResumeDestination == nil}, nil
	}})
	v := state.([]any)
	if v[0] != true {
		t.Error("live MoveIntent was superseded by the stale checkpointed walk")
	}
	if v[1] != true {
		t.Error("ResumeDestination not consumed on the skip path")
	}
}
