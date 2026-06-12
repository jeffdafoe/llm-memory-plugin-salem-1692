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
//
// DestStructureID / DestObjectID name the DESTINATION the mover walked to —
// the target of the MoveDestination, not necessarily where the actor ended
// up physically inside (mirrors ArrivalWarrantReason). A StructureVisit/knock
// arrives at a loiter slot OUTSIDE the shop, so FinalStructureID is empty
// there while DestStructureID names the shop; an ObjectVisit (well/tree/
// gather pile) sets DestObjectID and leaves both structure fields empty;
// both empty = a bare Position arrival with no nameable place. Carried so a
// subscriber that labels the destination (the action-log walked entry,
// ZBBS-WORK-359) resolves the same name the arrival warrant renders, without
// re-deriving it from the now-cleared MoveIntent. Both are value types with
// no inner pointer.
type ActorArrived struct {
	EventBase
	ActorID           ActorID
	FinalPosition     Position
	FinalStructureID  StructureID     // empty = arrived outdoors / at a visitor slot
	DestStructureID   StructureID     // destination target (StructureEnter/StructureVisit), else ""
	DestObjectID      VillageObjectID // destination target (ObjectVisit), else ""
	MovementAttemptID MovementAttemptID
	At                time.Time

	// Knocked — the walk was EnterOrKnock's knock routing (ZBBS-HOME-445):
	// DestStructureID is the knocked structure and the actor stopped at its
	// loiter slot. The knock-arrival subscriber forms the service huddle
	// with the receiver(s) inside; the outdoor encounter cascade skips the
	// arrival (the knocker's attention is on the door, not on passersby).
	Knocked bool
}

func (ActorArrived) isSimEvent() {}

// ActorInsideChanged fires when an actor's InsideStructureID actually
// changes (setActorInsideStructure no-ops an unchanged value, so every emit
// is a real flip). It is the authoritative inside-state push the client uses
// to re-render sprite visibility + the see-through-structure stand offset the
// moment the flip happens — not only at the walk-start / arrival brackets it
// otherwise reconstructs inside from. This restores the v1 npc_inside_changed
// broadcast the v2 rewrite never ported: the client's apply_npc_inside_change
// handler has been live the whole time, dead-lettered against an event the
// engine stopped sending. Emitting from the setActorInsideStructure chokepoint
// means it also catches inside flips that have no walk to bracket them (a
// structure removed out from under a standing actor, an admin move) and
// backstops a dropped npc_walking. ZBBS-WORK-373.
//
// Empty InsideStructureID = the actor is now outdoors. No At field: the wire
// frame carries only id + inside + inside_structure_id, so no wall-clock is
// needed and the emit needs no `now` threaded through its callers.
type ActorInsideChanged struct {
	EventBase
	ActorID           ActorID
	InsideStructureID StructureID // empty = now outdoors
}

func (ActorInsideChanged) isSimEvent() {}

// ActorTeleported fires when an operator displaces an actor via the
// umbilical set-position command (ZBBS-HOME-448) — an out-of-world
// position mutation with no walk. Deliberately a DISTINCT type from
// ActorArrived: the arrival cascades (encounter huddles, route advance,
// knock service-huddles) subscribe to the ActorArrived Go type and must
// not react to a teleport as if the actor walked somewhere. The wire
// translator maps this to the SAME npc_arrived frame ActorArrived uses
// (the client's authoritative snap-to-tile), so the viewer updates with
// no client change while the engine-side arrival machinery stays silent.
type ActorTeleported struct {
	EventBase
	ActorID           ActorID
	FromPosition      Position
	ToPosition        Position
	InsideStructureID StructureID // post-teleport attribution; empty = outdoors
	At                time.Time
}

func (ActorTeleported) isSimEvent() {}

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
	// (ZBBS-WORK-340). RETAINED for wire compatibility but NO LONGER EMITTED
	// as of ZBBS-HOME-327: a stably-blocked mover now walks THROUGH the
	// blocking actor and continues (see advanceActorViaReroute) instead of
	// stopping, so the only remaining trace of the event is the umbilical
	// /deadlocks ring (still recorded as a contention canary). Kept defined so
	// the wire enum and any historical consumer stay valid.
	MoveStoppedDeadlocked MoveStoppedReason = "deadlocked"

	// MoveStoppedCancelled — the actor voluntarily halted its own walk via
	// the `stop` tool (ZBBS-HOME-338). Not a failure: distinct from the
	// reasons above so the client / any subscriber can tell a deliberate halt
	// apart from a blocked / unreachable / invalidated / deadlocked stop. The
	// reactor stamps no warrant on any move-stop, so a cancel is benign there.
	MoveStoppedCancelled MoveStoppedReason = "cancelled"
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
// (declared in reactor.go's catalog). The prompt builder type-switches on
// this reason to render "You arrived at <place>" for the agent.
//
// AtStructureID / AtObjectID name the DESTINATION the mover walked to — not
// necessarily the structure it is physically inside: AtStructureID is the
// target of a StructureEnter or StructureVisit (a StructureVisit/knock
// arrives at a loiter slot OUTSIDE the shop, so InsideStructureID is empty
// there), AtObjectID the target of an ObjectVisit (a well, tree, gather
// pile). Both follow the v2 empty-string convention; both empty means the
// actor arrived at a bare Position with no nameable place ("You arrived.").
// Perception resolves whichever is set to a display name
// (build.buildWarrantPlaceNames). Both are value types with no inner
// pointer, so CloneActor's existing shallow Warrants copy stays correct
// (see the CloneActor doc-comment).
type ArrivalWarrantReason struct {
	AttemptID     MovementAttemptID
	AtStructureID StructureID     // destination structure (StructureEnter/StructureVisit), else ""
	AtObjectID    VillageObjectID // destination object (ObjectVisit), else ""
	AtPosition    Position
}

func (ArrivalWarrantReason) isWarrantReason()             {}
func (ArrivalWarrantReason) Kind() WarrantKind            { return WarrantKindArrived }
func (r ArrivalWarrantReason) DedupDiscriminator() uint64 { return uint64(r.AttemptID) }
