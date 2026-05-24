package sim_test

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// intp returns a pointer to i — for the *int offset fields on
// VillageObject (LoiterOffset) and Asset (DoorOffset).
func intp(i int) *int { return &i }

// anchorTile is the tile a VillageObject placed at world (320, 320)
// occupies: WorldToTile floors 320/32 = 10 onto the pad origin.
var anchorTile = sim.Position{X: sim.PadX + 10, Y: sim.PadY + 10}

// TestComputeLoiterTile covers the pure resolution order: per-instance
// loiter offset > asset door offset (+1 south) > footprint-bottom + 2.
func TestComputeLoiterTile(t *testing.T) {
	cases := []struct {
		name  string
		vobj  *sim.VillageObject
		asset *sim.Asset
		want  sim.Position
	}{
		{
			name: "per-instance loiter offset wins over door offset",
			vobj: &sim.VillageObject{
				Pos:           sim.WorldPos{X: 320, Y: 320},
				LoiterOffsetX: intp(2), LoiterOffsetY: intp(-3),
			},
			asset: &sim.Asset{
				DoorOffsetX: intp(1), DoorOffsetY: intp(4),
				FootprintBottom: 3,
			},
			want: sim.Position{X: anchorTile.X + 2, Y: anchorTile.Y - 3},
		},
		{
			name: "door offset, one tile south of the door",
			vobj: &sim.VillageObject{Pos: sim.WorldPos{X: 320, Y: 320}},
			asset: &sim.Asset{
				DoorOffsetX: intp(1), DoorOffsetY: intp(4),
				FootprintBottom: 3,
			},
			want: sim.Position{X: anchorTile.X + 1, Y: anchorTile.Y + 4 + 1},
		},
		{
			name:  "footprint-bottom + 2 fallback",
			vobj:  &sim.VillageObject{Pos: sim.WorldPos{X: 320, Y: 320}},
			asset: &sim.Asset{FootprintBottom: 3},
			want:  sim.Position{X: anchorTile.X, Y: anchorTile.Y + 3 + 2},
		},
		{
			name:  "only one loiter axis set falls through to door offset",
			vobj:  &sim.VillageObject{Pos: sim.WorldPos{X: 320, Y: 320}, LoiterOffsetX: intp(2)},
			asset: &sim.Asset{DoorOffsetX: intp(1), DoorOffsetY: intp(4)},
			want:  sim.Position{X: anchorTile.X + 1, Y: anchorTile.Y + 4 + 1},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sim.ComputeLoiterTile(c.vobj, c.asset)
			if got != c.want {
				t.Errorf("ComputeLoiterTile = %+v, want %+v", got, c.want)
			}
		})
	}
}

// anchorWorld builds a running world seeded with one terrain blob plus
// whatever the caller seeds via the returned handles. Caller seeds, then
// calls load(); load returns the running world and a cancel func.
type anchorWorld struct {
	repo    sim.Repository
	handles *mem.Handles
}

func newAnchorWorld() *anchorWorld {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	return &anchorWorld{repo: repo, handles: handles}
}

func (aw *anchorWorld) load(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	w, err := sim.LoadWorld(context.Background(), aw.repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// houseAsset is a non-obstacle structure asset — keeps the tiles around
// the loiter pin walkable on an all-grass map so visitor-slot tests can
// reason about occupancy without fighting the obstacle/overhang stamping.
func houseAsset() *sim.Asset {
	return &sim.Asset{ID: "house", Category: "structure"}
}

// TestVillageObjectForStructure covers the shared-identity bridge: ok is
// false when the structure has no same-UUID VillageObject, or when that
// VillageObject's asset is missing from the catalog.
func TestVillageObjectForStructure(t *testing.T) {
	aw := newAnchorWorld()
	aw.handles.Assets.Seed(map[sim.AssetID]*sim.Asset{"house": houseAsset()})
	aw.handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"with-asset":    {ID: "with-asset", AssetID: "house", Pos: sim.WorldPos{X: 320, Y: 320}},
		"missing-asset": {ID: "missing-asset", AssetID: "no-such-asset", Pos: sim.WorldPos{X: 320, Y: 320}},
	})
	aw.handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"with-asset":    {ID: "with-asset", DisplayName: "Has Placement"},
		"missing-asset": {ID: "missing-asset", DisplayName: "Bad Asset Ref"},
		"no-placement":  {ID: "no-placement", DisplayName: "No VillageObject"},
	})
	w, cancel := aw.load(t)
	defer cancel()

	type result struct {
		ok      bool
		assetID sim.AssetID
	}
	check := func(id sim.StructureID) result {
		res, _ := w.Send(sim.Command{
			Fn: func(world *sim.World) (any, error) {
				vobj, asset, ok := sim.VillageObjectForStructure(world, id)
				r := result{ok: ok}
				if ok {
					r.assetID = asset.ID
					if sim.VillageObjectID(id) != vobj.ID {
						t.Errorf("bridge returned vobj %q for structure %q", vobj.ID, id)
					}
				}
				return r, nil
			},
		})
		return res.(result)
	}

	if r := check("with-asset"); !r.ok || r.assetID != "house" {
		t.Errorf("with-asset: got ok=%v asset=%q, want ok=true asset=house", r.ok, r.assetID)
	}
	if r := check("missing-asset"); r.ok {
		t.Error("missing-asset: expected ok=false (asset not in catalog)")
	}
	if r := check("no-placement"); r.ok {
		t.Error("no-placement: expected ok=false (no same-UUID VillageObject)")
	}
}

// TestEffectiveLoiterTile covers the World-level wrapper: it crosses the
// bridge and applies computeLoiterTile, returning ok=false when the
// structure has no placement.
func TestEffectiveLoiterTile(t *testing.T) {
	aw := newAnchorWorld()
	aw.handles.Assets.Seed(map[sim.AssetID]*sim.Asset{"house": houseAsset()})
	aw.handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"inn": {ID: "inn", AssetID: "house", Pos: sim.WorldPos{X: 320, Y: 320},
			LoiterOffsetX: intp(0), LoiterOffsetY: intp(5)},
	})
	aw.handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"inn":          {ID: "inn", DisplayName: "Inn"},
		"no-placement": {ID: "no-placement", DisplayName: "No VillageObject"},
	})
	w, cancel := aw.load(t)
	defer cancel()

	type result struct {
		pos sim.Position
		ok  bool
	}
	call := func(id sim.StructureID) result {
		res, _ := w.Send(sim.Command{
			Fn: func(world *sim.World) (any, error) {
				p, ok := sim.EffectiveLoiterTile(world, id)
				return result{pos: p, ok: ok}, nil
			},
		})
		return res.(result)
	}

	want := sim.Position{X: anchorTile.X, Y: anchorTile.Y + 5}
	if r := call("inn"); !r.ok || r.pos != want {
		t.Errorf("inn: got %+v ok=%v, want %+v ok=true", r.pos, r.ok, want)
	}
	if r := call("no-placement"); r.ok {
		t.Error("no-placement: expected ok=false")
	}
}

// seedSlotWorld seeds a world with one structure ("inn") whose loiter pin
// sits at anchorTile + (0, 5), plus the supplied actors. Returns the
// running world and the resolved pin position.
func seedSlotWorld(t *testing.T, actors map[sim.ActorID]*sim.Actor) (*sim.World, context.CancelFunc, sim.Position) {
	t.Helper()
	aw := newAnchorWorld()
	aw.handles.Assets.Seed(map[sim.AssetID]*sim.Asset{"house": houseAsset()})
	aw.handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"inn": {ID: "inn", AssetID: "house", Pos: sim.WorldPos{X: 320, Y: 320},
			LoiterOffsetX: intp(0), LoiterOffsetY: intp(5)},
	})
	aw.handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"inn": {ID: "inn", DisplayName: "Inn"},
	})
	if actors != nil {
		aw.handles.Actors.Seed(actors)
	}
	w, cancel := aw.load(t)
	pin := sim.Position{X: anchorTile.X, Y: anchorTile.Y + 5}
	return w, cancel, pin
}

// pickSlot resolves a visitor slot for actorID against structure "inn",
// building a fresh WalkGrid inside the command.
func pickSlot(t *testing.T, w *sim.World, actorID sim.ActorID) (sim.Position, bool) {
	t.Helper()
	type result struct {
		pos sim.Position
		ok  bool
	}
	res, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			grid, gerr := sim.BuildWalkGrid(world)
			if gerr != nil {
				return nil, gerr
			}
			p, ok := sim.PickVisitorSlot(world, "inn", world.Actors[actorID], grid)
			return result{pos: p, ok: ok}, nil
		},
	})
	if err != nil {
		t.Fatalf("pickSlot command: %v", err)
	}
	r := res.(result)
	return r.pos, r.ok
}

// slotOffsetIndex returns the index of slot (relative to pin) within
// VisitorSlotOffsets, or -1 if it is not one of the eight ring tiles.
func slotOffsetIndex(pin, slot sim.Position) int {
	off := sim.Position{X: slot.X - pin.X, Y: slot.Y - pin.Y}
	for i, o := range sim.VisitorSlotOffsets {
		if o == off {
			return i
		}
	}
	return -1
}

// TestPickVisitorSlotDeterministic covers per-actor stability: the same
// actor resolving against the same structure always lands on the same
// slot (so a per-tick re-resolve doesn't thrash), and the slot is one of
// the eight ring tiles.
func TestPickVisitorSlotDeterministic(t *testing.T) {
	w, cancel, pin := seedSlotWorld(t, map[sim.ActorID]*sim.Actor{
		"actor-a": {ID: "actor-a", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
	})
	defer cancel()

	first, ok := pickSlot(t, w, "actor-a")
	if !ok {
		t.Fatal("pickSlot(actor-a): ok=false")
	}
	if idx := slotOffsetIndex(pin, first); idx < 0 {
		t.Fatalf("resolved slot %+v is not a ring tile around pin %+v", first, pin)
	}
	for i := 0; i < 5; i++ {
		again, ok := pickSlot(t, w, "actor-a")
		if !ok || again != first {
			t.Fatalf("re-resolve %d: got %+v ok=%v, want %+v ok=true", i, again, ok, first)
		}
	}
}

// TestPickVisitorSlotFirstFree covers occupancy fallthrough: when the
// actor's hash-derived start slot is taken by another actor, the scan
// advances to the next slot in ring order.
func TestPickVisitorSlotFirstFree(t *testing.T) {
	// First resolve actor-a's slot with nothing blocked.
	w, cancel, pin := seedSlotWorld(t, map[sim.ActorID]*sim.Actor{
		"actor-a": {ID: "actor-a", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
	})
	start, ok := pickSlot(t, w, "actor-a")
	if !ok {
		t.Fatal("baseline pickSlot: ok=false")
	}
	cancel()
	startIdx := slotOffsetIndex(pin, start)
	if startIdx < 0 {
		t.Fatalf("baseline slot %+v not a ring tile", start)
	}

	// Re-seed with a blocker actor sitting on actor-a's start slot.
	wantIdx := (startIdx + 1) % len(sim.VisitorSlotOffsets)
	wantOff := sim.VisitorSlotOffsets[wantIdx]
	want := sim.Position{X: pin.X + wantOff.X, Y: pin.Y + wantOff.Y}

	w2, cancel2, _ := seedSlotWorld(t, map[sim.ActorID]*sim.Actor{
		"actor-a": {ID: "actor-a", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
		"blocker": {ID: "blocker", Pos: sim.TilePos{X: start.X, Y: start.Y}},
	})
	defer cancel2()

	got, ok := pickSlot(t, w2, "actor-a")
	if !ok {
		t.Fatal("pickSlot with blocked start: ok=false")
	}
	if got != want {
		t.Errorf("blocked-start slot = %+v, want %+v (next in ring after index %d)", got, want, startIdx)
	}
}

// TestPickVisitorSlotAllBlocked covers the fallback: when every one of
// the eight visitor slots is occupied, pickVisitorSlot returns the loiter
// pin tile itself (ok stays true).
func TestPickVisitorSlotAllBlocked(t *testing.T) {
	pin := sim.Position{X: anchorTile.X, Y: anchorTile.Y + 5}
	actors := map[sim.ActorID]*sim.Actor{
		"actor-a": {ID: "actor-a", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
	}
	for i, off := range sim.VisitorSlotOffsets {
		id := sim.ActorID("blocker-" + string(rune('0'+i)))
		actors[id] = &sim.Actor{ID: id, Pos: sim.TilePos{X: pin.X + off.X, Y: pin.Y + off.Y}}
	}
	w, cancel, _ := seedSlotWorld(t, actors)
	defer cancel()

	got, ok := pickSlot(t, w, "actor-a")
	if !ok {
		t.Fatal("all-blocked pickSlot: ok=false, want ok=true with pin fallback")
	}
	if got != pin {
		t.Errorf("all-blocked slot = %+v, want loiter pin %+v", got, pin)
	}
}

// TestPickVisitorSlotPinAlsoBlocked covers the hardened fallback: when
// every one of the eight visitor slots AND the loiter pin tile are
// occupied, pickVisitorSlot returns ok=false rather than handing back an
// unusable pin tile (which would be accepted and then soft-block
// forever at the final step).
func TestPickVisitorSlotPinAlsoBlocked(t *testing.T) {
	pin := sim.Position{X: anchorTile.X, Y: anchorTile.Y + 5}
	actors := map[sim.ActorID]*sim.Actor{
		"actor-a": {ID: "actor-a", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
		// A blocker standing on the loiter pin itself.
		"pin-sitter": {ID: "pin-sitter", Pos: sim.TilePos{X: pin.X, Y: pin.Y}},
	}
	for i, off := range sim.VisitorSlotOffsets {
		id := sim.ActorID("blocker-" + string(rune('0'+i)))
		actors[id] = &sim.Actor{ID: id, Pos: sim.TilePos{X: pin.X + off.X, Y: pin.Y + off.Y}}
	}
	w, cancel, _ := seedSlotWorld(t, actors)
	defer cancel()

	if _, ok := pickSlot(t, w, "actor-a"); ok {
		t.Error("pin-also-blocked pickSlot: expected ok=false (no stand-able tile)")
	}
}

// TestPickVisitorSlotNilActor covers the nil-actor guard — the exported
// helper makes misuse easy, so a nil actor must return ok=false rather
// than panicking on actor.ID.
func TestPickVisitorSlotNilActor(t *testing.T) {
	w, cancel, _ := seedSlotWorld(t, nil)
	defer cancel()

	res, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			grid, gerr := sim.BuildWalkGrid(world)
			if gerr != nil {
				return nil, gerr
			}
			_, ok := sim.PickVisitorSlot(world, "inn", nil, grid)
			return ok, nil
		},
	})
	if res.(bool) {
		t.Error("nil actor: expected ok=false")
	}
}

// TestPickVisitorSlotNoPlacement covers ok=false when the structure has
// no VillageObject placement to anchor against.
func TestPickVisitorSlotNoPlacement(t *testing.T) {
	aw := newAnchorWorld()
	aw.handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"ghost": {ID: "ghost", DisplayName: "No Placement"},
	})
	aw.handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"actor-a": {ID: "actor-a", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
	})
	w, cancel := aw.load(t)
	defer cancel()

	type result struct {
		pos sim.Position
		ok  bool
	}
	res, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			grid, gerr := sim.BuildWalkGrid(world)
			if gerr != nil {
				return nil, gerr
			}
			p, ok := sim.PickVisitorSlot(world, "ghost", world.Actors["actor-a"], grid)
			return result{pos: p, ok: ok}, nil
		},
	})
	if res.(result).ok {
		t.Error("ghost structure: expected ok=false")
	}
}

// TestTileOccupiedByOtherActor covers the occupancy predicate, including
// that the excepted actor does not count as occupying its own tile.
func TestTileOccupiedByOtherActor(t *testing.T) {
	aw := newAnchorWorld()
	aw.handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"a": {ID: "a", Pos: sim.TilePos{X: sim.PadX + 1, Y: sim.PadY + 1}},
		"b": {ID: "b", Pos: sim.TilePos{X: sim.PadX + 2, Y: sim.PadY + 2}},
	})
	w, cancel := aw.load(t)
	defer cancel()

	type query struct {
		pos    sim.Position
		except sim.ActorID
	}
	check := func(q query) bool {
		res, _ := w.Send(sim.Command{
			Fn: func(world *sim.World) (any, error) {
				return sim.TileOccupiedByOtherActor(world, q.pos, q.except), nil
			},
		})
		return res.(bool)
	}

	bTile := sim.Position{X: sim.PadX + 2, Y: sim.PadY + 2}
	if !check(query{pos: bTile, except: "a"}) {
		t.Error("b's tile should read occupied when excepting a")
	}
	if check(query{pos: bTile, except: "b"}) {
		t.Error("b's tile should NOT read occupied when excepting b itself")
	}
	empty := sim.Position{X: sim.PadX + 9, Y: sim.PadY + 9}
	if check(query{pos: empty, except: "a"}) {
		t.Error("empty tile should read unoccupied")
	}
}
