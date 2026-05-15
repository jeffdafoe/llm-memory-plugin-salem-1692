package handlers

import (
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_reactor.go — Paid event subscriber. Phase 3 PR B.
//
// Mints one PaidWarrantReason warrant on the seller for every Paid event.
// The Paid event carries the authoritative buyer/seller pair (resolved on
// the world goroutine by sim.Pay); the subscriber does not re-resolve.
//
// Warrant policy choices (locked at the PR B design walkthrough):
//
//   - Always PaidWarrantReason. No PC/NPC split for now — pay warrants
//     only fire on NPC sellers (PCs don't deliberate). When a PC-as-
//     recipient flow lands, split type-per-kind the same way speech did.
//   - Force: false. Same reasoning as the speech reactor — v2's 5s
//     MinReactorTickGap is 60x looser than v1's 5-minute floor that
//     motivated v1's force=true. Force is reserved for the Admin kind.
//   - SourceEventID = Paid.EventID. Same one-ID-flows-through pattern as
//     speech — the event ID is the authoritative pay identifier; it flows
//     into both the warrant payload (PaidID) and the meta (SourceEventID,
//     RootEventID), giving free dedup via the (Kind, SourceEventID) key.
//   - Only the seller gets a warrant. The buyer just committed the pay —
//     they don't need their own tick to react to it; they're already
//     mid-tick.
//
// Excerpt: the ForText excerpt is rune-truncated to MaxSalientFactTextLen
// (220) — every reactor tick the seller takes re-renders the excerpt into
// the perception prompt, so bounding the excerpt bounds the per-tick token
// cost. The raw (200-char-capped, control-char-rejected) text travels on
// the Paid event for any consumer that wants the full flavor.
func handlePaidWarrants(w *sim.World, evt sim.Event) {
	paid, ok := evt.(*sim.Paid)
	if !ok {
		return
	}
	if paid.SellerID == "" {
		return
	}
	if _, ok := w.Actors[paid.SellerID]; !ok {
		return
	}
	now := time.Now().UTC()
	excerpt := truncateRunes(paid.ForText, sim.MaxSalientFactTextLen)
	meta := sim.WarrantMeta{
		TriggerActorID: paid.BuyerID,
		Force:          false,
		Reason: sim.PaidWarrantReason{
			PaidID:  paid.EventID(),
			Buyer:   paid.BuyerID,
			Amount:  paid.Amount,
			ForText: excerpt,
		},
		SourceEventID: paid.EventID(),
		RootEventID:   paid.RootEventID(),
		SourceActorID: paid.BuyerID,
		// HuddleID intentionally empty — Paid doesn't carry HuddleID
		// (1:1 transaction, not a broadcast); the seller's huddle context
		// is derivable from their own CurrentHuddleID at perception time.
		OccurredAt: paid.At,
	}
	// StampWarrant returns an error only on caller bugs (nil Reason,
	// unknown actor). Reason is a non-nil literal and we just looked up
	// the seller. A failure here is an invariant breach; log + move on.
	if _, err := sim.StampWarrant(paid.SellerID, meta, now).Fn(w); err != nil {
		log.Printf(
			"handlers: pay-reactor StampWarrant for seller %q (paid %d): %v",
			paid.SellerID, paid.EventID(), err,
		)
	}
}

// RegisterPayHandlers wires the Paid event subscriber into the world.
// Separate from RegisterSpeechHandlers / RegisterEncounterHandlers for the
// same opt-in-piecewise reason: a build that wants speech but not pay (or
// vice versa) can compose. Must run on the world goroutine — call before
// World.Run or from inside a Command.Fn.
//
// Idempotency: registering twice would invoke the subscriber twice per
// Paid event, but tryStampWarrant's source-aware dedup catches the
// duplicate ((WarrantKindPaid, EventID) collides with itself) and drops
// the second stamp. The general dedup mechanics are tested at the
// substrate level in reactor_pr3a_test.go; this subscriber inherits that
// guarantee by minting with nonzero SourceEventID.
func RegisterPayHandlers(w *sim.World) {
	if w == nil {
		panic("handlers: RegisterPayHandlers requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handlePaidWarrants))
}
