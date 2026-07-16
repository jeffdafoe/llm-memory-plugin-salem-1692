package sim

import (
	"context"
	"log"
	"time"
)

// cold.go — LLM-412. The cold need's driver: a per-minute exposure sweep that
// moves each actor's cold up or down by WHERE they are and WHAT the sky is
// doing. Cold is the registry's first externally-driven need (needs.go): the
// hourly IncrementNeedsTick never touches it, and no item satisfies it — it is
// relieved by a STATE (indoors, by a lit hearth), never by consuming.
//
// The exposure model, per minute:
//
//   - warm (inside a structure whose hearth is lit)  → recover fast
//   - storm, outdoors                                → accrue at full rate
//   - storm, outdoors, carrying a warm garment       → accrue slowly (a coat
//     or cloak is your roof — LLM-410; a PAID upgrade to keep working outside,
//     never a replacement for the free relief a roof already gives)
//   - storm, indoors with no live fire               → accrue slowly (a roof
//     is real relief — the free path — but not warmth)
//   - clear sky, not warm                            → recover slowly (the
//     chill fades once the weather passes; faster by a fire)
//
// Night presses harder: accrual (only) is scaled by ColdNightMultiplierX100
// during PhaseNight. All rates are ×100 per-minute settings knobs so they can
// be tuned live without floats. Fractional accrual is carried per actor in a
// TRANSIENT ×100 remainder (Actor.ColdCarryX100) — restart-loss of a fraction
// of a unit is harmless, the tiredness-recovery-cursor posture.
//
// The same sweep is the hearth keeper's storm wake: while a storm runs, an
// owner whose hearth is out/low is warranted to stoke it (HearthLowWarrantReason)
// — level-triggered and WarrantedSince-bounded like the need-threshold
// producer. This wake is load-bearing: indoor cold accrual is deliberately too
// slow to redline anyone inside a default 15-minute storm, so without it no
// fire would ever be lit in time to matter.

// ColdNeedKey is the registry key of the cold need (needs.go).
const ColdNeedKey NeedKey = "cold"

// CapabilityWarms tags an item_kind whose bearer is sheltered from the storm's
// cold while OUTDOORS — a coat or cloak (LLM-410). Data-driven on
// item_kind.capabilities, never a hardcoded kind set, so the clothing family
// stays operator-tunable via the item/set route. Holding at least one such
// garment caps outdoor storm accrual at ColdWarmGarmentPerMinuteX100 (see
// coldRatePerMinuteX100). Slice-2 models CARRYING one as relief — wear (a worn
// vs. packed distinction) is LLM-422.
const CapabilityWarms = "warms"

// Default WorldSettings knobs for cold exposure (LLM-412), ×100 per minute.
// At these rates: an actor caught outdoors in a storm goes red (16) in ~16
// minutes — one default storm — and peaks in ~24; the same storm reaches ~4
// through a roof (silent — shelter genuinely shelters, an hour-long storm is
// what chills an unheated room); a lit hearth clears a red chill in ~8
// minutes; and a passed storm's chill fades on its own inside the hour. Night
// multiplies accrual half again, so the night half of a storm redlines in ~11.
//
// A warm garment (LLM-410) caps outdoor storm accrual at the under-a-roof rate,
// so a coated worker outdoors chills like an unheated room — ~64 minutes to red,
// i.e. well past any default storm: the coat lets the work go on outside.
const (
	DefaultColdStormOutdoorsPerMinuteX100 = 100
	DefaultColdStormIndoorsPerMinuteX100  = 25
	// DefaultColdWarmGarmentPerMinuteX100 caps outdoor storm accrual for an actor
	// carrying a CapabilityWarms garment. Equal to the indoor (under-a-roof) rate —
	// "the coat is your roof": real outdoor relief, but a lit hearth still beats it
	// and going indoors/home is still free, so cold never loses a free relief path.
	DefaultColdWarmGarmentPerMinuteX100 = 25
	// DefaultColdThreadbareGarmentPerMinuteX100 caps outdoor storm accrual for an
	// actor whose best warms garment has worn THREADBARE (LLM-422). Between the
	// sound-garment rate (25) and the full unprotected outdoor rate (100): a worn
	// coat still turns some of the wind, but not like a fresh one — so replacing
	// it has real cold stakes, and the failing coat gets the bearer red (16) in
	// ~27 minutes outdoors rather than ~64. Min-only and pre-night like the
	// sound-garment cap.
	DefaultColdThreadbareGarmentPerMinuteX100 = 60
	DefaultColdNightMultiplierX100            = 150
	DefaultColdWarmRecoveryPerMinuteX100      = 200
	DefaultColdClearRecoveryPerMinuteX100     = 50

	// DefaultColdProduceSapPct scales a red-or-worse-cold producer's
	// production rate (produceRateScalePct) — miserable and productivity-
	// sapping, never lethal. 50 = half speed.
	DefaultColdProduceSapPct = 50
)

// actorIsWarm reports whether the actor stands inside a structure whose
// hearth fire is burning — the superior relief posture. The free relief
// (a roof alone) is the storm-indoors branch of the rate, not this.
func actorIsWarm(w *World, a *Actor, now time.Time) bool {
	return HearthLit(StructureHearth(w.VillageObjects, a.InsideStructureID), now)
}

// coldRatePerMinuteX100 returns the actor's current cold delta in ×100 units
// per minute — positive accruing, negative recovering, zero holding. The one
// place the exposure model lives: the sweep applies it, and
// actorActionableRedNeed reads its sign so a red-cold warrant is only stamped
// while cold is actively climbing (an actor already in relief posture is
// recovering on this same schedule — waking them would churn).
func coldRatePerMinuteX100(w *World, a *Actor, now time.Time) int {
	if actorIsWarm(w, a, now) {
		// max(0, …): a recovery rate is returned NEGATED, so a negative setting would
		// flip recovery into accrual (cold rising by a fire). LLM-439 clamps this at
		// load for the pg boot path; this runtime floor is the defense-in-depth twin
		// for any WorldSettings built another way (mem loader, test, a future
		// live-settings path). The accrual/garment branches keep their own g >= 0 guards.
		return -max(0, w.Settings.ColdWarmRecoveryPerMinuteX100)
	}
	if w.Environment.Weather == WeatherStorm {
		rate := w.Settings.ColdStormOutdoorsPerMinuteX100
		if a.InsideStructureID != "" {
			rate = w.Settings.ColdStormIndoorsPerMinuteX100
		}
		// A warm garment (LLM-410) caps storm accrual at the garment rate — "the
		// coat is your roof." OUTDOORS ONLY: indoors a roof already shelters, and the
		// coat is the roof's outdoor substitute (so a non-default indoor rate is never
		// lowered by a coat). A THREADBARE garment (worn into its last fraction,
		// LLM-422) caps at a WORSE rate — it still turns some wind, but the cloth is
		// thin — so replacing a failing coat has real cold stakes. Min-only (g < rate),
		// so a garment never raises accrual and is moot when the environmental rate is
		// already lower. g >= 0 ignores a misconfigured negative — a garment can't turn
		// a storm into active recovery; g == 0 makes a coat full outdoor relief (the
		// outdoors==0 off-switch posture). Both are PRE-night bases like the indoor roof
		// rate: the night multiplier below scales them exactly as it scales a roof, so a
		// coat stays equivalent to (a threadbare coat, worse than) a roof rather than
		// beating one. The free relief (a roof, a hearth, going home) is untouched — a
		// PAID upgrade to keep working outside.
		if a.InsideStructureID == "" {
			switch actorWarmGarmentTier(w, a) {
			case WarmGarmentSound:
				if g := w.Settings.ColdWarmGarmentPerMinuteX100; g >= 0 && g < rate {
					rate = g
				}
			case WarmGarmentThreadbare:
				if g := w.Settings.ColdThreadbareGarmentPerMinuteX100; g >= 0 && g < rate {
					rate = g
				}
			}
		}
		if w.Phase == PhaseNight && w.Settings.ColdNightMultiplierX100 > 0 {
			rate = rate * w.Settings.ColdNightMultiplierX100 / 100
		}
		return rate
	}
	// max(0, …): the clear-sky recovery twin of the warm-branch floor above — a
	// negative setting must not flip clear-sky recovery into accrual (LLM-439).
	return -max(0, w.Settings.ColdClearRecoveryPerMinuteX100)
}

// coldEligible mirrors the needs-tick eligibility filter: agent-backed or
// login-backed actors feel the weather; decoratives are scenery.
func coldEligible(a *Actor) bool {
	return a.LLMAgent != "" || a.LoginUsername != ""
}

// AdjustCold returns a Command that applies elapsedMinutes of cold exposure
// across all eligible actors and, while a storm runs, wakes hearth owners
// whose fire is out or low. Driven once a minute by RunColdTicker; the
// elapsed count is capped there so a stalled ticker resuming can't apply a
// backlog shock (the LLM-393 no-catch-up posture — the sweep just resumes).
func AdjustCold(elapsedMinutes int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if elapsedMinutes <= 0 {
				return 0, nil
			}
			now := time.Now().UTC()
			storm := w.Environment.Weather == WeatherStorm
			touched := 0
			for _, a := range w.Actors {
				if !coldEligible(a) {
					continue
				}
				rate := coldRatePerMinuteX100(w, a, now)
				if rate != 0 {
					if a.Needs == nil {
						a.Needs = make(map[NeedKey]int)
					}
					current := a.Needs[ColdNeedKey]
					// Nothing to recover and nothing accruing → skip the carry
					// math (the common clear-sky case: everyone at 0 stays 0).
					if rate > 0 || current > 0 {
						// Signed carry, applied in whole units. Go's integer
						// division truncates TOWARD ZERO, so the remainder always
						// keeps the carry's sign and |carry| stays < 100 after
						// every apply — a rate flipping sign mid-remainder just
						// works the leftover fraction off in the new direction,
						// never minting a spurious unit at the flip. Pinned by
						// TestAdjustCold_CarrySignFlip (code_review).
						a.ColdCarryX100 += rate * elapsedMinutes
						if units := a.ColdCarryX100 / 100; units != 0 {
							a.ColdCarryX100 -= units * 100
							a.Needs[ColdNeedKey] = ClampNeed(current + units)
						}
						touched++
					} else {
						a.ColdCarryX100 = 0
					}
				}

				// Storm wake for the fire-keeper (owner path; the hired twin is
				// stamped at startLaborWork). Level-triggered while the storm
				// runs and the fire wants wood; the WarrantedSince/TickInFlight
				// gate bounds re-stamps exactly like the need-threshold
				// producer. Agent NPCs only — PCs don't reactor-tick, visitors
				// run their own lifecycle.
				if storm &&
					(a.Kind == KindNPCStateful || a.Kind == KindNPCShared) &&
					a.VisitorState == nil && a.WarrantedSince == nil && !a.TickInFlight {
					if hearth := OwnedHearth(w.VillageObjects, a.ID); hearth != nil &&
						HearthNeedsStoking(hearth, now, w.Settings.HearthLowMinutes) {
						tryStampWarrant(w, a, WarrantMeta{
							TriggerActorID: a.ID,
							Reason:         HearthLowWarrantReason{HearthID: hearth.ID},
						}, now)
					}
				}
			}
			return touched, nil
		},
	}
}

// ColdTickerInterval is how often RunColdTicker wakes. One minute matches the
// tiredness-recovery cadence — fine-grained enough for storm-length dynamics,
// trivially cheap over the actor set.
const ColdTickerInterval = time.Minute

// RunColdTicker owns the cold-sweep goroutine. Wakes every ColdTickerInterval
// and applies exactly one minute of exposure — no catch-up across a stall
// (the sweep resumes rather than shock-applying a backlog, the same posture
// as the needs ticker's LLM-393 gap rule).
func RunColdTicker(ctx context.Context, w *World) {
	t := time.NewTicker(ColdTickerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.beatTicker("cold")
			if _, err := w.SendContext(ctx, AdjustCold(1)); err != nil && ctx.Err() == nil {
				log.Printf("sim/cold: exposure tick failed: %v", err)
			}
		}
	}
}
