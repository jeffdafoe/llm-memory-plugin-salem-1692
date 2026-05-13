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
	InsideRoomID      RoomID // 0 when not in a room
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

	// WarrantedSince marks the actor as having actionable state-change
	// since their last tick. Stamped by mutation commands when the
	// change is something the actor would want to re-think about (peer
	// joined/left their huddle, need crossed threshold, speech directed
	// at them, inventory delta). The reactor scheduler reads this to
	// gate scheduled ticks: a tick fires only when warranted OR when
	// the timer floor (idle backstop) reaches its threshold. Cleared by
	// the tick handler on consumption.
	//
	// Non-nil = warranted; nil = no actionable change pending. The
	// timestamp captures when the warrant was first stamped (for
	// oldest-first scheduling); subsequent stamps while already
	// warranted preserve the original timestamp.
	WarrantedSince *time.Time

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

	// RoomAccess — this actor's grants to enter private/staff rooms.
	// Keyed by (RoomID, Source). Stamped by AssignBedroomForLodger
	// (source=ledger) and flipped to Active=false by ExpireRoomAccess
	// when ExpiresAt passes.
	RoomAccess map[RoomAccessKey]*RoomAccess

	// Free-form behavior specs (typed lazily per subsystem during port).
	Attributes map[string][]byte
}

// CloneActor returns a deep copy of an Actor suitable for the mem-repo
// serialization boundary. Mutated containers (Needs, Inventory,
// DwellCredits, RoomAccess, ProduceState, Acquaintances) and pointer
// fields commands rebind (BreakUntil, SleepingUntil, LastTickedAt,
// NextSelfTickAt) are cloned. Attributes is deep-cloned including each
// []byte payload. The two RingBuffers are cloned via RingBuffer.Clone.
//
// Aliased today (NOT cloned) because no current command mutates them:
//   - VisitorState, Narrative, LastSnapshot — placeholder/empty structs
//
// TODO: clone Relationships values and RestockPolicy when a command
// starts mutating them. Both are pointer-bearing domain state (the
// Relationship struct is a placeholder today but will land with a
// per-actor relationship view; RestockPolicy is read-only post-load but
// future admin edits could mutate it via a command). Aliasing them now
// is correct but fragile against future command authors.
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
	if a.LastTickedAt != nil {
		t := *a.LastTickedAt
		cp.LastTickedAt = &t
	}
	if a.NextSelfTickAt != nil {
		t := *a.NextSelfTickAt
		cp.NextSelfTickAt = &t
	}
	if a.WarrantedSince != nil {
		t := *a.WarrantedSince
		cp.WarrantedSince = &t
	}
	if a.Acquaintances != nil {
		cp.Acquaintances = make(map[string]Acquaintance, len(a.Acquaintances))
		for k, v := range a.Acquaintances {
			cp.Acquaintances[k] = v
		}
	}
	if a.Relationships != nil {
		cp.Relationships = make(map[ActorID]*Relationship, len(a.Relationships))
		for k, v := range a.Relationships {
			cp.Relationships[k] = v // placeholder type; alias safe
		}
	}
	if a.RecentActions != nil {
		cp.RecentActions = a.RecentActions.Clone()
	}
	if a.RecentStateTrans != nil {
		cp.RecentStateTrans = a.RecentStateTrans.Clone()
	}
	if a.DwellCredits != nil {
		cp.DwellCredits = make(map[DwellCreditKey]*DwellCredit, len(a.DwellCredits))
		for k, v := range a.DwellCredits {
			if v == nil {
				continue
			}
			vc := *v
			if v.RemainingTicks != nil {
				rt := *v.RemainingTicks
				vc.RemainingTicks = &rt
			}
			cp.DwellCredits[k] = &vc
		}
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
	if a.RoomAccess != nil {
		cp.RoomAccess = make(map[RoomAccessKey]*RoomAccess, len(a.RoomAccess))
		for k, v := range a.RoomAccess {
			if v == nil {
				continue
			}
			vc := *v
			if v.ExpiresAt != nil {
				t := *v.ExpiresAt
				vc.ExpiresAt = &t
			}
			cp.RoomAccess[k] = &vc
		}
	}
	if a.Attributes != nil {
		cp.Attributes = make(map[string][]byte, len(a.Attributes))
		for k, v := range a.Attributes {
			cp.Attributes[k] = append([]byte(nil), v...)
		}
	}
	return &cp
}

// ActorSnapshot is the slim immutable view of an actor's decision-relevant
// state at the moment of the last tick. Consumed by:
//   - Snapshot publishing (admin reads, perception diff against previous)
//   - Checkpoint writes (serialized to actor_snapshot row)
//   - Scene origin capture (Scene.ParticipantStateAtOrigin) for diff-against-
//     scene-start in perception build
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
	return &cp
}
