package sim

import (
	"context"
	"log"
	"time"
)

// restock_tick.go — tick-driver producer (ZBBS-WORK-322): the buy-side restock
// producer. The missing half of the two-sided economy. produce_tick.go refills
// actors that MAKE their stock (`produce` RestockEntries); resellers that BUY
// their stock (`buy` RestockEntries) deplete through sales (pay_with_item) and,
// without this producer, never restock — any shop stocking bought goods empties
// over game-days. v1 had buy_walker.go (an engine-forced walker); v2 reserved
// the substrate (StateShopping, RestockSourceBuy) but never built the executor.
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
// REORDER THRESHOLD: a `buy` entry is "low" when its on-hand quantity is
// strictly below cap * RestockReorderPct / 100 (default 25%). cap==0 (no cap
// configured) is skipped — there's no fraction to take. RestockReorderPct==0
// disables the producer entirely (the operator off-switch).
//
// SUPPRESSION mirrors the other producers: PCs and transient visitors are out
// of scope (PCs are player-driven; visitors run their own ExpiresAt lifecycle),
// decoratives carry no real restock intent (they're not agent-backed), and
// sleeping / on-break resellers are left alone (the same rest suppressor the
// reactor and the #1/#2 producers use).

// DefaultRestockReorderPct is the reorder threshold as a whole percent of an
// entry's cap. A `buy` entry below this fraction of its cap warrants a restock
// tick. 25 == restock when on-hand drops under a quarter of cap.
const DefaultRestockReorderPct = 25

// RestockWarrantReason is the WarrantReason stamped when an agent-backed
// reseller holds a `buy` RestockEntry below the reorder threshold (ZBBS-WORK-
// 322). Item is the first low item found (deterministic by RestockEntry order)
// — carried for telemetry / admin replay and to render the warrant cue line;
// the deliberation reads the FULL low-stock set + suppliers from the
// "## Restocking" perception section, not this single field. Zero-sourced (a
// stock level is not an event), so DedupDiscriminator returns 0 and the
// substrate's source-key dedup paths are bypassed — the per-actor
// WarrantedSince gate in the producer is what prevents double-stamp. Mirrors
// NeedThresholdWarrantReason / ShiftDutyWarrantReason — the other condition-
// driven, zero-sourced reasons.
type RestockWarrantReason struct {
	Item ItemKind
}

func (RestockWarrantReason) isWarrantReason()           {}
func (RestockWarrantReason) Kind() WarrantKind          { return WarrantKindRestock }
func (RestockWarrantReason) DedupDiscriminator() uint64 { return 0 }

// RestockReorderThresholdMet reports whether currentQty sits strictly below
// the reorder threshold for an entry with the given cap, at the given percent.
// Shared by the producer and the perception gate so the warrant and the
// "## Restocking" section can never disagree on what counts as "low". A
// non-positive cap or pct yields false (nothing to reorder against / producer
// disabled). The comparison is done in integer cross-multiplied form
// (currentQty*100 < cap*pct) to avoid any float rounding at the boundary; the
// multiplications widen to int64 so a pathological cap/pct from a corrupt config
// or import can't overflow int and flip the comparison (code_review).
func RestockReorderThresholdMet(currentQty, cap, pct int) bool {
	if cap <= 0 || pct <= 0 {
		return false
	}
	return int64(currentQty)*100 < int64(cap)*int64(pct)
}

// firstLowBuyEntry returns the first `buy` RestockEntry on the policy whose
// on-hand quantity is below the reorder threshold, and whether one was found.
// Order follows RestockPolicy.Restock (first-listed wins), so the choice is
// deterministic. Used by the producer to pick the warrant's representative
// Item; perception surfaces the full set.
func firstLowBuyEntry(policy *RestockPolicy, inventory map[ItemKind]int, pct int) (RestockEntry, bool) {
	if policy == nil {
		return RestockEntry{}, false
	}
	for _, e := range policy.BuyEntries() {
		if RestockReorderThresholdMet(inventory[e.Item], e.Cap(), pct) {
			return e, true
		}
	}
	return RestockEntry{}, false
}

// restockEligible reports whether an actor is a candidate for a restock warrant
// this scan: an agent-backed NPC (stateful or shared VA), not a transient
// visitor, not already pending / mid-tick, and not resting (asleep / on break).
// Pure read of actor state. The low-stock check is separate (firstLowBuyEntry).
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
	if a.SleepingUntil != nil && a.SleepingUntil.After(now) {
		return false
	}
	if a.BreakUntil != nil && a.BreakUntil.After(now) {
		return false
	}
	return true
}

// EvaluateRestock returns a Command that applies one pass of the buy-side
// restock producer: stamp a restock warrant on every eligible reseller holding
// a `buy` entry below the reorder threshold. Runs on the world goroutine, so
// the tryStampWarrant calls are serialized. No-op when RestockReorderPct is 0.
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
				low, ok := firstLowBuyEntry(a.RestockPolicy, a.Inventory, pct)
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
					Reason:         RestockWarrantReason{Item: low.Item},
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
			if _, err := w.SendContext(ctx, EvaluateRestock(time.Now().UTC())); err != nil {
				if ctx.Err() == nil {
					log.Printf("sim/restock: tick failed: %v", err)
				}
			}
		}
	}
}
