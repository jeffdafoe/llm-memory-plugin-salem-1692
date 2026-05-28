package sim

// StructureID identifies a building or named location in the world.
type StructureID string

// Position is an X/Y grid (tile) coordinate on the village terrain. Alias of
// the canonical TilePos (geom.go) — retained as a name during the
// coordinate-type migration so existing call sites compile unchanged.
type Position = TilePos

// Structure is a building or named location. Loiter slots and asset
// placement details are ported when those subsystems land.
//
// A Structure has NO position field. The Shared-Identity Bridge (see
// engine/sim/structure_anchors.go) guarantees every Structure.ID matches
// a VillageObject.ID, and VillageObject.Pos (world-pixels) is the single
// source of truth for "where is this building." Code that needs the
// structure's tile anchor calls villageObjectForStructure(w, id) and
// reads vobj.Pos.Tile() (canonical padded-tile wire unit; see
// shared/notes/codebase/salem/coordinate-frames). ZBBS-WORK-342 removed
// the redundant Structure.Position field + the corresponding pg
// structure.position_x / position_y columns — they had been seeded
// unpadded from v1 and never updated by any runtime move, so every
// editor structure-move silently left them stale.
type Structure struct {
	ID          StructureID
	DisplayName string
	Tags        []string

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
	// append([]string(nil), empty...) returns nil, which pgx encodes as SQL
	// NULL and the tags TEXT[] NOT NULL column rejects — aborting the whole
	// checkpoint. make([]string, 0, len) keeps the clone non-nil for an
	// empty source, matching the load-side coercion (repo/pg/structures.go)
	// and the repo's "tags is always non-nil" invariant.
	cp.Tags = append(make([]string, 0, len(s.Tags)), s.Tags...)
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
