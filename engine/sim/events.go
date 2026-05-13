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
type Event interface {
	isSimEvent()
}

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
// OriginStructureID is empty for cascades not tied to a single structure
// (chronicler atmosphere refresh, admin-triggered fires).
type SceneMinted struct {
	SceneID           SceneID
	OriginKind        string
	OriginStructureID StructureID
	At                time.Time
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
	A, B     ActorID
	HuddleID HuddleID
	At       time.Time
}

func (ActorMet) isSimEvent() {}
