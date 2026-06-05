package handlers

import (
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_with_item_reactor.go — Phase 3 PR S4 step 7. Event subscribers
// that turn pay-with-item lifecycle events into reactor warrants on
// the affected NPCs.
//
// Three subscribers ship together because the offer state machine is
// one logical unit (architecture note § 11):
//
//   - PayOfferReceived  → stamps PayOfferWarrantReason on the seller.
//                         The seller's next reactor tick perceives the
//                         offer terms and decides accept_pay /
//                         decline_pay / counter_pay.
//
//   - PayWithItemResolved → stamps PayResolvedWarrantReason on the
//                           buyer. Covers accept / decline / withdraw /
//                           expire / failed_* terminals. The buyer
//                           perceives the outcome and can speak,
//                           in_response_to follow up (if countered —
//                           handled by the parallel subscriber below),
//                           or move on.
//
//   - PayCountered → stamps PayResolvedWarrantReason{TerminalState:
//                    Countered, CounterAmount, Message} on the buyer.
//                    Separate event family from PayWithItemResolved
//                    because the commerce isn't ENDED — the buyer's
//                    optional response is a fresh
//                    pay_with_item(in_response_to=parent_id, ...).
//
// All three skip non-NPC targets (PCs don't deliberate via the
// reactor; PC perception of pay-ledger resolutions surfaces via the
// client's Snapshot.PayLedger). Subscribers also skip the buyer-
// self-action paths (WithdrawnByBuyer terminal — the buyer drove that
// resolution themselves and got the result in their tool-call return,
// so a warrant about it would be redundant noise). Slow-path Accepted
// stamps the buyer warrant; fast-path Accepted also stamps it
// (redundant with the buyer's tool-call return, but cheap, and
// avoids the subscriber having to distinguish fast-path from slow-path
// — that distinction lives on the entry, not the event).

// handlePayOfferReceivedWarrants is the PayOfferReceived subscriber.
// Stamps PayOfferWarrantReason on the seller. Restart-stable dedup:
// the warrant's DedupDiscriminator is uint64(LedgerID), so a re-stamp
// from LoadWorld's restartReStampPayOfferWarrants pass that fires
// before normal cascade flow resumes dedupes cleanly against this
// subscriber's stamp.
func handlePayOfferReceivedWarrants(w *sim.World, evt sim.Event) {
	offer, ok := evt.(*sim.PayOfferReceived)
	if !ok {
		return
	}
	if offer.SellerID == "" {
		return
	}
	seller, ok := w.Actors[offer.SellerID]
	if !ok || seller == nil {
		return
	}
	if seller.Kind != sim.KindNPCStateful && seller.Kind != sim.KindNPCShared {
		// PC or decorative seller. PR S4 ships NPC-seller-only commerce
		// (architecture § "What this design does NOT cover" —
		// PC-as-seller lands at cutover). Defensive skip.
		return
	}

	now := time.Now().UTC()
	meta := sim.WarrantMeta{
		TriggerActorID: offer.BuyerID,
		Force:          false,
		Reason: sim.PayOfferWarrantReason{
			LedgerID:    offer.LedgerID,
			Buyer:       offer.BuyerID,
			Item:        offer.ItemKind,
			Qty:         offer.QtyPerConsumer,
			Amount:      offer.Amount,
			PayItems:    cloneItemKindQtys(offer.PayItems),
			ConsumeNow:  offer.ConsumeNow,
			ConsumerIDs: cloneActorIDs(offer.ConsumerIDs),
			ExpiresAt:   offer.ExpiresAt,
			Depth:       offer.Depth,
		},
		SourceEventID: offer.EventID(),
		RootEventID:   offer.RootEventID(),
		SourceActorID: offer.BuyerID,
		HuddleID:      offer.HuddleID,
		SceneID:       offer.SceneID,
		OccurredAt:    offer.At,
	}
	if _, err := sim.StampWarrant(offer.SellerID, meta, now).Fn(w); err != nil {
		log.Printf(
			"handlers: pay-with-item-reactor StampWarrant for seller %q (ledger %d, event %d): %v",
			offer.SellerID, offer.LedgerID, offer.EventID(), err,
		)
	}
}

// handlePayResolvedWarrants is the PayWithItemResolved subscriber.
// Stamps PayResolvedWarrantReason on the buyer for every non-counter
// terminal (counter has its own event family + subscriber below).
// Skips WithdrawnByBuyer — the buyer drove that resolution themselves
// and got the outcome in their tool-call return; a warrant would be
// redundant.
func handlePayResolvedWarrants(w *sim.World, evt sim.Event) {
	resolved, ok := evt.(*sim.PayWithItemResolved)
	if !ok {
		return
	}
	if resolved.TerminalState == sim.PayTerminalStateWithdrawnByBuyer {
		return // buyer self-drove; no warrant
	}
	if resolved.BuyerID == "" {
		return
	}
	buyer, ok := w.Actors[resolved.BuyerID]
	if !ok || buyer == nil {
		return
	}
	if buyer.Kind != sim.KindNPCStateful && buyer.Kind != sim.KindNPCShared {
		return // PC buyer; client-side perception handles it
	}

	now := time.Now().UTC()
	meta := sim.WarrantMeta{
		TriggerActorID: resolved.SellerID,
		Force:          false,
		Reason: sim.PayResolvedWarrantReason{
			LedgerID:        resolved.LedgerID,
			Seller:          resolved.SellerID,
			ItemKind:        resolved.ItemKind,
			Qty:             resolved.QtyPerConsumer,
			Amount:          resolved.Amount,
			TerminalState:   resolved.TerminalState,
			Message:         resolved.Message,
			ResolvedEventID: resolved.EventID(),
		},
		SourceEventID: resolved.EventID(),
		RootEventID:   resolved.RootEventID(),
		SourceActorID: resolved.SellerID,
		HuddleID:      resolved.HuddleID,
		SceneID:       resolved.SceneID,
		OccurredAt:    resolved.At,
	}
	if _, err := sim.StampWarrant(resolved.BuyerID, meta, now).Fn(w); err != nil {
		log.Printf(
			"handlers: pay-with-item-reactor StampWarrant for buyer %q (ledger %d, event %d): %v",
			resolved.BuyerID, resolved.LedgerID, resolved.EventID(), err,
		)
	}
}

// handlePayCounteredWarrants is the PayCountered subscriber. Stamps
// PayResolvedWarrantReason with TerminalState=Countered on the buyer,
// populated with CounterAmount + Message so the buyer's perception
// prompt can render the seller's counter terms without a separate
// ledger lookup.
//
// Separate from handlePayResolvedWarrants because PayCountered carries
// fields (CounterAmount, OriginalAmount) that PayWithItemResolved
// doesn't — the architecture-design EOS-26 lock split the event
// families to avoid a defensive switch in every subscriber.
func handlePayCounteredWarrants(w *sim.World, evt sim.Event) {
	countered, ok := evt.(*sim.PayCountered)
	if !ok {
		return
	}
	if countered.BuyerID == "" {
		return
	}
	buyer, ok := w.Actors[countered.BuyerID]
	if !ok || buyer == nil {
		return
	}
	if buyer.Kind != sim.KindNPCStateful && buyer.Kind != sim.KindNPCShared {
		return
	}

	now := time.Now().UTC()
	meta := sim.WarrantMeta{
		TriggerActorID: countered.SellerID,
		Force:          false,
		Reason: sim.PayResolvedWarrantReason{
			LedgerID:        countered.ParentID,
			Seller:          countered.SellerID,
			ItemKind:        countered.ItemKind,
			Qty:             countered.QtyPerConsumer,
			Amount:          countered.OriginalAmount,
			TerminalState:   sim.PayTerminalStateCountered,
			Message:         countered.Message,
			CounterAmount:   countered.CounterAmount,
			CounterPayItems: cloneItemKindQtys(countered.CounterPayItems),
			ResolvedEventID: countered.EventID(),
		},
		SourceEventID: countered.EventID(),
		RootEventID:   countered.RootEventID(),
		SourceActorID: countered.SellerID,
		HuddleID:      countered.HuddleID,
		SceneID:       countered.SceneID,
		OccurredAt:    countered.At,
	}
	if _, err := sim.StampWarrant(countered.BuyerID, meta, now).Fn(w); err != nil {
		log.Printf(
			"handlers: pay-with-item-reactor StampWarrant for buyer %q (countered ledger %d, event %d): %v",
			countered.BuyerID, countered.ParentID, countered.EventID(), err,
		)
	}
}

// RegisterPayWithItemHandlers wires all three pay-with-item event
// subscribers into the world (PayOfferReceived, PayWithItemResolved,
// PayCountered). Separate from RegisterPayHandlers /
// RegisterSpeechHandlers / RegisterSceneQuoteHandlers for the same
// opt-in-piecewise reason — a build that wants speech + pay but not
// the item-commerce flow can omit this. Must run on the world
// goroutine — call before World.Run or from inside a Command.Fn.
//
// Idempotency: registering twice would invoke each subscriber twice
// per event, but tryStampWarrant's source-aware dedup catches the
// duplicate. For PayOfferReceived: (WarrantKindPayOffer, LedgerID) is
// the key — same key both times, second drop. For
// PayWithItemResolved / PayCountered: (WarrantKindPayResolved,
// ResolvedEventID) — same.
func RegisterPayWithItemHandlers(w *sim.World) {
	if w == nil {
		panic("handlers: RegisterPayWithItemHandlers requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handlePayOfferReceivedWarrants))
	w.Subscribe(sim.SubscriberFunc(handlePayResolvedWarrants))
	w.Subscribe(sim.SubscriberFunc(handlePayCounteredWarrants))
}

// cloneActorIDs returns an independent copy of ids. Used by warrant
// stamps that snapshot the event's ConsumerIDs onto the warrant
// payload — a subsequent mutation of the event's slice (none today,
// but the contract is value semantics) must not reach the warrant.
func cloneActorIDs(ids []sim.ActorID) []sim.ActorID {
	if len(ids) == 0 {
		return nil
	}
	out := make([]sim.ActorID, len(ids))
	copy(out, ids)
	return out
}

// cloneItemKindQtys returns an independent copy of the barter goods-line
// slice (ZBBS-HOME-393), so the warrant stamp holds value semantics over
// the event's PayItems / CounterPayItems rather than aliasing them.
func cloneItemKindQtys(in []sim.ItemKindQty) []sim.ItemKindQty {
	if len(in) == 0 {
		return nil
	}
	out := make([]sim.ItemKindQty, len(in))
	copy(out, in)
	return out
}
