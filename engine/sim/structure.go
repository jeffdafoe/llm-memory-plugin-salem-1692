package sim

// StructureID identifies a building or named location in the world.
type StructureID string

// Position is an X/Y grid (tile) coordinate on the village terrain. Alias of
// the canonical TilePos (geom.go) — retained as a name during the
// coordinate-type migration so existing call sites compile unchanged.
type Position = TilePos

// Structure is a building or named location. Loiter slots and asset
// placement details are ported when those subsystems land.
type Structure struct {
	ID          StructureID
	DisplayName string
	Tags        []string
	Position    Position

	// Rooms — first-class per-instance rooms within this structure
	// (ZBBS-149). Common room is always present; private bedrooms +
	// staff rooms vary per structure.
	Rooms []*Room

	// Forward-compat for cross-realm coordination. Empty in v1; a future
	// orchestrator engine populates this on border-road structures (e.g.
	// "brunnfeld") so an actor entering a leads-to-brunnfeld structure
	// can be marked as leaving the realm. Single-realm engine ignores it.
	LeadsToRealm string
}

// CloneStructure returns a deep copy suitable for publication via Snapshot
// or for the mem-repo serialization boundary. The Rooms slice and its
// elements are cloned so callers can't reach into the live Structure via
// the snapshot.
func CloneStructure(s *Structure) *Structure {
	if s == nil {
		return nil
	}
	cp := *s
	if s.Tags != nil {
		cp.Tags = append([]string(nil), s.Tags...)
	}
	if s.Rooms != nil {
		cp.Rooms = make([]*Room, len(s.Rooms))
		for i, r := range s.Rooms {
			if r == nil {
				continue
			}
			rc := *r
			cp.Rooms[i] = &rc
		}
	}
	return &cp
}
