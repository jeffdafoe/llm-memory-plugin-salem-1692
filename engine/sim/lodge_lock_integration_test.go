package sim_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// lodge_lock_integration_test.go — LLM-130. The end-to-end PC knock against the
// walkable world: a non-member who navigates to a lodge whose keeper has bedded
// down for the night is routed to a knock, and because the keeper is asleep the
// door goes unanswered ("the door is shut fast"). The decision logic is
// unit-tested in package sim (lodge_lock_test.go); this proves the EnterOrKnock
// site wires the lock through the real MoveActor/pathing flow a unit test can't
// (no walk grid).

// buildLockedLodgeWorld builds a walkable village with a lodging-tagged "inn" (a
// door, a staff room, a common room), its live-in keeper, and a PC stranger
// outside. Mirrors buildMoveTestWorld's shape (real terrain so MoveActor paths).
func buildLockedLodgeWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"house": {ID: "house", Category: "structure", DoorOffsetX: intp(0), DoorOffsetY: intp(2)},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"inn": {
			ID: "inn", AssetID: "house", Pos: sim.WorldPos{X: 320, Y: 320},
			Tags: []string{"lodging"}, EntryPolicy: sim.EntryPolicyOpen,
		},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"inn": {ID: "inn", DisplayName: "Inn", Rooms: []*sim.Room{
			{ID: 1, StructureID: "inn", Kind: sim.RoomKindStaff, Name: "keeper_quarters"},
			{ID: 2, StructureID: "inn", Kind: sim.RoomKindCommon, Name: "common"},
		}},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"keeper": {
			ID: "keeper", DisplayName: "Hannah", Kind: sim.KindNPCStateful,
			Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}, HomeStructureID: "inn", WorkStructureID: "inn",
		},
		"stranger": {ID: "stranger", DisplayName: "Stranger", Kind: sim.KindPC, Pos: sim.TilePos{X: sim.PadX + 4, Y: sim.PadY}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// TestEnterOrKnock_LockedLodge_PCKnocksDoorShut: the keeper beds down in its
// staff quarters (the LLM-130 lock), then a stranger clicks to enter. The lodge
// is statically "open", but the lock makes it effectively members-only, so the
// non-member knocks instead of walking in — and the asleep keeper means the
// knock goes unanswered. The knock must not wake the keeper.
func TestEnterOrKnock_LockedLodge_PCKnocksDoorShut(t *testing.T) {
	w, cancel := buildLockedLodgeWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Bed the keeper into its staff room — the LLM-29 bed-down state lodgeLocked
	// keys off. (Set the businessowner marker here too so the inn reads as its
	// own keeper's establishment.)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		k := world.Actors["keeper"]
		k.BusinessownerState = &sim.BusinessownerState{}
		sim.SetActorInsideStructure(world, k, "inn")
		// Resolve the staff room from the structure rather than hard-coding its id,
		// so the test beds the keeper where the real keeperStaffRoomAt would.
		for _, r := range world.Structures["inn"].Rooms {
			if r.Kind == sim.RoomKindStaff {
				k.InsideRoomID = r.ID
				break
			}
		}
		k.State = sim.StateSleeping
		// A future SleepingUntil so the keeper reads as abed (not awake on the
		// floor) to the establishmentHasAwakeKeeperPresent gate in lodgeLocked.
		until := now.Add(8 * time.Hour)
		k.SleepingUntil = &until
		return nil, nil
	}}); err != nil {
		t.Fatalf("bed the keeper: %v", err)
	}

	res, err := w.Send(sim.EnterOrKnock("stranger", "inn", true, now))
	if err != nil {
		t.Fatalf("EnterOrKnock: %v", err)
	}
	out := res.(sim.EnterOrKnockResult)
	if !out.Knocked {
		t.Error("want Knocked=true — a locked lodge turns the PC's entry into a knock")
	}
	if !strings.Contains(out.KnockNarration, "shut fast") {
		t.Errorf("KnockNarration = %q, want the door-shut message (keeper asleep, no answer)", out.KnockNarration)
	}

	// The click-time knock must not wake the sleeping keeper.
	asleepRes, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["keeper"].State == sim.StateSleeping, nil
	}})
	if err != nil {
		t.Fatalf("read keeper state: %v", err)
	}
	if asleep, _ := asleepRes.(bool); !asleep {
		t.Error("the knock woke the sleeping keeper; want it still asleep")
	}
}
