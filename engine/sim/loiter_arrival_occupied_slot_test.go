package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// loiter_arrival_occupied_slot_test.go — LLM-380 regression guard.
//
// A StructureVisit is "arrived" by proximity to the loiter pin (the pin plus its
// eight king's-move ring slots), NOT by reaching the distinct free slot
// pickVisitorSlot assigned the mover. So a visitor whose approach is funneled onto
// a slot a stationary loiterer already holds would "arrive" stacked on that
// occupant and freeze there for the whole visit — the live 2026-07-12 case of
// Josiah Thorne resting on top of Prudence Ward at the Blacksmith. The fix refuses
// arrival on a tile another actor occupies, so the mover walks on to its own free
// slot (or the unoccupied pin fallback). Overlap in MOTION stays fine; only the
// resting tile must be clear.

// buildOccupiedSlotSmithyWorld seeds a smithy whose loiter ring is walled off by
// deep water on every side except a single vertical grass corridor running south
// from the pin. Seven of the eight ring slots are water; the lone grass slot (the
// south slot, sitting on the corridor) is held by a parked occupant. A visitor
// walking up the corridor is therefore funneled straight onto the occupant's tile —
// the only way onto the ring. With 4-connected movement and water walls there is no
// detour, so the mover soft-blocks and (last resort) walk-throughs onto the occupied
// slot, reproducing the live funnel deterministically.
//
// Returns the world, its cancel, and the load-bearing tiles: the pin (the only free
// rest tile the mover can reach), the occupied south slot, and the visitor's start.
func buildOccupiedSlotSmithyWorld(t *testing.T) (w *sim.World, cancel context.CancelFunc, pin, occupiedSlot, visitorStart sim.Position) {
	t.Helper()

	// Smithy anchor tile is (PadX+10, PadY+10) — world (320,320) at 32px tiles.
	anchorX, anchorY := sim.PadX+10, sim.PadY+10
	pin = sim.Position{X: anchorX, Y: anchorY + 3}          // loiter offset (0,3), clear of the 1x1 footprint
	occupiedSlot = sim.Position{X: anchorX, Y: anchorY + 4} // south ring slot, on the corridor
	visitorStart = sim.Position{X: anchorX, Y: anchorY + 7} // three tiles down the corridor

	ter := makeAllGrassTerrain()
	water := func(x, y int) { ter.Data[y*sim.MapW+x] = sim.TerrainDeepWater }
	// Wall the seven non-corridor ring slots (everything around the pin except the
	// south slot) so pickVisitorSlot skips them (CanWalk=false) and the mover can
	// only ever resolve to the south slot or the pin fallback.
	for _, s := range []sim.Position{
		{X: anchorX - 1, Y: anchorY + 2}, {X: anchorX, Y: anchorY + 2}, {X: anchorX + 1, Y: anchorY + 2}, // NW, N, NE
		{X: anchorX - 1, Y: anchorY + 3}, {X: anchorX + 1, Y: anchorY + 3}, // W, E
		{X: anchorX - 1, Y: anchorY + 4}, {X: anchorX + 1, Y: anchorY + 4}, // SW, SE
	} {
		water(s.X, s.Y)
	}
	// Flank the corridor (south slot down to the visitor's start) so there is no
	// lateral escape — the occupant is the sole gateway onto the ring.
	for y := anchorY + 4; y <= anchorY+7; y++ {
		water(anchorX-1, y)
		water(anchorX+1, y)
	}

	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(ter)
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"smithy-asset": {ID: "smithy-asset", Category: "structure", DoorOffsetX: intp(0), DoorOffsetY: intp(0)},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"smithy": {
			ID: "smithy", AssetID: "smithy-asset", DisplayName: "Blacksmith",
			Pos: sim.WorldPos{X: 320, Y: 320}, LoiterOffsetX: intp(0), LoiterOffsetY: intp(3),
		},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"smithy": {ID: "smithy", DisplayName: "Blacksmith"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		// The stationary loiterer plugging the south slot.
		"prudence": {
			ID: "prudence", DisplayName: "Prudence Ward", Kind: sim.KindNPCStateful,
			Pos: sim.TilePos{X: occupiedSlot.X, Y: occupiedSlot.Y},
		},
		// The visitor walking up the corridor toward the smithy.
		"josiah": {
			ID: "josiah", DisplayName: "Josiah Thorne", Kind: sim.KindNPCStateful,
			Pos: sim.TilePos{X: visitorStart.X, Y: visitorStart.Y},
		},
	})
	world, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancelFn := context.WithCancel(context.Background())
	go world.Run(ctx)
	return world, cancelFn, pin, occupiedSlot, visitorStart
}

// TestLoiterArrival_VisitorDoesNotRestOnOccupiedSlot is the end-to-end LLM-380
// repro: a real StructureVisit walk funneled onto an occupied slot must NOT leave
// the visitor stacked on the occupant. Pre-fix the visitor "arrives" on the south
// slot the instant the walk-through squeezes it there and freezes; post-fix it
// walks on to the free pin.
func TestLoiterArrival_VisitorDoesNotRestOnOccupiedSlot(t *testing.T) {
	w, cancel, pin, occupiedSlot, _ := buildOccupiedSlotSmithyWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.MoveActor("josiah", sim.NewStructureVisitDestination("smithy"), false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}
	driveToArrival(t, w, "josiah", now, 100)

	josiahPos, _ := actorSpatial(t, w, "josiah")
	prudencePos, _ := actorSpatial(t, w, "prudence")

	if josiahPos == prudencePos {
		t.Fatalf("visitor came to rest stacked on the occupant at %+v — the loiter ring did not keep them apart (LLM-380)", josiahPos)
	}
	if josiahPos != pin {
		t.Errorf("visitor rest tile = %+v, want the free pin %+v (the only reachable unoccupied loiter tile)", josiahPos, pin)
	}
	if prudencePos != occupiedSlot {
		t.Errorf("occupant drifted off its slot: at %+v, want %+v", prudencePos, occupiedSlot)
	}
}

// TestLoiterArrival_ArrivedAtDestinationRejectsOccupiedTile pins the invariant at
// the decision point, independent of the ticker: standing on a ring tile another
// actor holds is NOT arrival, while the free pin still is (so the HOME-329
// all-slots-blocked fallback keeps registering arrival and never loops).
func TestLoiterArrival_ArrivedAtDestinationRejectsOccupiedTile(t *testing.T) {
	w, cancel, pin, occupiedSlot, _ := buildOccupiedSlotSmithyWorld(t)
	defer cancel()

	arrivedStandingAt := func(x, y int) bool {
		res, err := w.Send(sim.Command{
			Fn: func(world *sim.World) (any, error) {
				a := world.Actors["josiah"]
				a.Pos = sim.TilePos{X: x, Y: y}
				return sim.ArrivedAtDestination(world, a, sim.NewStructureVisitDestination("smithy")), nil
			},
		})
		if err != nil {
			t.Fatalf("arrivedStandingAt(%d,%d): %v", x, y, err)
		}
		return res.(bool)
	}

	// prudence still holds occupiedSlot: standing there is within the pin radius
	// but occupied → must NOT count as arrived (else the visitor freezes stacked).
	if arrivedStandingAt(occupiedSlot.X, occupiedSlot.Y) {
		t.Errorf("ArrivedAtDestination=true on occupied ring slot %+v — visitor would freeze stacked on the occupant (LLM-380)", occupiedSlot)
	}
	// The pin is free: within radius and unoccupied → must still register arrival.
	if !arrivedStandingAt(pin.X, pin.Y) {
		t.Errorf("ArrivedAtDestination=false on the free pin %+v — the all-slots-blocked fallback must still arrive (HOME-329)", pin)
	}
}
