package sim_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// move_to_remembered_e2e_test.go — LLM-78 / LLM-142. End-to-end coverage of
// name-resolution on a running world: a far structure resolves by name (village
// geography is common knowledge, LLM-142) and ISSUES the walk; a far BARE gather
// patch resolves ONLY through the memory fallback and issues the object visit; a
// name matching no village structure and no remembered object rejects cleanly
// with a steer; and a since-removed structure rejects (liveness re-validated, no
// walk to a ghost). The resolver internals are covered white-box in
// move_to_remembered_test.go.

// buildRememberedTestWorld seeds a running world with:
//   - "tavern": a far open structure (10 tiles east, beyond any scene scan) —
//     resolves by village name with no shown/remembered threading (LLM-142).
//   - "berry_patch": a far BARE gather patch with no refresh row (memory-only;
//     the live object resolver skips non-refresh objects).
//
// All sit on an all-grass grid with a clear path from the walker at the pad.
func buildRememberedTestWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"house": {ID: "house", Category: "structure", DoorOffsetX: intp(0), DoorOffsetY: intp(2)},
		"prop":  {ID: "prop", Category: "prop"},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		// 10 tiles east — beyond the radius-3 scan, on-grid, path-reachable.
		"tavern": {ID: "tavern", AssetID: "house", Pos: sim.WorldPos{X: 320, Y: 0}, EntryPolicy: sim.EntryPolicyOpen},
		// 10 tiles south — a bare gather patch, NO refresh row (not a refresh source).
		"berry_patch": {ID: "berry_patch", AssetID: "prop", Pos: sim.WorldPos{X: 0, Y: 320}, DisplayName: "Raspberry Patch"},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "The Tavern"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"walker": {ID: "walker", DisplayName: "Walker", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// A far, never-visited, non-anchor structure resolves by name and issues the walk
// — village geography is common knowledge (LLM-142), no shown/remembered needed.
// This is the live scene-019f094d case (a Walker naming "the Tavern" across town).
func TestMoveToByName_FarStructureResolvesAndWalks(t *testing.T) {
	w, cancel := buildRememberedTestWorld(t)
	defer cancel()

	if _, err := w.Send(sim.MoveToStructureByName("walker", "The Tavern", nil, sim.RememberedPlaces{}, time.Now().UTC())); err != nil {
		t.Fatalf("MoveToStructureByName(The Tavern): %v", err)
	}
	_, sid := destKindOf(t, w, "walker")
	if sid != "tavern" {
		t.Errorf("dest structure = %q, want tavern (resolved as common-knowledge geography)", sid)
	}
}

// A remembered BARE gather patch (no refresh row) resolves by name and issues the
// object visit — proving the memory object resolver does NOT gate on
// objectIsRefreshSource the way the live object resolver does. Objects stay
// discovered (a wild bush is not common knowledge like a building).
func TestMoveToByName_RememberedGatherPatchResolvesAndWalks(t *testing.T) {
	w, cancel := buildRememberedTestWorld(t)
	defer cancel()

	remembered := sim.RememberedPlaces{ObjectIDs: []sim.VillageObjectID{"berry_patch"}}
	if _, err := w.Send(sim.MoveToStructureByName("walker", "Raspberry Patch", nil, remembered, time.Now().UTC())); err != nil {
		t.Fatalf("MoveToStructureByName(Raspberry Patch) via memory: %v", err)
	}
	kind, oid := objDestOf(t, w, "walker")
	if kind != sim.MoveDestinationObjectVisit || oid != "berry_patch" {
		t.Errorf("dest = %q/%q, want object_visit/berry_patch (resolved via the memory fallback)", kind, oid)
	}
}

// A name matching no village structure and no remembered object still rejects,
// even with a non-empty memory set — a bare source stays discovered.
func TestMoveToByName_UnknownNameRejectsDespiteMemory(t *testing.T) {
	w, cancel := buildRememberedTestWorld(t)
	defer cancel()

	remembered := sim.RememberedPlaces{ObjectIDs: []sim.VillageObjectID{"berry_patch"}}
	_, err := w.Send(sim.MoveToStructureByName("walker", "The Smithy", nil, remembered, time.Now().UTC()))
	if err == nil {
		t.Fatal("want reject for a name no structure or remembered source has, got nil")
	}
	if !strings.Contains(err.Error(), "no place called") {
		t.Errorf("error lacks the 'no place called' steer: %v", err)
	}
	if mi := moveIntentOf(t, w, "walker"); mi != nil {
		t.Error("a rejected unknown name stamped a MoveIntent; want none")
	}
}

// A structure since removed from the world rejects cleanly with a steer — the
// village resolver re-validates liveness against the live world, never a walk to
// a ghost.
func TestMoveToByName_RemovedStructureRejectsWithSteer(t *testing.T) {
	w, cancel := buildRememberedTestWorld(t)
	defer cancel()

	// Demolish the tavern on the world goroutine, then name it.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.Structures, sim.StructureID("tavern"))
		delete(world.VillageObjects, sim.VillageObjectID("tavern"))
		return nil, nil
	}}); err != nil {
		t.Fatalf("demolish tavern: %v", err)
	}

	_, err := w.Send(sim.MoveToStructureByName("walker", "The Tavern", nil, sim.RememberedPlaces{}, time.Now().UTC()))
	if err == nil {
		t.Fatal("want reject for a structure since removed, got nil")
	}
	if !strings.Contains(err.Error(), "no place called") {
		t.Errorf("error lacks the 'no place called' steer: %v", err)
	}
	if mi := moveIntentOf(t, w, "walker"); mi != nil {
		t.Error("a removed-structure reject stamped a MoveIntent; want none")
	}
}
