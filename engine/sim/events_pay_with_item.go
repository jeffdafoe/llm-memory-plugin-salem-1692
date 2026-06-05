package sim

import "time"

// events_pay_with_item.go — Phase 3 PR S4 event family for the
// buyer-initiated pay-with-item commerce flow. Three event types
// covering the offer lifecycle:
//
//   - PayOfferReceived fires when sim.PayWithItem inserts a new pending
//     entry into World.PayLedger (slow-path offer creation). The
//     pay-offer subscriber (handlers/pay_with_item_reactor.go in a
//     later step) stamps a PayOfferWarrantReason on the seller so
//     their next reactor tick perceives the offer.
//
//   - PayCountered fires when sim.CounterPay flips a parent entry to
//     terminal-countered. The buyer's resolution warrant subscriber
//     handles this branch separately from PayWithItemResolved because
//     counter terms (CounterAmount, Message) need their own payload
//     shape — the buyer's perception prompt has to render the
//     counter-proposal terms to decide whether to respond with
//     pay_with_item(in_response_to=ParentID, ...).
//
//   - PayWithItemResolved fires on every NON-counter terminal
//     transition — accept, decline, withdraw_by_buyer, expire, and
//     the three failed_* terminals. Single canonical resolution event
//     for "the commerce ENDED" (per architecture § 10 — see the
//     "transaction event is the source of truth" passage). Counter is
//     intentionally excluded because the commerce isn't ended — it's
//     been re-routed to a chained pending entry.
//
// Why three events instead of one: PayOfferReceived's payload is
// pre-resolution (no terminal state); PayCountered carries
// counter-specific fields (CounterAmount + Message together encode the
// new offer terms); PayWithItemResolved is the "end of commerce"
// signal. Splitting prevents subscribers from having to defensively
// switch on a TerminalState that might be "pending" or "countered"
// when they only care about resolutions.
//
// All three events embed full term snapshots (ItemKind, QtyPerConsumer,
// ConsumeNow, ConsumerIDs) rather than asking subscribers to look the
// entry up from World.PayLedger. Subscribers run on the world goroutine
// so the lookup would be safe, but embedding keeps event handlers
// snapshot-clean: an admin-projection subscriber processing events from
// a buffered channel after they were emitted doesn't have to chase live
// ledger state that may have moved on. Same posture as scene_quote's
// events.

// PayOfferReceived fires when sim.PayWithItem inserts a new pending
// PayLedgerEntry. Emitted only on the slow path — quote fast-path
// matches mint the entry already-accepted and emit
// PayWithItemResolved{TerminalState: Accepted} directly, bypassing
// PayOfferReceived entirely (the offer never sits in pending). The
// pay-offer subscriber consumes this event to stamp the seller's
// warrant.
//
// LedgerID, ItemKind, QtyPerConsumer, ConsumeNow, ConsumerIDs,
// Amount, and QuoteID are snapshotted from the entry at creation.
// ParentID is non-zero only for an in_response_to chain link.
// SceneID + HuddleID anchor the offer's co-presence context (the
// buyer + seller's shared huddle at offer creation; accept-time
// revalidation re-checks both are still in HuddleID).
//
// ExpiresAt is the wall-clock TTL boundary, computed by the Command
// Fn as `At + effectivePayLedgerTTL(w.Settings)`. The aging sweep
// uses ExpiresAt off the entry, not off this event, but the event
// carries it for admin replay and any subscriber that wants to
// surface the deadline to a downstream UI.
type PayOfferReceived struct {
	EventBase

	LedgerID       LedgerID
	BuyerID        ActorID
	SellerID       ActorID
	ItemKind       ItemKind
	QtyPerConsumer int
	ConsumeNow     bool
	ConsumerIDs    []ActorID
	Amount         int

	// PayItems are the goods the buyer offers to pay WITH (barter leg,
	// ZBBS-HOME-393). Empty for a pure-coin offer. Snapshotted from the
	// entry so the pay-offer warrant subscriber can stamp them onto the
	// seller's warrant without a live ledger lookup.
	PayItems []ItemKindQty

	// QuoteID is non-zero when the buyer's pay_with_item call
	// referenced a quote_id. Zero for a slow-path offer that didn't
	// engage quote matching.
	QuoteID QuoteID

	// ParentID is the LedgerID of the parent offer in the counter
	// chain. Zero for a root offer (no in_response_to); non-zero when
	// this entry was created via pay_with_item(in_response_to=N) after
	// the parent's counter.
	ParentID LedgerID

	// Depth is the entry's counter-chain depth (0 for a root offer,
	// parent.Depth+1 for an in_response_to response). Carried so the
	// seller's pay-offer warrant can gate counter_pay at the chain cap
	// without a ledger lookup. ZBBS-WORK-320.
	Depth int

	SceneID   SceneID
	HuddleID  HuddleID
	ExpiresAt time.Time
	At        time.Time
}

func (PayOfferReceived) isSimEvent() {}

// PayCountered fires when sim.CounterPay flips a parent entry to
// terminal-countered. Distinct from PayWithItemResolved because the
// commerce isn't ENDED — the buyer can respond with
// pay_with_item(in_response_to=ParentID, ...) to create a chained
// pending entry. The buyer's resolution warrant subscriber renders
// CounterAmount + Message to the buyer's perception prompt.
//
// OriginalAmount is the buyer's original offer (the parent entry's
// Amount field). CounterAmount is the seller's counter terms (positive,
// validated by the counter_pay handler). The two values let the buyer's
// prompt show "you offered N, seller counters with M" without a
// separate ledger lookup.
//
// Message is the seller's free-text counter message (length-capped at
// handler intake; rendered escaped in any other actor's prompt to
// prevent prompt-injection across actors). Empty when the seller
// passed no message.
//
// ItemKind / QtyPerConsumer / ConsumeNow / ConsumerIDs are snapshotted
// from the parent entry. v2 doesn't support changing item terms across
// a counter — only the price changes. If the buyer wants to change
// terms in their response, they pass new ItemKind/Qty/etc fields to
// pay_with_item(in_response_to=N) explicitly (the response is a new
// pay_with_item with its own validation, not a counter-to-the-counter).
type PayCountered struct {
	EventBase

	ParentID       LedgerID
	BuyerID        ActorID
	SellerID       ActorID
	ItemKind       ItemKind
	QtyPerConsumer int
	ConsumeNow     bool
	ConsumerIDs    []ActorID

	OriginalAmount int
	CounterAmount  int

	// CounterPayItems are the goods the seller demands in the counter
	// (symmetric-barter counter, ZBBS-HOME-393). Empty for a pure-coin
	// counter. Carried so the buyer's perception can render the seller's
	// goods terms without a ledger lookup.
	CounterPayItems []ItemKindQty

	Message string

	SceneID  SceneID
	HuddleID HuddleID
	At       time.Time
}

func (PayCountered) isSimEvent() {}

// PayWithItemResolved fires on every NON-counter terminal transition.
// Single source of truth for "this commerce ended" — covers
// PayTerminalStateAccepted, Declined, WithdrawnByBuyer, Expired, and
// the three failed_* terminals. The PayCountered event handles the
// countered transition separately.
//
// TerminalState is typed PayTerminalState (8 possible values) for
// compile-time enforcement that the event never carries the
// Pending state. PayTerminalStateCountered is type-allowed for
// completeness but the event is never EMITTED with that value
// (PayCountered owns that transition). A defensive subscriber
// switch on TerminalState should treat Countered as a bug signal,
// not a normal branch.
//
// Message carries:
//
//   - Declined        → seller's decline reason (length-capped at intake)
//   - WithdrawnByBuyer → buyer's withdraw note (length-capped at intake)
//   - Accepted        → empty (no flavor field on accept)
//   - Expired         → empty
//   - failed_*        → empty (failure cause is encoded in TerminalState)
//
// ItemKind / QtyPerConsumer / ConsumeNow / ConsumerIDs / Amount /
// SceneID / HuddleID are snapshotted from the entry. Admin projection,
// telemetry, and the resolution warrant subscriber all consume the
// event with these fields populated so neither has to chase live
// ledger state at handle time.
//
// Architecture § 10 mandates this is the canonical event. Don't make
// reactors infer the transaction by separately seeing Paid + Consumed —
// that risks partial interpretation. Keep PR B's existing Paid event
// as coins-only; PayWithItemResolved is the item-commerce signal.
type PayWithItemResolved struct {
	EventBase

	LedgerID       LedgerID
	BuyerID        ActorID
	SellerID       ActorID
	ItemKind       ItemKind
	QtyPerConsumer int
	ConsumeNow     bool
	ConsumerIDs    []ActorID
	Amount         int

	TerminalState PayTerminalState
	Message       string

	SceneID  SceneID
	HuddleID HuddleID
	At       time.Time
}

func (PayWithItemResolved) isSimEvent() {}
