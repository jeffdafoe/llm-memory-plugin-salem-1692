package sim

// StructureID identifies a building or named location in the world.
type StructureID string

// Position is an X/Y grid coordinate on the village terrain.
type Position struct {
	X int
	Y int
}

// Structure is a building or named location. Loiter slots, room layouts,
// and asset placements are ported when the structures subsystem is reached
// in the cutover sequence.
type Structure struct {
	ID          StructureID
	DisplayName string
	Tags        []string
	Position    Position

	// Forward-compat for cross-realm coordination. Empty in v1; a future
	// orchestrator engine populates this on border-road structures (e.g.
	// "brunnfeld") so an actor entering a leads-to-brunnfeld structure
	// can be marked as leaving the realm. Single-realm engine ignores it.
	LeadsToRealm string
}
