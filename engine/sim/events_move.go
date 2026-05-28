package sim

import "time"

// events_move.go — PR 4 locomotion events + the arrival warrant reason.
//
// All four movement events ride PR 1's typed event bus (see events.go):
// concrete struct + unexported isSimEvent marker, emitted synchronously
// from the locomotion command handlers / ticker as the mutation lands.
// (ActorMoveStarted was added later for the client read surface — see its
// doc-comment.)
//
// STRUCTURE-ID CONVENTION. The PR 4 design note typed the structure
// fields as *StructureID ("nullable — outdoor positions have no
// structure"). v2's actual convention, used everywhere else (Actor.
// InsideStructureID, HuddleJoined.StructureID, ...), is a plain
// StructureID with the empty string as the "no structure" sentinel.
// These events follow the v2 convention: an empty StructureID means the
// actor is outdoors / at a bare position.

// ActorMoveStarted fires when MoveActor accepts a request and stamps a
// fresh MoveIntent (the start of a walk). Unlike the per-tile ActorMoved
// — which is engine-internal — this is the event the CLIENT consumes to
// begin animating a walk.
//
// It carries the FULL cost-weighted tile Path the engine computed at
// move-accept (FindPath over the same WalkGrid the locomotion ticker
// uses: roads preferred, buildings + their overhang avoided, water
// impassable, door corridors carved). MoveActor already runs that
// FindPath as its reachability check, so broadcasting the result is free
// — and it keeps the engine's pathing intelligence on the screen rather
// than forcing the viewer to re-derive a (dumber) route to the goal.
// Path is inclusive of both endpoints: Path[0] is the actor's start tile
// (== FromPosition) and Path[len-1] is the resolved goal (==
// TargetPosition). FromPosition / TargetPosition are retained as
// convenience accessors for those endpoints.
//
// The Path is a VISUAL PREVIEW: the locomotion ticker re-plans one tile
// per LocomotionTickInterval and may diverge under dynamic actor-
// occupancy contention the static grid does not model. The engine stays
// authoritative on the outcome — ActorArrived on success, ActorMoveStopped
// on failure, both carrying the same MovementAttemptID — and the client
// reconciles any in-transit drift by snapping on arrival.
//
// A superseding MoveActor (replacing an in-flight intent) emits a fresh
// ActorMoveStarted with a new MovementAttemptID; the prior attempt has no
// explicit cancel event (same supersede posture as ActorMoveStopped) —
// the new attempt ID is the signal that the old walk is obsolete.
type ActorMoveStarted struct {
	EventBase
	ActorID           ActorID
	FromPosition      Position            // actor's tile at move start (== Path[0])
	TargetPosition    Position            // resolved goal tile (== Path[len-1])
	Path              []GridPoint         // full cost-weighted tile path, start→goal inclusive
	DestinationKind   MoveDestinationKind // structure_enter | structure_visit | object_visit | position
	StructureID       StructureID         // destination structure for enter/visit; empty otherwise
	ObjectID          VillageObjectID     // destination village object for object_visit; empty otherwise
	MovementAttemptID MovementAttemptID
	At                time.Time
}

func (ActorMoveStarted) isSimEvent() {}

// ActorMoved fires once per tile the locomotion ticker advances an actor.
// The event bus is synchronous with world mutation, so subscribers MUST
// stay lightweight — expensive consumers (narrative generation, LLM-
// facing perception) must coalesce or filter rather than do per-tile
// work. A subscriber that only cares about structure transitions filters
// on FromStructureID != ToStructureID; one that cares about arrival
// subscribes to ActorArrived instead.
type ActorMoved struct {
	EventBase
	ActorID           ActorID
	FromPosition      Position
	ToPosition        Position
	FromStructureID   StructureID // empty = was outdoors
	ToStructureID     StructureID // empty = now outdoors
	MovementAttemptID MovementAttemptID
	At                time.Time
}

func (ActorMoved) isSimEvent() {}

// ActorArrived fires when an actor reaches its MoveDestination. The
// arrival also stamps an ArrivalWarrantReason on the mover (PR 2's
// warrant funnel) so the agent layer can react to "you have arrived".
// FinalStructureID is empty for Position destinations that aren't inside
// a structure and for StructureVisit destinations (the actor stands at a
// visitor slot, outside).
type ActorArrived struct {
	EventBase
	ActorID           ActorID
	FinalPosition     Position
	FinalStructureID  StructureID // empty = arrived outdoors / at a visitor slot
	MovementAttemptID MovementAttemptID
	At                time.Time
}

func (ActorArrived) isSimEvent() {}

// MoveStoppedReason discriminates why an accepted movement attempt failed
// to reach its destination.
type MoveStoppedReason string

const (
	// MoveStoppedBlocked — a hard blocker (wall, closed door without a
	// key, a structure deleted onto the path). No retry.
	MoveStoppedBlocked MoveStoppedReason = "blocked"

	// MoveStoppedUnreachable — no path exists from the actor's current
	// position to the destination this tick.
	MoveStoppedUnreachable MoveStoppedReason = "unreachable"

	// MoveStoppedInvalidated — the destination itself became invalid
	// (e.g. the target structure was removed) while the actor was en
	// route.
	MoveStoppedInvalidated MoveStoppedReason = "invalidated"

	// MoveStoppedDeadlocked — the mover soft-blocked for
	// DeadlockStuckThreshold consecutive ticks with no advanceable re-plan
	// (ZBBS-WORK-340). Distinct from Blocked because the obstruction was
	// another actor (transient by nature), not a wall — NPC behavior trees
	// can treat it as "retry later" rather than "the destination is
	// fundamentally unreachable, pick a new goal". The umbilical
	// /deadlocks view records the event with mover + occupant + a
	// replan-failed flag so operators can see deadlock frequency in live
	// play.
	MoveStoppedDeadlocked MoveStoppedReason = "deadlocked"
)

// ActorMoveStopped fires when an ACCEPTED movement attempt fails to reach
// its destination — the locomotion equivalent of a non-arrival
// termination, which PR 2's reactor needs (silent failure isn't
// compatible with the warrant eligibility model).
//
// Supersede does NOT emit ActorMoveStopped: when a new MoveActor command
// replaces an in-flight intent, the new command is the observable
// transition and the old attempt dies silently. Subscribers tracking
// movement completion should compare MovementAttemptID against the
// actor's current MoveIntent.AttemptID before reacting.
type ActorMoveStopped struct {
	EventBase
	ActorID           ActorID
	Position          Position // where the actor stopped
	StructureID       StructureID
	Destination       MoveDestination
	Reason            MoveStoppedReason
	MovementAttemptID MovementAttemptID
	At                time.Time
}

func (ActorMoveStopped) isSimEvent() {}

// ArrivalWarrantReason is the WarrantReason stamped on a mover when it
// reaches its MoveDestination. Kind is the pre-existing WarrantKindArrived
// (declared in reactor.go's catalog). PR 3's prompt builder type-switches
// on this reason to render "you have arrived at X" for the agent.
//
// AtStructureID follows the v2 empty-string convention — empty means the
// actor arrived at a bare position or a visitor slot, not inside a
// structure. The reason therefore carries no inner pointer, so
// CloneActor's existing shallow Warrants copy stays correct (see the
// CloneActor doc-comment).
type ArrivalWarrantReason struct {
	AttemptID     MovementAttemptID
	AtStructureID StructureID
	AtPosition    Position
}

func (ArrivalWarrantReason) isWarrantReason()             {}
func (ArrivalWarrantReason) Kind() WarrantKind            { return WarrantKindArrived }
func (r ArrivalWarrantReason) DedupDiscriminator() uint64 { return uint64(r.AttemptID) }
