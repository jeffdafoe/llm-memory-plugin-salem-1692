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
// Excerpt carries the FULL ForText (LLM-400). It used to be rune-truncated
// to sim.MaxSalientFactTextLen (220) to bound per-tick token cost, the same
// bare cut LLM-396 removed from the speech path — a mid-word slice with no
// marker, handing the seller a payment reason that stops mid-clause as though
// it were whole.
//
// The cut could not actually fire: every producer of ForText rune-caps it at
// 200 first (the pay tool's MaxPayForChars, pay_with_item's
// MaxPayWithItemForChars, and the PC HTTP route's mirror of the same), so 200
// runes never reached a 220-rune cut. But the two constants live in different
// packages with nothing linking them: raising MaxPayForChars past 220 would
// have switched silent mid-word truncation on in the seller's prompt with no
// test failing. The dead cut is removed rather than left as a landmine.
//
// Length stays bounded where it belongs — upstream at the 200-rune tool cap,
// and downstream in the renderer, which caps the warrant payload at
// perception.RenderConfig.MaxBytesPerWarrant (600 bytes) and, unlike this
// path, MARKS the cut with an ellipsis. A 200-rune ForText only reaches that
// cap if it is heavily multi-byte, and then it is elided visibly rather than
// silently.
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
	excerpt := paid.ForText
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
// Separate from RegisterSpeechHandlers / cascade.RegisterEncounter for
// the same opt-in-piecewise reason: a build that wants speech but not
// pay (or vice versa) can compose. Must run on the world goroutine —
// call before World.Run or from inside a Command.Fn.
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
