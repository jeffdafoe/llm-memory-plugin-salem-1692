package sim

import "testing"

// structure_anchors_workpost_test.go — LLM-268. Covers the doorless-stall branch
// of the shared at-post predicate and pins that the exported map-based
// ActorAtWorkpost (perception calls it snapshot-side) agrees with the world-side
// actorAtWorkpost for the same inputs — the lockstep the off-post move_to gate and
// the return-to-post backstop both depend on. Reuses lrWorld / lrActor /
// placeInteriorShop / lrIntp (labor_relocate_internal_test.go).

// placeDoorlessStall seeds a doorless market stall (an asset with NO door offset)
// with a per-instance loiter pin, and returns that pin — the stall's staff post.
func placeDoorlessStall(w *World, id StructureID) TilePos {
	asset := &Asset{ID: AssetID(id) + "-asset"} // no DoorOffset → doorless
	vobj := &VillageObject{
		ID: VillageObjectID(id), AssetID: asset.ID, Pos: WorldPos{X: 320, Y: 320},
		LoiterOffsetX: lrIntp(0), LoiterOffsetY: lrIntp(0),
	}
	w.Assets[asset.ID] = asset
	w.VillageObjects[vobj.ID] = vobj
	w.Structures[id] = &Structure{ID: id, DisplayName: string(id)}
	return computeLoiterTile(vobj, asset)
}

func TestActorAtWorkpost_DoorlessStall(t *testing.T) {
	w := lrWorld()
	pin := placeDoorlessStall(w, "stall")

	// A doorless stall has no interior to enter — its loiter pin IS the post.
	at := lrActor("a", "", "")
	at.Pos = pin
	if !actorAtWorkpost(w, at, "stall") {
		t.Errorf("an actor at a doorless stall's loiter pin should be at the post")
	}
	away := lrActor("b", "", "")
	away.Pos = TilePos{X: pin.X + 5, Y: pin.Y + 5}
	if actorAtWorkpost(w, away, "stall") {
		t.Errorf("an actor far from a doorless stall's pin is not at the post")
	}
}

// TestActorAtWorkpost_MapsMatchesWorld is the lockstep guard: the exported
// map-based ActorAtWorkpost must return the same verdict as the world-side
// actorAtWorkpost for the same actor state, across interior + doorless + empty-post
// cases. A drift here would let the perception off-post gate and the world-side
// return-to-post backstop disagree on "at post" (a wasted wake, or a stripped tool
// on a woken worker).
func TestActorAtWorkpost_MapsMatchesWorld(t *testing.T) {
	w := lrWorld()
	interiorPin := placeInteriorShop(w, "store")
	stallPin := placeDoorlessStall(w, "stall")

	cases := []struct {
		inside StructureID
		pos    TilePos
		post   StructureID
	}{
		{"store", TilePos{}, "store"},        // inside an interior post → at post
		{"", interiorPin, "store"},           // at the interior door loiter → NOT at post
		{"", stallPin, "stall"},              // at a doorless stall pin → at post
		{"", TilePos{X: 99, Y: 99}, "stall"}, // far from the stall → not at post
		{"", TilePos{}, ""},                  // empty post → never at post
	}
	for _, c := range cases {
		a := &Actor{ID: "x", InsideStructureID: c.inside, Pos: c.pos}
		world := actorAtWorkpost(w, a, c.post)
		maps := ActorAtWorkpost(w.VillageObjects, w.Assets, c.inside, c.pos, c.post)
		if world != maps {
			t.Errorf("mismatch for inside=%q pos=%+v post=%q: world=%v maps=%v", c.inside, c.pos, c.post, world, maps)
		}
	}
}
