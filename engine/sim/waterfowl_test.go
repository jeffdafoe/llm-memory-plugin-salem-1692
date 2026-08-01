package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// waterfowl_test.go — LLM-579 duck wander coverage: the water-opened walk
// grid, self-defining lake region + shore band, the decision ticker's
// move dispatch, and the waterfowl gates (kind + behavior).

const duckSpriteID sim.SpriteID = "sprite-duck-uuid"

// pondRect is the test pond: an 8x6 water rectangle at tile offset
// (+20,+20) from the pad origin. Small enough that every tile is within
// WaterfowlSwimRange of every other.
var pondMinX, pondMinY, pondMaxX, pondMaxY = sim.PadX + 20, sim.PadY + 20, sim.PadX + 27, sim.PadY + 25

// makePondTerrain returns all-grass terrain with the pond rectangle
// painted shallow water.
func makePondTerrain() *sim.Terrain {
	data := make([]byte, sim.MapW*sim.MapH)
	for i := range data {
		data[i] = sim.TerrainLightGrass
	}
	for y := pondMinY; y <= pondMaxY; y++ {
		for x := pondMinX; x <= pondMaxX; x++ {
			data[y*sim.MapW+x] = sim.TerrainShallowWater
		}
	}
	return &sim.Terrain{Data: data}
}

func seedDuckSprite(handles *mem.Handles) {
	handles.Sprites.Seed(map[sim.SpriteID]*sim.Sprite{
		duckSpriteID: {
			ID:        duckSpriteID,
			Name:      "Duck (mallard)",
			Behaviors: []string{sim.BehaviorWaterfowl},
		},
	})
}

// duckWorld builds a world with the pond, the duck sprite, and one
// decorative duck actor standing at the given tile.
func duckWorld(t *testing.T, pos sim.Position) (*sim.World, sim.ActorID) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makePondTerrain())
	seedDuckSprite(handles)
	const duckID sim.ActorID = "duck-actor-0001"
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		duckID: {
			ID:          duckID,
			DisplayName: "Duck",
			Kind:        sim.KindDecorative,
			SpriteID:    duckSpriteID,
			Pos:         pos,
			Facing:      "south",
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	return w, duckID
}

// runOn drives a bare Fn through the world command channel.
func runOn(t *testing.T, w *sim.World, fn func(*sim.World) (any, error)) any {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: fn})
	if err != nil {
		t.Fatalf("world command: %v", err)
	}
	return res
}

// TestWaterfowlWalkGrid — water is walkable on the waterfowl grid and
// stays impassable on the standard grid; land costs are identical on both.
func TestWaterfowlWalkGrid(t *testing.T) {
	w, _ := duckWorld(t, sim.Position{X: pondMinX + 2, Y: pondMinY + 2})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	runOn(t, w, func(world *sim.World) (any, error) {
		std, err := sim.BuildWalkGrid(world)
		if err != nil {
			t.Fatalf("buildWalkGrid: %v", err)
		}
		duck, err := sim.BuildWaterfowlWalkGrid(world)
		if err != nil {
			t.Fatalf("buildWaterfowlWalkGrid: %v", err)
		}
		if std.CanWalk(pondMinX, pondMinY) {
			t.Error("standard grid: pond tile should be impassable")
		}
		if !duck.CanWalk(pondMinX, pondMinY) {
			t.Error("waterfowl grid: pond tile should be walkable")
		}
		if got := duck.CostAt(pondMinX, pondMinY); got != 1 {
			t.Errorf("waterfowl water cost = %d, want 1", got)
		}
		// Land identical on both.
		if std.CostAt(sim.PadX, sim.PadY) != duck.CostAt(sim.PadX, sim.PadY) {
			t.Errorf("land cost differs: std %d, waterfowl %d",
				std.CostAt(sim.PadX, sim.PadY), duck.CostAt(sim.PadX, sim.PadY))
		}
		return nil, nil
	})
}

// TestWaterfowlRegionAndShore — the flood fill finds the whole pond from
// a swimming duck, finds it from an ashore duck within the search
// radius, and comes up empty far from water. The shore band holds only
// walkable land tiles hugging the pond.
func TestWaterfowlRegionAndShore(t *testing.T) {
	w, duckID := duckWorld(t, sim.Position{X: pondMinX + 2, Y: pondMinY + 2})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	pondSize := (pondMaxX - pondMinX + 1) * (pondMaxY - pondMinY + 1)

	runOn(t, w, func(world *sim.World) (any, error) {
		duck := world.Actors[duckID]

		region := sim.WaterfowlRegion(world, duck)
		if len(region) != pondSize {
			t.Errorf("on-water region = %d tiles, want %d", len(region), pondSize)
		}

		// Ashore beside the pond: same region via nearest-water search.
		duck.Pos = sim.Position{X: pondMinX - 2, Y: pondMinY}
		region = sim.WaterfowlRegion(world, duck)
		if len(region) != pondSize {
			t.Errorf("ashore region = %d tiles, want %d", len(region), pondSize)
		}

		// Far from water: empty.
		duck.Pos = sim.Position{X: sim.PadX, Y: sim.PadY}
		if got := sim.WaterfowlRegion(world, duck); len(got) != 0 {
			t.Errorf("far-from-water region = %d tiles, want 0", len(got))
		}

		duck.Pos = sim.Position{X: pondMinX + 2, Y: pondMinY + 2}
		region = sim.WaterfowlRegion(world, duck)
		shore := sim.WaterfowlShore(world, region)
		if len(shore) == 0 {
			t.Fatal("shore band empty for an open-grass pond")
		}
		// No duplicates — a repeated tile would weight the uniform target
		// draw toward the most-connected shore tiles.
		seen := make(map[sim.GridPoint]bool, len(shore))
		for _, p := range shore {
			if seen[p] {
				t.Errorf("shore tile (%d,%d) appears more than once", p.X, p.Y)
			}
			seen[p] = true
		}
		for _, p := range shore {
			b := world.Terrain.Data[p.Y*sim.MapW+p.X]
			if b == sim.TerrainShallowWater || b == sim.TerrainDeepWater {
				t.Errorf("shore tile (%d,%d) is water", p.X, p.Y)
			}
			// Within two 4-steps of the pond rectangle.
			dx, dy := 0, 0
			if p.X < pondMinX {
				dx = pondMinX - p.X
			} else if p.X > pondMaxX {
				dx = p.X - pondMaxX
			}
			if p.Y < pondMinY {
				dy = pondMinY - p.Y
			} else if p.Y > pondMaxY {
				dy = p.Y - pondMaxY
			}
			if dx+dy > 2 {
				t.Errorf("shore tile (%d,%d) is %d steps from the pond, want <= 2", p.X, p.Y, dx+dy)
			}
		}
		return nil, nil
	})
}

// TestWaterfowlDecisionDispatchesMove — the decision pass dwells first,
// then issues a MoveIntent whose destination is water or shore.
func TestWaterfowlDecisionDispatchesMove(t *testing.T) {
	w, duckID := duckWorld(t, sim.Position{X: pondMinX + 3, Y: pondMinY + 3})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	now := time.Now()
	runOn(t, w, func(world *sim.World) (any, error) {
		// First pass: observes the idle duck, stamps a dwell, no move yet.
		sim.EvaluateWaterfowl(world, now)
		if world.Actors[duckID].MoveIntent != nil {
			t.Fatal("first pass should dwell, not move")
		}
		// Past the max dwell (2s base + 10s jitter): must decide.
		sim.EvaluateWaterfowl(world, now.Add(13*time.Second))
		duck := world.Actors[duckID]
		if duck.MoveIntent == nil {
			t.Fatal("second pass past dwell should have dispatched a move")
		}
		dest := duck.MoveIntent.Destination
		if dest.Kind != sim.MoveDestinationPosition || dest.Position == nil {
			t.Fatalf("unexpected destination %+v", dest)
		}
		// Target is the pond or its shore band (within 2 steps of the rect).
		p := *dest.Position
		dx, dy := 0, 0
		if p.X < pondMinX {
			dx = pondMinX - p.X
		} else if p.X > pondMaxX {
			dx = p.X - pondMaxX
		}
		if p.Y < pondMinY {
			dy = pondMinY - p.Y
		} else if p.Y > pondMaxY {
			dy = p.Y - pondMaxY
		}
		if dx+dy > 2 {
			t.Errorf("move target (%d,%d) is %d steps outside the pond", p.X, p.Y, dx+dy)
		}
		return nil, nil
	})
}

// TestWaterfowlGates — the wander seizes only decorative waterfowl: a
// stateful NPC wearing the duck sprite and a decorative in a villager
// sprite are both left alone, and neither gets the water grid.
func TestWaterfowlGates(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makePondTerrain())
	seedDuckSprite(handles)
	handles.Sprites.Seed(map[sim.SpriteID]*sim.Sprite{
		"sprite-villager": {ID: "sprite-villager", Name: "Villager A"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"npc-duck-sprite": {
			ID: "npc-duck-sprite", DisplayName: "Costumed NPC",
			Kind: sim.KindNPCStateful, LLMAgent: "zbbs-someone",
			SpriteID: duckSpriteID,
			Pos:      sim.Position{X: pondMinX - 1, Y: pondMinY},
		},
		"decorative-villager": {
			ID: "decorative-villager", DisplayName: "Statue",
			Kind:     sim.KindDecorative,
			SpriteID: "sprite-villager",
			Pos:      sim.Position{X: pondMinX - 1, Y: pondMinY + 1},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	now := time.Now()
	runOn(t, w, func(world *sim.World) (any, error) {
		if sim.ActorIsWaterfowl(world, world.Actors["npc-duck-sprite"]) {
			t.Error("stateful NPC in a duck sprite must not be waterfowl")
		}
		if sim.ActorIsWaterfowl(world, world.Actors["decorative-villager"]) {
			t.Error("decorative villager must not be waterfowl")
		}
		sim.EvaluateWaterfowl(world, now)
		sim.EvaluateWaterfowl(world, now.Add(13*time.Second))
		if world.Actors["npc-duck-sprite"].MoveIntent != nil {
			t.Error("wander seized a stateful NPC")
		}
		if world.Actors["decorative-villager"].MoveIntent != nil {
			t.Error("wander seized a non-waterfowl decorative")
		}
		return nil, nil
	})
}

// TestMoveActorWaterDestinationGating — a duck may be sent onto water;
// a villager may not. Same for the admin teleport.
func TestMoveActorWaterDestinationGating(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makePondTerrain())
	seedDuckSprite(handles)
	handles.Sprites.Seed(map[sim.SpriteID]*sim.Sprite{
		"sprite-villager": {ID: "sprite-villager", Name: "Villager A"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"duck-1": {
			ID: "duck-1", DisplayName: "Duck", Kind: sim.KindDecorative,
			SpriteID: duckSpriteID, Pos: sim.Position{X: pondMinX - 1, Y: pondMinY},
		},
		"villager-1": {
			ID: "villager-1", DisplayName: "Goody", Kind: sim.KindDecorative,
			SpriteID: "sprite-villager", Pos: sim.Position{X: pondMinX - 1, Y: pondMinY + 1},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	now := time.Now()
	waterPos := sim.Position{X: pondMinX + 1, Y: pondMinY + 1}
	dest := sim.MoveDestination{Kind: sim.MoveDestinationPosition, Position: &waterPos}

	if _, err := w.Send(sim.MoveActor("duck-1", dest, false, now)); err != nil {
		t.Errorf("duck move onto water refused: %v", err)
	}
	if _, err := w.Send(sim.MoveActor("villager-1", dest, false, now)); err == nil {
		t.Error("villager move onto water should be refused")
	}

	if _, err := w.Send(sim.SetActorPosition("duck-1", waterPos, now)); err != nil {
		t.Errorf("duck teleport onto water refused: %v", err)
	}
	if _, err := w.Send(sim.SetActorPosition("villager-1", waterPos, now)); err == nil {
		t.Error("villager teleport onto water should be refused")
	}
}

// TestCreateNPCBornDecorative — an agentless editor creation is
// KindDecorative (matching what ClassifyActorKind reconstructs on
// reload), and linking an agent flips it live.
func TestCreateNPCBornDecorative(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	seedDuckSprite(handles)
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	res, err := w.Send(sim.CreateNPC("Test Duck", string(duckSpriteID), sim.WorldPos{X: 100, Y: 100}, time.Now()))
	if err != nil {
		t.Fatalf("CreateNPC: %v", err)
	}
	id := res.(sim.CreateNPCResult).ActorID

	runOn(t, w, func(world *sim.World) (any, error) {
		if got := world.Actors[id].Kind; got != sim.KindDecorative {
			t.Errorf("fresh agentless NPC Kind = %v, want KindDecorative", got)
		}
		return nil, nil
	})

	// Linking an agent brings it online without a restart.
	if _, err := w.Send(sim.SetActorAgentLink(id, "zbbs-test-duck")); err != nil {
		t.Fatalf("SetActorAgentLink: %v", err)
	}
	runOn(t, w, func(world *sim.World) (any, error) {
		if got := world.Actors[id].Kind; got != sim.KindNPCStateful {
			t.Errorf("post-link Kind = %v, want KindNPCStateful", got)
		}
		return nil, nil
	})

	// Linking a shared VA classifies shared.
	res2, err := w.Send(sim.CreateNPC("Shared Villager", string(duckSpriteID), sim.WorldPos{X: 132, Y: 100}, time.Now()))
	if err != nil {
		t.Fatalf("CreateNPC: %v", err)
	}
	id2 := res2.(sim.CreateNPCResult).ActorID
	if _, err := w.Send(sim.SetActorAgentLink(id2, sim.VendorAgentName)); err != nil {
		t.Fatalf("SetActorAgentLink: %v", err)
	}
	runOn(t, w, func(world *sim.World) (any, error) {
		if got := world.Actors[id2].Kind; got != sim.KindNPCShared {
			t.Errorf("post-shared-link Kind = %v, want KindNPCShared", got)
		}
		return nil, nil
	})
}
