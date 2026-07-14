package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// huddle_loop_lingering_test.go — LLM-397. The lingering arm: a conversation
// that has simply run long is steered toward a close, and concluded only if it
// won't take the hint.
//
// The transcript below is VERBATIM from the live 2026-07-14 inn conversation
// (hud-e26338a9…, Hannah Boggs + Lewis Walker + Silence Walker). It is the
// reason this arm exists and the reason it must be gentle: the eco arc that
// preceded it read this scene as a runaway loop and cut it every three minutes,
// ten times in a hundred minutes. It is not a loop. Nobody repeats themselves,
// a bowl of porridge is bought and paid for and eaten, and the innkeeper tells a
// story about her dead husband. The engine's job here is to let it END, not to
// kill it.

// innSceneLines are the porridge-and-memories beats: varied, warm, and — after
// the sale — carrying no commerce at all. The lexical arm sees no repetition
// here and the endurance arm's counter is reset by the sale, so ONLY the
// lingering clock can see that this has been going on for an hour.
var innSceneLines = []string{
	"The new batch is done — hot and ready. I've got bowls of fresh porridge if either of you have a mind for more, or know someone who might. A coin a bowl, same as always.",
	"Good day, Hannah. A bowl of your fresh porridge sounds wonderful — I'll take one, if you please. Your cooking always warms the spirit as much as the belly.",
	"That does sound welcoming, Hannah. A bowl of your fresh porridge would be just the thing for this midday chill. Here's a coin for it.",
	"Here's your bowl, Lewis — fresh from the pot and still steaming. Eat it while it's hot, that's when it's best.",
	"This porridge is just what I needed — warms a body right through. You've outdone yourself today, Hannah.",
	"It's been a quiet sort of day, Lewis — the kind that lets you catch your breath and take stock. I helped Patience with the baking this morning, which was a comfort.",
	"His name was Thomas. Thomas Boggs. A blacksmith by trade, though he could turn his hand to anything — carpentry, mending a wheel, even stitching a wound if need pressed.",
	"Seven winters now since he passed, and I still find myself reaching for that memory on the cold mornings when the Inn feels too quiet.",
}

// lingeringSettings enables the sweep with the live production shape plus a
// wind-down window: 180s persistence gate, 60% repeat threshold, a turn budget
// high enough that the ENDURANCE arm cannot fire (so a test that concludes here
// proves the lingering arm did it), and the given wind-down.
func lingeringSettings(t *testing.T, w *sim.World, windDown time.Duration) {
	t.Helper()
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.HuddleLoopTimeout = 3 * time.Minute
		world.Settings.HuddleLoopRepeatPercent = 60
		world.Settings.HuddleLoopMaxTurns = 10000
		world.Settings.HuddleConversationWindDown = windDown
		return nil, nil
	}})
}

// huddleConversationSince reads a huddle's ConversationSince off the world
// goroutine — the clock that must survive re-formation.
func huddleConversationSince(t *testing.T, w *sim.World, id sim.HuddleID) time.Time {
	t.Helper()
	v := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		h, ok := world.Huddles[id]
		if !ok || h == nil {
			return time.Time{}, nil
		}
		return h.ConversationSince, nil
	}})
	ts, _ := v.(time.Time)
	return ts
}

// setConversationSince ages a conversation on the world goroutine — the test
// stand-in for a huddle that has been talking for a while.
func setConversationSince(t *testing.T, w *sim.World, id sim.HuddleID, at time.Time) {
	t.Helper()
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		if h := world.Huddles[id]; h != nil {
			h.ConversationSince = at
		}
		return nil, nil
	}})
}

// huddleLoopingReason reads the latched onset cause off the world goroutine.
func huddleLoopingReason(t *testing.T, w *sim.World, id sim.HuddleID) string {
	t.Helper()
	v := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		h, ok := world.Huddles[id]
		if !ok || h == nil {
			return "", nil
		}
		return h.LoopingReason, nil
	}})
	s, _ := v.(string)
	return s
}

// lingeringSteer reports whether the published snapshot is currently asking this
// actor to wind the conversation down.
func lingeringSteer(t *testing.T, w *sim.World, id sim.ActorID) bool {
	t.Helper()
	sa := w.Published().Actors[id]
	if sa == nil {
		return false
	}
	return sa.ConversationLingering
}

// TestLingeringArm_StillTalkingAfterWindDown_IsConcluded is the backstop half of
// the done-means: a conversation past the wind-down window that keeps talking
// through the whole persistence gate is silently concluded, tagged
// conversation_lingering — NOT as any of the three pathology reasons, because
// nothing about it is pathological.
func TestLingeringArm_StillTalkingAfterWindDown_IsConcluded(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	sink := wireLoopTelemetry(t, w)
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))
	lingeringSettings(t, w, 12*time.Minute)

	// The conversation is 13 minutes old and still going — past the window.
	feedTranscript(t, w, h, "alice", "bob", innSceneLines, t0, time.Minute)
	setConversationSince(t, w, h, t0.Add(-13*time.Minute))

	// Scan 1 arms the spell; the gate has not elapsed, so nothing is concluded —
	// this is the grace period in which the members are being steered to close.
	sendT(t, w, sim.EvaluateHuddleLoopSweep(t0))
	if huddleLoopingSince(t, w, h) == nil {
		t.Fatal("a conversation past the wind-down window should stamp LoopingSince (lingering onset)")
	}
	if huddleConcludedAt(t, w, h) != nil {
		t.Fatal("lingering must not conclude before the persistence gate elapses — the members get the whole gate to end it themselves")
	}

	// They keep talking right through the gate. Now the backstop fires.
	t1 := t0.Add(3 * time.Minute)
	appendUtterance(t, w, h, "alice", "And another thing about the weather this week…", t1.Add(-10*time.Second))
	sendT(t, w, sim.EvaluateHuddleLoopSweep(t1))
	if huddleConcludedAt(t, w, h) == nil {
		t.Fatal("a conversation that talks through the entire wind-down gate should be concluded")
	}

	var reasons []string
	for _, rec := range sink.snapshot() {
		if rec.Kind == "stuck" {
			reasons = append(reasons, rec.Detail["reason"])
		}
	}
	if len(reasons) == 0 {
		t.Fatal("conclusion should emit stuck telemetry per member")
	}
	for _, r := range reasons {
		if r != "conversation_lingering" {
			t.Errorf("telemetry reason = %q, want conversation_lingering — the other reasons all assert a pathology this scene does not have", r)
		}
	}
}

// TestLingeringArm_ProductiveConversationSurvivesTheWindow is the OTHER half of
// the done-means, and the one that matters more. The exact live scene — a sale,
// varied warm talk, no repetition — must be left completely alone while it is
// inside its window: no spell, no conclusion. This is the test that fails if
// anyone reintroduces a short guillotine.
func TestLingeringArm_ProductiveConversationSurvivesTheWindow(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))
	lingeringSettings(t, w, 12*time.Minute)

	// Eight minutes of the real thing: inside the window, actively spoken.
	feedTranscript(t, w, h, "alice", "bob", innSceneLines, t0, time.Minute)
	setConversationSince(t, w, h, t0.Add(-8*time.Minute))

	sendT(t, w, sim.EvaluateHuddleLoopSweep(t0))
	if since := huddleLoopingSince(t, w, h); since != nil {
		t.Fatalf("a healthy conversation inside its wind-down window must not be flagged at all (LoopingSince = %v)", since)
	}
	if huddleConcludedAt(t, w, h) != nil {
		t.Fatal("a healthy conversation inside its wind-down window must never be concluded — this is the scene the old eco arc cut ten times in a hundred minutes")
	}

	// And it is not steered either: the perception flag stays clear.
	if lingeringSteer(t, w, "alice") {
		t.Error("no wind-down steer should arm inside the window")
	}
}

// TestLingeringArm_LiveDealIsNeverCut: an over-long conversation carrying a
// pending pay-ledger entry is left alone entirely. Cutting here would strand the
// deal and tell a buyer with coin on the table to say farewell.
func TestLingeringArm_LiveDealIsNeverCut(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))
	lingeringSettings(t, w, 12*time.Minute)
	feedTranscript(t, w, h, "alice", "bob", innSceneLines, t0, time.Minute)
	setConversationSince(t, w, h, t0.Add(-30*time.Minute))

	// A pending offer stamped with this huddle — bob is waiting on alice's answer.
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.PayLedger[1] = &sim.PayLedgerEntry{
			ID:       1,
			BuyerID:  "bob",
			SellerID: "alice",
			ItemKind: "porridge",
			HuddleID: h,
			State:    sim.PayLedgerStatePending,
		}
		return nil, nil
	}})

	// Two full gates' worth of scans: a commerce-carrying huddle never arms.
	sendT(t, w, sim.EvaluateHuddleLoopSweep(t0))
	if huddleLoopingSince(t, w, h) != nil {
		t.Fatal("a huddle carrying a live deal must not arm the lingering spell")
	}
	t1 := t0.Add(6 * time.Minute)
	appendUtterance(t, w, h, "alice", "Let me fetch that for you.", t1.Add(-5*time.Second))
	sendT(t, w, sim.EvaluateHuddleLoopSweep(t1))
	if huddleConcludedAt(t, w, h) != nil {
		t.Fatal("a huddle carrying a live deal must never be concluded by the lingering arm — the counterparty is blocked on an answer")
	}
	if lingeringSteer(t, w, "alice") {
		t.Error("no wind-down steer while a deal is live — it would tell a seller to walk away mid-sale")
	}
}

// TestLingeringArm_ClockSurvivesHuddleChurn pins the defect that made the live
// conversation invisible: a clique that concludes and re-forms at the same
// structure carries its conversation clock. Ten huddle ids in a hundred minutes,
// none of them more than a few minutes old — measured on StartedAt, that
// conversation was forever young, and no age-based rule could ever see it.
func TestLingeringArm_ClockSurvivesHuddleChurn(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	t0 := time.Now().UTC()

	h1 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))
	lingeringSettings(t, w, 12*time.Minute)
	feedTranscript(t, w, h1, "alice", "bob", innSceneLines, t0, time.Minute)

	// This conversation began 20 minutes ago.
	began := t0.Add(-20 * time.Minute)
	setConversationSince(t, w, h1, began)

	// The clique disperses and reconvenes — a fresh huddle id, moments later.
	churn := t0.Add(30 * time.Second)
	sendT(t, w, sim.ConcludeHuddle(h1, churn))
	h2 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", churn)).(sim.JoinHuddleResult).HuddleID
	if h2 == h1 {
		t.Fatal("re-formation should mint a new huddle id")
	}
	if got := huddleConversationSince(t, w, h2); !got.Equal(began) {
		t.Fatalf("re-formed huddle ConversationSince = %v, want the carried %v — a churned clique must not reset its conversation clock", got, began)
	}
}

// TestLingeringArm_ConclusionEndsTheScene: after a lingering conclude the
// structure's carry-over is DROPPED, so the next huddle there is a genuinely new
// conversation with a fresh clock. Without this the clique re-forms, inherits an
// already-elapsed clock, and gets cut again one gate later — which is precisely
// the sawtooth the old eco arc produced.
func TestLingeringArm_ConclusionEndsTheScene(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	t0 := time.Now().UTC()

	h1 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))
	lingeringSettings(t, w, 12*time.Minute)
	feedTranscript(t, w, h1, "alice", "bob", innSceneLines, t0, time.Minute)
	setConversationSince(t, w, h1, t0.Add(-13*time.Minute))

	sendT(t, w, sim.EvaluateHuddleLoopSweep(t0))
	t1 := t0.Add(3 * time.Minute)
	appendUtterance(t, w, h1, "alice", "Anyway, as I was saying about the rain…", t1.Add(-10*time.Second))
	sendT(t, w, sim.EvaluateHuddleLoopSweep(t1))
	if huddleConcludedAt(t, w, h1) == nil {
		t.Fatal("precondition: the lingering conversation should be concluded")
	}

	// They come back a minute later. This is a NEW conversation.
	reform := t1.Add(time.Minute)
	h2 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", reform)).(sim.JoinHuddleResult).HuddleID
	if got := huddleConversationSince(t, w, h2); !got.Equal(reform) {
		t.Errorf("ConversationSince after a lingering conclude = %v, want a fresh clock at %v — the scene ended, so the next one starts from zero", got, reform)
	}
	if ring := huddleRing(t, w, h2); len(ring) != 0 {
		t.Errorf("carried ring after a lingering conclude = %d utterances, want 0 — the conversation is over, not paused", len(ring))
	}
	// And crucially: it is not instantly re-concluded on the next scan.
	appendUtterance(t, w, h2, "alice", "Good day to you again, Hannah.", reform)
	sendT(t, w, sim.EvaluateHuddleLoopSweep(reform.Add(time.Second)))
	if huddleConcludedAt(t, w, h2) != nil {
		t.Error("the fresh conversation must not be concluded immediately — that sawtooth is the bug this ticket exists to remove")
	}
}

// TestLingeringArm_SteerArmsBeforeTheConclude: the point of the arm is the
// STEER. Past the window, an armed conversation publishes ConversationLingering
// so perception can ask the members to close the scene themselves — a full
// persistence gate before the engine would do it for them.
func TestLingeringArm_SteerArmsBeforeTheConclude(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))
	lingeringSettings(t, w, 12*time.Minute)
	feedTranscript(t, w, h, "alice", "bob", innSceneLines, t0, time.Minute)
	setConversationSince(t, w, h, t0.Add(-13*time.Minute))

	if !lingeringSteer(t, w, "alice") {
		t.Fatal("past the wind-down window the steer should arm, so the members can close the scene in the fiction")
	}
	if huddleConcludedAt(t, w, h) != nil {
		t.Fatal("the steer must arm WITHOUT anything being concluded — a graceful in-world farewell is the whole point")
	}
}

// TestLingeringArm_EnduranceLatchedButLingeringConcluded is the code_review
// regression: the arms drift over a spell, and the LATCHED reason must not be
// what decides the carry-over.
//
// Sequence: a long conversation arms as endurance (turn budget exhausted, no
// progress). Then a sale completes — which stamps LastProgressAt and resets the
// endurance counter, exactly as designed — so the endurance arm goes quiet. But
// the conversation is still old, still talking, and now only the lingering clock
// holds it. It concludes under the lingering arm.
//
// Under the latched reason ("huddle_loop_endurance") the conclusion would have
// PRESERVED the carry-over: the clique re-forms seconds later, inherits an
// already-elapsed conversation clock, and gets cut again one gate later. That
// sawtooth is the entire defect this ticket removes, and it would have crept
// straight back in through the latch.
func TestLingeringArm_EnduranceLatchedButLingeringConcluded(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	sink := wireLoopTelemetry(t, w)
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))
	// Endurance budget low enough to arm on the transcript below; wind-down long
	// past, so both arms are live at onset.
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.HuddleLoopTimeout = 3 * time.Minute
		world.Settings.HuddleLoopRepeatPercent = 60
		world.Settings.HuddleLoopMaxTurns = 4
		world.Settings.HuddleConversationWindDown = 12 * time.Minute
		return nil, nil
	}})
	feedTranscript(t, w, h, "alice", "bob", innSceneLines, t0, time.Minute)
	setConversationSince(t, w, h, t0.Add(-20*time.Minute))

	// Onset: endurance latches first (precedence puts it above lingering).
	sendT(t, w, sim.EvaluateHuddleLoopSweep(t0))
	if got := huddleLoopingReason(t, w, h); got != "huddle_loop_endurance" {
		t.Fatalf("precondition: onset reason = %q, want huddle_loop_endurance", got)
	}

	// A sale lands mid-conversation: progress stamped, endurance counter reset.
	// The conversation keeps going, and is now held ONLY by the lingering clock.
	t1 := t0.Add(3 * time.Minute)
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		hh := world.Huddles[h]
		hh.LastProgressAt = t1.Add(-time.Minute)
		hh.TurnsSinceProgress = 0
		return nil, nil
	}})
	appendUtterance(t, w, h, "alice", "That's a fine bowl of porridge, Hannah — my thanks.", t1.Add(-10*time.Second))

	sendT(t, w, sim.EvaluateHuddleLoopSweep(t1))
	if huddleConcludedAt(t, w, h) == nil {
		t.Fatal("the conversation should still conclude — the lingering clock is armed even though endurance went quiet")
	}

	// Filed under the arm that ACTUALLY fired, not the stale latch.
	for _, rec := range sink.snapshot() {
		if rec.Kind != "stuck" {
			continue
		}
		if got := rec.Detail["reason"]; got != "conversation_lingering" {
			t.Errorf("telemetry reason = %q, want conversation_lingering — endurance stopped holding this conversation before it was concluded", got)
		}
	}

	// And the carry-over is DROPPED, so the clique doesn't resume into a sawtooth.
	reform := t1.Add(30 * time.Second)
	h2 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", reform)).(sim.JoinHuddleResult).HuddleID
	if got := huddleConversationSince(t, w, h2); !got.Equal(reform) {
		t.Errorf("ConversationSince after the conclude = %v, want a fresh clock at %v — a stale latch must not preserve a carry-over the lingering verdict drops", got, reform)
	}
	if ring := huddleRing(t, w, h2); len(ring) != 0 {
		t.Errorf("carried ring after the conclude = %d utterances, want 0", len(ring))
	}
}
