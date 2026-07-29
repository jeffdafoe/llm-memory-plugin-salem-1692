package sim

import (
	"math"
	"time"
)

// town_rate.go — LLM-557. The constable's income: every owned business owes him a
// coin a day, collected when he calls there on his rounds.
//
// He is the one villager with no trade of his own — no production, no restock
// policy, and a workplace (the Meeting House) that is not a shop. Seeded with a
// purse he spent down, he fell out of the market entirely: by his fifth day he had
// stopped buying and was living on foraged berries and well water. The rate puts
// him back in the shops, which is the whole point of a constable.
//
// A LEVY, not a stipend, and so coin-neutral: the village's only faucet is visitor
// injection (~10 coins/day net), and minting a comparable wage for one NPC would
// have been most of a second faucet. What he collects he spends straight back at
// the inn and the tavern — the LLM-83 circulation lever, the same motivation as the
// farm-upkeep levy.
//
// Modelled on farm_upkeep.go (LLM-215), whose central choice is the one that
// matters: THE ENGINE NEVER MOVES THE COIN. The daily pass only accrues an
// obligation; the coin moves when the keeper hands it over through the ordinary pay
// command, and settleTownRate draws the obligation down by what actually changed
// hands. That pattern is proven live — the farm-upkeep cue has Elizabeth Ellis
// buying a shovel from the smith nearly every day without anything forcing her.
//
// Where it differs from farm upkeep: that obligation is STOCK-based (a pure function
// of coins held, re-derived every build, needing no stored state). A daily rate
// cannot be derived from a stock — "have you paid today" is a record — so this one
// carries a per-object accumulator, VillageObject.RateOwed, in the shape of
// stall wear's Wear (LLM-118).
//
// Seams: assessTownRate fires once per game-day from checkAndRotate
// (world_rotation.go), beside ApplyFarmUpkeep on the same durable LastRotationAt
// boundary; settleTownRate hangs off the bare-pay transfer (pay_commands.go); the
// cues are perception/town_rate.go.

const (
	// DefaultTownRateCoinsPerDay is what one business owes the constable per
	// game-day. Sized against his own demonstrated burn — ale 1, porridge 2, stew
	// 4 — across the eight owned businesses: 8 coins/day is subsistence with a
	// little slack, deliberately NOT a comfortable wage. He should still forage
	// and barter sometimes; hauling well water to trade for a bowl of porridge is
	// good village behaviour and should not be priced out of existence.
	// A non-positive value disables the levy (the per-feature off-switch,
	// mirroring StallWearPerCoin==0 and FarmUpkeepCoinsPerShovel==0).
	DefaultTownRateCoinsPerDay = 1

	// DefaultTownRateMaxOwed caps what one business can accrue, so a keeper the
	// constable cannot catch never faces a shock bill.
	//
	// This is not hypothetical: the Tavern opens at 3 PM and the constable's watch
	// is 8am–6pm, so John Ellis is behind a shut door for most of every circuit and
	// the enter-vs-loiter rule (correctly) leaves the constable at the doorstep
	// without collecting. Uncapped, his debt would climb without limit and land as
	// an absurd demand the first time the two ever met on shift. Capped, it
	// plateaus at a few days and the arrears stay a believable "you're behind on
	// the rate" rather than a tax bill.
	DefaultTownRateMaxOwed = 3
)

// IsRateableBusiness reports whether obj owes the town rate: an OWNED business.
// Nil-safe.
//
// Deliberately the same gate as IsWearableStall (owned + TagBusiness) rather than a
// new scope of its own — the levy falls on exactly the set of places that are
// somebody's shop, which is also the set the constable's rounds call at
// (buildConstableRoundsCandidates takes every TagBusiness object). The owner
// requirement is what makes the obligation collectable: an unowned business has
// nobody to hand over the coin.
func IsRateableBusiness(obj *VillageObject) bool {
	return obj != nil && obj.OwnerActorID != "" && obj.HasTag(TagBusiness)
}

// RateableBusinessOf returns the rateable business owned by ownerID, or nil when
// they own none. Takes the object map so it serves both the live World
// (w.VillageObjects) and a perception Snapshot (snap.VillageObjects).
//
// Picks the LOWEST VillageObjectID rather than the map-iteration first, so the result
// is deterministic even if the one-business-per-owner data convention (shared with
// OwnedWearableStall / OwnedFarm) is ever broken by a live re-tag or a bad seed.
//
// Determinism is load-bearing HERE in a way it is not for its siblings, and this is
// the difference worth understanding before touching it. Stall wear and farm upkeep
// only ever ACCRUE against the object they resolve, so a wobble picks a different
// object to wear down and nothing contradicts itself. The town rate also SETTLES: the
// perception cue names a business to the keeper and settleTownRate decrements one when
// he pays. Resolved independently under map order, those two can disagree — the cue
// says the General Store owes three coins, the payment clears the arrears on his other
// shop, and the cue then repeats itself forever with the coin already handed over.
// Same reasoning as WearableStallToMend's lowest-LaborID tie-break, which exists
// because the cue, the repair and the sweep must all resolve the same stall.
func RateableBusinessOf(objects map[VillageObjectID]*VillageObject, ownerID ActorID) *VillageObject {
	if ownerID == "" {
		return nil
	}
	var best *VillageObject
	for _, obj := range objects {
		// nil-safe: also runs over hand-built perception/test maps where a stray
		// nil entry must not panic the world (the OwnedHearth / OwnedFarm guard).
		if obj == nil || obj.OwnerActorID != ownerID || !IsRateableBusiness(obj) {
			continue
		}
		if best == nil || obj.ID < best.ID {
			best = obj
		}
	}
	return best
}

// ActorIsConstable reports whether a carries the constable attribute — the
// collector side of the levy. The engine keys behaviour on attribute PRESENCE only
// (the value is unused), matching findActorsWithAttribute in the rounds driver.
// Nil-safe.
func ActorIsConstable(a *Actor) bool {
	if a == nil {
		return false
	}
	_, ok := a.Attributes[AttrConstable]
	return ok
}

// TownRateAccrual returns what a business's owed balance becomes after one daily
// assessment: owed + perDay, capped at maxOwed. Pure, so the assessment and any
// caller reasoning about the ceiling read the same rule.
//
// A non-positive perDay disables the levy and leaves the balance untouched — it
// does NOT zero an existing debt, so switching the feature off mid-arrears freezes
// what is owed rather than forgiving it, and switching it back on resumes from
// there. A non-positive maxOwed means uncapped. An existing balance already at or
// above the cap is clamped DOWN to it, so lowering the knob live takes effect on
// the next assessment instead of stranding balances above the new ceiling.
//
// The addition SATURATES rather than wrapping. Each knob is bounded at MaxInt32 by
// the setter, but maxOwed==0 is uncapped, so a village left running uncapped for long
// enough would eventually overflow — and a wrapped negative balance is the worst
// possible failure here, because it reads as "nothing owed" everywhere (the cue goes
// silent, settleTownRate returns early) and the levy dies quietly with no error.
func TownRateAccrual(owed, perDay, maxOwed int) int {
	if perDay <= 0 {
		return owed
	}
	next := math.MaxInt
	if owed <= math.MaxInt-perDay {
		next = owed + perDay
	}
	if maxOwed > 0 && next > maxOwed {
		return maxOwed
	}
	return next
}

// assessTownRate runs one daily pass over every owned business, adding the day's
// rate to what it owes the constable (capped). It moves NO coin — that happens when
// the keeper actually pays, at settleTownRate — and stamps no warrant.
//
// No wake, deliberately, unlike the farm-upkeep pass it otherwise mirrors. A farm
// owner is woken because the obligation sends him on an errand: he must walk to the
// smith and buy shovels. A keeper owes the constable nothing but a coin handed over
// when the man is standing in front of him — there is nowhere to go and nothing to
// do until then, so a daily wake would be pure nagging on a village whose stale-wake
// ledger already backs off ambient repeats. The keeper's cue is co-location-gated
// instead (perception/town_rate.go), and the collector already has a recurring wake
// of his own in the rounds interval.
//
// Called on the world goroutine from checkAndRotate, so the mutations are
// serialized with every other world write.
func assessTownRate(w *World, now time.Time) {
	if w == nil || w.Settings.TownRateCoinsPerDay <= 0 {
		return
	}
	// Nobody to collect it: with no constable in the village the levy would pile up
	// against a debt that can never be settled, and every keeper would carry a cue
	// naming a man who does not exist. Skipping the whole pass keeps the feature
	// dormant until a constable is appointed, and freezes (rather than forgives)
	// any balance outstanding when the last one goes.
	if !anyConstable(w) {
		return
	}
	perDay := w.Settings.TownRateCoinsPerDay
	maxOwed := w.Settings.TownRateMaxOwed
	for _, obj := range w.VillageObjects {
		if !IsRateableBusiness(obj) {
			continue
		}
		obj.RateOwed = TownRateAccrual(obj.RateOwed, perDay, maxOwed)
	}
	_ = now // the pass is keyed to the caller's daily boundary; no per-object stamp
}

// anyConstable reports whether the village currently has at least one constable to
// collect the rate.
func anyConstable(w *World) bool {
	if w == nil {
		return false
	}
	for _, a := range w.Actors {
		if ActorIsConstable(a) {
			return true
		}
	}
	return false
}

// ApplyTownRate wraps the daily assessment as a Command so the rotation driver can
// run it on the world goroutine. Mirrors ApplyFarmUpkeep / ApplyDailyRotation.
func ApplyTownRate(now time.Time) Command {
	return Command{Fn: func(w *World) (any, error) {
		assessTownRate(w, now)
		return nil, nil
	}}
}

// settleTownRate draws a business's rate debt down by coin its owner has just handed
// to a constable. Called from the bare-pay transfer once the coins have moved, the
// same inline placement accrueStallWear takes on the sale path — so the obligation
// and the coin change together and no reconciliation sweep is needed.
//
// # The settlement policy, stated as an invariant
//
// ANY bare coin payment from a business owner to a constable is applied to that
// business's town-rate arrears, whatever the payment was for.
//
// This is a deliberate product decision and it is deliberately OVER-broad; it is
// stated here rather than left implicit because it changes what an existing generic
// payment operation means (code_review, LLM-557). Consequences, all intended:
//
//   - A gift, a loan, a wage, or a repayment from a keeper to a constable clears
//     that keeper's arrears as a side effect. Villagers really do make unprompted
//     bare-coin gifts, so this is a live path, not a theoretical one.
//   - A payment made for one purpose can therefore discharge a different liability.
//   - Nothing is minted or destroyed either way: the constable ends up holding at
//     least what the rate asked for.
//
// Why not a structured purpose on the pay call: the settlement would then depend on
// the model emitting a marker correctly, and a model that omits it leaves the levy
// unsettled, the cue nagging forever, and the constable broke — which is the exact
// state this mechanism exists to fix. Over-broad settling costs bookkeeping fidelity;
// under-broad settling costs the mechanism. Given that trade, the loose direction is
// the safe one.
//
// What does NOT reach here: buying goods from a constable settles through
// pay_with_item / the quote flow, which is untouched. Only bare coin payments settle
// the rate.
//
// No-op unless the payer owns a business that owes something and the payee is a
// constable. Nil-safe on both actors.
func settleTownRate(w *World, payer, payee *Actor, amount int) {
	if w == nil || payer == nil || payee == nil || amount <= 0 {
		return
	}
	if !ActorIsConstable(payee) {
		return
	}
	business := RateableBusinessOf(w.VillageObjects, payer.ID)
	if business == nil || business.RateOwed <= 0 {
		return
	}
	if amount >= business.RateOwed {
		business.RateOwed = 0
		return
	}
	business.RateOwed -= amount
}
