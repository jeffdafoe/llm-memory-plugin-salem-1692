package sim_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// knock_at_door_test.go — LLM-785. The player's knock from the door popup:
// answered when someone awake is in, unanswered (with the private line)
// otherwise; refused away from the door, while walking, or at a door they may
// walk through.

// playerAtCottageDoor makes the stranger a live player and stands them at the
// cottage's door (its loiter pin).
func playerAtCottageDoor(t *testing.T, w *sim.World, now time.Time) {
	t.Helper()
	mutate(t, w, func(world *sim.World) {
		a := world.Actors["stranger"]
		a.Kind = sim.KindPC
		a.LastPCSeenAt = &now
		pin, ok := sim.ObjectLoiterPin(world.VillageObjects, world.Assets, "cottage")
		if !ok {
			t.Fatal("harness: the cottage has no loiter pin")
		}
		if _, err := sim.SetActorPosition("stranger", pin, now).Fn(world); err != nil {
			t.Fatalf("harness: put the stranger at the door: %v", err)
		}
	})
}

func knock(t *testing.T, w *sim.World, now time.Time) (sim.KnockResult, error) {
	t.Helper()
	res, err := w.Send(sim.KnockAtDoor("stranger", "cottage", now))
	if err != nil {
		return sim.KnockResult{}, err
	}
	return res.(sim.KnockResult), nil
}

func TestKnockAtDoorAnswered(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	setInside(t, w, "servant", "cottage")
	playerAtCottageDoor(t, w, now)
	if huddleOf(t, w, "stranger") != "" {
		t.Fatal("harness: standing at the door already formed a conversation")
	}
	out, err := knock(t, w, now)
	if err != nil || !out.Answered {
		t.Fatalf("knock with the servant in: %+v %v, want answered", out, err)
	}
	if h := huddleOf(t, w, "stranger"); h == "" || h != huddleOf(t, w, "servant") {
		t.Errorf("after the knock: stranger in %q, servant in %q, want one conversation", h, huddleOf(t, w, "servant"))
	}
	// Knocking again on an answered door changes nothing.
	before := huddleOf(t, w, "stranger")
	if out, err = knock(t, w, now); err != nil || !out.Answered || huddleOf(t, w, "stranger") != before {
		t.Errorf("a second knock: %+v %v, conversation %q -> %q", out, err, before, huddleOf(t, w, "stranger"))
	}
}

func TestKnockAtDoorUnanswered(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	var told int
	mutate(t, w, func(world *sim.World) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, e sim.Event) {
			if k, ok := e.(*sim.KnockUnansweredNarrated); ok && k.ActorID == "stranger" {
				told++
			}
		}))
	})
	playerAtCottageDoor(t, w, now)
	out, err := knock(t, w, now)
	if err != nil || out.Answered {
		t.Fatalf("knock with nobody in: %+v %v, want unanswered", out, err)
	}
	if told != 1 {
		t.Errorf("unanswered knock told %d times, want once", told)
	}
}

func TestKnockAtDoorRefusals(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	refused := func(label string) {
		t.Helper()
		if _, err := knock(t, w, now); !errors.Is(err, sim.ErrKnockRefused) {
			t.Errorf("%s: err %v, want ErrKnockRefused", label, err)
		}
	}
	setInside(t, w, "servant", "cottage")
	mutate(t, w, func(world *sim.World) {
		world.Actors["stranger"].Kind = sim.KindPC
		world.Actors["stranger"].LastPCSeenAt = &now
	})
	// Away from the door (the fixture's starting tile).
	refused("away from the door")
	playerAtCottageDoor(t, w, now)
	// Walking off.
	if _, err := w.Send(sim.MoveActor("stranger", sim.NewPositionDestination(sim.Position{X: sim.PadX + 4, Y: sim.PadY}), true, now)); err != nil {
		t.Fatalf("harness: walk off: %v", err)
	}
	refused("walking")
	mutate(t, w, func(world *sim.World) { world.Actors["stranger"].MoveIntent = nil })
	// A door they may walk through: no knock needed.
	mutate(t, w, func(world *sim.World) { world.Actors["stranger"].HomeStructureID = "cottage" })
	refused("their own home")
	if huddleOf(t, w, "stranger") != "" || huddleOf(t, w, "servant") != "" {
		t.Error("a refused knock formed a conversation")
	}
}

// TestKnockAtDoorRefusedAtMarketStall: a market stall has no door to knock on —
// a patron at its counter is served there — so a knock there is refused.
func TestKnockAtDoorRefusedAtMarketStall(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	setInside(t, w, "servant", "cottage")
	mutate(t, w, func(world *sim.World) {
		obj := world.VillageObjects["cottage"]
		obj.Tags = append(obj.Tags, sim.TagMarketStall)
	})
	playerAtCottageDoor(t, w, now)
	if _, err := knock(t, w, now); !errors.Is(err, sim.ErrKnockRefused) {
		t.Errorf("knock at a market stall: err %v, want ErrKnockRefused", err)
	}
}
