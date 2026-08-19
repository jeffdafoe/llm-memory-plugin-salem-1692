package sim_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/cascade"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

const cowSpriteID sim.SpriteID = "sprite-cow"
const cowID sim.ActorID = "cow-actor-0001"

// gateAssetID mirrors the LLM-639 Ranch Fence Gate shape: NOT an obstacle
// (people pass), a 2-tile footprint, one state tagged fence-gate.
const gateAssetID sim.AssetID = "ranch-gate"

func gateTestAsset() *sim.Asset {
	return &sim.Asset{
		ID: gateAssetID, Name: "Ranch Fence Gate", Category: "fence", DefaultState: "default",
		AnchorX: 0.5, AnchorY: 0.85, IsObstacle: false,
		FootprintLeft: 0, FootprintRight: 1, FootprintTop: 0, FootprintBottom: 0,
		States: []sim.AssetState{{ID: 20, State: "default", Tags: []string{"fence-gate"}}},
	}
}

// buildGrazerWorld seeds all-grass terrain, the fence + gate assets, the cow
// sprite (grazer+ambient) and one decorative cow at tile pos.
func buildGrazerWorld(t *testing.T, pos sim.TilePos) *sim.World {
	t.Helper()
	data := make([]byte, sim.MapW*sim.MapH)
	for i := range data {
		data[i] = sim.TerrainLightGrass
	}
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(&sim.Terrain{Data: data})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		fenceTestAssetID: fenceTestAsset(),
		gateAssetID:      gateTestAsset(),
	})
	handles.Sprites.Seed(map[sim.SpriteID]*sim.Sprite{
		cowSpriteID: {
			ID:        cowSpriteID,
			Name:      "Cow (white)",
			Behaviors: []string{sim.BehaviorGrazer, sim.BehaviorAmbient},
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		cowID: {
			ID: cowID, DisplayName: "Cow", Kind: sim.KindDecorative,
			SpriteID: cowSpriteID, Pos: sim.Position{X: pos.X, Y: pos.Y}, Facing: "south",
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel AND wait for the world goroutine to exit (code_review, LLM-639 —
	// the same leak the waterfowl harness had): a test must not finish while
	// its world is still draining queued commands or ticker callbacks.
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return w
}

// buildPenWithGate draws a fence ring around [x1..x2]×[y1..y2] (exclusive
// interior), deletes the two top-edge segments at gapX/gapX+1, and places the
// gate across the gap. Returns the gate's object id.
func buildPenWithGate(t *testing.T, w *sim.World, x1, y1, x2, y2, gapX int) sim.VillageObjectID {
	t.Helper()
	ax, ay := tilePx(sim.TilePos{X: x1, Y: y1})
	bx, by := tilePx(sim.TilePos{X: x2, Y: y2})
	r, err := w.Send(sim.PlaceFenceRun(fenceTestAssetID, ax, ay, bx, by, "tester"))
	if err != nil {
		t.Fatalf("pen: %v", err)
	}
	for _, obj := range r.(sim.PlaceFenceRunResult).Objects {
		tile := obj.Pos.Tile()
		if tile.Y == y1 && (tile.X == gapX || tile.X == gapX+1) {
			if _, err := w.Send(sim.DeleteVillageObject(obj.ID)); err != nil {
				t.Fatalf("open gap: %v", err)
			}
		}
	}
	// The gate's anchor tile is the LEFT gap tile; footprint right 1 covers the
	// second. Position = tile centre (anchor lands inside the tile).
	gx, gy := tilePx(sim.TilePos{X: gapX, Y: y1})
	gr, err := w.Send(sim.CreateVillageObject(gateAssetID, gx, gy, "", "tester"))
	if err != nil {
		t.Fatalf("gate: %v", err)
	}
	return gr.(sim.CreateObjectResult).Object.ID
}

// TestGrazerWalkGrid: gate footprint tiles are impassable on the grazer grid,
// walkable on the standard grid; everywhere else the two grids are identical.
func TestGrazerWalkGrid(t *testing.T) {
	inside := sim.TilePos{X: sim.PadX + 12, Y: sim.PadY + 12}
	w := buildGrazerWorld(t, inside)
	buildPenWithGate(t, w, sim.PadX+10, sim.PadY+10, sim.PadX+15, sim.PadY+14, sim.PadX+12)

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		std, err := sim.BuildWalkGrid(world)
		if err != nil {
			return nil, err
		}
		grz, err := sim.BuildGrazerWalkGrid(world)
		if err != nil {
			return nil, err
		}
		for _, gx := range []int{sim.PadX + 12, sim.PadX + 13} {
			if !std.CanWalk(gx, sim.PadY+10) {
				t.Errorf("standard grid: gate tile (%d) should be walkable", gx)
			}
			if grz.CanWalk(gx, sim.PadY+10) {
				t.Errorf("grazer grid: gate tile (%d) should be impassable", gx)
			}
		}
		// A fence tile is blocked on both; open ground identical on both.
		if std.CanWalk(sim.PadX+10, sim.PadY+10) || grz.CanWalk(sim.PadX+10, sim.PadY+10) {
			t.Error("fence corner should be impassable on both grids")
		}
		diff := 0
		for y := 0; y < sim.MapH; y++ {
			for x := 0; x < sim.MapW; x++ {
				if std.CostAt(x, y) != grz.CostAt(x, y) {
					diff++
				}
			}
		}
		if diff != 2 {
			t.Errorf("grids differ on %d tiles, want exactly the 2 gate tiles", diff)
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("grid inspect: %v", err)
	}
}

// TestGrazerPennedRegionAndVillagerPassage is the point of the ticket: the
// cow's reachable region is exactly the pen interior — the gate does not leak
// — while a villager paths from outside to the pen centre through the gate.
// Deleting a fence segment then lets the region grow past the pen.
func TestGrazerPennedRegionAndVillagerPassage(t *testing.T) {
	inside := sim.TilePos{X: sim.PadX + 12, Y: sim.PadY + 12}
	w := buildGrazerWorld(t, inside)
	// Pen ring 10..15 × 10..14 → interior 11..14 × 11..13 = 12 tiles.
	buildPenWithGate(t, w, sim.PadX+10, sim.PadY+10, sim.PadX+15, sim.PadY+14, sim.PadX+12)

	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		cow := world.Actors[cowID]
		region := sim.GrazerRegion(world, cow)
		std, err := sim.BuildWalkGrid(world)
		if err != nil {
			return nil, err
		}
		outside := sim.GridPoint{X: sim.PadX + 30, Y: sim.PadY + 30}
		villagerPath := sim.FindPath(std, outside, sim.GridPoint{X: inside.X, Y: inside.Y})
		return []int{len(region), len(villagerPath)}, nil
	}})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	got := res.([]int)
	// Region excludes the cow's own tile: 12 interior tiles - 1 = 11.
	if got[0] != 11 {
		t.Errorf("penned region = %d tiles, want 11 (the pen interior minus the cow's tile)", got[0])
	}
	if got[1] == 0 {
		t.Errorf("villager found no path into the gated pen — the gate should be open to people")
	}

	// Knock a hole in the east wall: the region must grow past the pen.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		for id, obj := range world.VillageObjects {
			if obj != nil && obj.AssetID == fenceTestAssetID {
				tile := obj.Pos.Tile()
				if tile.X == sim.PadX+15 && tile.Y == sim.PadY+12 {
					_, err := sim.DeleteVillageObject(id).Fn(world)
					return nil, err
				}
			}
		}
		return nil, errors.New("east wall segment not found")
	}}); err != nil {
		t.Fatalf("breach: %v", err)
	}
	res, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return len(sim.GrazerRegion(world, world.Actors[cowID])), nil
	}})
	if err != nil {
		t.Fatalf("re-inspect: %v", err)
	}
	if res.(int) <= 11 {
		t.Errorf("region after breach = %d tiles, want > 11 (the cow can escape)", res.(int))
	}
}

// TestGrazerTickerAmblesWithinPen drives decisions through EvaluateGrazerTick:
// the first pass stamps a dwell, a pass past the dwell dispatches a move, and
// the destination stays inside the pen. Delta-based (MoveIntent captured
// before), asserts outside Command.Fn per the waterfowl harness convention.
func TestGrazerTickerAmblesWithinPen(t *testing.T) {
	inside := sim.TilePos{X: sim.PadX + 12, Y: sim.PadY + 12}
	w := buildGrazerWorld(t, inside)
	buildPenWithGate(t, w, sim.PadX+10, sim.PadY+10, sim.PadX+15, sim.PadY+14, sim.PadX+12)

	now := time.Now()
	if _, err := w.Send(sim.EvaluateGrazerTick(now)); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	moved, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[cowID].MoveIntent != nil, nil
	}})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if moved.(bool) {
		t.Fatal("cow moved on the first tick — it should graze (dwell) first")
	}
	// Far past any dwell (dwell max is 45s).
	if _, err := w.Send(sim.EvaluateGrazerTick(now.Add(2 * time.Minute))); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		cow := world.Actors[cowID]
		if cow.MoveIntent == nil || cow.MoveIntent.Destination.Position == nil {
			return nil, nil
		}
		p := cow.MoveIntent.Destination.Position
		return []int{p.X, p.Y}, nil
	}})
	if err != nil {
		t.Fatalf("read 2: %v", err)
	}
	dest, ok := res.([]int)
	if !ok {
		t.Fatal("cow has no move intent after its dwell elapsed — the amble never dispatched")
	}
	if dest[0] < sim.PadX+11 || dest[0] > sim.PadX+14 || dest[1] < sim.PadY+11 || dest[1] > sim.PadY+13 {
		t.Errorf("amble destination (%d,%d) is outside the pen interior", dest[0], dest[1])
	}
}

// TestGrazerGates: the Kind gate and the teleport guard. A STATEFUL actor in a
// cow sprite is not a grazer (walks the standard grid, may stand on a gate
// tile); the decorative cow is refused a teleport onto a gate tile.
func TestGrazerGates(t *testing.T) {
	inside := sim.TilePos{X: sim.PadX + 12, Y: sim.PadY + 12}
	w := buildGrazerWorld(t, inside)
	buildPenWithGate(t, w, sim.PadX+10, sim.PadY+10, sim.PadX+15, sim.PadY+14, sim.PadX+12)

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["npc-in-cow-suit"] = &sim.Actor{
			ID: "npc-in-cow-suit", DisplayName: "Impostor", Kind: sim.KindNPCStateful,
			SpriteID: cowSpriteID, Pos: sim.Position{X: sim.PadX + 30, Y: sim.PadY + 30},
		}
		if sim.ActorIsGrazer(world, world.Actors["npc-in-cow-suit"]) {
			t.Error("a stateful NPC in a cow sprite must not be a grazer")
		}
		if !sim.ActorIsGrazer(world, world.Actors[cowID]) {
			t.Error("the decorative cow should be a grazer")
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("kind gate: %v", err)
	}

	gateTile := sim.Position{X: sim.PadX + 12, Y: sim.PadY + 10}
	if _, err := w.Send(sim.SetActorPosition(cowID, gateTile, time.Now())); err == nil {
		t.Error("teleporting the cow onto a gate tile should be refused")
	}
	if _, err := w.Send(sim.SetActorPosition("npc-in-cow-suit", gateTile, time.Now())); err != nil {
		t.Errorf("teleporting the stateful impostor onto the gate tile should be allowed: %v", err)
	}
}

// TestGrazerLeavesNoHistory is the stated behavioural boundary of the feature
// (code_review, LLM-639): a grazer's movement, driven end to end through the
// real command path — wander decision, locomotion ticks, arrival, with the
// cascade action-log subscribers registered — writes NOTHING to the action
// log, and the animal is absent from the atmosphere roster. Both ride the
// "ambient" behavior (LLM-593), so this regresses loudly if that filtering
// ever changes shape.
func TestGrazerLeavesNoHistory(t *testing.T) {
	inside := sim.TilePos{X: sim.PadX + 12, Y: sim.PadY + 12}
	w := buildGrazerWorld(t, inside)
	buildPenWithGate(t, w, sim.PadX+10, sim.PadY+10, sim.PadX+15, sim.PadY+14, sim.PadX+12)
	ctx, cancelCascade := context.WithCancel(context.Background())
	t.Cleanup(cancelCascade)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		cascade.RegisterActionLog(ctx, world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("register cascade: %v", err)
	}

	// Decision pass 1 stamps the dwell; pass 2 (past any dwell) dispatches.
	now := time.Now()
	if _, err := w.Send(sim.EvaluateGrazerTick(now)); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := w.Send(sim.EvaluateGrazerTick(now)); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	// Walk the amble to completion through real locomotion ticks (the grazer
	// steps every 2nd tick; an amble is at most GrazerAmbleRange tiles).
	arrived := false
	for i := 0; i < 40 && !arrived; i++ {
		now = now.Add(time.Second)
		if _, err := w.Send(sim.EvaluateLocomotion(now)); err != nil {
			t.Fatalf("locomotion: %v", err)
		}
		res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			return world.Actors[cowID].MoveIntent == nil, nil
		}})
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		arrived = res.(bool)
	}
	if !arrived {
		t.Fatal("the cow never completed its amble — nothing exercised the arrival path")
	}

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		for _, e := range world.ActionLog {
			if e.ActorID == cowID {
				t.Errorf("action log holds a %q row for the cow — ambient movement must leave no history", e.ActionType)
			}
		}
		for _, entry := range sim.BuildVillageContextRoster(world) {
			for _, name := range entry.DisplayNames {
				if strings.Contains(name, "Cow") {
					t.Errorf("atmosphere roster names the cow in bucket %q", entry.StructureLabel)
				}
			}
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("inspect: %v", err)
	}
}
