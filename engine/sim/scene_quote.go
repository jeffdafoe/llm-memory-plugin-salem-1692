package sim

import "time"

// scene_quote.go — Phase 3 PR S3 substrate. Carries the SceneQuote
// aggregate (a vendor-posted offer-to-sell visible to a scene's
// participants), the per-quote state enum, the QuoteID type, and the
// deep-clone helper used at the published-Snapshot and mem-repo
// serialization boundaries.
//
// Design spec: shared/tasks/engine-in-memory-rewrite/scene-quote-design
// (settled 2026-05-16 EOS-24 — 8 substrate decisions + PC-compat addenda).
// Parent architecture: shared/tasks/engine-in-memory-rewrite/pay-with-item-architecture-design.
// Underlying ledger substrate (lands at PR S5): pay-ledger-substrate-design.
//
// The quote is the authoritative seller-posted offer surface. A buyer's
// pay_with_item tool call (PR S4) can reference a QuoteID to take the
// fast path — bypassing seller deliberation when the buyer's terms
// match the quote exactly. A quote does NOT reserve stock or coins;
// the fast-path predicate revalidates everything at acceptance time.
//
// Quote storage is world-level flat (World.Quotes map[QuoteID]*SceneQuote)
// with a per-scene reverse index (Scene.QuoteIDs []QuoteID). The flat
// map matches LedgerID's pattern (set in PR S5's pay-ledger substrate)
// and gives the buyer's fast-path O(1) lookup by QuoteID alone — no
// caller-side scene context needed in the tool args. The reverse index
// supports O(quotes-in-scene) perception build for every actor in the
// scene.

// QuoteID identifies a SceneQuote within a single world run. uint64 over
// the v2 string-ID convention because QuoteID is LLM-visible — the model
// reads quote IDs off perception text and emits them back in
// pay_with_item(quote_id=N, ...) tool calls. UUIDs are unreliable for
// LLM readback (models hallucinate digits within long hex strings); a
// bare integer is dramatically more reliable. Same precedent as
// EventID — both are engine-internal mints, not entity IDs minted
// outside the engine.
//
// QuoteID(0) is the reserved invalid/unset sentinel: the counter starts
// at 1 (World.quoteSeq is incremented before assignment). Safety floor
// on restart: counter = max(checkpointed, max(IDs across entries)).
type QuoteID uint64

// SceneQuoteState is the lifecycle state of a SceneQuote. Five-state
// enum: one active state, four terminal states (no transitions out of
// any terminal). Mirrors the locked spec at scene-quote-design § 6,
// extended with the "taken" terminal (LLM-189).
type SceneQuoteState string

const (
	// SceneQuoteStateActive — quote is live; eligible for buyer fast-path
	// and visible in perception of scene participants.
	SceneQuoteStateActive SceneQuoteState = "active"

	// SceneQuoteStateExpired — quote aged out past ExpiresAt; sweep
	// flipped it. Terminal.
	SceneQuoteStateExpired SceneQuoteState = "expired"

	// SceneQuoteStateSuperseded — a new quote with the same non-Amount
	// key (seller + item + qty + consume_now + target_buyer + consumer
	// set) replaced this one. Re-pricing the same offer terms is the
	// legitimate use case for the duplicate-key path; replacement keeps
	// buyer perception uncluttered. Terminal.
	SceneQuoteStateSuperseded SceneQuoteState = "superseded"

	// SceneQuoteStateCapDisplaced — this quote was the oldest active
	// quote in the (seller, scene) bucket when the per-seller-per-scene
	// cap was hit. Terminal.
	SceneQuoteStateCapDisplaced SceneQuoteState = "cap_displaced"

	// SceneQuoteStateTaken — a buyer settled this quote via the
	// pay_with_item fast path (explicit quote_id or auto-match). The
	// quote represents a specific lot offered for sale; a take is
	// whole-lot (the fast path requires an exact qty match), so the
	// lot is sold and the offer is fulfilled — it closes rather than
	// lingering as a phantom "standing" offer in the seller's
	// perception. A seller with more to sell re-posts. This is the
	// "once-then-done" policy choice the SceneQuote doc comment defers
	// to pay-with-item time (LLM-189). Terminal.
	SceneQuoteStateTaken SceneQuoteState = "taken"

	// SceneQuoteStateShortfall — the seller can no longer cover the lot:
	// their coverable holding of a quoted good (on-hand minus goods
	// earmarked for a Ready order) dropped below what the lot promises,
	// because they spent, ate, or paid the goods away out from under
	// their own standing offer. The pre-publish coverage reconcile
	// (reconcileQuoteCoverage) flips the lot here so it stops advertising
	// stock the seller lacks and stops pinning them to a promise they
	// cannot keep. Whole-lot expire, not shrink: the seller announced the
	// lot's price aloud, so the engine kills it and narrates the broken
	// promise (the "## An offer you couldn't keep" beat) rather than
	// silently re-pricing a deal to a quantity nobody agreed to. The
	// seller re-posts what they still hold on their next turn. Terminal
	// (LLM-409).
	SceneQuoteStateShortfall SceneQuoteState = "shortfall"
)

// SceneQuoteTTLDefault is the default Time-To-Live for a scene quote
// when WorldSettings.SceneQuoteTTL is unset (zero). Locked at 10 minutes
// in scene-quote-design § 3 — asymmetric with the much shorter pay-ledger
// pending TTL (2-5 min) because a quote is a passive ad rather than a
// staked offer. Vendor walking out / taking a break ages the quote out
// within ~10 min naturally.
const SceneQuoteTTLDefault = 10 * time.Minute

// SceneQuoteSweepCadenceDefault is the default periodic-sweep cadence
// when WorldSettings.SceneQuoteSweepCadence is unset (zero). 60s gives
// ±60s expiry latency against a 10-minute TTL — invisible at gameplay
// scale. Matches the locked pay-ledger sweep cadence (settled in the
// same EOS-24 design pass) so when both substrates ship, an admin
// tuning sweep cadences sees a single mental model.
const SceneQuoteSweepCadenceDefault = 60 * time.Second

// SceneQuoteMaxPerSellerScene is the per-(seller, scene) cap on
// concurrent active quotes. Hitting the cap displaces the oldest
// active quote in the bucket (terminal state cap_displaced) rather
// than rejecting the new quote — auto-displacement is recoverable; a
// hard reject is annoying tool surface for an LLM. 10 from
// scene-quote-design § 4 — generous enough that a vendor with stew
// can legitimately quote eat-in / takeaway / bulk dimensions
// simultaneously, tight enough that a misbehaving LLM or cascade
// can't spam quotes unbounded.
const SceneQuoteMaxPerSellerScene = 10

// SceneQuoteMaxConsumers is the hard cap on len(ConsumerIDs) for a
// group-order quote. Mirrors the matching cap on pay-with-item group
// orders (architecture note § 9) so the matching predicate doesn't
// have to special-case quote-side vs offer-side limits.
const SceneQuoteMaxConsumers = 8

// MaxSceneQuoteLines caps the number of distinct item lines in one bundle
// quote (LLM-101). 8 matches the consumer cap — generous enough for a real
// "some of each" basket, tight enough to keep the per-quote prompt + pay-panel
// line list bounded. Duplicate kinds within a bundle are merged at creation,
// so this caps distinct kinds, not raw lines the model emitted.
const MaxSceneQuoteLines = 8

// QuoteLine is one item kind + per-consumer quantity within a SceneQuote's
// bundle (LLM-101). A single-line quote is the ordinary single-item offer; a
// multi-line quote bundles several kinds under one total Amount, taken whole
// by a buyer's quote_id (the seller could not previously represent "some of
// both" in one offer, so a weak model fumbled the multi-call decomposition).
// ItemKind is canonical (resolved at quote-creation time); Qty is units per
// consumer for this line.
type QuoteLine struct {
	ItemKind ItemKind
	Qty      int
}

// SceneQuote is a vendor-posted offer-to-sell anchored to one Scene.
// Authoritative seller-side commerce surface — created via the
// scene_quote tool by an NPC seller; buyer's pay_with_item (PR S4)
// can reference QuoteID to take the fast path.
//
// Substrate decisions locked at scene-quote-design § 1 and § 7:
//
//   - Flat world-level storage (World.Quotes) plus per-scene reverse
//     index (Scene.QuoteIDs). Quote IDs are LLM-visible uint64
//     counters; quotes outlive scenes only if a scene concludes mid-
//     quote-lifetime (in which case the aging sweep cleans them up
//     at TTL — no eager nuke on scene close).
//
//   - State is one of SceneQuoteState{Active, Expired, Superseded,
//     CapDisplaced, Taken}. The accepted transfer lives on the
//     pay-ledger entry created by pay_with_item; the quote carries a
//     "taken" terminal so the seller's perception stops advertising a
//     lot that is already sold (LLM-189). The "conceptually a quote
//     could be taken many times" hedge in the original spec deferred
//     the once-vs-many policy to pay-with-item time — that choice is
//     now made: a take is whole-lot (the fast path requires an exact
//     qty match), so one take fulfills the lot and closes the quote.
//     A seller with more to sell re-posts.
//
//   - TargetBuyer empty means public-to-huddle (every eligible scene
//     participant sees the quote); non-empty means a single addressed
//     buyer (only that buyer sees it prominently; warrant stamped
//     only when the target is an NPC).
//
//   - ConsumerIDs empty means buyer is implicit single consumer.
//     Non-empty enumerates a group-order participant set; buyer pays
//     the full bundle amount, stock check applies Qty * len(consumers)
//     against seller.Inventory.
//
//   - Amount is the bundle total, not per-unit. Fast-path treats
//     Amount as a floor: buyer paying more is tipping; less is reject.
//
//   - RootEventID/SourceEventID inherit from the SceneQuoteCreated
//     event that minted this quote. Carries the causal trail for
//     admin replay; not the dedup key for fast-path matching (that
//     uses non-Amount field equality).
type SceneQuote struct {
	ID QuoteID

	// SceneID anchors the quote's visibility scope. The scene's
	// Huddles set determines who can perceive the quote (every actor
	// in any observing huddle). When the scene concludes, the quote
	// remains in World.Quotes until the aging sweep flips it expired
	// — at which point it falls out of any rebuilt Scene.QuoteIDs
	// index too (the post-conclude scene no longer exists to hold
	// the index entry).
	SceneID SceneID

	// SellerID is the actor who posted the quote. Eligibility filter
	// in perception excludes the seller from seeing their own quote.
	SellerID ActorID

	// TargetBuyer is empty for a public-to-huddle quote; non-empty
	// means a single addressed buyer. Eligibility filter:
	// public quotes show to everyone in scene minus seller;
	// targeted quotes show only to the TargetBuyer (still minus
	// seller). NPC TargetBuyer gets a SceneQuoteTargetedWarrant
	// stamp; PC TargetBuyer relies on the client's perception
	// (warrant would be inert on a non-deliberating PC).
	TargetBuyer ActorID

	// Lines are the item kinds + per-consumer quantities this quote
	// bundles (LLM-101). A single-line quote is the common single-item
	// offer; a multi-line quote is a bundle ("2 blueberries + 2
	// raspberries for 8 coins") taken whole via the buyer's quote_id.
	// Each line's ItemKind is canonical (resolved against w.ItemKinds at
	// quote-creation time, NOT the raw LLM string); Qty is units per
	// consumer for that line. Always holds >=1 line; duplicate kinds are
	// merged at creation. Stock check at create time runs per line:
	// seller.Inventory[line.ItemKind] >= line.Qty * effectiveConsumerCount.
	// No reservation.
	Lines []QuoteLine

	// ConsumeNow toggles the immediate-apply semantics of the
	// downstream pay-with-item flow. Quotes match buyer's
	// pay_with_item only when ConsumeNow agrees — a buyer asking
	// for takeaway can't take a sit-down-eat quote.
	ConsumeNow bool

	// ConsumerIDs is the resolved group-order participant list.
	// Empty = buyer is implicit single consumer. Non-empty
	// requires the seller has an active huddle (the consumers must
	// be huddle peers at quote-creation time; their later departure
	// doesn't invalidate the quote but may fail fast-path
	// validation at pay-with-item time).
	ConsumerIDs []ActorID

	// Amount is the bundle total in coins. Fast-path treats this
	// as a floor — buyer may pay more (tipping) but not less. The
	// per-unit / per-consumer breakdown is derivable but the LLM
	// reads the bundle total, so that's what's stored.
	Amount int

	State SceneQuoteState

	CreatedAt  time.Time
	ExpiresAt  time.Time
	ResolvedAt time.Time // zero while State == Active

	// RootEventID and SourceEventID are the SceneQuoteCreated event's
	// IDs. The quote is causally rooted in its create event for
	// admin trace; the source event is also what the targeted-buyer
	// warrant stamp uses as its WarrantSourceKey discriminator
	// (pre-§8 dedup scheme — DedupDiscriminator interface migration
	// is slated for PR S5 alongside pay-offer warrants).
	RootEventID   EventID
	SourceEventID EventID
}

// CloneSceneQuote returns a deep copy suitable for the published
// Snapshot or the mem-repo serialization boundary. The ConsumerIDs
// slice is cloned so a snapshot reader can't reach back into world
// state through it. Returns nil for a nil input.
func CloneSceneQuote(q *SceneQuote) *SceneQuote {
	if q == nil {
		return nil
	}
	cp := *q
	if q.ConsumerIDs != nil {
		cp.ConsumerIDs = make([]ActorID, len(q.ConsumerIDs))
		copy(cp.ConsumerIDs, q.ConsumerIDs)
	}
	// QuoteLine is a flat value type (no slices/pointers), so a plain copy
	// of the backing array fully isolates the clone.
	if q.Lines != nil {
		cp.Lines = make([]QuoteLine, len(q.Lines))
		copy(cp.Lines, q.Lines)
	}
	return &cp
}

// nextQuoteSeq increments the per-run QuoteID counter and returns the
// new identifier. World-goroutine-only — called exclusively from
// Command.Fn callsites that mint a SceneQuote. The counter starts at
// 0, so the first minted quote gets ID 1; QuoteID(0) is reserved as
// the unset sentinel.
func (w *World) nextQuoteSeq() QuoteID {
	w.quoteSeq++
	return QuoteID(w.quoteSeq)
}

// effectiveSceneQuoteTTL returns the configured TTL or the default
// when WorldSettings.SceneQuoteTTL is zero/unset (tests that bypass
// repo loading don't seed it).
func effectiveSceneQuoteTTL(s WorldSettings) time.Duration {
	if s.SceneQuoteTTL > 0 {
		return s.SceneQuoteTTL
	}
	return SceneQuoteTTLDefault
}

// effectiveSceneQuoteSweepCadence returns the configured sweep cadence
// or the default when WorldSettings.SceneQuoteSweepCadence is zero/unset.
func effectiveSceneQuoteSweepCadence(s WorldSettings) time.Duration {
	if s.SceneQuoteSweepCadence > 0 {
		return s.SceneQuoteSweepCadence
	}
	return SceneQuoteSweepCadenceDefault
}

// restartExpireScannedQuotes walks World.Quotes at LoadWorld time and
// flips any active quote whose ExpiresAt has already passed to the
// terminal Expired state. No SceneQuoteExpired event is emitted — the
// original SceneQuoteCreated cascade root is gone, so an emit here
// would have no causal anchor (intentional: quote warrants are
// restart-noncritical per scene-quote-design § 7).
//
// DORMANT BY DESIGN: pending scene quotes are intentionally
// restart-lossy (decided 2026-05-20 — see
// work/tasks/payledger-restart-lossy/decision). There is no QuotesRepo
// and none planned, so w.Quotes always starts empty and this never has
// quotes to expire today. Kept because it encodes correct behavior if
// the decision is ever reversed.
//
// MUST be called from inside LoadWorld (single-threaded, before
// republish), or from inside a Command.Fn. ResolvedAt is stamped with
// `now` to give admin queries an honest "found-expired-at-load"
// timestamp distinct from CreatedAt.
func restartExpireScannedQuotes(w *World, now time.Time) {
	if w == nil {
		return
	}
	for _, q := range w.Quotes {
		if q == nil || q.State != SceneQuoteStateActive {
			continue
		}
		if !q.ExpiresAt.IsZero() && !now.Before(q.ExpiresAt) {
			q.State = SceneQuoteStateExpired
			q.ResolvedAt = now
		}
	}
}

// rebuildSceneQuoteIndex rebuilds Scene.QuoteIDs from the canonical
// World.Quotes map. Called at LoadWorld so any drift between the two
// stores (e.g. a future repo that serializes both sides separately)
// can't persist past startup. Only currently-active quotes are
// indexed — terminal-state quotes don't appear in perception.
//
// MUST be called from inside LoadWorld or a Command.Fn. Allocates a
// fresh slice per scene that had any quotes; scenes with no active
// quotes get nil (matches the "empty = not present" convention used
// for other optional Scene fields).
func rebuildSceneQuoteIndex(w *World) {
	if w == nil {
		return
	}
	// Clear first so a scene that had quotes pre-restart but has none
	// post-restart drops its index entry.
	for _, scene := range w.Scenes {
		if scene != nil {
			scene.QuoteIDs = nil
		}
	}
	for id, q := range w.Quotes {
		if q == nil || q.State != SceneQuoteStateActive {
			continue
		}
		scene, ok := w.Scenes[q.SceneID]
		if !ok || scene == nil {
			continue
		}
		scene.QuoteIDs = append(scene.QuoteIDs, id)
	}
}

// removeQuoteFromSceneIndex drops qid from scene.QuoteIDs. Called when
// a quote transitions to a terminal state (expired, superseded,
// cap_displaced). No-op when the index doesn't contain the entry —
// the index should always agree with World.Quotes state, but
// defensive removal makes terminal-state transitions safe to call
// even if the index was somehow already pruned.
//
// MUST be called from inside a Command.Fn.
func removeQuoteFromSceneIndex(scene *Scene, qid QuoteID) {
	if scene == nil || len(scene.QuoteIDs) == 0 {
		return
	}
	out := scene.QuoteIDs[:0]
	for _, id := range scene.QuoteIDs {
		if id != qid {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		scene.QuoteIDs = nil
		return
	}
	scene.QuoteIDs = out
}
