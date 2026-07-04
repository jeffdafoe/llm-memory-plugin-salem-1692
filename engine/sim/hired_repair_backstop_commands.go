package sim

import "time"

// hired_repair_backstop_commands.go — LLM-280. The substrate primitive for the
// hired-worker repair backstop: a paced sweep that RE-wakes a hired hand who is
// on-post at her employer's still-worn business, holds enough nails to mend it, but
// declined on her one-shot wake (maybeStampHiredRepairWarrant at startLaborWork) and
// was then shelved for the rest of the job. Sibling of the return-to-post backstop
// (return_to_post_backstop_commands.go) — same shape, opposite location: that one
// wakes a laboring worker who wandered OFF the post so she walks back; this one wakes
// a laboring worker who is AT the post so she lifts the hammer. The goroutine driver
// lives in engine/sim/cascade/hired_repair_backstop.go.
//
// WHY THIS EXISTS. The hired-repair wake (StallRepairHiredWarrantReason) is the ONLY
// thing that pierces the laboring shelve-gate (hasHiredRepairWarrant, reactor.go) to
// surface the repair tool to a StateLaboring worker — but maybeStampHiredRepairWarrant
// stamps it exactly once, at startLaborWork. A worker who narrates "I can fix that"
// instead of calling repair on that single tick (the LLM-278 case) is then shelved:
// the standing "## The business you're working at" cue still renders, but a laboring
// worker only draws a tick on a separate interrupt (a red need, a PC/NPC speaking, an
// operator nudge, a return-to-post warrant), so a worker alone on-post with none of
// those never draws another repair-capable tick and the employer's business stays
// worn until the 2-8h job window elapses (observed 2026-07-04: Patience Walker at the
// worn Ellis Farm). This sweep is the missing re-wake: it re-stamps the SAME hired
// warrant kind on a self-paced cadence, so the shelve-gate keeps lifting and she gets
// another chance to mend instead of one-and-done.
//
// ELIGIBILITY — the repair-ready-but-shelved case ONLY (hiredRepairBackstopEligible):
// a laboring worker whose hire resolves an employer business that is still worn, who
// is co-located with it, holds enough nails, and isn't already mid-activity — i.e.
// exactly the state in which she COULD call repair right now if she drew a tick.
// Gating on nails is the one deliberate divergence from the one-shot stamp (which
// fires even nail-less, as the initial "your workplace is worn" awareness nudge): a
// hired hand can't leave the job to shop, so re-nagging a worker who can't act would
// just burn ticks. An OFF-post hired worker is the return-to-post backstop's job, not
// this one — the two partition cleanly by location, so they never double-fire.
//
// YIELDS TO NEED. A worker who is ALSO red on hunger/thirst already ticks (the
// red-need warrant / hasBreakInterruptingNeedWarrant) and already sees the repair
// tool on that tick (gateTools advertises it whenever she's co-located + repairable),
// so the need path owns her — mend-or-eat is then the model's call. We skip without
// pacing the backoff, mirroring the return-to-post sweep's "eat before you walk back."
//
// COST DISCIPLINE. Eligibility is binary (repair-ready-and-shelved or not), so like
// the return-to-post / seek-work siblings there is no "partial progress" to reset the
// cadence — each sweep that actually STAMPS her escalates an EXPONENTIAL BACKOFF
// (HiredRepairBackoffLevel): base (90 s) doubling to the cap (30 min = the idle-
// backstop rate). A sweep that skips her for an open warrant cycle or a mid-tick
// (WarrantedSince / TickInFlight) does NOT pace — the stamp itself advances the timer.
// Mending resets Wear to 0, which makes her ineligible and clears the backoff; so does
// the job ending or her walking off-post. A worker who keeps declining costs no more
// than the idle backstop at steady state.
//
// Scope mirrors the sibling backstops: KindNPCStateful + KindNPCShared, excluding
// transient visitors.

const (
	defaultHiredRepairBackstopBaseDelay = 90 * time.Second
	defaultHiredRepairBackstopMaxDelay  = 30 * time.Minute
)

// HiredRepairBackstopTelemetry is the return value of EvaluateHiredRepairBackstop.
// Stamped is how many shelved-but-repair-ready hired workers got a re-wake this
// sweep; the Skipped* breakdown is why the rest didn't, for telemetry + the tests.
type HiredRepairBackstopTelemetry struct {
	Stamped              int
	SkippedScope         int // not KindNPCStateful / KindNPCShared, or a visitor
	SkippedNotEligible   int // not laboring, not hired, not worn, off-post, no nails, or mid-activity
	SkippedRedNeed       int // an actionable red need takes precedence (the need tick already surfaces repair)
	SkippedWarranted     int // open WarrantedSince cycle
	SkippedTickInFlight  int // mid-tick
	SkippedBackoff       int // still inside its backoff window
	SkippedStampDeclined int // tryStampWarrant funnel declined (unreachable today)
}

// EvaluateHiredRepairBackstop returns a Command that scans the world's actors and
// re-stamps a StallRepairHiredWarrantReason warrant on each laboring worker who is
// on-post at her employer's still-worn business with nails in hand (see
// hiredRepairBackstopEligible) and whose per-actor backoff window has elapsed. The
// whole scan + stamp + backoff update happens inside the single Fn on the world
// goroutine. now is the wall-clock the sweep started, passed in so tests can drive
// deterministic time-based scenarios.
func EvaluateHiredRepairBackstop(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			nowMinute := localMinuteOfDay(w, now)

			var t HiredRepairBackstopTelemetry
			for _, a := range w.Actors {
				if a.Kind != KindNPCStateful && a.Kind != KindNPCShared {
					t.SkippedScope++
					continue
				}
				if a.VisitorState != nil {
					t.SkippedScope++
					continue
				}

				stall, ok := hiredRepairBackstopEligible(w, a, now)
				if !ok {
					// Not a repair-ready shelved hired worker (mended, job ended,
					// wandered off-post, out of nails, mid-activity). Clear the backoff
					// so the NEXT worn-and-ready spell re-engages from base.
					clearHiredRepairBackstop(a)
					t.SkippedNotEligible++
					continue
				}

				// Eat before you mend: an actionable red need means the red-need backstop
				// (ZBBS-HOME-363) owns her wake — it stamps a break-interrupting need
				// warrant that lifts the SAME laboring shelve-gate, and repair is still
				// surfaced on that tick (gateTools re-checks co-location + nails), so she
				// can even mend then if she chooses. Deferring loses nothing and avoids a
				// redundant second wake; the "wait for another cascade" is that need path
				// doing its job, not a stuck worker. Skip WITHOUT pacing the backoff — once
				// the need resolves she re-engages on her existing cadence, not from base.
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

				// "paced" = we have already re-stamped this worker and are tracking her
				// backoff. Eligibility is binary, so a still-eligible paced worker made
				// no progress (she declined again) → escalate.
				paced := a.HiredRepairNextWarrantAt != nil
				if paced && now.Before(*a.HiredRepairNextWarrantAt) {
					t.SkippedBackoff++
					continue
				}

				level := 0
				if paced {
					level = a.HiredRepairBackoffLevel + 1
				}
				// Shared exponential-backoff helper (red_need_backstop_commands.go).
				delay := redNeedBackoffDelay(defaultHiredRepairBackstopBaseDelay, defaultHiredRepairBackstopMaxDelay, level)

				// Only advance the backoff (and count the stamp) if the funnel recorded
				// the warrant — same correct-by-construction posture as the sibling
				// sweeps (a declined stamp must not pace a tick that never happened).
				if !tryStampWarrant(w, a, WarrantMeta{
					TriggerActorID: a.ID,
					Reason:         StallRepairHiredWarrantReason{StallID: stall.ID},
				}, now) {
					t.SkippedStampDeclined++
					continue
				}

				next := now.Add(delay)
				a.HiredRepairNextWarrantAt = &next
				a.HiredRepairBackoffLevel = level
				t.Stamped++
			}
			return t, nil
		},
	}
}

// hiredRepairBackstopEligible reports whether a is a laboring worker who is on-post
// at her employer's still-worn business with enough nails to mend it — the "could
// call repair right now but is shelved" state — and returns the business she'd mend
// (for the warrant's StallID). Scope (kind / visitor) is checked by the caller. The
// mendable business + hired-vs-owned come from WearableStallToMend, the SAME resolver
// the repair cue (buildStallRepair) and StartRepair key off, so the sweep can't drift
// from them on which business or who may mend.
func hiredRepairBackstopEligible(w *World, a *Actor, now time.Time) (*VillageObject, bool) {
	// Must actually be laboring — the same three paired signals returnToPostEligible
	// requires: the StateLaboring enum, a live LaboringUntil window (the busy signal
	// actorCanReactNow shelves on), and (below, via WearableStallToMend) a Working
	// ledger offer. Requiring all three rejects a stale/corrupt mirror.
	if a.State != StateLaboring || a.LaboringUntil == nil || !a.LaboringUntil.After(now) {
		return nil, false
	}
	// Never nudge a legitimately-occupied worker: a stale sleep/break timer coexisting
	// with StateLaboring (a laboring worker shouldn't be sleeping/resting, but guard
	// defensively), or an in-flight SourceActivity — a running repair window IS a
	// SourceActivity, so this also stops us re-stamping mid-mend. The state enum can't
	// be Sleeping/Resting here (StateLaboring already excluded above), so guard on the
	// live timers, not a redundant enum compare.
	if (a.SleepingUntil != nil && a.SleepingUntil.After(now)) ||
		(a.BreakUntil != nil && a.BreakUntil.After(now)) ||
		a.SourceActivity != nil {
		return nil, false
	}
	// The business she's on the hook to mend, and only via a HIRE — an owner has her
	// own edge-triggered warrant and is never laboring-shelved at her own business.
	stall, hired := WearableStallToMend(w.VillageObjects, w.LaborLedger, a.ID)
	if stall == nil || !hired {
		return nil, false
	}
	// Still worth mending. Once she repairs, Wear resets to 0 and this goes false,
	// which clears the backoff — the sweep's own off-switch.
	if !StallRepairable(stall, w.Settings.StallWearRepairThreshold, w.Settings.StallWearDegradeThreshold) {
		return nil, false
	}
	// Must be AT the business to lift the hammer (the same co-location gate StartRepair
	// enforces). An off-post hired worker is the return-to-post backstop's case, not
	// this one — the two partition by location so they never double-fire.
	pin, pinOK := effectiveObjectLoiterTile(w, stall.ID)
	if !AtBusiness(a.Pos, a.InsideStructureID, stall.ID, pin, pinOK) {
		return nil, false
	}
	// A hired hand can't leave the job to shop, so only re-wake her when she can
	// actually mend right now — enough nails in hand. (The one-shot stamp fires even
	// nail-less, as the initial awareness nudge; the re-nag is stricter.)
	if a.Inventory[NailItemKind] < w.Settings.StallNailsPerRepair {
		return nil, false
	}
	return stall, true
}

// clearHiredRepairBackstop resets a worker's hired-repair backoff pacing. Called when
// she is no longer a repair-ready shelved hired worker (so the next worn-and-ready
// spell starts at base) and on LoadWorld via resetReactorStateOnLoad.
func clearHiredRepairBackstop(a *Actor) {
	a.HiredRepairNextWarrantAt = nil
	a.HiredRepairBackoffLevel = 0
}
