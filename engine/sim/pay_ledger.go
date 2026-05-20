package sim

import "time"

// pay_ledger.go — Phase 3 PR S4 substrate. Carries the PayLedger
// aggregate (offer-side state machine for the buyer-initiated
// pay_with_item commerce flow), the per-entry state enum, the LedgerID
// type, the deep-clone helper, the best-effort PayLedgerSink projection
// interface, and the LoadWorld restart-expire helper.
//
// Design spec: shared/tasks/engine-in-memory-rewrite/pay-ledger-substrate-design
// (settled 2026-05-16 EOS-24 — 11 substrate decisions). Parent
// architecture: shared/tasks/engine-in-memory-rewrite/pay-with-item-architecture-design.
// Adjacent substrate (already shipped): scene_quote.go (PR S3 —
// vendor-posted quote authority that pay_with_item fast-paths against).
//
// PayLedger ≠ Order. PayLedger is the OFFER-side state machine —
// terminates at accepted (or one of seven non-accepted terminals).
// Order is the POST-acceptance delivery state machine for take-away
// items (PR S6 reshape; v1 pay_ledger fulfillment columns belong there,
// NOT here). PR S4 ships PayLedger only; the existing engine/sim/order.go
// stub stays as-is for now and gets reshaped at S5/S6 when delivery
// flows land. The disambiguation is documented in ledger-substrate-design § 2.
//
// Buyer-initiated invariant (architecture § 7): a seller cannot mint a
// pending offer that auto-drains buyer coins. counter_pay flips the
// parent to terminal-countered and the buyer chooses whether to respond
// with a fresh pay_with_item(in_response_to=parent_id). The state
// machine enforces this — there is no command that creates a pending
// entry under any sellerID other than the buyer's own tool call.
//
// No reservation on pending (architecture § 2): a pending entry does
// NOT lock coins, stock, or consumer presence. accept_pay revalidates
// every gate at acceptance time (stock first, funds second per the
// S4 design pass) and flips to a failed_* terminal on mismatch rather
// than accepting against drift.

// LedgerID identifies a PayLedgerEntry within a single world run. uint64
// for the same reasons as QuoteID and EventID: LLM-visible (the model
// reads ledger IDs off perception text and emits them back in
// accept_pay(ledger_id=N) / decline_pay / counter_pay / withdraw_pay
// tool calls; UUID-style strings are unreliable for LLM readback).
// Same restart-stable-identity property: LoadWorld walks pending
// entries and re-stamps PayOfferWarrantReason on the seller keyed on
// uint64(LedgerID) — the source event is gone post-restart but the
// aggregate ID survives, which is exactly what the PR S4 DedupDiscriminator
// migration exists to support.
//
// LedgerID(0) is the reserved invalid/unset sentinel: World.payLedgerSeq
// starts at 0 and is incremented before assignment, so the first minted
// entry gets ID 1. Safety floor on restart:
// counter = max(checkpointed, max(IDs across entries)).
type LedgerID uint64

// PayLedgerState is the lifecycle state of a PayLedgerEntry. Nine-value
// enum: one non-terminal (pending) plus eight terminal states (no
// transitions out of any terminal). Locked at ledger-substrate-design § 3.
//
// No failed_stale: v1's stale-state was concurrency protection (Postgres
// MVCC, attempt-id checks). v2's single-goroutine substrate processes
// commands serially — "another command beat me to it" cases are handled
// by the idempotent tool-reject path (state != pending → tool error, no
// transition), not by writing a terminal failed_stale row.
//
// failed_unavailable is the umbrella for "social context broken" —
// seller on break, closed-shop, consumer left huddle, item kind
// deprecated, co-presence lost between offer and accept. Could split
// later if a use case needs differential narration; no use case
// identified at design time.
type PayLedgerState string

const (
	// PayLedgerStatePending — buyer has staked the offer; seller has not
	// yet acted (or quote fast-path matched and the entry was minted
	// already-accepted, in which case it never holds Pending). The
	// only non-terminal state.
	PayLedgerStatePending PayLedgerState = "pending"

	// PayLedgerStateAccepted — seller accepted (or quote fast-path
	// matched). At-acceptance: coins transferred, items moved, needs
	// decremented if ConsumeNow. Terminal.
	PayLedgerStateAccepted PayLedgerState = "accepted"

	// PayLedgerStateDeclined — seller declined via decline_pay. No
	// goods or coins move. Optional Message field carries the seller's
	// flavor reason. Terminal.
	PayLedgerStateDeclined PayLedgerState = "declined"

	// PayLedgerStateCountered — seller proposed a different price via
	// counter_pay. Parent flips terminal; the counter terms live in
	// CounterAmount + Message; the buyer's optional response is a fresh
	// pay_with_item(in_response_to=parent_id) creating a new pending
	// entry chained via ParentID. Terminal.
	PayLedgerStateCountered PayLedgerState = "countered"

	// PayLedgerStateWithdrawnByBuyer — buyer withdrew the pending offer
	// via withdraw_pay. Buyer-callable only; no co-presence required.
	// Optional Message carries the buyer's flavor reason. Terminal.
	PayLedgerStateWithdrawnByBuyer PayLedgerState = "withdrawn_by_buyer"

	// PayLedgerStateExpired — pending TTL elapsed. The aging sweep
	// flips the state and emits PayWithItemResolved{Expired}; an
	// accept_pay arriving past expiry also drives this flip in-band
	// rather than waiting for the next sweep tick. Terminal.
	PayLedgerStateExpired PayLedgerState = "expired"

	// PayLedgerStateFailedInsufficientFunds — accept-time revalidation
	// found buyer.Coins < entry.Amount. Buyer-side material failure.
	// Terminal.
	PayLedgerStateFailedInsufficientFunds PayLedgerState = "failed_insufficient_funds"

	// PayLedgerStateFailedInsufficientStock — accept-time revalidation
	// found seller.Inventory[ItemKind] < Qty * effectiveConsumerCount.
	// Seller-side material failure. Stock is revalidated BEFORE funds
	// per the S4 design pass (seller-knowable state checked first on
	// the seller's tick). Terminal.
	PayLedgerStateFailedInsufficientStock PayLedgerState = "failed_insufficient_stock"

	// PayLedgerStateFailedUnavailable — umbrella for "social context
	// broken at accept time": seller on break, item kind deprecated
	// from catalog, consumers left the huddle, co-presence lost between
	// offer creation and accept. Terminal.
	PayLedgerStateFailedUnavailable PayLedgerState = "failed_unavailable"
)

// PayTerminalState is PayLedgerState minus pending — the eight terminal
// values. Used on the PayWithItemResolved event's TerminalState field
// so the type signature documents the invariant that the event never
// carries pending. Same underlying string values as PayLedgerState; the
// split is a compile-time enforcement, not a runtime conversion.
type PayTerminalState string

const (
	PayTerminalStateAccepted                PayTerminalState = "accepted"
	PayTerminalStateDeclined                PayTerminalState = "declined"
	PayTerminalStateCountered               PayTerminalState = "countered"
	PayTerminalStateWithdrawnByBuyer        PayTerminalState = "withdrawn_by_buyer"
	PayTerminalStateExpired                 PayTerminalState = "expired"
	PayTerminalStateFailedInsufficientFunds PayTerminalState = "failed_insufficient_funds"
	PayTerminalStateFailedInsufficientStock PayTerminalState = "failed_insufficient_stock"
	PayTerminalStateFailedUnavailable       PayTerminalState = "failed_unavailable"
)

// PayLedgerTTLDefault is the default Time-To-Live for a pending entry
// when WorldSettings.PayLedgerTTL is unset (zero). 3 minutes — middle
// of architecture § 3's 2-5 minute range. Tight enough that an
// abandoned offer doesn't clog the seller's perception list past the
// conversational moment; loose enough that a deliberating NPC has
// time to weigh + speak + accept across a couple of reactor ticks.
// Tunable via settings.
const PayLedgerTTLDefault = 3 * time.Minute

// PayLedgerSweepCadenceDefault is the default periodic-sweep cadence
// when WorldSettings.PayLedgerSweepCadence is unset (zero). 60s gives
// ±60s expiry latency against the 3-minute TTL — invisible at
// gameplay scale. Matches scene_quote sweep cadence (SceneQuoteSweepCadenceDefault)
// so admin tuning sees one mental model across the two substrates.
const PayLedgerSweepCadenceDefault = 60 * time.Second

// PayLedgerEntry is the in-memory state of one buyer-staked pay offer.
// Lives in World.PayLedger keyed by ID. Survives checkpoint as part of
// world state (per architecture § 5 — pay_ledger is part of the world,
// Postgres is a best-effort projection via PayLedgerSink).
//
// Single-Message-three-meanings field choice: Message holds the
// counter message (when State == Countered), the decline reason (when
// State == Declined), or the withdraw note (when State == WithdrawnByBuyer).
// Context is fully recoverable from State; separate fields per kind
// would proliferate without payoff.
//
// Both SceneID and HuddleID stamped: SceneID anchors closed-shop /
// quote-matching context (which persists across huddle churn); HuddleID
// anchors accept-time co-presence (both buyer and seller must still be
// in entry.HuddleID at accept time, per architecture § 3). They diverge:
// a huddle can dissolve while the scene persists.
//
// QuoteID denormalized for cheap restart re-stamping and admin lookups
// without joining through SourceEventID. Zero when no quote was
// referenced (slow-path offer).
//
// ParentID + Depth carry the counter chain. ParentID is zero for a root
// offer; Depth is 0 for root and parent.Depth+1 for an in_response_to
// child. The chain terminates at the first non-Countered terminal
// entry. No enforced depth cap at S4; Depth exists for telemetry and a
// possible future cap.
type PayLedgerEntry struct {
	ID LedgerID

	BuyerID  ActorID
	SellerID ActorID

	// ItemKind / Qty / ConsumeNow / ConsumerIDs describe what's being
	// offered. For coin-only pays (the existing PR B pay handler — not
	// the buyer of items), ItemKind is empty and Qty is 0; PR B's flow
	// does NOT use this struct (it's a different commerce surface that
	// predates the ledger substrate).
	ItemKind    ItemKind
	Qty         int
	ConsumeNow  bool
	ConsumerIDs []ActorID

	// Amount is the offered total in coins. Always > 0 at intake (the
	// pay_with_item handler decode rejects 0/negative). For a countered
	// entry, Amount is the buyer's ORIGINAL offer; CounterAmount is the
	// seller's counter-proposal terms.
	Amount int

	// QuoteID is non-zero when the buyer's pay_with_item call referenced
	// a quote_id. Zero for a slow-path offer. Denormalized — cheap to
	// store, makes restart re-stamp + admin lookups straightforward.
	QuoteID QuoteID

	// ParentID is the LedgerID of the parent offer in the counter chain.
	// Zero for a root offer (no in_response_to). When non-zero, this
	// entry was created by pay_with_item(in_response_to=ParentID, ...)
	// after the parent was countered.
	ParentID LedgerID

	// Depth is the counter-chain depth — 0 for a root entry, parent.Depth+1
	// for an in_response_to child. Telemetry today; reserves room for a
	// future chain cap if degenerate counter loops appear in the wild.
	Depth int

	// CounterAmount is populated only when State == Countered. Carries
	// the seller's counter-proposal price; the buyer's optional response
	// is a fresh entry with this entry as ParentID.
	CounterAmount int

	// Message is the free-text payload whose meaning is driven by State:
	//   - Countered          → seller's counter message
	//   - Declined           → seller's decline reason
	//   - WithdrawnByBuyer   → buyer's withdraw note
	// Empty in every other state. Length-capped at handler intake
	// (MaxPayMessageRunes when those handlers land); model-controlled
	// text is render-escaped before showing up in another actor's
	// prompt to prevent prompt-injection across actors.
	Message string

	State PayLedgerState

	CreatedAt  time.Time
	ResolvedAt time.Time // zero while State == Pending
	ExpiresAt  time.Time // pending TTL boundary

	// Causal trail. RootEventID is the cascade root the
	// PayOfferReceived event was emitted under; SourceEventID is the
	// PayOfferReceived event's own ID. Both are zero for a LoadWorld
	// restart re-stamp (the original event is gone; the entry survives
	// and is re-engaged via warrant). The pay-offer warrant uses
	// uint64(LedgerID) as its DedupDiscriminator so re-stamp dedupes
	// against the normal-flow stamp — see reactor.go's WarrantReason
	// interface contract.
	RootEventID   EventID
	SourceEventID EventID

	// Co-presence context captured at entry creation. SceneID stays
	// stable through huddle churn; HuddleID is the active huddle both
	// parties shared at offer-creation time. accept_pay revalidates
	// both buyer and seller are still in HuddleID at accept time
	// (architecture § 3 — co-presence is a huddle property, not a
	// scene property).
	SceneID  SceneID
	HuddleID HuddleID
}

// ClonePayLedgerEntry returns a deep copy suitable for the published
// Snapshot or the mem-repo serialization boundary. The ConsumerIDs
// slice is cloned so snapshot readers can't reach back into world
// state through it. Returns nil for a nil input.
func ClonePayLedgerEntry(e *PayLedgerEntry) *PayLedgerEntry {
	if e == nil {
		return nil
	}
	cp := *e
	if e.ConsumerIDs != nil {
		cp.ConsumerIDs = make([]ActorID, len(e.ConsumerIDs))
		copy(cp.ConsumerIDs, e.ConsumerIDs)
	}
	return &cp
}

// nextLedgerSeq increments the per-run LedgerID counter and returns the
// new identifier. World-goroutine-only — called exclusively from
// Command.Fn callsites that mint a PayLedgerEntry. The counter starts
// at 0, so the first minted entry gets ID 1; LedgerID(0) is reserved
// as the unset sentinel (also used by ParentID and QuoteID fields to
// mean "no parent" / "no quote referenced").
func (w *World) nextLedgerSeq() LedgerID {
	w.payLedgerSeq++
	return LedgerID(w.payLedgerSeq)
}

// effectivePayLedgerTTL returns the configured TTL or the default when
// WorldSettings.PayLedgerTTL is zero/unset (tests that bypass repo
// loading don't seed it).
func effectivePayLedgerTTL(s WorldSettings) time.Duration {
	if s.PayLedgerTTL > 0 {
		return s.PayLedgerTTL
	}
	return PayLedgerTTLDefault
}

// effectivePayLedgerSweepCadence returns the configured sweep cadence
// or the default when WorldSettings.PayLedgerSweepCadence is zero/unset.
func effectivePayLedgerSweepCadence(s WorldSettings) time.Duration {
	if s.PayLedgerSweepCadence > 0 {
		return s.PayLedgerSweepCadence
	}
	return PayLedgerSweepCadenceDefault
}

// restartExpirePendingEntries walks World.PayLedger at LoadWorld time
// and flips any pending entry whose ExpiresAt has already passed to
// the terminal Expired state. No PayWithItemResolved event is emitted —
// the original PayOfferReceived cascade root is gone, so an emit here
// would have no causal anchor (mirrors the scene-quote
// restartExpireScannedQuotes posture).
//
// DORMANT BY DESIGN: pending pay_ledger entries are intentionally
// restart-lossy (decided 2026-05-20 — see
// work/tasks/payledger-restart-lossy/decision). w.PayLedger always
// starts empty, so this never has entries to expire today. Kept
// because it encodes correct behavior if the decision is ever
// reversed (a PayLedgerRepo is added).
//
// MUST be called from inside LoadWorld (single-threaded, before the
// aging sweep starts), or from inside a Command.Fn. ResolvedAt is
// stamped with `now` to give admin queries an honest
// "found-expired-at-load" timestamp distinct from CreatedAt.
//
// Non-pending entries are skipped (terminal states are inert). Pending
// entries with ExpiresAt in the future are left for the aging sweep —
// the sweep starts normally and picks them up at TTL.
func restartExpirePendingEntries(w *World, now time.Time) {
	if w == nil {
		return
	}
	for _, e := range w.PayLedger {
		if e == nil || e.State != PayLedgerStatePending {
			continue
		}
		if !e.ExpiresAt.IsZero() && !now.Before(e.ExpiresAt) {
			e.State = PayLedgerStateExpired
			e.ResolvedAt = now
		}
	}
}

// PayLedgerSink is a best-effort projection of pay-ledger state to an
// external store (admin / audit / Postgres pay_ledger table).
// Implementations MUST NOT block the world goroutine — the typical
// impl pushes onto a buffered channel and drains in a separate
// goroutine. Errors are implementation-handled; sink failures never
// propagate to commands.
//
// Project is invoked on every state transition: once at entry
// creation (initial pending insert) and again at every terminal flip
// (accepted / declined / countered / withdrawn_by_buyer / expired /
// failed_*). Each call carries the full post-transition entry; the
// impl handles upsert internally keyed on LedgerID.
//
// Drift-recovery is the admin tool's responsibility — periodically
// compare Snapshot.PayLedger against the projection store and upsert
// missing rows. The in-memory PayLedger is the source of truth;
// projection failure is bounded to "admin view stale," never "data
// lost from the world." Scheduled with the admin-dashboard PR; not
// in scope for S4.
//
// nullPayLedgerSink{} is the default installed by NewWorld so
// substrate tests + non-production builds run without a real
// projection target wired up.
type PayLedgerSink interface {
	Project(entry PayLedgerEntry)
}

// nullPayLedgerSink is the no-op default sink. Installed by NewWorld
// and restored by SetPayLedgerSink(nil) so the world is never carrying
// a nil sink — every Project call site can invoke without a nil guard.
type nullPayLedgerSink struct{}

func (nullPayLedgerSink) Project(PayLedgerEntry) {}

// restartReStampPayOfferWarrants walks World.PayLedger and stamps
// PayOfferWarrantReason on the seller for every still-pending entry.
// Phase 3 PR S4 step 7 — the load-bearing use case for the
// DedupDiscriminator interface migration.
//
// DORMANT BY DESIGN: pending pay_ledger entries are intentionally
// restart-lossy (decided 2026-05-20 — see
// work/tasks/payledger-restart-lossy/decision), so w.PayLedger is
// always empty here and there is nothing to re-stamp. Kept because it
// documents the rationale the DedupDiscriminator migration exists to
// support: were entries reloaded, this re-stamp keyed on
// uint64(LedgerID) would dedupe cleanly against a normal-flow
// PayOfferReceived emit firing after it.
//
// MUST be called from inside LoadWorld AFTER restartExpirePendingEntries
// (so already-expired pendings are skipped) and AFTER subscribers have
// been registered (so a future cascade-driven re-stamp dedupes against
// these load-time stamps). Today's LoadWorld calls this with no
// subscribers registered yet — the cascade-driven path runs on every
// normal-flow PayOfferReceived emit AFTER LoadWorld returns and
// handlers register; the dedup interlock relies on
// (WarrantKindPayOffer, LedgerID) being stable across both flows.
//
// The stamp uses SourceEventID=0 + RootEventID=0 (the original
// PayOfferReceived event is gone post-restart, but the LedgerID-based
// DedupDiscriminator still gives the stamp a non-zero discriminator,
// so it participates in dedup normally). Calling LoadWorld twice on
// the same checkpoint produces identical WarrantSourceKey{Kind:
// PayOffer, Discriminator: LedgerID}, and the second pass's stamps
// are dropped at the open-cycle dedup gate.
//
// Skips entries whose seller no longer exists in the world (caller bug
// or repo drift — defensive). Skips entries with empty SellerID
// (defensive — substrate intake gates this). Non-pending entries are
// silently skipped (terminal entries don't need a warrant).
func restartReStampPayOfferWarrants(w *World, now time.Time) {
	if w == nil {
		return
	}
	for _, e := range w.PayLedger {
		if e == nil || e.State != PayLedgerStatePending {
			continue
		}
		if e.SellerID == "" {
			continue
		}
		seller, ok := w.Actors[e.SellerID]
		if !ok || seller == nil {
			continue
		}
		meta := WarrantMeta{
			TriggerActorID: e.BuyerID,
			Force:          false,
			Reason: PayOfferWarrantReason{
				LedgerID:    e.ID,
				Buyer:       e.BuyerID,
				Item:        e.ItemKind,
				Qty:         e.Qty,
				Amount:      e.Amount,
				ConsumeNow:  e.ConsumeNow,
				ConsumerIDs: append([]ActorID(nil), e.ConsumerIDs...),
				ExpiresAt:   e.ExpiresAt,
			},
			// SourceEventID/RootEventID intentionally zero — the
			// original PayOfferReceived event no longer exists.
			// Dedup keys off the Reason's DedupDiscriminator
			// (uint64(LedgerID)), which IS stable across restart;
			// see WarrantReason interface contract.
			SourceActorID: e.BuyerID,
			HuddleID:      e.HuddleID,
			SceneID:       e.SceneID,
			OccurredAt:    e.CreatedAt,
		}
		tryStampWarrant(w, seller, meta, now)
	}
}
