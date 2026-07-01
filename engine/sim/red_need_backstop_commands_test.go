package sim

import (
	"testing"
	"time"
)

// red_need_backstop_commands_test.go — ZBBS-HOME-363. Substrate tests for the
// red-need backstop sweep + its per-actor exponential backoff (the cost
// guard). Drives EvaluateRedNeedBackstop(now).Fn(w) directly on an in-memory
// World (no goroutine) so the time-based backoff is fully deterministic.
// Reuses sleepTestWorld / agentNPCWithNeeds from the sibling sleep / needs
// tests (same package). Default thresholds: hunger 18, thirst 12, tiredness
// 20; default backoff base 90 s, max 30 min.

// evalRedNeed runs one sweep and returns the telemetry.
func evalRedNeed(t *testing.T, w *World, now time.Time) RedNeedBackstopTelemetry {
	t.Helper()
	v, err := EvaluateRedNeedBackstop(now).Fn(w)
	if err != nil {
		t.Fatalf("EvaluateRedNeedBackstop: %v", err)
	}
	tm, ok := v.(RedNeedBackstopTelemetry)
	if !ok {
		t.Fatalf("EvaluateRedNeedBackstop returned %T, want RedNeedBackstopTelemetry", v)
	}
	return tm
}

// nextDelay reports the gap between the actor's RedNeedNextWarrantAt and the
// supplied stamp moment — i.e. the backoff delay just applied.
func nextDelay(t *testing.T, a *Actor, stampedAt time.Time) time.Duration {
	t.Helper()
	if a.RedNeedNextWarrantAt == nil {
		t.Fatal("RedNeedNextWarrantAt is nil — no backoff timer set")
	}
	return a.RedNeedNextWarrantAt.Sub(stampedAt)
}

// TestRedNeedBackstop_StampsRedNeedIdleActor: an in-scope NPC sitting on a
// red need with no open warrant gets a need_threshold warrant, and the
// backoff state initializes (level 0, base-delay timer, tracked need/value).
func TestRedNeedBackstop_StampsRedNeedIdleActor(t *testing.T) {
	a := agentNPCWithNeeds("n", 24, 5, 5) // hunger 24 ≥ red 18
	w := sleepTestWorld(a)
	now := time.Now().UTC()

	tm := evalRedNeed(t, w, now)
	if tm.Stamped != 1 {
		t.Fatalf("Stamped = %d, want 1; telemetry=%+v", tm.Stamped, tm)
	}
	if a.WarrantedSince == nil {
		t.Fatal("no WarrantedSince after red-need backstop")
	}
	if !hasNeedThresholdWarrant(a) {
		t.Fatalf("warrant is not need_threshold; kinds=%v", warrantKinds(a))
	}
	for _, m := range a.Warrants {
		if r, ok := m.Reason.(NeedThresholdWarrantReason); ok && r.Need != "hunger" {
			t.Errorf("warrant need = %q, want hunger", r.Need)
		}
	}
	if a.RedNeedBackoffLevel != 0 {
		t.Errorf("backoff level = %d, want 0 on first stamp", a.RedNeedBackoffLevel)
	}
	if a.RedNeedLastKey != "hunger" || a.RedNeedLastValue != 24 {
		t.Errorf("tracked (key,value) = (%q,%d), want (hunger,24)", a.RedNeedLastKey, a.RedNeedLastValue)
	}
	if d := nextDelay(t, a, now); d != defaultRedNeedBackstopBaseDelay {
		t.Errorf("first delay = %v, want base %v", d, defaultRedNeedBackstopBaseDelay)
	}
}

// TestRedNeedBackstop_SkipsNoRedNeed: an actor below every threshold is not
// warranted, and any stale backoff state is cleared.
func TestRedNeedBackstop_SkipsNoRedNeed(t *testing.T) {
	a := agentNPCWithNeeds("n", 5, 5, 5) // all green
	// Seed stale backoff state to prove it gets cleared.
	stale := time.Now().UTC()
	a.RedNeedNextWarrantAt = &stale
	a.RedNeedBackoffLevel = 3
	a.RedNeedLastKey = "hunger"
	a.RedNeedLastValue = 24
	w := sleepTestWorld(a)

	tm := evalRedNeed(t, w, time.Now().UTC())
	if tm.Stamped != 0 || tm.SkippedNoRedNeed != 1 {
		t.Fatalf("Stamped=%d SkippedNoRedNeed=%d, want 0/1; telemetry=%+v", tm.Stamped, tm.SkippedNoRedNeed, tm)
	}
	if a.WarrantedSince != nil {
		t.Error("green actor was warranted")
	}
	if a.RedNeedNextWarrantAt != nil || a.RedNeedBackoffLevel != 0 || a.RedNeedLastKey != "" {
		t.Errorf("backoff state not cleared: next=%v level=%d key=%q", a.RedNeedNextWarrantAt, a.RedNeedBackoffLevel, a.RedNeedLastKey)
	}
}

// TestRedNeedBackstop_SkipsScopeAndState: PCs, transient visitors,
// already-warranted, and mid-tick actors are all skipped.
func TestRedNeedBackstop_SkipsScopeAndState(t *testing.T) {
	pc := agentNPCWithNeeds("pc", 24, 5, 5)
	pc.Kind = KindPC
	visitor := agentNPCWithNeeds("vis", 24, 5, 5)
	visitor.VisitorState = &VisitorState{}
	warranted := agentNPCWithNeeds("war", 24, 5, 5)
	flight := agentNPCWithNeeds("fly", 24, 5, 5)
	flight.TickInFlight = true
	w := sleepTestWorld(pc, visitor, warranted, flight)

	now := time.Now().UTC()
	// Give "war" an open warrant cycle.
	ws := now
	warranted.WarrantedSince = &ws
	warranted.WarrantDueAt = &ws
	warranted.Warrants = []WarrantMeta{{Reason: BasicWarrantReason{K: WarrantKindNPCSpoke}}}

	tm := evalRedNeed(t, w, now)
	if tm.Stamped != 0 {
		t.Errorf("Stamped = %d, want 0; telemetry=%+v", tm.Stamped, tm)
	}
	if tm.SkippedScope != 2 {
		t.Errorf("SkippedScope = %d, want 2 (pc + visitor)", tm.SkippedScope)
	}
	if tm.SkippedWarranted != 1 {
		t.Errorf("SkippedWarranted = %d, want 1", tm.SkippedWarranted)
	}
	if tm.SkippedTickInFlight != 1 {
		t.Errorf("SkippedTickInFlight = %d, want 1", tm.SkippedTickInFlight)
	}
}

// TestRedNeedBackstop_TirednessOffShiftExcluded: a tiredness-only red need is
// not actionable off-shift (the deterministic sleep loop owns overnight
// tiredness), mirroring the needs-tick carve-out.
func TestRedNeedBackstop_TirednessOffShiftExcluded(t *testing.T) {
	a := agentNPCWithNeeds("n", 5, 5, 24) // only tiredness red; no schedule → off-shift
	w := sleepTestWorld(a)

	tm := evalRedNeed(t, w, time.Now().UTC())
	if tm.Stamped != 0 || tm.SkippedNoRedNeed != 1 {
		t.Fatalf("Stamped=%d SkippedNoRedNeed=%d, want 0/1 (tiredness off-shift not actionable); telemetry=%+v",
			tm.Stamped, tm.SkippedNoRedNeed, tm)
	}
}

// TestRedNeedBackstop_RespectsBackoffWindow: a stalled need still inside its
// backoff window is not re-stamped — the core cost guard.
func TestRedNeedBackstop_RespectsBackoffWindow(t *testing.T) {
	a := agentNPCWithNeeds("n", 24, 5, 5)
	w := sleepTestWorld(a)
	t0 := time.Now().UTC()

	evalRedNeed(t, w, t0) // first stamp; next = t0 + 90s
	clearWarrant(a)       // simulate the tick firing + evaluator clearing it

	// 30 s later — inside the 90 s window, need unchanged (stalled).
	tm := evalRedNeed(t, w, t0.Add(30*time.Second))
	if tm.Stamped != 0 || tm.SkippedBackoff != 1 {
		t.Fatalf("Stamped=%d SkippedBackoff=%d, want 0/1 (inside backoff window); telemetry=%+v",
			tm.Stamped, tm.SkippedBackoff, tm)
	}
	if a.WarrantedSince != nil {
		t.Error("re-warranted inside the backoff window")
	}
}

// TestRedNeedBackstop_EscalatesOnStall: a need that makes no progress doubles
// the backoff each time the window elapses — 90 s → 180 s → 360 s.
func TestRedNeedBackstop_EscalatesOnStall(t *testing.T) {
	a := agentNPCWithNeeds("n", 24, 5, 5)
	w := sleepTestWorld(a)
	base := defaultRedNeedBackstopBaseDelay

	now := time.Now().UTC()
	evalRedNeed(t, w, now)
	if d := nextDelay(t, a, now); d != base {
		t.Fatalf("delay[0] = %v, want %v", d, base)
	}

	for level, wantMult := range []int{2, 4, 8} { // levels 1,2,3
		clearWarrant(a)
		now = *a.RedNeedNextWarrantAt // advance exactly to the due moment
		tm := evalRedNeed(t, w, now)
		if tm.Stamped != 1 {
			t.Fatalf("level %d: Stamped = %d, want 1; telemetry=%+v", level+1, tm.Stamped, tm)
		}
		if a.RedNeedBackoffLevel != level+1 {
			t.Errorf("level = %d, want %d", a.RedNeedBackoffLevel, level+1)
		}
		if d := nextDelay(t, a, now); d != time.Duration(wantMult)*base {
			t.Errorf("delay at level %d = %v, want %v", level+1, d, time.Duration(wantMult)*base)
		}
	}
}

// TestRedNeedBackstop_ResetsOnProgress: when the need value drops since the
// last stamp (the actor actually consumed something), the backoff resets to
// base even mid-window, and re-engages — so a resolving actor keeps getting
// nudged until it is fed rather than waiting out an escalated timer.
func TestRedNeedBackstop_ResetsOnProgress(t *testing.T) {
	a := agentNPCWithNeeds("n", 24, 5, 5)
	w := sleepTestWorld(a)
	base := defaultRedNeedBackstopBaseDelay
	t0 := time.Now().UTC()

	// Escalate once so the level is non-zero before progress.
	evalRedNeed(t, w, t0)
	clearWarrant(a)
	t1 := *a.RedNeedNextWarrantAt
	evalRedNeed(t, w, t1) // stalled → level 1
	if a.RedNeedBackoffLevel != 1 {
		t.Fatalf("setup: level = %d, want 1", a.RedNeedBackoffLevel)
	}
	clearWarrant(a)

	// Actor consumed: hunger 24 → 20 (still red). Sweep INSIDE the level-1
	// window (well before next due) — progress must bypass the window.
	a.Needs["hunger"] = 20
	t2 := t1.Add(30 * time.Second)
	tm := evalRedNeed(t, w, t2)
	if tm.Stamped != 1 {
		t.Fatalf("progress sweep: Stamped = %d, want 1 (bypass window); telemetry=%+v", tm.Stamped, tm)
	}
	if a.RedNeedBackoffLevel != 0 {
		t.Errorf("level after progress = %d, want 0 (reset)", a.RedNeedBackoffLevel)
	}
	if a.RedNeedLastValue != 20 {
		t.Errorf("tracked value = %d, want 20", a.RedNeedLastValue)
	}
	if d := nextDelay(t, a, t2); d != base {
		t.Errorf("delay after progress = %v, want base %v", d, base)
	}
}

// TestRedNeedBackstop_ResolvedClearsState: once the need falls below its
// threshold the actor is no longer eligible (no warrant, no cost) and the
// backoff state is wiped, so a future red need starts fresh at base.
func TestRedNeedBackstop_ResolvedClearsState(t *testing.T) {
	a := agentNPCWithNeeds("n", 24, 5, 5)
	w := sleepTestWorld(a)
	now := time.Now().UTC()

	evalRedNeed(t, w, now)
	clearWarrant(a)
	if a.RedNeedNextWarrantAt == nil {
		t.Fatal("setup: expected backoff state after first stamp")
	}

	a.Needs["hunger"] = 10 // resolved (below red 18)
	tm := evalRedNeed(t, w, now.Add(2*time.Minute))
	if tm.Stamped != 0 || tm.SkippedNoRedNeed != 1 {
		t.Fatalf("Stamped=%d SkippedNoRedNeed=%d, want 0/1; telemetry=%+v", tm.Stamped, tm.SkippedNoRedNeed, tm)
	}
	if a.RedNeedNextWarrantAt != nil || a.RedNeedLastKey != "" {
		t.Errorf("backoff state not cleared after resolution: next=%v key=%q", a.RedNeedNextWarrantAt, a.RedNeedLastKey)
	}
}

// TestRedNeedBackoffDelay_CapsAtMax: the exponential is clamped to maxDelay,
// and a large level never overflows.
func TestRedNeedBackoffDelay_CapsAtMax(t *testing.T) {
	base := 90 * time.Second
	max := 30 * time.Minute
	cases := []struct {
		level int
		want  time.Duration
	}{
		{0, 90 * time.Second},
		{1, 180 * time.Second},
		{2, 360 * time.Second},
		{3, 720 * time.Second},
		{4, 1440 * time.Second}, // 24 min — still under cap
		{5, max},                // 2880 s would exceed → capped to 30 min
		{40, max},               // no overflow at a large level
	}
	for _, tc := range cases {
		if got := redNeedBackoffDelay(base, max, tc.level); got != tc.want {
			t.Errorf("redNeedBackoffDelay(base,max,%d) = %v, want %v", tc.level, got, tc.want)
		}
	}

	// Overflow pressure (code_review #2): a huge base+maxDelay must clamp to
	// maxDelay, never wrap int64 negative. The clamp-before-doubling guard
	// returns maxDelay the moment another double would exceed it.
	const bigMax = time.Duration(1) << 62
	bigBase := time.Duration(1) << 60
	for _, level := range []int{1, 2, 5, 40} {
		got := redNeedBackoffDelay(bigBase, bigMax, level)
		if got < 0 {
			t.Errorf("overflow: redNeedBackoffDelay(1<<60, 1<<62, %d) = %v (negative)", level, got)
		}
		if got > bigMax {
			t.Errorf("redNeedBackoffDelay(1<<60, 1<<62, %d) = %v exceeds max %v", level, got, bigMax)
		}
	}

	// Degenerate inputs guard against reuse outside the clamping caller.
	if got := redNeedBackoffDelay(0, 30*time.Minute, 3); got != 0 {
		t.Errorf("base<=0: got %v, want 0", got)
	}
	if got := redNeedBackoffDelay(90*time.Second, 30*time.Second, 3); got != 90*time.Second {
		t.Errorf("maxDelay<base: got %v, want base 90s", got)
	}
}

// TestNeedProducers_OpenTirednessWarrantNotRestamped pins that neither need-warrant
// producer re-stamps an on-break actor carrying an already-open tiredness warrant.
// LLM-62 (slice 2) first kept a red-tiredness warrant from interrupting a break (the
// break is the cure); LLM-211 goes further — an on-break actor is suppressed at the
// source, exactly like a sleeper (actorActionableRedNeed returns nothing while
// resting), so NO need (tiredness or hunger/thirst) is evaluated as actionable. The
// already-open warrant therefore survives untouched: the fast backstop sweep skips
// it as SkippedNoRedNeed (the need is suppressed by the break, not merely gated by
// the already-warranted guard), and the hourly needs tick's warrantEligible gate
// (WarrantedSince==nil) skips it too — neither appends a second warrant. Drives BOTH
// producers against an on-shift exhausted vendor on break. (The SkippedWarranted
// already-warranted guard for a NON-resting actor is covered separately — see the
// crossing/warranted cases above and in idle_backstop_commands_test.go.)
func TestNeedProducers_OpenTirednessWarrantNotRestamped(t *testing.T) {
	allDayStart, allDayEnd := 0, 1440          // on shift at any minute-of-day (covers the wall-clock hourly tick)
	a := agentNPCWithNeeds("vendor", 5, 5, 23) // only tiredness red (≥ red 20)
	a.ScheduleStartMin, a.ScheduleEndMin = &allDayStart, &allDayEnd
	a.State = StateResting // on a break — the slice-2 scenario

	now := time.Now().UTC()
	future := now.Add(4 * time.Hour)
	a.BreakUntil = &future
	// Already holding an OPEN tiredness warrant cycle — the shelved warrant the
	// reactor refuses to act on while on break.
	a.WarrantedSince = &now
	a.WarrantDueAt = &now
	a.Warrants = []WarrantMeta{{Reason: NeedThresholdWarrantReason{Need: "tiredness"}}}

	w := needsTickWorld(1, a) // NeedsTickAmount=1 so the hourly tick actually runs

	// Producer 1 — fast red-need backstop sweep. The actor is on a break, so
	// actorActionableRedNeed suppresses its red need entirely (LLM-211, like a
	// sleeper): the sweep finds no actionable need and skips it as SkippedNoRedNeed,
	// so the already-open warrant is never re-stamped.
	if tm := evalRedNeed(t, w, now); tm.Stamped != 0 || tm.SkippedNoRedNeed != 1 {
		t.Fatalf("backstop sweep: Stamped=%d SkippedNoRedNeed=%d, want 0/1; telemetry=%+v", tm.Stamped, tm.SkippedNoRedNeed, tm)
	}

	// Producer 2 — hourly needs tick. Increments needs but must not stamp a
	// second warrant while a cycle is open.
	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}

	// Exactly the one original shelved tiredness warrant survives, and the cycle
	// marker is untouched — no re-stamp spam from either producer.
	if len(a.Warrants) != 1 {
		t.Fatalf("want exactly 1 warrant (the shelved tiredness one), got %d: %+v", len(a.Warrants), a.Warrants)
	}
	if r, ok := a.Warrants[0].Reason.(NeedThresholdWarrantReason); !ok || r.Need != "tiredness" {
		t.Errorf("surviving warrant = %+v, want a tiredness need_threshold warrant", a.Warrants[0].Reason)
	}
	if a.WarrantedSince == nil || !a.WarrantedSince.Equal(now) {
		t.Errorf("WarrantedSince changed: got %v, want unchanged %v", a.WarrantedSince, now)
	}
}
