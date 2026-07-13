package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// huddle_skip_dissolve_test.go — ZBBS-HOME-413, widened. A completed reactor
// tick that leaves the actor as the SOLE member of its huddle dissolves that
// dead huddle. Originally the dissolve keyed off TickStatusSkipped only ("a
// skip means confirmed won't act"), but a lone member whose ticks are driven
// by a skip-bypassing warrant (WarrantKindRestock) never skips and stayed
// stranded in a zombie huddle through real done() turns (the live John-Ellis
// case). Now any ADDRESSING terminal completion (terminalStatusAddresses)
// dissolves; non-addressing statuses (failed-before-render, shutdown) do not —
// the actor never perceived that turn. Reuses buildHuddleTestWorld / setActor
// / huddleOf / sendT (sibling huddle tests).

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

// Every ADDRESSING terminal completion dissolves alice's dead solo huddle —
// skip (the original HOME-413 case) and the real-turn statuses a
// skip-bypassing warrant produces (the John-Ellis widening: done / success /
// budget-forced / failed-after-render).
func TestCompleteReactorTick_AddressingCompletionDissolvesSoloHuddle(t *testing.T) {
	statuses := []sim.TickTerminalStatus{
		sim.TickStatusSkipped,
		sim.TickStatusDone,
		sim.TickStatusSuccess,
		sim.TickStatusBudgetForced,
		sim.TickStatusFailedAfterRender,
	}
	for _, status := range statuses {
		w, cancel := buildHuddleTestWorld(t)
		now := time.Now().UTC()
		soloHuddleForAlice(t, w, now)

		completeTickWithStatus(t, w, "alice", status, now.Add(2*time.Second))
		if h := huddleOf(t, w, "alice"); h != "" {
			t.Errorf("status %v: alice should be out of the dissolved solo huddle, still in %q", status, h)
		}
		cancel()
	}
}

// The control: a NON-addressing completion (the actor never perceived the
// turn — LLM failure before render, or world shutdown) leaves the solo huddle
// intact. The dissolve keys off "the actor ran a turn against current state
// and is still alone", which these statuses don't establish.
func TestCompleteReactorTick_NonAddressingKeepsSoloHuddle(t *testing.T) {
	statuses := []sim.TickTerminalStatus{
		sim.TickStatusFailedBeforeRender,
		sim.TickStatusShutdown,
	}
	for _, status := range statuses {
		w, cancel := buildHuddleTestWorld(t)
		now := time.Now().UTC()
		hud := soloHuddleForAlice(t, w, now)

		completeTickWithStatus(t, w, "alice", status, now.Add(2*time.Second))
		if h := huddleOf(t, w, "alice"); h != hud {
			t.Errorf("status %v: a non-addressing completion must not dissolve the huddle; alice now in %q, want %q", status, h, hud)
		}
		cancel()
	}
}

// Guard: a completion while the huddle still has ANOTHER member dissolves
// nothing — the dissolve is scoped to a SOLE-member huddle so a co-member is
// never stranded. This is THE protection for normal conversations now that
// every addressing status dissolves: each ordinary speak turn completes as
// success, and only the sole-member check keeps it from tearing the huddle
// down mid-conversation.
func TestCompleteReactorTick_CompletionKeepsMultiMemberHuddle(t *testing.T) {
	for _, status := range []sim.TickTerminalStatus{sim.TickStatusSkipped, sim.TickStatusSuccess} {
		w, cancel := buildHuddleTestWorld(t)
		now := time.Now().UTC()
		setActor(t, w, "alice", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful; a.State = sim.StateIdle })
		setActor(t, w, "bob", func(a *sim.Actor) { a.Kind = sim.KindNPCStateful })
		sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now))
		sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
		hud := huddleOf(t, w, "alice")

		completeTickWithStatus(t, w, "alice", status, now.Add(time.Second))
		if h := huddleOf(t, w, "alice"); h != hud {
			t.Errorf("status %v: alice should remain — the huddle has another member; got %q", status, h)
		}
		if h := huddleOf(t, w, "bob"); h != hud {
			t.Errorf("status %v: bob must not be stranded by alice's completion; got %q", status, h)
		}
		cancel()
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
