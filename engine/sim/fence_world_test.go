package sim_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// fenceTestAssetID is the fence asset every fence test places.
const fenceTestAssetID sim.AssetID = "ranch-fence"

// fenceTestAsset mirrors the LLM-637 catalog row: one asset, one state per
// piece, the role carried by the TAG (a cell that serves two roles carries
// both). IDs are distinct so StateForTag is deterministic.
func fenceTestAsset() *sim.Asset {
	return &sim.Asset{
		ID: fenceTestAssetID, Name: "Ranch Fence", Category: "fence", DefaultState: "h",
		AnchorX: 0.5, AnchorY: 0.85, IsObstacle: true,
		States: []sim.AssetState{
			{ID: 1, State: "corner-tl", Tags: []string{sim.TagFenceCornerTL}},
			{ID: 2, State: "h", Tags: []string{sim.TagFenceH}},
			{ID: 3, State: "corner-tr", Tags: []string{sim.TagFenceCornerTR}},
			{ID: 4, State: "v-top", Tags: []string{sim.TagFenceVTop}},
			{ID: 5, State: "v", Tags: []string{sim.TagFenceV}},
			{ID: 6, State: "v-bottom", Tags: []string{sim.TagFenceVBottom, sim.TagFencePost}},
			{ID: 7, State: "corner-bl", Tags: []string{sim.TagFenceCornerBL, sim.TagFenceEndLeft}},
			{ID: 8, State: "corner-br", Tags: []string{sim.TagFenceCornerBR, sim.TagFenceEndRight}},
		},
	}
}

// fenceTestHouseAsset is a 3x3-footprint obstacle to fence against.
func fenceTestHouseAsset() *sim.Asset {
	return &sim.Asset{
		ID: "house", Name: "House", Category: "structure", DefaultState: "default",
		IsObstacle: true, FootprintLeft: 1, FootprintRight: 1, FootprintTop: 1, FootprintBottom: 1,
		States: []sim.AssetState{{ID: 9, State: "default"}},
	}
}

// picketTestAsset is a SECOND fence asset — different art, so it never joins
// the ranch fence: a picket segment under a ranch run is an ordinary obstacle.
func picketTestAsset() *sim.Asset {
	a := fenceTestAsset()
	a.ID = "picket"
	a.Name = "Picket Fence"
	return a
}

// buildFenceWorld seeds all-grass terrain (with optional water tiles) plus the
// fence and house assets, and runs the world goroutine.
func buildFenceWorld(t *testing.T, water ...sim.TilePos) *sim.World {
	t.Helper()
	data := make([]byte, sim.MapW*sim.MapH)
	for i := range data {
		data[i] = sim.TerrainLightGrass
	}
	for _, tp := range water {
		data[tp.Y*sim.MapW+tp.X] = sim.TerrainDeepWater
	}
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(&sim.Terrain{Data: data})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		fenceTestAssetID: fenceTestAsset(),
		"house":          fenceTestHouseAsset(),
		"picket":         picketTestAsset(),
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)
	return w
}

// tilePx is the world-pixel centre of a padded tile — a click on that tile.
func tilePx(t sim.TilePos) (float64, float64) {
	c := t.Center()
	return c.X, c.Y
}

// placedFence is a race-free snapshot of a placed segment.
type placedFence struct {
	tile  sim.TilePos
	state string
	tags  []string
}

func snapFenceRun(t *testing.T, w *sim.World, ids []sim.VillageObjectID) []placedFence {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		out := make([]placedFence, 0, len(ids))
		for _, id := range ids {
			obj := world.VillageObjects[id]
			if obj == nil {
				t.Errorf("segment %s missing from world", id)
				continue
			}
			out = append(out, placedFence{tile: obj.Pos.Tile(), state: obj.CurrentState, tags: append([]string(nil), obj.Tags...)})
		}
		return out, nil
	}})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return res.([]placedFence)
}

func objectIDs(objs []*sim.VillageObject) []sim.VillageObjectID {
	ids := make([]sim.VillageObjectID, 0, len(objs))
	for _, o := range objs {
		ids = append(ids, o.ID)
	}
	return ids
}

func countObjects(t *testing.T, w *sim.World) int {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) { return len(world.VillageObjects), nil }})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return res.(int)
}

// TestPlaceFenceRun_RingPlacesTaggedSegmentsAndSealsThePen: a 4x3 drag mints
// ten segments with the right state per tile and one shared run tag, announces
// each with created + tags-updated, and the interior is unreachable from
// outside afterwards — the pen is sealed with no mechanism beyond the obstacle
// stamp.
func TestPlaceFenceRun_RingPlacesTaggedSegmentsAndSealsThePen(t *testing.T) {
	w := buildFenceWorld(t)
	var created, tagged int
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			switch evt.(type) {
			case *sim.VillageObjectCreated:
				created++
			case *sim.VillageObjectTagsUpdated:
				tagged++
			}
		}))
		return nil, nil
	}})

	a := sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 10}
	b := sim.TilePos{X: sim.PadX + 13, Y: sim.PadY + 12}
	ax, ay := tilePx(a)
	bx, by := tilePx(b)
	res, err := w.Send(sim.PlaceFenceRun(fenceTestAssetID, bx, by, ax, ay, "tester"))
	if err != nil {
		t.Fatalf("sim.PlaceFenceRun: %v", err)
	}
	out := res.(sim.PlaceFenceRunResult)
	if out.RunID == "" || len(out.Objects) != 10 {
		t.Fatalf("result = run %q, %d objects; want a run id and 10 segments", out.RunID, len(out.Objects))
	}
	if created != 10 || tagged != 10 {
		t.Errorf("events: created=%d tagged=%d, want 10 and 10", created, tagged)
	}

	got := snapFenceRun(t, w, objectIDs(out.Objects))
	wantStates := map[sim.TilePos]string{
		{sim.PadX + 10, sim.PadY + 10}: "corner-tl", {sim.PadX + 11, sim.PadY + 10}: "h", {sim.PadX + 12, sim.PadY + 10}: "h", {sim.PadX + 13, sim.PadY + 10}: "corner-tr",
		{sim.PadX + 10, sim.PadY + 11}: "v", {sim.PadX + 13, sim.PadY + 11}: "v",
		{sim.PadX + 10, sim.PadY + 12}: "corner-bl", {sim.PadX + 11, sim.PadY + 12}: "h", {sim.PadX + 12, sim.PadY + 12}: "h", {sim.PadX + 13, sim.PadY + 12}: "corner-br",
	}
	runTag := sim.FenceRunTagPrefix + out.RunID
	for _, pf := range got {
		if want, ok := wantStates[pf.tile]; !ok || want != pf.state {
			t.Errorf("tile %v state %q, want %q", pf.tile, pf.state, want)
		}
		if !reflect.DeepEqual(pf.tags, []string{runTag}) {
			t.Errorf("tile %v tags %v, want [%s]", pf.tile, pf.tags, runTag)
		}
	}

	// The interior (11..12, 11) is walled off from the rest of the map.
	reach, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		grid, err := sim.BuildWalkGrid(world)
		if err != nil {
			return nil, err
		}
		inside := sim.TilePos{X: sim.PadX + 11, Y: sim.PadY + 11}
		outside := sim.TilePos{X: sim.PadX + 20, Y: sim.PadY + 20}
		return map[string]bool{
			"inside walkable": grid.CanWalk(inside.X, inside.Y),
			"fence blocked":   !grid.CanWalk(sim.PadX+10, sim.PadY+10),
			"path exists":     sim.FindPath(grid, outside, inside) != nil,
		}, nil
	}})
	if err != nil {
		t.Fatalf("reach: %v", err)
	}
	r := reach.(map[string]bool)
	if !r["inside walkable"] || !r["fence blocked"] {
		t.Fatalf("grid shape wrong: %v", r)
	}
	if r["path exists"] {
		t.Errorf("a path from outside reached the pen interior — the ring is not sealed")
	}
}

// TestPlaceFenceRun_LineAndPost cover the non-ring vocabularies end to end.
func TestPlaceFenceRun_LineAndPost(t *testing.T) {
	w := buildFenceWorld(t)
	ax, ay := tilePx(sim.TilePos{X: sim.PadX + 5, Y: sim.PadY + 5})
	bx, by := tilePx(sim.TilePos{X: sim.PadX + 7, Y: sim.PadY + 5})
	res, err := w.Send(sim.PlaceFenceRun(fenceTestAssetID, ax, ay, bx, by, "tester"))
	if err != nil {
		t.Fatalf("line: %v", err)
	}
	line := snapFenceRun(t, w, objectIDs(res.(sim.PlaceFenceRunResult).Objects))
	var states []string
	for _, pf := range line {
		states = append(states, pf.state)
	}
	if want := []string{"corner-bl", "h", "corner-br"}; !reflect.DeepEqual(states, want) {
		t.Errorf("line states = %v, want %v (end-left / h / end-right cells)", states, want)
	}

	px, py := tilePx(sim.TilePos{X: sim.PadX + 9, Y: sim.PadY + 9})
	res, err = w.Send(sim.PlaceFenceRun(fenceTestAssetID, px, py, px, py, "tester"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	post := snapFenceRun(t, w, objectIDs(res.(sim.PlaceFenceRunResult).Objects))
	if len(post) != 1 || post[0].state != "v-bottom" {
		t.Errorf("post = %+v, want one v-bottom segment (the fence-post cell)", post)
	}
}

// TestPlaceFenceRun_BlockedTilePlacesNothing: a ring that crosses water, a
// building footprint, or an existing fence is refused naming the tile, and the
// world is untouched — no partial run.
func TestPlaceFenceRun_BlockedTilePlacesNothing(t *testing.T) {
	water := sim.TilePos{X: sim.PadX + 12, Y: sim.PadY + 10}
	w := buildFenceWorld(t, water)
	// A house whose footprint covers (20..22, 20..22).
	hx, hy := tilePx(sim.TilePos{X: sim.PadX + 21, Y: sim.PadY + 21})
	if _, err := w.Send(sim.CreateVillageObject("house", hx, hy, "", "tester")); err != nil {
		t.Fatalf("house: %v", err)
	}
	// A PICKET fence line along y=30, x=30..33 — another asset's fence is just
	// an obstacle to a ranch run (same-asset segments are shared instead; see
	// TestPlaceFenceRun_ConnectsToExistingRun).
	fx1, fy1 := tilePx(sim.TilePos{X: sim.PadX + 30, Y: sim.PadY + 30})
	fx2, fy2 := tilePx(sim.TilePos{X: sim.PadX + 33, Y: sim.PadY + 30})
	if _, err := w.Send(sim.PlaceFenceRun("picket", fx1, fy1, fx2, fy2, "tester")); err != nil {
		t.Fatalf("picket fence: %v", err)
	}
	before := countObjects(t, w)

	cases := []struct {
		name      string
		a, b      sim.TilePos
		wantTile  sim.TilePos
		blockedBy string
	}{
		{"water on the top edge", sim.TilePos{sim.PadX + 10, sim.PadY + 10}, sim.TilePos{sim.PadX + 13, sim.PadY + 12}, water, "water"},
		{"house footprint under the left edge", sim.TilePos{sim.PadX + 20, sim.PadY + 18}, sim.TilePos{sim.PadX + 25, sim.PadY + 24}, sim.TilePos{sim.PadX + 20, sim.PadY + 20}, "footprint"},
		{"another fence asset under the run", sim.TilePos{sim.PadX + 28, sim.PadY + 30}, sim.TilePos{sim.PadX + 35, sim.PadY + 30}, sim.TilePos{sim.PadX + 30, sim.PadY + 30}, "fence"},
		// An off-grid corner has no tile to name: the error carries (-1,-1) and the
		// clamped pixel position instead.
		{"off the map", sim.TilePos{-2, sim.PadY + 5}, sim.TilePos{sim.PadX + 2, sim.PadY + 5}, sim.TilePos{-1, -1}, "bounds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ax, ay := tilePx(tc.a)
			bx, by := tilePx(tc.b)
			_, err := w.Send(sim.PlaceFenceRun(fenceTestAssetID, ax, ay, bx, by, "tester"))
			if !errors.Is(err, sim.ErrFenceTileBlocked) {
				t.Fatalf("err = %v, want sim.ErrFenceTileBlocked (%s)", err, tc.blockedBy)
			}
			var blocked *sim.FenceTileBlocked
			if !errors.As(err, &blocked) || blocked.Tile != tc.wantTile {
				t.Errorf("blocked tile = %+v, want %v", blocked, tc.wantTile)
			}
			if got := countObjects(t, w); got != before {
				t.Errorf("objects after refused run = %d, want %d (nothing placed)", got, before)
			}
		})
	}
}

// TestPlaceFenceRun_Guards: unknown asset, an asset without the piece tags, the
// size cap, and non-finite coordinates are all refused before any mutation.
func TestPlaceFenceRun_Guards(t *testing.T) {
	w := buildFenceWorld(t)
	before := countObjects(t, w)
	ax, ay := tilePx(sim.TilePos{X: sim.PadX + 5, Y: sim.PadY + 5})
	bx, by := tilePx(sim.TilePos{X: sim.PadX + 8, Y: sim.PadY + 8})

	if _, err := w.Send(sim.PlaceFenceRun("nope", ax, ay, bx, by, "tester")); !errors.Is(err, sim.ErrUnknownAsset) {
		t.Errorf("unknown asset err = %v", err)
	}
	// The house carries no fence tags.
	if _, err := w.Send(sim.PlaceFenceRun("house", ax, ay, bx, by, "tester")); !errors.Is(err, sim.ErrFenceAssetUnsupported) {
		t.Errorf("untagged asset err = %v, want sim.ErrFenceAssetUnsupported", err)
	}
	// Fully tagged but passable: looks like a fence, would not seal a pen.
	passable := fenceTestAsset()
	passable.ID = "passable-fence"
	passable.IsObstacle = false
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) { world.Assets["passable-fence"] = passable; return nil, nil }})
	if _, err := w.Send(sim.PlaceFenceRun("passable-fence", ax, ay, bx, by, "tester")); !errors.Is(err, sim.ErrFenceAssetUnsupported) {
		t.Errorf("non-obstacle asset err = %v, want ErrFenceAssetUnsupported", err)
	}
	// Corners far off the grid are refused as blocked BEFORE any layout, so a
	// huge finite coordinate cannot make the layout allocate or overflow.
	if _, err := w.Send(sim.PlaceFenceRun(fenceTestAssetID, ax, ay, 1e15, 1e15, "tester")); !errors.Is(err, sim.ErrFenceTileBlocked) {
		t.Errorf("far corner err = %v, want ErrFenceTileBlocked", err)
	}
	// Beyond the int range the float→int conversion is implementation-specific,
	// so the pixel range is checked before any conversion.
	if _, err := w.Send(sim.PlaceFenceRun(fenceTestAssetID, ax, ay, math.MaxFloat64, -math.MaxFloat64, "tester")); !errors.Is(err, sim.ErrFenceTileBlocked) {
		t.Errorf("MaxFloat64 corner err = %v, want ErrFenceTileBlocked", err)
	}
	// A ring over nearly the whole grid is 752 segments > cap (and every tile
	// of it is in bounds, so the cap — not the bounds check — is what refuses).
	cx, cy := tilePx(sim.TilePos{X: 1, Y: 1})
	dx, dy := tilePx(sim.TilePos{X: sim.MapW - 1, Y: sim.MapH - 1})
	if _, err := w.Send(sim.PlaceFenceRun(fenceTestAssetID, cx, cy, dx, dy, "tester")); !errors.Is(err, sim.ErrFenceRunTooLarge) {
		t.Errorf("oversized run err = %v, want sim.ErrFenceRunTooLarge", err)
	}
	nan := 0.0
	nan = nan / nan
	if _, err := w.Send(sim.PlaceFenceRun(fenceTestAssetID, nan, ay, bx, by, "tester")); !errors.Is(err, sim.ErrInvalidObjectPosition) {
		t.Errorf("NaN err = %v, want sim.ErrInvalidObjectPosition", err)
	}
	if got := countObjects(t, w); got != before {
		t.Errorf("objects after refused runs = %d, want %d", got, before)
	}
}

// TestDeleteFenceRun_RemovesExactlyTheRun: two runs side by side; deleting one
// removes all of its segments and none of the other's, emits one deleted event
// per segment, and the tiles are walkable again. A second delete of the same
// run is sim.ErrFenceRunNotFound.
func TestDeleteFenceRun_RemovesExactlyTheRun(t *testing.T) {
	w := buildFenceWorld(t)
	ax, ay := tilePx(sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 10})
	bx, by := tilePx(sim.TilePos{X: sim.PadX + 13, Y: sim.PadY + 12})
	r1, err := w.Send(sim.PlaceFenceRun(fenceTestAssetID, ax, ay, bx, by, "tester"))
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	cx, cy := tilePx(sim.TilePos{X: sim.PadX + 20, Y: sim.PadY + 10})
	dx, dy := tilePx(sim.TilePos{X: sim.PadX + 23, Y: sim.PadY + 12})
	r2, err := w.Send(sim.PlaceFenceRun(fenceTestAssetID, cx, cy, dx, dy, "tester"))
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	run1 := r1.(sim.PlaceFenceRunResult)
	run2 := r2.(sim.PlaceFenceRunResult)

	var deleted int
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			if _, ok := evt.(*sim.VillageObjectDeleted); ok {
				deleted++
			}
		}))
		return nil, nil
	}})

	res, err := w.Send(sim.DeleteFenceRun(run1.RunID))
	if err != nil {
		t.Fatalf("sim.DeleteFenceRun: %v", err)
	}
	if got := len(res.(sim.DeleteFenceRunResult).DeletedIDs); got != 10 || deleted != 10 {
		t.Errorf("deleted %d ids / %d events, want 10 and 10", got, deleted)
	}
	left, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		var gone, kept int
		for _, o := range run1.Objects {
			if _, ok := world.VillageObjects[o.ID]; !ok {
				gone++
			}
		}
		for _, o := range run2.Objects {
			if _, ok := world.VillageObjects[o.ID]; ok {
				kept++
			}
		}
		grid, err := sim.BuildWalkGrid(world)
		if err != nil {
			return nil, err
		}
		return []int{gone, kept, map[bool]int{true: 1, false: 0}[grid.CanWalk(sim.PadX+10, sim.PadY+10)]}, nil
	}})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if got := left.([]int); got[0] != 10 || got[1] != 10 || got[2] != 1 {
		t.Errorf("run1 gone=%d (want 10), run2 kept=%d (want 10), corner walkable=%d (want 1)", got[0], got[1], got[2])
	}

	if _, err := w.Send(sim.DeleteFenceRun(run1.RunID)); !errors.Is(err, sim.ErrFenceRunNotFound) {
		t.Errorf("second delete err = %v, want sim.ErrFenceRunNotFound", err)
	}
	if _, err := w.Send(sim.DeleteFenceRun("")); !errors.Is(err, sim.ErrFenceRunNotFound) {
		t.Errorf("blank id err = %v, want sim.ErrFenceRunNotFound", err)
	}
}

// fenceStates snapshots state + run ids for a set of tiles of the fence asset
// ("" state when no segment stands there).
func fenceStates(t *testing.T, w *sim.World, tiles []sim.TilePos) ([]string, [][]string) {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		byTile := make(map[sim.TilePos]*sim.VillageObject)
		for _, o := range world.VillageObjects {
			if o != nil && o.AssetID == fenceTestAssetID {
				byTile[o.Pos.Tile()] = o
			}
		}
		states := make([]string, len(tiles))
		runs := make([][]string, len(tiles))
		for i, tile := range tiles {
			if o := byTile[tile]; o != nil {
				states[i] = o.CurrentState
				runs[i] = sim.FenceRunIDsOf(o)
			}
		}
		return [2]any{states, runs}, nil
	}})
	if err != nil {
		t.Fatalf("fenceStates: %v", err)
	}
	pair := res.([2]any)
	return pair[0].([]string), pair[1].([][]string)
}

// TestPlaceFenceRun_ConnectsToExistingRun is the LLM-638 ask: draw a horizontal
// line, then draw a vertical line down from its right end. The shared end tile
// is not re-minted, joins the second run, and its piece becomes the corner; the
// new run resolves against it. Deleting the second run releases the junction
// (still standing for run 1, end cap restored); deleting the first removes it.
func TestPlaceFenceRun_ConnectsToExistingRun(t *testing.T) {
	w := buildFenceWorld(t)
	tp := func(x, y int) sim.TilePos { return sim.TilePos{X: sim.PadX + x, Y: sim.PadY + y} }
	ax, ay := tilePx(tp(10, 10))
	bx, by := tilePx(tp(13, 10))
	r1, err := w.Send(sim.PlaceFenceRun(fenceTestAssetID, ax, ay, bx, by, "tester"))
	if err != nil {
		t.Fatalf("line: %v", err)
	}
	run1 := r1.(sim.PlaceFenceRunResult)

	var restateEvents int
	w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			if _, ok := evt.(*sim.VillageObjectStateChanged); ok {
				restateEvents++
			}
		}))
		return nil, nil
	}})

	// Vertical line starting ON the right end of the horizontal one.
	cx, cy := tilePx(tp(13, 10))
	dx, dy := tilePx(tp(13, 13))
	r2, err := w.Send(sim.PlaceFenceRun(fenceTestAssetID, cx, cy, dx, dy, "tester"))
	if err != nil {
		t.Fatalf("attached vertical: %v", err)
	}
	run2 := r2.(sim.PlaceFenceRunResult)
	if len(run2.Objects) != 3 || len(run2.Shared) != 1 {
		t.Fatalf("run2: %d new, %d shared; want 3 and 1", len(run2.Objects), len(run2.Shared))
	}
	if len(run2.Restated) != 1 || restateEvents != 1 {
		t.Errorf("restated = %v (%d events), want exactly the junction once", run2.Restated, restateEvents)
	}

	tiles := []sim.TilePos{tp(10, 10), tp(11, 10), tp(12, 10), tp(13, 10), tp(13, 11), tp(13, 12), tp(13, 13)}
	states, runs := fenceStates(t, w, tiles)
	want := []string{"corner-bl", "h", "h", "corner-tr", "v", "v", "v-bottom"}
	if !reflect.DeepEqual(states, want) {
		t.Errorf("states after attach = %v, want %v", states, want)
	}
	if !reflect.DeepEqual(runs[3], []string{run1.RunID, run2.RunID}) {
		t.Errorf("junction runs = %v, want [run1 run2]", runs[3])
	}
	if !reflect.DeepEqual(runs[4], []string{run2.RunID}) || !reflect.DeepEqual(runs[0], []string{run1.RunID}) {
		t.Errorf("segment runs = %v / %v, want run2-only / run1-only", runs[4], runs[0])
	}

	// Retracing the horizontal line adds nothing.
	if _, err := w.Send(sim.PlaceFenceRun(fenceTestAssetID, ax, ay, bx, by, "tester")); !errors.Is(err, sim.ErrFenceRunNothingNew) {
		t.Errorf("retrace err = %v, want ErrFenceRunNothingNew", err)
	}

	// Delete run 2: the junction is released, not deleted, and its cap returns.
	d2, err := w.Send(sim.DeleteFenceRun(run2.RunID))
	if err != nil {
		t.Fatalf("delete run2: %v", err)
	}
	out2 := d2.(sim.DeleteFenceRunResult)
	if len(out2.DeletedIDs) != 3 || len(out2.ReleasedIDs) != 1 {
		t.Errorf("delete run2: %d deleted, %d released; want 3 and 1", len(out2.DeletedIDs), len(out2.ReleasedIDs))
	}
	states, runs = fenceStates(t, w, tiles)
	want = []string{"corner-bl", "h", "h", "corner-br", "", "", ""}
	if !reflect.DeepEqual(states, want) {
		t.Errorf("states after deleting run2 = %v, want %v", states, want)
	}
	if !reflect.DeepEqual(runs[3], []string{run1.RunID}) {
		t.Errorf("junction runs after release = %v, want [run1]", runs[3])
	}

	// Delete run 1: everything goes, junction included.
	d1, err := w.Send(sim.DeleteFenceRun(run1.RunID))
	if err != nil {
		t.Fatalf("delete run1: %v", err)
	}
	if got := len(d1.(sim.DeleteFenceRunResult).DeletedIDs); got != 4 {
		t.Errorf("delete run1 removed %d, want 4", got)
	}
	states, _ = fenceStates(t, w, tiles)
	if !reflect.DeepEqual(states, make([]string, len(tiles))) {
		t.Errorf("states after deleting run1 = %v, want all empty", states)
	}
}

// TestPlaceFenceRun_PenAttachedToLine: a ring drawn with one side ON an existing
// line shares that whole side; the line's mids become T-junctions (h, the
// straight fallback) and the new corners resolve against it.
func TestPlaceFenceRun_PenAttachedToLine(t *testing.T) {
	w := buildFenceWorld(t)
	tp := func(x, y int) sim.TilePos { return sim.TilePos{X: sim.PadX + x, Y: sim.PadY + y} }
	ax, ay := tilePx(tp(10, 10))
	bx, by := tilePx(tp(16, 10))
	if _, err := w.Send(sim.PlaceFenceRun(fenceTestAssetID, ax, ay, bx, by, "tester")); err != nil {
		t.Fatalf("line: %v", err)
	}
	// Ring 12..14 x 10..12: its top edge lies on the line.
	cx, cy := tilePx(tp(12, 10))
	dx, dy := tilePx(tp(14, 12))
	r, err := w.Send(sim.PlaceFenceRun(fenceTestAssetID, cx, cy, dx, dy, "tester"))
	if err != nil {
		t.Fatalf("pen: %v", err)
	}
	out := r.(sim.PlaceFenceRunResult)
	if len(out.Objects) != 5 || len(out.Shared) != 3 {
		t.Fatalf("pen: %d new, %d shared; want 5 and 3", len(out.Objects), len(out.Shared))
	}
	tiles := []sim.TilePos{tp(11, 10), tp(12, 10), tp(13, 10), tp(14, 10), tp(15, 10), tp(12, 11), tp(14, 11), tp(12, 12), tp(13, 12), tp(14, 12)}
	states, _ := fenceStates(t, w, tiles)
	// (12,10): W+E+S → h; (14,10): W+E+S → h; the line's mids stay h; the pen's
	// sides and bottom are v / corner-bl / h / corner-br.
	want := []string{"h", "h", "h", "h", "h", "v", "v", "corner-bl", "h", "corner-br"}
	if !reflect.DeepEqual(states, want) {
		t.Errorf("states = %v, want %v", states, want)
	}
	// The pen is sealed: no path from outside into (13,11).
	sealed, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		grid, err := sim.BuildWalkGrid(world)
		if err != nil {
			return nil, err
		}
		return sim.FindPath(grid, tp(30, 30), tp(13, 11)) == nil, nil
	}})
	if err != nil || !sealed.(bool) {
		t.Errorf("pen attached to a line is not sealed (err=%v)", err)
	}
}

// TestDeleteVillageObject_RecapsFenceNeighbours: removing one segment from a
// line (opening a gate gap) re-resolves both sides — the tile left of the gap
// becomes a right end cap, the tile right of it a left end cap.
func TestDeleteVillageObject_RecapsFenceNeighbours(t *testing.T) {
	w := buildFenceWorld(t)
	tp := func(x, y int) sim.TilePos { return sim.TilePos{X: sim.PadX + x, Y: sim.PadY + y} }
	ax, ay := tilePx(tp(10, 10))
	bx, by := tilePx(tp(14, 10))
	r, err := w.Send(sim.PlaceFenceRun(fenceTestAssetID, ax, ay, bx, by, "tester"))
	if err != nil {
		t.Fatalf("line: %v", err)
	}
	objs := r.(sim.PlaceFenceRunResult).Objects
	if _, err := w.Send(sim.DeleteVillageObject(objs[2].ID)); err != nil { // (12,10)
		t.Fatalf("delete segment: %v", err)
	}
	tiles := []sim.TilePos{tp(10, 10), tp(11, 10), tp(12, 10), tp(13, 10), tp(14, 10)}
	states, _ := fenceStates(t, w, tiles)
	want := []string{"corner-bl", "corner-br", "", "corner-bl", "corner-br"} // end-left / end-right cells
	if !reflect.DeepEqual(states, want) {
		t.Errorf("states after opening a gap = %v, want %v", states, want)
	}
}
