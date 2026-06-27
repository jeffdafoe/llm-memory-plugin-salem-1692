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

	old := now.Add(-3 * time.Minute) // LoopingSince already past the timeout
	setHuddleLoopState(t, w, loop, loopingLines(now), &old, time.Time{})
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

// TestHuddleLoopSweep_ProgressResets confirms a transaction/membership change
// recorded after the loop onset spares the huddle — it is advancing, not stuck.
func TestHuddleLoopSweep_ProgressResets(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	h := sendT(t, w, sim.JoinHuddle("alice", "tavern", "", now)).(sim.JoinHuddleResult).HuddleID
	sendT(t, w, sim.JoinHuddle("bob", "tavern", "", now))
	enableLoopSweep(t, w, 2*time.Minute, 60)

	old := now.Add(-3 * time.Minute)      // onset already past timeout…
	progress := now.Add(-1 * time.Minute) // …but progress happened after onset
	setHuddleLoopState(t, w, h, loopingLines(now), &old, progress)

	sendT(t, w, sim.EvaluateHuddleLoopSweep(now))
	if huddleConcludedAt(t, w, h) != nil {
		t.Error("a huddle with progress after loop onset must NOT be concluded")
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
