package sim

import "time"

// sale_standoff.go — LLM-525. Experiential "my offers for this good at this shop
// aren't finding a deal" memory. The co-present half already exists: once the
// buyer has been turned down SaleStandoffDeclineThreshold times in one
// conversation, perception softens the "Buy it now" goad to "hold off and come
// back later" (coPresentBuyStandoff, LLM-297/LLM-510). But that classification is
// derived per-tick from the pay ledger scoped to the CURRENT huddle, so it
// evaporates the moment the buyer walks away — and the off-post buy errand
// ("## Nails to mend your business", "## Farm upkeep", "## Restocking") re-names
// that same shop as a move_to destination on the very next tick. The owner turns
// around, is softened again, leaves again: the live Elizabeth Ellis
// farm<->blacksmith oscillation of 2026-07-25, four round trips in twelve minutes.
//
// The fix is the one LLM-198 already made on the labor side — a refusal the actor
// REMEMBERS rather than a refusal only legible while standing in front of it. When
// the standoff trips, stamp it into the buyer's observed-state store; perception
// then drops that (structure, item) from the buy directory for the TTL, so
// "come back later" means later instead of after the walk home.
//
// This is the CAPTURE half (a PayWithItemResolved subscriber, additive). The
// SURFACE half lives in perception (businessRememberedSaleStandoff in
// consumable_vendors.go, read by findItemVendors). The store itself is the unified
// observed-state memory in observed_state.go (the ObservedSaleStandoff condition),
// the same decaying, restart-lossy store that backs ObservedClosed /
// ObservedOutOfStock / ObservedDeclinedWork.
//
// "Standoff", not "refused": the threshold counts the same three terminals
// coPresentBuyStandoff counts, and one of them (FailedInsufficientGoods) is the
// BUYER's bundle falling short rather than the seller saying no. What they share is
// that the deal did not close and re-walking there will not change that within the
// hour — which is what the directory drop is about.

// SaleStandoffMemoryTTL is how long a dead-ended negotiation suppresses that
// (structure, item) from the buyer's buy directory before perception lists it
// again. 4 game-hours, matching OutOfStockMemoryTTL / ClosedBusinessMemoryTTL: long
// enough that "come back later" costs the buyer an errand rather than a walk, short
// enough that a smith who was holding his last two nails for his own forge is worth
// asking again the same afternoon. Deliberately milder than the employer-side
// DeclinedWorkMemoryTTL (12h) — a shop's stock and mood turn over faster than a
// hiring decision.
const SaleStandoffMemoryTTL = 4 * time.Hour

// SaleStandoffDeclineThreshold is how many of the buyer's offers for one item must
// dead-end with one seller, within one conversation, before the negotiation counts
// as stuck. One decline is ordinary haggling; a second means the terms aren't going
// to meet (LLM-297). Shared with perception's co-present soften
// (copresentStandoffDeclineThreshold) so the remembered standoff and the rendered
// hold-off trip on exactly the same event — the memory is precisely "the soften
// fired, and here is the record of it that outlives the huddle".
const SaleStandoffDeclineThreshold = 2

// saleStandoffTerminal reports whether a resolved pay terminal counts toward a
// standoff. Mirrors the decline arm of perception's coPresentBuyStandoff: an
// explicit refusal, the seller unable to fill, or the buyer's offered bundle
// falling short. Deliberately NOT countered (a counter-offer is the negotiation
// continuing, not dead-ending), withdrawn (the buyer's own doing), expired, or
// failed-unavailable (a transient co-presence failure). An insufficient-FUNDS
// failure is excluded too: an empty purse is the buyer's problem and is already
// governed by merchantConserve, and a purse can refill within the hour, so it must
// not suppress the destination for four.
func saleStandoffTerminal(state PayTerminalState) bool {
	switch state {
	case PayTerminalStateDeclined,
		PayTerminalStateFailedInsufficientStock,
		PayTerminalStateFailedInsufficientGoods:
		return true
	}
	return false
}

// handleSaleStandoffOnResolved is the PayWithItemResolved subscriber that records
// (or clears) the buyer's memory of a negotiation dead-ending at a shop. It is a
// no-op for non-agent buyers, for sellers with no workplace (a co-present peer buy
// — there is no structure to walk-avoid), and for terminals that are neither a
// standoff step nor a success.
//
// A standoff terminal stamps the memory only once the conversation's running count
// reaches SaleStandoffDeclineThreshold, so a first no still leaves the shop in the
// directory and the buyer may press once more — the haggling LLM-297 deliberately
// allows. An accepted buy clears it: they dealt after all.
func handleSaleStandoffOnResolved(w *World, evt Event) {
	res, ok := evt.(*PayWithItemResolved)
	if !ok {
		return
	}
	buyer := w.Actors[res.BuyerID]
	if buyer == nil || !isAgentNPC(buyer) {
		return // only NPC buyers carry experiential memory
	}
	// Keyed by the SELLER's workplace — the structure the buy directory names and
	// the buyer walks to. A seller with no workplace is a co-present peer; there is
	// no destination to remember-and-avoid.
	seller := w.Actors[res.SellerID]
	if seller == nil || seller.WorkStructureID == "" {
		return
	}
	key := ObservedStateKey{StructureID: seller.WorkStructureID, ItemKind: res.ItemKind, Condition: ObservedSaleStandoff}
	switch {
	case res.TerminalState == PayTerminalStateAccepted:
		// They dealt — whatever went wrong before is settled; don't keep the shop out
		// of the directory. Mirrors the out-of-stock self-clear.
		buyer.Observed.Clear(key)
	case saleStandoffTerminal(res.TerminalState):
		if saleStandoffReached(w, res) {
			buyer.Observed.Observe(key, res.At)
		}
	}
}

// saleStandoffReached reports whether this resolved terminal is the one that takes
// the (buyer, seller, item) negotiation to SaleStandoffDeclineThreshold dead ends
// within the conversation it happened in — the same (buyer, seller, item, huddle)
// scope perception's coPresentBuyStandoff counts over, and the same whole-huddle
// lifetime with no recency filter (LLM-510: re-offers are paced minutes apart, so a
// rolling window can never latch).
//
// The conversation comes from the EVENT's own HuddleID, not from a ledger lookup on
// res.LedgerID. Every emitter stamps it (from entry.HuddleID on the slow-path
// terminals, from the buyer's current huddle on the fast-path accept), so the count
// does not depend on the resolving entry still being present in the ledger, nor on
// whether the subscriber runs before or after that entry's own state write — an
// ordering this function must not assume (code_review LLM-525). Prior entries are
// still read from the ledger, which is where the conversation's history lives; the
// resolving entry is excluded by ledger ID and added by hand, so it counts exactly
// once whether or not its own terminal state has landed.
//
// A resolved event carrying no huddle yields false: with no conversation to scope the
// count to there is nothing to call a standoff, the same disabled-scan posture
// perception takes on an empty huddle.
func saleStandoffReached(w *World, res *PayWithItemResolved) bool {
	if res.HuddleID == "" {
		return false
	}
	count := 0
	for _, e := range w.PayLedger {
		if e == nil || e.ID == res.LedgerID {
			continue // the resolving entry is added below, however its own write landed
		}
		if e.BuyerID != res.BuyerID || e.SellerID != res.SellerID || e.ItemKind != res.ItemKind || e.HuddleID != res.HuddleID {
			continue
		}
		if e.ResolvedAt.IsZero() {
			continue // still pending, or mid-construction without its resolve stamp
		}
		// A prior entry resolved AFTER this event did not happen yet from this
		// event's point of view, and counting it would trip the standoff before the
		// buyer had actually been turned down twice (code_review LLM-525). The two
		// stamps share a basis — resolvePayTerminal writes entry.ResolvedAt and the
		// event's At from the same command timestamp — so the comparison is sound.
		// Skipped when the event carries no time at all: rejecting every prior entry
		// as "future" against a zero clock would silently disable the count, a worse
		// failure than the skew this guards against.
		if !res.At.IsZero() && e.ResolvedAt.After(res.At) {
			continue
		}
		if SaleStandoffLedgerState(e.State) {
			count++
		}
	}
	return count+1 >= SaleStandoffDeclineThreshold
}

// SaleStandoffLedgerState is saleStandoffTerminal over the ledger's own state enum —
// the two enums are parallel but distinct types, and the ledger is what a prior entry
// carries. Exported because perception's coPresentBuyStandoff counts the same
// terminals off the same ledger: sharing this predicate is what keeps the rendered
// co-present hold-off and the remembered standoff tripping on exactly the same
// events. Two parallel switches saying they mirror each other is precisely the drift
// this replaces (code_review LLM-525).
func SaleStandoffLedgerState(state PayLedgerState) bool {
	switch state {
	case PayLedgerStateDeclined,
		PayLedgerStateFailedInsufficientStock,
		PayLedgerStateFailedInsufficientGoods:
		return true
	}
	return false
}

// RegisterSaleStandoffSubscriber wires the sale-standoff-memory subscriber. Call
// before World.Run or from inside a Command (world-goroutine-safe). Mirrors
// RegisterOutOfStockSubscriber — another observed-state capture subscriber on the
// same event. LLM-525.
func RegisterSaleStandoffSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterSaleStandoffSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleSaleStandoffOnResolved))
}
