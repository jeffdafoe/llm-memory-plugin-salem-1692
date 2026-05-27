package sim_test

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// room_reconcile_test.go — setActorInsideStructure must keep InsideRoomID
// reconciled with InsideStructureID. A room belongs to exactly one structure,
// so a structure change that leaves a foreign InsideRoomID dangling produces
// the cross-structure mismatch that validateActorStructureRefs hard-fails the
// next LoadWorld on (it crash-looped the engine when an NPC walked from its
// workplace room back home).
func TestSetActorInsideStructure_ReconcilesRoomOnStructureChange(t *testing.T) {
	repo, h := mem.NewRepository()
	h.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"store": {ID: "store", DisplayName: "General Store", Rooms: []*sim.Room{
			{ID: 9, StructureID: "store", Kind: sim.RoomKindCommon, Name: "common"},
		}},
		"home": {ID: "home", DisplayName: "Residence", Rooms: []*sim.Room{
			{ID: 20, StructureID: "home", Kind: sim.RoomKindCommon, Name: "common"},
		}},
	})
	h.Actors.Seed(map[sim.ActorID]*sim.Actor{
		// In the store, in the store's room — the consistent starting state.
		"walker": {ID: "walker", InsideStructureID: "store", InsideRoomID: 9},
		// Contrived: in the store but holding home's room. Entering home should
		// PRESERVE the room (it belongs to the structure being entered).
		"preserve": {ID: "preserve", InsideStructureID: "store", InsideRoomID: 20},
		// In the store's room, about to step outdoors.
		"outgoer": {ID: "outgoer", InsideStructureID: "store", InsideRoomID: 9},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	roomAfterMove := func(actorID sim.ActorID, dest sim.StructureID) sim.RoomID {
		res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			sim.SetActorInsideStructure(world, world.Actors[actorID], dest)
			return world.Actors[actorID].InsideRoomID, nil
		}})
		if err != nil {
			t.Fatalf("%s -> %q: %v", actorID, dest, err)
		}
		return res.(sim.RoomID)
	}

	// Cross-structure: the stale store room must be cleared.
	if got := roomAfterMove("walker", "home"); got != 0 {
		t.Errorf("walker store(room9)->home: InsideRoomID = %d, want 0 (stale room cleared)", got)
	}
	// Room belongs to the structure being entered: preserved.
	if got := roomAfterMove("preserve", "home"); got != 20 {
		t.Errorf("preserve store(room20)->home: InsideRoomID = %d, want 20 (room belongs to entered structure)", got)
	}
	// Stepping outdoors: no room can apply.
	if got := roomAfterMove("outgoer", ""); got != 0 {
		t.Errorf("outgoer store(room9)->outdoors: InsideRoomID = %d, want 0", got)
	}
}
