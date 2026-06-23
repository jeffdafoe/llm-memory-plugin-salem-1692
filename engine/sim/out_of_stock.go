package sim

import "time"

// out_of_stock.go — ZBBS-HOME-363. Experiential "this vendor was out of that
// item" memory — the out-of-stock sibling of closed_business.go (HOME-353).
//
// Vendors are allowed to run dry: a farm regenerates its origin good only while
// the producer is at its post (Moses off-post → 0 carrots), and some advertised
// goods are finite seed stock that never regenerate (Ellis/James water). So the
// buy menu can name a vendor-item the NPC then walks to and CANNOT buy. Without
// memory the NPC re-walks the same dry vendor in a loop. The fix is experiential,
// not omniscient: when a buy fails on stock, the buyer remembers that
// (structure, item) — and perception deprioritizes that vendor-item in the buy
// menu so the model picks a different source. The memory self-clears on a later
// SUCCESSFUL buy of the same (structure, item), and DECAYS after
// OutOfStockMemoryTTL so the NPC retries rather than believing it dry forever.
//
// This is the CAPTURE half. Most stock failures arrive as a PayWithItemResolved
// event (the canonical "this commerce ended" signal — the ledger-acceptance
// path), but the quote-payment FAST PATH rejects insufficient stock with a bare
// error and no event (no ledger entry exists to resolve). So capture has two
// entry points that funnel through the same noteOutOfStock recorder: the event
// subscriber here, and an inline call at the fast-path stock reject
// (pay_with_item_commands.go). The SURFACE half lives in perception
// (consumable_vendors.go / satiation.go) and reads the actor's Observed store
// (ObservedOutOfStock). The store itself is the unified observed-state memory in
// observed_state.go (LLM-80); the (structure, item) identity is carried by an
// ObservedStateKey keyed on the vendor's WORKPLACE (what the buy-menu cue names
// and the buyer's move_to walks to), not the vendor actor.

// OutOfStockMemoryTTL is how long an out-of-stock observation stays actionable
// before perception ignores it — matched to ClosedBusinessMemoryTTL (4 game-
// hours): long enough to stop the same-session re-walk loop, short enough that a
// vendor that restocks within the day clears the avoidance.
const OutOfStockMemoryTTL = 4 * time.Hour

// handleOutOfStockOnResolved is the PayWithItemResolved subscriber that records
// (or clears) the buyer's memory of a vendor-item being out of stock. It is a
// no-op for non-agent buyers, for sellers with no workplace (a co-present peer
// buy — there is no structure to walk-avoid), and for terminals other than a
// stock failure (record) or a success (clear).
func handleOutOfStockOnResolved(w *World, evt Event) {
	res, ok := evt.(*PayWithItemResolved)
	if !ok {
		return
	}
	buyer := w.Actors[res.BuyerID]
	if buyer == nil || !isAgentNPC(buyer) {
		return
	}
	// The memory is keyed by the SELLER's workplace — the structure the buy menu
	// names and the buyer walks to. A seller with no workplace is a co-present
	// peer (the buy-from-the-person-beside-you affordance); there is nothing to
	// remember-and-avoid walking to, so skip it.
	seller := w.Actors[res.SellerID]
	if seller == nil || seller.WorkStructureID == "" {
		return
	}
	switch res.TerminalState {
	case PayTerminalStateFailedInsufficientStock:
		noteOutOfStock(w, res.BuyerID, res.SellerID, res.ItemKind, res.At)
	case PayTerminalStateAccepted:
		// Bought it successfully — they have it after all; clear any stale "dry"
		// memory for this exact (structure, item).
		buyer.Observed.Clear(ObservedStateKey{StructureID: seller.WorkStructureID, ItemKind: res.ItemKind, Condition: ObservedOutOfStock})
	}
}

// noteOutOfStock records the buyer's experiential memory that it tried to buy
// itemKind from sellerID and found it out of stock. Shared by the
// PayWithItemResolved subscriber (ledger path) and the quote-payment fast-path
// reject (pay_with_item_commands.go), so both buyer-initiated stock-failure
// routes funnel through one recorder. No-op for a non-agent buyer (PCs get no
// experiential memory) or a seller with no workplace (a co-present peer — there
// is nothing to remember-and-avoid walking to). Keyed by the seller's WORKPLACE
// (what the buy-menu cue names and move_to walks to). MUST run on the world
// goroutine.
func noteOutOfStock(w *World, buyerID, sellerID ActorID, itemKind ItemKind, at time.Time) {
	buyer := w.Actors[buyerID]
	if buyer == nil || !isAgentNPC(buyer) {
		return
	}
	seller := w.Actors[sellerID]
	if seller == nil || seller.WorkStructureID == "" {
		return
	}
	buyer.Observed.Observe(ObservedStateKey{StructureID: seller.WorkStructureID, ItemKind: itemKind, Condition: ObservedOutOfStock}, at)
}

// RegisterOutOfStockSubscriber wires the out-of-stock-memory subscriber. Call
// before World.Run or from inside a Command (world-goroutine-safe). Mirrors
// RegisterClosedBusinessSubscriber. ZBBS-HOME-363.
func RegisterOutOfStockSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterOutOfStockSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleOutOfStockOnResolved))
}
