package sim_test

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// loiter_resolve_test.go — ResolveLoiteringObject (the v2 port of v1's
// resolveLoiteringStructure). Pins are controlled via the per-instance
// loiter-offset branch of computeLoiterTile: an object placed at world (0,0)
// anchors at tile WorldToTile(0,0); the offset shifts the pin from there.

var loiterAssets = map[sim.AssetID]*sim.Asset{"a": {ID: "a"}}

// loiterObj builds a named object at world (0,0) (anchor = WorldToTile(0,0))
// whose loiter pin is (anchor + offset) via the per-instance offset branch.
func loiterObj(id sim.VillageObjectID, name string, offX, offY int) *sim.VillageObject {
	x, y := offX, offY
	return &sim.VillageObject{
		ID: id, DisplayName: name, AssetID: "a",
		Pos: sim.WorldPos{X: 0, Y: 0}, LoiterOffsetX: &x, LoiterOffsetY: &y,
	}
}

func TestResolveLoiteringObject_NearestWithinTolerance(t *testing.T) {
	actor := sim.WorldToTile(0, 0) // (PadX, PadY)
	objs := map[sim.VillageObjectID]*sim.VillageObject{
		"near": loiterObj("near", "Near", 0, 0), // pin == actor, dist 0
		"far":  loiterObj("far", "Far", 5, 0),   // dist 5
	}
	id, ok := sim.ResolveLoiteringObject(objs, loiterAssets, actor, sim.LoiterAttributionTiles)
	if !ok || id != "near" {
		t.Fatalf("want near (dist 0), got id=%q ok=%v", id, ok)
	}
}

func TestResolveLoiteringObject_ToleranceBoundary(t *testing.T) {
	actor := sim.WorldToTile(0, 0)
	objs := map[sim.VillageObjectID]*sim.VillageObject{
		"two": loiterObj("two", "Two", 2, 0), // Chebyshev 2 from actor
	}
	// Attribution radius (1 tile) must NOT reach a 2-tile pin...
	if id, ok := sim.ResolveLoiteringObject(objs, loiterAssets, actor, sim.LoiterAttributionTiles); ok {
		t.Errorf("dist-2 pin must be out of the 1-tile attribution radius, got %q", id)
	}
	// ...but the audience radius (2 tiles) reaches it.
	if id, ok := sim.ResolveLoiteringObject(objs, loiterAssets, actor, sim.AudienceScopeTiles); !ok || id != "two" {
		t.Errorf("dist-2 pin must be in the 2-tile audience radius, got id=%q ok=%v", id, ok)
	}
}

func TestResolveLoiteringObject_SkipsUnnamed(t *testing.T) {
	actor := sim.WorldToTile(0, 0)
	objs := map[sim.VillageObjectID]*sim.VillageObject{
		"decor": loiterObj("decor", "", 0, 0), // unnamed → decorative, excluded
	}
	if id, ok := sim.ResolveLoiteringObject(objs, loiterAssets, actor, sim.AudienceScopeTiles); ok {
		t.Errorf("unnamed placement must be excluded, got %q", id)
	}
}

func TestResolveLoiteringObject_SkipsDanglingAsset(t *testing.T) {
	actor := sim.WorldToTile(0, 0)
	o := loiterObj("ghost", "Ghost", 0, 0)
	o.AssetID = "missing" // not in the catalog
	objs := map[sim.VillageObjectID]*sim.VillageObject{"ghost": o}
	if id, ok := sim.ResolveLoiteringObject(objs, loiterAssets, actor, sim.AudienceScopeTiles); ok {
		t.Errorf("object with an unresolvable asset must be skipped, got %q", id)
	}
}

func TestResolveLoiteringObject_TieBreaksByID(t *testing.T) {
	actor := sim.WorldToTile(0, 0)
	objs := map[sim.VillageObjectID]*sim.VillageObject{
		"bbb": loiterObj("bbb", "B", 0, 0), // both pin on the actor (dist 0)
		"aaa": loiterObj("aaa", "A", 0, 0),
	}
	// Run repeatedly — map iteration is randomized; the result must be stable.
	for i := 0; i < 25; i++ {
		id, ok := sim.ResolveLoiteringObject(objs, loiterAssets, actor, sim.AudienceScopeTiles)
		if !ok || id != "aaa" {
			t.Fatalf("tie must resolve to the smallest id 'aaa', got %q", id)
		}
	}
}

func TestResolveLoiteringObject_NoneInRange(t *testing.T) {
	actor := sim.WorldToTile(0, 0)
	objs := map[sim.VillageObjectID]*sim.VillageObject{
		"far": loiterObj("far", "Far", 9, 9),
	}
	if id, ok := sim.ResolveLoiteringObject(objs, loiterAssets, actor, sim.AudienceScopeTiles); ok {
		t.Errorf("nothing within range must yield ok=false, got %q", id)
	}
}
