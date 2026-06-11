package sim

import (
	mathrand "math/rand/v2"
	"time"
)

// Reactor primitive — warrant-driven evaluator (Phase 2 PR 2).
//
// Replaces v1's heap-based reactor_scheduler.go (269 LOC) with a state-as-
// queue design: Actor.WarrantedSince is the source of truth, the evaluator
// scans warranted actors on a coalesced cadence and emits ReactorTickDue
// events for those whose jitter window has elapsed.
//
// Why warrant-driven over heap-driven: v1's heap is a parallel queue that
// can desync from the actor's actual state — every pop must re-check the
// warrant, at which point the heap is just an optimization over scanning.
// At village scale (50-100 actors) the scan is microseconds inside the
// world goroutine. No merge logic, no index map, no heap.Fix; the warrant
// IS the queue.
//
// Critical invariants:
//
//   - Warrants are consumed at EMIT time, NOT at LLM completion. The LLM
//     call takes seconds; events arriving during that window stamp fresh
//     warrants that fire after the current tick completes. Clearing on
//     completion would lose any signal that arrived mid-call (stampWarrant
//     no-ops on already-warranted actors). See WarrantMeta.
//
//   - TickAttemptID is a generation, not just a bool. A timed-out attempt
//     completing late must not clear a newer attempt's in-flight flag.
//
//   - Warrants are ephemeral. LoadWorld wipes Warrants / WarrantedSince /
//     WarrantDueAt / TickInFlight / TickAttemptID. Cascade origins re-
//     engage actors via fresh events post-restart. No interface-typed
//     fields cross the checkpoint serialization boundary.

// WarrantKind discriminates the reason an actor's tick was warranted.
// Typed string so log output and tests stay readable. Open set — adding a
// new kind is a one-line append; consumers SHOULD include a default branch
// so unknown kinds don't break them.
type WarrantKind string

const (
	WarrantKindUnknown            WarrantKind = ""
	WarrantKindPCSpoke            WarrantKind = "pc_spoke"
	WarrantKindNPCSpoke           WarrantKind = "npc_spoke"
	WarrantKindHuddleJoined       WarrantKind = "huddle_joined"      // the joiner
	WarrantKindHuddlePeerJoined   WarrantKind = "huddle_peer_joined" // prior members
	WarrantKindHuddleLeft         WarrantKind = "huddle_left"        // the leaver
	WarrantKindHuddlePeerLeft     WarrantKind = "huddle_peer_left"   // remaining members
	WarrantKindHuddleConcluded    WarrantKind = "huddle_concluded"   // evicted members
	WarrantKindArrived            WarrantKind = "arrived"
	WarrantKindNeedThreshold      WarrantKind = "need_threshold"
	WarrantKindShiftDuty          WarrantKind = "shift_duty"
	WarrantKindRestock            WarrantKind = "restock"
	WarrantKindIdleBackstop       WarrantKind = "idle_backstop"
	WarrantKindPaid               WarrantKind = "paid"
	WarrantKindSceneQuoteTargeted WarrantKind = "scene_quote_targeted"
	WarrantKindPayOffer           WarrantKind = "pay_offer"
	WarrantKindPayResolved        WarrantKind = "pay_resolved"
	WarrantKindDwellStarted       WarrantKind = "dwell_started"
	WarrantKindDwellTickApplied   WarrantKind = "dwell_tick_applied"
	WarrantKindDwellEnded         WarrantKind = "dwell_ended"
	WarrantKindConsumed           WarrantKind = "consumed" // immediate consume self-narration beat
	WarrantKindAdmin              WarrantKind = "admin"    // operator forced a bare tick
	WarrantKindImpulse            WarrantKind = "impulse"  // operator-injected in-world felt impulse (umbilical directive nudge)
)

// WarrantReason is the marker interface for kind-specific warrant payloads.
// Each concrete reason carries its own data and reports its Kind so the
// kind discriminator and payload can't drift apart (no separate Kind field
// on WarrantMeta — single source of truth).
//
// The marker is unexported on purpose — external packages cannot satisfy
// it, so the set of warrant reasons is closed at the sim package boundary.
//
// PR 2 ships two concrete reasons:
//   - BasicWarrantReason for kinds without extra payload (most current callers).
//   - PCSpeechWarrantReason / NPCSpeechWarrantReason for speech-triggered
//     warrants — the speak handler subsystem (Phase 3 PR A) emits the Spoke
//     event whose subscriber mints these.
//
// Future reasons (ArrivalWarrantReason, ProductionWarrantReason, etc.) land
// in the PRs that introduce their producer subsystems.
//
// DedupDiscriminator returns the uint64 used alongside Kind in
// WarrantSourceKey for tryStampWarrant's three dedup paths. Each Reason
// returns its inherent identity field (SpeechID / PaidID / AttemptID /
// QuoteID / LedgerID — all uint64-typed engine mints). Reasons without
// an inherent identity (BasicWarrantReason for lifecycle stamps) return
// 0 — the "not event-sourced" sentinel that bypasses dedup, since
// (Kind, 0) would collapse unrelated warrants. The per-Reason
// discriminator scheme (vs the original meta.SourceEventID scheme)
// supports restart-stable dedup for aggregate-keyed reasons like
// PayOfferWarrantReason (PR S4), where the source event is gone after
// LoadWorld but the aggregate ID survives in world state.
type WarrantReason interface {
	isWarrantReason()
	Kind() WarrantKind
	DedupDiscriminator() uint64
}

// BasicWarrantReason is the catch-all reason for warrant kinds that don't
// carry kind-specific data beyond what WarrantMeta already has
// (TriggerActorID, Force). Most current huddle-event warrants use this.
type BasicWarrantReason struct {
	K WarrantKind
}

func (BasicWarrantReason) isWarrantReason()           {}
func (r BasicWarrantReason) Kind() WarrantKind        { return r.K }
func (BasicWarrantReason) DedupDiscriminator() uint64 { return 0 }

// PCSpeechWarrantReason captures speech by a PC (player character) that
// warranted the listening NPC's tick. NPC-spoken warrants use the parallel
// NPCSpeechWarrantReason. The two are split rather than unified-with-a-
// kind-field for the same reason events.go has separate ActorMoved /
// ActorArrived / ActorMet types instead of one generic Event{Kind}: it
// matches the type-per-kind discriminated-union pattern used elsewhere,
// makes the kind a compile-time guarantee rather than a runtime check, and
// lets PC- vs NPC-specific fields diverge cleanly if future PRs need them.
//
// SpeechID aliases the source event's EventID — the speak handler emits a
// Spoke event whose EventID is the canonical identifier; the speech
// reactor subscriber copies that EventID into both SpeechID (on the
// warrant payload) and SourceEventID (on the warrant meta). One number
// flows through event, payload, and dedup key, so logs and replay tooling
// trace a single cascade by one ID.
//
// Excerpt is the speech text truncated to MaxSalientFactTextLen runes —
// other actors' perception prompts re-render this on every reactor tick
// they consume, so bounding the excerpt at warrant-stamp time bounds the
// per-tick prompt cost. The raw (1000-char-capped, control-char-rejected)
// text travels on the Spoke event for any consumer that wants the full
// utterance.
type PCSpeechWarrantReason struct {
	SpeechID SpeechID
	Speaker  ActorID
	Excerpt  string
}

func (PCSpeechWarrantReason) isWarrantReason()             {}
func (PCSpeechWarrantReason) Kind() WarrantKind            { return WarrantKindPCSpoke }
func (r PCSpeechWarrantReason) DedupDiscriminator() uint64 { return uint64(r.SpeechID) }

// NPCSpeechWarrantReason captures speech by an NPC that warranted the
// listening peer NPC's tick. Parallel to PCSpeechWarrantReason; see that
// type's doc for the SpeechID / Excerpt invariants.
type NPCSpeechWarrantReason struct {
	SpeechID SpeechID
	Speaker  ActorID
	Excerpt  string
}

func (NPCSpeechWarrantReason) isWarrantReason()             {}
func (NPCSpeechWarrantReason) Kind() WarrantKind            { return WarrantKindNPCSpoke }
func (r NPCSpeechWarrantReason) DedupDiscriminator() uint64 { return uint64(r.SpeechID) }

// SpeechID is a stable identifier for a single speech utterance. The
// speech reactor subscriber copies the source Spoke event's EventID into
// the SpeechID — same one-ID-flows-through-everything pattern as
// PaidID / QuoteID / LedgerID. v1 used a UUID-string shape; v2 normalizes
// to the engine's uint64 event sequence for LLM readback reliability.
type SpeechID uint64

// PaidWarrantReason captures a pay transaction that warranted the seller's
// tick. Phase 3 PR B — pure coin transfer slice. The pay handler emits a
// Paid event whose subscriber mints this reason on the seller; the seller's
// next reactor tick can then speak thanks, walk over, or otherwise respond.
//
// PaidID aliases the source Paid event's EventID — same one-ID-flows-
// through-everything pattern as SpeechID. The pay reactor subscriber copies
// EventID into both PaidID (on the warrant payload) and SourceEventID /
// RootEventID (on the warrant meta), giving free dedup via the (Kind,
// SourceEventID) source key.
//
// Buyer is the actor whose pay tool call triggered this warrant — surfaces
// to the seller's perception prompt as the source of the payment.
//
// Amount is the coin total transferred (always > 0 — the handler decode
// rejects zero/negative).
//
// ForText is the buyer's flavor text rune-truncated to MaxSalientFactTextLen
// — the seller's perception prompt re-renders the excerpt on every reactor
// tick they consume, so bounding the excerpt at warrant-stamp time bounds
// the per-tick prompt cost. The raw (200-char-capped, control-char-rejected)
// text travels on the Paid event for any consumer that wants the full
// flavor.
//
// No PC/NPC split for now — pay warrants only fire on NPC sellers (PCs
// don't deliberate). When a PC-as-recipient flow lands, split type-per-kind
// the same way speech split into PCSpeechWarrantReason / NPCSpeechWarrantReason.
type PaidWarrantReason struct {
	PaidID  EventID
	Buyer   ActorID
	Amount  int
	ForText string
}

func (PaidWarrantReason) isWarrantReason()             {}
func (PaidWarrantReason) Kind() WarrantKind            { return WarrantKindPaid }
func (r PaidWarrantReason) DedupDiscriminator() uint64 { return uint64(r.PaidID) }

// SceneQuoteTargetedWarrantReason captures a vendor-posted scene quote
// directly addressed to this actor. Phase 3 PR S3 — the quote handler
// emits a SceneQuoteCreated event whose subscriber mints this reason on
// the TargetBuyer when TargetBuyer is an NPC. PCs receive targeted
// quotes via Snapshot.Quotes + per-scene QuoteIDs index in the client's
// perception (PCs don't deliberate, so a warrant on a PC would be inert).
//
// Public quotes (no target buyer) do NOT stamp warrants on anyone —
// they're surfaced at perception build via the pull-based render path,
// not via reactor activation. The asymmetry is deliberate: a public
// quote is a passive ad and stamping warrants on every buyer in scene
// would flood the reactor with low-signal activations.
//
// QuoteID aliases the SceneQuote's ID — same one-ID-flows-through-
// everything pattern as PaidID/SpeechID. The subscriber copies the
// quote's ID into both this payload AND the WarrantMeta's
// SourceEventID (pre-§8 dedup scheme — uses the SceneQuoteCreated
// event's EventID, NOT the QuoteID, as SourceEventID; quote warrants
// are restart-noncritical so they ride the existing
// (Kind, SourceEventID) discriminator pattern rather than the
// restart-stable scheme ledger-substrate-design § 8 designs for
// pay-offer warrants).
//
// Amount/Qty/ConsumeNow/ItemKind/ExpiresAt all travel on the warrant
// payload so the buyer's tick prompt can render the offer terms
// directly off WarrantMeta without a separate World.Quotes lookup
// (the prompt builder runs off the published Snapshot off the world
// goroutine; pulling the live quote at prompt time would race).
type SceneQuoteTargetedWarrantReason struct {
	QuoteID    QuoteID
	SellerID   ActorID
	ItemKind   ItemKind
	Qty        int
	Amount     int
	ConsumeNow bool
	ExpiresAt  time.Time
}

func (SceneQuoteTargetedWarrantReason) isWarrantReason()             {}
func (SceneQuoteTargetedWarrantReason) Kind() WarrantKind            { return WarrantKindSceneQuoteTargeted }
func (r SceneQuoteTargetedWarrantReason) DedupDiscriminator() uint64 { return uint64(r.QuoteID) }

// PayOfferWarrantReason captures a pending pay-with-item offer staked
// against this actor (the seller). Phase 3 PR S4 — the pay-with-item
// handler emits a PayOfferReceived event whose subscriber mints this
// reason on the seller so their next reactor tick perceives the
// offer terms and decides among accept_pay / decline_pay /
// counter_pay.
//
// Restart-stable dedup: DedupDiscriminator returns uint64(LedgerID),
// not the source event ID. LoadWorld walks World.PayLedger and
// re-stamps PayOfferWarrant on every still-pending entry's seller —
// the original PayOfferReceived event is gone post-restart, but the
// aggregate LedgerID survives, which is exactly what the PR S4
// WarrantReason interface migration exists to support. Normal-flow
// stamp also keys on LedgerID, so the two paths dedupe cleanly
// against each other (calling LoadWorld twice on the same checkpoint
// doesn't produce duplicate warrants).
//
// All offer terms travel on the warrant payload (LedgerID + Buyer +
// item terms + ExpiresAt) so the seller's tick prompt builder can
// render the offer directly off WarrantMeta without a separate
// World.PayLedger lookup. Same posture as SceneQuoteTargetedWarrantReason
// — prompt build runs on the world goroutine via Snapshot, and pulling
// the live ledger entry at prompt time would race the world's owning
// goroutine.
//
// Buyer is the actor whose pay_with_item tool call staked the offer.
// Surfaces in the seller's perception prompt as the offerer.
//
// ConsumerIDs is the group-order participant set (empty for a
// sole-buyer offer; buyer is the implicit single consumer in that
// case). Length-capped at handler intake (architecture § 9 caps at 8).
//
// No fast-path / quote_id field on this reason — the fast path skips
// the pending state entirely, so a quote-matched offer never
// produces a PayOfferReceived event or a PayOfferWarrant. Slow-path
// offers may reference a quote_id that failed fast-path gates, but
// at that point the buyer's tool call returns an error rather than
// falling through to pending (architecture § 4 "strict reject — no
// silent fall-through").
//
// No PC variant — PCs don't deliberate via the reactor loop, so a
// PC-as-seller flow uses a different (UI-driven) decision surface.
// PR S4 ships NPC-seller-only; PC-as-seller lands at the cutover-layer
// PC commit path.
type PayOfferWarrantReason struct {
	LedgerID LedgerID
	Buyer    ActorID
	Item     ItemKind
	Qty      int
	Amount   int
	// PayItems are the goods the buyer offered to pay WITH (barter leg,
	// ZBBS-HOME-393). Empty for a pure-coin offer. Carried on the warrant
	// so the seller's offer-decision section (renderPayOffers) can show
	// the goods terms the seller is judging, without a live ledger lookup.
	PayItems    []ItemKindQty
	ConsumeNow  bool
	ConsumerIDs []ActorID
	ExpiresAt   time.Time
	// Depth is the offer's counter-chain depth (the source ledger entry's
	// Depth — 0 for a root offer, parent.Depth+1 for an in_response_to
	// response). Carried on the warrant so the seller-side tool gate can
	// drop counter_pay for an offer already at MaxPayCounterChainDepth (the
	// buyer can no longer answer a counter — validateInResponseTo rejects
	// parent.Depth >= cap), without a live ledger lookup at gate time.
	// ZBBS-WORK-320 (pc/pay scar #4 seller-side gating).
	Depth int
}

func (PayOfferWarrantReason) isWarrantReason()             {}
func (PayOfferWarrantReason) Kind() WarrantKind            { return WarrantKindPayOffer }
func (r PayOfferWarrantReason) DedupDiscriminator() uint64 { return uint64(r.LedgerID) }

// PayResolvedWarrantReason captures the buyer-side resolution of a
// pay-with-item offer. Phase 3 PR S4 step 7 — the pay-resolved
// subscriber emits this on the buyer when PayWithItemResolved or
// PayCountered fires, so the buyer's next reactor tick perceives the
// outcome (accept / decline / counter / expire / failed_*) and can
// follow up via speak, in_response_to chain, etc.
//
// Dedup key uses ResolvedEventID (the PayWithItemResolved /
// PayCountered event's ID — same one-ID-flows-through-everything
// pattern as Spoke/Paid/SceneQuoteCreated). Restart-noncritical:
// resolution warrants fire once per terminal transition, and LoadWorld
// wipes ephemeral warrant state. If a buyer was about to address a
// resolution and the world restarted, the resolution itself is
// preserved in PayLedger state — the buyer can re-discover via
// perception render off Snapshot.PayLedger on their next tick rather
// than via a fresh warrant (contrast PayOfferWarrantReason, which
// re-stamps at restart because a missed seller warrant would mean the
// offer sits forgotten).
//
// Seller is the actor who drove the resolution (accept_pay /
// decline_pay / counter_pay caller). Empty for terminal states the
// buyer themselves drove (withdraw_pay — the resolution subscriber
// skips those, see the buyer-driven-skip in
// handlePayResolvedWarrants). Also empty for expired / failed_*
// (aging sweep / AcceptPay revalidation drives those, not a specific
// actor).
//
// Message carries the seller's counter / decline note (already
// rune-truncated at PayLedgerEntry intake). CounterAmount is populated
// only when TerminalState == PayTerminalStateCountered.
//
// No PC variant — PCs don't deliberate via the reactor loop. PC-as-
// buyer resolution surfaces via the client's perception against
// Snapshot.PayLedger, not via warrant.
type PayResolvedWarrantReason struct {
	LedgerID      LedgerID
	Seller        ActorID
	ItemKind      ItemKind
	Qty           int
	Amount        int
	TerminalState PayTerminalState
	Message       string
	CounterAmount int
	// CounterPayItems are the goods the seller demands in a counter
	// (symmetric-barter counter, ZBBS-HOME-393). Populated only when
	// TerminalState == Countered and the seller countered with goods
	// terms. Lets the buyer's perception render the counter's goods
	// without a ledger lookup.
	CounterPayItems []ItemKindQty
	ResolvedEventID EventID
}

func (PayResolvedWarrantReason) isWarrantReason()             {}
func (PayResolvedWarrantReason) Kind() WarrantKind            { return WarrantKindPayResolved }
func (r PayResolvedWarrantReason) DedupDiscriminator() uint64 { return uint64(r.ResolvedEventID) }

// IdleBackstopWarrantReason captures an engine-injected liveness tick —
// the actor has been quiet for longer than WorldSettings.IdleBackstopThreshold
// (measured against max(lastReactorTickAt, World.LoadedAt)), so the
// idle-backstop sweep (engine/sim/cascade/idle_backstop.go) stamps this
// warrant to give them a chance to act on their own initiative.
//
// Replaces v1's chronicler-attend-to dispatch role: in v1 the chronicler
// LLM decided who to engage; in v2 a cheap periodic sweep stamps idle
// warrants on quiet actors and the actor's own LLM tick decides what (if
// anything) to do.
//
// QuietDuration is the wall-clock duration since the actor's last
// reactor tick at the moment the warrant was stamped, so perception can
// render meaningful context ("you've been quiet for 32 minutes —
// consider what to do next"). Carried as duration not timestamps to
// keep the rendering deterministic across runs.
//
// Not event-sourced: idle backstop has no source event (it fires from
// the absence of activity, not a specific stimulus). WarrantMeta is
// stamped with SourceEventID = 0 and the substrate dedup paths are
// bypassed by design — the cascade slice does cheap pre-filter against
// already-pending actors (open WarrantedSince / TickInFlight) on the
// world goroutine before stamping. DedupDiscriminator returns 0 to
// match the zero-source posture; per-cycle dedup against an open
// IdleBackstop warrant on the same actor still works via the open-
// cycle path because the slice's pre-filter rejects already-warranted
// actors outright.
type IdleBackstopWarrantReason struct {
	QuietDuration time.Duration
}

func (IdleBackstopWarrantReason) isWarrantReason()           {}
func (IdleBackstopWarrantReason) Kind() WarrantKind          { return WarrantKindIdleBackstop }
func (IdleBackstopWarrantReason) DedupDiscriminator() uint64 { return 0 }

// AdminDirectiveWarrantReason carries an operator-authored directive injected
// via the umbilical /nudge route (ZBBS-WORK-329 — the "if you see an NPC stuck,
// prompt it home" capability). It rides the same warrant-reason → perception
// render rail as the autonomous producers (shift-duty, restock): the operator's
// Message surfaces in the forced tick's "## What just happened" section, framed
// as an in-world felt impulse rather than an out-of-world meta-instruction (see
// perception.renderImpulseWarrantLine). Kind is WarrantKindImpulse — a distinct,
// in-world-neutral tag so the rendered line reads as a feeling, not the bare
// admin force-tick (WarrantKindAdmin) the message-less nudge still uses.
//
// Message is untrusted operator free text; the renderer sanitizes + caps it.
//
// One-shot: the directive lives only for the single forced tick it is stamped
// on and clears when that warrant cycle completes — it is not a sticky standing
// order. Not event-sourced (the operator's manual invocation is the sole
// trigger), so DedupDiscriminator returns 0, matching the other zero-sourced
// reasons; a second /nudge is a deliberate re-stamp, not an accidental dup.
type AdminDirectiveWarrantReason struct {
	Message string
}

func (AdminDirectiveWarrantReason) isWarrantReason()           {}
func (AdminDirectiveWarrantReason) Kind() WarrantKind          { return WarrantKindImpulse }
func (AdminDirectiveWarrantReason) DedupDiscriminator() uint64 { return 0 }

// WarrantMeta is one entry in an actor's Warrants list — a signal that
// fired during the actor's warranted window. The evaluator carries the
// full list into ReactorTickDue; the prompt builder (PR 3) renders each
// entry to surface what the actor should address.
//
// Force=true bypasses the per-minute gross gate at emit time (used for
// admin overrides and emergency reasons). Idempotency: multiple stamps in
// the same warrant cycle accumulate the list; the earliest WarrantedSince
// / WarrantDueAt are preserved.
type WarrantMeta struct {
	TriggerActorID ActorID
	Force          bool
	Reason         WarrantReason

	// PR 3a source metadata — makes a warrant causally identifiable so
	// PR 3's perception can resolve the warrant's scene without reverse-
	// scanning, and admin replay can trace cascade lineage. All value-
	// typed (plain IDs with empty sentinels, no pointers) so CloneActor's
	// shallow Warrants copy stays correct.
	//
	// SourceEventID is the exact event that produced this warrant. Carried
	// as lineage metadata for perception/debug only; the dedup key now
	// comes from the Reason itself via Reason.DedupDiscriminator() (PR S4)
	// to support restart-stable dedup for aggregate-keyed reasons like
	// PayOfferWarrantReason. SourceEventID stays populated by PR 3
	// perception callsites for prompt-render and admin-replay lookups; a
	// zero SourceEventID still marks a warrant as not-event-sourced per
	// the zero-lineage invariant below.
	SourceEventID EventID
	// RootEventID is a copy of the source event's causal root. Never a
	// dedup key — distinct SourceEventIDs under the same root are distinct
	// developments and must each stamp.
	RootEventID EventID
	// SourceActorID is the actor whose action produced the source event.
	// Empty = none / bulk (e.g. a force-conclude eviction with no single
	// trigger).
	SourceActorID ActorID
	// HuddleID / SceneID scope the warrant; empty = none. SceneID is load-
	// bearing — it is step 1 of PR 3's scene-resolution order.
	HuddleID HuddleID
	SceneID  SceneID
	// OccurredAt is the source event's wall-clock timestamp. Display /
	// debug metadata only — EventID is the authoritative causal order.
	OccurredAt time.Time
}

//
// Zero-lineage invariant (PR 3a): a warrant either carries FULL event
// lineage (SourceEventID != 0, with the rest of the source fields
// populated from that event) or NONE (all source fields left at their
// zero values). A nonzero RootEventID alongside a zero SourceEventID is
// not a valid state — there is no partial "looks sourced" metadata. The
// existing synchronous lifecycle stamp callsites (huddle join/leave/
// conclude, arrival) are stamp-before-emit, so in PR 3a they produce
// fully-zero, "not event-sourced" warrants; they are retrofitted with
// real lineage in PR 3 (see the PR 3 design note).

// WarrantSourceKey identifies the (warrant kind, discriminator) pair a
// warrant came from. It is the single dedup key shared by all three of
// tryStampWarrant's dedup paths — open-cycle, in-flight, and recently-
// consumed. A single source can produce different kinds for the same
// actor, so Kind is part of the key.
//
// Discriminator comes from the Reason itself via Reason.DedupDiscriminator()
// — for event-sourced reasons it's the source event's ID (SpeechID /
// PaidID / AttemptID / QuoteID, all 1:1 with their source event); for
// aggregate-keyed reasons it's the aggregate's ID (LedgerID for
// PayOfferWarrantReason), which survives LoadWorld so restart re-stamp
// dedupes against the normal-flow stamp.
//
// Dedup applies ONLY when Discriminator != 0. A zero Discriminator is the
// "not event-sourced" sentinel; (Kind, 0) as a key would collapse
// unrelated non-event-sourced warrants, so they bypass dedup. As a
// consequence, a zero-Discriminator key is NEVER stored in the in-flight
// or recently-consumed sets either — sourceKeySet filters non-event-
// sourced warrants out at consume time, so the sets only ever hold real
// keys.
type WarrantSourceKey struct {
	Kind          WarrantKind
	Discriminator uint64
}

// sourceKey returns the WarrantSourceKey for this meta. The key is only
// meaningful for dedup when the Reason's discriminator is non-zero —
// callers check that via eventSourced before using it.
func (m WarrantMeta) sourceKey() WarrantSourceKey {
	return WarrantSourceKey{Kind: m.Kind(), Discriminator: m.dedupDiscriminator()}
}

// eventSourced reports whether this meta's Reason carries a non-zero
// dedup discriminator and therefore participates in tryStampWarrant's
// dedup paths. A nil Reason or a Reason whose DedupDiscriminator returns
// 0 bypasses dedup.
func (m WarrantMeta) eventSourced() bool {
	return m.dedupDiscriminator() != 0
}

// dedupDiscriminator returns the Reason's dedup discriminator, or 0 when
// Reason is nil. Nil-Reason warrants are rejected at the tryStampWarrant
// entry guard anyway, but defensive iteration through warrant slices
// (sourceKeySet, the open-cycle dedup scan) reaches this helper too.
func (m WarrantMeta) dedupDiscriminator() uint64 {
	if m.Reason == nil {
		return 0
	}
	return m.Reason.DedupDiscriminator()
}

// Kind returns the WarrantKind of the meta's reason, or WarrantKindUnknown
// if Reason is nil. Convenience for filtering and metrics.
func (m WarrantMeta) Kind() WarrantKind {
	if m.Reason == nil {
		return WarrantKindUnknown
	}
	return m.Reason.Kind()
}

// tryStampWarrant is the single funnel for stamping a warrant on an actor.
// All callsites that observe an event the actor should think about route
// through here.
//
//   - Already-warranted: appends meta to Warrants (capped at
//     Settings.MaxWarrantsPerActor; oldest dropped). Preserves earliest
//     WarrantedSince and WarrantDueAt — merge by accumulation, not
//     replacement.
//   - Not warranted: stamps WarrantedSince=now, picks a jitter from
//     Settings.ReactorJitterMin..Max, stamps WarrantDueAt=now+jitter,
//     initializes Warrants with [meta].
//
// Source-aware dedup (PR 3a, refined PR S4): an event-sourced warrant
// (Reason.DedupDiscriminator() != 0) is dropped if its WarrantSourceKey
// is already (1) pending in the open warrant cycle, (2) consumed into the
// in-flight tick attempt, or (3) in the recently-consumed set within
// recentlyConsumedTTL. Together these coalesce near-simultaneous multi-
// path triggers and suppress a delayed duplicate of a stimulus a
// completed tick already addressed. Warrants whose Reason returns
// discriminator 0 ("not event-sourced", e.g. BasicWarrantReason from
// lifecycle stamps) bypass dedup — (Kind, 0) would collapse unrelated
// warrants.
//
// Tick-in-flight does NOT block stamping a NEW source — fresh signals must
// accumulate so they're available for the NEXT tick. The TickInFlight gate
// only prevents the evaluator from re-emitting the same actor while their
// LLM call is pending; the in-flight DEDUP path above suppresses only an
// exact-same-source duplicate, never a distinct development.
//
// Unexported by design — warrant stamping is the privilege of mutation
// commands inside Command.Fn. External callers reach it through Commands.
//
// Returns true when the warrant was recorded (a fresh cycle opened, or the
// meta appended to an open cycle), false when the funnel declined it (nil
// args, an agent-less actor kind, or a source-dedup hit). Most callers ignore
// the result — they stamp and move on. The red-need backstop (ZBBS-HOME-363)
// consults it because it advances real per-actor backoff pacing on a stamp,
// and must not pace an actor for a deliberation that the funnel never
// produced.
func tryStampWarrant(w *World, actor *Actor, meta WarrantMeta, now time.Time) bool {
	if actor == nil || meta.Reason == nil {
		return false
	}

	// Only agent-backed NPC kinds are ever warranted: a warrant exists to
	// drive an LLM reactor tick, and PCs / decoratives have no agent to
	// drive (ZBBS-HOME-428). This invariant used to live in each producer's
	// scope check by convention, and the huddle join/leave producers missed
	// it — a PC swept into a huddle got a HuddleJoined / HuddlePeerJoined
	// warrant, the reactor ticked the agent-less human, the tick died before
	// render (failed_before_render / malformed), and the before-render
	// carry-forward re-opened the same warrant in a permanent retry loop
	// (the 2026-06-10 play session's "52 malformed" telemetry). Gating at
	// the single stamping funnel closes every producer path at once;
	// warrants are wiped on load (resetReactorStateOnLoad), so a stale PC
	// cycle can't survive a boot either.
	if actor.Kind != KindNPCStateful && actor.Kind != KindNPCShared {
		return false
	}

	// Source-aware dedup. Only event-sourced warrants participate; reads
	// from nil maps are safe (zero value, ok=false), so no nil-guards.
	if meta.eventSourced() {
		key := meta.sourceKey()
		// 1. Open-cycle: same source already pending this cycle.
		for _, pending := range actor.Warrants {
			if pending.eventSourced() && pending.sourceKey() == key {
				return false
			}
		}
		// 2. In-flight: same source consumed into the attempt mid-LLM-call.
		if _, ok := actor.inFlightSourceKeys[key]; ok {
			return false
		}
		// 3. Recently-consumed: a completed attempt addressed this exact
		//    source within the TTL window. Expired entries are ignored
		//    here and swept on the next insert (rememberConsumedSourceKey).
		if ts, ok := actor.recentlyConsumedSourceKeys[key]; ok &&
			now.Sub(ts) < recentlyConsumedTTL {
			return false
		}
	}

	if actor.WarrantedSince != nil {
		actor.Warrants = appendCappedWarrant(actor.Warrants, meta, w.Settings.MaxWarrantsPerActor)
		return true
	}
	t := now
	actor.WarrantedSince = &t
	due := now.Add(pickWarrantJitter(w.Settings, now))
	actor.WarrantDueAt = &due
	actor.Warrants = []WarrantMeta{meta}
	return true
}

// pickWarrantJitter returns a duration in [ReactorJitterMin,
// ReactorJitterMax). Falls back to a small safe default if settings
// haven't been loaded yet (e.g. tests that don't seed the environment).
func pickWarrantJitter(s WorldSettings, _ time.Time) time.Duration {
	min := s.ReactorJitterMin
	max := s.ReactorJitterMax
	if min <= 0 {
		min = defaultReactorJitterMin
	}
	if max <= 0 {
		max = defaultReactorJitterMax
	}
	if max <= min {
		return min
	}
	span := int64(max - min)
	return min + time.Duration(mathrand.Int64N(span))
}

// appendCappedWarrant appends meta to the slice. If len(list) >= cap (cap
// > 0), drops the oldest entry — the freshest signals are the ones most
// likely to be relevant. cap <= 0 means uncapped.
func appendCappedWarrant(list []WarrantMeta, meta WarrantMeta, cap int) []WarrantMeta {
	list = append(list, meta)
	if cap > 0 && len(list) > cap {
		drop := len(list) - cap
		list = append([]WarrantMeta(nil), list[drop:]...)
	}
	return list
}

// clearWarrant resets the warrant state on the actor. Called by the
// evaluator at emit time and by LoadWorld during restart.
func clearWarrant(a *Actor) {
	a.WarrantedSince = nil
	a.WarrantDueAt = nil
	a.Warrants = nil
}

// warrantCycleStale reports whether the actor's open warrant cycle began
// longer ago than MaxWarrantAge. The evaluator uses it to expire the queue
// of a shelved actor (asleep, or on break with no interrupting warrant) that
// is therefore never consuming its warrants — without this a gated actor
// banks signals up to MaxWarrantsPerActor and wakes to a stale transcript
// instead of current state (ZBBS-WORK-361).
//
// Cycle-level (keyed on the always-set WarrantedSince) rather than
// per-warrant: an awake actor consumes its whole cycle within seconds of
// WarrantDueAt, so it never accumulates a mixed-age pile — the shelved actor
// is the only real staleness site, and there every warrant in the cycle is
// equally unaddressable. WarrantedSince is reliable for every warrant kind,
// whereas the per-warrant WarrantMeta.OccurredAt is zero for the lifecycle
// (huddle-churn) stamps that are the bulk of the noise.
//
// Falls back to defaultMaxWarrantAge when the setting is unset / non-positive,
// matching the parse-time posture in repo/pg/environment.go.
func warrantCycleStale(a *Actor, now time.Time, s WorldSettings) bool {
	if a == nil || a.WarrantedSince == nil {
		return false
	}
	maxAge := s.MaxWarrantAge
	if maxAge <= 0 {
		maxAge = defaultMaxWarrantAge
	}
	return now.Sub(*a.WarrantedSince) > maxAge
}

// retainForcedWarrants prunes a stale warrant cycle down to only its Force
// warrants (operator nudges, which must survive shelving) and re-anchors the
// cycle clock to now, so the kept warrants get a fresh MaxWarrantAge window
// instead of being re-pruned on the next scan. The caller has already
// established the cycle is stale; this is the path taken when it ALSO holds at
// least one Force warrant. Keeping the whole cycle just because one warrant is
// forced would re-protect exactly the stale pile TTL eviction exists to drop
// (ZBBS-WORK-361 code_review), so the non-Force warrants are dropped here while
// the operator signal is preserved. Defensive: if no Force warrant actually
// survives the filter, the cycle is cleared (matches the no-force path).
func retainForcedWarrants(a *Actor, now time.Time, s WorldSettings) {
	kept := make([]WarrantMeta, 0, len(a.Warrants))
	for _, wm := range a.Warrants {
		if wm.Force {
			kept = append(kept, wm)
		}
	}
	if len(kept) == 0 {
		clearWarrant(a)
		return
	}
	a.Warrants = kept
	t := now
	a.WarrantedSince = &t
	due := now.Add(pickWarrantJitter(s, now))
	a.WarrantDueAt = &due
}

// resetReactorStateOnLoad wipes ephemeral reactor state on LoadWorld so a
// checkpoint with TickInFlight=true doesn't wedge the actor after restart
// and stale rate-gate history doesn't delay fresh post-restart warrants.
// Warrants are also cleared — interface-typed payloads aren't designed to
// survive serialization, and post-restart cascade origins re-engage actors
// via fresh events anyway.
//
// RecentReactorTicks stays nil after the reset — lastReactorTickAt
// reports ok=false for fresh-loaded actors, which is what the
// MinReactorTickGap pacing floor and per-minute rate gate both expect
// (a fresh actor has no recent-tick history; both gates correctly
// no-op). The cold-start anchor for the idle-backstop sweep lives on
// World.LoadedAt instead, so only that consumer sees the "world woke
// up" timestamp; lastReactorTickAt's semantics ("most recent reactor
// tick — newest entry of RecentReactorTicks") stay pure.
func resetReactorStateOnLoad(a *Actor) {
	clearWarrant(a)
	a.TickInFlight = false
	a.TickAttemptID = ""
	a.RecentReactorTicks = nil
	a.inFlightSourceKeys = nil
	a.recentlyConsumedSourceKeys = nil
	a.awaitingReplyFrom = nil // ZBBS-WORK-370 turn-state — ephemeral
	// Red-need backstop pacing (ZBBS-HOME-363) — ephemeral, like the
	// rate-gate history above. A fresh-loaded actor starts un-paced so a
	// red need re-engages promptly after restart rather than inheriting a
	// stale backoff timer.
	clearRedNeedBackstop(a)
}

// actorReactorDue is the cheap pre-check the evaluator runs against every
// actor on each scan. Returns true when:
//
//   - the actor has a warrant (both WarrantedSince and WarrantDueAt non-nil),
//   - now is at or past WarrantDueAt,
//   - the actor is not already mid-tick (TickInFlight false).
//
// Requires BOTH WarrantedSince and WarrantDueAt — the evaluator
// dereferences both at emit time, so the precheck defends the invariant.
// An inconsistent state with one set and the other nil is treated as
// not-due (caller can clear and re-stamp via tryStampWarrant).
//
// Per-minute rate gating is applied separately (see checkRateGate) so a
// rate-capped actor can be delayed by pushing WarrantDueAt rather than
// silently skipped each scan.
//
// Unexported by design — eligibility primitives are part of the reactor
// boundary, not a public API.
func actorReactorDue(a *Actor, now time.Time) bool {
	if a == nil || a.WarrantedSince == nil || a.WarrantDueAt == nil {
		return false
	}
	if a.TickInFlight {
		return false
	}
	return !now.Before(*a.WarrantDueAt)
}

// actorCanReactNow is the context-aware eligibility check the reactor
// evaluator consults BEFORE consuming warrants. Filters out states
// where firing an LLM tick is wasted cost — sleeping/resting actors,
// concluded huddles, keepers who just engine-spoke. Replaces v1's
// scattered "skip if NPC asleep" checks at individual subscriber callsites
// with one chokepoint that applies to all warrant kinds.
//
// What's checked today:
//   - Nil-actor guard (caller already has the pointer; this is defensive).
//   - Concluded-huddle stale: if CurrentHuddleID points at a huddle that
//     has been concluded, the warrant's conversational context no longer
//     exists. Return stale=true; caller clears the warrant.
//   - Sleeping (StateSleeping enum OR SleepingUntil timestamp): return
//     eligible=false, stale=false — sleep is sacrosanct and is never
//     interrupted (v1 decision). The warrant stays OPEN; the evaluator backs
//     off and resumes it on the next scan after wake.
//   - Resting / on-break (StateResting enum OR BreakUntil timestamp): also
//     eligible=false BY DEFAULT, same back-off posture — EXCEPT a red-tier
//     need warrant (ZBBS-HOME-329 #3) or an operator nudge — admin force-tick
//     or directive impulse (#4) — makes the break interruptible (eligible=true,
//     matched by KIND, not the broad Force flag). The interrupting
//     tick's emit path (EvaluateReactors) calls endBreak so the actor actually
//     leaves rest. Without the exception an actor that beds down while already
//     starving never wakes to eat, and the operator /nudge couldn't rescue it.
//     The ZBBS-HOME-284 sleep lifecycle drives both enum and timestamp; the
//     enum can lag an auto-bedded NPC, so both are checked.
//   - Businessowner engine-speech suppression: if the actor engine-spoke
//     a hospitality line within businessownerEngineSpeechSuppressionTTL
//     (5s), their LLM tick on the same triggering event is skipped so
//     the model doesn't follow up with a redundant "welcome friend" of
//     its own. Returns (false, false); the warrant stays OPEN and the
//     evaluator backs off, then picks the warrant up once the suppression
//     window expires.
//
// Note on StateResting: per actor.go's State enum comment, Resting is
// the take_break / dwell-credit-accumulating posture (in-bed/recovering),
// NOT "sitting in tavern, can respond." It is gated like Sleeping for the
// same reason — the actor has withdrawn from the active surface — but
// UNLIKE sleep, a break yields to a pressing need or an operator nudge
// (ZBBS-HOME-329 #3/#4): a vendor should not starve on a coffee break.
//
// What's NOT checked here (deferred):
//   - Off-stage / deceased actors (subsystems haven't ported).
//   - Noop-skip — "actor has nothing to act on" gating belongs in
//     tick-handler preflight where full perception is available; applies
//     across warrant kinds but needs the perception build to make the
//     call.
//
// Returns (eligible, stale). When stale=true, caller clears the warrant
// (it was for a context that no longer exists). When eligible=false but
// stale=false, caller backs off (temporarily unavailable; warrant stays).
func actorCanReactNow(w *World, a *Actor, now time.Time) (eligible bool, stale bool) {
	if a == nil {
		return false, true
	}
	if a.CurrentHuddleID != "" {
		if h, ok := w.Huddles[a.CurrentHuddleID]; ok && h.ConcludedAt != nil {
			return false, true
		}
	}
	// now is the evaluator's scan time, threaded in (not a fresh wall clock) so
	// the break/sleep decision agrees with the rest of the EvaluateReactors pass
	// and with sim-time / delayed-command callers (code_review, ZBBS-HOME-329).
	// Sleep is sacrosanct — never interrupt a sleeper, via either the enum or
	// the authoritative SleepingUntil timestamp (v1 decision, reaffirmed by Jeff
	// 2026-05-29). The sleep/break lifecycle drives these timestamps directly;
	// the State enum is a soft-set companion (executeNPCSleep sets StateSleeping)
	// that can lag, so gate on both.
	if a.State == StateSleeping {
		return false, false
	}
	if a.SleepingUntil != nil && a.SleepingUntil.After(now) {
		return false, false
	}
	// A scheduled break (StateResting / BreakUntil) shelves the tick too — EXCEPT
	// a red-tier need warrant (ZBBS-HOME-329 #3), an operator nudge — a bare
	// admin force-tick or a directive impulse (#4) — or a PC speaking into this
	// actor's huddle (ZBBS-HOME-377): any of these cuts the break short. The PC
	// case warrants every recipient of the player's utterance, not just a parsed
	// vocative addressee — narrowing to the named person is the deferred fix B.
	// It is the conversational counterpart of the need case — a player addressing
	// the room an NPC is in outranks that NPC's nap, so the keeper a customer is
	// talking to actually answers instead of resting through the conversation. Without
	// this, an actor that beds down for a break while already hungry/thirsty (or
	// while a player is trying to talk to it) sits locked in rest: the reactor
	// defers the warrant for the whole break and nothing wakes it, and even an
	// operator /nudge couldn't rescue it (this gate runs before the pacing
	// Force-bypass). The interrupting tick's emit path (EvaluateReactors) calls
	// endBreak so the actor actually leaves rest. We match specific warrant KINDS
	// (need / operator-nudge / PC-speech) rather than the broad Force flag so a
	// future non-operator forced warrant can't silently gain the power to wake
	// resters — and so NPC-to-NPC speech (NPCSpeechWarrantReason) stays gated,
	// keeping village chatter from yanking a rester out. Sleep (above) yields to
	// none of these — only a break is interruptible.
	onBreak := a.State == StateResting || (a.BreakUntil != nil && a.BreakUntil.After(now))
	if onBreak && !hasNeedWarrant(a.Warrants) && !hasOperatorNudgeWarrant(a.Warrants) && !hasPCSpeechWarrant(a.Warrants) {
		return false, false
	}
	if businessownerEngineSpeechRecent(w, a.ID, now) {
		return false, false
	}
	return true, false
}

// TickAdmissionController decides whether the reactor evaluator may admit
// a tick right now — i.e. whether there is downstream capacity to actually
// run it. The evaluator consults CanAdmit BEFORE consuming an actor's
// warrants (Option A — admit before consume), so a "no" leaves the
// warrants open and nothing is lost.
//
// The substrate owns this interface; the default is alwaysAdmit, so the
// evaluator runs standalone in substrate tests with no handler wired. PR
// 3's worker pool implements it (CanAdmit reports len(jobChan) <
// cap(jobChan)) and MUST return false once the pool is stopping/stopped,
// otherwise an admit-then-send-to-closed-channel race is possible during
// shutdown.
type TickAdmissionController interface {
	CanAdmit() bool
}

// alwaysAdmit is the default TickAdmissionController — it admits every
// tick. With no PR 3 worker pool wired, the evaluator behaves exactly as
// it did before admission control existed.
type alwaysAdmit struct{}

func (alwaysAdmit) CanAdmit() bool { return true }

// checkRateGate returns true when the actor is below the per-minute cap.
// The cap is a "gross gate" — settings-driven, no cost calculation. cap
// <= 0 disables the gate. RecentReactorTicks is the per-actor ring of
// recent tick timestamps; entries older than rateWindow don't count.
func checkRateGate(a *Actor, now time.Time, cap int, rateWindow time.Duration) bool {
	if cap <= 0 {
		return true
	}
	if a.RecentReactorTicks == nil {
		return true
	}
	cutoff := now.Add(-rateWindow)
	count := 0
	for _, t := range a.RecentReactorTicks.Snapshot() {
		if t.After(cutoff) {
			count++
		}
	}
	return count < cap
}

// lastReactorTickAt returns the timestamp of the actor's most recent
// reactor-tick emission — the newest entry of RecentReactorTicks. ok is
// false when the actor has never ticked (nil/empty ring); the
// MinReactorTickGap floor does not apply to a first tick.
func lastReactorTickAt(a *Actor) (time.Time, bool) {
	if a.RecentReactorTicks == nil || a.RecentReactorTicks.Len() == 0 {
		return time.Time{}, false
	}
	snap := a.RecentReactorTicks.Snapshot()
	return snap[len(snap)-1], true
}

// recordReactorTick appends now to the actor's RecentReactorTicks ring,
// allocating the buffer lazily. Capacity is sized to comfortably exceed
// the per-minute cap so the rate-gate's window-count stays exact.
//
// Resize semantics: if cap is raised at runtime above the existing ring's
// capacity, the ring is rebuilt at the larger size with existing entries
// preserved in order. Without this, a ring allocated under a low cap
// couldn't enforce a later-raised cap (the new threshold could never be
// reached because the ring drops old ticks before count reaches cap).
func recordReactorTick(a *Actor, now time.Time, cap int) {
	capacity := cap * 2
	if capacity < defaultRecentReactorTicksCap {
		capacity = defaultRecentReactorTicksCap
	}
	if a.RecentReactorTicks == nil {
		a.RecentReactorTicks = NewRingBuffer[time.Time](capacity)
	} else if a.RecentReactorTicks.Cap() < capacity {
		old := a.RecentReactorTicks.Snapshot()
		rb := NewRingBuffer[time.Time](capacity)
		for _, t := range old {
			rb.Push(t)
		}
		a.RecentReactorTicks = rb
	}
	a.RecentReactorTicks.Push(now)
}

// TickAttemptID is the generation identifier for a reactor tick attempt.
// It disambiguates stale completions: CompleteReactorTick is honored only
// when its AttemptID matches the actor's current TickAttemptID, so a late-
// returning timed-out attempt cannot clear a newer attempt's in-flight
// flag. Minted by newTickAttemptID; ephemeral — wiped on LoadWorld with
// the rest of the reactor state.
type TickAttemptID string

// newTickAttemptID mints an opaque generation identifier for a reactor
// tick attempt. Used to disambiguate stale completions: a completion
// command is only honored when its AttemptID matches the actor's current
// TickAttemptID. Implementation is random-hex (same idiom as huddle/scene
// IDs) — sortability isn't required since the comparison is exact.
func newTickAttemptID() TickAttemptID {
	return TickAttemptID("tk-" + randomHex(12))
}

// Defaults applied when WorldSettings hasn't been initialized (e.g. test
// worlds that bypass repo loading and don't seed an Environment). Real
// production settings come from WorldSettings; these exist so the reactor
// is functional in test scaffolds without forcing every test to seed
// settings.
const (
	defaultReactorJitterMin        = 1 * time.Second
	defaultReactorJitterMax        = 4 * time.Second
	defaultReactorEvaluatorCadence = 250 * time.Millisecond
	defaultMaxWarrantAge           = 90 * time.Second
	defaultMaxWarrantsPerActor     = 16
	defaultRateWindow              = time.Minute
	defaultRecentReactorTicksCap   = 32

	// defaultMinReactorTickGap is the per-actor minimum wall-clock gap
	// between reactor ticks when WorldSettings.MinReactorTickGap is unset.
	// A pacing floor independent of the optional per-minute rate cap.
	defaultMinReactorTickGap = 5 * time.Second

	// defaultAdmissionBackoff is how far the evaluator pushes an actor's
	// WarrantDueAt when tick admission control turns it away, when
	// WorldSettings.AdmissionBackoff is unset. ≈ the evaluator cadence, so
	// a deferred warrant is re-examined on roughly the next scan.
	defaultAdmissionBackoff = 250 * time.Millisecond

	// defaultIdleBackstopThreshold is the wall-clock duration an actor
	// must go without a reactor tick before the idle-backstop sweep
	// stamps a WarrantKindIdleBackstop warrant, when
	// WorldSettings.IdleBackstopThreshold is unset. 30 min — engine-
	// injected liveness for actors no other warrant has engaged.
	//
	// The companion sweep-cadence default lives in the cascade package
	// (engine/sim/cascade/idle_backstop.go) since cascade owns the
	// goroutine driver; sim only knows the per-actor criterion.
	defaultIdleBackstopThreshold = 30 * time.Minute

	// Red-need backstop defaults (ZBBS-HOME-363). Base is the first/floor
	// re-warrant gap for a red-need idle actor; the per-actor backoff
	// doubles it each no-progress sweep, capped at max. Max == the idle-
	// backstop default so a permanently-stuck actor's steady-state
	// re-warrant cost never exceeds the idle-backstop rate. The companion
	// sweep-cadence default lives in cascade/red_need_backstop.go.
	defaultRedNeedBackstopBaseDelay = 90 * time.Second
	defaultRedNeedBackstopMaxDelay  = 30 * time.Minute

	// recentlyConsumedTTL / recentlyConsumedCap bound the per-actor
	// recently-consumed source-key set — tryStampWarrant's third dedup
	// path. A consumed key suppresses a delayed duplicate of the same
	// source event for up to the TTL; the cap is a hard ceiling with
	// expired-first-then-oldest eviction (see rememberConsumedSourceKey).
	recentlyConsumedTTL = 5 * time.Minute
	recentlyConsumedCap = 256
)

// sourceKeySet collects the WarrantSourceKeys of the event-sourced
// warrants in list into a set. Returns nil when none are event-sourced;
// a nil in-flight set is the valid "no source keys consumed" state.
// Called at ReactorTickDue emit to record what the attempt consumed.
func sourceKeySet(list []WarrantMeta) map[WarrantSourceKey]struct{} {
	var set map[WarrantSourceKey]struct{}
	for _, m := range list {
		if !m.eventSourced() {
			continue
		}
		if set == nil {
			set = make(map[WarrantSourceKey]struct{})
		}
		set[m.sourceKey()] = struct{}{}
	}
	return set
}

// rememberConsumedSourceKey records key in the actor's recently-consumed
// set with insertion time now, allocating the map lazily. When the set is
// already at recentlyConsumedCap it first sweeps entries older than
// recentlyConsumedTTL, then — if still at cap — evicts the single oldest
// entry by insertion time, before inserting. Called by CompleteReactorTick
// when a terminal status marks a source key as addressed.
func rememberConsumedSourceKey(a *Actor, key WarrantSourceKey, now time.Time) {
	if a.recentlyConsumedSourceKeys == nil {
		a.recentlyConsumedSourceKeys = make(map[WarrantSourceKey]time.Time)
	}
	m := a.recentlyConsumedSourceKeys
	if len(m) >= recentlyConsumedCap {
		cutoff := now.Add(-recentlyConsumedTTL)
		for k, ts := range m {
			if ts.Before(cutoff) {
				delete(m, k)
			}
		}
		for len(m) >= recentlyConsumedCap {
			var oldestKey WarrantSourceKey
			var oldestTS time.Time
			first := true
			for k, ts := range m {
				if first || ts.Before(oldestTS) {
					oldestKey, oldestTS, first = k, ts, false
				}
			}
			delete(m, oldestKey)
		}
	}
	m[key] = now
}
