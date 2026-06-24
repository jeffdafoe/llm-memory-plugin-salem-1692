package sim

import (
	"strconv"
	"time"
)

// Degeneracy observer (LLM-94)
//
// The reactor decides WHEN to wake an actor (warrants + cadence) but never
// looks back at WHAT the waking produced. So an NPC can burn a full LLM
// deliberation every cadence cycle, indefinitely, accomplishing nothing —
// real model cost, and (when the agent has gone off the rails) a drift signal
// we otherwise can't see. The live case: Prudence Ward firing six rejected
// move_to calls a tick, every minute, for half an hour, never moving.
//
// This observer is the DETECTION + telemetry half: after each completed tick
// it scores the tick's YIELD and tracks a per-actor streak of consecutive
// obviously-futile ticks. When the streak crosses a threshold it advances a
// per-actor DegeneracyStage and writes a redacted telemetry record the
// umbilical surfaces. The Stage-1 (perception thinning) and Stage-2 (surgical
// warrant throttle) RESPONSES read DegenStage; they are layered on separately.
//
// Design posture (LLM-94): GENEROUS / high-precision. The observer is OFF
// unless explicitly enabled, and only ever flags obviously-egregious SUSTAINED
// futility. A missing baseline, any state change, any material commit, or an
// audience-bearing interaction all read as productive. It is fine to miss
// subtle low-yield cases — the bar is "this is plainly broken." The narrower
// red-need backstop ([[red_need_backstop_commands.go]]) handles the specific
// stuck-on-a-red-need cadence; this generalizes the idea to any zero-yield
// loop.

// DegeneracyStage is the per-actor escalation level of the degeneracy
// observer. It advances as a futility streak lengthens and resets to
// DegeneracyNone the moment a productive tick (or, later, a salient warrant)
// breaks the streak.
type DegeneracyStage int

const (
	// DegeneracyNone — the actor is not in a tracked futility streak (or the
	// streak is below the first threshold). The zero value.
	DegeneracyNone DegeneracyStage = iota
	// DegeneracyFlagged — the streak crossed the Stage-1 ("thin the driving
	// perception") threshold. Surfaced to the operator; gentle response.
	DegeneracyFlagged
	// DegeneracyThrottled — the streak crossed the Stage-2 ("surgically raise
	// the wake threshold") threshold: obviously-egregious sustained futility.
	DegeneracyThrottled
)

// String renders the stage as a stable lowercase label for telemetry.
func (d DegeneracyStage) String() string {
	switch d {
	case DegeneracyNone:
		return "none"
	case DegeneracyFlagged:
		return "flagged"
	case DegeneracyThrottled:
		return "throttled"
	default:
		return "unknown"
	}
}

// Defaults for the degeneracy observer's sub-thresholds. Deliberately
// generous: Stage 1 engages only after several unbroken futile ticks, Stage 2
// only after a long sustained streak that also spans a wall-clock minimum.
const (
	defaultDegeneracyThrottleAfterTicks  = 20
	defaultDegeneracyThrottleMinDuration = 15 * time.Minute
	defaultDegeneracyThrottleBackoff     = 5 * time.Minute
)

// degeneracyEnabled reports whether the observer is active. It is OFF unless
// explicitly enabled via a positive DegeneracyThinAfterTicks — a deliberately
// opt-in posture for a gate that can suppress an agent's ticks. The safe
// default is to do nothing; an operator turns it on (and tunes it live)
// against the running village. This is also why the master knob is the
// Stage-1 threshold rather than a separate bool: one positive number both
// enables the observer and sets its first trigger point.
func (s WorldSettings) degeneracyEnabled() bool {
	return s.DegeneracyThinAfterTicks > 0
}

// degeneracyThrottleAfterTicks / …MinDuration / …Backoff resolve the Stage-2
// sub-knobs, falling back to safe defaults when left unset so an operator who
// only sets the master Stage-1 threshold still gets a sane Stage-2.
func (s WorldSettings) degeneracyThrottleAfterTicks() int {
	if s.DegeneracyThrottleAfterTicks > 0 {
		return s.DegeneracyThrottleAfterTicks
	}
	return defaultDegeneracyThrottleAfterTicks
}

func (s WorldSettings) degeneracyThrottleMinDuration() time.Duration {
	if s.DegeneracyThrottleMinDuration > 0 {
		return s.DegeneracyThrottleMinDuration
	}
	return defaultDegeneracyThrottleMinDuration
}

func (s WorldSettings) degeneracyThrottleBackoff() time.Duration {
	if s.DegeneracyThrottleBackoff > 0 {
		return s.DegeneracyThrottleBackoff
	}
	return defaultDegeneracyThrottleBackoff
}

// updateDegeneracy folds a completed tick's yield into the actor's futility
// streak. Called by CompleteReactorTick on a matching (non-stale) completion,
// on the world goroutine, so the per-actor tracker is mutated race-free.
//
// No-op when the observer is disabled. Only SUBSTANTIVE ticks (the LLM
// actually deliberated) are scored: cheap skip / stale / before-render /
// shutdown completions are NEUTRAL — they neither increment the streak nor
// reset it, because an idle actor that the noop-skip gate keeps cheap is not
// evidence of degeneracy.
func updateDegeneracy(w *World, a *Actor, result TickResult, now time.Time) {
	if !w.Settings.degeneracyEnabled() {
		// Observer disabled. Actively unwind any stage left over from when it
		// was enabled so turning it off lifts the Stage-1 thinning and the
		// Stage-2 throttle rather than leaving them stuck. The Stage-2 throttle
		// gate also checks degeneracyEnabled, so a throttled actor still reaches
		// a scored tick here and clears on its own.
		if a.DegenStage != DegeneracyNone || a.DegenStreak != 0 {
			clearDegeneracy(w, a, now)
		}
		return
	}
	if !degeneracyTickScored(result.TerminalStatus) {
		return
	}
	if !degeneracyTickWasFutile(result) {
		// A productive tick breaks the streak and releases any stage.
		clearDegeneracy(w, a, now)
		return
	}
	if a.DegenStreak == 0 {
		t := now
		a.DegenStreakSince = &t
	}
	a.DegenStreak++
	advanceDegeneracyStage(w, a, now)
}

// degeneracyTickScored reports whether a terminal status means the LLM
// actually deliberated this tick — only those count toward (or break) a
// futility streak. Skipped / stale / before-render / shutdown are neutral.
func degeneracyTickScored(s TickTerminalStatus) bool {
	switch s {
	case TickStatusSuccess, TickStatusDone, TickStatusBudgetForced, TickStatusFailedAfterRender:
		return true
	default:
		return false
	}
}

// degeneracyTickWasFutile reports whether a scored tick accomplished nothing
// worth the LLM call. The "no-yield core" is a present scene baseline showing
// no change since the actor arrived AND no successful world-mutating commit
// beyond speech. On top of that, one of two obviously-futile signatures:
//
//	(A) futile-action loop — the model requested tools and every one was
//	    rejected (the live Prudence all-move_to-rejected case).
//	(B) no-audience theater — nothing changed and there was no one present to
//	    perceive the act (ticking with no one near it).
//
// Anything else — any state change, any material commit, or a productive
// interaction with an audience — is treated as productive (generous default).
func degeneracyTickWasFutile(r TickResult) bool {
	// No-yield core. A missing baseline is inconclusive, never "stuck."
	if !r.BaselinePresent || r.StateChanged {
		return false
	}
	if hasMaterialSuccess(r.ToolsSucceeded) {
		return false
	}
	// (A) futile-action loop.
	if len(r.ToolsRequested) > 0 && len(r.ToolsSucceeded) == 0 {
		return true
	}
	// (B) no-audience theater.
	if !r.HadAudience {
		return true
	}
	return false
}

// hasMaterialSuccess reports whether any succeeded tool had a real-world
// effect — anything other than speech or a memory recall. A material success
// means the tick accomplished something, so it is not futile. (A successful
// move_to, consume, gather, pay, etc. all count as material progress.)
func hasMaterialSuccess(succeeded []string) bool {
	for _, name := range succeeded {
		switch name {
		case "speak", "recall":
			// non-material — a social act / a memory lookup
		default:
			return true
		}
	}
	return false
}

// advanceDegeneracyStage recomputes the actor's stage from the current streak
// length (and, for Stage 2, the streak's wall-clock span) and emits a
// telemetry record on any transition. Stage 2 requires BOTH a long tick streak
// AND a minimum elapsed duration so a burst of fast ticks can't trip the clamp
// before the behavior has plainly persisted.
func advanceDegeneracyStage(w *World, a *Actor, now time.Time) {
	s := w.Settings
	newStage := DegeneracyNone
	if a.DegenStreak >= s.DegeneracyThinAfterTicks {
		newStage = DegeneracyFlagged
	}
	if a.DegenStreak >= s.degeneracyThrottleAfterTicks() &&
		a.DegenStreakSince != nil &&
		now.Sub(*a.DegenStreakSince) >= s.degeneracyThrottleMinDuration() {
		newStage = DegeneracyThrottled
	}
	if newStage != a.DegenStage {
		prev := a.DegenStage
		a.DegenStage = newStage
		writeDegeneracyTelemetry(w, a, now, prev)
	}
}

// clearDegeneracy resets a streak after a productive tick. When the actor was
// flagged/throttled, the reset is a recovery transition and is surfaced as
// telemetry so an operator sees the actor come back on its own.
func clearDegeneracy(w *World, a *Actor, now time.Time) {
	prev := a.DegenStage
	a.DegenStreak = 0
	a.DegenStreakSince = nil
	if prev != DegeneracyNone {
		a.DegenStage = DegeneracyNone
		writeDegeneracyTelemetry(w, a, now, prev)
	}
}

// writeDegeneracyTelemetry emits a redacted stage-transition record to the
// tick telemetry ring (the umbilical surfaces it). `stuck` for an escalation
// into a non-None stage, `recovered` for a return to None. Like all tick
// telemetry it carries only labels — no prompts, responses, or tool args.
func writeDegeneracyTelemetry(w *World, a *Actor, now time.Time, prev DegeneracyStage) {
	if w.repo.TickTelemetry == nil {
		return
	}
	kind := "stuck"
	if a.DegenStage == DegeneracyNone {
		kind = "recovered"
	}
	w.repo.TickTelemetry.WriteTickTelemetry(TickTelemetryRecord{
		At:      now,
		ActorID: a.ID,
		Kind:    kind,
		Detail: map[string]string{
			"stage":      a.DegenStage.String(),
			"prev_stage": prev.String(),
			"streak":     strconv.Itoa(a.DegenStreak),
		},
	})
}

// isAmbientWarrantKind reports whether a warrant kind is AMBIENT for the
// Stage-2 throttle: engine-injected liveness or self-narration with no external
// counterparty — the wakeups it is safe to defer for a known-degenerate actor.
// The default is SALIENT (return false): every speech, huddle-join, economic,
// need-threshold, arrival, or operator warrant passes through, so the throttle
// only ever slows the engine's own poking of a stuck actor, never a real
// interaction. Default-is-salient is the safe direction — a newly added kind is
// never deferred by accident (the same posture as handlers.isLowInfoWarrantKind,
// which this deliberately does NOT reuse: that classifier answers a different
// question ["nothing to react to THIS tick", which also covers huddle-departure
// kinds] and lives in handlers, which imports sim — the throttle runs inside
// sim's EvaluateReactors and cannot call back into it).
func isAmbientWarrantKind(k WarrantKind) bool {
	switch k {
	case WarrantKindIdleBackstop,
		WarrantKindStranded,
		WarrantKindShiftDuty,
		WarrantKindRestock,
		WarrantKindDwellStarted,
		WarrantKindDwellTickApplied,
		WarrantKindDwellEnded:
		return true
	default:
		return false
	}
}

// warrantCycleAllAmbient reports whether every warrant in a pending cycle is
// ambient — the condition under which the Stage-2 throttle defers the actor's
// wake. A single salient warrant makes the whole cycle salient and the throttle
// steps aside. An empty cycle returns false (defensive: never defer a cycle the
// evaluator somehow reached with nothing in it).
func warrantCycleAllAmbient(list []WarrantMeta) bool {
	if len(list) == 0 {
		return false
	}
	for _, m := range list {
		if !isAmbientWarrantKind(m.Kind()) {
			return false
		}
	}
	return true
}
