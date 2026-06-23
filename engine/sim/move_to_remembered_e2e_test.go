package sim_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// move_to_remembered_e2e_test.go — LLM-78. End-to-end coverage of the
// memory-backed name-resolution fallback on a running world: a remembered place
// that THIS tick's perception did not surface resolves by name and ISSUES the
// walk; a never-known name still rejects (no omniscience leak); a remembered
// place since removed rejects cleanly with a steer; and a live cue wins a name it
// shares with a remembered place (prefer live, fall back to memory). The resolver
// internals are covered white-box in move_to_remembered_test.go.

// buildRememberedTestWorld seeds a running world with places that are all BEYOND
// the radius-3 name-resolution scene scan and are not the walker's anchors, so
// they are reachable by name ONLY through the threaded shown / remembered sets:
//   - "tavern": a far open structure (memory-only).
//   - "berry_patch": a far BARE gather patch with no refresh row (memory-only;
//     the live object resolver skips non-refresh objects).
//   - "market_near" / "market_far": two structures sharing the name "Market",
//     for the live-wins-over-memory precedence test.
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
		// Two "Market" placements, both beyond the scan: one reachable only via the
		// shown set, one only via memory.
		"market_near": {ID: "market_near", AssetID: "house", Pos: sim.WorldPos{X: 288, Y: 0}, EntryPolicy: sim.EntryPolicyOpen},
		"market_far":  {ID: "market_far", AssetID: "house", Pos: sim.WorldPos{X: 0, Y: 288}, EntryPolicy: sim.EntryPolicyOpen},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern":      {ID: "tavern", DisplayName: "The Tavern"},
		"market_near": {ID: "market_near", DisplayName: "Market"},
		"market_far":  {ID: "market_far", DisplayName: "Market"},
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

// A remembered structure not shown this tick resolves by name and issues the walk.
func TestMoveToByName_RememberedStructureResolvesAndWalks(t *testing.T) {
	w, cancel := buildRememberedTestWorld(t)
	defer cancel()

	remembered := sim.RememberedPlaces{StructureIDs: []sim.StructureID{"tavern"}}
	if _, err := w.Send(sim.MoveToStructureByName("walker", "The Tavern", nil, nil, remembered, time.Now().UTC())); err != nil {
		t.Fatalf("MoveToStructureByName(The Tavern) via memory: %v", err)
	}
	_, sid := destKindOf(t, w, "walker")
	if sid != "tavern" {
		t.Errorf("dest structure = %q, want tavern (resolved via the memory fallback)", sid)
	}
}

// A remembered BARE gather patch (no refresh row) resolves by name and issues the
// object visit — proving the memory object resolver does NOT gate on
// objectIsRefreshSource the way the live object resolver does.
func TestMoveToByName_RememberedGatherPatchResolvesAndWalks(t *testing.T) {
	w, cancel := buildRememberedTestWorld(t)
	defer cancel()

	remembered := sim.RememberedPlaces{ObjectIDs: []sim.VillageObjectID{"berry_patch"}}
	if _, err := w.Send(sim.MoveToStructureByName("walker", "Raspberry Patch", nil, nil, remembered, time.Now().UTC())); err != nil {
		t.Fatalf("MoveToStructureByName(Raspberry Patch) via memory: %v", err)
	}
	kind, oid := objDestOf(t, w, "walker")
	if kind != sim.MoveDestinationObjectVisit || oid != "berry_patch" {
		t.Errorf("dest = %q/%q, want object_visit/berry_patch (resolved via the memory fallback)", kind, oid)
	}
}

// A never-known place name still rejects even with a non-empty memory set — the
// no-omniscience guard holds (only shown OR personally-experienced places resolve).
func TestMoveToByName_NeverKnownRejectsDespiteMemory(t *testing.T) {
	w, cancel := buildRememberedTestWorld(t)
	defer cancel()

	remembered := sim.RememberedPlaces{
		StructureIDs: []sim.StructureID{"tavern"},
		ObjectIDs:    []sim.VillageObjectID{"berry_patch"},
	}
	_, err := w.Send(sim.MoveToStructureByName("walker", "The Smithy", nil, nil, remembered, time.Now().UTC()))
	if err == nil {
		t.Fatal("want reject for a never-known place name, got nil")
	}
	if !strings.Contains(err.Error(), "no place called") {
		t.Errorf("error lacks the 'no place called' steer: %v", err)
	}
	if mi := moveIntentOf(t, w, "walker"); mi != nil {
		t.Error("a rejected never-known name stamped a MoveIntent; want none")
	}
}

// A remembered place since removed from the world rejects cleanly with a steer —
// liveness is re-validated against the live world, never a walk to a ghost.
func TestMoveToByName_RememberedButDeletedRejectsWithSteer(t *testing.T) {
	w, cancel := buildRememberedTestWorld(t)
	defer cancel()

	// Demolish the tavern on the world goroutine, then name it from memory.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.Structures, sim.StructureID("tavern"))
		delete(world.VillageObjects, sim.VillageObjectID("tavern"))
		return nil, nil
	}}); err != nil {
		t.Fatalf("demolish tavern: %v", err)
	}

	remembered := sim.RememberedPlaces{StructureIDs: []sim.StructureID{"tavern"}}
	_, err := w.Send(sim.MoveToStructureByName("walker", "The Tavern", nil, nil, remembered, time.Now().UTC()))
	if err == nil {
		t.Fatal("want reject for a remembered place since removed, got nil")
	}
	if !strings.Contains(err.Error(), "no place called") {
		t.Errorf("error lacks the 'no place called' steer: %v", err)
	}
	if mi := moveIntentOf(t, w, "walker"); mi != nil {
		t.Error("a remembered-but-removed reject stamped a MoveIntent; want none")
	}
}

// A name shared by a live-cue place (market_near, in the shown set) and a
// remembered place (market_far) resolves to the LIVE one — and the result is
// stable regardless of the remembered slice's order (prefer live, fall back to
// memory). The memory tier alone would tie-break to market_far (lower id at equal
// distance), so this proves live truly wins rather than coincidentally agreeing.
func TestMoveToByName_LiveCueWinsOverMemory(t *testing.T) {
	shown := []sim.StructureID{"market_near"}
	orders := [][]sim.StructureID{
		{"market_far"},                // memory names a DIFFERENT place by the same name
		{"market_far", "market_near"}, // memory names both; live must still win
	}
	for _, order := range orders {
		w, cancel := buildRememberedTestWorld(t)
		remembered := sim.RememberedPlaces{StructureIDs: order}
		_, err := w.Send(sim.MoveToStructureByName("walker", "Market", shown, nil, remembered, time.Now().UTC()))
		if err != nil {
			cancel()
			t.Fatalf("MoveToStructureByName(Market) order %v: %v", order, err)
		}
		_, sid := destKindOf(t, w, "walker")
		cancel()
		if sid != "market_near" {
			t.Errorf("order %v: dest = %q, want market_near (live cue wins over memory)", order, sid)
		}
	}
}
