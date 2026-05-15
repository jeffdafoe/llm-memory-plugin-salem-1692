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
// SalientFacts is bounded by consolidation, which periodically rewrites
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
	// Both survive checkpoint reload — MoveDestination is a closed tagged
	// struct (unlike the interface-typed reactor payloads), so MoveIntent
	// serializes cleanly, and the counter must persist to stay monotonic
	// across restarts.
	MoveIntent         *MoveIntent
	MoveAttemptCounter MovementAttemptID

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
// DwellCredits, RoomAccess, ProduceState, Acquaintances, Relationships)
// and pointer fields commands rebind (BreakUntil, SleepingUntil,
// LastTickedAt, NextSelfTickAt, Narrative) are cloned. Attributes is
// deep-cloned including each []byte payload. The two RingBuffers are
// cloned via RingBuffer.Clone. MoveIntent is deep-cloned via
// cloneMoveIntent (its MoveDestination carries StructureID / Position
// pointer fields that would otherwise alias across the boundary).
//
// Aliased today (NOT cloned) because no current command mutates them:
//   - VisitorState, LastSnapshot — placeholder/empty structs
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
	if a.Acquaintances != nil {
		cp.Acquaintances = cloneAcquaintances(a.Acquaintances)
	}
	if a.Relationships != nil {
		cp.Relationships = cloneRelationships(a.Relationships)
	}
	if a.Narrative != nil {
		cp.Narrative = cloneNarrativeState(a.Narrative)
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
	if a.MoveIntent != nil {
		cp.MoveIntent = cloneMoveIntent(a.MoveIntent)
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
	AtTick            uint64
	DisplayName       string
	Kind              ActorKind
	State             ActorState // checkpointed; restart resumes in same state
	Role              string
	InsideStructureID StructureID
	CurrentX          int
	CurrentY          int
	CurrentHuddleID   HuddleID
	Needs             map[NeedKey]int
	InventoryHash     uint64 // fast-compare; computed at snapshot time
	Coins             int

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
	return &cp
}
