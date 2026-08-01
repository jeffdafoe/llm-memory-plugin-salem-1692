package cascade

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// decorative_encounter_test.go — LLM-582. A decorative actor is sprite-only
// and never ticked, so it can neither speak nor stamp huddle activity. An
// outdoor encounter that includes one mints a conversation nobody can advance
// AND freezes every member's movement (MoveActor rejects a huddled actor
// without LeaveHuddleFirst), until the 2h silence sweep. Live, two ducks that
// came ashore beside each other pinned one another for hours.
//
// Both directions matter and are covered below: a decorative arriver must not
// INITIATE an encounter, and a decorative bystander must not be GRABBED by a
// non-decorative arriver.

// buildDecorativeEncounterWorld seeds three actors on open ground within a
// tile of each other: a decorative duck, a second decorative duck, and a
// stateful villager. No structures — these are open-ground encounters, and a
// worked-structure loiter pin would be excluded by a different gate
// (InOpenLoiterStructureScope) and make the assertions ambiguous.
func buildDecorativeEncounterWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"duck": {
			ID: "duck", DisplayName: "Duck",
			Kind:  sim.KindDecorative,
			State: sim.StateIdle,
			Pos:   sim.TilePos{X: sim.PadX + 5, Y: sim.PadY + 5},
		},
		"drake": {
			ID: "drake", DisplayName: "Drake",
			Kind:  sim.KindDecorative,
			State: sim.StateIdle,
			Pos:   sim.TilePos{X: sim.PadX + 6, Y: sim.PadY + 5},
		},
		"villager": {
			ID: "villager", DisplayName: "Villager",
			Kind:  sim.KindNPCStateful,
			State: sim.StateIdle,
			Pos:   sim.TilePos{X: sim.PadX + 6, Y: sim.PadY + 6},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() { cancel(); <-done }
}

// huddleOf reads an actor's current huddle back off the world goroutine.
func huddleOf(t *testing.T, w *sim.World, actorID sim.ActorID) sim.HuddleID {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[actorID].CurrentHuddleID, nil
	}})
	if err != nil {
		t.Fatalf("read %s huddle: %v", actorID, err)
	}
	return res.(sim.HuddleID)
}

// dispatchEncounterArrival fires one arrival event through the world goroutine.
func dispatchEncounterArrival(t *testing.T, w *sim.World, actorID sim.ActorID, pos sim.Position, now time.Time) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handleArrivalEncounter(world, &sim.ActorArrived{
			ActorID:       actorID,
			FinalPosition: pos,
			At:            now,
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("dispatch arrival for %s: %v", actorID, err)
	}
}

// TestHandleArrivalEncounter_DecorativeArriverFormsNoHuddle: the live shape —
// one duck walks ashore beside another and neither is huddled by it.
func TestHandleArrivalEncounter_DecorativeArriverFormsNoHuddle(t *testing.T) {
	w, cleanup := buildDecorativeEncounterWorld(t)
	defer cleanup()

	dispatchEncounterArrival(t, w, "duck", sim.Position{X: sim.PadX + 5, Y: sim.PadY + 5}, time.Now().UTC())

	if got := huddleOf(t, w, "duck"); got != "" {
		t.Errorf("a decorative arriver must not form an encounter; duck in %q", got)
	}
	if got := huddleOf(t, w, "drake"); got != "" {
		t.Errorf("a decorative bystander must not be grabbed; drake in %q", got)
	}
	// The villager stands in range too — a decorative arriver must not pull a
	// real NPC into a huddle either, or the duck would still freeze a villager.
	if got := huddleOf(t, w, "villager"); got != "" {
		t.Errorf("a decorative arriver must not pull in a villager; villager in %q", got)
	}
}

// TestHandleArrivalEncounter_DecorativeBystanderNotGrabbed: the other
// direction — a real NPC arriving beside a duck forms no huddle with it. This
// is the case that would otherwise let ANY villager freeze a duck by standing
// next to it, not just another duck.
func TestHandleArrivalEncounter_DecorativeBystanderNotGrabbed(t *testing.T) {
	w, cleanup := buildDecorativeEncounterWorld(t)
	defer cleanup()

	dispatchEncounterArrival(t, w, "villager", sim.Position{X: sim.PadX + 6, Y: sim.PadY + 6}, time.Now().UTC())

	if got := huddleOf(t, w, "duck"); got != "" {
		t.Errorf("a decorative must not be grabbed by a villager's arrival; duck in %q", got)
	}
	if got := huddleOf(t, w, "drake"); got != "" {
		t.Errorf("a decorative must not be grabbed by a villager's arrival; drake in %q", got)
	}
	// The villager arrived with only decoratives in range, so there is nobody
	// left to meet and no huddle should have been minted at all.
	if got := huddleOf(t, w, "villager"); got != "" {
		t.Errorf("villager should have nobody to meet but is in %q", got)
	}
}

// TestHandleArrivalEncounter_NonDecorativesStillMeet is the control: with two
// ordinary NPCs in range the encounter still forms, so the two skip tests
// above are not vacuously passing on a world that never huddles anyone.
func TestHandleArrivalEncounter_NonDecorativesStillMeet(t *testing.T) {
	w, cleanup := buildDecorativeEncounterWorld(t)
	defer cleanup()

	// Promote a duck to an ordinary NPC — same positions, same arrival, so the
	// only thing that changed between this test and the one above is Kind.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["duck"].Kind = sim.KindNPCStateful
		return nil, nil
	}}); err != nil {
		t.Fatalf("promote duck: %v", err)
	}

	dispatchEncounterArrival(t, w, "villager", sim.Position{X: sim.PadX + 6, Y: sim.PadY + 6}, time.Now().UTC())

	if got := huddleOf(t, w, "villager"); got == "" {
		t.Error("two non-decorative actors in range should still form an encounter — the skip tests would be vacuous otherwise")
	}
	if got := huddleOf(t, w, "drake"); got != "" {
		t.Errorf("the still-decorative drake must stay out of it; drake in %q", got)
	}
}
