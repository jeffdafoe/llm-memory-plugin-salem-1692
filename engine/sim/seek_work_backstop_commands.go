package sim

import "time"

// seek_work_backstop_commands.go — LLM-141. The substrate primitive for the
// idle-broke-worker backstop: a paced sweep that re-engages a Worker who has
// run out of coins but has no pressing need, so it goes and earns instead of
// going dormant. Sibling of the red-need backstop
// (red_need_backstop_commands.go); the goroutine driver lives in
// engine/sim/cascade/seek_work_backstop.go.
//
// WHY THIS EXISTS. A Worker's whole point is the labor income faucet, but
// "broke" is not a need and so stamps no warrant. A worker with sub-red needs
// and an empty purse therefore has nothing to wake it — the only re-engagers
// (hourly needs tick, 30-min idle backstop) either need a red need or produce
// a contentless idle tick that goes stale before render. So the worker sits
// frozen, never soliciting work (observed: Lewis Walker, 0 ticks / 0 warrants,
// idle in a berry patch). This sweep stamps a SeekWorkWarrantReason — an
// engine-authored felt impulse ("your purse is empty, you take work for pay")
// that is real rendered content, so the tick actually deliberates.
//
// YIELDS TO NEED. A broke worker that is ALSO red on a need is left to the
// red-need backstop — eat before you work. We skip it here without touching
// its seek-work backoff, so once the need resolves it re-engages on its
// existing cadence.
//
// COST DISCIPLINE. Eligibility is binary (coins == 0), so unlike the red-need
// sweep there is no "partial progress" to reset the cadence — every sweep the
// worker stays broke-and-idle escalates an EXPONENTIAL BACKOFF
// (SeekWorkBackoffLevel): base (90 s) doubling to the cap (30 min = the idle-
// backstop rate). A worker who can never find work therefore costs no more at
// steady state than the idle backstop. Earning a coin (or going off-shift /
// taking a job) makes it ineligible, which clears the backoff so the next
// broke spell re-engages from base.
//
// Scope mirrors the red-need backstop: KindNPCStateful + KindNPCShared,
// excluding transient visitors.

const (
	defaultSeekWorkBackstopBaseDelay = 90 * time.Second
	defaultSeekWorkBackstopMaxDelay  = 30 * time.Minute
)

// SeekWorkBackstopTelemetry is the return value of EvaluateSeekWorkBackstop.
// Stamped is how many broke workers got a seek-work warrant this sweep; the
// Skipped* breakdown is why the rest didn't, for telemetry + the unit tests.
type SeekWorkBackstopTelemetry struct {
	Stamped              int
	SkippedScope         int // not KindNPCStateful / KindNPCShared, or a visitor
	SkippedNotEligible   int // not a worker, has coins, off-shift, asleep, or already working
	SkippedRedNeed       int // an actionable red need takes precedence (eat before work)
	SkippedWarranted     int // open WarrantedSince cycle
	SkippedTickInFlight  int // mid-tick
	SkippedBackoff       int // still inside its backoff window
	SkippedStampDeclined int // tryStampWarrant funnel declined (unreachable today)
}

// EvaluateSeekWorkBackstop returns a Command that scans the world's actors and
// stamps a SeekWorkWarrantReason warrant on each broke, on-shift, idle Worker
// whose per-actor backoff window has elapsed. The whole scan + stamp + backoff
// update happens inside the single Fn on the world goroutine. now is the
// wall-clock the sweep started, passed in so tests can drive deterministic
// time-based scenarios.
func EvaluateSeekWorkBackstop(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			nowMinute := localMinuteOfDay(w, now)

			var t SeekWorkBackstopTelemetry
			for _, a := range w.Actors {
				if a.Kind != KindNPCStateful && a.Kind != KindNPCShared {
					t.SkippedScope++
					continue
				}
				if a.VisitorState != nil {
					t.SkippedScope++
					continue
				}

				if !seekWorkEligible(w, a, now, nowMinute) {
					// Not a broke idle worker (earned, off-shift, working, …).
					// Clear the backoff so the NEXT broke spell re-engages from
					// base rather than inheriting a stale escalated timer.
					clearSeekWorkBackstop(a)
					t.SkippedNotEligible++
					continue
				}

				// Eat before you work: a broke worker that is also red on a need
				// is the red-need backstop's job. Leave the seek-work backoff
				// untouched — once the need resolves it re-engages on its
				// existing cadence rather than resetting to base.
				if _, red := actorActionableRedNeed(w, a, now, nowMinute); red {
					t.SkippedRedNeed++
					continue
				}

				// An actor already pending a tick or mid-LLM-call doesn't need
				// an injected warrant. Don't touch the backoff timer either.
				if a.WarrantedSince != nil {
					t.SkippedWarranted++
					continue
				}
				if a.TickInFlight {
					t.SkippedTickInFlight++
					continue
				}

				// "paced" = we have already stamped a seek-work warrant and are
				// tracking this worker's backoff. Eligibility is binary, so a
				// still-eligible paced worker has made no progress → escalate.
				paced := a.SeekWorkNextWarrantAt != nil
				if paced && now.Before(*a.SeekWorkNextWarrantAt) {
					t.SkippedBackoff++
					continue
				}

				level := 0
				if paced {
					level = a.SeekWorkBackoffLevel + 1
				}
				// Shared exponential-backoff helper (red_need_backstop_commands.go).
				delay := redNeedBackoffDelay(defaultSeekWorkBackstopBaseDelay, defaultSeekWorkBackstopMaxDelay, level)

				// Only advance the backoff (and count the stamp) if the funnel
				// recorded the warrant — same correct-by-construction posture as
				// the red-need sweep (a declined stamp must not pace a tick that
				// never happened).
				if !tryStampWarrant(w, a, WarrantMeta{
					TriggerActorID: a.ID,
					Reason:         SeekWorkWarrantReason{},
				}, now) {
					t.SkippedStampDeclined++
					continue
				}

				next := now.Add(delay)
				a.SeekWorkNextWarrantAt = &next
				a.SeekWorkBackoffLevel = level
				t.Stamped++
			}
			return t, nil
		},
	}
}

// seekWorkEligible reports whether a is a broke, on-shift, awake Worker with no
// live or pending labor job — the core "should be out earning but isn't" state.
// Scope (kind / visitor) is checked by the caller.
func seekWorkEligible(w *World, a *Actor, now time.Time, nowMinute int) bool {
	if !actorIsWorker(a) {
		return false
	}
	if a.Coins != 0 {
		return false
	}
	// Never nudge a sleeper (the seek-work warrant can't wake one anyway — it is
	// not a rester-interrupting kind — so stamping it would only waste a slot).
	if a.State == StateSleeping {
		return false
	}
	if a.SleepingUntil != nil && a.SleepingUntil.After(now) {
		return false
	}
	// Don't disturb a cleanly-occupied worker: a scheduled break (StateResting /
	// BreakUntil — recovering) or an in-flight timed source activity (mid
	// eat/drink/harvest). The seek-work kind isn't a rester-interrupting one, so
	// such a warrant would only shelve in actorCanReactNow anyway — skipping here
	// keeps the invariant local and avoids burning a warrant slot + advancing the
	// backoff on a worker that is legitimately busy.
	if a.State == StateResting {
		return false
	}
	if a.BreakUntil != nil && a.BreakUntil.After(now) {
		return false
	}
	if a.SourceActivity != nil {
		return false
	}
	if !actorOnShift(w, a, nowMinute) {
		return false
	}
	// Ledger-authoritative busy check (same rationale as SolicitWork): a worker
	// mid-job or holding a live pending offer is already engaged. Shares
	// workerPendingLaborOffer with SolicitWork's gate so the predicate can't drift.
	if workerHasLiveJob(w, a.ID) {
		return false
	}
	if workerPendingLaborOffer(w, a.ID, now) != nil {
		return false
	}
	return true
}

// clearSeekWorkBackstop resets a worker's seek-work backoff pacing. Called when
// the worker is no longer an eligible broke idler (so the next broke spell
// starts at base) and on LoadWorld via resetReactorStateOnLoad.
func clearSeekWorkBackstop(a *Actor) {
	a.SeekWorkNextWarrantAt = nil
	a.SeekWorkBackoffLevel = 0
}
