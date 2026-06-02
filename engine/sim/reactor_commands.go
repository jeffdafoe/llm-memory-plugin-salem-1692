package sim

import (
	"fmt"
	mathrand "math/rand/v2"
	"time"
)

// StampWarrantResult is what StampWarrant returns. Stamped is true when
// the call started a fresh warrant cycle (actor wasn't warranted before);
// false when it appended to an existing cycle.
type StampWarrantResult struct {
	Stamped bool // true on fresh cycle, false on append-to-existing
}

// StampWarrant returns a Command that funnels a warrant stamp through
// tryStampWarrant. Public command form for callers outside the package
// (admin endpoints, test setup); internal callsites in command handlers
// call tryStampWarrant directly (they already hold the world goroutine).
//
// Rejects with error when actorID isn't a known actor. meta.Reason must
// be non-nil — that's the load-bearing carrier for the warrant kind.
func StampWarrant(actorID ActorID, meta WarrantMeta, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if meta.Reason == nil {
				return StampWarrantResult{}, fmt.Errorf("warrant meta requires a non-nil Reason")
			}
			actor, ok := w.Actors[actorID]
			if !ok {
				return StampWarrantResult{}, fmt.Errorf("actor %q not found", actorID)
			}
			fresh := actor.WarrantedSince == nil
			tryStampWarrant(w, actor, meta, now)
			return StampWarrantResult{Stamped: fresh}, nil
		},
	}
}

// CompleteReactorTickResult is what CompleteReactorTick returns. Stale is
// true when the completion's AttemptID didn't match the actor's current
// TickAttemptID — the completion is for an attempt the world has already
// moved past (typed out, superseded). Stale completions are a no-op.
type CompleteReactorTickResult struct {
	Stale bool
}

// TickTerminalStatus enumerates how a reactor tick attempt ended. PR 3a
// defines it because CompleteReactorTick's terminal-status warrant policy
// is the consumer; PR 3's harness is the producer. The policy switch has
// a default branch, so adding a status later is safe — but the set below
// covers every outcome PR 3's harness produces.
type TickTerminalStatus int

const (
	// TickStatusUnknown is the zero value — an unset status, i.e. the PR 2
	// placeholder TickResult{}. CompleteReactorTick treats it as a minimal
	// completion: clear the attempt, move nothing to recently-consumed,
	// carry nothing forward.
	TickStatusUnknown TickTerminalStatus = iota
	// TickStatusSuccess — the turn completed normally.
	TickStatusSuccess
	// TickStatusDone — the turn ended via the terminal `done` tool.
	TickStatusDone
	// TickStatusBudgetForced — the turn hit the iteration budget and was
	// force-terminated; its rendered inputs were still addressed.
	TickStatusBudgetForced
	// TickStatusFailedBeforeRender — an LLM / render / infra failure before
	// the actor could perceive the stimulus. Nothing was addressed.
	TickStatusFailedBeforeRender
	// TickStatusFailedAfterRender — a failure after the prompt rendered but
	// before clean completion. Rendered inputs count as addressed; the rest
	// carry forward.
	TickStatusFailedAfterRender
	// TickStatusStale — the attempt was superseded. CompleteReactorTick
	// detects this from the AttemptID mismatch and returns before the
	// policy runs, so this value is informational only.
	TickStatusStale
	// TickStatusShutdown — the world is shutting down. Treated like a
	// before-render failure for warrant purposes: nothing addressed, the
	// consumed batch carries forward (when the world persists).
	TickStatusShutdown
	// TickStatusSkipped — the harness's noop-skip preflight determined
	// the rendered perception had nothing actionable (no co-present peer,
	// no need at red, and the consumed batch carried only low-information
	// warrant kinds). The LLM was not called; the consumed warrants are
	// treated as addressed so they don't re-fire in a loop. Replaces v1's
	// salem-vendor-only `agent_tick.go` skip at lines 211-221.
	TickStatusSkipped
)

// StaleStage names where in the tick lifecycle a stale attempt was
// detected. Diagnostic only — CompleteReactorTick reads only
// TerminalStatus and UnaddressedWarrants. Telemetry consumers (and
// debug tooling) read StaleStage to localize the timeout/supersede.
type StaleStage int

const (
	// StaleStageNone — the tick did not go stale.
	StaleStageNone StaleStage = iota

	// StaleStageBeforeRender — preflight stale-check fired before the
	// harness built perception (snapshot's TickAttemptID no longer
	// matched the job's). Cheapest detection; the LLM was not called.
	StaleStageBeforeRender

	// StaleStageAtTool — a commit-class tool dispatch returned
	// sim.ErrTickAttemptStale (the in-flight attempt was superseded
	// between the LLM response and the world-goroutine guard).
	StaleStageAtTool

	// StaleStageAtComplete — CompleteReactorTick itself returned
	// Stale=true (the attempt was superseded between the harness
	// finishing and the completion landing on the world goroutine).
	// The harness can't observe this from inside RunTick; the worker
	// (which calls CompleteReactorTick) does, and may overwrite the
	// staged StaleStage on the telemetry record.
	StaleStageAtComplete
)

// String renders the stage as a stable lowercase label.
func (s StaleStage) String() string {
	switch s {
	case StaleStageNone:
		return "none"
	case StaleStageBeforeRender:
		return "before_render"
	case StaleStageAtTool:
		return "at_tool"
	case StaleStageAtComplete:
		return "at_complete"
	default:
		return "unknown"
	}
}

// TickResult is the outcome of an LLM tick, handed to CompleteReactorTick.
// PR 2 shipped it as an empty placeholder; PR 3a added the two fields its
// warrant-lifecycle behavior reads — TerminalStatus and UnaddressedWarrants;
// PR 3d adds the diagnostic fields the harness populates and telemetry
// consumes. PR 2 / PR 3a callers passing TickResult{} still work — every
// added field has a meaningful zero value (TickStatusUnknown, etc.).
//
// CompleteReactorTick reads only TerminalStatus and UnaddressedWarrants;
// the other fields are for telemetry (TickTelemetrySink) and debug.
type TickResult struct {
	// AttemptID is the tick attempt this result describes — matches the
	// job's attemptID. Diagnostic; CompleteReactorTick takes attemptID
	// as a separate argument and uses that for the stale check.
	AttemptID TickAttemptID

	// ActorID is the actor whose tick this result describes — matches
	// the job's actorID. Diagnostic.
	ActorID ActorID

	// TerminalStatus is how the tick ended — it selects the terminal-status
	// warrant policy in CompleteReactorTick (see applyTerminalWarrantPolicy).
	TerminalStatus TickTerminalStatus

	// IterationCount is the number of LLM Complete calls the harness
	// made for this tick (1-based: 0 means the harness short-circuited
	// before reaching the iteration loop). Counts ALL iterations,
	// including the final one that ended the tick.
	IterationCount int

	// ToolsRequested is the ordered list of tool names the model
	// dispatched across all iterations (including post-cap-truncated
	// calls and validation failures). Useful for diagnosing prompt
	// regressions and runaway-call patterns.
	ToolsRequested []string

	// ToolsSucceeded is the subset of ToolsRequested whose handler ran
	// without error AND (for commits) whose world-goroutine command
	// returned no error. A successful commit + a tool that returned an
	// error are NOT bundled; the failure goes to ToolsFailedRejected.
	ToolsSucceeded []string

	// ToolsFailedRejected is the subset of ToolsRequested rejected by
	// the validator (unknown / disabled / oversize / malformed args),
	// rejected by the multi-call cap (excess_calls_truncated), or
	// failed at handler execution (handler error or command failure).
	ToolsFailedRejected []string

	// StaleStage names where staleness was detected, when it was.
	// StaleStageNone for non-stale completions; one of the other
	// stages for TerminalStatus == TickStatusStale.
	StaleStage StaleStage

	// BudgetHit is true iff the iteration budget was exhausted
	// (TerminalStatus == TickStatusBudgetForced).
	BudgetHit bool

	// LLMErrorClass is the llm.ErrorClass label that ended the tick on
	// an LLM failure path, empty for non-LLM-error exits. The harness
	// uses llm.Classify to populate.
	LLMErrorClass string

	// Duration is the wall-clock time spent inside RunTick (preflight
	// through final return). Telemetry consumes it directly.
	Duration time.Duration

	// UnaddressedWarrants are warrants the turn consumed but could not
	// address — dropped by a prompt length/size cap, never rendered, or
	// (for a before-render failure) the entire consumed batch. PR 3's
	// harness collects them; CompleteReactorTick re-opens them directly so
	// they fire again, and excludes their source keys from recently-
	// consumed suppression.
	UnaddressedWarrants []WarrantMeta
}

// CompleteReactorTick returns a Command that records the completion of an
// in-flight reactor tick. The command:
//
//   - Returns Stale=true with no mutation unless the actor is genuinely
//     mid-tick under THIS exact attempt — TickInFlight set, attemptID
//     non-empty, and matching the actor's current TickAttemptID. This
//     catches a timed-out attempt-1 completing AFTER attempt-2 has started
//     (attempt-1 must not clear attempt-2's in-flight flag) AND a stray
//     completion against an idle actor: the zero value of TickAttemptID is
//     "", so without the TickInFlight / non-empty guards a
//     CompleteReactorTick(id, "", ...) would match an idle actor and
//     wrongly run the warrant policy. A stale completion touches nothing.
//
//   - On a matching attempt: applies the terminal-status warrant policy
//     (see applyTerminalWarrantPolicy) — carry-forward of unaddressed
//     warrants and the move of addressed source keys into the recently-
//     consumed dedup set — then clears TickInFlight, TickAttemptID, and
//     inFlightSourceKeys. It does NOT clear a fresh warrant cycle stamped
//     while the LLM call was pending: WarrantedSince / WarrantDueAt /
//     Warrants for a NEW source survive completion to fire on the next
//     evaluator pass.
//
// now is the wall-clock completion time, used for the recently-consumed
// TTL stamp and for any carry-forward warrant cycle's WarrantDueAt jitter.
//
// Result handling beyond the warrant lifecycle (applying tool calls — they
// self-apply as their own guarded commands) is PR 3's responsibility; the
// TickResult fields PR 3a reads are TerminalStatus and UnaddressedWarrants.
func CompleteReactorTick(actorID ActorID, attemptID TickAttemptID, result TickResult, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return CompleteReactorTickResult{}, fmt.Errorf("actor %q not found", actorID)
			}
			// Stale unless the actor is genuinely mid-tick under this exact
			// attempt. The TickInFlight + non-empty guards matter because
			// the zero value of TickAttemptID is also "" — without them a
			// stray CompleteReactorTick(id, "", ...) would match an idle
			// actor and wrongly run the warrant policy on it.
			if !actor.TickInFlight || attemptID == "" || actor.TickAttemptID != attemptID {
				return CompleteReactorTickResult{Stale: true}, nil
			}

			// Apply the terminal-status warrant policy BEFORE clearing the
			// in-flight markers — the policy reads inFlightSourceKeys.
			applyTerminalWarrantPolicy(w, actor, result, now)

			actor.TickInFlight = false
			actor.TickAttemptID = ""
			actor.inFlightSourceKeys = nil
			return CompleteReactorTickResult{Stale: false}, nil
		},
	}
}

// applyTerminalWarrantPolicy resolves a completing tick attempt's consumed
// source keys per the terminal-status policy table. Called by
// CompleteReactorTick on a matching (non-stale) attempt, before the
// in-flight markers are cleared.
//
// The attempt's consumed keys are in actor.inFlightSourceKeys.
// result.UnaddressedWarrants (populated by PR 3) names the warrants the
// turn could not address — dropped by a prompt cap, never rendered, or,
// for a before-render failure, the entire consumed batch. They are
// re-opened directly (reopenWarrants), never via tryStampWarrant: their
// source keys are still in the in-flight set and the addressed ones are
// about to land in recently-consumed, so a normal re-stamp would be
// dedup-rejected.
//
// "Addressed" keys = inFlightSourceKeys minus the carried-forward keys.
// Whether the addressed keys move into recently-consumed depends on the
// terminal status (terminalStatusAddresses):
//
//	success / done / budget-forced / failed-after-render — the turn
//	  perceived and addressed those inputs; addressed keys move into
//	  recently-consumed, suppressing a delayed duplicate for the TTL.
//	failed-before-render / shutdown — the actor never perceived the
//	  stimulus (or the world is going away): move nothing; PR 3 carries
//	  the whole consumed set forward via UnaddressedWarrants.
//	unknown (PR 2 placeholder TickResult{}) — minimal completion: with no
//	  carried-forward warrants and a non-addressing status, this re-opens
//	  nothing and moves nothing. The attempt is simply cleared.
func applyTerminalWarrantPolicy(w *World, actor *Actor, result TickResult, now time.Time) {
	// Carry-forward first — re-open the unaddressed warrants directly,
	// bypassing tryStampWarrant's dedup.
	reopenWarrants(w, actor, result.UnaddressedWarrants, now)

	if !terminalStatusAddresses(result.TerminalStatus) {
		return
	}

	// Addressed keys = consumed keys minus the carried-forward keys. Move
	// them into the recently-consumed dedup set.
	carried := make(map[WarrantSourceKey]struct{}, len(result.UnaddressedWarrants))
	for _, m := range result.UnaddressedWarrants {
		if m.eventSourced() {
			carried[m.sourceKey()] = struct{}{}
		}
	}
	for key := range actor.inFlightSourceKeys {
		if _, isCarried := carried[key]; isCarried {
			continue
		}
		rememberConsumedSourceKey(actor, key, now)
	}
}

// terminalStatusAddresses reports whether a terminal status means the turn
// actually perceived and addressed its inputs — i.e. whether its addressed
// source keys should move into the recently-consumed dedup set. The
// default branch (failed-before-render, shutdown, unknown, any future
// status) is the conservative "not addressed — move nothing".
//
// TickStatusSkipped addresses too: the noop-skip preflight read perception
// and concluded the batch wasn't worth an LLM call. The consumed keys must
// land in recently-consumed or the same warrants would re-emit on the next
// scan and re-skip — turning the gate into a busy-loop.
func terminalStatusAddresses(s TickTerminalStatus) bool {
	switch s {
	case TickStatusSuccess, TickStatusDone, TickStatusBudgetForced, TickStatusFailedAfterRender, TickStatusSkipped:
		return true
	default:
		return false
	}
}

// reopenWarrants re-opens warrants directly onto the actor, bypassing
// tryStampWarrant's dedup — used by carry-forward, where the warrants'
// source keys are still in the in-flight set (and the addressed ones are
// about to land in recently-consumed), so a normal re-stamp would be
// rejected. An existing open cycle's WarrantedSince / WarrantDueAt are
// preserved; a fresh cycle (now + jitter) starts when the actor has none.
func reopenWarrants(w *World, actor *Actor, metas []WarrantMeta, now time.Time) {
	if len(metas) == 0 {
		return
	}
	if actor.WarrantedSince == nil {
		t := now
		actor.WarrantedSince = &t
		due := now.Add(pickWarrantJitter(w.Settings, now))
		actor.WarrantDueAt = &due
	}
	for _, m := range metas {
		actor.Warrants = appendCappedWarrant(actor.Warrants, m, w.Settings.MaxWarrantsPerActor)
	}
}

// EvaluateReactors returns a Command that scans every actor for due
// warrants and emits ReactorTickDue events for those that pass the
// eligibility and rate gates. Called periodically by the evaluator
// AfterFunc chain (see reactor_evaluator.go); exported here so tests can
// drive the body synchronously without timing dependencies.
//
// For each due actor:
//
//  1. actorCanReactNow filters out asleep/concluded-huddle/etc. Stale
//     warrants (returns stale=true) are cleared inline; the warrant cycle
//     is dropped — the conversational context no longer applies.
//
//  2. MinReactorTickGap enforces a per-actor pacing floor; checkRateGate
//     enforces the optional per-minute cap (MaxReactorTicksPerActorPer-
//     Minute). An actor that trips either gets its WarrantDueAt pushed
//     rather than dropped — the warrant survives, just delayed. A Force
//     warrant bypasses both pacing gates.
//
//  3. TickAdmissionController.CanAdmit gates on downstream capacity
//     (Option A — admit before consume). A "no" pushes WarrantDueAt by
//     AdmissionBackoff, writes a `deferred` telemetry record, and emits
//     nothing — the warrants stay OPEN. Force does NOT bypass this:
//     admission is real capacity, not pacing.
//
//  4. Warrant is consumed at EMIT time (clearWarrant) — see reactor.go.
//     TickInFlight + TickAttemptID set, inFlightSourceKeys recorded from
//     the consumed warrants, RecentReactorTicks ring appended for the
//     rate-gate window count.
//
//  5. ReactorTickDue emitted with the consumed Warrants list.
//
// After the scan, the next AfterFunc evaluation is re-armed via
// armNextEvaluation. Idempotent re-arming: if a re-arm already happened
// during this command (shouldn't, since we're in the world goroutine
// throughout), the second one is a no-op.
func EvaluateReactors(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			rateCap := w.Settings.MaxReactorTicksPerActorPerMinute
			window := defaultRateWindow

			for _, actor := range w.Actors {
				if !actorReactorDue(actor, now) {
					continue
				}

				eligible, stale := actorCanReactNow(w, actor, now)
				if stale {
					clearWarrant(actor)
					continue
				}
				if !eligible {
					// Temporarily unavailable / shelved: asleep, on break with
					// no interrupting warrant, or suppressed just after engine
					// speech. A shelved actor never consumes its warrant cycle,
					// so a cycle aged past MaxWarrantAge is stale — evict it so
					// the actor wakes to current state rather than a transcript
					// of everything it slept through (ZBBS-WORK-361). Force
					// warrants (operator nudges) MUST survive shelving, so a
					// stale cycle is pruned down to its Force warrants rather
					// than kept whole — keeping the whole cycle just because one
					// warrant is forced would re-protect exactly the stale pile
					// we're dropping (code_review). The freshest signals re-stamp
					// a new cycle while the actor stays shelved.
					if warrantCycleStale(actor, now, w.Settings) {
						if hasForcedWarrant(actor.Warrants) {
							retainForcedWarrants(actor, now, w.Settings)
						} else {
							clearWarrant(actor)
						}
						continue
					}
					// Not yet stale — push WarrantDueAt out by a backoff so we
					// don't reconsider this actor on every 250ms scan.
					next := now.Add(unavailableBackoff)
					actor.WarrantDueAt = &next
					continue
				}

				// Per-actor minimum tick gap — an always-on pacing floor,
				// separate from the optional per-minute rate cap below. A
				// warrant coming due inside the gap has its WarrantDueAt
				// pushed to the gap boundary. Force bypasses it (same as
				// the rate gate): an admin / emergency tick must fire
				// regardless of pacing.
				if !hasForcedWarrant(actor.Warrants) {
					gap := w.Settings.MinReactorTickGap
					if gap <= 0 {
						gap = defaultMinReactorTickGap
					}
					if last, ok := lastReactorTickAt(actor); ok && now.Sub(last) < gap {
						next := last.Add(gap)
						actor.WarrantDueAt = &next
						continue
					}
				}

				// Rate-gate check. Capped actors get their fire delayed to
				// the next-allowed boundary (the cap'th-oldest entry in
				// the window expires at that time). The cap is a
				// settings-driven gross gate — no $ math.
				//
				// Force-bypass: any pending warrant with Force=true skips
				// the rate gate. Used by admin overrides and emergency
				// reasons that must fire even when an actor is loud.
				if rateCap > 0 && !hasForcedWarrant(actor.Warrants) &&
					!checkRateGate(actor, now, rateCap, window) {
					next := nextRateAllowedAt(actor, now, rateCap, window)
					actor.WarrantDueAt = &next
					continue
				}

				// Tick admission control (Option A — admit before consume).
				// If downstream capacity is unavailable, push the warrant
				// out by AdmissionBackoff and emit nothing — the warrants
				// stay OPEN, so no signal is lost. Force does NOT bypass
				// this: admission is real downstream capacity, not pacing;
				// emitting into a full pool would drop the job. A `deferred`
				// telemetry record is written so the deferral is visible.
				if !w.tickAdmission.CanAdmit() {
					backoff := w.Settings.AdmissionBackoff
					if backoff <= 0 {
						backoff = defaultAdmissionBackoff
					}
					next := now.Add(backoff)
					actor.WarrantDueAt = &next
					if w.repo.TickTelemetry != nil {
						w.repo.TickTelemetry.WriteTickTelemetry(TickTelemetryRecord{
							At:      now,
							ActorID: actor.ID,
							Kind:    "deferred",
							Detail:  map[string]string{"gate": "admission"},
						})
					}
					continue
				}

				// Snapshot the warrant cycle metadata BEFORE clearing.
				warrantsCopy := append([]WarrantMeta(nil), actor.Warrants...)
				warrantedSince := *actor.WarrantedSince
				dueAt := *actor.WarrantDueAt

				// ZBBS-HOME-329 #3/#4: a red-need or operator-force warrant is the
				// only thing that lets an on-break actor reach this emit point
				// (actorCanReactNow shelves resters otherwise; sleepers never reach
				// here at all). We're committing to its tick, so end the break now —
				// the actor leaves rest to act instead of deliberating while still
				// flagged StateResting, which would re-shelve it on the next scan.
				if actor.State == StateResting || (actor.BreakUntil != nil && actor.BreakUntil.After(now)) {
					endBreak(w, actor)
				}

				clearWarrant(actor)
				actor.TickInFlight = true
				actor.TickAttemptID = newTickAttemptID()
				// Record which source events this attempt consumed — the
				// in-flight dedup path reads this set, and CompleteReactorTick
				// resolves it under the terminal-status policy.
				actor.inFlightSourceKeys = sourceKeySet(warrantsCopy)
				recordReactorTick(actor, now, rateCap)
				// ZBBS-HOME-329 (#6): stamp the human-facing "last ticked"
				// marker at the single tick-emit chokepoint, so the umbilical /
				// admin last_agent_tick_at reflects reality instead of freezing
				// at the value loaded from the last checkpoint. Distinct from
				// RecentReactorTicks (the rate-gate ring) — this is the durable
				// column, persisted on the next checkpoint. Fresh per-iteration
				// copy avoids aliasing the loop-shared `now` across actors.
				tickedAt := now
				actor.LastTickedAt = &tickedAt

				w.emit(&ReactorTickDue{
					ActorID:        actor.ID,
					AttemptID:      actor.TickAttemptID,
					Warrants:       warrantsCopy,
					WarrantedSince: warrantedSince,
					DueAt:          dueAt,
					EmittedAt:      now,
				})
			}

			armNextEvaluation(w)
			return nil, nil
		},
	}
}

// nextRateAllowedAt computes the earliest time at which the actor will
// be below the per-minute cap. The expiring entry that matters is the
// (len(inWindow) - cap)th — the one that, when it drops out of the
// window, brings the in-window count down to cap-1. Adds a small jitter
// so co-capped actors don't all clear simultaneously.
//
// When len(inWindow) < cap the cap isn't actually breached (caller
// shouldn't have invoked this) — returns now to avoid pushing the fire
// out of the present.
func nextRateAllowedAt(a *Actor, now time.Time, cap int, window time.Duration) time.Time {
	if a.RecentReactorTicks == nil {
		return now
	}
	ticks := a.RecentReactorTicks.Snapshot()
	// Drop ticks already outside the window — they don't count toward the cap.
	cutoff := now.Add(-window)
	inWindow := ticks[:0]
	for _, t := range ticks {
		if t.After(cutoff) {
			inWindow = append(inWindow, t)
		}
	}
	if cap <= 0 || len(inWindow) < cap {
		return now
	}
	idx := len(inWindow) - cap
	return inWindow[idx].Add(window).Add(rateBackoffJitter())
}

// hasNeedWarrant reports whether any meta in the list is a need-threshold
// warrant — a red-tier need pressing the actor to act. ZBBS-HOME-329 #3 uses it
// to let a critical need interrupt a scheduled break (never sleep).
func hasNeedWarrant(list []WarrantMeta) bool {
	for _, m := range list {
		if m.Reason != nil && m.Reason.Kind() == WarrantKindNeedThreshold {
			return true
		}
	}
	return false
}

// hasOperatorNudgeWarrant reports whether any meta is an operator-injected
// nudge — a bare admin force-tick (WarrantKindAdmin) or a directive impulse
// (WarrantKindImpulse), both stamped only by the umbilical /nudge route.
// ZBBS-HOME-329 #4 uses it to let an operator interrupt a scheduled break
// (never sleep). Deliberately narrower than hasForcedWarrant: matching the
// nudge KINDS rather than the broad Force flag keeps a future non-operator
// forced warrant from silently gaining the power to wake a rester.
func hasOperatorNudgeWarrant(list []WarrantMeta) bool {
	for _, m := range list {
		if m.Reason == nil {
			continue
		}
		if k := m.Reason.Kind(); k == WarrantKindAdmin || k == WarrantKindImpulse {
			return true
		}
	}
	return false
}

// hasForcedWarrant returns true if any meta in the list has Force=true.
// Linear scan; the list is bounded by Settings.MaxWarrantsPerActor
// (default 16) so this is cheap.
func hasForcedWarrant(list []WarrantMeta) bool {
	for _, m := range list {
		if m.Force {
			return true
		}
	}
	return false
}

// rateBackoffJitter returns a small randomized offset (50-250ms) used to
// stagger rate-cap clearings so several co-capped actors don't all re-
// fire on the same scan cycle.
func rateBackoffJitter() time.Duration {
	return 50*time.Millisecond + time.Duration(mathrand.Int64N(int64(200*time.Millisecond)))
}

// unavailableBackoff is the delay applied when actorCanReactNow returns
// eligible=false (but not stale). A short backoff lets the evaluator
// recheck soon without burning every 250ms scan cycle on the same actor.
const unavailableBackoff = 2 * time.Second
