package sim_test

import (
	"context"
	"fmt"
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
			// Within WaterfowlShoreBandRings 4-steps of the pond rectangle.
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
			if dx+dy > sim.WaterfowlShoreBandRings {
				t.Errorf("shore tile (%d,%d) is %d steps from the pond, want <= %d",
					p.X, p.Y, dx+dy, sim.WaterfowlShoreBandRings)
			}
		}

		// The band must actually REACH the configured range on open grass —
		// otherwise widening the constant would be a no-op the bound check
		// above could never catch (it only ever asserts an upper limit).
		maxStep := 0
		for _, p := range shore {
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
			if dx+dy > maxStep {
				maxStep = dx + dy
			}
		}
		if maxStep != sim.WaterfowlShoreBandRings {
			t.Errorf("furthest shore tile is %d steps out, want %d — the band is not reaching its configured range",
				maxStep, sim.WaterfowlShoreBandRings)
		}
		return nil, nil
	})
}

// TestWaterfowlShoreBandStopsAtWalls — LLM-585. The band is grown ring by ring
// as a walkable BFS out from the bank, not as "every tile within N of the
// water". The difference only shows up once the band is wide enough to reach
// an obstacle: a duck must not potter to the far side of a wall that happens
// to stand within WaterfowlShoreBandRings of the pond.
func TestWaterfowlShoreBandStopsAtWalls(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makePondTerrain())
	seedDuckSprite(handles)
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"wall": {ID: "wall", IsObstacle: true},
	})
	// A wall two tiles west of the pond, running from well above its top edge
	// to well below its bottom one, so the band cannot round the ends within
	// its ring budget. Terrain cannot express this: no LAND terrain is
	// impassable (TerrainCost's default is walkable), and water is walkable to
	// a duck by design — only obstacle stamping blocks one.
	wallX := pondMinX - 2
	objects := map[sim.VillageObjectID]*sim.VillageObject{}
	for y := pondMinY - sim.WaterfowlShoreBandRings - 2; y <= pondMaxY+sim.WaterfowlShoreBandRings+2; y++ {
		coord := sim.TileToWorld(sim.GridPoint{X: wallX, Y: y})
		id := sim.VillageObjectID(fmt.Sprintf("wall-%d", y))
		objects[id] = &sim.VillageObject{ID: id, AssetID: "wall", Pos: sim.WorldPos{X: coord.X, Y: coord.Y}}
	}
	handles.VillageObjects.Seed(objects)
	const duckID sim.ActorID = "duck-actor-0001"
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		duckID: {
			ID: duckID, DisplayName: "Duck",
			Kind:     sim.KindDecorative,
			SpriteID: duckSpriteID,
			Pos:      sim.Position{X: pondMinX + 2, Y: pondMinY + 2},
			Facing:   "south",
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	defer func() { cancel(); <-done }()

	type shoreResult struct {
		size     int
		beyond   []sim.GridPoint
		reachedX bool
	}
	res := runOn(t, w, func(world *sim.World) (any, error) {
		duck := world.Actors[duckID]
		region := sim.WaterfowlRegion(world, duck)
		shore := sim.WaterfowlShore(world, region)
		out := shoreResult{size: len(shore)}
		for _, p := range shore {
			if p.X < wallX {
				out.beyond = append(out.beyond, p)
			}
			if p.X == wallX+1 {
				// The tile immediately in front of the wall, proving the band
				// grew far enough west to be stopped BY the wall rather than
				// falling short of it.
				out.reachedX = true
			}
		}
		return out, nil
	}).(shoreResult)

	if res.size == 0 {
		t.Fatal("shore band empty")
	}
	if !res.reachedX {
		t.Fatalf("band never reached the tile in front of the wall (x=%d) — the test proves nothing about blocking", wallX+1)
	}
	for _, p := range res.beyond {
		t.Errorf("shore tile (%d,%d) is beyond the wall at x=%d — the band crossed an unwalkable tile", p.X, p.Y, wallX)
	}
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
		if dx+dy > sim.WaterfowlShoreBandRings {
			t.Errorf("move target (%d,%d) is %d steps outside the pond, want <= %d",
				p.X, p.Y, dx+dy, sim.WaterfowlShoreBandRings)
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

// TestWaterfowlWandersOutOfAHuddle — LLM-582. MoveActor refuses to move an
// actor in an active huddle unless LeaveHuddleFirst is set, and the wander's
// dispatch-error path is deliberately silent, so a huddled duck used to stop
// dead until the 2h silence sweep freed it, logging nothing. The wander now
// passes LeaveHuddleFirst, so a duck that ends up in a huddle by ANY path
// (LLM-582 closes the arrival-encounter one, but this is the general guard)
// leaves it on its next decision and keeps swimming.
func TestWaterfowlWandersOutOfAHuddle(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makePondTerrain())
	seedDuckSprite(handles)
	const duckID sim.ActorID = "duck-actor-0001"
	const peerID sim.ActorID = "peer-actor-0001"
	// Both stand on the pond's shore band, where a live duck huddles.
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		duckID: {
			ID: duckID, DisplayName: "Duck",
			Kind:     sim.KindDecorative,
			SpriteID: duckSpriteID,
			Pos:      sim.Position{X: pondMinX + 1, Y: pondMinY - 1},
			Facing:   "south",
		},
		peerID: {
			ID: peerID, DisplayName: "Peer",
			Kind:  sim.KindNPCStateful,
			State: sim.StateIdle,
			Pos:   sim.Position{X: pondMinX + 2, Y: pondMinY - 1},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Wait for the world goroutine to actually exit rather than just cancelling
	// it, so this test can't leak a goroutine into the ones that follow.
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	defer func() { cancel(); <-done }()

	now := time.Now()
	// StartOutdoorHuddle does not itself gate on Kind — that gate lives in the
	// cascade's encounter filter. Calling it directly is exactly the "some
	// other huddle path reached a duck" case this guard exists for.
	if _, err := w.Send(sim.StartOutdoorHuddle(
		[]sim.ActorID{duckID, peerID},
		sim.Position{X: pondMinX + 1, Y: pondMinY - 1},
		4, nil, now,
	)); err != nil {
		t.Fatalf("StartOutdoorHuddle: %v", err)
	}

	// Assertions run OUTSIDE the world command on purpose. A t.Fatal inside
	// Command.Fn calls runtime.Goexit on the world goroutine, so a regression
	// here would hang the package instead of reporting — the failure mode is
	// worth avoiding on a test whose whole subject is a freeze.
	type wanderResult struct {
		huddledAtSetup bool
		intentAtSetup  bool
		movedAfter     bool
		stillHuddled   sim.HuddleID
		peerSpoke      bool
	}
	res := runOn(t, w, func(world *sim.World) (any, error) {
		huddleID := world.Actors[duckID].CurrentHuddleID
		out := wanderResult{
			huddledAtSetup: huddleID != "",
			// Captured so movedAfter below is unambiguously the work of THIS
			// decision rather than a leftover intent from an earlier movement.
			intentAtSetup: world.Actors[duckID].MoveIntent != nil,
		}
		// Same reasoning for the peer's speech: compare before against after,
		// so the assertion measures what the LEAVE caused rather than whatever
		// the peer's utterance stamp happened to hold already.
		peerSpokeBefore := time.Time{}
		if h := world.Huddles[huddleID]; h != nil {
			peerSpokeBefore = h.LastUtteranceAtBy(peerID)
		}

		// First pass stamps the dwell; the second is past it and must decide.
		sim.EvaluateWaterfowl(world, now)
		sim.EvaluateWaterfowl(world, now.Add(13*time.Second))

		duck := world.Actors[duckID]
		out.movedAfter = duck.MoveIntent != nil
		out.stillHuddled = duck.CurrentHuddleID
		// The duck's departure emits HuddleLeft (the peer remains, so the
		// huddle is not concluded and is still readable). Nothing should have
		// made the peer speak on the way out.
		if h := world.Huddles[huddleID]; h != nil {
			out.peerSpoke = !h.LastUtteranceAtBy(peerID).Equal(peerSpokeBefore)
		}
		return out, nil
	}).(wanderResult)

	if !res.huddledAtSetup {
		t.Fatal("setup: duck should be huddled before the wander runs")
	}
	if res.intentAtSetup {
		t.Fatal("setup: duck should hold no MoveIntent before the wander runs, or the move assertion below proves nothing")
	}
	if !res.movedAfter {
		t.Error("a huddled duck must still dispatch a wander leg — it froze until the 2h silence sweep before LLM-582")
	}
	if res.stillHuddled != "" {
		t.Errorf("the wander should have left the huddle; duck still in %q", res.stillHuddled)
	}
	if res.peerSpoke {
		t.Error("a decorative leaving a huddle must not make its peer speak")
	}
}
