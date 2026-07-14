package sim

import "time"

// events_scene_quote.go — Phase 3 PR S3 event family.
//
// SceneQuoteCreated fires when a new quote is minted by
// sim.SceneQuoteCreate. SceneQuoteExpired fires on every quote
// terminal-state transition (TTL, superseded, cap_displaced). One
// event family covers all three terminal flavors with a Reason
// discriminator — admin filtering wants to group these by-reason and
// subscribers that care about all three (e.g. a future SceneQuoteSink
// projector) only need one subscription point.

// SceneQuoteCreated fires when a new SceneQuote is minted and inserted
// into World.Quotes by sim.SceneQuoteCreate. The targeted-quote
// subscriber (handlers/scene_quote_reactor.go) stamps a
// SceneQuoteTargetedWarrantReason on TargetBuyer when TargetBuyer is
// non-empty AND the targeted actor is an NPC; PC TargetBuyers see the
// quote via Snapshot.Quotes in their client perception.
//
// Public quotes (TargetBuyer == "") fire the same event; when the
// seller is in an active huddle the subscriber fans the warrant out to
// the seller's NPC huddle peers instead (ZBBS-HOME-431 — they heard
// the offer). Outside a huddle no warrant is stamped and scene
// participants pick the quote up at perception build via the
// pull-based render path (see scene-quote-design § 7).
//
// All quote terms ride on the event for downstream subscribers (admin
// projection, telemetry). Same one-ID-flows-through pattern as
// Spoke/Paid: the event's EventID is canonical, copied into the
// quote's SourceEventID and any derived warrant's source key.
type SceneQuoteCreated struct {
	EventBase
	QuoteID     QuoteID
	SceneID     SceneID
	SellerID    ActorID
	TargetBuyer ActorID // empty for public
	// Lines carries the bundle's item kinds + per-consumer quantities
	// (LLM-101). Single-line for the common single-item quote. Deep-cloned
	// at emit so subscribers can't mutate the quote's slice.
	Lines       []QuoteLine
	Amount      int
	ConsumeNow  bool
	ConsumerIDs []ActorID
	ExpiresAt   time.Time
	At          time.Time

	// HuddleID scopes the quote to the seller's conversation. (ZBBS-HOME-470
	// removed PCRecipientIDs: a quote no longer produces a buyer-facing wire
	// frame — a PC learns the offer from the seller's own speech and the Pay
	// modal's /pc/quotes read, so there is no per-PC audience to carry.)
	HuddleID HuddleID
}

func (SceneQuoteCreated) isSimEvent() {}

// Scene-quote terminal-state reason codes, surfaced on
// SceneQuoteExpired.Reason. String-typed for admin filtering ease.
// Promoted to a typed enum if a subscriber ever needs exhaustive
// switching, but at S3 ship there's only the one subscriber
// (admin projection, deferred to PR S5) so a string is enough.
const (
	// SceneQuoteExpiredReasonTTL — aging sweep flipped this quote
	// because its ExpiresAt passed.
	SceneQuoteExpiredReasonTTL = "ttl"

	// SceneQuoteExpiredReasonSuperseded — a new quote with the same
	// non-Amount key replaced this one. The new QuoteID rides on the
	// SceneQuoteCreated event the displacement was triggered by, so
	// admin replay can chain the two via timestamps + seller ID.
	SceneQuoteExpiredReasonSuperseded = "superseded"

	// SceneQuoteExpiredReasonCapDisplaced — the per-(seller, scene)
	// cap was hit; this was the oldest active quote in the bucket
	// so it got displaced.
	SceneQuoteExpiredReasonCapDisplaced = "cap_displaced"

	// SceneQuoteExpiredReasonTaken — a buyer settled this quote via the
	// pay_with_item fast path (LLM-189). The quote's lot is sold, so it
	// closes (terminal SceneQuoteStateTaken) instead of lingering as a
	// phantom standing offer. The transfer itself rides the
	// PayWithItemResolved{Accepted} event; this is the quote-side close.
	SceneQuoteExpiredReasonTaken = "taken"

	// SceneQuoteExpiredReasonShortfall — the seller's coverable holding of
	// a quoted good fell below the lot's remaining quantity (they spent /
	// ate / paid the goods away out from under their own offer), so the
	// pre-publish coverage reconcile flipped the lot to
	// SceneQuoteStateShortfall. The seller is told via the perception beat;
	// they re-post what they still hold next turn (LLM-409).
	SceneQuoteExpiredReasonShortfall = "shortfall"
)

// SceneQuoteExpired fires on every scene-quote terminal-state
// transition. Reason distinguishes the terminal paths:
//
//   - "ttl" — aging sweep flipped expired (the common case).
//   - "superseded" — duplicate-key replacement at create time.
//   - "cap_displaced" — per-(seller, scene) cap hit at create time.
//   - "taken" — a buyer settled the quote via the pay_with_item fast
//     path; the lot is sold (LLM-189).
//
// No subscribers — the event is shipped for the PR S5
// SceneQuoteSink admin projection and any later policy subscribers.
// Quote warrants are restart-noncritical (TargetBuyer's perception
// surfaces fresh quotes on every tick anyway), so no warrant is
// stamped on expiry.
type SceneQuoteExpired struct {
	EventBase
	QuoteID  QuoteID
	SceneID  SceneID
	SellerID ActorID
	Reason   string
	At       time.Time
}

func (SceneQuoteExpired) isSimEvent() {}
