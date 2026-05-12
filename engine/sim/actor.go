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

// CreditKey identifies a dwell-credit channel keyed by item-effect category.
type CreditKey string

// DwellCredit accumulates "I've been here X minutes" toward need recovery,
// per item-effect category (ZBBS-172).
//
// TODO: port concrete fields from engine/dwell.go when this subsystem is
// reached in the cutover sequence.
type DwellCredit struct{}

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

	DwellCredits map[CreditKey]*DwellCredit

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
