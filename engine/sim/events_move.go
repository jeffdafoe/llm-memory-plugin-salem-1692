package sim

import "time"

// events_move.go — PR 4 locomotion events + the arrival warrant reason.
//
// All three movement events ride PR 1's typed event bus (see events.go):
// concrete struct + unexported isSimEvent marker, emitted synchronously
// from the locomotion command handlers / ticker as the mutation lands.
//
// STRUCTURE-ID CONVENTION. The PR 4 design note typed the structure
// fields as *StructureID ("nullable — outdoor positions have no
// structure"). v2's actual convention, used everywhere else (Actor.
// InsideStructureID, HuddleJoined.StructureID, ...), is a plain
// StructureID with the empty string as the "no structure" sentinel.
// These events follow the v2 convention: an empty StructureID means the
// actor is outdoors / at a bare position.

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
