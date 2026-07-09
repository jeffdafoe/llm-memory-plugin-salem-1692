package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// huddle_loop_endurance_test.go — LLM-333. The endurance arm: a huddle that
// accumulates HuddleLoopMaxTurns spoken lines with no progress event is stuck
// regardless of wording. The transcripts here are VERBATIM from the two live
// 2026-07-08 loops the lexical arm measurably could not see.

// farewellLoopLines is the John+Elizabeth farewell loop
// (hud-b070ba372987c29328552d9524a9cbea, tavern, 20:53-21:04 UTC): ~12
// consecutive paraphrased farewell rounds — Elizabeth announcing departure,
// John farewelling, neither leaving. Every line is worded differently; the
// lexical arm measured 0.00 repetition in every possible 8-line ring window
// against the 0.60 threshold.
var farewellLoopLines = []string{
	"Thank you for the porridge, John. I'd best be off to the farm now — the animals will be waiting. Good day to you.",
	"Take care on the road back, love. I'll keep the fire warm for when you're ready to return. Give my greetings to the animals, eh?",
	"I will, John. Keep the hearth warm — I'll be back before you know it. Take care now.",
	"Safe home, Elizabeth. I'll be here when you get back. Give the cows a scratch behind the ears for me.",
	"I'll be off to the farm just as soon as I finish this, John. A warm meal sets a body right for the afternoon's work.",
	"Take your time finishing, love. No rush — I'll be here wiping down the pewter and keeping an eye on the fire. The evening crowd won't be in for a bit yet.",
	"Aye, John. I'll be on my way soon as this bowl's empty. The animals won't feed themselves, and there's mending that needs doing at the farm.",
	"Well, finish up whenever you're ready, love. I'll be here — got pewter to polish and the fire to tend before the evening trade starts up. Safe travels to the farm.",
	"I'll give 'em a scratch for you, John. A good bowl of porridge was just what I needed — thank you for the warm meal. I'll be heading to the farm directly to see to the animals and finish the mending.",
	"You go on now, love — I'll see you when the chores are done. I'll have a bit of supper waiting if the evening trade isn't too heavy. Give the farm my best.",
	"I'd best get to the farm now, John — the animals won't feed themselves. Thank you kindly for the porridge and the company. I'll see you this evening if the fates allow.",
	"Go on then, love — the farm won't tend itself, and I know you've a care for those animals. I'll be here stoking the fire and keeping the tavern ready. Safe now, and I'll see you back here come evening.",
	"Aye, I'll be off to the farm now, John. The porridge was just what I needed. I'll tend the animals and see you come evening.",
	"Right you are, love. Off with you then — the farm's waiting. I'll have the fire bright when you're back this evening. Safe travels.",
	"Aye, I'll just finish my porridge and then I'm off to the farm. The animals will be wanting their feed soon enough.",
	"Finish up at your leisure, love. I'll be about the tavern — the afternoon won't tend itself, and there's a stack of pewter that needs wiping before the evening trade drifts in.",
	"Right then, I'm off to the farm now. Thanks for the porridge, John — just what I needed to get me through the afternoon. I'll see you later this evening.",
	"Godspeed, love. Mind the muddy spots on the road, and don't let the chickens talk you into staying out past dark. I'll be right here when you're through.",
}

// enduranceSettings enables the sweep with the live production shape: 180s
// timeout, 60% repeat threshold, and the given turn budget.
func enduranceSettings(t *testing.T, w *sim.World, maxTurns int) {
	t.Helper()
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.HuddleLoopTimeout = 3 * time.Minute
		world.Settings.HuddleLoopRepeatPercent = 60
		world.Settings.HuddleLoopMaxTurns = maxTurns
		return nil, nil
	}})
}

// feedTranscript appends lines alternating between the two speakers, one per
// interval, ending at end — through AppendUtterance so TurnsSinceProgress
// advances exactly as live speech does.
func feedTranscript(t *testing.T, w *sim.World, id sim.HuddleID, a, b sim.ActorID, lines []string, end time.Time, interval time.Duration) {
	t.Helper()
	for i, line := range lines {
		speaker := a
		if i%2 == 1 {
			speaker = b
		}
		at := end.Add(-time.Duration(len(lines)-1-i) * interval)
		appendUtterance(t, w, id, speaker, line, at)
	}
}

// TestFarewellLoop_InvisibleToLexicalArm pins the gap the endurance arm exists
// to cover: the live farewell transcript never reads as lexically repetitive —
// in ANY ring window — under the production 60% threshold. If a similarity
// upgrade ever makes this fail, the endurance arm has become redundant for this
// shape; revisit rather than delete blindly.
func TestFarewellLoop_InvisibleToLexicalArm(t *testing.T) {
	now := time.Now().UTC()
	s := sim.WorldSettings{HuddleLoopTimeout: 3 * time.Minute, HuddleLoopRepeatPercent: 60}
	for i := 0; i+sim.MaxRecentUtterancesPerHuddle <= len(farewellLoopLines); i++ {
		ring := make([]sim.Utterance, 0, sim.MaxRecentUtterancesPerHuddle)
		for j, txt := range farewellLoopLines[i : i+sim.MaxRecentUtterancesPerHuddle] {
			ring = append(ring, sim.Utterance{
				SpeakerID: sim.ActorID([]string{"john", "elizabeth"}[j%2]),
				Text:      txt,
				At:        now.Add(time.Duration(j-sim.MaxRecentUtterancesPerHuddle) * time.Minute),
			})
		}
		h := &sim.Huddle{RecentUtterances: ring}
		if sim.HuddleLoopRepetitive(s, h) {
			t.Fatalf("window %d: live paraphrase loop unexpectedly trips the lexical arm — endurance arm may be redundant for this shape", i)
		}
	}
}

// TestHuddleLoopSweep_EnduranceConcludesFarewellLoop is the LLM-333 done-means
// regression: the live farewell shape — paraphrased lines, no progress — arms
// via the endurance turn budget and is concluded once the spell persists the
// timeout, with the telemetry record tagged huddle_loop_endurance and the
// members' social-only warrant cycles cleared so the conclusion sticks.
func TestHuddleLoopSweep_EnduranceConcludesFarewellLoop(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	sink := wireLoopTelemetry(t, w)
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))
	enduranceSettings(t, w, 16)

	// 18 paraphrased lines, one per minute, newest at t0 — the eco-paced live
	// cadence. Ring holds only the last 8; the counter holds all 18.
	feedTranscript(t, w, h, "alice", "bob", farewellLoopLines, t0, time.Minute)

	// Pending social-only cycles on both members (the last speech warrants), plus
	// a mixed cycle on a third actor to prove the clear is scoped to members.
	sendT(t, w, sim.StampWarrant("alice", sim.WarrantMeta{
		TriggerActorID: "bob",
		Reason:         sim.NPCSpeechWarrantReason{SpeechID: 1, Speaker: "bob", Excerpt: "…"},
		SourceEventID:  1,
		OccurredAt:     t0,
	}, t0))
	sendT(t, w, sim.StampWarrant("bob", sim.WarrantMeta{
		TriggerActorID: "alice",
		Reason:         sim.BasicWarrantReason{K: sim.WarrantKindNPCSpoke},
		SourceEventID:  2,
		OccurredAt:     t0,
	}, t0))
	sendT(t, w, sim.StampWarrant("bob", sim.WarrantMeta{
		TriggerActorID: "alice",
		Reason:         sim.BasicWarrantReason{K: sim.WarrantKindNeedThreshold},
		SourceEventID:  3,
		OccurredAt:     t0,
	}, t0))

	// Scan 1: endurance arms the spell (turns >= budget), persistence gate holds.
	sendT(t, w, sim.EvaluateHuddleLoopSweep(t0))
	if huddleConcludedAt(t, w, h) != nil {
		t.Fatal("endurance-armed loop must not conclude before the persistence gate elapses")
	}
	if huddleLoopingSince(t, w, h) == nil {
		t.Fatal("an exhausted turn budget should stamp LoopingSince (endurance onset)")
	}

	// One more paced beat past the timeout keeps the loop LIVE, then scan 2
	// concludes it.
	t1 := t0.Add(3 * time.Minute)
	appendUtterance(t, w, h, "alice", farewellLoopLines[0], t1.Add(-10*time.Second))
	sendT(t, w, sim.EvaluateHuddleLoopSweep(t1))
	if huddleConcludedAt(t, w, h) == nil {
		t.Fatal("the farewell loop should be concluded once the endurance spell persists the timeout")
	}

	// Telemetry tagged as an endurance kill (the lexical arm never armed).
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
		if r != "huddle_loop_endurance" {
			t.Errorf("telemetry reason = %q, want huddle_loop_endurance", r)
		}
	}

	// The conclusion sticks: alice's cycle (npc_spoke + the huddle beats the
	// join flow stamped — all social-cadence kinds) is cleared; bob's mixed
	// cycle survives whole, need_threshold included.
	pendingKinds := func(id sim.ActorID) []sim.WarrantKind {
		v := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
			var kinds []sim.WarrantKind
			for _, m := range world.Actors[id].Warrants {
				kinds = append(kinds, m.Kind())
			}
			return kinds, nil
		}})
		kinds, _ := v.([]sim.WarrantKind)
		return kinds
	}
	if got := pendingKinds("alice"); len(got) != 0 {
		t.Errorf("alice pending warrants = %v, want none (social-only cycle cleared at conclude)", got)
	}
	bobKinds := pendingKinds("bob")
	if len(bobKinds) == 0 {
		t.Error("bob's mixed cycle must survive the clear whole")
	}
	foundNeed := false
	for _, k := range bobKinds {
		if k == sim.WarrantKindNeedThreshold {
			foundNeed = true
		}
	}
	if !foundNeed {
		t.Errorf("bob pending warrants = %v, want need_threshold retained", bobKinds)
	}
}

// TestHuddleLoopSweep_LatchedReasonSurvivesArmDrift (code_review R1): a spell
// stamped by the LEXICAL arm whose ring then churns into varied-but-over-budget
// lines must still conclude tagged "huddle_loop" — the latched onset cause —
// not be re-diagnosed as an endurance kill at conclude time.
func TestHuddleLoopSweep_LatchedReasonSurvivesArmDrift(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	sink := wireLoopTelemetry(t, w)
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))
	enduranceSettings(t, w, 16)

	// Onset: a lexically repetitive ring, budget NOT yet exhausted.
	setHuddleLoopState(t, w, h, loopingLines(t0), nil, time.Time{})
	sendT(t, w, sim.EvaluateHuddleLoopSweep(t0))
	if huddleLoopingSince(t, w, h) == nil {
		t.Fatal("lexical ring should stamp the spell")
	}

	// Drift: the ring churns varied while the turn budget exhausts (18 paraphrase
	// lines through AppendUtterance — the ring keeps only the last 8, which are
	// varied, so the lexical arm is no longer armed at conclude time).
	t1 := t0.Add(3 * time.Minute)
	feedTranscript(t, w, h, "alice", "bob", farewellLoopLines, t1.Add(-10*time.Second), time.Second)
	sendT(t, w, sim.EvaluateHuddleLoopSweep(t1))
	if huddleConcludedAt(t, w, h) == nil {
		t.Fatal("the drifted spell should still conclude (endurance keeps the durable condition true)")
	}
	for _, rec := range sink.snapshot() {
		if rec.Kind == "stuck" && rec.Detail["reason"] != "huddle_loop" {
			t.Errorf("telemetry reason = %q, want huddle_loop (latched onset cause)", rec.Detail["reason"])
		}
	}
}

// TestHuddleEndurancePresent_ScalesWithMembers (code_review R2): the effective
// budget is max(configured, members × 4), so a crowded scene gets a per-actor
// allowance instead of the 2-actor default total.
func TestHuddleEndurancePresent_ScalesWithMembers(t *testing.T) {
	s := sim.WorldSettings{HuddleLoopTimeout: 3 * time.Minute, HuddleLoopMaxTurns: 16}
	members := func(n int) map[sim.ActorID]struct{} {
		m := make(map[sim.ActorID]struct{}, n)
		for i := 0; i < n; i++ {
			m[sim.ActorID(rune('a'+i))] = struct{}{}
		}
		return m
	}

	// 2 members: floor holds — 16 turns arms.
	h := &sim.Huddle{Members: members(2), TurnsSinceProgress: 16}
	if !sim.HuddleEndurancePresent(s, h) {
		t.Error("2-member huddle at the configured budget should arm")
	}
	// 6 members: scaled budget 24 — 16 turns must NOT arm, 24 does.
	h = &sim.Huddle{Members: members(6), TurnsSinceProgress: 16}
	if sim.HuddleEndurancePresent(s, h) {
		t.Error("6-member huddle at 16 turns must not arm (scaled budget 24)")
	}
	h.TurnsSinceProgress = 24
	if !sim.HuddleEndurancePresent(s, h) {
		t.Error("6-member huddle at 24 turns should arm")
	}
}

// TestHuddleLoopSweep_EnduranceSparesProgressingHuddle: the same volume of talk
// WITH a progress event resetting the counter mid-way never arms — a busy
// vendor scene outlives any budget as long as deals keep landing.
func TestHuddleLoopSweep_EnduranceSparesProgressingHuddle(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))
	enduranceSettings(t, w, 16)

	feedTranscript(t, w, h, "alice", "bob", farewellLoopLines[:10], t0.Add(-10*time.Minute), time.Minute)
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.TouchHuddleProgress(world, h, t0.Add(-9*time.Minute))
		return nil, nil
	}})
	feedTranscript(t, w, h, "alice", "bob", farewellLoopLines[10:], t0, time.Minute)

	sendT(t, w, sim.EvaluateHuddleLoopSweep(t0))
	if huddleLoopingSince(t, w, h) != nil {
		t.Error("a huddle whose progress reset kept turns under budget must not arm")
	}
	if huddleConcludedAt(t, w, h) != nil {
		t.Error("a progressing huddle must not be concluded")
	}
}

// TestTurnsSinceProgress_ResetSites pins every reset: transaction progress,
// a genuinely-new member joining, and a player line (via the speak path).
func TestTurnsSinceProgress_ResetSites(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))

	turns := func() int {
		v := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
			return world.Huddles[h].TurnsSinceProgress, nil
		}})
		return v.(int)
	}

	appendUtterance(t, w, h, "alice", "line one", t0)
	appendUtterance(t, w, h, "bob", "line two", t0.Add(time.Second))
	if got := turns(); got != 2 {
		t.Fatalf("turns after two lines = %d, want 2", got)
	}

	// Transaction progress resets.
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.TouchHuddleProgress(world, h, t0.Add(2*time.Second))
		return nil, nil
	}})
	if got := turns(); got != 0 {
		t.Fatalf("turns after transaction progress = %d, want 0", got)
	}

	// A genuinely-new member joining resets.
	appendUtterance(t, w, h, "alice", "line three", t0.Add(3*time.Second))
	sendT(t, w, sim.JoinHuddle("charlie", "tavern", "", t0.Add(4*time.Second)))
	if got := turns(); got != 0 {
		t.Fatalf("turns after new-member join = %d, want 0", got)
	}
}

// TestTurnsSinceProgress_CarriedAcrossChurn: the counter survives the
// conclude+re-form churn via the LLM-170 carry-over, so a churning clique
// cannot evade the endurance budget by re-forming.
func TestTurnsSinceProgress_CarriedAcrossChurn(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))
	for i := 0; i < 6; i++ {
		speaker := sim.ActorID("alice")
		if i%2 == 1 {
			speaker = "bob"
		}
		appendUtterance(t, w, h, speaker, farewellLoopLines[i], t0.Add(time.Duration(i)*time.Second))
	}

	// Churn: everyone leaves (last leave concludes + writes the carry-over),
	// then the same clique re-forms within the continuity window.
	drainHuddle(t, w, []sim.ActorID{"alice", "bob"}, t0.Add(10*time.Second))
	if got, ok := carryoverTurns(t, w, "tavern"); !ok || got != 6 {
		t.Fatalf("carry-over turns = %d (present=%v), want 6", got, ok)
	}

	t1 := t0.Add(30 * time.Second)
	h2 := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t1)).(sim.JoinHuddleResult).HuddleID
	v := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Huddles[h2].TurnsSinceProgress, nil
	}})
	if got := v.(int); got != 6 {
		t.Errorf("re-formed huddle turns = %d, want 6 (carried across churn)", got)
	}
}

func carryoverTurns(t *testing.T, w *sim.World, structureID sim.StructureID) (int, bool) {
	t.Helper()
	type out struct {
		n  int
		ok bool
	}
	v := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		n, ok := sim.CarryoverTurnsSinceProgress(world, structureID)
		return out{n, ok}, nil
	}})
	o := v.(out)
	return o.n, o.ok
}

// TestRepublish_ConversationRunLongFlag: an endurance-armed (but not lexically
// looping) huddle flags members ConversationRunLong — and NOT
// ConversationLooping — so perception renders the wind-down steer, not the
// false "you keep saying the same thing" line. A lexically-armed loop keeps
// the specific diagnosis.
func TestRepublish_ConversationRunLongFlag(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))
	enduranceSettings(t, w, 16)

	// Varied, fresh ring + exhausted budget → run-long, not looping.
	feedTranscript(t, w, h, "alice", "bob", farewellLoopLines, t0, time.Second)
	snap := w.Published()
	if !snap.Actors["alice"].ConversationRunLong || !snap.Actors["bob"].ConversationRunLong {
		t.Errorf("endurance-armed huddle should flag members ConversationRunLong: alice=%v bob=%v",
			snap.Actors["alice"].ConversationRunLong, snap.Actors["bob"].ConversationRunLong)
	}
	if snap.Actors["alice"].ConversationLooping {
		t.Error("a varied over-long conversation must NOT flag ConversationLooping")
	}

	// A lexically-armed loop takes the specific diagnosis instead.
	setHuddleLoopState(t, w, h, loopingLines(t0), nil, time.Time{})
	snap = w.Published()
	if !snap.Actors["alice"].ConversationLooping {
		t.Error("a lexically-armed loop should flag ConversationLooping")
	}
	if snap.Actors["alice"].ConversationRunLong {
		t.Error("ConversationRunLong must yield to the more specific ConversationLooping")
	}

	// Master enable off → neither flag.
	enableLoopSweep(t, w, 0, 60)
	snap = w.Published()
	if snap.Actors["alice"].ConversationLooping || snap.Actors["alice"].ConversationRunLong {
		t.Error("a disabled sweep must leave both steer flags false")
	}
}