package sim_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// let_in_test.go — LLM-20. A resident lets a knocker in: the knocker gets a
// guest pass and steps inside at once, still in the conversation; the pass is
// a membership leg until it runs out. Someone already allowed in steps in
// without a pass. Refused for anyone who is not of the household, from
// outside, with nobody at the door, or with two at the door of one name; and
// nothing changes when the step in cannot happen.

// doorInside gives the cottage a footprint reaching its door (offset 0, 2), as
// every live house type has, so standing on the door tile is being inside.
func doorInside(t *testing.T, w *sim.World) {
	t.Helper()
	mutate(t, w, func(world *sim.World) { world.Assets["cottage-asset"].FootprintBottom = 2 })
}

// knockAt puts actorID at the cottage's door (its loiter pin, where a knock
// stops) and forms the doorway conversation with whoever is in.
func knockAt(t *testing.T, w *sim.World, actorID sim.ActorID, now time.Time) {
	t.Helper()
	mutate(t, w, func(world *sim.World) {
		pin, ok := sim.ObjectLoiterPin(world.VillageObjects, world.Assets, "cottage")
		if !ok {
			t.Fatal("harness: the cottage has no loiter pin")
		}
		if _, err := sim.SetActorPosition(actorID, pin, now).Fn(world); err != nil {
			t.Fatalf("harness: put %s at the door: %v", actorID, err)
		}
	})
	if _, err := w.Send(sim.EnsureKnockServiceHuddle(actorID, "cottage", now)); err != nil {
		t.Fatalf("EnsureKnockServiceHuddle: %v", err)
	}
	if huddleOf(t, w, actorID) == "" {
		t.Fatalf("harness: %s's knock formed no conversation", actorID)
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

func hasPass(t *testing.T, w *sim.World, actorID sim.ActorID) bool {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		_, ok := world.GuestPasses["cottage"][actorID]
		return ok, nil
	}})
	if err != nil {
		t.Fatalf("read pass: %v", err)
	}
	return res.(bool)
}

// refusedLetIn asserts a model-facing refusal.
func refusedLetIn(t *testing.T, w *sim.World, label string, host sim.ActorID, name string, now time.Time) {
	t.Helper()
	_, err := w.Send(sim.LetIn(host, name, now))
	var mf sim.ModelFacingError
	if !errors.As(err, &mf) {
		t.Errorf("%s: err %v, want a model-facing refusal", label, err)
	}
}

func TestLetInStepsTheKnockerInside(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	doorInside(t, w)
	setInside(t, w, "spouse", "cottage")
	knockAt(t, w, "stranger", now)
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

// Someone already allowed in — the servant works here — standing at the door
// is stepped in like anyone, and given no pass.
func TestLetInAlreadyAllowedNeedsNoPass(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	doorInside(t, w)
	setInside(t, w, "spouse", "cottage")
	knockAt(t, w, "servant", now)
	if _, err := w.Send(sim.LetIn("spouse", "servant", now)); err != nil {
		t.Fatalf("LetIn: %v", err)
	}
	if got := insideOf(t, w, "servant"); got != "cottage" {
		t.Errorf("the servant is inside %q, want the cottage", got)
	}
	if hasPass(t, w, "servant") {
		t.Error("someone already allowed in was given a pass")
	}
}

func TestLetInRefusals(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	doorInside(t, w)
	setInside(t, w, "spouse", "cottage")
	refusedLetIn(t, w, "nobody at the door", "spouse", "stranger", now)
	knockAt(t, w, "stranger", now)
	// The servant works here but does not live here: not theirs to open.
	setInside(t, w, "servant", "cottage")
	refusedLetIn(t, w, "staff", "servant", "stranger", now)
	// The owner, from outside the house.
	refusedLetIn(t, w, "the owner, outdoors", "homeowner", "stranger", now)
	// The owner inside, but not in the doorway conversation (it formed before
	// they came in): the stranger is not at the door of their conversation.
	setInside(t, w, "homeowner", "cottage")
	if huddleOf(t, w, "homeowner") == huddleOf(t, w, "stranger") {
		t.Fatal("harness: the owner is in the doorway conversation")
	}
	refusedLetIn(t, w, "owner outside the doorway conversation", "homeowner", "stranger", now)
	if got := insideOf(t, w, "stranger"); got != "" {
		t.Errorf("after refusals the stranger is inside %q", got)
	}
	if hasPass(t, w, "stranger") {
		t.Error("a refusal left a pass behind")
	}
}

// A market stall has no door to open (LLM-793): its customer is served across
// the counter, so even one who lives in it lets nobody in. The same shape as
// TestLetInStepsTheKnockerInside, the cottage tagged a stall.
func TestLetInRefusedAtAMarketStall(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	doorInside(t, w)
	mutate(t, w, func(world *sim.World) {
		world.VillageObjects["cottage"].Tags = append(world.VillageObjects["cottage"].Tags, sim.TagMarketStall)
	})
	setInside(t, w, "spouse", "cottage")
	knockAt(t, w, "stranger", now)
	refusedLetIn(t, w, "resident at a stall", "spouse", "stranger", now)
	if got := insideOf(t, w, "stranger"); got != "" {
		t.Errorf("the customer is inside %q, want outside at the counter", got)
	}
	if hasPass(t, w, "stranger") {
		t.Error("a refusal at a stall left a pass behind")
	}
}

// Two at the door sharing a name: which one is meant cannot be told, so
// nobody is let in.
func TestLetInTwoAtTheDoorOfOneName(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	doorInside(t, w)
	setInside(t, w, "spouse", "cottage")
	mutate(t, w, func(world *sim.World) {
		world.Actors["stranger"].DisplayName = "Goodman Hale"
		world.Actors["boarder"].DisplayName = "Goodman Hale"
		world.Actors["boarder"].RoomAccess = nil
	})
	knockAt(t, w, "stranger", now)
	knockAt(t, w, "boarder", now)
	if huddleOf(t, w, "stranger") != huddleOf(t, w, "boarder") {
		t.Fatal("harness: the two knockers are not in one conversation")
	}
	refusedLetIn(t, w, "two of one name", "spouse", "Goodman Hale", now)
	if insideOf(t, w, "stranger") != "" || insideOf(t, w, "boarder") != "" {
		t.Error("someone was let in when two at the door share the name")
	}
}

// A door tile outside the house's footprint: the step in cannot make the
// visitor inside, so nothing changes — no move, no pass.
func TestLetInDoorOutsideTheHouseChangesNothing(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	setInside(t, w, "spouse", "cottage")
	knockAt(t, w, "stranger", now)
	before := huddleOf(t, w, "stranger")
	if _, err := w.Send(sim.LetIn("spouse", "stranger", now)); err == nil {
		t.Fatal("LetIn succeeded with the door tile outside the house")
	}
	if insideOf(t, w, "stranger") != "" || hasPass(t, w, "stranger") || huddleOf(t, w, "stranger") != before {
		t.Error("a failed let_in moved the stranger, left a pass, or broke the conversation")
	}
}
