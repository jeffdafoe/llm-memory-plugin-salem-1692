package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// pc_me_my_room_test.go — LLM-847. /pc/me names the bedroom the player holds at
// the building they are in, beside vacant_rooms: the door the client marks with
// an arrow in the upstairs hall.

// The Tavern: its upstairs plans are keyed by this id (roomlayout).
const myRoomTavern sim.StructureID = "019dbcd2-c0b1-7bf9-98c2-0610cfb7f5e9"

func myRoomWorld(t *testing.T, inside sim.StructureID, grantEnds time.Time) *sim.World {
	t.Helper()
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)
	_, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Structures[myRoomTavern] = &sim.Structure{ID: myRoomTavern, DisplayName: "Tavern", Rooms: []*sim.Room{
			{ID: 16, StructureID: myRoomTavern, Kind: sim.RoomKindCommon, Name: "common"},
			{ID: 17, StructureID: myRoomTavern, Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
			{ID: 18, StructureID: myRoomTavern, Kind: sim.RoomKindPrivate, Name: "bedroom_2"},
			{ID: 31, StructureID: myRoomTavern, Kind: sim.RoomKindHall, Name: "hall"},
		}}
		world.Structures["shed"] = &sim.Structure{ID: "shed", DisplayName: "Shed"}
		world.Actors["p1"] = &sim.Actor{
			ID: "p1", DisplayName: "Tester", Kind: sim.KindPC, State: sim.StateIdle, LoginUsername: "tester",
			Pos: sim.TilePos{X: 10, Y: 10}, InsideStructureID: inside,
			RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: 17, Source: sim.AccessSourceLedger}: ledgerGrant(17, grantEnds),
			},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return w
}

func TestHandlePCMe_MyRoom(t *testing.T) {
	soon := time.Now().UTC().Add(6 * time.Hour)
	for _, tc := range []struct {
		name   string
		inside sim.StructureID
		ends   time.Time
		want   string
	}{
		{"in the Tavern holding bedroom_1", myRoomTavern, soon, "bedroom_1"},
		{"the stay has run out", myRoomTavern, time.Now().UTC().Add(-time.Hour), ""},
		{"in a building with no upstairs", "shed", soon, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := pcMe(t, NewServer(myRoomWorld(t, tc.inside, tc.ends), okAuth{}))
			if resp.MyRoom != tc.want {
				t.Errorf("my_room = %q, want %q", resp.MyRoom, tc.want)
			}
		})
	}
}
