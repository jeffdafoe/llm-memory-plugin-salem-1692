package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// setInside marks an actor as physically inside a structure (the receiver-present
// precondition for a knock to be answered). buildMembershipTestWorld seeds
// everyone outside, so tests opt an actor in explicitly.
func setInside(t *testing.T, w *sim.World, actorID sim.ActorID, sid sim.StructureID) {
	t.Helper()
	// Use the real setter (exported for tests) so the outdoor-actors index and
	// room reconciliation stay consistent — a raw InsideStructureID write would
	// leave stale index entries and hide "inside"-scan bugs.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.SetActorInsideStructure(world, world.Actors[actorID], sid)
		return nil, nil
	}}); err != nil {
		t.Fatalf("set %s inside %s: %v", actorID, sid, err)
	}
}

// mutate runs fn against the world on its goroutine — a test setup escape hatch
// for tweaking seeded entry policy / door offsets per case.
func mutate(t *testing.T, w *sim.World, fn func(world *sim.World)) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		fn(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("mutate world: %v", err)
	}
}

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

func insideOf(t *testing.T, w *sim.World, actorID sim.ActorID) sim.StructureID {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[actorID].InsideStructureID, nil
	}})
	if err != nil {
		t.Fatalf("read %s inside: %v", actorID, err)
	}
	return res.(sim.StructureID)
}

// knockFlagOf reads the Knock stamp off the actor's in-flight MoveIntent
// destination. Fails the test when no MoveIntent is in flight.
func knockFlagOf(t *testing.T, w *sim.World, actorID sim.ActorID) bool {
	t.Helper()
	mi := moveIntentOf(t, w, actorID)
	if mi == nil {
		t.Fatalf("%s has no MoveIntent in flight", actorID)
	}
	return mi.Destination.Knock
}

// TestEnterOrKnock_MemberEnters: a member of an owner-only structure is routed
// through the door, not turned away — Knocked stays false and no service huddle
// forms (the inside flip happens on arrival, as for a bare StructureEnter).
func TestEnterOrKnock_MemberEnters(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	res, err := w.Send(sim.EnterOrKnock("spouse", "cottage", true, now))
	if err != nil {
		t.Fatalf("member EnterOrKnock: %v", err)
	}
	out := res.(sim.EnterOrKnockResult)
	if out.Knocked {
		t.Error("a member should enter, not knock")
	}
	if got := huddleOf(t, w, "spouse"); got != "" {
		t.Errorf("a member entering should not be huddled; got %q", got)
	}
	if out.MovementAttemptID == 0 {
		t.Error("expected a movement attempt to be stamped")
	}
	if knockFlagOf(t, w, "spouse") {
		t.Error("a member's enter walk should not carry the Knock stamp")
	}
}

// TestEnterOrKnock_StrangerKnocksKeeperInside: a non-member at an owner-only
// structure with a receptive receiver inside knocks — routed to the loiter slot
// with the destination stamped Knock=true. NO huddle forms at click
// (ZBBS-HOME-445: the service huddle forms on arrival, so the locomotion
// ticker's mover-leave rule has no mid-walk membership to evict and the
// businessowner farewell cascade sees no phantom departure), and the narration
// stays empty — the arrival-time greet is the "door answered" feedback.
func TestEnterOrKnock_StrangerKnocksKeeperInside(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	setInside(t, w, "servant", "cottage") // servant's WorkStructureID is cottage → the receiver

	res, err := w.Send(sim.EnterOrKnock("stranger", "cottage", true, now))
	if err != nil {
		t.Fatalf("stranger EnterOrKnock: %v", err)
	}
	out := res.(sim.EnterOrKnockResult)
	if !out.Knocked {
		t.Error("a non-member at an owner-only structure should knock")
	}
	if out.KnockNarration != "" {
		t.Errorf("a receiver is in — narration should be empty (the arrival greet is the feedback); got %q", out.KnockNarration)
	}
	if !knockFlagOf(t, w, "stranger") {
		t.Error("the knock walk should carry the Knock destination stamp")
	}
	if got := huddleOf(t, w, "stranger"); got != "" {
		t.Errorf("no huddle should form at click time; knocker in %q", got)
	}
	if got := huddleOf(t, w, "servant"); got != "" {
		t.Errorf("no huddle should form at click time; receiver in %q", got)
	}
}

// TestEnterOrKnock_StrangerKnocksNoKeeper: a non-member knocking with no
// receptive receiver inside still knocks (Knock stamp rides the walk — a
// receiver who returns mid-walk answers on arrival), and the click-time
// narration explains the likely-unanswered door.
func TestEnterOrKnock_StrangerKnocksNoKeeper(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	res, err := w.Send(sim.EnterOrKnock("stranger", "cottage", true, now))
	if err != nil {
		t.Fatalf("stranger EnterOrKnock: %v", err)
	}
	out := res.(sim.EnterOrKnockResult)
	if !out.Knocked {
		t.Error("a non-member at an owner-only structure should knock")
	}
	if out.KnockNarration == "" {
		t.Error("an unanswered-looking knock should carry narration")
	}
	if !knockFlagOf(t, w, "stranger") {
		t.Error("the Knock stamp should ride the walk even with no receiver at click time")
	}
	if got := huddleOf(t, w, "stranger"); got != "" {
		t.Errorf("an unanswered knock should not put the knocker in a huddle; got %q", got)
	}
}

// TestEnterOrKnock_SleepingReceiverIsNoReceiver: a receiver who is asleep
// inside does not answer — the click narrates the unanswered door, exactly as
// if no one were in (receptiveKnockReceivers applies the same standard the
// arrival join uses).
func TestEnterOrKnock_SleepingReceiverIsNoReceiver(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	setInside(t, w, "servant", "cottage")
	mutate(t, w, func(world *sim.World) {
		world.Actors["servant"].State = sim.StateSleeping
	})

	res, err := w.Send(sim.EnterOrKnock("stranger", "cottage", true, now))
	if err != nil {
		t.Fatalf("stranger EnterOrKnock: %v", err)
	}
	out := res.(sim.EnterOrKnockResult)
	if !out.Knocked {
		t.Error("a non-member at an owner-only structure should knock")
	}
	if out.KnockNarration == "" {
		t.Error("a sleeping receiver answers no knock — narration should say so")
	}
}

// TestEnterOrKnock_DoorlessOwnerOnlyIsVisit: an owner-only structure with no
// door offset can't be knocked on — a non-member routes to a plain visit, not a
// knock, and the walk carries no Knock stamp even with a receiver inside.
func TestEnterOrKnock_DoorlessOwnerOnlyIsVisit(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	mutate(t, w, func(world *sim.World) {
		a := world.Assets["cottage-asset"]
		a.DoorOffsetX, a.DoorOffsetY = nil, nil
	})
	setInside(t, w, "servant", "cottage")

	res, err := w.Send(sim.EnterOrKnock("stranger", "cottage", true, now))
	if err != nil {
		t.Fatalf("doorless EnterOrKnock: %v", err)
	}
	out := res.(sim.EnterOrKnockResult)
	if out.Knocked {
		t.Error("a doorless structure has no door to knock on — expected a plain visit")
	}
	if knockFlagOf(t, w, "stranger") {
		t.Error("a doorless visit should not carry the Knock stamp")
	}
	if got := huddleOf(t, w, "stranger"); got != "" {
		t.Errorf("doorless visit should not huddle the visitor; got %q", got)
	}
}

// TestEnterOrKnock_ClosedPolicyIsVisit: a closed structure (well-like, no
// interior) routes a structure_enter click to a plain visit — not a knock, no
// Knock stamp — even for a non-member.
func TestEnterOrKnock_ClosedPolicyIsVisit(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	mutate(t, w, func(world *sim.World) {
		world.VillageObjects["cottage"].EntryPolicy = sim.EntryPolicyClosed
	})

	res, err := w.Send(sim.EnterOrKnock("stranger", "cottage", true, now))
	if err != nil {
		t.Fatalf("closed EnterOrKnock: %v", err)
	}
	out := res.(sim.EnterOrKnockResult)
	if out.Knocked {
		t.Error("a closed structure is a plain visit, not a knock")
	}
	if knockFlagOf(t, w, "stranger") {
		t.Error("a closed-structure visit should not carry the Knock stamp")
	}
}

// TestEnterOrKnock_LeavesPriorHuddle: a knocker already in a huddle is not
// rejected — EnterOrKnock threads leaveHuddleFirst (the handler always passes
// true), so the prior huddle is left and the move proceeds. A bare move out of
// an active huddle would otherwise be rejected.
func TestEnterOrKnock_LeavesPriorHuddle(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.JoinHuddle("stranger", "cottage", "", now)); err != nil {
		t.Fatalf("seed prior huddle: %v", err)
	}
	prior := huddleOf(t, w, "stranger")
	if prior == "" {
		t.Fatal("stranger should be in a huddle after JoinHuddle")
	}

	if _, err := w.Send(sim.EnterOrKnock("stranger", "cottage", true, now)); err != nil {
		t.Fatalf("EnterOrKnock while already huddled should not be rejected: %v", err)
	}
	if after := huddleOf(t, w, "stranger"); after == prior {
		t.Errorf("knocker should have left the prior huddle %q", prior)
	}
}
