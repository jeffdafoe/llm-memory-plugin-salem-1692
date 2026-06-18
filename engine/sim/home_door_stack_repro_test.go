package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// LLM-9 repro/diagnostic. Recreates the live umbilical /deadlocks cases — a
// structure_enter whose single door tile is occupied by a non-yielding actor
// (Zechariah → home blocked by a sleeping housemate; buyers → Inn blocked by the
// keeper on the door) — to answer one question: does the mover EVER enter, or
// strand forever? The corridor walls force a single-file approach with NO detour,
// matching the live replan_failed:true shape (masking the sole approach/door tile
// leaves no alternate path). If any case logs STRANDED it's a terminal bug; if all
// enter, the existing HOME-327/348 walk-through self-resolves it (churn, not strand).
//
// Geometry — anchor/door D at (PadX+10, PadY+10), door offset (0,0):
//
//	        x=9    x=10          x=11
//	y=9    WALL    WALL          WALL
//	y=10   WALL    D (door)      WALL
//	y=11   WALL    approach      WALL
//	y=12   WALL    walker start  WALL
//	y=13   WALL    (open)        WALL
func buildDoorStackWorld(t *testing.T, walkerHome sim.StructureID, blockApproach bool) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"home-asset": {ID: "home-asset", Category: "structure", DoorOffsetX: intp(0), DoorOffsetY: intp(0)},
		"wall":       {ID: "wall", IsObstacle: true},
	})

	worldPos := func(gp sim.GridPoint) sim.WorldPos {
		c := sim.TileToWorld(gp)
		return sim.WorldPos{X: c.X, Y: c.Y}
	}
	door := sim.GridPoint{X: sim.PadX + 10, Y: sim.PadY + 10}
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		"home": {ID: "home", AssetID: "home-asset", Pos: worldPos(door)},
	}
	// Box the single-file column: walls on both flanks for rows 9..13 plus the
	// north cap, so the door is reachable ONLY from the approach tile below it,
	// and masking that tile (or the door) leaves no detour (replan_failed).
	wallTiles := []sim.GridPoint{{X: sim.PadX + 10, Y: sim.PadY + 9}}
	for y := sim.PadY + 9; y <= sim.PadY+13; y++ {
		wallTiles = append(wallTiles,
			sim.GridPoint{X: sim.PadX + 9, Y: y},
			sim.GridPoint{X: sim.PadX + 11, Y: y},
		)
	}
	for i, gp := range wallTiles {
		id := sim.VillageObjectID("wall-" + string(rune('a'+i)))
		objects[id] = &sim.VillageObject{ID: id, AssetID: "wall", Pos: worldPos(gp)}
	}
	handles.VillageObjects.Seed(objects)
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"home": {ID: "home", DisplayName: "Home"},
	})

	future := time.Now().Add(time.Hour)
	actors := map[sim.ActorID]*sim.Actor{
		// Mover starts two tiles south of the door. HomeStructureID == "home"
		// makes it a member (HOME-348 fast-path eligible); "" models a
		// non-member buyer entering an open structure.
		"walker": {ID: "walker", DisplayName: "Walker", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, HomeStructureID: walkerHome,
			Pos: sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 12}},
		// A non-yielding occupant ON the door tile (no MoveIntent → never moves).
		"door-sleeper": {ID: "door-sleeper", DisplayName: "Door Sleeper", Kind: sim.KindNPCShared,
			State: sim.StateResting, SleepingUntil: &future,
			Pos: sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 10}},
	}
	if blockApproach {
		// A second sleeper on the approach tile makes it a DEEP stack: the mover
		// can't even reach the door-adjacent tile, so HOME-348's fast-path gate
		// (occupiedNext == door) never holds until the approach clears.
		actors["approach-sleeper"] = &sim.Actor{ID: "approach-sleeper", DisplayName: "Approach Sleeper",
			Kind: sim.KindNPCShared, State: sim.StateResting, SleepingUntil: &future,
			Pos: sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 11}}
	}
	handles.Actors.Seed(actors)

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

func TestLLM9_DoorStackEntry(t *testing.T) {
	cases := []struct {
		name       string
		walkerHome sim.StructureID
		deepStack  bool
	}{
		{"member_adjacent_door", "home", false}, // HOME-348 member fast-path
		{"member_deep_stack", "home", true},     // Zechariah → home (sleeping housemate stack)
		{"nonmember_open_structure", "", false}, // Inn buyer blocked by keeper on the door
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			w, cancel := buildDoorStackWorld(t, tc.walkerHome, tc.deepStack)
			defer cancel()
			now := time.Now().UTC()

			if _, err := w.Send(sim.MoveActor("walker", sim.NewStructureEnterDestination("home"), false, now)); err != nil {
				t.Fatalf("MoveActor: %v", err)
			}

			const maxTicks = 40
			entered := 0
			for i := 1; i <= maxTicks; i++ {
				tickLoco(t, w, now)
				if _, inside := actorSpatial(t, w, "walker"); inside == "home" {
					entered = i
					break
				}
			}

			walkerDeadlocks, replanFailed := 0, false
			for _, d := range w.DeadlockSnapshot() {
				if d.MoverID == "walker" {
					walkerDeadlocks++
					replanFailed = replanFailed || d.ReplanFailed
				}
			}
			t.Logf("entered=tick%d  walker-deadlock-records=%d  replanFailed=%v", entered, walkerDeadlocks, replanFailed)

			if entered == 0 {
				_, inside := actorSpatial(t, w, "walker")
				t.Fatalf("STRANDED: walker never entered within %d ticks (InsideStructureID=%q)", maxTicks, inside)
			}
			// LLM-9 fix: a single-file door entry (replanFailed) walks through the
			// immediate blocker right away, so the mover enters within a couple ticks
			// and never burns the DeadlockStuckThreshold window or emits a spurious
			// /deadlocks record — member or not, adjacent or deep-stacked.
			if entered > 4 {
				t.Errorf("entered in %d ticks, want <= 4 (immediate walk-through, not the %d-tick stuck window)", entered, sim.DeadlockStuckThreshold)
			}
			if walkerDeadlocks != 0 {
				t.Errorf("walker recorded %d /deadlocks entries, want 0 (a single-file door entry must not reach the stuck-window record)", walkerDeadlocks)
			}
		})
	}
}
