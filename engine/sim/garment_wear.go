package sim

import (
	"context"
	"log"
	"time"
)

// garment_wear.go — LLM-422. Clothing wear: garments worn by a WORKING actor
// degrade and eventually need replacing, turning the LLM-410 clothing goods
// from a durable-forever one-time equip into a RECURRING market (the wholesale
// factor keeps a reason to come). The worked-MINUTE sibling of per-use tool
// durability (tool_wear.go / LLM-330): a tool's life is produce executions, a
// garment's is time worn under labor.
//
// The model, mirroring tool wear:
//   - A kind whose catalog def carries WearMinutes > 0 is a wearable garment.
//   - Wear is per KIND on the actor's IN-USE unit (Actor.GarmentWear): inventory
//     has no item instances (map[ItemKind]int), so diegetically the worker wears
//     one coat threadbare while the spares on the shelf stay fresh. A missing
//     entry means no unit has been taken up — all on-hand units are fresh.
//   - A per-minute sweep (RunGarmentWearTicker) draws GarmentWearPerMinute worked
//     minutes off the in-use unit of every garment a WORKING actor holds. At 0 the
//     unit is spent (inventory -1, delete-on-zero), the next use takes up a fresh
//     unit, and the count drop trips the untouched restock/rebuy loop — the
//     recurring demand this ticket exists to create.
//
// Who wears (actorWearsGarments): a worker or business person actively working —
// NOT the clothing STOCKHOLDERS (the distributor + the visiting factor), whose
// held garments are sale stock, not clothing they wear. That exclusion is what
// keeps a coat on a distributor's shelf from wearing out just because he stands
// his shift; inventory carries no personal-vs-stock split, so the wearer set is
// drawn on role + posture instead.
//
// Cold coupling: a warms garment (coat/cloak) that has worn threadbare warms
// LESS — actorWarmGarmentTier feeds coldRatePerMinuteX100 a worse outdoor-storm
// cap than a sound one, so replacing a failing coat has real mechanical stakes
// (LLM-412's live cold need), not just flavor.

// Default WorldSettings knobs for garment wear (LLM-422).
const (
	// DefaultGarmentWearPerMinute is the worked-minutes of wear a garment's
	// in-use unit takes per real minute its bearer spends in a working posture.
	// 1 = one worked minute wears one minute of budget; the budgets on the item
	// kinds (item_kind.wear_minutes) are calibrated in these units. 0 disables
	// garment wear entirely — the feature off-switch, mirroring StallWearPerCoin
	// and cold's outdoors off-switch.
	DefaultGarmentWearPerMinute = 1

	// DefaultGarmentThreadbareFractionX100 is the fraction (×100, so 20 = 20%) of
	// a garment's budget below which its in-use unit reads THREADBARE — worn thin
	// enough that a warms garment stops warming fully (actorWarmGarmentTier) and
	// the cold self-line nudges toward a replacement. The last fifth of a coat's
	// life is when the wind starts getting through.
	DefaultGarmentThreadbareFractionX100 = 20
)

// WarmGarmentTier grades the cold relief an actor's best warms garment provides,
// accounting for wear (LLM-422). A sound coat is full relief; a threadbare one
// helps but the wind gets through; none is no relief. Ordered so a higher tier
// is better relief (the resolver keeps the best across an actor's warms kinds).
type WarmGarmentTier int

const (
	WarmGarmentNone       WarmGarmentTier = iota // no warms garment held
	WarmGarmentThreadbare                        // only a worn-thin warms garment (last fraction of its budget)
	WarmGarmentSound                             // a sound warms garment — a fresh spare or an in-use unit above the threadbare line
)

// GarmentWearMinutes resolves kind's per-unit worked-minute budget from the
// catalog: > 0 marks the kind a wearable garment lasting that many worked
// minutes. 0 for a durable-forever good, a kind absent from the catalog, or a
// nil map. The worked-minute sibling of DurableToolUses. Exported so perception
// (over snap.ItemKinds) and the wear sweep (over w.ItemKinds) share one
// definition of "is this a wearable garment".
func GarmentWearMinutes(kinds map[ItemKind]*ItemKindDef, kind ItemKind) int {
	if def := kinds[kind]; def != nil && def.WearMinutes > 0 {
		return def.WearMinutes
	}
	return 0
}

// garmentUnitThreadbare reports whether an in-use garment unit with `remaining`
// worked minutes left of a `budget` is threadbare — worn into the last
// fractionX100 percent of its life. A missing/zero remaining means a fresh unit
// (no wear entry yet — full budget), never threadbare; a remaining above budget
// (an operator retuned the budget down live) is likewise treated as fresh, the
// applyGarmentWear clamp posture. budget <= 0 (not a garment) is never
// threadbare.
func garmentUnitThreadbare(budget, remaining, fractionX100 int) bool {
	if budget <= 0 || fractionX100 <= 0 {
		return false
	}
	if remaining <= 0 || remaining > budget {
		remaining = budget
	}
	return remaining*100 < budget*fractionX100
}

// ResolveWarmGarmentTier grades the cold relief the actor's warms garments give,
// accounting for wear (LLM-422). The best tier across every warms kind held wins:
// a kind is SOUND if there's a fresh spare (qty >= 2 — only the in-use unit wears)
// or the single in-use unit is above the threadbare line; THREADBARE if held but
// only as worn-thin single units; NONE if no warms garment at all. Pure over the
// three maps + the threshold fraction so the live sweep (actorWarmGarmentTier)
// and perception (actorSnapWarmGarmentTier) resolve the SAME tier from the same
// inputs — the shared-predicate posture (sim.AtBusiness).
func ResolveWarmGarmentTier(kinds map[ItemKind]*ItemKindDef, inventory, garmentWear map[ItemKind]int, thresholdFractionX100 int) WarmGarmentTier {
	best := WarmGarmentNone
	for kind, qty := range inventory {
		if qty <= 0 {
			continue
		}
		def := kinds[kind]
		if def == nil || !def.HasCapability(CapabilityWarms) {
			continue
		}
		// Holding any warms garment is at least threadbare-tier relief.
		if best < WarmGarmentThreadbare {
			best = WarmGarmentThreadbare
		}
		// Sound if a fresh spare exists (qty >= 2) or the in-use unit is still
		// above the threadbare line (or unworn — no wear entry). Sound is the top
		// tier, so the first sound kind short-circuits.
		if qty >= 2 || !garmentUnitThreadbare(def.WearMinutes, garmentWear[kind], thresholdFractionX100) {
			return WarmGarmentSound
		}
	}
	return best
}

// actorWarmGarmentTier grades the live actor's warms-garment cold relief over the
// world catalog + settings — the sim-side read behind coldRatePerMinuteX100's
// tiered outdoor-storm cap (LLM-422).
func actorWarmGarmentTier(w *World, a *Actor) WarmGarmentTier {
	return ResolveWarmGarmentTier(w.ItemKinds, a.Inventory, a.GarmentWear, w.Settings.GarmentThreadbareFractionX100)
}

// garmentWearResult reports one sweep's wear on one garment kind, for the sweep's
// telemetry (touched count) and tests. No player-facing narration: unlike a
// produce tool result, garment wear happens in the background of a work sweep,
// so its escalation surfaces through perception's threadbare cue, not a per-tick
// message.
type garmentWearResult struct {
	Item        ItemKind
	MinutesLeft int  // worked minutes remaining on the in-use unit after this draw
	Spent       bool // this draw used up at least one unit (inventory already decremented)
	OnHand      int  // units still in inventory after any spend (includes the in-use one)
}

// applyGarmentWear draws `minutes` of worked wear off the actor's in-use unit of
// a garment kind (the worked-minute sibling of applyToolWear). A missing/zero
// wear entry takes up a fresh unit at full budget first (an entry above budget
// clamps — the live-retune-down case). Each time the in-use unit reaches 0 it is
// spent (inventory -1, delete-on-zero) and the next unit is taken up; wear stops
// once the bearer runs out of the garment (you can't wear what you no longer
// hold — the leftover wear is lost, not banked). budget must be > 0 (the caller
// gates on GarmentWearMinutes); minutes > 0.
func applyGarmentWear(a *Actor, kind ItemKind, budget, minutes int) garmentWearResult {
	onHand := a.Inventory[kind]
	if onHand <= 0 || budget <= 0 || minutes <= 0 {
		return garmentWearResult{Item: kind, MinutesLeft: a.GarmentWear[kind], OnHand: onHand}
	}
	if a.GarmentWear == nil {
		a.GarmentWear = make(map[ItemKind]int)
	}
	wear := a.GarmentWear[kind]
	if wear <= 0 || wear > budget {
		wear = budget // fresh in-use unit (or clamp a live-retune-down)
	}
	spent := 0
	for minutes > 0 {
		if minutes < wear {
			wear -= minutes
			break
		}
		// minutes >= wear: the in-use unit is worn through.
		minutes -= wear
		spent++
		if spent >= onHand {
			// Out of this garment — the last unit is gone; drop any leftover wear.
			wear = 0
			break
		}
		wear = budget // take up the next unit
	}
	if spent > 0 {
		a.Inventory[kind] -= spent
		if a.Inventory[kind] <= 0 {
			delete(a.Inventory, kind)
		}
	}
	// Persist a partially-worn in-use unit; drop the entry when it's fresh
	// (wear == budget → canonical "no entry = fresh"), spent out, or unbacked.
	if wear > 0 && wear < budget && a.Inventory[kind] > 0 {
		a.GarmentWear[kind] = wear
	} else {
		delete(a.GarmentWear, kind)
	}
	return garmentWearResult{Item: kind, MinutesLeft: a.GarmentWear[kind], Spent: spent > 0, OnHand: a.Inventory[kind]}
}

// actorWearsGarments reports whether this actor wears (and so degrades) the
// garments they carry right now — the eligibility gate for the wear sweep.
//
// Two conditions, both required:
//   - NOT a clothing STOCKHOLDER. The distributor and the visiting factor hold
//     garments as sale stock, not clothing they wear; inventory carries no
//     personal-vs-stock split, so their stock would otherwise wear out from them
//     standing a shift. They are the only clothing sellers (the factor deals only
//     with the distributor; everyone else buys from the distributor), so excluding
//     exactly these two keeps all sale stock pristine — resolved by existing role
//     predicates, no hardcoded ids.
//   - In a WORKING posture. Occupation/activity drives wear (Jeff): on shift at a
//     workplace (StateWorking — the business-person case), fulfilling a hired labor
//     stint (StateLaboring), mid a production cycle, or mid a timed source activity.
//     An idle, commuting, conversing, shopping, resting, or sleeping actor wears
//     nothing.
//
// The one uncovered corner (documented, not a current case): an actor who BOTH
// holds clothing as sale stock AND does exertion work would wear a unit of stock.
// No live actor does both — the distributor is pure commerce and is excluded here
// regardless. The stall-wear "one wearable business per owner" convention has the
// same shape.
func actorWearsGarments(w *World, a *Actor) bool {
	if a == nil {
		return false
	}
	if ActorHasTradeErrand(a) || ActorIsDistributor(w.VillageObjects, a.WorkStructureID) {
		return false
	}
	return a.State == StateWorking ||
		a.State == StateLaboring ||
		a.ProductionActivity != nil ||
		a.SourceActivity != nil
}

// WearGarments returns a Command that applies elapsedMinutes of garment wear to
// every working actor's in-use garment units. Driven once a minute by
// RunGarmentWearTicker; the elapsed count is capped there so a stalled ticker
// resuming can't shock-apply a backlog (the cold/needs no-catch-up posture — the
// sweep just resumes). Returns the number of actors whose garments moved, for
// the ticker telemetry.
func WearGarments(elapsedMinutes int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if elapsedMinutes <= 0 {
				return 0, nil
			}
			per := w.Settings.GarmentWearPerMinute
			if per <= 0 {
				return 0, nil // wear disabled (off-switch)
			}
			draw := per * elapsedMinutes
			touched := 0
			for _, a := range w.Actors {
				if !actorWearsGarments(w, a) {
					continue
				}
				worn := false
				// Deleting the current key mid-range is legal in Go, and
				// applyGarmentWear only ever deletes the kind it is passed, so
				// this range is safe.
				for kind, qty := range a.Inventory {
					if qty <= 0 {
						continue
					}
					budget := GarmentWearMinutes(w.ItemKinds, kind)
					if budget <= 0 {
						continue // not a wearable garment
					}
					applyGarmentWear(a, kind, budget, draw)
					worn = true
				}
				if worn {
					touched++
				}
			}
			return touched, nil
		},
	}
}

// GarmentWearTickerInterval is how often RunGarmentWearTicker wakes. One minute
// matches the cold/tiredness cadence — fine enough for the slow economic
// pressure of clothing wearing out, trivially cheap over the actor set.
const GarmentWearTickerInterval = time.Minute

// RunGarmentWearTicker owns the garment-wear sweep goroutine. Wakes every
// GarmentWearTickerInterval and applies exactly one minute of wear — no catch-up
// across a stall (the sweep resumes rather than shock-applying a backlog, the
// cold ticker's LLM-393 posture).
func RunGarmentWearTicker(ctx context.Context, w *World) {
	t := time.NewTicker(GarmentWearTickerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.beatTicker("garment_wear")
			if _, err := w.SendContext(ctx, WearGarments(1)); err != nil && ctx.Err() == nil {
				log.Printf("sim/garment_wear: wear tick failed: %v", err)
			}
		}
	}
}
