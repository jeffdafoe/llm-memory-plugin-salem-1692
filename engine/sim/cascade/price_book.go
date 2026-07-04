package cascade

import (
	"log"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// price_book.go — wires the per-(seller, item) accepted-price ring
// buffer driver. One subscriber on PayWithItemResolved: for every
// terminal state Accepted (ConsumeNow AND take-home), append one
// PriceObservation to the keyed ring buffer.
//
// v1 reference: engine/pay_history.go's `lastPaidPrice` filters
// `state='accepted'` — knowledge of price lands at acceptance, not
// at delivery. PayWithItemResolved is the architecture-§-10 canonical
// "commerce ended" event and fires for both eat-on-the-spot and
// take-home flows; subscribing here covers v1's full filter.
//
// Why not OrderDelivered: ConsumeNow accepts never mint an Order,
// so OrderDelivered would miss every dine-in transaction. Subscribing
// to PayWithItemResolved at TerminalState=Accepted is the strict
// superset.
//
// Lifecycle:
//
//   RegisterPriceBook(w)
//   └─> w.Subscribe(handlePayWithItemResolvedPriceBook)
//
// No sweep goroutine — the substrate is a bounded ring buffer
// (PriceBookRingCapacity); old observations age out naturally as new
// ones arrive. No compaction is needed.

// RegisterPriceBook wires the single PayWithItemResolved subscriber
// that maintains the price book. Must run on the world goroutine —
// call before World.Run, or from inside a Command.Fn.
//
// Idempotency: registering twice double-appends every observation.
// Wiring guards live at the registration site — don't register twice.
//
// Panics on nil w (wiring guard, mirrors RegisterActionLog /
// RegisterAtmosphere).
func RegisterPriceBook(w *sim.World) {
	if w == nil {
		panic("cascade: RegisterPriceBook requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handlePayWithItemResolvedPriceBook))
}

// handlePayWithItemResolvedPriceBook appends a PriceObservation to
// the (SellerID, ItemKind) ring buffer when a pay-with-item offer
// resolves to Accepted. Non-Accepted terminals (Declined, Withdrawn,
// Expired, the three Failed_*) are no-ops — no money changed hands
// at those terminals, so no price knowledge was earned.
//
// Defensive normalizations:
//
//   - Consumers floored at 1: an Accepted offer with zero
//     ConsumerIDs would be malformed (no recipient), but the
//     substrate prefers to record SOMETHING over panicking. A
//     malformed event is logged at the event's source, not here.
//   - Amount, Qty stored as-supplied. The substrate trusts that
//     PayLedgerEntry-side validation rejected nonsensical values
//     before the Accept terminal flipped.
func handlePayWithItemResolvedPriceBook(w *sim.World, evt sim.Event) {
	resolved, ok := evt.(*sim.PayWithItemResolved)
	if !ok {
		return
	}
	if resolved.TerminalState != sim.PayTerminalStateAccepted {
		return
	}
	// Pure barter (no coins, goods-only) records no price observation —
	// there is no single coin price to remember for the (seller, item)
	// pair, and an Amount of 0 would poison the price book with a free
	// reading. Mixed coin+goods accepts still record the coin leg.
	// ZBBS-HOME-393.
	if resolved.Amount <= 0 {
		return
	}
	// A bundle quote-take carries its goods in Lines and leaves ItemKind
	// empty — the lump Amount has no per-line split, so there is no single
	// (seller, item) pair to file an observation under. Record nothing
	// rather than keying the ring on "". LLM-246.
	if resolved.ItemKind == "" {
		return
	}
	consumers := len(resolved.ConsumerIDs)
	if consumers < 1 {
		consumers = 1
	}
	key := sim.PriceBookKey{
		SellerID: resolved.SellerID,
		Item:     resolved.ItemKind,
	}
	obs := sim.PriceObservation{
		BuyerID:   resolved.BuyerID,
		Amount:    resolved.Amount,
		Qty:       resolved.QtyPerConsumer,
		Consumers: consumers,
		At:        resolved.At,
	}
	if _, err := sim.RecordPriceObservation(key, obs).Fn(w); err != nil {
		log.Printf("cascade/price_book: record (seller %q item %q buyer %q event %d): %v",
			resolved.SellerID, resolved.ItemKind, resolved.BuyerID, resolved.EventID(), err)
	}
}
