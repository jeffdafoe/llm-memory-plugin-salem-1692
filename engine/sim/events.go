package sim

import "time"

// Event is the marker interface for in-world events emitted from command
// handlers as state mutations land. Subscribers (registered via
// World.Subscribe) receive every event in emission order, synchronously
// inside the world goroutine.
//
// Concrete event types in this package describe what changed; subscribers
// type-switch on the concrete type to react. The marker method
// (isSimEvent) is unexported so external packages cannot accidentally
// satisfy the interface — events are a closed set defined here.
//
// Why event-driven side effects instead of inline calls in the command
// handler: the v1 huddle code mixed lifecycle (join/leave/conclude),
// acquaintance recording, audit emission, greet/farewell narration, and
// loiter-slot adoption into one 621-LOC file with five overlapping
// concerns. Each concern was hard-wired into joinOrCreateHuddle. Adding
// a new side effect (e.g. "warn the LLM about loop patterns") would have
// meant touching the lifecycle primitive. Here, lifecycle commands emit
// typed events; concerns subscribe independently.
//
// Every Event also carries causal identity (EventID + RootEventID) stamped
// by World.emit — see EventBase. The setEventBase mutation path is
// unexported (pointer receiver), which both keeps the Event set closed at
// the sim boundary AND makes every concrete event pointer-only: only
// *ConcreteEvent satisfies Event, never the value.
type Event interface {
	isSimEvent()

	// EventID is the event's unique per-run identifier.
	EventID() EventID
	// RootEventID is the EventID of the cascade's causal root.
	RootEventID() EventID
	// setEventBase stamps identity at emit time. Unexported — only
	// World.emit assigns IDs, and the pointer receiver keeps the Event
	// set closed (external packages can neither emit nor forge identity).
	setEventBase(id, root EventID)
}

// EventID uniquely identifies an emitted event within a single world run.
// Assigned by World.emit from a plain monotonic counter — the world
// goroutine is the only emitter, so no atomic is needed. EventID(0) is the
// reserved invalid/unset sentinel: the counter starts at 1, so a real
// emitted event never has ID 0. The monotonic order is also a free total
// emission order PR 3's prompt builder relies on.
//
// EventID is per-run only — it is NOT persisted across the checkpoint
// boundary (warrants and event identity stay ephemeral).
type EventID uint64

// EventBase carries the causal identity every Event is stamped with at
// emit time. It is embedded BY VALUE (not *EventBase) in every concrete
// event type. A pointer embed would create a nil-base hazard — a
// zero-value event pointer would satisfy Event with a nil base, and
// setEventBase would panic. Value embedding has no nil risk and still
// yields pointer-only events: setEventBase has a pointer receiver, so
// only *ConcreteEvent satisfies Event, never the bare value.
type EventBase struct {
	id     EventID
	rootID EventID
}

// EventID returns the event's unique per-run identifier, or 0 if the event
// has not been emitted yet.
func (b *EventBase) EventID() EventID { return b.id }

// RootEventID returns the EventID of the causal root of the cascade this
// event belongs to. A fresh-origin event is its own root; a consequent
// event (emitted by a subscriber, or by a worker tool-call command
// continuing the tick) inherits the triggering event's root.
func (b *EventBase) RootEventID() EventID { return b.rootID }

// setEventBase stamps identity onto the event. Unexported with a pointer
// receiver: it is the mutation path World.emit uses to assign IDs, and the
// pointer receiver is what keeps the Event set closed at the sim package
// boundary.
func (b *EventBase) setEventBase(id, root EventID) { b.id, b.rootID = id, root }

// EventSubscriber consumes Events emitted by command handlers. Handle
// runs inline in the world goroutine after the command's mutation lands,
// so subscribers may mutate world state freely (atomically with the
// emitting command). They MUST NOT block on I/O — any DB write-through
// goes via a buffered channel feeding a dedicated background goroutine.
//
// Subscribers must not call World.Send / SendContext (would deadlock the
// single world goroutine). To trigger a follow-up command, mutate state
// directly here, or schedule the follow-up via the reactor when that
// lands in a later phase.
type EventSubscriber interface {
	Handle(w *World, evt Event)
}

// SubscriberFunc adapts a plain function to the EventSubscriber interface
// for tests and small subscribers that don't need their own struct.
type SubscriberFunc func(w *World, evt Event)

// Handle satisfies EventSubscriber by invoking the underlying function.
func (f SubscriberFunc) Handle(w *World, evt Event) { f(w, evt) }

// SceneMinted fires when a fresh Scene is created at cascade origin.
// Bound carries the scene's spatial scope (structure / area / unbounded);
// OriginPosition is the scene's anchor tile. Subscribers can read Bound
// directly or use the helper OriginStructureID() for the legacy
// "structure-id-or-empty" pattern.
type SceneMinted struct {
	EventBase
	SceneID        SceneID
	OriginKind     string
	Bound          SceneBound
	OriginPosition Position
	At             time.Time
}

// OriginStructureID returns the structure ID this scene was minted at,
// or empty string for non-structure-bound scenes. Convenience accessor
// for subscribers that only care about the legacy structure-tied case.
func (e SceneMinted) OriginStructureID() StructureID {
	if e.Bound.Kind != SceneBoundStructure || e.Bound.StructureID == nil {
		return ""
	}
	return *e.Bound.StructureID
}

func (SceneMinted) isSimEvent() {}

// HuddleJoined fires when ActorID enters HuddleID. OtherMembers carries
// the IDs of actors who were already in the huddle at the moment of the
// join (does not include the joining actor). SceneID is non-empty when
// the join was associated with a specific scene's narrative beat.
//
// Subscribers see this once per join. Pairwise "introductions" are
// emitted separately as ActorMet events so subscribers (acquaintance
// reactor, future relationship reactor) don't have to derive pairs.
type HuddleJoined struct {
	EventBase
	ActorID      ActorID
	HuddleID     HuddleID
	SceneID      SceneID
	StructureID  StructureID
	OtherMembers []ActorID
	HuddleNew    bool // true if the huddle was created by this join
	At           time.Time
}

func (HuddleJoined) isSimEvent() {}

// HuddleLeft fires when ActorID is removed from HuddleID. RemainingMembers
// carries the IDs of actors still in the huddle after the departure. When
// the huddle becomes empty, a HuddleConcluded event is emitted in
// addition to (and after) HuddleLeft.
type HuddleLeft struct {
	EventBase
	ActorID          ActorID
	HuddleID         HuddleID
	StructureID      StructureID
	RemainingMembers []ActorID
	At               time.Time
}

func (HuddleLeft) isSimEvent() {}

// HuddleConcluded fires when a huddle reaches zero members (or is
// force-concluded by ConcludeHuddle). Always preceded by the HuddleLeft
// (or, for force-conclude, no HuddleLeft) for the last departing
// member.
type HuddleConcluded struct {
	EventBase
	HuddleID    HuddleID
	StructureID StructureID
	At          time.Time
}

func (HuddleConcluded) isSimEvent() {}

// ActorMet fires once per (joining, prior-member) pair when an actor
// joins a huddle — captures the pairwise introductions produced by
// huddle membership. The acquaintance reactor consumes these to update
// Actor.Acquaintances and to write through to npc_acquaintance.
//
// Symmetric: a join with two prior members produces two ActorMet events
// (one per pair). Subscribers handle both directions of the relationship
// inside their handler — the event itself is a single pair (A joined,
// B was already there).
type ActorMet struct {
	EventBase
	A, B     ActorID
	HuddleID HuddleID
	At       time.Time
}

func (ActorMet) isSimEvent() {}

// ReactorTickDue fires when the evaluator emits a warrant-driven tick
// opportunity for an actor. Phase 2 PR 2 lands the event; PR 3's tick
// handler subscribes — handler builds perception (off the world goroutine
// via Published snapshot), calls the LLM, then sends CompleteReactorTick
// back through the command channel.
//
// AttemptID is the generation that makes stale completions detectable —
// the handler echoes AttemptID in CompleteReactorTick; the completion
// command is a no-op when the actor's current AttemptID has moved on.
//
// Warrants is the snapshot of the actor's pending signals at emit time,
// in stamp order. The list is consumed (cleared on the actor) at emit
// time, so this is the only place the metadata travels — the consumer
// can't fetch it later from the actor.
//
// Subscribers MUST NOT call the LLM inline (would hold the world
// goroutine for seconds). Pattern: copy IDs + AttemptID + Warrants into a
// worker queue; the worker reads world.Published() for perception build.
type ReactorTickDue struct {
	EventBase
	ActorID        ActorID
	AttemptID      TickAttemptID
	Warrants       []WarrantMeta // snapshot at emit; consumed from actor
	WarrantedSince time.Time     // when the warrant cycle began
	DueAt          time.Time     // when the warrant became due (= WarrantedSince + jitter)
	EmittedAt      time.Time
}

func (ReactorTickDue) isSimEvent() {}

// ActorDeparted fires when an actor is removed from World.Actors —
// today only emitted by the visitor cleanup path (engine/sim/visitor.go
// CleanupExpiredVisitor) past the visitor's ExpiresAt + grace window.
// Subscribers handle "left the village" semantics: action-log entry,
// analytics, downstream cache invalidation. Distinct from ActorMoveStopped
// (which is a movement-state transition for a still-present actor) and
// HuddleLeft (which only fires while the actor is alive).
//
// LastInsideStructureID and LastPosition capture the actor's last known
// location BEFORE removal so subscribers needn't read a freshly-deleted
// row off the world. Both are snapshots, not pointers into world state.
//
// VisitorContext is non-nil when the departing actor was a visitor;
// carries Archetype/Origin/Disposition so action-log / analytics can
// surface "Elias the peddler left the village" prose without joining
// back to a world record that no longer exists. Nil for non-visitor
// departures (no such path today; reserved for future).
type ActorDeparted struct {
	EventBase
	ActorID               ActorID
	DisplayName           string
	LastInsideStructureID StructureID
	LastPosition          Position
	VisitorContext        *VisitorState
	At                    time.Time
}

func (ActorDeparted) isSimEvent() {}
