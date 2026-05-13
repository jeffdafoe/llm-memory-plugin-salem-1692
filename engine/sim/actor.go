package sim

import "time"

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

// StateTransition records a single move from one macro-state to another.
// Stored in the actor's RecentStateTrans ring buffer for loop detection
// ("Walking → Idle → Walking 5 times in 18 min — you're stuck") and admin-
// side debuggability.
type StateTransition struct {
	At     time.Time
	From   ActorState
	To     ActorState
	Reason string // "arrived at structure", "joined huddle", "started walk_to", ...
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
type DwellCredit struct {
	ObjectID           VillageObjectID
	Attribute          NeedKey
	Source             DwellCreditSource
	LastCreditedAt     time.Time
	RemainingTicks     *int // nil for source=object; >0 for source=item
	DwellDelta         int  // negative — applied per period
	DwellPeriodMinutes int
}

// Acquaintance is a per-actor view of someone they know by display name.
//
// TODO: port from engine/actor_narrative.go acquaintance handling.
type Acquaintance struct{}

// Relationship is a per-actor relationship view keyed by other ActorID.
//
// TODO: port from engine/actor_relationship handling.
type Relationship struct{}

// NarrativeState is the engine-side continuity layer for shared-VA NPCs
// (Hannah, Moses, Elizabeth, etc.). Nil for stateful-VA actors that have
// their own memory on llm-memory-api.
//
// TODO: port from engine/actor_narrative.go.
type NarrativeState struct{}

// VisitorState is the per-visitor archetype state (wandering, leaving,
// etc.). Nil for non-visitor actors.
//
// TODO: port from engine/visitor.go.
type VisitorState struct{}

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
	// visitor archetype state.
	LLMAgent      string
	LoginUsername string
	VisitorState  *VisitorState

	// Spatial — current location.
	InsideStructureID StructureID
	CurrentX          int
	CurrentY          int
	CurrentHuddleID   HuddleID

	// Anchors — home and work bindings (empty for actors without them).
	HomeStructureID StructureID
	WorkStructureID StructureID

	// Schedule (minute-of-day; -1 if unset — falls back to world dawn/dusk).
	ScheduleStartMin int
	ScheduleEndMin   int

	// Mutable state.
	Needs     map[NeedKey]int
	Inventory map[ItemKind]int
	Coins     int

	// Activity windows.
	BreakUntil    *time.Time
	SleepingUntil *time.Time

	// Tick scheduling.
	LastTickedAt       *time.Time
	NextSelfTickAt     *time.Time
	NextSelfTickReason string

	// Relationships (per-actor views, not a global graph).
	Acquaintances map[string]Acquaintance
	Relationships map[ActorID]*Relationship
	Narrative     *NarrativeState

	// Behavior history — load-bearing for diff-against-previous and loop
	// detection.
	RecentActions *RingBuffer[Action]
	LastSnapshot  *ActorSnapshot

	// Macro-state — soft transitions, engine sets on observation (no strict
	// FSM validation). RecentStateTrans is in-memory only (not checkpointed);
	// State itself is checkpointed so restart resumes in the same state.
	State            ActorState
	StateEnteredAt   time.Time
	RecentStateTrans *RingBuffer[StateTransition]

	DwellCredits map[DwellCreditKey]*DwellCredit

	// RestockPolicy carries this actor's produce/buy entries, unioned
	// across their role attributes (tavernkeeper + worker, etc.). Read
	// from actor_attribute.params.restock in legacy; nil for actors
	// without a restock-bearing attribute.
	RestockPolicy *RestockPolicy

	// ProduceState carries the per-item production anchor — used by
	// produce_tick to compute units owed since the last execution.
	// One entry per item the actor produces; populated lazily on first
	// observation.
	ProduceState map[ItemKind]*ProduceState

	// Free-form behavior specs (typed lazily per subsystem during port).
	Attributes map[string][]byte
}

// ActorSnapshot is the slim immutable view of an actor's decision-relevant
// state at the moment of the last tick. Consumed by:
//   - Snapshot publishing (admin reads, perception diff against previous)
//   - Checkpoint writes (serialized to actor_snapshot row)
type ActorSnapshot struct {
	AtTick            uint64
	State             ActorState // checkpointed; restart resumes in same state
	InsideStructureID StructureID
	CurrentX          int
	CurrentY          int
	CurrentHuddleID   HuddleID
	Needs             map[NeedKey]int
	InventoryHash     uint64 // fast-compare; computed at snapshot time
	Coins             int
}
