package sim

import "time"

// return_to_post_backstop_commands.go — LLM-268. The substrate primitive for the
// off-post-laboring-worker backstop: a paced sweep that re-engages a hired worker
// who has wandered off the employer's post so she heads back and actually helps,
// instead of standing marooned wherever a need-break left her until the job's
// completion sweep clears her. Sibling of the seek-work backstop
// (seek_work_backstop_commands.go); the goroutine driver lives in
// engine/sim/cascade/return_to_post_backstop.go.
//
// WHY THIS EXISTS. LLM-230 strips a laboring worker's move_to to keep her
// committed to the job, with one carve-out: a red hunger/thirst need re-grants it
// so she can reach food (laboringMayBreakOffToEat / hasBreakInterruptingNeedWarrant).
// She walks off to eat — and when the need clears, the carve-out closes and takes
// move_to AND her tick eligibility with it, wherever she happens to be standing.
// With green needs and no warrant she never ticks again (observed: Silence Walker,
// 240-min contract, stranded at the Well from 15:15 until the completion sweep).
// LLM-268 re-grants move_to for an off-post laboring worker (gateTools reads her
// LaboringView.OffPost); this sweep is the matching tick driver, so she actually
// wakes to use it — the tool surface and the reactor tick must move in lockstep or
// she'd hold move_to but never wake (the marooning), or wake but be denied the tool
// (a wasted tick).
//
// ELIGIBILITY — the wandered-worker case ONLY. Off the post while the employer
// STILL HOLDS it. If the employer is also away she is likely following them (the
// "come with me" accompany case, LLM-268 condition 3), which rides the employer's
// speech tick, not a spontaneous return — nudging her back to an unheld post would
// fight that, so we skip it. A worker AT the post is not eligible; nor is one whose
// employer has no work structure (an in-place hire — no post to be off).
//
// YIELDS TO NEED. An off-post worker who is ALSO red on hunger/thirst already keeps
// move_to (the LLM-230 need carve-out) and already ticks (the red-need warrant), so
// the need path owns her. We skip without touching the backoff, so once the need
// resolves she re-engages on her existing cadence.
//
// COST DISCIPLINE. Eligibility is binary (off-post-while-employer-present or not),
// so like the seek-work sweep there is no "partial progress" to reset the cadence —
// every sweep she stays off-post escalates an EXPONENTIAL BACKOFF
// (ReturnToPostBackoffLevel): base (90 s) doubling to the cap (30 min = the idle-
// backstop rate). In practice off-post is brief (she walks back within a tick or
// two, which makes her ineligible and clears the backoff), so steady-state cost is
// ~nil; a worker who somehow can never get back costs no more than the idle
// backstop. Returning to the post, or the job ending, makes her ineligible, which
// clears the backoff so the next off-post spell re-engages from base.
//
// Scope mirrors the seek-work backstop: KindNPCStateful + KindNPCShared, excluding
// transient visitors.

const (
	defaultReturnToPostBackstopBaseDelay = 90 * time.Second
	defaultReturnToPostBackstopMaxDelay  = 30 * time.Minute
)

// ReturnToPostBackstopTelemetry is the return value of EvaluateReturnToPostBackstop.
// Stamped is how many off-post workers got a return-to-post warrant this sweep; the
// Skipped* breakdown is why the rest didn't, for telemetry + the unit tests.
type ReturnToPostBackstopTelemetry struct {
	Stamped              int
	SkippedScope         int // not KindNPCStateful / KindNPCShared, or a visitor
	SkippedNotEligible   int // at the post, no live job, employer also away, or asleep/resting/mid-activity
	SkippedRedNeed       int // an actionable red need takes precedence (eat before walking back)
	SkippedWarranted     int // open WarrantedSince cycle
	SkippedTickInFlight  int // mid-tick
	SkippedBackoff       int // still inside its backoff window
	SkippedStampDeclined int // tryStampWarrant funnel declined (unreachable today)
}

// EvaluateReturnToPostBackstop returns a Command that scans the world's actors and
// stamps a ReturnToPostWarrantReason warrant on each off-post laboring worker (green
// needs, employer still at the post) whose per-actor backoff window has elapsed. The
// whole scan + stamp + backoff update happens inside the single Fn on the world
// goroutine. now is the wall-clock the sweep started, passed in so tests can drive
// deterministic time-based scenarios.
func EvaluateReturnToPostBackstop(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			nowMinute := localMinuteOfDay(w, now)

			var t ReturnToPostBackstopTelemetry
			for _, a := range w.Actors {
				if a.Kind != KindNPCStateful && a.Kind != KindNPCShared {
					t.SkippedScope++
					continue
				}
				if a.VisitorState != nil {
					t.SkippedScope++
					continue
				}

				if !returnToPostEligible(w, a, now) {
					// Not an off-post laboring worker (at the post, no job, employer
					// away, occupied). Clear the backoff so the NEXT off-post spell
					// re-engages from base rather than inheriting a stale timer.
					clearReturnToPostBackstop(a)
					t.SkippedNotEligible++
					continue
				}

				// Eat before you walk back: an off-post worker who is ALSO red on a
				// need already keeps move_to (the LLM-230 need carve-out) and already
				// ticks (hasBreakInterruptingNeedWarrant), so the need path owns her.
				// Leave the backoff untouched — once the need resolves she re-engages
				// on her existing cadence rather than resetting to base.
				if _, red := actorActionableRedNeed(w, a, now, nowMinute); red {
					t.SkippedRedNeed++
					continue
				}

				// An actor already pending a tick or mid-LLM-call doesn't need an
				// injected warrant. Don't touch the backoff timer either.
				if a.WarrantedSince != nil {
					t.SkippedWarranted++
					continue
				}
				if a.TickInFlight {
					t.SkippedTickInFlight++
					continue
				}

				// "paced" = we have already stamped a return-to-post warrant and are
				// tracking this worker's backoff. Eligibility is binary, so a still-
				// eligible paced worker has made no progress → escalate.
				paced := a.ReturnToPostNextWarrantAt != nil
				if paced && now.Before(*a.ReturnToPostNextWarrantAt) {
					t.SkippedBackoff++
					continue
				}

				level := 0
				if paced {
					level = a.ReturnToPostBackoffLevel + 1
				}
				// Shared exponential-backoff helper (red_need_backstop_commands.go).
				delay := redNeedBackoffDelay(defaultReturnToPostBackstopBaseDelay, defaultReturnToPostBackstopMaxDelay, level)

				// Only advance the backoff (and count the stamp) if the funnel
				// recorded the warrant — same correct-by-construction posture as the
				// seek-work sweep (a declined stamp must not pace a tick that never
				// happened).
				if !tryStampWarrant(w, a, WarrantMeta{
					TriggerActorID: a.ID,
					Reason:         ReturnToPostWarrantReason{},
				}, now) {
					t.SkippedStampDeclined++
					continue
				}

				next := now.Add(delay)
				a.ReturnToPostNextWarrantAt = &next
				a.ReturnToPostBackoffLevel = level
				t.Stamped++
			}
			return t, nil
		},
	}
}

// returnToPostEligible reports whether a is a laboring worker who has wandered off
// the employer's post while the employer still holds it — the "should head back but
// won't wake on her own" state. Scope (kind / visitor) is checked by the caller. The
// employer + post come from the live Working offer (workerWorkingOffer), the same
// ledger the perception self-state reads, so the sweep and the OffPost cue agree on
// which post she belongs at.
func returnToPostEligible(w *World, a *Actor, now time.Time) bool {
	// Must be actively laboring. Read the ledger (authoritative for the employer +
	// post), not the LaboringUntil mirror: a stranded StateLaboring with no live
	// Working offer is the settle/reconcile path's job, not this one.
	offer := workerWorkingOffer(w, a.ID)
	if offer == nil {
		return false
	}
	// Never nudge a sleeper / rester / mid-activity worker — she's legitimately
	// occupied, and the return-to-post kind can't wake a rester anyway (it's not a
	// rester-interrupting kind), so a stamp would only waste a slot. A need-break
	// eater has an in-flight SourceActivity; a laboring worker shouldn't be resting,
	// but guard defensively.
	if a.State == StateSleeping || (a.SleepingUntil != nil && a.SleepingUntil.After(now)) {
		return false
	}
	if a.State == StateResting || (a.BreakUntil != nil && a.BreakUntil.After(now)) {
		return false
	}
	if a.SourceActivity != nil {
		return false
	}
	employer := w.Actors[offer.EmployerID]
	if employer == nil {
		return false
	}
	post := employer.WorkStructureID
	if post == "" {
		return false // an in-place hire (workless employer) — no post to be off
	}
	if actorAtWorkpost(w, a, post) {
		return false // already at the post
	}
	// The employer must still hold the post. If they are ALSO away, she is likely
	// following them (the accompany case) — that rides the employer's speech, not a
	// spontaneous return, and nudging her back to an unheld post would fight it.
	if !actorAtWorkpost(w, employer, post) {
		return false
	}
	return true
}

// clearReturnToPostBackstop resets a worker's return-to-post backoff pacing. Called
// when the worker is no longer an off-post laboring worker (so the next off-post
// spell starts at base) and on LoadWorld via resetReactorStateOnLoad.
func clearReturnToPostBackstop(a *Actor) {
	a.ReturnToPostNextWarrantAt = nil
	a.ReturnToPostBackoffLevel = 0
}
