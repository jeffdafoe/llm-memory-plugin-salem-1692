package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// setInside marks an actor as physically inside a structure (the keeper-present
// precondition for a knock to form a service huddle). buildMembershipTestWorld
// seeds everyone outside, so tests opt an actor in explicitly.
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
	if out.HuddleJoined {
		t.Error("a member entering should not form a knock huddle")
	}
	if out.MovementAttemptID == 0 {
		t.Error("expected a movement attempt to be stamped")
	}
}

// TestEnterOrKnock_StrangerKnocksKeeperInside: a non-member at an owner-only
// structure with an associated keeper inside knocks — routed to the loiter slot
// (stays outside), and shares a newly-formed service huddle with the keeper so
// speak/pay (both huddle-scoped) work across the doorway.
func TestEnterOrKnock_StrangerKnocksKeeperInside(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	setInside(t, w, "servant", "cottage") // servant's WorkStructureID is cottage → the keeper

	res, err := w.Send(sim.EnterOrKnock("stranger", "cottage", true, now))
	if err != nil {
		t.Fatalf("stranger EnterOrKnock: %v", err)
	}
	out := res.(sim.EnterOrKnockResult)
	if !out.Knocked {
		t.Error("a non-member at an owner-only structure should knock")
	}
	if !out.HuddleJoined {
		t.Error("a keeper is inside, so a service huddle should form")
	}
	if out.KnockNarration == "" {
		t.Error("a knock should carry narration")
	}
	if got := insideOf(t, w, "stranger"); got != "" {
		t.Errorf("knocker should stay physically outside; InsideStructureID = %q", got)
	}
	stranger, keeper := huddleOf(t, w, "stranger"), huddleOf(t, w, "servant")
	if stranger == "" || stranger != keeper {
		t.Errorf("knocker and keeper should share one huddle; stranger=%q servant=%q", stranger, keeper)
	}
}

// TestEnterOrKnock_StrangerKnocksNoKeeper: a non-member knocking with no keeper
// inside is still routed to the loiter slot and knocks, but no huddle is minted
// (don't strand the knocker alone in a one-person service huddle).
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
	if out.HuddleJoined {
		t.Error("no keeper inside → no service huddle should form")
	}
	if out.KnockNarration == "" {
		t.Error("an unanswered knock should still carry narration")
	}
	if got := huddleOf(t, w, "stranger"); got != "" {
		t.Errorf("an unanswered knock should not put the knocker in a huddle; got %q", got)
	}
}

// TestEnterOrKnock_DoorlessOwnerOnlyIsVisit: an owner-only structure with no
// door offset can't be knocked on — a non-member routes to a plain visit, not a
// knock, and no service huddle forms even with a keeper inside.
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
	if out.HuddleJoined {
		t.Error("a doorless visit should not form a service huddle")
	}
	if got := huddleOf(t, w, "stranger"); got != "" {
		t.Errorf("doorless visit should not huddle the visitor; got %q", got)
	}
}

// TestEnterOrKnock_ClosedPolicyIsVisit: a closed structure (well-like, no
// interior) routes a structure_enter click to a plain visit — not a knock, no
// huddle — even for a non-member.
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
	if out.HuddleJoined {
		t.Error("a closed-structure visit should not form a huddle")
	}
}

// TestEnterOrKnock_MultipleKeepersAllJoin: with several associated keepers
// inside, the knock pulls them all into one shared service huddle with the
// knocker (deterministic sorted join order, same resulting huddle).
func TestEnterOrKnock_MultipleKeepersAllJoin(t *testing.T) {
	w, cancel := buildMembershipTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	setInside(t, w, "servant", "cottage") // WorkStructureID == cottage
	setInside(t, w, "spouse", "cottage")  // HomeStructureID == cottage

	res, err := w.Send(sim.EnterOrKnock("stranger", "cottage", true, now))
	if err != nil {
		t.Fatalf("multi-keeper EnterOrKnock: %v", err)
	}
	out := res.(sim.EnterOrKnockResult)
	if !out.HuddleJoined {
		t.Fatal("keepers inside → a service huddle should form")
	}
	h := huddleOf(t, w, "stranger")
	if h == "" {
		t.Fatal("knocker should be in a huddle")
	}
	if got := huddleOf(t, w, "servant"); got != h {
		t.Errorf("servant should share the knocker's huddle; got %q want %q", got, h)
	}
	if got := huddleOf(t, w, "spouse"); got != h {
		t.Errorf("spouse should share the knocker's huddle; got %q want %q", got, h)
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
