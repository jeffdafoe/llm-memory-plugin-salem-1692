package sim_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// let_in_test.go — LLM-20. A resident lets a knocker in: the knocker gets a
// guest pass and steps inside at once, still in the conversation; the pass is
// a membership leg until it runs out. Refused for anyone who is not of the
// household, from outside, or with nobody at the door.

// knockAtCottage puts the stranger at the cottage's door (its loiter pin, where
// a knock stops) and forms the doorway conversation with whoever is in.
func knockAtCottage(t *testing.T, w *sim.World, now time.Time) {
	t.Helper()
	mutate(t, w, func(world *sim.World) {
		// A footprint reaching the door (offset 0, 2), as every live house has,
		// so standing on the door tile is being inside.
		world.Assets["cottage-asset"].FootprintBottom = 2
		pin, ok := sim.ObjectLoiterPin(world.VillageObjects, world.Assets, "cottage")
		if !ok {
			t.Fatal("harness: the cottage has no loiter pin")
		}
		if _, err := sim.SetActorPosition("stranger", pin, now).Fn(world); err != nil {
			t.Fatalf("harness: put the stranger at the door: %v", err)
		}
	})
	if _, err := w.Send(sim.EnsureKnockServiceHuddle("stranger", "cottage", now)); err != nil {
		t.Fatalf("EnsureKnockServiceHuddle: %v", err)
	}
	if huddleOf(t, w, "stranger") == "" {
		t.Fatal("harness: the knock formed no conversation")
	}
}

func allowsIn(t *testing.T, w *sim.World, actorID sim.ActorID, when time.Time) bool {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.StructureMembershipAllows(world, world.Actors[actorID], "cottage", when), nil
	}})
	if err != nil {
		t.Fatalf("membership: %v", err)
	}
	return res.(bool)
}

func TestLetInStepsTheKnockerInside(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	setInside(t, w, "spouse", "cottage")
	knockAtCottage(t, w, now)
	talk := huddleOf(t, w, "spouse")
	if allowsIn(t, w, "stranger", now) {
		t.Fatal("harness: the stranger may already come in")
	}

	// The stranger has no name the spouse could give: the one at the door is
	// let in whatever name is said.
	res, err := w.Send(sim.LetIn("spouse", "the stranger", now))
	if err != nil {
		t.Fatalf("LetIn: %v", err)
	}
	if r, ok := res.(sim.LetInResult); !ok || r.VisitorID != "stranger" || r.StructureID != "cottage" {
		t.Errorf("LetIn result %#v", res)
	}
	if got := insideOf(t, w, "stranger"); got != "cottage" {
		t.Errorf("after let_in the stranger is inside %q, want the cottage", got)
	}
	if got := huddleOf(t, w, "stranger"); got == "" || got != talk || huddleOf(t, w, "spouse") != talk {
		t.Errorf("after let_in: stranger in %q, spouse in %q, want both still in %q", got, huddleOf(t, w, "spouse"), talk)
	}
	// The pass: in for its length, not after.
	if !allowsIn(t, w, "stranger", now.Add(sim.GuestPassDuration-time.Minute)) {
		t.Error("a let-in guest may not come and go while the pass lasts")
	}
	if allowsIn(t, w, "stranger", now.Add(sim.GuestPassDuration+time.Minute)) {
		t.Error("the pass still lets the guest in after it ran out")
	}
}

func TestLetInRefusals(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	refused := func(label string, host sim.ActorID) {
		t.Helper()
		_, err := w.Send(sim.LetIn(host, "stranger", now))
		var mf sim.ModelFacingError
		if !errors.As(err, &mf) {
			t.Errorf("%s: err %v, want a model-facing refusal", label, err)
		}
	}
	setInside(t, w, "spouse", "cottage")
	// Nobody at the door yet.
	refused("nobody at the door", "spouse")
	knockAtCottage(t, w, now)
	// The servant works here but does not live here: not theirs to open.
	setInside(t, w, "servant", "cottage")
	refused("staff", "servant")
	// From outside the house.
	refused("the owner, outdoors", "homeowner")
	if got := insideOf(t, w, "stranger"); got != "" {
		t.Errorf("after refusals the stranger is inside %q", got)
	}
	// The owner, inside, may.
	setInside(t, w, "homeowner", "cottage")
	if _, err := w.Send(sim.LetIn("homeowner", "stranger", now)); err == nil {
		// The owner was not in the doorway conversation when it formed, so the
		// stranger is not in theirs: refused too, without stepping anyone in.
		if insideOf(t, w, "stranger") == "cottage" && huddleOf(t, w, "homeowner") != huddleOf(t, w, "stranger") {
			t.Error("the owner let in someone outside their conversation")
		}
	}
}
