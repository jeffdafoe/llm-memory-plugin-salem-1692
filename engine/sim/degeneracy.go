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

// Defaults for the oscillation arm (LLM-124). Deliberately conservative: the
// arm fires only on a FULL window that shows a tight, sustained shuttle. An
// operator tunes these live alongside the master Stage-1 threshold.
const (
	defaultDegeneracyOscillationWindow         = 8
	defaultDegeneracyOscillationMinTransitions = 3
	defaultDegeneracyOscillationMaxDistinct    = 2
)

// DegenVisit is one scored tick's snapshot in the oscillation window (LLM-124):
// the place the actor ended the tick at — the structure it is inside, else the
// named village object whose loiter pin it stands at (degenVisitScope, LLM-255;
// empty when genuinely in transit or in the open) — and how many of its needs
// were red at that moment. The red count lets the arm tell a pointless shuttle
// (count flat or rising) from a trip that resolved a need (count fell —
// genuine progress, not futile).
type DegenVisit struct {
	Structure StructureID
	RedNeeds  int
}

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

// degeneracyOscillationWindow / …MinTransitions / …MaxDistinct resolve the
// oscillation arm's tunables, each falling back to a safe default when unset so
// the arm is fully configured whenever the observer is enabled.
func (s WorldSettings) degeneracyOscillationWindow() int {
	if s.DegeneracyOscillationWindow > 0 {
		return s.DegeneracyOscillationWindow
	}
	return defaultDegeneracyOscillationWindow
}

func (s WorldSettings) degeneracyOscillationMinTransitions() int {
	if s.DegeneracyOscillationMinTransitions > 0 {
		return s.DegeneracyOscillationMinTransitions
	}
	return defaultDegeneracyOscillationMinTransitions
}

func (s WorldSettings) degeneracyOscillationMaxDistinct() int {
	if s.DegeneracyOscillationMaxDistinct > 0 {
		return s.DegeneracyOscillationMaxDistinct
	}
	return defaultDegeneracyOscillationMaxDistinct
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
		a.DegenVisits = nil
		return
	}
	if !degeneracyTickScored(result.TerminalStatus) {
		return
	}
	// Snapshot this scored tick's structure scope + red-need state into the
	// oscillation window before scoring, so the arm sees the current tick.
	recordDegenVisit(w, a)
	if !degeneracyTickWasFutile(result) && !degeneracyOscillationFutile(w.Settings, a) {
		// A tick that is neither zero-yield nor an unproductive oscillation is
		// productive: it breaks the streak and releases any stage.
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
//	(A) futile-action loop — the model requested tools and EVERY ONE failed,
//	    with none succeeding (the live Prudence all-move_to-rejected case).
//	(B) no-audience theater — nothing changed and there was no one present to
//	    perceive the act (ticking with no one near it).
//
// Anything else — any state change, any material commit, or a productive
// interaction with an audience — is treated as productive (generous default).
//
// Arm A keys on ToolsFailedRejected (every requested call landed in the failed
// set) rather than the looser "nothing succeeded": this is the failed-call
// signal the harness actually records, robust to any future tick-bookkeeping
// that could leave a requested call in neither bucket. NOTE the failed set is
// not exclusively MODEL rejections — a validator/command rejection (Prudence's
// move_to), a same-tick dedup bounce, AND a handler/command error all land
// here. Counting a SUSTAINED all-fail tick as futile regardless of the failure
// cause is deliberate: a tool path that errors every tick for the whole streak
// is still a zero-yield loop worth damping (cost) and surfacing to the operator
// (a `stuck` actor whose tools keep erroring IS a finding), and the
// consecutive-streak requirement plus the auto-recovering, OFF-by-default
// response bound any false escalation off a transient bug.
func degeneracyTickWasFutile(r TickResult) bool {
	// No-yield core. A missing baseline is inconclusive, never "stuck."
	if !r.BaselinePresent || r.StateChanged {
		return false
	}
	if hasMaterialSuccess(r.ToolsSucceeded) {
		return false
	}
	// (A) futile-action loop — the model tried to act and every requested call
	// failed (ToolsFailedRejected is a subset of ToolsRequested by contract, so
	// equal lengths means none succeeded).
	if len(r.ToolsRequested) > 0 && len(r.ToolsFailedRejected) == len(r.ToolsRequested) {
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

// recordDegenVisit appends the current scored tick's post-tick structure scope
// and red-need snapshot to the actor's oscillation window, trimming to the
// configured size (oldest dropped). Runs once per scored tick, before scoring,
// on the world goroutine (it reads w.VillageObjects / w.Assets for the pin
// lookup).
func recordDegenVisit(w *World, a *Actor) {
	a.DegenVisits = append(a.DegenVisits, DegenVisit{
		Structure: degenVisitScope(w, a),
		RedNeeds:  countRedNeeds(w.Settings, a),
	})
	window := w.Settings.degeneracyOscillationWindow()
	if len(a.DegenVisits) > window {
		// Copy the trailing window into a fresh slice so the backing array
		// cannot grow without bound across a long-lived actor.
		a.DegenVisits = append([]DegenVisit(nil), a.DegenVisits[len(a.DegenVisits)-window:]...)
	}
}

// degenVisitScope resolves the place identity a scored tick's visit is
// attributed to: the structure the actor is inside when set, else the named
// village object at whose loiter pin the actor is standing — pin tile or its
// eight visitor slots (LoiterAttributionTiles, the exact inverse of
// pickVisitorSlot). Empty when the actor is in transit or in the open.
//
// LLM-255: keying the window on InsideStructureID alone made the oscillation
// arm blind to any shuttle among structures whose arrival resolves at the
// loiter pin (market stalls, owner-only entry) — the actor is never "inside",
// every visit recorded blank, and the arm could not fire no matter how long
// the shuttle persisted (the live John Ellis Tavern<->General Store case).
//
// The radius is deliberately the ARRIVAL scope, not the wider audience scope
// (AudienceScopeTiles): a visit means the actor is standing at the place, and
// the tighter ring keeps mid-walk pass-bys near a named object from minting
// phantom visits that could assemble a false oscillation window
// (code_review). Radius 0 would be too tight the other way — an arriving
// visitor stands on the slot ring at Chebyshev 1, not the pin tile itself.
//
// A bare named prop (a well, a shade tree) has no Structure entry; its object
// id still identifies the place for the arm's transition / distinct-place
// counting, and buildings share one id across both maps (the shared-identity
// bridge), so the cast is safe for both. Nothing dereferences the id through
// w.Structures — it is an identity token only.
func degenVisitScope(w *World, a *Actor) StructureID {
	if a.InsideStructureID != "" {
		return a.InsideStructureID
	}
	if objID, ok := ResolveLoiteringObject(w.VillageObjects, w.Assets, a.Pos, LoiterAttributionTiles); ok {
		return StructureID(objID)
	}
	return ""
}

// countRedNeeds counts how many of the actor's needs sit at or past their red
// threshold right now, using the world's (settings-tuned) thresholds.
func countRedNeeds(s WorldSettings, a *Actor) int {
	n := 0
	for _, need := range Needs {
		if a.Needs[need.Key] >= s.NeedThresholds.Get(need.Key) {
			n++
		}
	}
	return n
}

// degeneracyOscillationFutile reports whether the actor's recent scored ticks
// form a tight structure oscillation with no goal-completion — futile even
// though each leg state-changed (so degeneracyTickWasFutile alone reads it as
// productive). This is the LLM-124 arm: it measures NET progress over a window
// rather than per-tick yield, catching the Ezekiel Crane Blacksmith<->Tavern
// shuttle where every move_to leg individually looks productive.
//
// It requires a FULL window (sustained behavior). "Tight oscillation": collapse
// the window's post-tick structures (dropping in-transit blanks and consecutive
// repeats) into a visit sequence; the actor must have changed structure at
// least MinTransitions times while touching no more than MaxDistinct distinct
// structures — i.e. it keeps returning to the same few places. "No
// goal-completion": the red-need count never fell anywhere in the window. A red
// need that resolved (the count dropped between two consecutive ticks) is real
// progress and exempts the window, so incidental eating/drinking mid-shuttle is
// not mistaken for thrashing.
func degeneracyOscillationFutile(s WorldSettings, a *Actor) bool {
	window := s.degeneracyOscillationWindow()
	if len(a.DegenVisits) < window {
		return false
	}
	// Goal-completion guard: if the red-need count dropped at any point in the
	// window, the actor resolved a need on one of these trips — genuine
	// progress, not a futile shuttle. (Endpoint comparison alone would miss a
	// resolve-then-reclimb within the window.)
	for i := 1; i < len(a.DegenVisits); i++ {
		if a.DegenVisits[i].RedNeeds < a.DegenVisits[i-1].RedNeeds {
			return false
		}
	}
	// Collapse the window into a visit sequence: drop in-transit blanks and
	// consecutive repeats, so dwelling in one place doesn't read as movement.
	var seq []StructureID
	for _, v := range a.DegenVisits {
		if v.Structure == "" {
			continue
		}
		if len(seq) > 0 && seq[len(seq)-1] == v.Structure {
			continue
		}
		seq = append(seq, v.Structure)
	}
	if len(seq)-1 < s.degeneracyOscillationMinTransitions() {
		return false
	}
	distinct := make(map[StructureID]struct{}, len(seq))
	for _, id := range seq {
		distinct[id] = struct{}{}
	}
	return len(distinct) <= s.degeneracyOscillationMaxDistinct()
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
