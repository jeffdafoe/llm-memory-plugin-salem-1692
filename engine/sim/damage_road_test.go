package sim_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// damage_road_test.go — LLM-677: a fallen tree across a road.

// roadTiles is a set of terrain tiles to paint on the test map.
type roadTiles func(x, y int) byte

// northSouthRoad paints a road `width` tiles wide at x0.. running north-south
// from y 20 to 70; everything else is light grass.
func northSouthRoad(x0, width int) roadTiles {
	return func(x, y int) byte {
		if x >= x0 && x < x0+width && y >= 20 && y <= 70 {
			return sim.TerrainDirt
		}
		return sim.TerrainLightGrass
	}
}

// eastWestRoad paints a road `width` tiles tall at y0.. running east-west from
// x 70 to 130.
func eastWestRoad(y0, width int) roadTiles {
	return func(x, y int) byte {
		if y >= y0 && y < y0+width && x >= 70 && x <= 130 {
			return sim.TerrainDirt
		}
		return sim.TerrainLightGrass
	}
}

// buildRoadDamageWorld is buildDamageWorld plus terrain painted by paint, both
// fallen-tree assets as 3x2 obstacles, a named building (the Mill) at
// landmark, and road terms of 10 coins for an hour.
func buildRoadDamageWorld(t *testing.T, paint roadTiles, landmark sim.TilePos) *sim.World {
	t.Helper()
	w, cancel := buildDamageWorld(t)
	t.Cleanup(cancel)
	mustSend(t, w, func(world *sim.World) {
		data := make([]byte, sim.MapW*sim.MapH)
		for y := 0; y < sim.MapH; y++ {
			for x := 0; x < sim.MapW; x++ {
				data[y*sim.MapW+x] = paint(x, y)
			}
		}
		world.Terrain = &sim.Terrain{Data: data}
		// The live Fallen Maple (3x3 footprint, stump two tiles east of the
		// anchor) and the Storm Stump; the other variants are absent, so every
		// break is a maple.
		world.Assets[sim.FallenMapleAssetID] = &sim.Asset{ID: sim.FallenMapleAssetID, Name: "Fallen Maple", DefaultState: "bare",
			IsObstacle: true, FootprintLeft: 1, FootprintRight: 1, FootprintTop: 1, FootprintBottom: 1,
			States: []sim.AssetState{{ID: 40, State: "bare"}, {ID: 41, State: "winter"}}}
		world.Assets[sim.StormStumpAssetID] = &sim.Asset{ID: sim.StormStumpAssetID, Name: "Storm Stump", DefaultState: "maple-bare",
			IsObstacle: true, States: []sim.AssetState{{ID: 42, State: "maple-bare"}, {ID: 43, State: "maple-winter"}}}
		world.Assets["mill-asset"] = &sim.Asset{ID: "mill-asset", Name: "Mill"}
		world.VillageObjects["mill"] = &sim.VillageObject{ID: "mill", DisplayName: "Mill", AssetID: "mill-asset", Pos: landmark.Center()}
		world.Structures["mill"] = &sim.Structure{ID: "mill", DisplayName: "Mill"}
		world.Settings.PublicWorksRoadBounty = 10
		world.Settings.PublicWorksRoadRepairSeconds = 3600
		world.Settings.RoadDamageMinGapHours = 24
		world.Settings.RoadStumpDays = 7
	})
	return w
}

// stormStumps returns every placed storm stump.
func stormStumps(world *sim.World) []*sim.VillageObject {
	var out []*sim.VillageObject
	for _, o := range world.VillageObjects {
		if o.HasTag(sim.TagStormStump) {
			out = append(out, o)
		}
	}
	return out
}

// roadObstacles returns every placed road obstacle.
func roadObstacles(world *sim.World) []*sim.VillageObject {
	var out []*sim.VillageObject
	for _, o := range world.VillageObjects {
		if o.IsRoadObstacle() {
			out = append(out, o)
		}
	}
	return out
}

// footprintTiles lists the tiles the Fallen Maple's 3x3 footprint covers.
func footprintTiles(o *sim.VillageObject) []sim.TilePos {
	a := o.Pos.Tile()
	var out []sim.TilePos
	for y := a.Y - 1; y <= a.Y+1; y++ {
		for x := a.X - 1; x <= a.X+1; x++ {
			out = append(out, sim.TilePos{X: x, Y: y})
		}
	}
	return out
}

// TestRoadDamagePlacesAFallenTreeAcrossTheRoad — a daily hit brings a named,
// damaged fallen maple down across a north-south road near the Mill; it covers
// the road's whole width, its stump stands on the grass at the cut end, and
// walkers route around both.
func TestRoadDamagePlacesAFallenTreeAcrossTheRoad(t *testing.T) {
	w := buildRoadDamageWorld(t, northSouthRoad(100, 2), sim.TilePos{X: 106, Y: 45})
	mustSend(t, w, func(world *sim.World) { world.Settings.RoadDamageChancePermille = 1000 })

	// Roll a hit, pick the first variant (the bare maple), pick a site mid-list.
	res, err := w.Send(sim.RollDailyDamage(time.Now().UTC(), &seqRoller{vals: []float64{0, 0, 0.5}}))
	if err != nil {
		t.Fatal(err)
	}
	if broke, _ := res.([]*sim.VillageObject); len(broke) != 1 {
		t.Fatalf("broke = %v, want one road obstacle", broke)
	}
	mustSend(t, w, func(world *sim.World) {
		obs := roadObstacles(world)
		if len(obs) != 1 {
			t.Fatalf("road obstacles = %d, want 1", len(obs))
		}
		o := obs[0]
		if !o.Damaged() || o.AssetID != sim.FallenMapleAssetID || o.CurrentState != "bare" || o.DisplayName != "Fallen maple" {
			t.Fatalf("obstacle = %+v, want a damaged bare Fallen maple", o)
		}
		if got := sim.PublicWorksKind(o); got != sim.PublicWorksRoad {
			t.Errorf("kind = %q, want road", got)
		}
		if got := sim.DamageFact(world.VillageObjects, world.Structures, world.Assets, o); got != "A fallen maple lies across the road by the Mill" {
			t.Errorf("fact = %q", got)
		}
		// The log spans the road: both road columns sit inside the footprint.
		if a := o.Pos.Tile(); a.X != 100 && a.X != 101 {
			t.Errorf("anchor %v does not span the road at x 100-101", a)
		}
		grid, err := sim.BuildWalkGrid(world)
		if err != nil {
			t.Fatal(err)
		}
		blocked := map[sim.TilePos]bool{}
		for _, p := range footprintTiles(o) {
			blocked[p] = true
			if grid.CanWalk(p.X, p.Y) {
				t.Errorf("footprint tile %v is walkable, want blocked", p)
			}
		}
		a := o.Pos.Tile()
		// Its stump stands two tiles east of the anchor, past the cut end, on
		// grass — and blocks its own tile.
		stumps := stormStumps(world)
		if len(stumps) != 1 {
			t.Fatalf("storm stumps = %d, want 1", len(stumps))
		}
		st := stumps[0]
		stTile := st.Pos.Tile()
		if st.AssetID != sim.StormStumpAssetID || st.CurrentState != "maple-bare" || !st.ExpiresAt.IsZero() {
			t.Errorf("stump = %+v, want a maple-bare Storm Stump with no expiry yet", st)
		}
		if stTile != (sim.TilePos{X: a.X + 2, Y: a.Y}) {
			t.Errorf("stump tile %v, want %v — just past the cut end", stTile, sim.TilePos{X: a.X + 2, Y: a.Y})
		}
		if b := world.Terrain.Data[stTile.Y*sim.MapW+stTile.X]; b == sim.TerrainDirt || b == sim.TerrainCobblestone {
			t.Errorf("stump stands on the road at %v", stTile)
		}
		if grid.CanWalk(stTile.X, stTile.Y) {
			t.Errorf("stump tile %v is walkable, want blocked", stTile)
		}
		blocked[stTile] = true
		path := sim.FindPath(grid, sim.TilePos{X: 100, Y: a.Y - 4}, sim.TilePos{X: 100, Y: a.Y + 4})
		if path == nil {
			t.Fatal("no way past the log — a site must leave a detour")
		}
		for _, p := range path {
			if blocked[p] {
				t.Fatalf("path steps through the log at %v", p)
			}
		}
		lines := strings.Join(sim.PublicWorksNoticeLines(world), "\n")
		for _, want := range []string{"walkers must go around it until it is cleared.", "The town pays 10 coins to the hand who clears it."} {
			if !strings.Contains(lines, want) {
				t.Errorf("notice lines missing %q:\n%s", want, lines)
			}
		}
	})
}

// TestRoadDamageGuards — one obstacle at a time, and none within the min gap
// of the last clearing.
func TestRoadDamageGuards(t *testing.T) {
	w := buildRoadDamageWorld(t, northSouthRoad(100, 2), sim.TilePos{X: 106, Y: 45})
	mustSend(t, w, func(world *sim.World) { world.Settings.RoadDamageChancePermille = 1000 })
	roll := func() {
		if _, err := w.Send(sim.RollDailyDamage(time.Now().UTC(), &seqRoller{vals: []float64{0, 0, 0.5}})); err != nil {
			t.Fatal(err)
		}
	}
	roll()
	roll()
	var id sim.VillageObjectID
	mustSend(t, w, func(world *sim.World) {
		obs := roadObstacles(world)
		if len(obs) != 1 {
			t.Fatalf("road obstacles after two hits = %d, want 1 — one at a time", len(obs))
		}
		id = obs[0].ID
	})
	if _, err := w.Send(sim.SetObjectDamage(id, "repair")); err != nil {
		t.Fatal(err)
	}
	roll()
	mustSend(t, w, func(world *sim.World) {
		if n := len(roadObstacles(world)); n != 0 {
			t.Fatalf("road obstacles right after a clearing = %d, want 0 — the min gap holds", n)
		}
		world.Environment.LastRoadRepairAt = time.Now().UTC().Add(-25 * time.Hour)
	})
	roll()
	mustSend(t, w, func(world *sim.World) {
		if n := len(roadObstacles(world)); n != 1 {
			t.Fatalf("road obstacles once the gap has passed = %d, want 1", n)
		}
	})
}

// TestRoadClearingByAHand — a hand at the fallen tree takes the town's work on
// the road terms; on completion the top is gone, the chest pays 10, the road's
// gap anchor is stamped, and the stump starts its week: the daily sweep leaves
// it before the week is out and takes it after (LLM-678).
func TestRoadClearingByAHand(t *testing.T) {
	w := buildRoadDamageWorld(t, northSouthRoad(100, 2), sim.TilePos{X: 106, Y: 45})
	res, err := w.Send(sim.ForceRoadDamage(sim.DamageTriggerStorm, &seqRoller{vals: []float64{0.9, 0.5}}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.(sim.VillageObjectID)
	mustSend(t, w, func(world *sim.World) {
		o := world.VillageObjects[id]
		if o.AssetID != sim.FallenMapleAssetID || o.CurrentState != "winter" {
			t.Fatalf("obstacle = %+v, want the winter Fallen maple", o)
		}
		// The loiter pin is two tiles below the footprint (bottom 1) — where
		// move_to parks a hand.
		a := o.Pos.Tile()
		world.Actors["anne"].Pos = sim.TilePos{X: a.X, Y: a.Y + 3}
	})
	if _, err := w.Send(sim.StartRepair("anne")); err != nil {
		t.Fatalf("StartRepair at the fallen tree: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		act := world.Actors["anne"].SourceActivity
		if act == nil || !act.PublicWorks || act.Bounty != 10 || act.ObjectID != id {
			t.Fatalf("activity = %+v, want the town's clearing at 10", act)
		}
		if got := act.Until.Sub(act.StartedAt); got != time.Hour {
			t.Errorf("window = %v, want 1h", got)
		}
		act.Until = time.Now().UTC().Add(-time.Second)
	})
	mustSend(t, w, func(world *sim.World) { sim.CompleteDueSourceActivities(world, time.Now().UTC()) })
	mustSend(t, w, func(world *sim.World) {
		if _, ok := world.VillageObjects[id]; ok {
			t.Error("the fallen tree is still in the road after the clearing landed")
		}
		if got := world.Actors["anne"].Coins; got != 10 {
			t.Errorf("anne coins = %d, want 10", got)
		}
		if got := world.Environment.TownChest; got != 90 {
			t.Errorf("chest = %d, want 90", got)
		}
		if world.Environment.LastRoadRepairAt.IsZero() {
			t.Error("LastRoadRepairAt not stamped by the clearing")
		}
		stumps := stormStumps(world)
		if len(stumps) != 1 {
			t.Fatalf("storm stumps after the clearing = %d, want 1 — it outlives the tree", len(stumps))
		}
		if left := time.Until(stumps[0].ExpiresAt); left < 7*24*time.Hour-time.Minute || left > 7*24*time.Hour {
			t.Errorf("stump expires in %v, want a week", left)
		}
	})
	if _, err := w.Send(sim.RemoveExpiredObjects(time.Now().UTC().Add(6 * 24 * time.Hour))); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) {
		if n := len(stormStumps(world)); n != 1 {
			t.Errorf("storm stumps six days on = %d, want 1", n)
		}
	})
	if _, err := w.Send(sim.RemoveExpiredObjects(time.Now().UTC().Add(8 * 24 * time.Hour))); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) {
		if n := len(stormStumps(world)); n != 0 {
			t.Errorf("storm stumps eight days on = %d, want 0", n)
		}
	})
	if got, want := sim.PublicWorksCompletionNarration(sim.PublicWorksRoad, "Fallen tree", 10), "You finish clearing the fallen tree; the road is open again, and the town pays you 10 coins for the work."; got != want {
		t.Errorf("completion = %q, want %q", got, want)
	}
}

// TestRoadObstacleSiteRules — the snapped maple must cut a road, with open dry
// ground around it, its stump off the road, and a named building near: a
// north-south road up to three wide takes one; a wider road, water close by, no
// landmark in range — or an east-west road, where the stump in line with the
// trunk would stand on the road — takes none.
func TestRoadObstacleSiteRules(t *testing.T) {
	water := func(base roadTiles, wx int) roadTiles {
		return func(x, y int) byte {
			if x == wx {
				return sim.TerrainShallowWater
			}
			return base(x, y)
		}
	}
	cases := []struct {
		name     string
		paint    roadTiles
		landmark sim.TilePos
		want     bool
	}{
		{"north-south, 1 wide", northSouthRoad(100, 1), sim.TilePos{X: 106, Y: 45}, true},
		{"north-south, 3 wide", northSouthRoad(100, 3), sim.TilePos{X: 106, Y: 45}, true},
		{"north-south, 4 wide", northSouthRoad(100, 4), sim.TilePos{X: 106, Y: 45}, false},
		{"east-west, 2 wide (stump would stand on the road)", eastWestRoad(40, 2), sim.TilePos{X: 100, Y: 46}, false},
		{"water beside the road", water(northSouthRoad(100, 2), 103), sim.TilePos{X: 106, Y: 45}, false},
		{"no building within range", northSouthRoad(100, 2), sim.TilePos{X: 150, Y: 150}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := buildRoadDamageWorld(t, tc.paint, tc.landmark)
			_, err := w.Send(sim.ForceRoadDamage(sim.DamageTriggerForce, &seqRoller{vals: []float64{0, 0.5}}))
			if tc.want && err != nil {
				t.Fatalf("want a site, got %v", err)
			}
			if !tc.want && !errors.Is(err, sim.ErrNoRoadSite) {
				t.Fatalf("want ErrNoRoadSite, got %v", err)
			}
		})
	}
}

// TestRoadObstacleNeverFallsOnSomeone — the only stretch of road where an
// actor stands under every candidate footprint takes no log.
func TestRoadObstacleNeverFallsOnSomeone(t *testing.T) {
	// A short road, just long enough for one row of candidates.
	short := func(x, y int) byte {
		if x == 100 && y >= 43 && y <= 47 {
			return sim.TerrainDirt
		}
		return sim.TerrainLightGrass
	}
	w := buildRoadDamageWorld(t, short, sim.TilePos{X: 106, Y: 45})
	mustSend(t, w, func(world *sim.World) {
		for i, id := range []sim.ActorID{"anne", "joseph", "gideon"} {
			world.Actors[id].Pos = sim.TilePos{X: 100, Y: 44 + i}
		}
	})
	if _, err := w.Send(sim.ForceRoadDamage(sim.DamageTriggerForce, &seqRoller{vals: []float64{0, 0.5}})); !errors.Is(err, sim.ErrNoRoadSite) {
		t.Fatalf("want ErrNoRoadSite with the road occupied, got %v", err)
	}
}

// TestRoadClearingThatCannotLandPaysNothing (code_review) — a road obstacle the
// engine cannot delete (here a structure tagged road_obstacle, which
// DeleteVillageObject refuses) is not cleared by the hand's window: it stays
// in the road and damaged, the chest pays nothing, and no gap is stamped.
func TestRoadClearingThatCannotLandPaysNothing(t *testing.T) {
	w := buildRoadDamageWorld(t, northSouthRoad(100, 2), sim.TilePos{X: 106, Y: 45})
	res, err := w.Send(sim.ForceRoadDamage(sim.DamageTriggerForce, &seqRoller{vals: []float64{0, 0.5}}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.(sim.VillageObjectID)
	mustSend(t, w, func(world *sim.World) {
		world.Structures[sim.StructureID(id)] = &sim.Structure{ID: sim.StructureID(id), DisplayName: "Fallen log"}
		a := world.VillageObjects[id].Pos.Tile()
		world.Actors["anne"].Pos = sim.TilePos{X: a.X, Y: a.Y + 3}
	})
	if _, err := w.Send(sim.StartRepair("anne")); err != nil {
		t.Fatalf("StartRepair: %v", err)
	}
	mustSend(t, w, func(world *sim.World) { world.Actors["anne"].SourceActivity.Until = time.Now().UTC().Add(-time.Second) })
	mustSend(t, w, func(world *sim.World) { sim.CompleteDueSourceActivities(world, time.Now().UTC()) })
	mustSend(t, w, func(world *sim.World) {
		o, ok := world.VillageObjects[id]
		if !ok || !o.Damaged() {
			t.Fatalf("obstacle present=%v damaged=%v, want it still in the road and damaged", ok, ok && o.Damaged())
		}
		if got := world.Actors["anne"].Coins; got != 0 {
			t.Errorf("anne coins = %d, want 0 — the clearing never landed", got)
		}
		if got := world.Environment.TownChest; got != 100 {
			t.Errorf("chest = %d, want 100 untouched", got)
		}
		if !world.Environment.LastRoadRepairAt.IsZero() {
			t.Error("LastRoadRepairAt stamped for a clearing that did not land")
		}
	})
}

// TestRoadTrunkVariantHasNoStump — the broken-trunk variant (the summer-forest
// Fallen Tree art) comes down with no stump, and, having none to keep off the
// road, can close an east-west road the snapped trees cannot.
func TestRoadTrunkVariantHasNoStump(t *testing.T) {
	w := buildRoadDamageWorld(t, eastWestRoad(40, 2), sim.TilePos{X: 100, Y: 46})
	mustSend(t, w, func(world *sim.World) {
		delete(world.Assets, sim.FallenMapleAssetID)
		world.Assets[sim.FallenTrunkAssetID] = &sim.Asset{ID: sim.FallenTrunkAssetID, Name: "Fallen Trunk", DefaultState: "default",
			IsObstacle: true, FootprintLeft: 1, FootprintTop: 1, States: []sim.AssetState{{ID: 44, State: "default"}}}
	})
	res, err := w.Send(sim.ForceRoadDamage(sim.DamageTriggerForce, &seqRoller{vals: []float64{0, 0.5}}))
	if err != nil {
		t.Fatalf("want the trunk across the east-west road, got %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		o := world.VillageObjects[res.(sim.VillageObjectID)]
		if o.AssetID != sim.FallenTrunkAssetID || o.DisplayName != "Fallen tree" {
			t.Fatalf("obstacle = %+v, want the Fallen tree trunk", o)
		}
		if n := len(stormStumps(world)); n != 0 {
			t.Errorf("storm stumps = %d, want none for the trunk", n)
		}
	})
}
