package sim

import "time"

// Snapshot is the immutable, slim view of the world that admin endpoints,
// perception build, and the checkpoint writer all consume. The world
// goroutine publishes a fresh Snapshot via World.published (atomic.Pointer)
// after every command, so readers atomic.Load and serialize without
// touching the world goroutine.
//
// The snapshot deliberately omits secondary indices, mutable handler state,
// and any field that consumers can recompute or don't need.
type Snapshot struct {
	AtTick      uint64
	PublishedAt time.Time

	Actors         map[ActorID]*ActorSnapshot
	Huddles        map[HuddleID]*Huddle
	Scenes         map[SceneID]*Scene
	Structures     map[StructureID]*Structure
	Orders         map[OrderID]*Order
	VillageObjects map[VillageObjectID]*VillageObject

	// Quotes is the published snapshot of World.Quotes — every scene
	// quote in the world (active and terminal), deep-cloned via
	// CloneSceneQuote so snapshot readers can't reach back into world
	// state. PC client perception build looks up
	// Snapshot.Scenes[sceneID].QuoteIDs and dereferences each ID
	// against this map; NPC perception build reads the same data on
	// the world goroutine via the live World.Quotes (no snapshot
	// trip needed). Phase 3 PR S3.
	Quotes map[QuoteID]*SceneQuote

	Environment WorldEnvironment
	Phase       Phase
}
