package sim

// locomotion.go — PR 4 locomotion types.
//
// MoveDestination is the tagged union describing where a MoveActor
// command sends an actor; MoveIntent is the in-flight movement state the
// locomotion ticker re-plans against each tick; MovementAttemptID
// disambiguates superseded attempts for async subscribers.
//
// The locomotion *machinery* — the MoveActor command, the ticker, the
// movement events — lands in commands_move.go, locomotion_ticker.go and
// events_move.go. This file carries only the shared types.

// MoveDestinationKind enumerates the three arrival cases, derived from
// v1's EnterOnArrival semantics. See
// shared/notes/codebase/salem/structure-lookups for the v1 model.
type MoveDestinationKind string

const (
	// MoveDestinationStructureEnter — walk into the structure interior.
	// Arrival fires when Actor.InsideStructureID equals the destination
	// structure. v1's EnterOnArrival = true: home / work / social-enter.
	// Rejected at command validation for structures whose entry policy
	// forbids entry — wells, fountains and decoratives have no interior.
	MoveDestinationStructureEnter MoveDestinationKind = "structure_enter"

	// MoveDestinationStructureVisit — walk to one of the structure's
	// eight visitor slots and stop outside. Arrival fires when the actor
	// stands on the chosen slot. v1's EnterOnArrival = false: the default
	// "go to X" for chore destinations, agent move_to, loiter relocates.
	// Entry policy is NOT checked — an actor can always stand near a well.
	MoveDestinationStructureVisit MoveDestinationKind = "structure_visit"

	// MoveDestinationPosition — walk to an exact tile. Arrival fires when
	// the actor's position equals the destination tile. Patrol
	// coordinates, custom anchors, outdoor loiter points not tied to a
	// structure.
	MoveDestinationPosition MoveDestinationKind = "position"
)

// MoveDestination is a closed tagged union over the three arrival cases.
// Exactly one payload field is set, selected by Kind:
//
//   - StructureEnter / StructureVisit → StructureID set, Position nil
//   - Position                        → Position set, StructureID nil
//
// It is a tagged struct rather than a Go interface for the same reason as
// SceneBound: MoveIntent rides on Actor, which is published in Snapshot,
// cloned at the mem-repo boundary, and will eventually be checkpointed.
// Interfaces don't serialize cleanly across those boundaries; closed
// tagged unions do.
type MoveDestination struct {
	Kind        MoveDestinationKind
	StructureID *StructureID // set iff Kind is StructureEnter or StructureVisit
	Position    *Position    // set iff Kind is Position
}

// NewStructureEnterDestination returns a MoveDestination that walks the
// actor into the interior of structureID.
func NewStructureEnterDestination(structureID StructureID) MoveDestination {
	id := structureID
	return MoveDestination{Kind: MoveDestinationStructureEnter, StructureID: &id}
}

// NewStructureVisitDestination returns a MoveDestination that walks the
// actor to a visitor slot outside structureID.
func NewStructureVisitDestination(structureID StructureID) MoveDestination {
	id := structureID
	return MoveDestination{Kind: MoveDestinationStructureVisit, StructureID: &id}
}

// NewPositionDestination returns a MoveDestination that walks the actor
// to an exact tile.
func NewPositionDestination(pos Position) MoveDestination {
	p := pos
	return MoveDestination{Kind: MoveDestinationPosition, Position: &p}
}

// cloneMoveDestination deep-copies a MoveDestination. Its StructureID and
// Position pointer fields would otherwise alias across the
// published-Snapshot and mem-repo boundary once MoveIntent rides on
// Actor. Mirrors cloneSceneBound.
func cloneMoveDestination(d MoveDestination) MoveDestination {
	cp := MoveDestination{Kind: d.Kind}
	if d.StructureID != nil {
		id := *d.StructureID
		cp.StructureID = &id
	}
	if d.Position != nil {
		p := *d.Position
		cp.Position = &p
	}
	return cp
}

// MovementAttemptID is a per-actor monotonically increasing generation
// stamped on every accepted MoveActor command. It parallels PR 2's
// TickAttemptID: async subscribers compare the attempt ID carried on a
// movement event against the actor's current MoveIntent.AttemptID and
// ignore work derived from a superseded or cancelled attempt.
type MovementAttemptID uint64

// MoveIntent is an actor's in-flight movement state — carried on
// Actor.MoveIntent, nil when the actor is not moving.
//
// MoveIntent deliberately does NOT cache a path. The locomotion ticker
// re-plans from the actor's CURRENT position to the destination every
// tick, so resume-after-huddle, dynamic blockers, and displacement are
// all handled by the next tick's replan with no stale-path bookkeeping.
type MoveIntent struct {
	Destination MoveDestination
	AttemptID   MovementAttemptID
}

// cloneMoveIntent deep-copies a MoveIntent (nil-safe). Wired into
// CloneActor once Actor.MoveIntent lands, so an actor's in-flight
// movement state doesn't alias across the snapshot / mem-repo boundary.
func cloneMoveIntent(mi *MoveIntent) *MoveIntent {
	if mi == nil {
		return nil
	}
	cp := *mi
	cp.Destination = cloneMoveDestination(mi.Destination)
	return &cp
}
