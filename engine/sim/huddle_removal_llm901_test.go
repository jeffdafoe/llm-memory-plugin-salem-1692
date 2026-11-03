package sim_test

import (
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// huddle_removal_llm901_test.go — LLM-901. An actor deleted from the world while
// in a huddle must leave it through the ordinary leave, and a member with no
// actor behind it must not outlive the next departure. Live case: the messenger
// was removed by visitor cleanup while eating at the Inn, stayed in Members, and
// every NPC there was shown "a stranger" for four and a half hours.
// Reuses buildHuddleTestWorld / setActor / huddleOf / sendT (sibling tests).

// huddleMembersOf returns the sorted member ids of huddleID, or nil when the
// huddle is gone.
func huddleMembersOf(t *testing.T, w *sim.World, huddleID sim.HuddleID) []sim.ActorID {
	t.Helper()
	res := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		h := world.Huddles[huddleID]
		if h == nil {
			return []sim.ActorID(nil), nil
		}
		out := make([]sim.ActorID, 0, len(h.Members))
		for id := range h.Members {
			out = append(out, id)
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out, nil
	}})
	return res.([]sim.ActorID)
}

// recordHuddleLefts subscribes a recorder for HuddleLeft events and returns a
// reader of the ids that left, in emit order.
func recordHuddleLefts(t *testing.T, w *sim.World) func() []sim.ActorID {
	t.Helper()
	var lefts []sim.ActorID
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			if l, ok := evt.(*sim.HuddleLeft); ok {
				lefts = append(lefts, l.ActorID)
			}
		}))
		return nil, nil
	}})
	return func() []sim.ActorID {
		res := sendT(t, w, sim.Command{Fn: func(*sim.World) (any, error) {
			return append([]sim.ActorID(nil), lefts...), nil
		}})
		return res.([]sim.ActorID)
	}
}

func containsActor(ids []sim.ActorID, id sim.ActorID) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// TestVisitorCleanup_LeavesHuddle: the live case. A visitor past his cleanup
// grace is still in a huddle with alice and bob. Cleanup must take him out of
// Members (with a HuddleLeft), and the conversation goes on without him.
func TestVisitorCleanup_LeavesHuddle(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	setActor(t, w, "alice", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful })
	setActor(t, w, "bob", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful })
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["roger"] = &sim.Actor{
			ID:          "roger",
			DisplayName: "Roger Standish the messenger",
			Kind:        sim.KindNPCShared,
			LLMAgent:    sim.VisitorAgentName,
			Pos:         sim.TilePos{X: sim.PadX + 5, Y: sim.PadY + 5},
			Needs:       sim.SeedVisitorNeedsForTest(),
			VisitorState: &sim.VisitorState{
				Archetype: "messenger",
				ExpiresAt: now.Add(-time.Duration(sim.VisitorCleanupGraceMinutes+1) * time.Minute),
				Phase:     sim.VisitorPhaseDeparting,
			},
		}
		sim.RebuildIndicesForTest(world)
		return nil, nil
	}})
	sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now))
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	sendT(t, w, sim.JoinHuddle("roger", "tavern", "", now))
	hud := huddleOf(t, w, "alice")
	lefts := recordHuddleLefts(t, w)

	res := sendT(t, w, sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now, Rand: rand.New(rand.NewSource(0))}))
	if tm := res.(sim.VisitorCascadeTelemetry); tm.CleanedUp != 1 {
		t.Fatalf("CleanedUp = %d, want 1", tm.CleanedUp)
	}

	if got := huddleMembersOf(t, w, hud); len(got) != 2 || containsActor(got, "roger") {
		t.Errorf("huddle members after cleanup = %v, want [alice bob]", got)
	}
	if !containsActor(lefts(), "roger") {
		t.Error("visitor cleanup emitted no HuddleLeft for the removed visitor")
	}
	if h := huddleOf(t, w, "alice"); h != hud {
		t.Errorf("alice's huddle = %q, want %q (the conversation goes on)", h, hud)
	}
}

// TestVisitorCleanup_LastMemberConcludesHuddle: a visitor removed as the only
// member concludes the huddle, as any last leave does.
func TestVisitorCleanup_LastMemberConcludesHuddle(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["roger"] = &sim.Actor{
			ID:          "roger",
			DisplayName: "Roger Standish the messenger",
			Kind:        sim.KindNPCShared,
			LLMAgent:    sim.VisitorAgentName,
			Pos:         sim.TilePos{X: sim.PadX + 5, Y: sim.PadY + 5},
			Needs:       sim.SeedVisitorNeedsForTest(),
			VisitorState: &sim.VisitorState{
				Archetype: "messenger",
				ExpiresAt: now.Add(-time.Duration(sim.VisitorCleanupGraceMinutes+1) * time.Minute),
				Phase:     sim.VisitorPhaseDeparting,
			},
		}
		sim.RebuildIndicesForTest(world)
		return nil, nil
	}})
	sendT(t, w, sim.JoinHuddle("roger", "tavern", "", now))
	hud := huddleOf(t, w, "roger")

	sendT(t, w, sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now, Rand: rand.New(rand.NewSource(0))}))

	concluded := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		h := world.Huddles[hud]
		return h != nil && h.ConcludedAt != nil && len(h.Members) == 0, nil
	}}).(bool)
	if !concluded {
		t.Error("huddle should have concluded when its only member was removed")
	}
}

// TestDeleteActor_LeavesHuddle: the editor delete of an NPC mid-conversation.
// Besides the ghost, a non-visitor id left in Members would be checkpointed into
// huddle_member and fail the next boot's actor-huddle reconciliation.
func TestDeleteActor_LeavesHuddle(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	setActor(t, w, "alice", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful })
	setActor(t, w, "bob", func(a *sim.Actor) { a.Kind = sim.KindNPCShared })
	sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now))
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	hud := huddleOf(t, w, "alice")
	lefts := recordHuddleLefts(t, w)

	sendT(t, w, sim.DeleteActor("bob"))

	if got := huddleMembersOf(t, w, hud); len(got) != 1 || got[0] != "alice" {
		t.Errorf("huddle members after delete = %v, want [alice]", got)
	}
	if !containsActor(lefts(), "bob") {
		t.Error("DeleteActor emitted no HuddleLeft for the deleted NPC")
	}
}

// TestLeave_PrunesDepartedMembers: the backstop. A member deleted from the world
// without leaving (a removal path that skips the leave) is cleared at the next
// ordinary departure, at ANY member count — not only once it is the last one.
// Before LLM-901 only a lone ghost was evicted, so a busy room kept it for hours.
func TestLeave_PrunesDepartedMembers(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	setActor(t, w, "alice", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful })
	setActor(t, w, "bob", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful })
	setActor(t, w, "charlie", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful })
	sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now))
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	sendT(t, w, sim.JoinHuddle("charlie", "tavern", "", now))
	hud := huddleOf(t, w, "alice")
	// Delete charlie from the world but leave his membership ref in the huddle.
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.Actors, "charlie")
		return nil, nil
	}})
	lefts := recordHuddleLefts(t, w)

	res := sendT(t, w, sim.LeaveHuddle("alice", now)).(sim.LeaveHuddleResult)

	if res.Concluded {
		t.Error("huddle should not conclude — bob is still in it")
	}
	if containsActor(res.RemainingMembers, "charlie") {
		t.Errorf("leave result names the ghost as remaining: %v", res.RemainingMembers)
	}
	if got := huddleMembersOf(t, w, hud); len(got) != 1 || got[0] != "bob" {
		t.Errorf("huddle members after leave = %v, want [bob]", got)
	}
	if !containsActor(lefts(), "charlie") {
		t.Error("no HuddleLeft emitted for the pruned ghost")
	}
}

// TestLeave_PruneEventsNeverNameAGhost: with two ghosts and a live survivor,
// no HuddleLeft emitted during the leave may list a ghost (or anyone gone) in
// RemainingMembers — subscribers must only ever see resolvable members
// (code_review round 1).
func TestLeave_PruneEventsNeverNameAGhost(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["dora"] = &sim.Actor{ID: "dora", DisplayName: "Dora"}
		sim.RebuildIndicesForTest(world)
		return nil, nil
	}})
	for _, id := range []sim.ActorID{"alice", "bob", "charlie", "dora"} {
		setActor(t, w, id, func(a *sim.Actor) { a.Kind = sim.KindNPCStateful })
		sendT(t, w, sim.JoinHuddle(id, "tavern", "", now))
	}
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.Actors, "charlie")
		delete(world.Actors, "dora")
		return nil, nil
	}})
	var events []sim.HuddleLeft
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			if l, ok := evt.(*sim.HuddleLeft); ok {
				events = append(events, *l)
			}
		}))
		return nil, nil
	}})

	sendT(t, w, sim.LeaveHuddle("alice", now))

	got := sendT(t, w, sim.Command{Fn: func(*sim.World) (any, error) {
		return append([]sim.HuddleLeft(nil), events...), nil
	}}).([]sim.HuddleLeft)
	var left []sim.ActorID
	for _, e := range got {
		left = append(left, e.ActorID)
		if len(e.RemainingMembers) != 1 || e.RemainingMembers[0] != "bob" {
			t.Errorf("HuddleLeft for %q: remaining = %v, want [bob]", e.ActorID, e.RemainingMembers)
		}
	}
	for _, want := range []sim.ActorID{"alice", "charlie", "dora"} {
		if !containsActor(left, want) {
			t.Errorf("no HuddleLeft for %q (got %v)", want, left)
		}
	}
}
