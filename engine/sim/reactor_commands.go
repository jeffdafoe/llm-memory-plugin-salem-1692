package sim

import (
	"fmt"
	mathrand "math/rand/v2"
	"strings"
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

	// StaleStageSnapshotLag — NOT a genuine supersession. The preflight
	// freshness wait expired with the newest published snapshot still
	// predating this attempt's dispatch (AtTick <= dispatchTick), so the
	// snapshot could not witness a stale/superseded verdict either way — a
	// snapshot older than our own dispatch reflects nothing at or after it
	// (LLM-275). The consumed batch carries forward for a clean retry;
	// telemetry consumers MUST treat this separately from the real-stale
	// stages above (it means "world goroutine lagging, re-attempt", not
	// "the actor moved on"). The LLM was not called.
	StaleStageSnapshotLag
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
	case StaleStageSnapshotLag:
		return "snapshot_lag"
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

	// Degeneracy observer yield facts (LLM-94). The harness derives these
	// from this tick's perception so CompleteReactorTick can score the tick's
	// yield without re-perceiving. All have a meaningful zero value, so older
	// callers passing TickResult{} are unaffected.
	//
	//   - BaselinePresent: a scene baseline resolved this tick
	//     (Payload.Baseline == BaselinePresent). The observer only treats a
	//     tick as "no change" when this is true — a missing baseline is
	//     inconclusive, never evidence of a stuck loop.
	//   - StateChanged: the loop-detection diff (Payload.Primary.Diff.AnyChange)
	//     — any change since the actor arrived. Meaningful only when
	//     BaselinePresent.
	//   - HadAudience: any awake, addressable peer was co-present (a huddle
	//     peer or a co-present conversational actor).
	BaselinePresent bool
	StateChanged    bool
	HadAudience     bool

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

	// PreflightWait is how long RunTick spent waiting for the published
	// snapshot to catch up to this attempt's dispatch (the freshness wait —
	// see the harness preflight and LLM-275). Zero when the first read was
	// already fresh. Telemetry surfaces it so the wait ceiling can be tuned:
	// a StaleStageSnapshotLag whose PreflightWait sits at the ceiling means
	// the ceiling is too low for the world goroutine's lag under load.
	PreflightWait time.Duration

	// UnaddressedWarrants are warrants the turn consumed but could not
	// address — dropped by a prompt length/size cap, never rendered, or
	// (for a before-render failure) the entire consumed batch. PR 3's
	// harness collects them; CompleteReactorTick re-opens them directly so
	// they fire again, and excludes their source keys from recently-
	// consumed suppression.
	UnaddressedWarrants []WarrantMeta

	// SkippedIntentTools names the commit-class tool calls the model queued
	// AFTER a terminal call in the same batch — dropped by the harness's
	// post-terminal skip, i.e. the actor's own declared, unfinished intent
	// (LLM-414). CompleteReactorTick stamps an unfinished_intent warrant so
	// the actor re-ticks promptly and can finish the deed. The harness
	// leaves this empty on a tick that was ITSELF triggered by an
	// unfinished_intent warrant (one retry, never a storm) and never
	// includes `speak` or terminal-class `done` (re-ticking to re-say is the
	// LLM-184 storm this must not reintroduce).
	SkippedIntentTools []string
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

			// Degeneracy observer (LLM-94): fold this completion's yield into
			// the actor's futility streak. No-op unless explicitly enabled.
			updateDegeneracy(w, actor, result, now)

			// ZBBS-HOME-413: a completed tick that leaves the actor as the SOLE
			// member of its huddle is the moment to dissolve that dead huddle —
			// post-WORK-367 the lone member never ticks itself out (its
			// HuddlePeerLeft is low-info), so left alone it stays stranded,
			// rendering a departed peer in its perception (the live
			// Elizabeth-Ellis case). Originally scoped to TickStatusSkipped
			// ("a skip means confirmed won't act"), but a lone member whose
			// ticks are driven by a skip-bypassing warrant (WarrantKindRestock)
			// never skips, and sat in a zombie huddle for 40 minutes answering
			// done() (the live John-Ellis case, hud-db18626e). The signal is
			// not skip-vs-ran — no action a lone member can take repopulates
			// its huddle (any peer joining would have made it non-solo by
			// completion time), so ANY addressing completion that ends with the
			// actor still alone confirms the conversation is over. Non-addressing
			// statuses (failed-before-render, shutdown) are excluded: the actor
			// never perceived the turn, or the world is going away. The actor
			// re-huddles for free on the next co-located speak, and LLM-170's
			// per-structure carry-over preserves the conversation for a
			// returning peer.
			if terminalStatusAddresses(result.TerminalStatus) {
				dissolveSoloHuddleAfterTick(w, actor, now)
			}

			// LLM-414: the model queued commit calls after its terminal one —
			// its own unfinished intent. Stamp a prompt re-tick (normal
			// jitter) so the second beat lands in seconds, not on whatever
			// unrelated wake comes minutes later. Stamped AFTER the in-flight
			// markers clear so it opens a fresh cycle. Only on addressing
			// completions: a failed/shutdown turn never really made the
			// choice this warrant exists to honor.
			if len(result.SkippedIntentTools) > 0 && terminalStatusAddresses(result.TerminalStatus) {
				tryStampWarrant(w, actor, WarrantMeta{
					TriggerActorID: actorID,
					Reason: UnfinishedIntentWarrantReason{
						Tools: strings.Join(result.SkippedIntentTools, ", "),
					},
				}, now)
			}
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

// dissolveSoloHuddleAfterTick leaves+concludes the actor's current huddle when
// the actor is its only remaining member (ZBBS-HOME-413). Called from
// CompleteReactorTick after any addressing terminal completion — see that
// callsite for the why (originally skip-only; widened because a skip-bypassing
// warrant keeps a lone member's ticks real forever).
//
// No-op unless the actor is the SOLE member: a completion with other members
// still in the huddle means the conversation may be live for them (or, for a
// skip, that peers drifted out of co-presence without leaving the membership
// set — a broader stale-huddle desync, boot-collapse Finding 6), which is
// deliberately out of scope here — dissolving a still-populated huddle would
// strand the OTHER members. A stale back-ref (huddle missing from w.Huddles) is
// also left untouched; leaveCurrentHuddle's own stale-ref path owns that.
//
// The membership re-check matters: len(Members)==1 alone only proves the huddle
// has one member, not that it's THIS actor. If actor.CurrentHuddleID is a stale
// back-ref to a huddle whose lone member is someone else, calling
// leaveCurrentHuddle(actor) would stamp a spurious HuddlePeerLeft on that
// bystander and could conclude their huddle. So we confirm the actor is actually
// in the membership set before leaving (code_review).
//
// When the actor is genuinely the lone member, leaveCurrentHuddle removes it,
// finds the huddle empty, and concludes it (emitting HuddleLeft + HuddleConcluded
// and detaching it from any scenes) — the same teardown the normal last-leaver
// path runs.
func dissolveSoloHuddleAfterTick(w *World, actor *Actor, now time.Time) {
	if actor.CurrentHuddleID == "" {
		return
	}
	huddle, ok := w.Huddles[actor.CurrentHuddleID]
	if !ok || len(huddle.Members) != 1 {
		return
	}
	if _, isMember := huddle.Members[actor.ID]; !isMember {
		return
	}
	leaveCurrentHuddle(w, actor, now)
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
			// Eco mode (LLM-313): computed once per scan — O(actors) over the
			// PC presence stamps, trivial at village scale. True means "master
			// switch on AND no fresh player presence"; the per-cycle gap below
			// decides whether a given actor is actually paced.
			ecoEngaged := ecoModeEngaged(w, now)

			for _, actor := range w.Actors {
				if !actorReactorDue(actor, now) {
					continue
				}

				// Agent-less kinds never tick (ZBBS-HOME-428). The stamping
				// funnel refuses them, so a due cycle here can only mean
				// direct field mutation (tests, future code) or pre-fix
				// state carried in memory — clear it rather than skip, or
				// the dead cycle would re-enter this scan forever. This is
				// the structural backstop for the reopenWarrants path, which
				// bypasses the funnel by design.
				if actor.Kind != KindNPCStateful && actor.Kind != KindNPCShared {
					clearWarrant(actor)
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

				// Eco pacing floor (LLM-313). While unwatched, a cycle whose
				// every warrant sits in a throttled bucket (ecoCycleGap > 0 —
				// any survival/duty/commerce warrant in the pile returns 0 and
				// exempts the whole cycle) waits out a wider per-actor gap
				// before emitting. Same push-WarrantDueAt idiom as the min-gap
				// above: the warrants survive, just delayed. The salient
				// re-arm in tryStampWarrant may pull a pushed due time back in
				// when a fresh warrant appends — that's fine: the pulled cycle
				// re-enters this gate on the next scan, and either the fresh
				// kind re-classified it to full speed (gap 0, emits) or it's
				// still all-throttled and re-pushes. The gate is the
				// enforcement point; the due time is just scheduling. Force
				// bypasses, like every pacing gate. An actor with no tick
				// history (fresh boot) emits immediately — eco is a rate
				// bound, not added latency for a quiet actor. When the
				// audience returns, ecoEngaged reads false on the very next
				// scan and parked cycles were only ever pushed to
				// last-tick+gap, so the tableau resumes within seconds.
				if ecoEngaged && !hasForcedWarrant(actor.Warrants) {
					if ecoGap := ecoCycleGapClamped(actor.Warrants, w.Settings); ecoGap > 0 {
						if last, ok := lastReactorTickAt(actor); ok && now.Sub(last) < ecoGap {
							next := last.Add(ecoGap)
							actor.WarrantDueAt = &next
							continue
						}
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

				// Per-agent rate gate (LLM-156). The per-actor gate above bounds
				// ONE NPC; this bounds the SHARED VA slug backing many NPCs
				// (salem-vendor). memory-api's rate limit is keyed per agent-name,
				// so without pacing the pool's aggregate ticks burst past the cap
				// and drop the whole pool into a silent cooldown. The engine paces
				// per-agent emission to stay under that cap.
				//
				// admitAgentRateFair (LLM-258) allocates the slug's limited slots by
				// starvation-age fairness instead of first-eligible-wins: the cap is
				// unchanged, but a reserved tail is held for actors starved past a
				// threshold so chatty NPCs can't consume the whole budget every
				// window and freeze quiet on-shift producers. See its doc for the
				// three bands (general / reserved / hard-ceiling bypass) and the
				// accepted one-tick overage the ceiling can cause.
				//
				// Force bypasses, like the per-actor gate: an admin nudge or a
				// red-need emergency must fire even when the slug is saturated by
				// decorative chatter. Forced ticks are still RECORDED at emit
				// (recordAgentTick below), so they consume the agent's budget and
				// the gate paces the remaining NON-forced ticks accordingly.
				//
				// Bounded-wait-then-shed: a cycle that can't win an agent slot before
				// it ages out (warrantCycleStale) is dropped rather than deferred
				// forever — the conversational moment is gone, and unbounded deferral
				// on a saturated shared slug would pile up. The shed is safe under
				// fairness because starvation age keys on the last SERVED tick, which
				// survives the shed/re-stamp churn, so a re-warranting producer keeps
				// climbing toward the reserved band and the ceiling.
				if !hasForcedWarrant(actor.Warrants) &&
					!admitAgentRateFair(w, actor, now) {
					shed := warrantCycleStale(actor, now, w.Settings)
					if shed {
						clearWarrant(actor)
					} else {
						// Floor re-examination at unavailableBackoff: a reserved-band
						// gate leaves the bucket UNDER cap, where nextAgentRateAllowedAt
						// returns now — without the floor the loser of a fairness race
						// would be re-scanned (and re-emit a deferred record) every
						// 250ms until its warrant sheds. An at-cap defer is already
						// further out and dominates the floor.
						next := nextAgentRateAllowedAt(w, actor.LLMAgent, now)
						if soon := now.Add(unavailableBackoff); next.Before(soon) {
							next = soon
						}
						actor.WarrantDueAt = &next
					}
					if w.repo.TickTelemetry != nil {
						detail := map[string]string{"gate": "agent_rate"}
						if shed {
							detail["outcome"] = "shed"
						}
						// Record how long the actor has starved so the fairness gate
						// is observable in the telemetry stream / umbilical. Clamp at 0
						// so a clock-skewed / future last-tick can't emit a negative age.
						if served, age := servedStarvationAge(actor, now); served {
							if age < 0 {
								age = 0
							}
							detail["starved_ms"] = fmt.Sprintf("%d", age.Milliseconds())
						} else {
							detail["starved_ms"] = "never"
						}
						w.repo.TickTelemetry.WriteTickTelemetry(TickTelemetryRecord{
							At:      now,
							ActorID: actor.ID,
							Kind:    "deferred",
							Detail:  detail,
						})
					}
					continue
				}

				// Degeneracy Stage-2 throttle (LLM-94). A throttled actor — one
				// the observer has watched burn an obviously-futile tick every
				// cadence cycle past the Stage-2 threshold — has its AMBIENT-only
				// wake cycles pushed out by the backoff, so the engine stops
				// paying to wake it every minute for nothing. A cycle carrying any
				// SALIENT warrant (speech, an economic event, a need threshold, an
				// operator nudge) is never throttled — the throttle slows the
				// engine's own poking, never a real interaction. Gated on
				// degeneracyEnabled so that turning the observer OFF lifts the
				// throttle immediately (otherwise a throttled actor would never
				// tick, never reach updateDegeneracy, and stay deferred forever);
				// Force bypasses it like the other pacing gates. Self-recovering:
				// a productive tick clears DegenStage, so the throttle simply
				// stops applying — no separate un-throttle step. A `deferred`
				// record (gate=degeneracy) makes each suppression visible, the
				// same posture as the admission deferral below.
				if w.Settings.degeneracyEnabled() &&
					actor.DegenStage >= DegeneracyThrottled &&
					!hasForcedWarrant(actor.Warrants) &&
					warrantCycleAllAmbient(actor.Warrants) {
					next := now.Add(w.Settings.degeneracyThrottleBackoff())
					actor.WarrantDueAt = &next
					if w.repo.TickTelemetry != nil {
						w.repo.TickTelemetry.WriteTickTelemetry(TickTelemetryRecord{
							At:      now,
							ActorID: actor.ID,
							Kind:    "deferred",
							Detail:  map[string]string{"gate": "degeneracy"},
						})
					}
					continue
				}

				// Staleness decay (LLM-233). An all-ambient cycle whose every
				// warrant kind has ALREADY been ticked under the actor's
				// current situation fingerprint is a repeat wake against an
				// unchanged world — defer it to the kind's decayed re-wake
				// time (base·2^streak, capped) instead of paying full producer
				// rate. Deterministic, not a heuristic: the fingerprint
				// (stale_wake.go) hashes location, state, purse/inventory,
				// huddle membership, and the newest non-self utterance, so ANY
				// real development passes at full rate, as does a kind with no
				// ledger entry yet (the day's first shift_duty) or any salient
				// / forced warrant (same bypass posture as the degeneracy
				// throttle above — this gate only ever slows the engine's own
				// re-poking of a world that hasn't moved). Unlike the throttle
				// it needs no futility diagnosis, so it also bounds loops the
				// observer scores as productive (the John Ellis restock case:
				// 256 wakes in 2h at a shelved, unresponsive audience).
				if w.Settings.staleWakeDecayEnabled() &&
					!hasForcedWarrant(actor.Warrants) &&
					warrantCycleAllAmbient(actor.Warrants) {
					fp := actorSituationFingerprint(w, actor, now)
					if next, stale := staleWakeDeferUntil(w.Settings, actor, fp, now); stale {
						actor.WarrantDueAt = &next
						if w.repo.TickTelemetry != nil {
							w.repo.TickTelemetry.WriteTickTelemetry(TickTelemetryRecord{
								At:      now,
								ActorID: actor.ID,
								Kind:    "deferred",
								Detail:  map[string]string{"gate": "stale_wake"},
							})
						}
						continue
					}
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

				// ZBBS-HOME-329 #3/#4: an operator-force or PC-speech warrant is the
				// only thing that now lets an on-break actor reach this emit point
				// (actorCanReactNow shelves resters otherwise; sleepers never reach
				// here at all). A red need NO LONGER does — LLM-211 made
				// actorActionableRedNeed suppress warrants for a rester (like a
				// sleeper), so the reactor stops ending a break to service the actor's
				// own recovering need (the take_break churn). We're committing to this
				// tick, so end the break now — the actor leaves rest to act instead of
				// deliberating while still flagged StateResting, which would re-shelve
				// it on the next scan.
				if actor.State == StateResting || (actor.BreakUntil != nil && actor.BreakUntil.After(now)) {
					endBreak(w, actor)
				}

				// Staleness-decay ledger advance (LLM-233): recorded HERE, at
				// the emit commitment, never on a deferral — a streak counts
				// real LLM calls. Only unforced all-ambient cycles are
				// recorded: a salient cycle doesn't advance ambient decay
				// (its kinds aren't in the ledger's scope), and a FORCED
				// ambient tick must not touch the ledger either — Force
				// bypasses the gate entirely, so an operator nudge can
				// neither extend a deferral (LastEmitAt push) nor deepen a
				// streak (code_review).
				if w.Settings.staleWakeDecayEnabled() &&
					!hasForcedWarrant(actor.Warrants) &&
					warrantCycleAllAmbient(actor.Warrants) {
					recordStaleWake(actor, actor.Warrants, actorSituationFingerprint(w, actor, now), now)
				}

				clearWarrant(actor)
				actor.TickInFlight = true
				actor.TickAttemptID = newTickAttemptID()
				// Record which source events this attempt consumed — the
				// in-flight dedup path reads this set, and CompleteReactorTick
				// resolves it under the terminal-status policy.
				actor.inFlightSourceKeys = sourceKeySet(warrantsCopy)
				recordReactorTick(actor, now, rateCap)
				// LLM-156: count this tick toward the shared-VA's per-agent window
				// too, so the slug's aggregate pacing sees it. No-op for an
				// ungated slug.
				recordAgentTick(w, actor.LLMAgent, now)
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

// hasBreakInterruptingNeedWarrant reports whether any meta is a need-threshold
// warrant for a need that a BREAK does not itself resolve — i.e. any red need
// other than tiredness. ZBBS-HOME-329 #3 lets a critical need cut a scheduled
// break short (never sleep), so a resting-but-starving actor wakes to eat — but
// a break RECOVERS tiredness (the tiredness sweep credits while BreakUntil is
// open), so a red-TIREDNESS warrant must NOT interrupt one: doing so cancels the
// very rest that is curing the need. That was the on-shift exhaustion loop in
// LLM-62 — the need producers only stamp a tiredness warrant while ON shift
// (actorActionableRedNeed, needs_tick.go), so every break an on-shift tired
// vendor took was immediately cut, leaving it stuck red and its post unmanned.
// Hunger/thirst are not eased by resting, so those still interrupt. Kind-aware
// counterpart of the other has*Warrant predicates; the warrant's Need field
// (NeedThresholdWarrantReason) is what makes the carve-out possible.
func hasBreakInterruptingNeedWarrant(list []WarrantMeta) bool {
	for _, m := range list {
		if m.Reason == nil || m.Reason.Kind() != WarrantKindNeedThreshold {
			continue
		}
		r, ok := m.Reason.(NeedThresholdWarrantReason)
		if !ok {
			// Kind claims need-threshold but the concrete type doesn't match —
			// only NeedThresholdWarrantReason returns that kind, so this is
			// unreachable; treat an unexpected shape as interrupting (the prior
			// behavior) rather than silently swallowing it.
			return true
		}
		if r.Need != "tiredness" {
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

// hasPCSpeechWarrant reports whether any meta is a PC-speech warrant — a player
// character speaking into this actor's huddle (this actor is a recipient of the
// player's utterance, not necessarily a parsed vocative addressee; ZBBS-HOME-377).
// actorCanReactNow uses it to let a player's in-person address interrupt a
// scheduled break, the same posture as a red-tier need (never sleep). A player
// talking to your group outranks your nap; an NPC's chatter does not — NPC-speech
// warrants stay gated behind the break so the village's own conversations can't
// yank a rester out.
func hasPCSpeechWarrant(list []WarrantMeta) bool {
	for _, m := range list {
		if m.Reason != nil && m.Reason.Kind() == WarrantKindPCSpoke {
			return true
		}
	}
	return false
}

// hasNPCSpeechWarrant reports whether any meta is an NPC-to-NPC directed-speech
// warrant (WarrantKindNPCSpoke). The laboring carve-out (LLM-230) uses it to let
// a mid-job worker reply to a peer on a cadence — deliberately NOT reused by the
// break / source-activity gates, which stay closed to NPC chatter (only the
// PC-speech kind cuts a rester/eater short; the village's own conversations must
// not yank one out). Mirrors hasPCSpeechWarrant.
func hasNPCSpeechWarrant(list []WarrantMeta) bool {
	for _, m := range list {
		if m.Reason != nil && m.Reason.Kind() == WarrantKindNPCSpoke {
			return true
		}
	}
	return false
}

// hasHiredRepairWarrant reports whether any meta is the hired-worker repair
// warrant (WarrantKindStallRepairHired, LLM-271). The laboring shelve-gate uses it
// to let a worker who just started a job at their employer's already-worn business
// draw one tick to mend it — a StateLaboring worker is otherwise shelved. Scoped to
// the hired kind, NOT the owner's WarrantKindStallRepair: an owner mending their own
// stall is never laboring for someone else, so only the hired warrant should pierce
// this gate. Mirrors hasPCSpeechWarrant.
func hasHiredRepairWarrant(list []WarrantMeta) bool {
	for _, m := range list {
		if m.Reason != nil && m.Reason.Kind() == WarrantKindStallRepairHired {
			return true
		}
	}
	return false
}

// hasReturnToPostWarrant reports whether any meta is a return-to-post impulse
// (WarrantKindReturnToPost). The laboring tick-shelve (actorCanReactNow, LLM-268)
// uses it to wake an off-post worker so she walks back — deliberately its own
// kind, NOT reused by the break / source-activity gates, so a "get back to the
// job" nudge never cuts short a rester or a mid-bite eater. Mirrors
// hasNPCSpeechWarrant.
func hasReturnToPostWarrant(list []WarrantMeta) bool {
	for _, m := range list {
		if m.Reason != nil && m.Reason.Kind() == WarrantKindReturnToPost {
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
