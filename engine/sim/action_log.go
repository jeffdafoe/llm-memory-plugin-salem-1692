package sim

import "time"

// action_log.go — engine-internal append-only audit trail. World-scoped
// in-memory slice of recent agent + engine-source actions, consumed by
// the atmosphere refresh cascade (group-by-actor-by-action since last
// fire) and per-actor narrative consolidation (own + peer rows within
// a recent window). Mirrors v1's agent_action_log pg table at a
// v2-scale-appropriate shape: in-memory only, capped retention,
// happy-path-only.
//
// Storage shape: flat []ActionLogEntry on World. No secondary indices
// — consumers walk the slice with a time-cutoff filter, which at
// Hannah scale (<10 NPCs, low TPS, 48h retention) is microseconds. If
// atmosphere or C2 reads ever measure meaningfully, retrofit
// per-actor / per-huddle indices on the same slice.
//
// Result field deliberately absent — failed/rejected actions don't
// land in the log. v1 logged failures for admin investigation reads
// (which happen against the durable pg projection at cutover, not
// against this in-memory cache). Every entry here is OK by
// construction. Deliberation outcomes (declined / countered pay) are
// their own ActionType values when those handlers port.
//
// No Source field — v2's magistrate role is gone; agent-vs-player-vs-
// engine inferable from ActorID kind via World.Actors lookup.
//
// No SpeakerName field — derive from
// Snapshot.Actors[ActorID].DisplayName at render time. v1
// denormalized to avoid a SQL JOIN; v2's snapshot reader has the data
// in hand.
//
// Durability: in-memory only at MVP. The ActionLogSink interface in
// repo.go stays a noop (mem.noopActionLog) — a future cutover will
// wire a pg projection if external admin reads need historical rows.
// Restart-loss is acceptable: atmosphere's last-fire stamp resets on
// restart and consolidation re-snapshots from current state.

// ActionType is the typed enum for entries appended to the action log.
// Open string set — new values land as commit-bearing handlers port.
// Matches the InteractionKind / WarrantKind posture (typed string,
// not free TEXT like v1's column).
type ActionType string

const (
	// ActionTypeSpoke — committed speak tool call. ActorID is the
	// speaker; Text is the full utterance (rune-bounded to
	// MaxSpokenActionLogTextLen, not the tighter MaxActionLogTextLen the
	// other types share, so the PC talk-panel backload renders the whole
	// line); HuddleID is the speaker's huddle at emit time.
	ActionTypeSpoke ActionType = "spoke"

	// ActionTypePaid — committed pay tool call. ActorID is the
	// buyer; Text is the ForText (may be empty); HuddleID is the
	// buyer's huddle at append time (the same-huddle gate guarantees
	// the seller shares it).
	ActionTypePaid ActionType = "paid"

	// ActionTypeConsumed — committed consume tool call. ActorID is
	// the actor that ate; Text is the item kind (with qty prefix
	// when qty > 1); HuddleID is the actor's huddle if any.
	ActionTypeConsumed ActionType = "consumed"

	// ActionTypeDelivered — committed deliver_order tool call.
	// ActorID is the seller (the deliver action is theirs); Text is
	// the item kind (with qty prefix when qty > 1); HuddleID is the
	// seller's huddle at append time.
	ActionTypeDelivered ActionType = "delivered"

	// ActionTypeWalked — arrival at a movement destination. ActorID is
	// the mover; Text is the DESTINATION's DisplayName — the structure or
	// village object the mover walked TO (names a visited shop even when the
	// actor stopped at a loiter slot outside it, and an ObjectVisit well/
	// tree/pile). Empty only for a bare outdoor Position arrival with no
	// nameable place. HuddleID is empty (arrival precedes any encounter-
	// cascade huddle join that may follow).
	ActionTypeWalked ActionType = "walked"

	// ActionTypeDeparted — the inverse of ActionTypeWalked: the mover crossed
	// OUT of a structure footprint mid-walk. ActorID is the mover; Text is the
	// LEFT structure's DisplayName; HuddleID is empty (a departure leaves any
	// huddle behind). Emitted by the locomotion exit seam BEFORE the inside-flip
	// (via ActorLeftStructure) so the central scope stamp lands on the structure
	// being left and a co-present PC's talk-panel backload shows the exit. Renders
	// "<name> leaves the <place>." (httpapi.renderActionLogEntry).
	ActionTypeDeparted ActionType = "departed"

	// ActionTypeTookBreak — committed take_break tool call
	// (ZBBS-HOME-284 #4). ActorID is the actor that stepped away; Text
	// is the model-supplied reason; HuddleID is the actor's huddle at
	// append time (usually empty — a break closes the post).
	ActionTypeTookBreak ActionType = "took_break"

	// ActionTypeStayedOpen — committed stay_open tool call (ZBBS-WORK-387).
	// ActorID is the keeper that committed to staying open late; Text is the
	// model-supplied reason; HuddleID is the keeper's huddle at append time.
	ActionTypeStayedOpen ActionType = "stayed_open"

	// ActionTypeSummoned — a summon messenger delivered a summons to the
	// target (ZBBS-HOME-311). ActorID is the TARGET (the summons is the
	// event that happened to them, not an action they took); Text is the
	// engine-authored delivery line; HuddleID is the target's huddle at
	// delivery time. Engine-sourced, not a tool call — the messenger is a
	// non-VA NPC.
	ActionTypeSummoned ActionType = "summoned"

	// ActionTypeLabored — a worker's accepted solicit_work commitment (LLM-26)
	// completed and the reward transferred from the employer to the worker
	// (labor_settle.go settle-at-completion). ActorID is the WORKER — the labor
	// is theirs and worker-side is the salient economic beat (the broke NPC
	// earning); CounterpartyName is the employer who paid; Amount is the reward
	// coins; HuddleID is the offer's captured huddle (the same-huddle peer
	// filter reaches the employer). Engine-sourced — written by the completion
	// sweep, not a tool call. Only the Completed terminal logs a row;
	// declined/expired/failed move no coins and log nothing, mirroring the
	// accepted-only pay row (LLM-162).
	ActionTypeLabored ActionType = "labored"

	// ActionTypeSolicitedWork — a worker's committed solicit_work that minted a
	// live pending LaborOffer (LLM-213). ActorID is the WORKER (the offer is
	// theirs); CounterpartyName is the employer solicited; Amount is the reward
	// asked; HuddleID is the offer's huddle. Event-sourced off LaborOfferReceived,
	// which SolicitWork emits ONLY on the live-pending path — so the LLM-193
	// affordability auto-decline (which mints then finalizes Declined without
	// emitting the event) logs nothing, mirroring ActionTypeLabored logging only
	// the Completed terminal. No coins move at solicit.
	ActionTypeSolicitedWork ActionType = "solicited_work"

	// ActionTypeHired — an employer's committed accept_work (LLM-213). ActorID is
	// the EMPLOYER (the hire is theirs); CounterpartyName is the worker taken on;
	// Amount is the agreed reward; HuddleID is the offer's huddle. Event-sourced
	// off LaborOfferAccepted, which AcceptWork emits only when every accept gate
	// passes. No coins move here — the reward settles at completion
	// (ActionTypeLabored).
	ActionTypeHired ActionType = "hired"

	// ActionTypeGathered — a committed gather (NPC `gather` tool or PC
	// POST /api/village/pc/gather). ActorID is the gatherer; Text is the
	// harvested item kind (with qty prefix when qty > 1, the same
	// formatItemQty shape as consumed/delivered); CounterpartyName is the
	// source object's display name (Well / berry bush / firewood pile), so
	// the talk-panel line can name where it came from; HuddleID is the
	// gatherer's huddle at append time. Event-sourced off ItemGathered,
	// which both actor kinds emit post-validation (LLM-273).
	ActionTypeGathered ActionType = "gathered"

	// ActionTypeRepairing — a committed `repair` tool call opened the mending
	// window on a worn business (LLM-354). ActorID is the mender: the owner, or
	// the hired hand mending the employer's business (LLM-271). Text is the
	// business's display name — named, not possessive, so the rendered line stays
	// right for a hired hand; HuddleID is the mender's huddle at append time.
	//
	// Event-sourced off SourceActivityStarted with Kind==SourceActivityRepair —
	// the START of the window, not its completion. The start is where StartRepair
	// has already validated responsibility, co-location, wear, and nails, and
	// where the nails are consumed; the ninety-second window that follows is what
	// an onlooker actually sees. The harvest/refresh starts share the event and
	// are deliberately NOT logged — eating and foraging are the actor's own
	// business, and gather already logs its yield at the mint (ActionTypeGathered).
	//
	// NOT feed-only (contrast ActionTypeOffered): a shopkeeper busy with a hammer
	// is ordinary village awareness, so this reaches the atmosphere digest (via an
	// atmosphereDigestVerbs entry) and the mender's own narrative consolidation
	// like any other beat.
	ActionTypeRepairing ActionType = "repairing"

	// ActionTypeOffered — a buyer's slow-path pay_with_item minted (or renewed,
	// via in_response_to) a PENDING pay-ledger offer (LLM-283, event-sourced off
	// PayOfferReceived). ActorID is the BUYER (the offer is theirs);
	// CounterpartyName is the seller; Amount is the coin offer (0 for a
	// goods-only barter offer); Text is the item summary ("3x milk"); HuddleID is
	// the offer's huddle. Fast-path / auto-match quote-takes never sit pending, so
	// they emit no PayOfferReceived and no offered row — this type marks exactly
	// the offers that wait on a seller decision (the dead-air the feed was
	// missing). Gift offers (give_goods, IsGift) are excluded by the subscriber —
	// they're a one-way flow, not a purchase haggle. FEED-ONLY: the live per-tick
	// NPC consumers (atmosphere digest + narrative consolidation) drop it via
	// isNegotiationActionType, and the durable mirror — written for barter tracing
	// — is dropped from dream narration by the distiller (memory-api), so no
	// NPC-facing path (live or dream) sees it; only the Village debugging window.
	ActionTypeOffered ActionType = "offered"

	// ActionTypeDeclined — a seller's decline_pay flipped a pending purchase offer
	// to the Declined terminal (LLM-283, event-sourced off PayWithItemResolved
	// with TerminalState=Declined). ActorID is the SELLER (the decline is theirs);
	// CounterpartyName is the buyer whose offer was declined; HuddleID is the
	// offer's huddle. No coins move. Gift declines (decline_gift, IsGift) are
	// excluded by the subscriber. FEED-ONLY (see ActionTypeOffered).
	ActionTypeDeclined ActionType = "declined"

	// ActionTypeCountered — a seller's counter_pay flipped a pending offer to the
	// Countered terminal (LLM-283, event-sourced off PayCountered). ActorID is the
	// SELLER (the counter is theirs); CounterpartyName is the buyer; Amount is the
	// seller's counter price (CounterAmount, always above the buyer's offer — a
	// non-increasing counter is coerced to an accept and emits no PayCountered);
	// HuddleID is the offer's huddle. FEED-ONLY (see ActionTypeOffered).
	ActionTypeCountered ActionType = "countered"
)

// isNegotiationActionType reports whether t is one of the pay-ledger negotiation
// beats (offered / declined / countered, LLM-283). These are FEED-ONLY: they
// render in the Village debugging window (httpapi.renderActionLogEntry) but are
// filtered OUT of every LIVE, in-memory NPC-facing consumer of the ring — the
// atmosphere activity digest (buildVillageContextActivityDigest) and the
// per-actor narrative consolidation (snapshotEventsForActor / actorHasEventSince).
// The OTHER NPC-facing path — the durable agent_action_log rows that feed offline
// dream distillation — is gated separately, distiller-side in memory-api (the
// sim-conversation distiller drops unmapped kinds rather than narrating them),
// because this guard only sees the in-process ring. Together they keep a live
// haggle a debugging affordance rather than a change to NPC perception or memory;
// surfacing negotiation to co-present NPCs would be a separate, deliberate
// decision. Single source of truth so a fourth negotiation type can't be added to
// the vocabulary and silently leak into one of the live consumers.
func isNegotiationActionType(t ActionType) bool {
	switch t {
	case ActionTypeOffered, ActionTypeDeclined, ActionTypeCountered:
		return true
	default:
		return false
	}
}

// ActionLogEntry is one row in the in-memory action log. Carries the
// minimum the in-engine consumers (atmosphere digest + C2
// consolidation) need; see the package doc for what's dropped vs v1's
// pg schema and why.
//
// CounterpartyName + Amount are NOT consumed by the in-engine readers
// (atmosphere counts by ActionType; C2 reads only Text). They exist
// solely for the PC talk-panel renderer (ZBBS-WORK-377,
// httpapi.renderActionLogEntry), which narrates paid/delivered beats to
// the human player and needs the recipient + coin amount — neither of
// which is recoverable from the snapshot at render time (the snapshot
// holds the acting actor, not the counterparty, and the amount is a
// transaction fact that lives nowhere else). Both are scalar, so the
// snapshot clone (CloneActionLogEntry) stays a plain value copy and the
// ring stays cheap. Text is left structurally unchanged so the C2
// consolidation prompt the NPCs' memory is built from is unaffected.
type ActionLogEntry struct {
	// Seq is the append sequence — strictly increasing, world-scoped,
	// assigned by AppendActionLogEntry (ZBBS-WORK-399). It exists because
	// OccurredAt is only approximately monotonic and can collide within a
	// world-goroutine batch: cursor-style readers (the Village activity
	// feed) page by Seq, which is collision-free and total-ordered by
	// construction. Slice order == Seq order (tail-append only; compaction
	// preserves order). Resets each boot with the rest of the log.
	Seq        uint64
	ActorID    ActorID
	OccurredAt time.Time
	ActionType ActionType
	Text       string   // freeform, rune-bounded at write time
	HuddleID   HuddleID // "" for outdoor / pre-huddle / non-huddle actions

	// StructureID / RoomID are the actor's conversational scope AT ACTION
	// TIME, stamped centrally by AppendActionLogEntry (ZBBS-HOME-437):
	// structure = the inside-or-loiter-pin scope (conversationalScopeStructure),
	// room = the private/staff subspace or 0 for public (audienceRoomScope).
	// They exist so the talk-panel backload can show a huddle-less PC what was
	// recently said in the room it is standing in — the huddle key alone
	// cannot, because huddles conclude and their ids stop resolving. Zero
	// values for actors out of any scope (open ground).
	StructureID StructureID
	RoomID      RoomID

	// DestStructureID is the structure an ActionTypeWalked entry walked TO —
	// the destination the mover aimed at, not the scope it ended up standing in
	// (ArrivalDestinationStructure). Empty for every other ActionType and for an
	// arrival with no structure destination (an ObjectVisit at a well, a bare
	// outdoor Position).
	//
	// It exists because StructureID above CANNOT answer "which business did this
	// trip go to" for the case that matters most: conversationalScopeStructure
	// blanks the outdoor loiter scope of a SHUT structure (loiterScopeConversable,
	// LLM-359), so a walk to a keeperless shop stamps StructureID "" — exactly the
	// trip the shut-business trail needs to name. Keying off the destination also
	// makes the trail independent of loiter-pin geometry: LLM-463 broke when a
	// per-instance loiter_offset moved the Tavern's pin out of its footprint, so
	// arrivals stopped reading as "inside" and the scope stamp started coming back
	// empty, with no code change and no test to catch it.
	//
	// Deliberately NOT falling back to StructureID when this is empty: that field
	// means "what scope was the actor in", not "where was this trip going", and
	// consulting it is the bug. No rollout gap follows from that — the action log is
	// in-memory and rebuilt from scratch each boot (see Seq above), so no row
	// predating this field ever reaches a reader.
	DestStructureID StructureID

	// CounterpartyName is the other party in a two-sided action: the
	// seller for ActionTypePaid, the buyer for ActionTypeDelivered, the
	// employer for ActionTypeLabored. Empty when the counterparty has no
	// display name (the renderer falls back to a counterparty-less phrasing
	// rather than show a raw id). Unset for all other ActionTypes.
	CounterpartyName string
	// Amount is the coin total for ActionTypePaid and the reward earned for
	// ActionTypeLabored (whole coins, > 0). Zero means "no amount to show" —
	// the renderer omits it. Unset for all other ActionTypes.
	Amount int

	// PayItems are the barter goods the payer handed over ALONGSIDE Amount on
	// an ActionTypePaid settlement — the buyer's non-coin leg of a pay_with_item
	// deal (LLM-374). Empty for a pure-coin pay and for every non-Paid action.
	// The Paid renderers append it after the coin amount so a mixed coins+goods
	// payment narrates in full ("pays 4 coins and 3 cheese for …") instead of
	// silently reading as coins-only.
	PayItems []ItemKindQty
}

// MaxActionLogTextLen bounds the Text field at write time for every
// action type EXCEPT ActionTypeSpoke. Same value as MaxSalientFactTextLen
// — both feed the LLM and share a per-token-budget concern.
const MaxActionLogTextLen = MaxSalientFactTextLen

// MaxSpokenActionLogTextLen is the wider bound for ActionTypeSpoke rows.
// A spoken line is player-facing: the PC talk-panel backload renders it
// verbatim (httpapi.renderActionLogEntry), so clipping it to the 220-rune
// MaxActionLogTextLen budget cut long utterances off mid-word in the panel
// history (the live npc_spoke frame already carries the full text, so only
// the history backload showed the clip). Set to the utterance's own
// upstream validation bound, so the ring stores exactly what the speak
// handler accepted. The one LLM-facing reader of this text — the C2
// narrative/soul consolidation (cascade.buildSoulDaySnapshot) — is
// count-capped (NarrativeEventsLimit) and typical utterances fall well
// under 220, so the wider bound only lengthens genuinely long lines, which
// are the ones truncation was losing in both the panel and the NPC's own
// memory. Mirrors handlers.MaxSpeakTextChars — a separate sim-package
// constant to avoid a sim→handlers import cycle.
const MaxSpokenActionLogTextLen = 1000

// DefaultActionLogRetention is the fallback for
// WorldSettings.ActionLogRetention when unset. 48h covers atmosphere's
// 4h refresh interval with comfortable headroom and consolidation's
// expected 24h window cleanly. Tunable via settings for dev / staging
// to drop closer to the sweep cadence.
const DefaultActionLogRetention = 48 * time.Hour

// CloneActionLogEntry is a value-copy. The struct has no nested
// pointers or maps today, so a plain dereference suffices. Kept as a
// named helper so the republish path uses the same idiom as the other
// clone helpers — if a field grows that requires deep-copy (a slice
// or map payload), this is the single chokepoint to update.
func CloneActionLogEntry(e ActionLogEntry) ActionLogEntry {
	return e
}

// CloneActionLog returns a value-copy of the slice. Used by republish
// to produce Snapshot.ActionLog without exposing world-goroutine-owned
// storage to readers. Returns nil for an empty input so the snapshot's
// field semantics match an unset slice exactly.
func CloneActionLog(in []ActionLogEntry) []ActionLogEntry {
	if len(in) == 0 {
		return nil
	}
	out := make([]ActionLogEntry, len(in))
	copy(out, in)
	return out
}
