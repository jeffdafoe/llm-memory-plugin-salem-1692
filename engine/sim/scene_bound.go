package sim

// SceneBound describes the spatial scope of a Scene — the rule that
// decides whether an actor counts as "in this scene's area" for the
// purposes of JoinHuddle's physical-presence invariant and the drift
// auto-leave check.
//
// The bound is a closed tagged union over three kinds:
//
//   - SceneBoundStructure: the scene is tied to a specific building.
//     Containment = actor is physically inside that structure.
//     Scenes of this kind may contain multiple parallel huddles (the
//     tavern with three simultaneous conversations).
//
//   - SceneBoundArea: the scene is tied to an outdoor circular area.
//     Containment = actor is outdoors AND within Radius tiles of Anchor.
//     Scenes of this kind contain exactly one huddle by invariant; when
//     that huddle concludes, the scene concludes as well.
//
//   - SceneBoundUnbounded: the scene has no spatial scope (chronicler
//     atmosphere refresh, admin-triggered fires, future world-scope
//     scenes). Containment returns true for every actor; in practice
//     unbounded scenes don't receive JoinHuddle calls.
//
// SceneBound is a tagged struct (not a Go interface) because Scene state
// is published in Snapshot, captured in ParticipantStateAtOrigin, cloned
// at mem-repo boundaries, and will eventually be written to checkpoint
// rows. Interfaces don't serialize cleanly across those boundaries;
// closed tagged unions do.
type SceneBound struct {
	Kind SceneBoundKind

	// StructureID is set iff Kind == SceneBoundStructure.
	StructureID *StructureID

	// Anchor and Radius are set iff Kind == SceneBoundArea.
	Anchor *Position
	Radius *int
}

// SceneBoundKind enumerates the spatial-scope variants of a Scene.
type SceneBoundKind string

const (
	SceneBoundStructure SceneBoundKind = "structure"
	SceneBoundArea      SceneBoundKind = "area"
	SceneBoundUnbounded SceneBoundKind = "unbounded"
)

// NewStructureBound returns a SceneBound for a scene tied to the given
// structure. The structure must exist in the world; CreateScene
// validates that at command time.
func NewStructureBound(structureID StructureID) SceneBound {
	id := structureID
	return SceneBound{Kind: SceneBoundStructure, StructureID: &id}
}

// NewAreaBound returns a SceneBound for an outdoor scene centered on
// anchor with the given Chebyshev radius. Radius < 0 is clamped to 0
// (a 1-tile bound on the anchor itself); the caller is expected to pass
// WorldSettings.DefaultOutdoorSceneRadius for the typical "conversational
// distance" case.
func NewAreaBound(anchor Position, radius int) SceneBound {
	if radius < 0 {
		radius = 0
	}
	a := anchor
	r := radius
	return SceneBound{Kind: SceneBoundArea, Anchor: &a, Radius: &r}
}

// NewUnboundedBound returns a SceneBound for a scene with no spatial
// scope. Used for chronicler atmosphere refresh, admin-triggered fires,
// and any scene that observes village-wide activity rather than a
// single location.
func NewUnboundedBound() SceneBound {
	return SceneBound{Kind: SceneBoundUnbounded}
}

// Contains reports whether the actor satisfies this bound's spatial
// rule. Used by JoinHuddle's physical-presence invariant and the drift
// auto-leave check.
//
// Nil actors are not contained (defensive guard so callers don't have
// to nil-check before each call). The world parameter is reserved for
// future bound kinds that need to read additional state (line of
// sight, terrain, occlusion); current implementations ignore it.
func (b SceneBound) Contains(_ *World, actor *Actor) bool {
	if actor == nil {
		return false
	}
	switch b.Kind {
	case SceneBoundStructure:
		if b.StructureID == nil {
			return false
		}
		return actor.InsideStructureID != "" && actor.InsideStructureID == *b.StructureID
	case SceneBoundArea:
		if b.Anchor == nil || b.Radius == nil {
			return false
		}
		// Outdoor scenes never contain an actor who is inside any
		// structure. A tile within Radius of Anchor that happens to be
		// owned by a nearby building would otherwise let an indoor
		// actor count as "in the outdoor huddle" — wrong.
		if actor.InsideStructureID != "" {
			return false
		}
		return chebyshevDistance(Position{X: actor.CurrentX, Y: actor.CurrentY}, *b.Anchor) <= *b.Radius
	case SceneBoundUnbounded:
		return true
	}
	return false
}

// cloneSceneBound returns a deep copy of the bound — required by
// CloneScene because SceneBound's pointer fields would otherwise alias
// across the published-Snapshot and mem-repo boundary.
func cloneSceneBound(b SceneBound) SceneBound {
	cp := SceneBound{Kind: b.Kind}
	if b.StructureID != nil {
		id := *b.StructureID
		cp.StructureID = &id
	}
	if b.Anchor != nil {
		a := *b.Anchor
		cp.Anchor = &a
	}
	if b.Radius != nil {
		r := *b.Radius
		cp.Radius = &r
	}
	return cp
}

// chebyshevDistance returns the king's-move distance between two tiles.
// Used by SceneBoundArea.Contains — a conversational radius of N means
// "within N king's-moves" so diagonal proximity counts the same as
// orthogonal, which matches how a circular area reads on a grid.
func chebyshevDistance(a, b Position) int {
	dx := a.X - b.X
	if dx < 0 {
		dx = -dx
	}
	dy := a.Y - b.Y
	if dy < 0 {
		dy = -dy
	}
	if dx > dy {
		return dx
	}
	return dy
}
