package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// huddle_skip_dissolve_test.go — ZBBS-HOME-413. A noop-skipped reactor tick
// dissolves the actor's dead SOLO huddle. The skip gate (handlers.shouldSkipNoop)
// only fires when the actor has no co-present huddle peer, so a skip while still
// pinned in a one-member huddle means the conversation is over and no one is
// left — yet post-WORK-367 the lone member never ticks itself out. The dissolve
// runs in CompleteReactorTick on TickStatusSkipped. Reuses buildHuddleTestWorld
// / setActor / huddleOf / sendT (sibling huddle tests).

// completeTickWithStatus puts the actor mid-tick under a fixed attempt id, then
// completes that tick with the given terminal status (asserting the completion
// is not stale). Mirrors the in-flight setup the reactor tests use.
func completeTickWithStatus(t *testing.T, w *sim.World, id sim.ActorID, status sim.TickTerminalStatus, now time.Time) {
	t.Helper()
	const attempt = sim.TickAttemptID("tk-skip")
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		a.TickInFlight = true
		a.TickAttemptID = attempt
		return nil, nil
	}})
	res := sendT(t, w, sim.CompleteReactorTick(id, attempt, sim.TickResult{TerminalStatus: status}, now)).(sim.CompleteReactorTickResult)
	if res.Stale {
		t.Fatalf("CompleteReactorTick(%s) unexpectedly stale", id)
	}
}

// soloHuddleForAlice joins alice + bob, then has bob leave so alice is the lone
// (idle) remaining member. Under HOME-363's resting-only gate an active lone
// member persists, so this leaves alice genuinely stuck in a 1-member huddle —
// the exact state HOME-413's skip-time dissolve targets. Returns the huddle id.
func soloHuddleForAlice(t *testing.T, w *sim.World, now time.Time) sim.HuddleID {
	t.Helper()
	setActor(t, w, "alice", func(a *sim.Actor) {
		a.Kind = sim.KindNPCStateful
		a.State = sim.StateIdle // active — HOME-363 won't evict her at bob's leave
	})
	setActor(t, w, "bob", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful })
	sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now))
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	hud := huddleOf(t, w, "alice")
	sendT(t, w, sim.LeaveHuddle("bob", now.Add(time.Second)))
	if h := huddleOf(t, w, "alice"); h != hud {
		t.Fatalf("precondition: alice should still be the lone member of %q, got %q", hud, h)
	}
	return hud
}

// A noop-skipped tick dissolves alice's dead solo huddle.
func TestCompleteReactorTick_SkippedDissolvesSoloHuddle(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	soloHuddleForAlice(t, w, now)

	completeTickWithStatus(t, w, "alice", sim.TickStatusSkipped, now.Add(2*time.Second))
	if h := huddleOf(t, w, "alice"); h != "" {
		t.Errorf("alice should be out of the dissolved solo huddle, still in %q", h)
	}
}

// The control: a NON-skip completion (the actor actually ran a turn) leaves the
// solo huddle intact — only a skip means "confirmed won't act", which is the
// signal the dissolve keys off.
func TestCompleteReactorTick_SuccessKeepsSoloHuddle(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	hud := soloHuddleForAlice(t, w, now)

	completeTickWithStatus(t, w, "alice", sim.TickStatusSuccess, now.Add(2*time.Second))
	if h := huddleOf(t, w, "alice"); h != hud {
		t.Errorf("a non-skip completion must not dissolve the huddle; alice now in %q, want %q", h, hud)
	}
}

// Guard: a skip while the huddle still has ANOTHER member dissolves nothing —
// the dissolve is scoped to a SOLE-member huddle so a co-member is never
// stranded (a multi-member skip is a separate drift desync, out of scope).
func TestCompleteReactorTick_SkippedKeepsMultiMemberHuddle(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	setActor(t, w, "alice", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful; a.State = sim.StateIdle })
	setActor(t, w, "bob", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful })
	sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now))
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	hud := huddleOf(t, w, "alice")

	completeTickWithStatus(t, w, "alice", sim.TickStatusSkipped, now.Add(time.Second))
	if h := huddleOf(t, w, "alice"); h != hud {
		t.Errorf("alice should remain — the huddle has another member; got %q", h)
	}
	if h := huddleOf(t, w, "bob"); h != hud {
		t.Errorf("bob must not be stranded by alice's skip; got %q", h)
	}
}

// Guard: a skip on an actor whose CurrentHuddleID is a STALE back-ref to a huddle
// it isn't actually a member of must not touch that huddle. len(Members)==1 alone
// would wrongly fire if the lone member were someone else; the membership re-check
// prevents stamping a spurious HuddlePeerLeft on / concluding a bystander's huddle
// (code_review).
func TestCompleteReactorTick_SkippedIgnoresStaleBackref(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	setActor(t, w, "alice", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful; a.State = sim.StateIdle })
	setActor(t, w, "bob", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful; a.State = sim.StateIdle })
	// bob is the sole member of his own huddle; alice is NOT in it.
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	hudBob := huddleOf(t, w, "bob")
	if hudBob == "" {
		t.Fatal("precondition: bob should be in a huddle")
	}
	// Point alice's back-ref at bob's huddle without joining her to its members.
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["alice"].CurrentHuddleID = hudBob
		return nil, nil
	}})

	completeTickWithStatus(t, w, "alice", sim.TickStatusSkipped, now.Add(time.Second))
	if h := huddleOf(t, w, "bob"); h != hudBob {
		t.Errorf("bob's huddle must be untouched by alice's stale-backref skip; bob now in %q, want %q", h, hudBob)
	}
}
