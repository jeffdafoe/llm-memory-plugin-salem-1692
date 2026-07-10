package sim

import "time"

// seek_work_backstop_commands.go — LLM-141/168. The substrate primitive for the
// idle-workless-worker backstop: a paced sweep that re-engages a Worker who has
// no workplace of its own and no pressing need, so it goes and finds odd jobs
// instead of going dormant. Sibling of the red-need backstop
// (red_need_backstop_commands.go); the goroutine driver lives in
// engine/sim/cascade/seek_work_backstop.go.
//
// WHY THIS EXISTS. A Worker's whole point is the labor income faucet, but a
// WORKLESS worker (the worker attribute, no work_structure_id) has no post to
// keep — the shift-duty warrant + duty steer, which both need a work anchor,
// never fire for it. With sub-red needs it therefore has nothing to wake it on
// shift: the only re-engagers (hourly needs tick, 30-min idle backstop) either
// need a red need or produce a contentless idle tick that goes stale before
// render. So the worker sits frozen, never soliciting work (observed: Lewis
// Walker, 0 ticks / 0 warrants, idle in a berry patch). This sweep stamps a
// SeekWorkWarrantReason — an engine-authored felt impulse ("you have no work of
// your own … you take work for pay") that is real rendered content, so the tick
// actually deliberates. (LLM-168 re-anchored eligibility from broke/Coins==0 to
// workless: a workless worker holding a few coins, like the brand-new Walkers,
// was left idle all shift by the old broke gate.)
//
// YIELDS TO NEED. A workless worker that is ALSO red on a need is left to the
// red-need backstop — eat before you work. We skip it here without touching
// its seek-work backoff, so once the need resolves it re-engages on its
// existing cadence.
//
// COST DISCIPLINE. Eligibility is binary (workless or not), so unlike the red-
// need sweep there is no "partial progress" to reset the cadence — every sweep
// the worker stays workless-and-idle escalates an EXPONENTIAL BACKOFF
// (SeekWorkBackoffLevel): base (90 s) doubling to the cap (30 min = the idle-
// backstop rate). A worker who can never find work therefore costs no more at
// steady state than the idle backstop. Gaining a workplace (or going off-shift /
// taking a job) makes it ineligible, which clears the backoff so the next
// workless spell re-engages from base.
//
// Scope mirrors the red-need backstop: KindNPCStateful + KindNPCShared,
// excluding transient visitors.

const (
	defaultSeekWorkBackstopBaseDelay = 90 * time.Second
	defaultSeekWorkBackstopMaxDelay  = 30 * time.Minute
)

// SeekWorkBackstopTelemetry is the return value of EvaluateSeekWorkBackstop.
// Stamped is how many workless workers got a seek-work warrant this sweep; the
// Skipped* breakdown is why the rest didn't, for telemetry + the unit tests.
type SeekWorkBackstopTelemetry struct {
	Stamped              int
	Redirected           int // of Stamped, how many were re-aimed at eating/drinking (TendNeed) instead of seeking work (LLM-276)
	AtEase               int // of Stamped, how many were a comfortable idler's at-ease liveness impulse instead of seek-work (LLM-352)
	SkippedScope         int // not KindNPCStateful / KindNPCShared, or a visitor
	SkippedNotEligible   int // not a worker, has a workplace, off-shift, asleep, or already working
	SkippedRedNeed       int // an actionable red need takes precedence (eat before work)
	SkippedWarranted     int // open WarrantedSince cycle
	SkippedTickInFlight  int // mid-tick
	SkippedBackoff       int // still inside its backoff window
	SkippedEco           int // a comfortable idler's at-ease impulse withheld while unwatched (LLM-352 audience-gate)
	SkippedStampDeclined int // tryStampWarrant funnel declined (unreachable today)
}

// EvaluateSeekWorkBackstop returns a Command that scans the world's actors and
// stamps a SeekWorkWarrantReason warrant on each workless, on-shift, idle Worker
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

				if !worklessIdleWorkerOnShift(w, a, now, nowMinute) {
					// Not a workless idle worker (has a workplace, off-shift,
					// working, …). Clear the backoff so the NEXT workless spell
					// re-engages from base rather than inheriting a stale timer.
					// NOTE the gate is the coin-ceiling-AGNOSTIC predicate: a
					// comfortable workless idler is still handled here (it takes the
					// at-ease arm below), where the old seek-work-only gate skipped
					// it into the freeze this ticket fixes (LLM-352).
					clearSeekWorkBackstop(a)
					t.SkippedNotEligible++
					continue
				}

				// Eat before you work: a workless worker that is also red on a need
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

				// Which felt impulse to stamp — three arms sharing this waker and its
				// backoff:
				//   1. TendNeed (LLM-276): a worker (broke OR comfortable) that has grown
				//      hungry/thirsty (upper felt band) AND can resolve it now — carries
				//      food, holds coin, or a free source is nearby — is steered to EAT.
				//   2. SeekWork (LLM-141): a below-ceiling worker goes to earn.
				//   3. AtEase (LLM-352): an at/above-ceiling ("comfortable") worker has no
				//      go-earn nudge to give — that would be a lie, it doesn't need the
				//      work — so it gets the "the day is your own" leisure impulse instead
				//      of the freeze the coin ceiling (LLM-194) otherwise left it in.
				// AtEase is AUDIENCE-GATED: withheld while unwatched (like the plain idle
				// backstop under eco, LLM-313). No coin moves and no counterparty waits —
				// it is cosmetic liveness for a watcher, so it costs nothing when nobody is
				// looking. It re-qualifies every sweep, so the first sweep after the
				// audience returns stamps as usual; the backoff is left untouched on the
				// eco-skip so recovery is prompt.
				var reason WarrantReason = SeekWorkWarrantReason{}
				redirected := false
				atEase := false
				if need, ok := pressingResolvableConsumableNeed(w, a); ok {
					reason = TendNeedWarrantReason{Need: need}
					redirected = true
				} else if workerIsComfortable(w, a) {
					if ecoModeEngaged(w, now) {
						t.SkippedEco++
						continue
					}
					reason = AtEaseWarrantReason{}
					atEase = true
				}
				// Only advance the backoff (and count the stamp) if the funnel
				// recorded the warrant — same correct-by-construction posture as
				// the red-need sweep (a declined stamp must not pace a tick that
				// never happened).
				if !tryStampWarrant(w, a, WarrantMeta{
					TriggerActorID: a.ID,
					Reason:         reason,
				}, now) {
					t.SkippedStampDeclined++
					continue
				}

				next := now.Add(delay)
				a.SeekWorkNextWarrantAt = &next
				a.SeekWorkBackoffLevel = level
				t.Stamped++
				if redirected {
					t.Redirected++
				}
				if atEase {
					t.AtEase++
				}
			}
			return t, nil
		},
	}
}

// SeekWorkCoinCeilingDefault is the wealth shelf above which a workless worker
// stops seeking/soliciting work (LLM-194): at or above this coin balance the worker
// is "comfortable" and reads as a plain idle villager, draining its purse via
// ordinary eating/drinking until it dips back under the ceiling and re-enters the
// labor market. Used when WorldSettings.SeekWorkCoinCeiling is unset (<= 0). 25 ≈ a
// few days of vendor food, but workers forage free berries + drink free at the well,
// so coins are discretionary, not a survival buffer — this is a "how rich before they
// chill" feel knob. Live-tunable + persisted via settings/seek-work-ceiling; the read
// side is GET /settings.
const SeekWorkCoinCeilingDefault = 25

// effectiveSeekWorkCoinCeiling resolves the configured seek-work coin ceiling,
// falling back to SeekWorkCoinCeilingDefault when WorldSettings.SeekWorkCoinCeiling is
// unset (<= 0). A zero ceiling would otherwise read as "every worker is comfortable"
// (coins >= 0 is always true), suppressing seek-work entirely — so zero means "use the
// default", mirroring effectiveHuddleLoopSweepCadence's zero-is-default posture. To
// effectively DISABLE the shelf (restore always-seek), set a very large ceiling.
func effectiveSeekWorkCoinCeiling(s WorldSettings) int {
	if s.SeekWorkCoinCeiling > 0 {
		return s.SeekWorkCoinCeiling
	}
	return SeekWorkCoinCeilingDefault
}

// workerIsComfortable reports whether a worker holds enough coin to stop hustling for
// work (LLM-194): coins at or above the effective seek-work ceiling. The single
// sim-side predicate behind the seek-work warrant gate; the perception side mirrors it
// via subjectIsComfortable reading snap.SeekWorkCoinCeiling (the effective value copied
// at publish), so the warrant and the directory/affordance cues can't disagree.
func workerIsComfortable(w *World, a *Actor) bool {
	return a.Coins >= effectiveSeekWorkCoinCeiling(w.Settings)
}

// worklessIdleWorkerOnShift reports whether a is a workless, on-shift, awake Worker
// with no live or pending labor job — the coin-ceiling-AGNOSTIC "has no post to keep
// but is here and unengaged" state shared by BOTH backstop arms. seekWorkEligible
// adds "and below the coin ceiling" (goes to earn, LLM-141); the at-ease arm covers
// the at/above-ceiling worker (takes its ease instead of freezing, LLM-352). Scope
// (kind / visitor) is checked by the caller.
func worklessIdleWorkerOnShift(w *World, a *Actor, now time.Time, nowMinute int) bool {
	if !actorIsWorker(a) {
		return false
	}
	// LLM-168: the population this nudge serves is the WORKLESS worker — it
	// carries the worker attribute (the labor income faucet) but has no RESOLVABLE
	// work_structure_id, so it has no post to keep and the shift-duty warrant +
	// duty steer (both of which need a resolvable work anchor) never usefully fire
	// for it. On-shift it therefore has no driver at all, so without this it goes
	// dormant or idle-loops. A worker WITH a resolvable workplace is already driven
	// to its post by the duty steer and doesn't need seek-work. "Resolvable" (not a
	// bare WorkStructureID != "") matches perception's buildAnchors, so the warrant
	// agrees with the directory even for a set-but-dangling id. (Previously gated on
	// Coins==0 — a "broke" proxy that left a workless worker holding a few coins,
	// like the brand-new Walker family, in a dead zone for its whole shift.)
	if actorHasResolvableWorkplace(w, a) {
		return false
	}
	// Never nudge a sleeper (neither impulse is a rester-interrupting kind — it
	// can't wake one anyway — so stamping it would only waste a slot).
	if a.State == StateSleeping {
		return false
	}
	if a.SleepingUntil != nil && a.SleepingUntil.After(now) {
		return false
	}
	// Don't disturb a cleanly-occupied worker: a scheduled break (StateResting /
	// BreakUntil — recovering) or an in-flight timed source activity (mid
	// eat/drink/harvest). Neither impulse is a rester-interrupting one, so such a
	// warrant would only shelve in actorCanReactNow anyway — skipping here keeps the
	// invariant local and avoids burning a warrant slot + advancing the backoff on a
	// worker that is legitimately busy.
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

// seekWorkEligible reports whether a workless idle worker should be SEEKING work:
// worklessIdleWorkerOnShift AND below the seek-work coin ceiling. At/above the
// ceiling ("comfortable", workerIsComfortable / LLM-194) the worker doesn't need
// odd jobs — the at-ease arm keeps it a live idle villager instead of leaving it to
// freeze (LLM-352). The sweep gates on worklessIdleWorkerOnShift and splits by
// workerIsComfortable inline; this named predicate is retained for the tests and
// any future caller.
func seekWorkEligible(w *World, a *Actor, now time.Time, nowMinute int) bool {
	return worklessIdleWorkerOnShift(w, a, now, nowMinute) && !workerIsComfortable(w, a)
}

// actorHasResolvableWorkplace reports whether the actor's WorkStructureID names a
// structure (or shared village_object) PRESENT in the live world — the sim-side
// mirror of perception's resolveStructureLabel. Seek-work keys on this, not the raw
// field, so the warrant agrees with the duty steer and the seek-work directory: a
// worker is "workless" exactly when it has no post the engine can route it to. A
// set-but-dangling WorkStructureID reads as workless, so such a worker still seeks
// work rather than dead-zoning between an unroutable duty steer and a suppressed
// seek-work cue (LLM-168).
func actorHasResolvableWorkplace(w *World, a *Actor) bool {
	if a.WorkStructureID == "" {
		return false
	}
	if w.Structures[a.WorkStructureID] != nil {
		return true
	}
	if w.VillageObjects[VillageObjectID(a.WorkStructureID)] != nil {
		return true
	}
	return false
}

// clearSeekWorkBackstop resets a worker's seek-work backoff pacing. Called when
// the worker is no longer an eligible broke idler (so the next broke spell
// starts at base) and on LoadWorld via resetReactorStateOnLoad.
func clearSeekWorkBackstop(a *Actor) {
	a.SeekWorkNextWarrantAt = nil
	a.SeekWorkBackoffLevel = 0
}

// consumableNeeds are the needs a worker can resolve by eating or drinking — the
// scope of the LLM-276 seek-work→eat redirect. Tiredness is excluded (its remedy is
// rest/sleep, not a purchase, and it has its own on-shift + red-need handling).
// Mirrors perception's satiationNeeds and the fixed hunger-before-thirst render
// order so the warrant and the eat/drink cue agree on which needs count.
var consumableNeeds = []NeedKey{"hunger", "thirst"}

// SeekWorkNeedYieldMarginDefault is the default width, below each need's red-line
// threshold, of the "upper felt" band in which the seek-work backstop redirects a
// resolvable-need worker to eat/drink instead of seeking work (LLM-276). At the
// default hunger threshold (18) a margin of 5 opens the redirect at hunger 13 — high
// enough that a worker spends most of its day below it (still job-hunting), but with
// a ~30-40 min cushion before the red-line so it eats calmly rather than spiraling
// into a beg-for-food loop. Used when WorldSettings.SeekWorkNeedYieldMargin is unset
// (<= 0). Live-tunable + persisted via settings/seek-work-need-margin; the read side
// is GET /settings.
const SeekWorkNeedYieldMarginDefault = 5

// effectiveSeekWorkNeedYieldMargin resolves the configured upper-felt margin,
// falling back to SeekWorkNeedYieldMarginDefault when unset (<= 0). Zero means "use
// the default": a zero margin would collapse the redirect band to nothing (only a
// need already at its red-line would qualify — the red-need backstop's job),
// mirroring effectiveSeekWorkCoinCeiling's zero-is-default posture.
func effectiveSeekWorkNeedYieldMargin(s WorldSettings) int {
	if s.SeekWorkNeedYieldMargin > 0 {
		return s.SeekWorkNeedYieldMargin
	}
	return SeekWorkNeedYieldMarginDefault
}

// pressingResolvableConsumableNeed reports the first consumable need (hunger, then
// thirst) that a workless idle worker should break off job-hunting to resolve right
// now: it sits in the UPPER FELT band — at or above (red threshold - margin) but
// still below red — and it is RESOLVABLE (consumableNeedResolvable). ok=false when
// none qualifies, in which case the caller stamps the ordinary seek-work impulse.
//
// The band is deliberately sub-red: a red need is the red-need backstop's job (the
// seek-work sweep already yields to it upstream via actorActionableRedNeed), and a
// need below the margin is not pressing enough to interrupt earning. The caller has
// already established the actor is on-shift, awake, and not mid source-activity
// (seekWorkEligible), so no dwell/rest re-check is needed here.
func pressingResolvableConsumableNeed(w *World, a *Actor) (NeedKey, bool) {
	if a.Needs == nil {
		return "", false
	}
	margin := effectiveSeekWorkNeedYieldMargin(w.Settings)
	for _, need := range consumableNeeds {
		threshold := w.Settings.NeedThresholds.Get(need)
		level := a.Needs[need]
		if level < threshold-margin || level >= threshold {
			continue // below the redirect band, or already red (red-need backstop's job)
		}
		if consumableNeedResolvable(w, a, need) {
			return need, true
		}
	}
	return "", false
}

// consumableNeedResolvable reports whether the actor has SOME plausible way to ease
// `need` right now: it carries a satisfier, it holds coin (Salem's food vendors take
// coin, and an unknown-price vendor is a walk-over-and-learn per the LLM-176
// redirect), or a free public source for the need exists in the world. Deliberately
// COARSE — it only has to split "eating is possible" from "genuinely stuck" (a broke
// worker with no free source, who should keep seeking work for meal money); the
// perception satiation cue picks the actual target and drops payment/stock dead-ends.
// This is the same fire-then-let-perception-render posture the red-need backstop
// takes, and the sim-vs-perception split follows the workerIsComfortable /
// subjectIsComfortable precedent.
func consumableNeedResolvable(w *World, a *Actor, need NeedKey) bool {
	if actorCarriesSatisfier(w, a, need) {
		return true
	}
	if freeConsumableSourceExists(w, a, need) {
		return true
	}
	// Coin is only a means if a vendor actually sells a satisfier this actor can
	// afford (or one whose price it hasn't learned — a walk-over-and-learn, matching
	// the LLM-176 need-redirect's unknown-price handling). Gating on coins alone
	// would promise "you have the means to see to it" to a worker holding 1 coin in a
	// village where a meal costs 5 (code_review) — a false tend-need cue.
	return a.Coins > 0 && affordableConsumableVendorExists(w, a, need)
}

// itemEasesNeed reports whether a unit of the item eases `need` on the immediate hit
// per the catalog — the shared satisfier test behind the own-stock and vendor arms of
// resolvability. Mirrors perception's itemNeedMagnitude ( > 0 ). Nil-safe (an item
// kind absent from the catalog eases nothing).
func itemEasesNeed(def *ItemKindDef, need NeedKey) bool {
	if def == nil {
		return false
	}
	for _, satisfies := range def.Satisfies {
		if satisfies.Attribute == need && satisfies.Immediate > 0 {
			return true
		}
	}
	return false
}

// actorCarriesSatisfier reports whether the actor holds any item that eases `need`
// on the immediate hit — the own-stock arm of resolvability. Mirrors perception's
// gatherOwnStock magnitude read (itemNeedMagnitude).
func actorCarriesSatisfier(w *World, a *Actor, need NeedKey) bool {
	for kind, qty := range a.Inventory {
		if qty > 0 && itemEasesNeed(w.ItemKinds[kind], need) {
			return true
		}
	}
	return false
}

// affordableConsumableVendorExists reports whether some non-PC actor stationed at a
// resolvable workplace sells an item that eases `need` which this actor can pay for —
// a known retail price within its purse, or an unknown price (no catalog recipe) it
// would walk over to learn. The coin arm of resolvability. Coarse by design: it does
// NOT model per-buyer last-paid, barter, wholesaler routing, or reachability — the
// satiation cue applies those when it renders the actual buy targets. Mirrors
// perception's eachVendorOffer structural-vendorship (a vendor is a stationed holder).
func affordableConsumableVendorExists(w *World, a *Actor, need NeedKey) bool {
	for vendorID, vendor := range w.Actors {
		if vendor == nil || vendorID == a.ID || vendor.Kind == KindPC {
			continue
		}
		if !actorHasResolvableWorkplace(w, vendor) {
			continue
		}
		for kind, qty := range vendor.Inventory {
			if qty <= 0 || !itemEasesNeed(w.ItemKinds[kind], need) {
				continue
			}
			recipe := w.Recipes[kind]
			if recipe == nil || recipe.RetailPrice <= 0 || recipe.RetailPrice <= a.Coins {
				return true
			}
		}
	}
	return false
}

// freeConsumableSourceExists reports whether any free public village object eases
// `need` on arrival and still has stock — the free-forage/well arm of resolvability,
// the sim-side echo of perception's gatherFreeSatiationSources (free public sources
// are common knowledge: every actor knows the town's wells/bushes). Owner-gated: an
// object owned by someone else is not free food for this actor, matching the
// perception scan's OwnedByOther skip. The immediate ease is -r.Amount (the arrival
// decrement) plus any dwell delta, the same magnitude objectRefreshMagnitude computes.
func freeConsumableSourceExists(w *World, a *Actor, need NeedKey) bool {
	for _, obj := range w.VillageObjects {
		if obj == nil || obj.OwnedByOther(a.ID) {
			continue
		}
		for _, refresh := range obj.Refreshes {
			if refresh == nil || refresh.Attribute != need {
				continue
			}
			if refresh.IsFinite() && refresh.AvailableQuantity != nil && *refresh.AvailableQuantity <= 0 {
				continue
			}
			mag := -refresh.Amount
			if refresh.DwellDelta != nil {
				mag += -*refresh.DwellDelta
			}
			if mag > 0 {
				return true
			}
		}
	}
	return false
}
