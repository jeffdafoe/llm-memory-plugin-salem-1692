package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// huddle_dissolve_test.go — ZBBS-HOME-363. Degenerate-huddle dissolve: when a
// leave drops a huddle to a single RESTING member (asleep / on-break, who won't
// tick to leave it themselves), that member is evicted and the huddle concludes.
// An ACTIVE lone member keeps the transient huddle (the system's existing model:
// they tick on the HuddlePeerLeft warrant and decide for themselves).
// Reuses buildHuddleTestWorld / setActor / huddleOf / sendT (sibling tests).

// TestHuddleDissolve_RestingLoneMemberEvicted: bob is on break; alice leaves;
// bob is the lone remaining member, so the degenerate huddle dissolves under
// him (the live hud-ce173 / on-break-keeper case).
func TestHuddleDissolve_RestingLoneMemberEvicted(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	breakUntil := now.Add(time.Hour)
	setActor(t, w, "alice", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful })
	setActor(t, w, "bob", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.BreakUntil = &breakUntil // on break — won't tick to leave on its own
	})
	sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now))
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))

	res := sendT(t, w, sim.LeaveHuddle("alice", now)).(sim.LeaveHuddleResult)
	if !res.Concluded {
		t.Error("huddle should have concluded — the lone remaining member was resting")
	}
	if h := huddleOf(t, w, "bob"); h != "" {
		t.Errorf("resting lone member bob should have been evicted, still in %q", h)
	}
}

// TestHuddleDissolve_MissingLoneMemberEvicted: a lone remaining member that is
// a stale ref (in the huddle but already deleted from the world) can never tick
// to leave, so it's the same stale-degenerate-huddle class as a resting member
// and must be evicted too (code_review). We delete bob from w.Actors while
// leaving him in the huddle, then alice leaves.
func TestHuddleDissolve_MissingLoneMemberEvicted(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	setActor(t, w, "alice", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful })
	setActor(t, w, "bob", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful })
	sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now))
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	// Delete bob from the world but leave his membership ref in the huddle.
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.Actors, "bob")
		return nil, nil
	}})

	res := sendT(t, w, sim.LeaveHuddle("alice", now)).(sim.LeaveHuddleResult)
	if !res.Concluded {
		t.Error("huddle should conclude — the lone remaining member is a stale/missing ref")
	}
}

// TestHuddleDissolve_ActiveLoneMemberPersists: the control. bob is active;
// alice leaves; bob remains in the (now 1-member) huddle and it does NOT
// conclude — preserving the transient model the rest of the system relies on.
func TestHuddleDissolve_ActiveLoneMemberPersists(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	setActor(t, w, "alice", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful })
	setActor(t, w, "bob", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful }) // active
	sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now))
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	hud := huddleOf(t, w, "bob")

	res := sendT(t, w, sim.LeaveHuddle("alice", now)).(sim.LeaveHuddleResult)
	if res.Concluded {
		t.Error("huddle should NOT conclude — active lone member keeps the transient huddle")
	}
	if h := huddleOf(t, w, "bob"); h != hud {
		t.Errorf("active lone member bob should remain in %q, got %q", hud, h)
	}
}
