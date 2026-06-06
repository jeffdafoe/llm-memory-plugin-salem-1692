package perception

import (
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// BaselineStatus reports whether perception could establish a diff baseline
// for the subject actor against the primary scene's origin snapshot.
//
// The contract — see Build — is "unknown, never no-change": any Missing*
// status means perception MUST NOT claim "nothing changed since the scene
// started" and loop detection is inconclusive (not negative). A stuck-loop
// signal requires BaselinePresent; absence of evidence is not evidence of
// a loop.
type BaselineStatus int

const (
	// BaselineMissingNoScene — no scene resolved at all. Neither the
	// consumed warrants nor the actor's active huddle pointed at a scene
	// present in the snapshot. There is nothing to diff against.
	//
	// This is deliberately the zero value: a zero-value Payload is then
	// honestly degraded (no baseline) rather than falsely "present", so
	// Render(Payload{}) and any other unset path stays on the safe side of
	// the "unknown, never no-change" contract.
	BaselineMissingNoScene BaselineStatus = iota

	// BaselinePresent — the primary scene resolved and captured an origin
	// snapshot for the subject actor. SceneView.Diff is populated.
	BaselinePresent

	// BaselineMissingNoOriginSnapshot — a scene resolved but it captured no
	// participant baseline at all (ParticipantStateAtOrigin nil/empty —
	// e.g. an unbounded atmosphere-refresh scene). No actor has a baseline
	// here, so the absence carries no "joined after" signal.
	BaselineMissingNoOriginSnapshot

	// BaselineMissingJoinedAfterOrigin — a scene resolved and captured a
	// baseline for *other* participants, but not for the subject actor —
	// so the actor joined after the scene was minted. The diff baseline
	// would be meaningless; continuity claims about "since the scene
	// started" are weakened or omitted.
	BaselineMissingJoinedAfterOrigin
)

// String renders the status as a stable lowercase label — used in
// SelectionReason text, debug output, and telemetry Detail.
func (s BaselineStatus) String() string {
	switch s {
	case BaselinePresent:
		return "present"
	case BaselineMissingNoScene:
		return "missing_no_scene"
	case BaselineMissingNoOriginSnapshot:
		return "missing_no_origin_snapshot"
	case BaselineMissingJoinedAfterOrigin:
		return "missing_joined_after_origin"
	default:
		return "unknown"
	}
}

// Payload is the immutable result of Build — everything Render needs to
// produce a prompt, derived purely from a published *sim.Snapshot. It is
// "immutable" by convention (built once, never mutated), the same way
// sim.Snapshot is.
type Payload struct {
	ActorID sim.ActorID

	// Actor is the subject actor's own current decision-relevant state.
	Actor ActorView

	// Surroundings is where the actor is right now — structure, huddle,
	// and co-present actors.
	Surroundings SurroundingsView

	// TurnState is the subject's conversation turn-state for this tick
	// (ZBBS-WORK-370): which present huddle peers it is awaiting a reply from,
	// and which are awaiting a reply from it. Drives the turn-line and the
	// act-now coda swap that stop an NPC re-pitching a peer who hasn't answered.
	// Zero value (both slices empty) when the actor has no pending turn.
	TurnState TurnStateView

	// Anchors are the actor's OWN home and work structures, always surfaced
	// (when set) as standing move_to targets with their structure_ids — so a
	// wandering NPC can always head home or to work, not only when a need-cue
	// happens to point somewhere. nil for an actor with neither anchor (e.g. a
	// PC, or an unanchored NPC). ZBBS-HOME-349.
	Anchors *AnchorsView

	// DutySteer is the standing return-to-post cue (ZBBS-HOME-352): non-nil when
	// the subject is an agent NPC that, by its schedule (or the dawn/dusk
	// fallback) and current position, is on-shift away from work or off-shift
	// away from home. The always-present, level-triggered perception voice for
	// shift duty — the engine's edge-stamped ShiftDutyWarrant drives the wake
	// tick but its rendered line is filtered, so this is the single voice. NOT
	// need-suppressed (the model weighs duty against need). nil when at-post, off
	// scope, or the clock/anchors are unknown. See buildDutySteer.
	DutySteer *DutySteerView

	// Warrants is every consumed warrant, ordered by SourceEventID
	// ascending — PR 3a's monotonic EventID is the authoritative causal
	// order. Zero-lineage warrants (SourceEventID == 0, legacy/non-event-
	// sourced) sort first; ties hold input order (stable). This is the
	// canonical ordered list; the per-scene groupings below reference the
	// same WarrantMeta values.
	Warrants []sim.WarrantMeta

	// Primary is the scene the baseline diff is computed against — the
	// scene of the warrant with the maximum SourceEventID, or (when no
	// warrant carries a scene) the actor's active-huddle scene. nil when
	// no scene resolved (Baseline == BaselineMissingNoScene).
	Primary *SceneView

	// Secondary holds warrants that reference a scene *other* than the
	// primary one. They render as independent source signals with their
	// own SceneID/HuddleID — the primary scene's baseline is deliberately
	// NOT applied to them. Ordered by SceneID for determinism.
	Secondary []SceneSignal

	// Baseline reports whether Primary.Diff could be established.
	Baseline BaselineStatus

	// MultiSceneWarrantCount is the number of distinct scenes referenced
	// by the consumed warrant batch (1 for the common single-scene tick,
	// 0 when no warrant carries a scene). Surfaced for the handlers-layer
	// telemetry field of the same name.
	MultiSceneWarrantCount int

	// NarrativeState is the actor's engine-side identity continuity —
	// seed_text identity frame + evolving_summary the consolidator
	// rewrites. Non-nil ONLY for KindNPCShared actors that have a
	// populated NarrativeState in the snapshot. Stateful-VA actors get
	// this content from their own VA's <Self> system prompt block via
	// memory-api; injecting engine-side would duplicate or conflict.
	NarrativeState *NarrativeStateView

	// Relationships are per-co-huddle-peer relationship views for the
	// subject actor — summary + recent salient facts for each peer in
	// the actor's current huddle. Populated ONLY for KindNPCShared
	// actors and only for peers the actor has a Relationship row for;
	// empty otherwise. Stateful-VA actors don't get this for the same
	// reason as NarrativeState (their own VA's per-peer context notes
	// cover this — see the symmetric stateful-VA gap at
	// shared/tasks/pending/salem-stateful-va-missing-peer-context).
	//
	// Ordering: sorted by PeerID for determinism.
	Relationships []RelationshipPeerView

	// Businessowner reports whether the subject actor runs a business
	// (Actor.BusinessownerState != nil — the existing keeper predicate).
	// Drives the engine-side vendor operating-block (renderVendorOperating):
	// the trade conduct rules that used to live in salem-vendor's
	// startup_instructions, moved engine-side so the whole decision prompt is
	// code-owned and the rules sit at the decision point. ZBBS-WORK-374.
	Businessowner bool

	// PendingDeliveriesFromMe lists open Orders where the subject is
	// the seller — items they owe to a buyer/consumers from a previously
	// accepted pay-with-item offer that hasn't been delivered yet.
	// Populated for any KindNPCShared/Stateful seller; empty for PCs.
	// Renders as "## Orders to deliver:" surfacing item, qty, buyer/
	// consumer names, age, and time-remaining.
	//
	// Ordering: sorted by Order.ID (uint64 ascending) for determinism.
	// Phase 3 PR S6 — perception is the only awareness mechanism for
	// pending delivery; no new warrant kind (S4's PayResolved warrant
	// handles the initial post-accept tick cue).
	PendingDeliveriesFromMe []OrderView

	// PendingDeliveriesToMe lists open Orders where the subject is
	// the buyer OR a member of ConsumerIDs — items they're waiting on
	// the seller to hand over. Populated for any NPC subject; PCs get
	// this via UI.
	//
	// Renders as "## Orders you're waiting on:" surfacing item, qty,
	// seller name, age, time-remaining. Same OrderView struct as
	// PendingDeliveriesFromMe; the renderer picks fields per subject
	// role.
	//
	// Ordering: sorted by Order.ID for determinism.
	PendingDeliveriesToMe []OrderView

	// LocalDateUTC is midnight UTC of the village's current calendar date,
	// copied from Snapshot.LocalDateUTC. Render's order-book split
	// (renderPendingDeliveries*) compares it against each OrderView.ReadyBy so
	// the ready/future/overdue classification uses the same world-TZ date that
	// ReadyBy was built from — not the host UTC day, which drifts by the UTC
	// offset near the boundary. Zero when the snapshot has no clock (hand-built
	// payloads); the renderer falls back to the host UTC day then. ZBBS-HOME-403.
	LocalDateUTC time.Time

	// RecoveryOptions surfaces how a tired-or-homeless actor could rest —
	// free tiredness-bearing objects (shade trees) and inns to rent a room.
	// nil when the actor isn't tired/homeless or no options exist (the
	// homeless arm fires every tick — the lodging bootstrap cue). ZBBS-HOME-297.
	RecoveryOptions *RecoveryOptionsView

	// Satiation surfaces how a hungry-or-thirsty actor could eat/drink — the
	// items the actor already carries (consume-first) and nearby vendors selling
	// a satisfier. nil when neither hunger nor thirst is at its red threshold, or
	// no satisfier exists anywhere. The consume-first own-stock line is the core:
	// it connects the pressing need to the consume tool. ZBBS-HOME-304.
	Satiation *SatiationView

	// Restocking surfaces how a reseller could replenish its low `buy` stock —
	// each item below the reorder threshold (current/cap) and the suppliers
	// selling it (workplace + structure_id + per-buyer price hint). nil when the
	// actor holds no `buy` entries below threshold, or restock is disabled
	// (RestockReorderPct == 0). The reseller's LLM decides whether/what/how-much
	// and acts via move_to + pay_with_item. ZBBS-WORK-322.
	Restocking *RestockingView

	// Lodging surfaces the subject's own active lodging — the inn their room
	// is at and when the grant expires — so a lodger NPC can renew before it
	// lapses. nil when the actor holds no active ledger RoomAccess (not a
	// lodger). Sourced from the soonest-expiring active ledger grant.
	// ZBBS-HOME-296 PR2 (lodger view). The affordability cue lands in a
	// follow-on slice with the rent-rate setting.
	Lodging *LodgingView

	// KeeperLodging surfaces room availability to an actor who keeps a
	// lodging structure (works at a structure with private bedrooms), so the
	// keeper LLM can answer "do you have a room?" from real occupancy. nil
	// when the subject doesn't work at a lodging structure. ZBBS-HOME-296
	// PR2. The salem-vendor identity/persona preface lands in a follow-on
	// (it needs a typed vendor_persona projection + the keeper data seed).
	KeeperLodging *KeeperLodgingView

	// SummonsForYou surfaces a pending summons delivered to the subject by a
	// messenger — "<summoner> asks you to come to <place>" — driving them to
	// move_to. nil when the actor has no pending summons. Fades after the
	// actor next acts (the cue is cleared on the reactor tick). ZBBS-HOME-311.
	SummonsForYou *SummonsForYouView

	// SummonRefusal surfaces, to a summoner whose messenger returned unable
	// to locate the target, "<target> could not be found." nil otherwise.
	// Fades after the actor next acts. ZBBS-HOME-311.
	SummonRefusal *SummonRefusalView

	// SelectionReason is a human-readable explanation of how Primary was
	// chosen (or why it wasn't) — debug/test output only, never prompt
	// content.
	SelectionReason string

	// WarrantActorNames maps every actor referenced by a warrant (or pay
	// offer) in this payload to its acquaintance-gated display label —
	// DisplayName when the subject knows them, else "the <role>", else "a
	// stranger". The same name-vs-descriptor gating SurroundingsView's
	// HuddleMembers use. Render consults this so a "## What just happened"
	// line reads "Goodwife Ellis arrived nearby" instead of leaking the raw
	// actor UUID (ZBBS-HOME-339). The subject's own ID is deliberately
	// absent — Render resolves self to "you". Empty when no warrant
	// references another actor.
	WarrantActorNames map[sim.ActorID]string

	// WarrantPlaceNames maps a destination id (structure or village object)
	// named by an ArrivalWarrantReason in this payload to its display name, so
	// Render can say "You arrived at the General Store" instead of the vacuous
	// "arrived nearby" (ZBBS-WORK-358). Keyed by the raw id string because
	// structure and object ids share one space (the shared-identity bridge).
	// Empty when no arrival warrant names a place.
	WarrantPlaceNames map[string]string
}

// OrderView is the perception-side projection of one sim.Order.
// Same struct shape regardless of whether the subject is the seller
// (PendingDeliveriesFromMe) or a buyer/consumer (PendingDeliveriesToMe);
// the renderer interprets fields differently based on which slice
// the view appears in.
//
// Names are resolved at build time from the snapshot's ActorSnapshot
// map (DisplayName falls back to ActorID when the actor is missing
// from the snapshot — defensive).
type OrderView struct {
	ID            sim.OrderID
	Item          sim.ItemKind
	Qty           int
	BuyerName     string
	SellerName    string
	ConsumerNames []string // empty when ConsumerIDs == [BuyerID] (implicit buyer-is-consumer)
	// AbsentRecipientNames lists the consumers NOT co-present with the seller —
	// the recipients DeliverOrder's gate-6 co-presence check would reject a
	// handover to. Populated only for the seller-side PendingDeliveriesFromMe
	// bucket; empty => every recipient is here and the order is deliverable now.
	// ZBBS-WORK-373 (boot-collapse Finding 6 bundle).
	AbsentRecipientNames []string
	CreatedAt            time.Time
	ExpiresAt            time.Time
	// ReadyBy is the order's booked date (lodging check-in date for an
	// advance booking; the creation date for a same-day order). Midnight UTC
	// of a calendar date. Render uses it to split the seller view into
	// ready-to-hand-over-now vs upcoming reservations, and the buyer view into
	// waiting-on vs overdue. ZBBS-HOME-403.
	ReadyBy time.Time
}

// NarrativeStateView is the kind-aware "Who you are:" content. Slim by
// design — Render combines SeedText and EvolvingSummary into one
// section.
type NarrativeStateView struct {
	SeedText        string
	EvolvingSummary string
}

// RelationshipPeerView is the per-peer entry in the "What you remember
// of those here:" section. RecentFacts holds the most-recent N facts
// (most-recent-first) — Build slices them from the actor's
// Relationships[peerID].SalientFacts, which is stored oldest-first.
type RelationshipPeerView struct {
	PeerID      sim.ActorID
	PeerName    string
	SummaryText string
	RecentFacts []sim.SalientFact
}

// ActorView is the subject actor's own current state, lifted from the
// snapshot's ActorSnapshot. Slim by design — content fills in incrementally
// (PR 3c ships the mechanism, not the final prompt surface).
type ActorView struct {
	State             sim.ActorState
	InsideStructureID sim.StructureID
	Position          sim.Position
	CurrentHuddleID   sim.HuddleID
	Coins             int
	Needs             map[sim.NeedKey]int

	// NeedThresholds is the red-tier boundary per need, copied from the
	// snapshot so Render can classify each need value into its felt tier
	// (peckish/hungry/starving) without re-reading world state. ZBBS-HOME-339
	// — replaces the raw "needs: hunger=24" dump with felt language.
	NeedThresholds sim.NeedThresholds

	// ActiveDwellCredits is the actor's in-progress dwell credits at
	// snapshot time — meals being eaten, rests being taken. Renders as
	// "you are currently eating stew at the tavern, ~14 minutes
	// remaining" so the LLM can see the meal as a coherent state across
	// the per-minute event stream (DwellTickApplied warrants). The
	// structured surface is the load-bearing piece that keeps NPCs
	// parked: even if the per-tick narration warrant hasn't landed yet
	// for this tick, the structured field is always present while the
	// credit lives, so perception render can keep the cue in front of
	// the LLM on every tick.
	//
	// Sort order is deterministic: by (Source, Attribute, ObjectID) so
	// golden tests + admin replay see stable ordering.
	ActiveDwellCredits []DwellCreditView

	// InFlightMove is the subject's current walk, nil when not moving.
	// Rendered as "currently: walking to <label>" so a reactor tick firing
	// mid-walk reminds the LLM it already has a destination — the movement
	// analogue of ActiveDwellCredits. ZBBS-HOME-336.
	InFlightMove *InFlightMoveView

	// Inventory is the actor's carried goods — the STANDING "what you're
	// carrying" readout (ZBBS-HOME-361), restored after the v2 rewrite dropped
	// v1's inventory line and left NPCs blind to their own pockets (a hungry
	// NPC holding cheese walked to a shop because nothing in perception told it
	// what it held). Deliberately NEUTRAL and UNGATED: it states possession, not
	// need-resolution. The "consume to eat / drink" push stays in the
	// need-gated satiation own-stock line — so a not-yet-pressing actor sees it
	// has cheese without being nudged to eat, and a vendor (e.g. an innkeeper)
	// sees its sellable stock regardless of its own needs. Resolved + sorted at
	// build time; empty (render omits the line) when carrying nothing.
	Inventory []InventoryItem
}

// InventoryItem is one carried item kind in the standing inventory readout —
// its display label + quantity. kind is the unexported sort tie-break so two
// kinds sharing a label order deterministically (Inventory is a map).
// ZBBS-HOME-361.
type InventoryItem struct {
	Label string
	Qty   int
	kind  sim.ItemKind
}

// InFlightMoveView is the perception-side projection of the subject's
// in-flight MoveIntent. DestinationLabel is resolved at build time (structure
// / object DisplayName, or a tile coordinate for a bare position move); empty
// when the destination can't be resolved. Kind drives the render phrasing
// ("walking to enter X" for a structure-enter vs "walking to X" for a visit).
// ZBBS-HOME-336.
type InFlightMoveView struct {
	Kind             sim.MoveDestinationKind
	DestinationLabel string
}

// DwellCreditView is the perception-side projection of one
// sim.DwellCredit. Carries the structure label resolved at build time
// (so render doesn't re-fetch from world state), plus the raw
// countdown fields so Hub clients can derive "next-tick at" and
// "remaining time" without tracking prior state.
//
// Kind is empty for source=object credits (resting under a tree —
// no item involved); render falls back to a generic phrasing in that
// case ("you are resting under the willow" rather than "you are
// currently eating X").
type DwellCreditView struct {
	ObjectID       sim.VillageObjectID
	StructureLabel string // resolved from snap.Structures or snap.VillageObjects; "" when neither resolves
	Source         sim.DwellCreditSource
	Kind           sim.ItemKind
	Attribute      sim.NeedKey
	RemainingTicks *int // nil for source=object; >0 for source=item
	PeriodMinutes  int
	DwellDelta     int // negative — applied per period
	LastCreditedAt time.Time
}

// SurroundingsView is the actor's immediate context — the structure it is
// in and the huddle it belongs to, with co-present actors named or
// rendered as descriptors based on acquaintance.
type SurroundingsView struct {
	InsideStructureID sim.StructureID

	// StructureName is the structure's DisplayName, or empty when the
	// actor is outdoors or the structure is absent from the snapshot.
	StructureName string

	// NearbyStructureName is the DisplayName of the structure the actor is
	// standing at the loiter slot of while OUTDOORS (within
	// sim.LoiterAttributionTiles) — e.g. a shopkeeper at their own stall, a
	// customer outside a shop. Empty when inside a structure or not at any
	// structure's loiter slot. Lets Render say "outdoors by the General
	// Store" instead of dumping raw "(94, 126)" coordinates (ZBBS-HOME-339).
	NearbyStructureName string

	// HuddleID is the actor's current huddle, empty when not huddled.
	HuddleID sim.HuddleID

	// HuddleMembers are the *other* members of the actor's current huddle
	// (the subject actor is excluded), sorted by ID for determinism.
	// Each carries acquaintance info so Render can pick name vs.
	// descriptor without re-reading the snapshot.
	HuddleMembers []HuddleMember

	// Atmosphere is the village-wide ambient line authored by the atmosphere
	// cascade (Environment.Atmosphere — LLM-phrased by the cheap salem-generic
	// VA ~every 4h on phase transitions, NOT the v1 chronicler). Surfaced into
	// every NPC's perception so the ambient mood colors deliberation; it reuses
	// the already-generated string, so it adds no LLM call (ZBBS-WORK-327, the
	// v1 atmosphere perception line restored). Empty until the cascade first
	// fires (restart-lossy by design) → the render omits the line.
	Atmosphere string

	// LocalMinuteOfDay is the village wall-clock minute (0–1439), copied from
	// Snapshot.LocalMinuteOfDay, or nil when the clock isn't established.
	// renderSurroundings turns it into a time-of-day prose line (timeOfDayProse).
	// v2 rendered no time at all, so an NPC couldn't tell its working hours from
	// the dead of night — the missing context HOME-352 builds on. ZBBS-HOME-351.
	LocalMinuteOfDay *int

	// GatherableItem / GatherableSource carry the harvest affordance cue
	// (ZBBS-WORK-328): when the actor is loitering at a gatherable source (a
	// well, a berry bush), GatherableItem is the item the source yields and
	// GatherableSource is its display name. Both empty when not at a source.
	// Render emits a "you can gather X here" line from these; gateTools reads
	// the SAME fields to advertise the `gather` tool — so the cue and the tool
	// can't drift (the pay-offer-warrant pattern). Computed by an asset-free
	// snapshot scan (findGatherableCue) — a permissive advertising heuristic;
	// the sim.Gather Command is the authoritative resolver at gather time.
	GatherableItem   sim.ItemKind
	GatherableSource string
}

// TurnStateView is the subject actor's conversation turn-state, derived in
// Build from the directed awaiting-reply edges (ActorSnapshot.AwaitingReplyFrom)
// among its present huddle peers (ZBBS-WORK-370). Names are the acquaintance-
// gated labels (descriptorLabel), resolved at build time like OrderView's, so a
// line never leaks a name a shared-VA NPC shouldn't know.
//
// Only LIVE edges are surfaced: an edge older than the addressee-kind window
// (Snapshot.{PC,NPC}AwaitReplyWindow, measured against PublishedAt) is dropped,
// matching the sim.Speak backstop's expiry so the nudge and the gate agree on
// when a turn has lapsed.
type TurnStateView struct {
	// AwaitingReplyFrom names the present peers this actor has addressed and is
	// still awaiting a live reply from. Non-empty → render "you spoke to them,
	// wait for their reply, don't re-address them" AND swap the act-now coda for
	// a wait-framing (the cadence fix — stops the re-pitch loop).
	AwaitingReplyFrom []string

	// OwedReplyTo names the present peers awaiting a live reply FROM this actor —
	// it is this actor's turn to answer them. Rendered as a "they are waiting for
	// your reply" nudge so an addressed NPC takes its turn.
	OwedReplyTo []string
}

// AwaitingReply reports whether the subject is awaiting at least one live reply
// — the condition that swaps the act-now coda for a wait-framing.
func (t TurnStateView) AwaitingReply() bool { return len(t.AwaitingReplyFrom) > 0 }

// HuddleMember is one co-huddle peer's identity slice for the
// SurroundingsView. Render emits DisplayName when Acquainted is true,
// otherwise falls back to Role ("the blacksmith") or a generic
// stranger label. Mirrors v1's coLocatedHuddleMembers name-vs-
// descriptor gating.
type HuddleMember struct {
	ID          sim.ActorID
	DisplayName string
	Role        string
	Acquainted  bool
}

// AnchorsView carries the actor's own home and work structures as standing
// move_to targets. Label is the structure's DisplayName (may be empty — render
// falls back to a generic phrase but always keeps the id); the id is the
// load-bearing field the model passes to move_to. SamePlace is true when home
// and work resolve to the same structure (a keeper who lives at their own
// establishment), so render says it once rather than naming the same place
// twice. A zero-value field (empty id) means that anchor isn't set.
type AnchorsView struct {
	WorkLabel string
	WorkID    sim.StructureID
	HomeLabel string
	HomeID    sim.StructureID
	SamePlace bool
}

// DutySteerView is the standing return-to-post cue (ZBBS-HOME-352). ToWork
// distinguishes the two directions; TargetID/TargetLabel name the destination
// (reused from the actor's AnchorsView, so the structure_id the model needs is
// already in the adjacent "## Where you belong" section). See buildDutySteer.
type DutySteerView struct {
	ToWork      bool // true = on-shift, head to work; false = off-shift, head home
	TargetID    sim.StructureID
	TargetLabel string // resolved DisplayName; may be empty (render falls back)
}

// SceneView describes the primary scene and, when a baseline could be
// established, the actor's diff against that scene's origin snapshot.
type SceneView struct {
	SceneID    sim.SceneID
	OriginKind string
	OriginAt   time.Time

	// Warrants are the consumed warrants that reference this scene,
	// ordered by SourceEventID ascending. May be empty when the primary
	// scene was resolved from the actor's active huddle rather than from a
	// scene-bearing warrant.
	Warrants []sim.WarrantMeta

	// Diff is the actor's change since the scene's origin snapshot. Set
	// iff the enclosing Payload's Baseline == BaselinePresent; nil
	// otherwise (the Missing* statuses all mean "no diff").
	Diff *Diff
}

// SceneSignal is a secondary scene referenced by the warrant batch — a
// scene other than the primary. It carries no baseline diff by design:
// the primary scene's origin snapshot says nothing about a different
// scene, and reverse-resolving a baseline per secondary scene would
// multiply the cost and the failure modes for marginal value.
type SceneSignal struct {
	SceneID  sim.SceneID
	HuddleID sim.HuddleID
	Warrants []sim.WarrantMeta
}

// Diff is the subject actor's change between a scene's origin snapshot and
// its current snapshot. It is the loop-detection seam: AnyChange == false
// across several consecutive ticks is the "this actor is stuck" signal.
// Every field is only meaningful when the enclosing Baseline is
// BaselinePresent.
type Diff struct {
	StateChanged     bool
	PositionChanged  bool
	StructureChanged bool
	HuddleChanged    bool
	CoinsChanged     bool
	InventoryChanged bool
	NeedsChanged     bool

	// AnyChange is the OR of every field above — the single value loop
	// detection reads.
	AnyChange bool
}
