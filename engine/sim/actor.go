package sim

import (
	"strings"
	"time"
)

// ActorID identifies an actor uniquely within the world.
type ActorID string

// ActorKind discriminates the four populations: stateful NPCs (own VA with
// memory), shared-VA NPCs (salem-vendor / salem-visitor backed), human PCs,
// and decorative sprite-only actors that the engine moves but never ticks.
type ActorKind int

const (
	KindNPCStateful ActorKind = iota
	KindNPCShared
	KindPC
	KindDecorative
)

// Shared-VA agent slugs. An actor whose llm_memory_agent points at one of
// these is KindNPCShared: the VA is a stateless switchboard with no private
// memory, not the actor's own persistent VA. VisitorAgentName lives with the
// visitor lifecycle code (visitor.go). salem-generic backs the
// atmosphere/noticeboard cascades rather than any actor, but it is included
// here so an actor that ever points at it is never mistaken for stateful.
const (
	VendorAgentName  = "salem-vendor"
	GenericAgentName = "salem-generic"
)

// isSharedVAAgent reports whether an llm_memory_agent slug is one of the
// shared switchboard VAs rather than an actor's own private VA.
func isSharedVAAgent(agent string) bool {
	switch agent {
	case VendorAgentName, VisitorAgentName, GenericAgentName:
		return true
	default:
		return false
	}
}

// ClassifyActorKind derives an actor's Kind from its persisted driver columns.
// There is no actor_kind column, so Kind is reconstructed on every DB load
// from login_username + llm_memory_agent (a CHECK constraint keeps the two
// mutually exclusive). This mirrors the Kind that create_pc / create_npc set
// in memory at creation time:
//   - login_username present        -> KindPC (human player)
//   - llm_memory_agent is a shared VA -> KindNPCShared (vendor / visitor)
//   - llm_memory_agent present        -> KindNPCStateful (own persistent VA)
//   - neither                         -> KindDecorative (sprite-only, never ticked)
func ClassifyActorKind(loginUsername, llmAgent string) ActorKind {
	loginUsername = strings.TrimSpace(loginUsername)
	llmAgent = strings.TrimSpace(llmAgent)
	if loginUsername != "" {
		return KindPC
	}
	if isSharedVAAgent(llmAgent) {
		return KindNPCShared
	}
	if llmAgent != "" {
		return KindNPCStateful
	}
	return KindDecorative
}

// ActorState is the macro-state of an actor: what it is doing right now at
// a coarse level. Set softly by engine handlers when they observe a change;
// there is no strict FSM that validates transitions. Consumer switches must
// always include a default branch so adding new state values never breaks
// them. See shared/tasks/engine-in-memory-rewrite/state-model-sketch.
type ActorState string

const (
	StateIdle          ActorState = "idle"
	StateWalking       ActorState = "walking"
	StateConversing    ActorState = "conversing"
	StateWorking       ActorState = "working" // on shift, performing chores at workplace
	StateResting       ActorState = "resting" // take_break, dwell-credit accumulating
	StateSleeping      ActorState = "sleeping"
	StateShopping      ActorState = "shopping"       // buy_walker active
	StateInTransaction ActorState = "in_transaction" // pay flow open
	StateEating        ActorState = "eating"         // mid-consume
)

// Action is one LLM tool call (or engine-initiated action) recorded in the
// actor's RecentActions ring buffer. Used by perception build to diff
// against previous tick and by debug surfaces.
type Action struct {
	At      time.Time
	Tool    string // "speak", "move_to", "pay", ...
	Params  map[string]any
	Outcome string
	SceneID SceneID
}

// NeedKey identifies a kind of need: "hunger", "thirst", "tiredness", etc.
type NeedKey string

// ItemKind identifies a kind of item in inventory: "bread", "ale", etc.
type ItemKind string

// DwellCreditSource discriminates the two flavors of dwell credit:
// "object" (persistent while the actor is at a recovery-tagged village
// object — a Shade Tree, a Well) and "item" (one-shot countdown unlocked
// by consuming an item with a dwell effect — bread that keeps satiating
// you for a few minutes after eating).
type DwellCreditSource string

const (
	DwellSourceObject DwellCreditSource = "object"
	DwellSourceItem   DwellCreditSource = "item"
)

// DwellCreditKey is the composite primary key for an actor's dwell-credit
// row: object + attribute + source. Multiple rows on one (actor, object)
// are allowed — a shaded oak credits both tiredness and hunger
// independently, and "object" + "item" credits on the same attribute are
// separate rows.
type DwellCreditKey struct {
	ObjectID  VillageObjectID
	Attribute NeedKey
	Source    DwellCreditSource
}

// DwellCredit accumulates "I've been here long enough" toward periodic
// need recovery (ZBBS-172). The per-minute dwell tick reads these rows,
// applies DwellDelta to the actor when a DwellPeriodMinutes window has
// elapsed since LastCreditedAt, and advances the anchor.
//
// Source="object" credits persist as long as the actor stays at the
// object; their RemainingTicks is nil (open-ended). Source="item"
// credits have a finite RemainingTicks countdown that decrements per
// applied period and removes the row at zero.
//
// Kind carries the ItemKind that created an item-source credit so
// perception ("you are currently eating stew at the tavern") and event
// payloads can identify the meal without a separate lookup. Empty for
// source=object credits (no item involved).
type DwellCredit struct {
	ObjectID           VillageObjectID
	Kind               ItemKind // empty for source=object
	Attribute          NeedKey
	Source             DwellCreditSource
	LastCreditedAt     time.Time
	RemainingTicks     *int // nil for source=object; >0 for source=item
	DwellDelta         int  // negative — applied per period
	DwellPeriodMinutes int
}

// Acquaintance is a per-actor "do I know this person by name?" marker.
// Keyed by display name on the actor's Acquaintances map (TEXT-keyed in
// the underlying npc_acquaintance table so NPC↔PC pairs work without a
// cross-table FK). Applies to ALL NPCs regardless of Kind — even stateful
// NPCs need the gate so perception renders strangers as descriptors
// ("the blacksmith") rather than greeting unknowns by name.
//
// Written by a subscriber to ActorMet, fired on huddle membership change.
// Symmetric in concept but stored as directed pairs — the subscriber
// writes both directions.
type Acquaintance struct {
	FirstInteractedAt time.Time
}

// Relationship is the per-pair narrative state for a SHARED-VA NPC's
// view of another actor: a summary + an append-only trail of recent
// interactions, plus consolidation bookkeeping. Stateful NPCs do NOT
// populate Relationships — their own VA carries continuity via memory-
// api. Gate: Actor.Kind == KindNPCShared.
//
// SalientFacts is hard-bounded by MaxSalientFactsPerRelationship in
// RecordInteraction (FIFO eviction, DroppedFactCount telemetry) and
// further bounded by consolidation (when it lands), which rewrites
// SummaryText from the trail and prunes consolidated facts. Per-fact
// Text is truncated at write time to MaxSalientFactTextLen runes.
type Relationship struct {
	SummaryText        string
	SalientFacts       []SalientFact
	InteractionCount   int
	LastInteractionAt  *time.Time
	LastConsolidatedAt *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
	// DroppedFactCount counts FIFO evictions when SalientFacts hit
	// MaxSalientFactsPerRelationship. Per-pair telemetry: admin views
	// can spot relationships that are churning facts faster than
	// consolidation prunes them. Never decremented.
	DroppedFactCount int
}

// SalientFact is one entry in a Relationship's interaction trail. Mirrors
// the v1 JSONB element shape {at, kind, text} so the pg-impl SaveSnapshot
// can round-trip without a separate intermediate.
type SalientFact struct {
	At   time.Time
	Kind InteractionKind
	Text string
}

// InteractionKind tags what produced a SalientFact. Stored as plain
// string in JSONB; typed at the callsite to survive rename refactors
// and prevent typos.
type InteractionKind string

const (
	InteractionSpoke         InteractionKind = "spoke"
	InteractionHeard         InteractionKind = "heard"
	InteractionPaid          InteractionKind = "paid"
	InteractionPaidBy        InteractionKind = "paid_by"
	InteractionPayDeclinedBy InteractionKind = "pay_declined_by"
	InteractionDeclinedPay   InteractionKind = "declined_pay"
	InteractionCounteredBy   InteractionKind = "countered_by"
	InteractionCountered     InteractionKind = "countered"
	InteractionServed        InteractionKind = "served"
	InteractionServedBy      InteractionKind = "served_by"
	InteractionDelivered     InteractionKind = "delivered"
	InteractionReceived      InteractionKind = "received"
)

// MaxSalientFactTextLen caps per-fact Text at write time so a single
// rambling speech turn can't blow out a relationship's JSONB row. Mirrors
// v1's salientTextMaxLen (220 runes).
const MaxSalientFactTextLen = 220

// MaxSalientFactsPerRelationship caps stored SalientFacts per pair.
// Enforced in RecordInteraction with FIFO eviction (oldest dropped) +
// Relationship.DroppedFactCount increment. The cap is the upper-bound
// safety net — the consolidation cascade (when it lands) is expected to
// trigger and prune well below this in normal operation, so hitting the
// cap signals consolidation is failing or hasn't run yet. Will likely
// move to WorldSettings when consolidation MVP lands and tuning becomes
// per-environment.
const MaxSalientFactsPerRelationship = 30

// NewSalientFact builds a SalientFact with Text truncated to
// MaxSalientFactTextLen runes. Use this at every write callsite — never
// construct a SalientFact directly when the text comes from LLM output
// or other untrusted source.
func NewSalientFact(at time.Time, kind InteractionKind, text string) SalientFact {
	runes := []rune(text)
	if len(runes) > MaxSalientFactTextLen {
		text = string(runes[:MaxSalientFactTextLen])
	}
	return SalientFact{At: at, Kind: kind, Text: text}
}

// cloneRelationships deep-copies a Relationships map. Used by CloneActor
// and snapshotActor so the published Snapshot's Relationships are
// genuinely isolated from world state — a snapshot consumer mutating
// rel.SalientFacts[0].Text would otherwise corrupt the world's source
// of truth.
func cloneRelationships(src map[ActorID]*Relationship) map[ActorID]*Relationship {
	if src == nil {
		return nil
	}
	dst := make(map[ActorID]*Relationship, len(src))
	for k, v := range src {
		if v == nil {
			continue
		}
		vc := *v
		if v.LastInteractionAt != nil {
			t := *v.LastInteractionAt
			vc.LastInteractionAt = &t
		}
		if v.LastConsolidatedAt != nil {
			t := *v.LastConsolidatedAt
			vc.LastConsolidatedAt = &t
		}
		if v.SalientFacts != nil {
			// SalientFact is a value type with no inner pointers
			// (time.Time is a value), so slice copy is enough.
			vc.SalientFacts = append([]SalientFact(nil), v.SalientFacts...)
		}
		dst[k] = &vc
	}
	return dst
}

// cloneNarrativeState deep-copies a NarrativeState pointer. Same
// rationale as cloneRelationships — published snapshot must be
// isolated from world state.
func cloneNarrativeState(src *NarrativeState) *NarrativeState {
	if src == nil {
		return nil
	}
	nc := *src
	if src.LastConsolidatedAt != nil {
		t := *src.LastConsolidatedAt
		nc.LastConsolidatedAt = &t
	}
	return &nc
}

// cloneAcquaintances copies an Acquaintances map. Acquaintance is a
// value type with no inner pointers, so a per-key value-copy is enough.
func cloneAcquaintances(src map[string]Acquaintance) map[string]Acquaintance {
	if src == nil {
		return nil
	}
	dst := make(map[string]Acquaintance, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// NarrativeState is the engine-side continuity layer for shared-VA NPCs:
// the seed_text identity frame plus the evolving_summary the consolidator
// rewrites from accumulated relationship trails. Nil for stateful-VA
// actors — their own VA loads context/soul into the system prompt.
type NarrativeState struct {
	SeedText           string
	EvolvingSummary    string
	LastConsolidatedAt *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// VisitorState is the per-visitor archetype state. Non-nil marks the actor
// as a transient salem-visitor — a shared-VA NPC that arrived on a random
// map edge, hangs around the tavern for hours-to-a-day, then departs.
// Nil for every non-visitor actor (stateful NPCs, persistent shared-VA
// vendors, PCs, decoratives).
//
// Visitors are KindNPCShared but cross-cascade gates check VisitorState !=
// nil to skip narrative state accumulation (relationships, narrative
// consolidation, idle backstops). The pointer-presence check is the
// "transient visitor" predicate across the engine; see
// shared/notes/codebase/salem-engine-v2/visitor for the full surface.
//
// Archetype / Origin / Disposition come from per-spawn random pools in
// engine/sim/visitor.go and feed the perception "Visitors here" block plus
// the per-call identity preface the shared salem-visitor VA reads.
// ExpiresAt is the wall-clock departure deadline; LeaveDispatched flags
// whether the despawn walk has been issued (so the visitor ticker
// doesn't keep re-issuing it tick after tick).
type VisitorState struct {
	Archetype       string
	Origin          string
	Disposition     string
	ExpiresAt       time.Time
	LeaveDispatched bool
}

// cloneVisitorState deep-copies a VisitorState pointer. All fields are
// value types (string / time.Time / bool), so a struct copy is sufficient
// — but the helper exists so future pointer-bearing fields don't silently
// alias across the snapshot / mem-repo boundary.
func cloneVisitorState(src *VisitorState) *VisitorState {
	if src == nil {
		return nil
	}
	cp := *src
	return &cp
}

// Actor is the in-memory model of one participant in the simulation: NPC,
// PC, or decorative. One actor's data is logically one aggregate from the
// repository's perspective — ActorsRepo owns this entity plus all child
// tables (needs, inventory, relationships, acquaintances, narrative, dwell
// credits, attributes).
type Actor struct {
	ID          ActorID
	DisplayName string
	Role        string
	Kind        ActorKind

	// Identity routing — which VA backs this actor, login binding for PCs,
	// visitor archetype state, businessowner attribute (engine-authored
	// hospitality speech for shopkeepers / innkeepers / smiths — see
	// engine/sim/businessowner.go).
	LLMAgent           string
	LoginUsername      string
	VisitorState       *VisitorState
	BusinessownerState *BusinessownerState

	// IsAdmin gates the admin/editor write routes on the HTTP surface
	// (force-phase, object reposition/delete). Externally managed — set
	// directly in the DB for the human operators who administer the
	// village; the sim never writes it, and the checkpoint UPSERT
	// deliberately omits it so a save can't clobber the operator-set value
	// (LoadWorld reads it, SaveWorld leaves it). See migration ZBBS-WORK-271.
	IsAdmin bool

	// Spatial — current location. Pos is the actor's tile (padded grid
	// coords); see geom.go. Was a CurrentX/CurrentY int pair — folded into
	// TilePos so it can never be mixed with a world-pixel coordinate.
	InsideStructureID StructureID
	InsideRoomID      RoomID // 0 when not in a room
	Pos               TilePos
	CurrentHuddleID   HuddleID

	// Render identity — client-facing only, the engine never branches on
	// these. SpriteID references the npc_sprite catalog (World.Sprites) for
	// the sheet + animation rows; the client read surface inlines the
	// resolved sprite onto the agent DTO. Facing is the initial/spawn render
	// direction (north/south/east/west). The v2 engine does NOT update Facing
	// per-tick — the client derives live facing from movement delta — so it
	// round-trips through checkpoint as the last-persisted value, restoring
	// spawn orientation on restart (interior-facing writeback is a far-out
	// follow-up). Both empty for actors without a sprite (some PCs / purely
	// logical actors).
	SpriteID SpriteID
	Facing   string

	// Anchors — home and work bindings (empty for actors without them).
	HomeStructureID StructureID
	WorkStructureID StructureID

	// Schedule (minute-of-day; nil if unset — falls back to world dawn/dusk).
	// Persisted as nullable SMALLINT; nil round-trips through SQL NULL.
	ScheduleStartMin *int
	ScheduleEndMin   *int

	// Social schedule (decorative-NPC mover, #4 — persistence here; the
	// RunSocialTicker mover is a follow-up slice). SocialTag / SocialStartMin /
	// SocialEndMin are config that travels together — all-or-none, enforced by
	// the DB's actor_social_all_or_none CHECK and mirrored by the SaveSnapshot
	// pre-pass. When set on a decorative NPC they drive a daily walk to the
	// nearest village_object carrying SocialTag and back home.
	// SocialLastBoundaryAt is the edge-trigger idempotency stamp (the last
	// processed social boundary), kept separate from any shift stamp so the
	// two schedulers can't collide. These are pre-existing v1 columns; empty/
	// nil round-trips through SQL NULL.
	SocialTag            string     // "" = unset
	SocialStartMin       *int       // minute-of-day [0,1439]; nil = unset
	SocialEndMin         *int       // minute-of-day [0,1439]; nil = unset
	SocialLastBoundaryAt *time.Time // last processed boundary; nil = none

	// Mutable state.
	Needs     map[NeedKey]int
	Inventory map[ItemKind]int
	Coins     int

	// Activity windows.
	BreakUntil    *time.Time
	SleepingUntil *time.Time

	// SourceActivity is an in-flight, timed action AT a village object — eating
	// or drinking in place at a refresh source, or harvesting a gatherable
	// source (LLM-54). The actor is occupied until SourceActivity.Until; the
	// completion sweep (RunSourceActivityTicker) applies the effect then. nil
	// when not engaged at a source.
	//
	// TRANSIENT — deliberately NOT checkpointed (unlike BreakUntil/SleepingUntil),
	// like OpenUntil. The window is seconds-scale, so restart-loss is wholly
	// benign: a lost in-flight bite/harvest just never applied its effect (the
	// persistent need/inventory/supply mutation lands atomically at completion,
	// never mid-window), so there is no torn state to recover and the actor
	// simply re-engages on its next arrival/tool call. A durable column for a
	// 3-second timer would be exactly the "Postgres as cadence store" the
	// architecture avoids.
	SourceActivity *SourceActivity

	// OpenUntil is a keeper's commitment to stay open past the end of its shift,
	// until this instant (ZBBS-WORK-387 stay_open). The inverse of BreakUntil:
	// while set it SUPPRESSES the off-shift wind-down (the go-home / to-inn duty
	// in shiftDutyTarget and the renderDutySteer perception cue) so the
	// level-triggered shift producer stops re-ticking the keeper home every
	// cycle — UNLESS the keeper is peak-exhausted, in which case the needs floor
	// wins and it closes early. Set by sim.StayOpen, read by shiftDutyTarget;
	// mirrored onto ActorSnapshot.OpenUntil for buildDutySteer.
	//
	// TRANSIENT — deliberately NOT checkpointed (no repo round-trip), unlike
	// BreakUntil. Restart-loss is benign: a lost commitment just reverts the
	// keeper to the default close-on-schedule (the safe direction), self-heals via
	// the level-triggered shift producer, and couples with no persistent write
	// (open/closed is presence-derived in occupancy.go, not a stored flag).
	// Contrast BreakUntil, which IS checkpointed because it gates an in-flight
	// needs-recovery process whose interruption is a real regression (WORK-410).
	OpenUntil *time.Time

	// LastTirednessRecoveryAt is the cursor the tiredness-recovery sweep
	// advances as it credits recovery while BreakUntil/SleepingUntil are
	// open. It doubles as the fractional carry: the sweep advances it by
	// exactly the time represented by whole recovered units, so sub-unit
	// minutes stay in the next pass's window. Cleared the moment the actor
	// stops resting (or its window ends) so a fresh window can't be credited
	// against a stale cursor.
	//
	// TRANSIENT — deliberately not persisted (no repo round-trip), unlike
	// BreakUntil/SleepingUntil which ARE checkpointed. So on a LoadWorld
	// where an actor was mid-sleep, the window is restored but the cursor is
	// nil and re-inits to "now" — forfeiting all recovery accrued since
	// bed-down, which can be many units, not just a sub-unit fraction. That
	// loss is bounded and practically nil: a full night over-recovers past
	// NeedMax, and HOME-282 wakes NPCs on shift-start regardless of
	// tiredness, so a restored-mid-sleep NPC still wakes fully rested.
	// Re-persisting the cursor would reintroduce a durable cadence field we
	// deliberately avoid (Postgres is durable storage, not a cadence store).
	LastTirednessRecoveryAt *time.Time

	// LastPCInputAt is the wall-clock instant of this PC's last deliberate
	// action (move / speak / pay), stamped by touchPCInput. It drives two PC
	// sleep behaviors (pc_sleep.go): the idle-auto-bed sweep beds a lodger PC
	// once this is older than the idle threshold, and any action that stamps it
	// also input-wakes a sleeping PC. nil for NPCs and for a PC that hasn't
	// acted since load. v1 parity: actor.last_pc_input_at.
	//
	// TRANSIENT — not persisted (like LastTirednessRecoveryAt). On a LoadWorld
	// a restored PC starts with a nil cursor, so the idle sweep won't bed them
	// until they next act; harmless (an idle-but-never-acted PC simply isn't
	// auto-bedded until its first stamped action) and keeps cadence state out
	// of durable storage.
	LastPCInputAt *time.Time

	// LastPCSeenAt is the wall-clock instant of this PC's last client poll,
	// stamped by StampPCSeen on every /pc/me hit (ZBBS-WORK-326). The v2 client
	// polls /pc/me every 10s while the game is open, so a fresh LastPCSeenAt means
	// "a live client is driving this PC"; once the player closes the tab the polls
	// stop and it goes stale. The presence sweep (pc_presence.go) ejects a stale PC
	// from its huddle, and the encounter cascades skip stale PCs, so co-located
	// NPCs stop burning ticks greeting an absent player (v1 parity: the
	// last_pc_seen_at presence-cleanup sweep; the prod ghost-PC cost bug). nil for
	// NPCs and for a PC no client has polled this session.
	//
	// TRANSIENT — not persisted (like LastPCInputAt / LastTirednessRecoveryAt).
	// Presence is live-session state, not durable: a restored PC starts nil (=
	// treated as absent until its first poll), which is correct — after a restart
	// the client must re-attach to be "present". Keeps ephemeral cadence state out
	// of durable storage.
	LastPCSeenAt *time.Time

	// Tick scheduling.
	LastTickedAt *time.Time

	// Reactor-evaluator state — Phase 2 PR 2. WarrantedSince + WarrantDueAt
	// + Warrants together form the actor's tick-eligibility record:
	//
	//   - WarrantedSince: timestamp the warrant cycle began (earliest stamp
	//     in this cycle). Nil = no pending signal.
	//   - WarrantDueAt: now + jitter, stamped at warrant time. The evaluator
	//     emits ReactorTickDue when now >= WarrantDueAt.
	//   - Warrants: list of signals accumulated during this warrant cycle.
	//     Cleared at evaluator emit time; new stamps during the in-flight
	//     LLM call start a fresh cycle that fires after completion. See
	//     reactor.go for the full design rationale.
	//
	// All three are ephemeral — wiped on LoadWorld so checkpoint reload
	// doesn't wedge actors with stale interface-typed payloads.
	WarrantedSince *time.Time
	WarrantDueAt   *time.Time
	Warrants       []WarrantMeta

	// TickInFlight gates the evaluator from re-emitting an actor whose LLM
	// call is pending. TickAttemptID is the generation that disambiguates
	// stale completions — a late-arriving completion from a timed-out
	// attempt must not clear a newer attempt's in-flight flag.
	//
	// Both wiped on LoadWorld.
	TickInFlight  bool
	TickAttemptID TickAttemptID

	// RecentReactorTicks is the per-actor ring of recent reactor-tick
	// emission timestamps. Drives the per-minute gross gate
	// (MaxReactorTicksPerActorPerMinute). Lazily allocated on first emit.
	RecentReactorTicks *RingBuffer[time.Time]

	// Red-need backstop pacing (ZBBS-HOME-363). Per-actor exponential
	// backoff for the red-need re-warrant sweep
	// (engine/sim/red_need_backstop_commands.go). The hourly needs-tick
	// re-warrant (needs_tick.go, HOME-329 level-trigger) is too slow to
	// re-engage an actor that burned a tick failing to resolve a red need
	// and then went idle — it sits frozen until the next hour boundary or
	// the 30-min idle backstop. The backstop sweep re-warrants such an
	// actor promptly, but a genuinely-unresolvable red need must NOT
	// re-warrant on a tight loop: every warrant is an LLM deliberation, so
	// a stuck actor would burn tokens indefinitely. The cadence therefore
	// backs off exponentially toward the idle-backstop floor and resets to
	// base only when the need actually drops (real progress).
	//
	//   - RedNeedNextWarrantAt: earliest wall-clock the sweep may stamp the
	//     next red-need warrant. Nil = eligible immediately (never paced).
	//   - RedNeedBackoffLevel: escalation level; the delay is
	//     base << level, capped at RedNeedBackstopMaxDelay.
	//   - RedNeedLastKey / RedNeedLastValue: the need + its value recorded
	//     at the last stamp, so the next sweep detects progress (value
	//     dropped → reset to base) vs. stall (unchanged → escalate).
	//
	// All ephemeral — wiped on LoadWorld with the rest of the reactor
	// pacing state, so a fresh-loaded actor starts un-paced.
	RedNeedNextWarrantAt *time.Time
	RedNeedBackoffLevel  int
	RedNeedLastKey       NeedKey
	RedNeedLastValue     int

	// inFlightSourceKeys is the set of WarrantSourceKeys consumed into the
	// actor's current in-flight tick attempt — recorded at ReactorTickDue
	// emit, consulted by tryStampWarrant's in-flight dedup path, and
	// resolved by CompleteReactorTick's terminal-status policy. nil when no
	// tick is in flight. Unexported — internal dedup bookkeeping, not part
	// of the observable reactor contract. Ephemeral: wiped on LoadWorld.
	inFlightSourceKeys map[WarrantSourceKey]struct{}

	// recentlyConsumedSourceKeys is the bounded per-actor set of warrant
	// source keys whose tick attempt addressed them — tryStampWarrant's
	// third dedup path, suppressing a delayed duplicate of an already-
	// addressed stimulus. The value is the insertion time, for TTL expiry
	// (recentlyConsumedTTL) and oldest-first eviction (recentlyConsumedCap).
	// Unexported; ephemeral — wiped on LoadWorld.
	recentlyConsumedSourceKeys map[WarrantSourceKey]time.Time

	// awaitingReplyFrom is this actor's turn-state as a SPEAKER: for each
	// peer it has addressed and is awaiting a reply from, the wall-clock
	// time it last addressed them. The single authoritative directed edge
	// — "is it my turn / am I owed a reply" is DERIVED from peers' maps
	// (some peer holds awaitingReplyFrom[me]), never stored separately, so
	// the two views can't drift. Set when this actor speaks to a resolved
	// addressee (sim.Speak / Spoke.AddressedID); cleared when the awaited
	// party speaks (any utterance by them IS the reply) or on huddle
	// leave/conclude. Keyed by addressee ActorID. Drives the ZBBS-WORK-370
	// turn-taking gate: the sim.Speak backstop reads it to reject an idle
	// re-pitch (turn_state.go), and perception renders a turn-line off the
	// snapshot copy. (Supersedes the retired HOME-331 heard-speech miss-counter,
	// ZBBS-WORK-371.) Unexported;
	// ephemeral — wiped on LoadWorld, copied in CloneActor so the published
	// snapshot sees it.
	awaitingReplyFrom map[ActorID]time.Time

	// Locomotion — Phase 2 PR 4.
	//
	// MoveIntent is the actor's in-flight movement state, nil when the
	// actor is not moving. The locomotion ticker re-plans a path against
	// it every tick (it deliberately caches no path — see MoveIntent).
	//
	// MoveAttemptCounter is the per-actor monotonic generation:
	// incremented on every accepted MoveActor command and stamped as the
	// new MoveIntent.AttemptID, so async subscribers can tell a
	// superseded attempt's events from the current one.
	//
	// The counter is checkpointed (it must stay monotonic across
	// restarts). MoveIntent itself is NOT — what the checkpoint carries
	// is the intent's DESTINATION (actor.move_destination, derived from
	// the live MoveIntent at every checkpoint write), which comes back as
	// ResumeDestination below and is re-dispatched through MoveActor at
	// boot. ZBBS-HOME-449: without that, a deploy restart stranded any
	// mid-walk actor wherever the final checkpoint caught them.
	MoveIntent         *MoveIntent
	MoveAttemptCounter MovementAttemptID

	// ResumeDestination is the checkpointed destination of a walk the
	// PREVIOUS process had in flight at shutdown (ZBBS-HOME-449).
	// Load-only: pg LoadAll populates it from actor.move_destination; the
	// boot resume sweep (ResumeCheckpointedWalks) re-dispatches it through
	// the normal MoveActor — path re-planned from the checkpointed tile,
	// arrival warrant fires as usual — and clears it. Never written back:
	// checkpoint writes derive move_destination from the live MoveIntent,
	// so a walk that ends normally clears its column on the next write.
	ResumeDestination *MoveDestination

	// lastStrandedWarrantAt rate-limits the anomalous-position backstop
	// (ZBBS-HOME-450): the idle-backstop sweep stamps at most one
	// StrandedWarrantReason per strandedWarrantCooldown on a still-
	// stranded actor, so an actor that deliberates and CHOOSES to stand
	// in the open doesn't burn an LLM call every sweep. In-memory,
	// restart-lossy on purpose — the first post-boot sweep re-fires for a
	// still-stranded actor, which doubles as boot recovery.
	lastStrandedWarrantAt time.Time

	// Relationships (per-actor views, not a global graph).
	Acquaintances map[string]Acquaintance
	Relationships map[ActorID]*Relationship
	Narrative     *NarrativeState

	// Behavior history — load-bearing for diff-against-previous and loop
	// detection. RecentActions and LastSnapshot are in-memory only (not
	// checkpointed); post-restart blind spot for the first few ticks is
	// acceptable.
	RecentActions *RingBuffer[Action]
	LastSnapshot  *ActorSnapshot

	// Macro-state — soft transitions, engine sets on observation (no strict
	// FSM validation). State is checkpointed so restart resumes in the same
	// state.
	State ActorState

	DwellCredits map[DwellCreditKey]*DwellCredit

	// Observed is this actor's decaying, in-memory experiential memory of volatile
	// place conditions — businesses found shut (ObservedClosed, HOME-353) and
	// (vendor, item) pairs found out of stock (ObservedOutOfStock, HOME-363) —
	// folded into one store keyed by (structure, item, condition). Perception
	// deprioritizes a cue pointing at a remembered-shut/dry place; the memory
	// self-clears when the place is re-observed otherwise; each condition DECAYS
	// after its TTL so the NPC retries rather than believing it shut/dry forever.
	// Experiential (learned only by going / trying, never map-wide omniscience)
	// and restart-lossy by design — contrast KnownPlaces below, which is durable
	// positive knowledge. Written by the capture subscribers in closed_business.go
	// / out_of_stock.go; the zero value is an empty store. LLM-80 (epic LLM-76).
	// See observed_state.go.
	Observed ObservedStates

	// KnownPlaces is this actor's DURABLE world-memory: the places/sources it
	// knows and what each is good for (its Affordances). Unlike the decaying,
	// in-memory Observed store above (negative "found it shut/dry just now"
	// observations), a known place is PERMANENT positive knowledge — a location
	// doesn't move, you don't un-know your own farm — and
	// is checkpointed to actor_known_place (same durability tier as
	// salient_facts). Populated on affordance-bearing experience
	// (gather/purchase/consume-at-source) by the known_place.go capture path, and
	// seeded a-priori for owned sources + home/work anchors at LoadWorld.
	// nil/empty when the actor knows no places yet — a loaded actor carries an
	// empty (non-nil) map like the other child collections. LLM-77 (epic
	// LLM-76); ships inert — no resolver/cue reads it yet (LLM-78/79).
	KnownPlaces map[PlaceRef]*KnownPlace

	// RestockPolicy carries this actor's produce/buy entries, unioned
	// across their role attributes (tavernkeeper + worker, etc.). Read
	// from actor_attribute.params.restock in legacy; nil for actors
	// without a restock-bearing attribute.
	RestockPolicy *RestockPolicy

	// GatherTargetObjectID is the village object an agent NPC deliberately
	// walked to (ActorArrived.DestObjectID, stamped by handleGatherTargetOnArrival),
	// so a later gather / StartHarvest prefers THAT bush over the nearest one.
	// The fix for a dense interleaved plot where nearest-wins resolution handed
	// her a depleted or wrong-item bush (LLM-93). An arrival at a structure or a
	// bare position carries an empty DestObjectID, which clears a stale bush
	// target. Transient (not checkpointed); validity (in reach + stocked) is
	// re-checked at gather time, so a lingering id is harmless.
	GatherTargetObjectID VillageObjectID

	// ProduceState carries the per-item production anchor — used by
	// produce_tick to compute units owed since the last execution.
	// One entry per item the actor produces; populated lazily on first
	// observation.
	ProduceState map[ItemKind]*ProduceState

	// RoomAccess — this actor's grants to enter private/staff rooms.
	// Keyed by (RoomID, Source). Stamped by AssignBedroomForLodger
	// (source=ledger) and flipped to Active=false by ExpireRoomAccess
	// when ExpiresAt passes.
	RoomAccess map[RoomAccessKey]*RoomAccess

	// Free-form behavior specs (typed lazily per subsystem during port).
	Attributes map[string][]byte

	// Summon errand perception cues (ZBBS-HOME-311). Both transient,
	// in-memory only (restart-lossy like the errand machine itself), and
	// consumed-on-next-act:
	//
	//   - PendingSummon is set on the TARGET when a messenger delivers a
	//     summons ("come to <place>"), driving them to move_to the summon
	//     point. Non-nil drives the "## You have been summoned" perception
	//     section.
	//   - SummonRefusal is set on the SUMMONER when their messenger returns
	//     unable to find the target. Non-nil drives the "## Your messenger
	//     returned" perception section.
	//
	// Each fades after the actor next acts (ConsumeSummonCuesOnTick clears
	// them on the actor's reactor tick), mirroring v1's drop-once-consumed
	// behavior. Deep-cloned by CloneActor + mirrored into ActorSnapshot so
	// perception (which runs purely off the snapshot) can read them.
	PendingSummon *PendingSummon
	SummonRefusal *SummonRefusal
}

// PendingSummon is the target-side perception cue: a messenger delivered a
// summons asking this actor to come to a place. Value-cloned (no inner
// pointers).
type PendingSummon struct {
	SummonerName string
	Place        string
	Reason       string // "" when the summoner gave none
	At           time.Time
}

// SummonRefusal is the summoner-side perception cue: the messenger returned
// unable to locate the target. Value-cloned (no inner pointers).
type SummonRefusal struct {
	TargetName string
	At         time.Time
}

// clonePendingSummon / cloneSummonRefusal deep-copy the cue structs. They
// carry no inner pointers, so a dereference suffices; named helpers keep the
// clone idiom uniform with the other CloneActor helpers.
func clonePendingSummon(p *PendingSummon) *PendingSummon {
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

func cloneSummonRefusal(r *SummonRefusal) *SummonRefusal {
	if r == nil {
		return nil
	}
	cp := *r
	return &cp
}

// CloneActor returns a deep copy of an Actor suitable for the mem-repo
// serialization boundary. Mutated containers (Needs, Inventory,
// DwellCredits, RoomAccess, ProduceState, Acquaintances, Relationships)
// and pointer fields commands rebind (BreakUntil, SleepingUntil,
// LastTickedAt, SocialLastBoundaryAt, Narrative) are cloned.
// Attributes is
// deep-cloned including each []byte payload. RecentActions is cloned
// via RingBuffer.Clone. MoveIntent is deep-cloned via
// cloneMoveIntent (its MoveDestination carries StructureID / Position
// pointer fields that would otherwise alias across the boundary).
//
// Aliased today (NOT cloned) because no current command mutates them:
//   - LastSnapshot — placeholder/empty struct
//
// TODO: clone RestockPolicy when a command starts mutating it. Read-only
// post-load today but future admin edits could mutate it via a command;
// aliasing now is correct but fragile against future command authors.
//
// Used by mem.ActorsRepo.Seed / LoadAll / SaveSnapshot to enforce that a
// round-trip through the repo breaks pointer identity, the way the pg
// impl will at cutover.
func CloneActor(a *Actor) *Actor {
	if a == nil {
		return nil
	}
	cp := *a

	if a.Needs != nil {
		cp.Needs = make(map[NeedKey]int, len(a.Needs))
		for k, v := range a.Needs {
			cp.Needs[k] = v
		}
	}
	if a.Inventory != nil {
		cp.Inventory = make(map[ItemKind]int, len(a.Inventory))
		for k, v := range a.Inventory {
			cp.Inventory[k] = v
		}
	}
	if a.BreakUntil != nil {
		t := *a.BreakUntil
		cp.BreakUntil = &t
	}
	if a.SleepingUntil != nil {
		t := *a.SleepingUntil
		cp.SleepingUntil = &t
	}
	if a.SourceActivity != nil {
		// Value struct with no nested pointers — a shallow copy breaks aliasing.
		sa := *a.SourceActivity
		cp.SourceActivity = &sa
	}
	if a.OpenUntil != nil {
		t := *a.OpenUntil
		cp.OpenUntil = &t
	}
	if a.LastTirednessRecoveryAt != nil {
		t := *a.LastTirednessRecoveryAt
		cp.LastTirednessRecoveryAt = &t
	}
	if a.LastPCInputAt != nil {
		t := *a.LastPCInputAt
		cp.LastPCInputAt = &t
	}
	if a.LastPCSeenAt != nil {
		t := *a.LastPCSeenAt
		cp.LastPCSeenAt = &t
	}
	if a.SocialLastBoundaryAt != nil {
		// Deep-cloned (the social mover stamps it each boundary), like the
		// other mutated *time.Time cursors. SocialStartMin/EndMin are config
		// pointers rebound on edit, not mutated through, so they follow the
		// schedule-pointer convention and stay shallow-aliased via `cp := *a`.
		t := *a.SocialLastBoundaryAt
		cp.SocialLastBoundaryAt = &t
	}
	if a.LastTickedAt != nil {
		t := *a.LastTickedAt
		cp.LastTickedAt = &t
	}
	if a.WarrantedSince != nil {
		t := *a.WarrantedSince
		cp.WarrantedSince = &t
	}
	if a.WarrantDueAt != nil {
		t := *a.WarrantDueAt
		cp.WarrantDueAt = &t
	}
	if a.Warrants != nil {
		// WarrantMeta is a value type whose Reason field holds an interface
		// over concrete value structs (BasicWarrantReason, PCSpeechWarrantReason,
		// NPCSpeechWarrantReason).
		// Slice copy is safe — appending to one side won't reflect in the
		// other, and the concrete reason structs have no inner pointers
		// today. If a future WarrantReason adds inner pointers, deep-clone
		// it here.
		cp.Warrants = append([]WarrantMeta(nil), a.Warrants...)
	}
	if a.RecentReactorTicks != nil {
		cp.RecentReactorTicks = a.RecentReactorTicks.Clone()
	}
	if a.inFlightSourceKeys != nil {
		cp.inFlightSourceKeys = make(map[WarrantSourceKey]struct{}, len(a.inFlightSourceKeys))
		for k := range a.inFlightSourceKeys {
			cp.inFlightSourceKeys[k] = struct{}{}
		}
	}
	if a.recentlyConsumedSourceKeys != nil {
		cp.recentlyConsumedSourceKeys = make(map[WarrantSourceKey]time.Time, len(a.recentlyConsumedSourceKeys))
		for k, v := range a.recentlyConsumedSourceKeys {
			cp.recentlyConsumedSourceKeys[k] = v
		}
	}
	if a.awaitingReplyFrom != nil {
		cp.awaitingReplyFrom = make(map[ActorID]time.Time, len(a.awaitingReplyFrom))
		for k, v := range a.awaitingReplyFrom {
			cp.awaitingReplyFrom[k] = v
		}
	}
	if a.Acquaintances != nil {
		cp.Acquaintances = cloneAcquaintances(a.Acquaintances)
	}
	if a.Relationships != nil {
		cp.Relationships = cloneRelationships(a.Relationships)
	}
	if a.Narrative != nil {
		cp.Narrative = cloneNarrativeState(a.Narrative)
	}
	if a.VisitorState != nil {
		cp.VisitorState = cloneVisitorState(a.VisitorState)
	}
	if a.BusinessownerState != nil {
		cp.BusinessownerState = cloneBusinessownerState(a.BusinessownerState)
	}
	if a.RecentActions != nil {
		cp.RecentActions = a.RecentActions.Clone()
	}
	if a.DwellCredits != nil {
		cp.DwellCredits = cloneDwellCredits(a.DwellCredits)
	}
	// cp := *a aliased the backing map; Clone breaks the alias (cheap no-op when
	// the store is empty).
	cp.Observed = a.Observed.Clone()
	if a.KnownPlaces != nil {
		cp.KnownPlaces = cloneKnownPlaces(a.KnownPlaces)
	}
	if a.ProduceState != nil {
		cp.ProduceState = make(map[ItemKind]*ProduceState, len(a.ProduceState))
		for k, v := range a.ProduceState {
			if v == nil {
				continue
			}
			vc := *v
			cp.ProduceState[k] = &vc
		}
	}
	cp.RoomAccess = cloneRoomAccess(a.RoomAccess)
	if a.Attributes != nil {
		cp.Attributes = make(map[string][]byte, len(a.Attributes))
		for k, v := range a.Attributes {
			cp.Attributes[k] = append([]byte(nil), v...)
		}
	}
	if a.MoveIntent != nil {
		cp.MoveIntent = cloneMoveIntent(a.MoveIntent)
	}
	if a.ResumeDestination != nil {
		dest := cloneMoveDestination(*a.ResumeDestination)
		cp.ResumeDestination = &dest
	}
	if a.PendingSummon != nil {
		cp.PendingSummon = clonePendingSummon(a.PendingSummon)
	}
	if a.SummonRefusal != nil {
		cp.SummonRefusal = cloneSummonRefusal(a.SummonRefusal)
	}
	return &cp
}

// ActorSnapshot is the slim immutable view of an actor's decision-relevant
// state at the moment of the last tick. Consumed by:
//   - Snapshot publishing (admin reads, perception diff against previous)
//   - Checkpoint writes (serialized to actor_snapshot row)
//   - Scene origin capture (Scene.ParticipantStateAtOrigin) for diff-against-
//     scene-start in perception build
//
// MoveIntent is deliberately NOT part of this slim view. In-flight
// movement state crosses the mem-repo / checkpoint boundary on the full
// Actor (via CloneActor); a consumer that needs it reads the Actor, not
// the snapshot.
type ActorSnapshot struct {
	AtTick      uint64
	DisplayName string
	Kind        ActorKind
	State       ActorState // checkpointed; restart resumes in same state
	Role        string

	// LLMAgent mirrors the live Actor's LLM-agent slug (VA backing this
	// actor in llm-memory-api). Off-world consumers — notably the
	// reactor-tick harness — read this to populate llm.Request.Model when
	// calling Complete. Empty for actors with no VA backing (PCs, purely
	// decorative NPCs).
	LLMAgent string

	// LoginUsername mirrors the live Actor's PC login (the PC counterpart to
	// LLMAgent — empty for NPCs). Carried so the read surface (httpapi pc/me)
	// can resolve the caller's own PC from the authenticated session by
	// scanning the published snapshot, instead of a command-channel round trip
	// into live world state for a pure read.
	LoginUsername string

	// LastPCSeenAt mirrors the live Actor's last /pc/me poll stamp (nil for
	// NPCs and for a PC that hasn't polled this session). Carried so read-path
	// consumers can apply the same presence-staleness gate as the sim side
	// (PCPresenceStale) — notably the pc/me indoor co-located roster, which
	// must not advertise a stale (logged-out) PC the speak path's
	// EnsureColocatedHuddle would exclude (ZBBS-HOME-371).
	LastPCSeenAt *time.Time

	InsideStructureID StructureID
	// InsideRoomID mirrors the live Actor's current room (0 when not in a
	// room). Carried so the read surface (httpapi pc/me) can compute the
	// private-room audience scope — the v2 port of v1 actorPrivateRoomScope —
	// purely over the snapshot: look the id up in the actor's
	// InsideStructureID Rooms and scope speech when its Kind is private/staff.
	InsideRoomID    RoomID
	Pos             TilePos // padded grid tile; was CurrentX/CurrentY (see geom.go)
	CurrentHuddleID HuddleID
	Needs           map[NeedKey]int
	InventoryHash   uint64 // fast-compare; computed at snapshot time
	Coins           int

	// SpriteID + Facing mirror the live Actor's render identity at snapshot
	// time so the client read surface (httpapi) can resolve + inline the
	// sprite without a world-goroutine round trip. Both checkpointed (carried
	// on the full *Actor via CloneActor); these snapshot copies are the
	// read-path view. See Actor.SpriteID / Actor.Facing.
	SpriteID SpriteID
	Facing   string

	// In-flight movement read-path projection (ZBBS-HOME-336). The
	// value-typed destination of the actor's MoveIntent at snapshot time —
	// MoveDestKind is "" when the actor is not moving. This is NOT the live
	// MoveIntent (deliberately excluded, per the doc-comment above); it is the
	// read-path view perception uses to remind the subject of its own
	// in-progress walk ("currently: walking to the Tavern"), the movement
	// analogue of the ActiveDwellCredits cue that keeps an NPC from abandoning
	// an in-progress meal. Resolved to a label in perception.buildActorView
	// against snap.Structures / snap.VillageObjects.
	MoveDestKind        MoveDestinationKind
	MoveDestStructureID StructureID
	MoveDestObjectID    VillageObjectID
	MoveDestPos         TilePos

	// Editor read-path config — mirrors the live Actor's anchors + schedules
	// at snapshot time so the client read surface (httpapi AgentDTO) can show
	// current state without a world-goroutine round trip, the same posture as
	// SpriteID/Facing above. The engine never branches on these snapshot copies
	// — it reads the live Actor; these exist only for the editor/HUD read API.
	// AttributeSlugs is the SORTED set of the actor's attribute keys (the live
	// Actor.Attributes map's keys); the editor renders them as chips and only
	// needs the slugs, so the opaque param payloads are deliberately NOT carried
	// here. HomeStructureID/WorkStructureID are the actor's home/work anchors
	// (empty when unset). ScheduleStartMin/EndMin + SocialTag/SocialStartMin/
	// SocialEndMin are the work-shift and social-gathering windows (nil/empty =
	// unset → the editor shows "inherit dawn/dusk"); the *int fields are copied
	// into fresh pointers by snapshotActor so the published snapshot never
	// aliases the live Actor's pointers.
	AttributeSlugs   []string
	HomeStructureID  StructureID
	WorkStructureID  StructureID
	ScheduleStartMin *int
	ScheduleEndMin   *int
	SocialTag        string
	SocialStartMin   *int
	SocialEndMin     *int

	// Per-actor knowledge state — read by perception build:
	//   - Acquaintances gates "Around you" name-vs-descriptor rendering
	//     (all NPC kinds — stateful and shared).
	//   - Relationships + Narrative populate the shared-only "Who you
	//     are:" / "What you remember of those here:" sections; nil/empty
	//     for stateful and PC kinds.
	// All three deep-cloned by snapshotActor so the published Snapshot is
	// isolated from world state.
	Acquaintances map[string]Acquaintance
	Relationships map[ActorID]*Relationship
	Narrative     *NarrativeState

	// AwaitingReplyFrom mirrors the live Actor's turn-state edge
	// (Actor.awaitingReplyFrom) at snapshot time: addressee -> when this actor
	// last addressed them and is awaiting a reply. Deep-cloned by snapshotActor so
	// the published snapshot doesn't alias the world's mutable map. Perception
	// build (ZBBS-WORK-370) reads the subject's OWN edges AND its present peers'
	// edges to derive the turn-line ("you spoke to X, wait for their reply" / "X
	// is waiting for your reply") and the act-now coda swap. nil until the actor
	// first addresses someone. Ephemeral on the live Actor (wiped on load); this
	// snapshot copy is the read-path view.
	AwaitingReplyFrom map[ActorID]time.Time

	// ColocatedAudienceIDs are the conversational actors an UNHUDDLED actor would
	// reach if it spoke from its current position — the non-mutating read mirror
	// of the audience the speak path assembles (EnsureColocatedHuddle forms/joins
	// the structure huddle, then buildHuddlePeerSet). Computed world-side by
	// colocatedAudienceIDs at publish time so perception's "## Around you"
	// co-presence line and the speak "there is no one here to hear you" gate
	// derive from ONE scope rule rather than two that can drift (ZBBS-WORK-407).
	// Empty for a huddled actor (its company is the huddle, surfaced via
	// SurroundingsView.HuddleMembers) and for an actor genuinely alone in scope.
	// A derived per-publish read projection — NOT checkpointed (the checkpoint
	// serializes the live *Actor's columns, not this struct), recomputed each
	// republish like the MoveDest* projections above.
	ColocatedAudienceIDs []ActorID

	// ColocatedSleeperIDs are the co-present SLEEPING conversational actors an
	// UNHUDDLED actor can see in its scope — the asleep counterpart to
	// ColocatedAudienceIDs, which omits sleepers. Surfaced so perception's
	// "## Around you" can mark a sleeper "(asleep)" instead of dropping it (a
	// sleeper used to vanish from the speaker's view entirely, who then
	// addressed it expecting a reply — ZBBS-WORK-426, residual of HOME-436).
	// Sleepers stay OUT of ColocatedAudienceIDs, so they are never a speak
	// target and the no-audience gate is unchanged. Same per-publish projection
	// posture as ColocatedAudienceIDs — NOT checkpointed, recomputed each
	// republish.
	ColocatedSleeperIDs []ActorID

	// CurrentLoiterObjectID is the named village object whose loiter pin owns
	// the actor's current tile (resolveLoiteringObject, Chebyshev <=
	// LoiterAttributionTiles), or "" when the actor stands at no pin. It is the
	// co-location signal perception's buildActiveDwellCredits gates on: a
	// DwellCredit renders as an active "you are <verb> at X" self-state line
	// only while its ObjectID matches this — so a credit that lingers in the
	// map after a walk-away (until the next dwell-tick sweep deletes it) stops
	// being asserted as live the instant the actor leaves the pin (LLM-68).
	// Resolved world-side with the SAME resolver/radius the dwell-tick
	// walk-away check uses (actorAtCreditObject), so perception and the engine
	// agree on keep-vs-drop. Stamped only when the actor holds a dwell credit
	// (the sole consumer). Same per-publish projection posture as
	// ColocatedAudienceIDs — NOT checkpointed, recomputed each republish.
	CurrentLoiterObjectID VillageObjectID

	// GatherTargetObjectID mirrors Actor.GatherTargetObjectID onto the published
	// snapshot so the at-bush gather cue (findGatherableCue) can prefer the bush
	// the actor walked to, in lockstep with the gather command (LLM-93). Stamped
	// in snapshotActor from the live actor (not recomputed at republish like
	// CurrentLoiterObjectID).
	GatherTargetObjectID VillageObjectID

	// SourceActivityKind / SourceActivityObjectID / SourceActivityAttribute are
	// the read-path projection of an in-flight timed eat/drink/harvest at a
	// source (Actor.SourceActivity, LLM-54). Kind == "" when the actor is not
	// engaged. Surfaced so perception renders a STANDING "you are picking at the
	// bush — stay put, walking off abandons it" self-state line (the source-
	// activity analogue of the MoveDest* in-progress-walk cue): whatever ticks
	// the actor mid-window — a PC speaking, a red need — it reads its own state
	// and holds rather than re-deciding from scratch (LLM-69). Attribute is the
	// primary need a refresh eases (drives the eat/drink verb); empty for a
	// harvest. ObjectID resolves the source's display label in perception
	// (resolveDwellPinLabel), the same way MoveDest* / dwell pins do. Projected
	// only while the window is live (BusyAtSource) — an expired-but-unswept
	// window, cleared by the next completion sweep, reads as not-engaged. Same
	// per-publish projection posture as ColocatedAudienceIDs — NOT checkpointed,
	// recomputed each republish.
	SourceActivityKind      SourceActivityKind
	SourceActivityObjectID  VillageObjectID
	SourceActivityAttribute NeedKey

	// VisitorState mirrors the live Actor's transient-visitor state at
	// snapshot time. Non-nil marks the actor as a salem-visitor; the
	// perception "Visitors here" block reads Archetype/Origin/Disposition
	// from it. Nil for every non-visitor actor (the steady-state case).
	// Deep-cloned by snapshotActor so published snapshots don't alias the
	// world's mutable visitor record.
	VisitorState *VisitorState

	// BusinessownerState mirrors the live Actor's businessowner attribute
	// at snapshot time. Non-nil marks the actor as a shopkeeper / innkeeper
	// / smith eligible for engine-authored hospitality speech. Flavor
	// selects the phrase pool (see engine/sim/businessowner.go). Deep-cloned
	// by snapshotActor so published snapshots don't alias the world's
	// mutable record.
	BusinessownerState *BusinessownerState

	// DwellCredits mirror the live Actor's per-pin recovery credits at
	// snapshot time so perception build can surface "you are currently
	// eating stew at the tavern" as part of the actor's self-state. Deep-
	// cloned by snapshotActor so the published Snapshot does not alias
	// the world's mutable credit map.
	DwellCredits map[DwellCreditKey]*DwellCredit

	// Observed mirrors the live Actor's decaying experiential observed-state
	// memory (shut businesses, out-of-stock vendor-items) at snapshot time, so
	// perception can deprioritize a cue pointing at a remembered-shut/dry place.
	// Deep-cloned by snapshotActor so published snapshots don't alias the world's
	// mutable store. The zero value is an empty store. See Actor.Observed and
	// observed_state.go. LLM-80 (epic LLM-76).
	Observed ObservedStates

	// KnownPlaces mirrors the live Actor's durable world-memory at snapshot time
	// so the (future LLM-78/79) move_to resolver + perception cues can read
	// remembered places off the published Snapshot. Deep-cloned by snapshotActor
	// (via cloneKnownPlaces) so published snapshots don't alias the world's
	// mutable map. nil/empty when the actor knows no places. See
	// Actor.KnownPlaces. LLM-77.
	KnownPlaces map[PlaceRef]*KnownPlace

	// RoomAccess mirrors the live Actor's private/staff-room grants at
	// snapshot time so perception build can surface the lodger view ("your
	// room at the inn is paid through <day>") and compute keeper-side room
	// occupancy off the snapshot — both pure over the published Snapshot,
	// never the live Actor. Deep-cloned by snapshotActor (via
	// cloneRoomAccess) so published snapshots don't alias the world's
	// mutable grant map. Keyed by (RoomID, Source) like Actor.RoomAccess.
	RoomAccess map[RoomAccessKey]*RoomAccess

	// OpenUntil mirrors the live Actor's stay-open commitment at snapshot time
	// (ZBBS-WORK-387) so buildDutySteer can suppress the off-shift wind-down cue
	// for a keeper that has committed to staying open late — agreeing with the
	// shiftDutyTarget warrant, which reads the live Actor.OpenUntil. nil when no
	// commitment is held. Carried by CloneActorSnapshot's struct copy like the
	// other *time.Time snapshot fields (the published snapshot is immutable, so
	// no per-clone deep copy is needed). See Actor.OpenUntil.
	OpenUntil *time.Time

	// Inventory mirrors the live Actor's item-kind→quantity map at snapshot
	// time so the read surface (httpapi pc/me) can serve a player's held
	// items without a world-goroutine round trip. InventoryHash above stays
	// the fast-compare digest; this is the full contents. Value-typed map,
	// so snapshotActor copies it with a plain per-entry copy (no pointer
	// cloning needed), the same posture as Needs. Empty/nil for actors with
	// no items.
	Inventory map[ItemKind]int

	// RestockPolicy mirrors the live Actor's RestockPolicy at snapshot time so
	// the "## Restocking" perception section can surface a reseller's low
	// `buy` stock + caps without a world-goroutine round trip. ALIASED, not
	// cloned — RestockPolicy is read-only post-load (same posture as
	// CloneActor; see the TODO there) and perception only reads it. nil for
	// actors with no restock-bearing attribute.
	RestockPolicy *RestockPolicy

	// TickInFlight + TickAttemptID mirror the live Actor fields so PR 3d's
	// harness can do a cheap pre-LLM stale-check by reading the snapshot
	// alone (no world-goroutine round trip). A worker that observes its
	// job.attemptID no longer matching the snapshot's TickAttemptID — or
	// observes TickInFlight false — can short-circuit before spending
	// tokens on a tick the world has already moved past.
	//
	// Both fields are ephemeral on the live Actor (cleared on LoadWorld);
	// they appear here only for the snapshot-time view the harness needs.
	TickInFlight  bool
	TickAttemptID TickAttemptID

	// PendingSummon / SummonRefusal mirror the live Actor's summon cues at
	// snapshot time so perception build (which reads only the snapshot) can
	// surface the target-side "you have been summoned" and summoner-side
	// "your messenger returned" sections (ZBBS-HOME-311). Deep-cloned by
	// snapshotActor. nil for the overwhelming majority of actors with no
	// summon in flight.
	PendingSummon *PendingSummon
	SummonRefusal *SummonRefusal
}

// CloneActorSnapshot returns a deep copy of an ActorSnapshot. Needed by
// any aggregate that captures snapshots and then crosses the published-
// Snapshot or mem-repo serialization boundary (notably Scene's
// ParticipantStateAtOrigin map).
func CloneActorSnapshot(s *ActorSnapshot) *ActorSnapshot {
	if s == nil {
		return nil
	}
	cp := *s
	if s.Needs != nil {
		cp.Needs = make(map[NeedKey]int, len(s.Needs))
		for k, v := range s.Needs {
			cp.Needs[k] = v
		}
	}
	if s.Acquaintances != nil {
		cp.Acquaintances = cloneAcquaintances(s.Acquaintances)
	}
	if s.Relationships != nil {
		cp.Relationships = cloneRelationships(s.Relationships)
	}
	if s.Narrative != nil {
		cp.Narrative = cloneNarrativeState(s.Narrative)
	}
	if s.VisitorState != nil {
		cp.VisitorState = cloneVisitorState(s.VisitorState)
	}
	if s.BusinessownerState != nil {
		cp.BusinessownerState = cloneBusinessownerState(s.BusinessownerState)
	}
	if s.DwellCredits != nil {
		cp.DwellCredits = cloneDwellCredits(s.DwellCredits)
	}
	cp.Observed = s.Observed.Clone()
	if s.KnownPlaces != nil {
		cp.KnownPlaces = cloneKnownPlaces(s.KnownPlaces)
	}
	if s.AttributeSlugs != nil {
		cp.AttributeSlugs = append([]string(nil), s.AttributeSlugs...)
	}
	cp.ScheduleStartMin = copyIntPtr(s.ScheduleStartMin)
	cp.ScheduleEndMin = copyIntPtr(s.ScheduleEndMin)
	cp.SocialStartMin = copyIntPtr(s.SocialStartMin)
	cp.SocialEndMin = copyIntPtr(s.SocialEndMin)
	cp.PendingSummon = clonePendingSummon(s.PendingSummon)
	cp.SummonRefusal = cloneSummonRefusal(s.SummonRefusal)
	return &cp
}

// cloneKnownPlaces deep-copies a KnownPlaces map. The value is a *KnownPlace,
// so the struct is cloned (not aliased) and its Affordances slice is copied —
// mutating the snapshot copy's affordances must not touch the live actor.
// Returns nil for a nil source; skips nil entries. LLM-77.
func cloneKnownPlaces(src map[PlaceRef]*KnownPlace) map[PlaceRef]*KnownPlace {
	if src == nil {
		return nil
	}
	dst := make(map[PlaceRef]*KnownPlace, len(src))
	for k, v := range src {
		if v == nil {
			continue
		}
		vc := *v
		if v.Affordances != nil {
			vc.Affordances = append([]string(nil), v.Affordances...)
		}
		dst[k] = &vc
	}
	return dst
}

// cloneDwellCredits deep-copies a DwellCredits map. RemainingTicks is a
// pointer so it must be cloned separately; the other fields are value
// types and a per-entry struct copy is enough.
func cloneDwellCredits(src map[DwellCreditKey]*DwellCredit) map[DwellCreditKey]*DwellCredit {
	if src == nil {
		return nil
	}
	dst := make(map[DwellCreditKey]*DwellCredit, len(src))
	for k, v := range src {
		if v == nil {
			continue
		}
		vc := *v
		if v.RemainingTicks != nil {
			rt := *v.RemainingTicks
			vc.RemainingTicks = &rt
		}
		dst[k] = &vc
	}
	return dst
}

// cloneRoomAccess deep-copies a RoomAccess map. ExpiresAt is a pointer so
// it must be cloned separately; the other fields are value types and a
// per-entry struct copy is enough. Shared by CloneActor (the repo
// serialization boundary) and snapshotActor (the published read view) so
// neither aliases the world's mutable grant map.
func cloneRoomAccess(src map[RoomAccessKey]*RoomAccess) map[RoomAccessKey]*RoomAccess {
	if src == nil {
		return nil
	}
	dst := make(map[RoomAccessKey]*RoomAccess, len(src))
	for k, v := range src {
		if v == nil {
			continue
		}
		vc := *v
		if v.ExpiresAt != nil {
			t := *v.ExpiresAt
			vc.ExpiresAt = &t
		}
		dst[k] = &vc
	}
	return dst
}
