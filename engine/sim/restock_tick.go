package sim

import (
	"context"
	"log"
	"time"
)

// restock_tick.go — tick-driver producer (ZBBS-WORK-322): the reorder producer.
// The missing half of the two-sided economy. produce_tick.go refills actors that
// MAKE their stock (`produce` RestockEntries); resellers that BUY their stock
// (`buy` RestockEntries) deplete through sales (pay_with_item) and, without this
// producer, never restock — any shop stocking bought goods empties over game-
// days. v1 had buy_walker.go (an engine-forced walker); v2 reserved the substrate
// (StateShopping, RestockSourceBuy) but never built the executor.
//
// LLM-90 folds the `forage` source into the same producer: a grower-seller whose
// own harvested sell-stock runs low (Prudence Ward's empty berry shelf) is woken
// the same way, and the "## Your bushes to harvest" cue (perception/forage.go)
// renders on that tick instead of "## Restocking". One ticker, one warrant kind,
// one eligibility gate — the warrant's Source field is the only thing that
// distinguishes the two so the right section is pointed at.
//
// LLM-DECIDED, not engine-forced (Jeff's call). This producer does NOT walk the
// reseller or force a purchase. It mirrors the need→satiation pattern: the
// engine surfaces the OPPORTUNITY (a warrant brings the reseller to a reactor
// tick; the "## Restocking" perception section names the low items, a supplier,
// and the supplier's structure_id), and the reseller's own LLM decides whether,
// what, and how much to restock — acting via the existing move_to +
// pay_with_item tools, picking its own quantity. No new commit tool.
//
// LEVEL-TRIGGERED, like the shift/duty producer: each per-minute scan re-checks
// the standing condition (any `buy` entry below the reorder threshold), so the
// warrant keeps re-firing until the reseller actually restocks or the operator
// disables the producer. The per-actor WarrantedSince / TickInFlight gate keeps
// it from double-stamping an already-pending or mid-tick reseller.
//
// REORDER THRESHOLD: a `buy` entry is "low" when its on-hand quantity is below
// cap * RestockReorderPct / 100 (default 25%) — strictly below that fraction,
// except a sub-one-unit fraction rounds up so a small cap reorders at its last
// unit rather than only when empty (see RestockReorderThresholdMet). cap==0 (no
// cap configured) is skipped — there's no fraction to take. RestockReorderPct==0
// disables the producer entirely (the operator off-switch).
//
// PRODUCE-INPUT BATCH FLOOR (LLM-279): a cap fraction is the wrong geometry for a
// good the actor consumes to PRODUCE — a recipe draws a whole batch of the input
// at once, so a fraction the batch draw steps clean over never triggers, and a
// producer either stalls every cycle (fires only at 0, after production already
// halted) or deadlocks outright (stock knocked off the batch multiple sits above
// the fraction forever, unable to produce, never reordered). So for a `buy` entry
// that is also a required input to one of the actor's produce recipes, the
// threshold is floored at RestockInputBatchBuffer batches of that input
// (ReorderFloors), reordering while one whole batch still remains to feed
// production across the supplier trip. Pure-resale goods keep the cap fraction.
//
// SUPPRESSION mirrors the other producers: PCs and transient visitors are out
// of scope (PCs are player-driven; visitors run their own ExpiresAt lifecycle),
// decoratives carry no real restock intent (they're not agent-backed), and
// sleeping / on-break resellers are left alone (the same rest suppressor the
// reactor and the #1/#2 producers use). A reseller already walking (a live
// MoveIntent) is also left to arrive rather than re-warranted mid-walk — unlike
// the other producers, restock sends the actor on a multi-minute trip to a
// remote supplier, so a per-minute re-stamp would thrash that trip
// (see restockEligible, ZBBS-HOME-386).

// DefaultRestockReorderPct is the reorder threshold as a whole percent of an
// entry's cap. A `buy` entry below this fraction of its cap warrants a restock
// tick. 25 == restock when on-hand drops under a quarter of cap.
const DefaultRestockReorderPct = 25

// RestockWarrantReason is the WarrantReason stamped when an agent-backed
// reseller holds a reorderable RestockEntry below the reorder threshold (ZBBS-
// WORK-322; forage source added LLM-90). Item is the first low item found
// (deterministic by RestockEntry order) — carried for telemetry / admin replay
// and to render the warrant cue line; the deliberation reads the FULL low-stock
// set + suppliers from the perception section, not this single field. Source is
// the supply mode of that low item — `buy` (restock by purchasing → the
// "## Restocking" section) or `forage` (restock by harvesting one's own bushes →
// the "## Your bushes to harvest" section, LLM-90); it routes the warrant cue
// line to the matching section and distinguishes the two in telemetry while
// keeping a single WarrantKindRestock / wake path. Zero-sourced (a stock level
// is not an event), so DedupDiscriminator returns 0 and the substrate's
// source-key dedup paths are bypassed — the per-actor WarrantedSince gate in the
// producer is what prevents double-stamp. Mirrors NeedThresholdWarrantReason /
// ShiftDutyWarrantReason — the other condition-driven, zero-sourced reasons.
type RestockWarrantReason struct {
	Item   ItemKind
	Source RestockSource
}

func (RestockWarrantReason) isWarrantReason()           {}
func (RestockWarrantReason) Kind() WarrantKind          { return WarrantKindRestock }
func (RestockWarrantReason) DedupDiscriminator() uint64 { return 0 }

// RestockReorderThresholdMet reports whether currentQty is low enough to warrant
// a reorder for an entry with the given cap, at the given percent, given an
// optional per-item batch floor (reorderFloor, 0 for goods that are not a
// produce-recipe input — see ReorderFloors). Shared by the producer and the
// perception gates so the warrant and the "## Restocking" / "## Keeping up
// production" sections can never disagree on what counts as "low".
//
// pct <= 0 is the operator off-switch and yields false unconditionally — the
// batch floor is disabled with the rest of the feature (LLM-279).
//
// A produce-recipe input reorders on batch coverage, not a cap fraction: when
// reorderFloor > 0 the entry is low as soon as currentQty drops below it. The
// floor is RestockInputBatchBuffer (2) batches of the input, so the reorder fires
// while one whole batch still remains to feed production across the multi-minute
// supplier trip — the shelf never stalls mid-trip, and stock knocked off the batch
// multiple can't strand above the fraction forever. The floor fires independently
// of cap, so an explicit `buy` input authored without a cap still reorders on
// batch coverage.
//
// Otherwise (pure-resale goods, forage/gather stock, elective boosters — all
// reorderFloor 0) the test is strictly below the cap*pct/100 fraction, in integer
// cross-multiplied form (currentQty*100 < cap*pct) to avoid float rounding at the
// boundary. But when that fraction is below one whole unit (cap*pct < 100, e.g. a
// skillet cap of 2 at 25% = 0.5) strict-below floors the trigger to "only when
// empty" — the reseller never rebuys until it is completely out. In that case the
// fraction rounds up to one unit and the reorder fires when down to the last unit
// (currentQty <= 1), so a small cap still gets a proactive trigger. Caps large
// enough that cap*pct >= 100 are unaffected; the int64 widening keeps a
// pathological cap/pct from a corrupt config or import from overflowing.
func RestockReorderThresholdMet(currentQty, cap, pct, reorderFloor int) bool {
	if pct <= 0 {
		return false // producer/feature disabled — the off-switch dominates, batch floor included
	}
	if reorderFloor > 0 && currentQty < reorderFloor {
		return true // produce-recipe input below its batch buffer (LLM-279)
	}
	if cap <= 0 {
		return false // no cap and no batch floor — nothing to reorder against
	}
	if int64(cap)*int64(pct) < 100 {
		return currentQty <= 1
	}
	return int64(currentQty)*100 < int64(cap)*int64(pct)
}

// firstActionableLowEntry returns the first reorderable RestockEntry below the
// threshold for which an actionable restock cue will actually render this tick,
// that entry's supply source, and whether one was found. It spans both engine-
// surfaced reorder modes — `buy` ("## Restocking") and `forage` ("## Your bushes
// to harvest", LLM-90); `produce` is excluded (produce_tick refills it on its own
// cadence). Buy entries are checked first so a buy-side reseller's representative
// Item is unchanged from before forage existed.
//
// The buy side spans the EFFECTIVE demand (LLM-260): explicit `buy` entries plus
// the ones derived from the actor's produce recipes' unsourced inputs — the same
// EffectiveBuyEntries set buildRestocking works from.
//
// BOTH sources carry an actionability gate mirroring their cue, because a
// high-information restock warrant (WarrantKindRestock bypasses noop-skip) that
// points at a section which declines to render is a wake loop — the actor is
// woken every scan for nothing. Forage warrants only when the grower remembers a
// still-owned forage bush for the item (actorRemembersForageSource, the
// buildForage precondition). Buy warrants only when actorHasBuyPath holds — a
// co-present seller or a surviving walk-to supplier, the buildRestocking
// LLM-216 item gate. (Before LLM-260 the buy side had no gate: the comment here
// claimed "## Restocking" renders for any low buy item, which LLM-216 had made
// false — a low item nobody sells warranted every 60s while the cue rendered
// nothing. Derived demand for a vendor-less input (water, today) would have
// mass-produced that loop.) Order within each source follows the entry order
// (first wins).
func firstActionableLowEntry(a *Actor, w *World, pct int, now time.Time, conserving bool) (RestockEntry, RestockSource, bool) {
	policy := a.RestockPolicy
	if policy == nil {
		return RestockEntry{}, "", false
	}
	// Batch floors for the actor's produce inputs (LLM-279) — 0 for any item that
	// is not a required recipe input, so forage entries below fall through to the
	// cap-fraction rule unchanged. Same catalog the perception gates read.
	floors := ReorderFloors(w.Recipes, policy)
	// LLM-298: a conserving keeper (coin-poor + overstocked) is told to hold off buying
	// and sell down — the "## Restocking" section flips to that steer, so waking it to
	// BUY a low input contradicts the cue and just re-fires every minute for a keeper
	// with correctly nothing to do (the live John Ellis carrots nag, ~5×/hr). Skip the
	// buy entries entirely while conserving. Forage entries still wake below: harvesting
	// one's own bushes costs no coin, so the coin gate does not apply to them.
	if !conserving {
		for _, e := range EffectiveBuyEntries(w.Recipes, policy) {
			if RestockReorderThresholdMet(a.Inventory[e.Item], e.Cap(), pct, floors[e.Item]) && actorHasBuyPath(w, a, e.Item, now) {
				return e, RestockSourceBuy, true
			}
		}
	}
	for _, e := range policy.ForageEntries() {
		if RestockReorderThresholdMet(a.Inventory[e.Item], e.Cap(), pct, 0) && actorRemembersForageSource(a, w, e.Item) {
			return e, RestockSourceForage, true
		}
	}
	return RestockEntry{}, "", false
}

// actorHasBuyPath reports whether at least one actionable buy path for item
// exists for the actor right now — the warrant-side mirror of what makes
// buildRestocking (perception/restock.go) actually render an item line, so the
// buy warrant and the "## Restocking" section can never disagree on
// actionability (the same lockstep discipline the forage side has via
// actorRemembersForageSource). A path is:
//
//   - a CO-PRESENT seller: a qualifying vendor of the item sharing the actor's
//     current huddle (the cue's buy-here imperative, ZBBS-HOME-388) — actionable
//     this very tick regardless of the walk-to drops below; or
//   - a SURVIVING walk-to supplier: a qualifying vendor whose workplace the
//     actor does not remember finding shut, and which the actor has the MEANS to
//     pay — coin that covers the remembered price, coin with no price yet on
//     record, or, failing coin, goods to barter (buyerCanTransact, LLM-406).
//     These are the drops findItemVendors applies. A BATCH-PINNED buyer (mid-
//     production-batch at post — the batch only advances while the actor is
//     there, LLM-319) has NO walk-to path: the trip would stall the batch, so
//     the actor correctly answers done() on every wake (the live John-Ellis
//     sage case — 8 unanswerable wakes across a bread batch, bounded but not
//     eliminated by the LLM-233 decay). Mirrors the LLM-335 mid-batch leisure
//     suppression and the ZBBS-HOME-386 mid-walk gate: don't wake an actor the
//     cue's own imperative can't move. Nothing is lost — the batch-landing
//     wake (ProductionDoneWarrantReason) and the level-triggered per-minute
//     scan re-pose the reorder the moment the batch lands, the boundary where
//     the trip actually happens. A co-present seller still counts while
//     pinned (pay_with_item resolves without leaving post). The perception
//     cue is deliberately NOT gated the same way: warrant ⊂ cue is the safe
//     direction of the lockstep discipline (the wake-loop hazard is a warrant
//     whose cue declines to render, not extra information on a tick driven by
//     another wake).
//
// "Qualifying vendor" mirrors the shared structural-vendorship scan
// (perception/consumable_vendors.go eachVendorOffer) + the LLM-252 supplier
// gate: a non-PC actor with a resolvable workplace holding qty>0, not wholesale-
// gated for this buyer (LLM-223/252), and supplying the item at FIRST HAND
// (ProducesOrForages) or via the distributor — never a fellow reseller's retail
// stock. Runs on the world goroutine over live state; perception runs the same
// tests over the snapshot.
func actorHasBuyPath(w *World, a *Actor, item ItemKind, now time.Time) bool {
	buyerIsDistributor := ActorIsDistributor(w.VillageObjects, a.WorkStructureID)
	pinned := actorBatchPinnedAtPost(a)
	for vendorID, vendor := range w.Actors {
		if vendor == nil || vendorID == a.ID || vendor.Kind == KindPC {
			continue
		}
		if vendor.WorkStructureID == "" || w.Structures[vendor.WorkStructureID] == nil {
			continue
		}
		if vendor.Inventory[item] <= 0 {
			continue
		}
		if !buyerIsDistributor && SellerAtWholesaler(w.VillageObjects, vendor.WorkStructureID) {
			continue
		}
		if !vendor.RestockPolicy.ProducesOrForages(item) && !ActorIsDistributor(w.VillageObjects, vendor.WorkStructureID) {
			continue // LLM-252: first-hand suppliers (or the distributor) only
		}
		// Co-present seller: pay_with_item resolves this very tick, and the cue's
		// buy-here imperative renders without consulting the walk-to drops.
		// DisplayName is required because coPresentSellerForItem can't name a
		// nameless seller and falls back to the walk-to list.
		if a.CurrentHuddleID != "" && vendor.CurrentHuddleID == a.CurrentHuddleID && vendor.DisplayName != "" {
			return true
		}
		// Walk-to drops (LLM-216): a supplier remembered shut, or one whose
		// remembered price the purse can't cover, is not a destination. A
		// batch-pinned buyer has no walk-to path at all (see doc above).
		if pinned {
			continue
		}
		if a.Observed.Active(ObservedStateKey{StructureID: vendor.WorkStructureID, Condition: ObservedClosed}, now) {
			continue
		}
		if !buyerCanTransact(w, a, vendorID, item) {
			continue
		}
		return true
	}
	return false
}

// buyerCanTransact reports whether the buyer has the MEANS to pay this vendor for
// item — the LLM-406 means-to-pay gate, and the warrant-side mirror of the
// findItemVendors drop (perception/restock.go). Three ways to pay:
//
//   - coins that cover the price the buyer REMEMBERS paying this vendor for the
//     item;
//   - coins, with no price yet on record — patronage earns the number, so the buyer
//     walks over, learns it, and pays (an unknown price was never a drop);
//   - failing coin, any OTHER good to put up in a pay_with_item bundle
//     (HoldsBarterableGoodsExcept) — the seller adjudicates the bundle, so goods the
//     buyer carries ARE means to pay. The item being bought is excluded: a keeper
//     down to his last few carrots cannot buy carrots by offering carrots.
//
// Only a buyer with no coin AND no goods is a hard payment dead-end, and only that
// buyer is dropped. The old test was coins-only (LLM-216), which asked whether the
// buyer could pay in COIN and dropped a supplier whenever it couldn't — erasing the
// goods-rich, coin-poor keeper from his own supply chain and leaving him in a silent
// absorbing state (see means_to_pay.go for the live incident). Same coin-OR-goods
// shape the consumer buy cue has had since LLM-222.
func buyerCanTransact(w *World, a *Actor, vendorID ActorID, item ItemKind) bool {
	price := LastPaidCoins(w.PriceBook, a.ID, vendorID, item)
	switch {
	case price > 0 && a.Coins >= price:
		return true
	case price == 0 && a.Coins > 0:
		return true
	default:
		return HoldsBarterableGoodsExcept(a.Inventory, item)
	}
}

// actorBatchPinnedAtPost reports whether the actor is mid-production-batch at
// its post — the LLM-319 pause model means the batch only advances while the
// actor is there, so a walk-to trip has a real opportunity cost the actor
// keeps (correctly) declining. Same at-post + in-flight-batch predicate as
// shouldChooseProduction (production_choice_tick.go) and the LLM-335 shift-
// duty suppression (shift_duty.go).
func actorBatchPinnedAtPost(a *Actor) bool {
	return a.WorkStructureID != "" && a.InsideStructureID == a.WorkStructureID && a.ProductionActivity != nil
}

// actorRemembersForageSource reports whether the actor remembers a still-owned
// forage-to-sell bush for item — the minimum precondition buildForage
// (perception/forage.go) needs to render "## Your bushes to harvest". Mirrors that
// scan exactly: a known place tagged gather:<item> (LLM-77 ownership-seeding),
// object kind, still present in the world, still owned by the actor, still a
// forage source. Sharing VillageObject.HasForageSourceFor /
// ObjectRefresh.IsForageToSellFor with the cue keeps the warrant and the section
// in lockstep on what's actionable.
func actorRemembersForageSource(a *Actor, w *World, item ItemKind) bool {
	affordance := "gather:" + string(item)
	for ref, kp := range a.KnownPlaces {
		if kp == nil || kp.Kind != PlaceKindObject || !kp.HasAffordance(affordance) {
			continue
		}
		obj := w.VillageObjects[VillageObjectID(ref)]
		if obj == nil || obj.OwnerActorID != a.ID {
			continue
		}
		if obj.HasForageSourceFor(item) {
			return true
		}
	}
	return false
}

// restockEligible reports whether an actor is a candidate for a restock warrant
// this scan: an agent-backed NPC (stateful or shared VA), not a transient
// visitor, not already pending / mid-tick, not already walking somewhere, and
// not resting (asleep / on break). Pure read of actor state. The low-stock +
// actionability check is separate (firstActionableLowEntry).
func restockEligible(a *Actor, now time.Time) bool {
	if a.Kind != KindNPCStateful && a.Kind != KindNPCShared {
		return false
	}
	if a.VisitorState != nil {
		return false
	}
	if a.WarrantedSince != nil || a.TickInFlight {
		return false
	}
	// Already walking: leave the reseller alone until it arrives. This producer
	// is level-triggered (re-fires every minute while stock is low), but the
	// trip to a supplier takes longer than the tick interval — so without this
	// gate a fresh restock warrant re-stamps the reseller mid-walk every minute,
	// waking it to re-decide and (on the weaker stateful model) abandon and
	// reverse the very supplier trip the cue asked for: the live Josiah-Thorne
	// oscillation — store → set off toward farm → stop mid-walk on the next
	// 60s re-stamp → head back to store → pitch the restock at no one → repeat
	// (ZBBS-HOME-386). The standing low-stock condition still holds on arrival,
	// so the next stationary scan re-stamps and the reseller ticks at the
	// supplier, where it can pay_with_item.
	if a.MoveIntent != nil {
		return false
	}
	if a.SleepingUntil != nil && a.SleepingUntil.After(now) {
		return false
	}
	if a.BreakUntil != nil && a.BreakUntil.After(now) {
		return false
	}
	return true
}

// EvaluateRestock returns a Command that applies one pass of the restock
// producer: stamp a restock warrant on every eligible actor holding a `buy` or
// `forage` entry below the reorder threshold (LLM-90 folded forage into the same
// wake path — a bare sell-shelf a grower replenishes from her own bushes is the
// same "an item I'm responsible for ran low" fact as a reseller's empty buy-in
// shelf, with the same downstream "the matching section renders this tick"
// action). Runs on the world goroutine, so the tryStampWarrant calls are
// serialized. No-op when RestockReorderPct is 0.
func EvaluateRestock(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			pct := w.Settings.RestockReorderPct
			if pct <= 0 {
				return 0, nil // producer disabled
			}
			stamped := 0
			for _, a := range w.Actors {
				if a.RestockPolicy == nil {
					continue
				}
				// LLM-304: a degraded business is shut for restock — no reorder warrant
				// while it's too worn to keep stock. The owner sells down what's on hand
				// and mends to reopen the refill; the "## Restocking" cue is suppressed.
				if ownerStallDegraded(w, a.ID) {
					continue
				}
				if !restockEligible(a, now) {
					continue
				}
				low, src, ok := firstActionableLowEntry(a, w, pct, now, actorConserving(w, a, now))
				if !ok {
					continue
				}
				// tryStampWarrant is void, but restockEligible already guaranteed
				// WarrantedSince == nil and the reason is zero-sourced (dedup
				// bypassed), so this call always opens a fresh warrant cycle —
				// the count is an accurate stamped-this-pass total, not just
				// "eligible" (code_review).
				tryStampWarrant(w, a, WarrantMeta{
					TriggerActorID: a.ID,
					Reason:         RestockWarrantReason{Item: low.Item, Source: src},
				}, now)
				stamped++
			}
			return stamped, nil
		},
	}
}

// RestockTickerInterval — once a minute, matching RunProduceTicker /
// RunShiftTicker / RunNeedsTicker. ~60s reorder-retry granularity.
const RestockTickerInterval = time.Minute

// RunRestockTicker owns the restock-producer goroutine: once a minute, submit
// an EvaluateRestock. Same time.NewTicker idiom as RunProduceTicker /
// RunShiftTicker. Returns when ctx is cancelled.
func RunRestockTicker(ctx context.Context, w *World) {
	t := time.NewTicker(RestockTickerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.beatTicker("restock")
			if _, err := w.SendContext(ctx, EvaluateRestock(time.Now().UTC())); err != nil {
				if ctx.Err() == nil {
					log.Printf("sim/restock: tick failed: %v", err)
				}
			}
		}
	}
}
