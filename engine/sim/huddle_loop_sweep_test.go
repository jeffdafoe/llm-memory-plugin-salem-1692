package sim_test

import (
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// loopRecordingTelemetry captures the `stuck` records the loop sweep writes so
// tests can assert the loop surfaced to the operator.
type loopRecordingTelemetry struct {
	mu      sync.Mutex
	records []sim.TickTelemetryRecord
}

func (r *loopRecordingTelemetry) WriteTickTelemetry(rec sim.TickTelemetryRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec)
}

func (r *loopRecordingTelemetry) snapshot() []sim.TickTelemetryRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sim.TickTelemetryRecord(nil), r.records...)
}

// loopingLines is the actual Walker livelock (hud-9fb3…): the same intent
// restated with light variation, never converting to an action.
func loopingLines(now time.Time) []sim.Utterance {
	texts := []string{
		"Let's go to the market",
		"I'm ready to go to the market",
		"Shall we go to the market now?",
		"Let's go to the market",
		"I'm ready to go to the market, let's head out",
		"Let's go to the market",
		"I'm ready to go",
		"Let's go to the market together",
	}
	return stampUtterances(texts, now)
}

// variedLines is a healthy, advancing conversation — each turn introduces new
// content.
func variedLines(now time.Time) []sim.Utterance {
	texts := []string{
		"Good morning, how is your family faring this week?",
		"The harvest was poor, I worry about the winter stores",
		"Perhaps the miller would trade flour for your wool",
		"That is a kind thought, I shall ask him tomorrow",
		"Did you hear the constable arrested a stranger?",
		"Strange times indeed, best keep the children close",
		"I must tend the goats before the rain comes",
		"Safe travels, and give my regards to your husband",
	}
	return stampUtterances(texts, now)
}

// stampUtterances builds a ring with each line a few seconds apart ending at now,
// so the newest is fresh against the live-window guard.
func stampUtterances(texts []string, now time.Time) []sim.Utterance {
	out := make([]sim.Utterance, len(texts))
	for i, txt := range texts {
		out[i] = sim.Utterance{
			SpeakerName: "speaker",
			Text:        txt,
			At:          now.Add(time.Duration(i-len(texts)) * 4 * time.Second),
		}
	}
	return out
}

// enableLoopSweep sets the loop-sweep settings on the world goroutine.
func enableLoopSweep(t *testing.T, w *sim.World, timeout time.Duration, repeatPct int) {
	t.Helper()
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.HuddleLoopTimeout = timeout
		world.Settings.HuddleLoopRepeatPercent = repeatPct
		return nil, nil
	}})
}

// setHuddleLoopState overwrites a huddle's loop-relevant fields directly on the
// world goroutine, so a test can stage a loop without replaying dozens of speaks.
func setHuddleLoopState(t *testing.T, w *sim.World, id sim.HuddleID, ring []sim.Utterance, loopingSince *time.Time, lastProgress time.Time) {
	t.Helper()
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		h, ok := world.Huddles[id]
		if !ok {
			t.Fatalf("huddle %q not found when staging loop state", id)
		}
		h.RecentUtterances = ring
		h.LoopingSince = loopingSince
		h.LastProgressAt = lastProgress
		return nil, nil
	}})
}

// huddleLoopingSince reads a huddle's LoopingSince off the world goroutine.
func huddleLoopingSince(t *testing.T, w *sim.World, id sim.HuddleID) *time.Time {
	t.Helper()
	v := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		h, ok := world.Huddles[id]
		if !ok {
			return (*time.Time)(nil), nil
		}
		return h.LoopingSince, nil
	}})
	ls, _ := v.(*time.Time)
	return ls
}

func wireLoopTelemetry(t *testing.T, w *sim.World) *loopRecordingTelemetry {
	t.Helper()
	sink := &loopRecordingTelemetry{}
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.SetTickTelemetrySink(world, sink)
		return nil, nil
	}})
	return sink
}

// --- pure metric / predicate tests ---

func TestHuddleUtteranceRepetition(t *testing.T) {
	now := time.Now().UTC()
	if got := sim.HuddleUtteranceRepetition(loopingLines(now)); got < 0.6 {
		t.Errorf("looping ring repetition = %.2f, want >= 0.60", got)
	}
	if got := sim.HuddleUtteranceRepetition(variedLines(now)); got >= 0.3 {
		t.Errorf("varied ring repetition = %.2f, want < 0.30", got)
	}
	// Degenerate sizes never read as looping.
	if got := sim.HuddleUtteranceRepetition(nil); got != 0 {
		t.Errorf("empty ring repetition = %.2f, want 0", got)
	}
}

func TestEffectiveHuddleLoop_Defaults(t *testing.T) {
	if sim.HuddleLoopEnabled(sim.WorldSettings{}) {
		t.Error("loop sweep must be OFF by default (zero HuddleLoopTimeout)")
	}
	if !sim.HuddleLoopEnabled(sim.WorldSettings{HuddleLoopTimeout: time.Minute}) {
		t.Error("a positive HuddleLoopTimeout should enable the sweep")
	}
	if got := sim.EffectiveHuddleLoopSweepCadence(sim.WorldSettings{}); got != sim.HuddleLoopSweepCadenceDefault {
		t.Errorf("cadence(zero) = %v, want %v", got, sim.HuddleLoopSweepCadenceDefault)
	}
	wantFrac := float64(sim.HuddleLoopRepeatPercentDefault) / 100
	if got := sim.EffectiveHuddleLoopRepeatFraction(sim.WorldSettings{}); got != wantFrac {
		t.Errorf("repeat fraction(zero) = %.2f, want %.2f", got, wantFrac)
	}
	if got := sim.EffectiveHuddleLoopRepeatFraction(sim.WorldSettings{HuddleLoopRepeatPercent: 80}); got != 0.8 {
		t.Errorf("repeat fraction(80) = %.2f, want 0.80", got)
	}
}

func TestHuddleConversationLooping_Predicate(t *testing.T) {
	now := time.Now().UTC()
	s := sim.WorldSettings{HuddleLoopTimeout: time.Minute, HuddleLoopRepeatPercent: 60}

	looping := &sim.Huddle{RecentUtterances: loopingLines(now)}
	if !sim.HuddleConversationLooping(s, looping, now) {
		t.Error("a fresh, repetitive ring should read as looping")
	}

	varied := &sim.Huddle{RecentUtterances: variedLines(now)}
	if sim.HuddleConversationLooping(s, varied, now) {
		t.Error("a varied conversation must NOT read as looping")
	}

	// Too few turns to judge.
	short := &sim.Huddle{RecentUtterances: loopingLines(now)[:3]}
	if sim.HuddleConversationLooping(s, short, now) {
		t.Error("a ring below the minimum size must not read as looping")
	}

	// Gone quiet — the silence sweep's domain, not this one.
	if sim.HuddleConversationLooping(s, looping, now.Add(5*time.Minute)) {
		t.Error("a stale (quiet) ring must not read as looping")
	}
}

// TestRepublish_ConversationLoopingFlag (LLM-169) pins the core contract: the
// per-actor ConversationLooping flag the snapshot carries for perception is computed
// from the SAME armed-loop signal the sweep uses (repetition predicate + post-dates-
// progress guard). An armed loop flags every member; progress newer than the ring, a
// varied conversation, and a disabled sweep all leave it false — so the per-tick steer
// can never drift from the sweep's arming semantics. republish runs after every command
// (World.Run), so staging the loop state refreshes the snapshot.
func TestRepublish_ConversationLoopingFlag(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	loop := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now)) // a second member, so the loop has an audience
	enableLoopSweep(t, w, 2*time.Minute, 60)

	aliceLooping := func() bool { return w.Published().Actors["alice"].ConversationLooping }

	// Armed: a fresh repetitive ring with no recorded progress → flags BOTH members.
	setHuddleLoopState(t, w, loop, loopingLines(now), nil, time.Time{})
	snap := w.Published()
	if !snap.Actors["alice"].ConversationLooping || !snap.Actors["bob"].ConversationLooping {
		t.Errorf("armed loop should flag both members: alice=%v bob=%v",
			snap.Actors["alice"].ConversationLooping, snap.Actors["bob"].ConversationLooping)
	}

	// Progress newer than the ring disarms it (the huddle advanced after all).
	setHuddleLoopState(t, w, loop, loopingLines(now), nil, now.Add(time.Second))
	if aliceLooping() {
		t.Error("a ring whose newest line pre-dates the last progress must not flag (post-dates-progress guard)")
	}

	// A varied, advancing conversation is never a loop.
	setHuddleLoopState(t, w, loop, variedLines(now), nil, time.Time{})
	if aliceLooping() {
		t.Error("a varied conversation must not flag as looping")
	}

	// Master enable off (HuddleLoopTimeout <= 0) leaves the flag false regardless of repetition.
	setHuddleLoopState(t, w, loop, loopingLines(now), nil, time.Time{})
	enableLoopSweep(t, w, 0, 60)
	if aliceLooping() {
		t.Error("a disabled loop sweep (HuddleLoopTimeout<=0) must leave the flag false")
	}
}

// setHuddlePCUtterance stamps a huddle's LastPCUtteranceAt directly on the world
// goroutine (LLM-185), simulating a player's spoken line without a full PC speak.
func setHuddlePCUtterance(t *testing.T, w *sim.World, id sim.HuddleID, at time.Time) {
	t.Helper()
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		h, ok := world.Huddles[id]
		if !ok {
			t.Fatalf("huddle %q not found when stamping PC utterance", id)
		}
		h.LastPCUtteranceAt = at
		return nil, nil
	}})
}

// TestHuddlePCAttended_Predicate (LLM-185) pins the attendance window: zero =
// never spoke, a recent PC line = attended, a stale one = not, and a negative age
// (out-of-order clock) reads as not-attended.
func TestHuddlePCAttended_Predicate(t *testing.T) {
	now := time.Now().UTC()
	if sim.HuddlePCAttended(&sim.Huddle{}, now) {
		t.Error("a zero LastPCUtteranceAt must read as not-attended")
	}
	if !sim.HuddlePCAttended(&sim.Huddle{LastPCUtteranceAt: now.Add(-30 * time.Second)}, now) {
		t.Error("a PC line 30s ago must read as attended")
	}
	if sim.HuddlePCAttended(&sim.Huddle{LastPCUtteranceAt: now.Add(-10 * time.Minute)}, now) {
		t.Error("a PC line 10m ago is past the window — not attended")
	}
	if sim.HuddlePCAttended(&sim.Huddle{LastPCUtteranceAt: now.Add(time.Minute)}, now) {
		t.Error("a negative age (out-of-order clock) must read as not-attended")
	}
}

// TestHuddleLoopSweep_SparesPlayerAttended (LLM-185): a huddle a player spoke in
// recently is an active human conversation — the sweep must not conclude it even
// when the loop has otherwise persisted past the timeout, and it clears the spell.
func TestHuddleLoopSweep_SparesPlayerAttended(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	enableLoopSweep(t, w, 2*time.Minute, 60)

	onset := now.Add(-3 * time.Minute)          // already past the timeout
	staleProgress := now.Add(-10 * time.Minute) // progress older than the ring
	setHuddleLoopState(t, w, h, loopingLines(now), &onset, staleProgress)
	setHuddlePCUtterance(t, w, h, now.Add(-30*time.Second)) // a player spoke 30s ago

	sendT(t, w, sim.EvaluateHuddleLoopSweep(now))
	if huddleConcludedAt(t, w, h) != nil {
		t.Error("a player-attended huddle must not be concluded even past the timeout")
	}
	if huddleLoopingSince(t, w, h) != nil {
		t.Error("an attended huddle should have its loop spell cleared (restarts fresh once the player goes quiet)")
	}
}

// TestHuddleLoopSweep_ConcludesWhenPlayerSilent (LLM-185) is the tavern-immunity
// guard: a PC parked at the structure but whose last line is well past the
// attention window does NOT shield the NPC loop — the sweep still concludes it.
func TestHuddleLoopSweep_ConcludesWhenPlayerSilent(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	enableLoopSweep(t, w, 2*time.Minute, 60)

	onset := now.Add(-3 * time.Minute)
	staleProgress := now.Add(-10 * time.Minute)
	setHuddleLoopState(t, w, h, loopingLines(now), &onset, staleProgress)
	setHuddlePCUtterance(t, w, h, now.Add(-10*time.Minute)) // last PC line long past the window

	sendT(t, w, sim.EvaluateHuddleLoopSweep(now))
	if huddleConcludedAt(t, w, h) == nil {
		t.Error("a silent, parked PC must not shield an NPC loop — the sweep should still conclude it")
	}
}

// TestRepublish_ConversationLooping_SuppressedWhenPlayerAttended (LLM-185): the
// per-tick steer is suppressed for a player-attended huddle, so the NPCs aren't
// nudged to wrap up / disengage while a player is talking to them.
func TestRepublish_ConversationLooping_SuppressedWhenPlayerAttended(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	loop := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	enableLoopSweep(t, w, 2*time.Minute, 60)

	// Precondition: an armed loop flags the members.
	setHuddleLoopState(t, w, loop, loopingLines(now), nil, time.Time{})
	if !w.Published().Actors["alice"].ConversationLooping {
		t.Fatal("precondition: an armed loop should flag alice")
	}
	// A player speaks → the steer is suppressed for the attended huddle.
	setHuddlePCUtterance(t, w, loop, now.Add(-10*time.Second))
	if w.Published().Actors["alice"].ConversationLooping {
		t.Error("ConversationLooping must be suppressed while the huddle is player-attended")
	}
}

// TestSpeak_StampsLastPCUtterance (LLM-185) exercises the REAL speak path: a
// KindPC member speaking into a huddle must stamp LastPCUtteranceAt so the huddle
// reads as player-attended. The direct-stamp tests above can't prove the stamp
// site itself fires; this covers that integration risk.
func TestSpeak_StampsLastPCUtterance(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now)) // an audience for the line
	// Make alice a player, then drive a real speak through the production command.
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["alice"].Kind = sim.KindPC
		return nil, nil
	}})
	sendT(t, w, sim.Speak("alice", "Good evening — what news from the road?", now))

	res := sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		hud := world.Huddles[h]
		return []any{hud.LastPCUtteranceAt.IsZero(), sim.HuddlePCAttended(hud, now)}, nil
	}})
	v := res.([]any)
	if v[0].(bool) {
		t.Error("a PC's spoken line must stamp LastPCUtteranceAt")
	}
	if !v[1].(bool) {
		t.Error("after a PC speaks, the huddle should read as player-attended")
	}
}

// --- sweep integration tests (full harness) ---

// TestHuddleLoopSweep_ConcludesLoopSparesProductive is the core lever: a sustained
// loop is concluded (members evicted) while a varied conversation is left intact.
func TestHuddleLoopSweep_ConcludesLoopSparesProductive(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	loop := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now)) // a second member, so the loop has an audience
	good := sendT(t, w, sim.JoinHuddle("charlie", "smithy", "", now)).(sim.JoinHuddleResult).HuddleID

	enableLoopSweep(t, w, 2*time.Minute, 60)

	onset := now.Add(-3 * time.Minute)          // LoopingSince already past the timeout
	staleProgress := now.Add(-10 * time.Minute) // progress older than the repetitive ring
	setHuddleLoopState(t, w, loop, loopingLines(now), &onset, staleProgress)
	setHuddleLoopState(t, w, good, variedLines(now), nil, time.Time{})

	sendT(t, w, sim.EvaluateHuddleLoopSweep(now))

	if huddleConcludedAt(t, w, loop) == nil {
		t.Error("a sustained conversational loop should be concluded by the sweep")
	}
	if huddleConcludedAt(t, w, good) != nil {
		t.Error("a varied conversation must NOT be concluded")
	}
	snap := w.Published()
	if got := snap.Actors["alice"].CurrentHuddleID; got != "" {
		t.Errorf("alice CurrentHuddleID = %q, want cleared after loop conclude", got)
	}
	if got := snap.Actors["charlie"].CurrentHuddleID; got != good {
		t.Errorf("charlie CurrentHuddleID = %q, want %q (productive huddle intact)", got, good)
	}
}

// TestHuddleLoopSweep_PersistenceGate confirms a freshly-looping huddle is armed
// (LoopingSince stamped) but NOT concluded until the timeout has elapsed across
// scans.
func TestHuddleLoopSweep_PersistenceGate(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	t0 := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", t0)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", t0))

	// Timeout below the live window so the second scan still sees fresh utterances.
	enableLoopSweep(t, w, 60*time.Second, 60)
	setHuddleLoopState(t, w, h, loopingLines(t0), nil, time.Time{})

	// Scan 1: arms the loop, does not conclude.
	sendT(t, w, sim.EvaluateHuddleLoopSweep(t0))
	if huddleConcludedAt(t, w, h) != nil {
		t.Fatal("loop must not be concluded on the first scan (persistence gate)")
	}

	// Scan 2 a full timeout later: now concludes. Re-stamp the ring fresh at t1 so
	// the live-window guard still holds.
	t1 := t0.Add(60 * time.Second)
	sendT(t, w, sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Huddles[h].RecentUtterances = loopingLines(t1)
		return nil, nil
	}})
	sendT(t, w, sim.EvaluateHuddleLoopSweep(t1))
	if huddleConcludedAt(t, w, h) == nil {
		t.Error("loop should be concluded once it has persisted the full timeout")
	}
}

// TestHuddleLoopSweep_ProgressResets confirms that a transaction / membership
// change more recent than the repetitive ring spares the huddle and clears the
// spell — the loop must re-form with fresh repetitive speech AFTER the progress
// before it can be concluded.
func TestHuddleLoopSweep_ProgressResets(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	enableLoopSweep(t, w, 2*time.Minute, 60)

	onset := now.Add(-3 * time.Minute)               // onset already past the timeout…
	ring := loopingLines(now.Add(-30 * time.Second)) // …a repetitive ring, newest ~34s old…
	progress := now.Add(-5 * time.Second)            // …but a transaction happened AFTER the newest line
	setHuddleLoopState(t, w, h, ring, &onset, progress)

	sendT(t, w, sim.EvaluateHuddleLoopSweep(now))
	if huddleConcludedAt(t, w, h) != nil {
		t.Error("a huddle whose latest progress post-dates its repetitive ring must NOT be concluded")
	}
	if huddleLoopingSince(t, w, h) != nil {
		t.Error("progress newer than the ring should clear LoopingSince (the spell is broken)")
	}
}

// TestHuddleLoopSweep_DisabledNoop confirms the master knob: with the timeout at
// zero the sweep ignores even a blatant, long-running loop.
func TestHuddleLoopSweep_DisabledNoop(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	// Loop sweep left disabled (default zero timeout).
	old := now.Add(-1 * time.Hour)
	setHuddleLoopState(t, w, h, loopingLines(now), &old, time.Time{})

	sendT(t, w, sim.EvaluateHuddleLoopSweep(now))
	if huddleConcludedAt(t, w, h) != nil {
		t.Error("disabled loop sweep must not conclude anything")
	}
}

// TestHuddleLoopSweep_SilentConclusionEmitsTelemetry verifies the conclusion is
// silent (no HuddleConcluded warrant woken) and that a `stuck` telemetry record
// is emitted per member.
func TestHuddleLoopSweep_SilentConclusionEmitsTelemetry(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()
	sink := wireLoopTelemetry(t, w)

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	enableLoopSweep(t, w, 2*time.Minute, 60)
	old := now.Add(-3 * time.Minute)
	setHuddleLoopState(t, w, h, loopingLines(now), &old, time.Time{})

	sendT(t, w, sim.EvaluateHuddleLoopSweep(now))

	if huddleConcludedAt(t, w, h) == nil {
		t.Fatal("loop should be concluded")
	}
	if actorHasWarrantKind(t, w, "alice", sim.WarrantKindHuddleConcluded) {
		t.Error("loop-swept member must NOT carry a HuddleConcluded warrant (conclusion must be silent)")
	}

	var stuck int
	for _, rec := range sink.snapshot() {
		if rec.Kind == "stuck" && rec.Detail["reason"] == "huddle_loop" {
			stuck++
			if rec.Detail["huddle"] != string(h) {
				t.Errorf("telemetry huddle = %q, want %q", rec.Detail["huddle"], h)
			}
		}
	}
	if stuck != 2 {
		t.Errorf("huddle_loop telemetry records = %d, want 2 (one per member)", stuck)
	}
}
