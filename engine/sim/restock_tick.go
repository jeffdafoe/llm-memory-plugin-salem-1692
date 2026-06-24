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
// a reorder for an entry with the given cap, at the given percent. Shared by the
// producer and the perception gate so the warrant and the "## Restocking" section
// can never disagree on what counts as "low". A non-positive cap or pct yields
// false (nothing to reorder against / producer disabled).
//
// Normally the test is strictly below the cap*pct/100 fraction, in integer
// cross-multiplied form (currentQty*100 < cap*pct) to avoid float rounding at the
// boundary. But when that fraction is below one whole unit (cap*pct < 100, e.g. a
// skillet cap of 2 at 25% = 0.5) strict-below floors the trigger to "only when
// empty" — the reseller never rebuys until it is completely out. In that case the
// fraction rounds up to one unit and the reorder fires when down to the last unit
// (currentQty <= 1), so a small cap still gets a proactive trigger. Caps large
// enough that cap*pct >= 100 are unaffected; the int64 widening keeps a
// pathological cap/pct from a corrupt config or import from overflowing.
func RestockReorderThresholdMet(currentQty, cap, pct int) bool {
	if cap <= 0 || pct <= 0 {
		return false
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
// The actionability gate is the asymmetry between the two cues (code_review):
// "## Restocking" renders for ANY low buy item (the supplier is resolved in
// perception, vendor present or not), so a low buy entry is always actionable. The
// forage cue, by contrast, renders only when the grower remembers a still-owned
// forage bush for the item — so a low forage entry warrants ONLY when
// actorRemembersForageSource holds. Without this gate a high-information forage
// warrant (WarrantKindRestock bypasses noop-skip) would wake the actor every scan
// with a cue line pointing at a "## Your bushes to harvest" section buildForage
// declines to render — a wake loop on forgotten / sold / deleted / never-seeded
// bushes. Order within each source follows RestockPolicy.Restock (first wins).
func firstActionableLowEntry(a *Actor, w *World, pct int) (RestockEntry, RestockSource, bool) {
	policy := a.RestockPolicy
	if policy == nil {
		return RestockEntry{}, "", false
	}
	for _, e := range policy.BuyEntries() {
		if RestockReorderThresholdMet(a.Inventory[e.Item], e.Cap(), pct) {
			return e, RestockSourceBuy, true
		}
	}
	for _, e := range policy.ForageEntries() {
		if RestockReorderThresholdMet(a.Inventory[e.Item], e.Cap(), pct) && actorRemembersForageSource(a, w, e.Item) {
			return e, RestockSourceForage, true
		}
	}
	return RestockEntry{}, "", false
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
				if !restockEligible(a, now) {
					continue
				}
				low, src, ok := firstActionableLowEntry(a, w, pct)
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
