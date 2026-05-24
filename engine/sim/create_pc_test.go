package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// newCreatePCWorld stands up a running world with a lodging structure (an inn
// with one common + one private room, tagged "lodging") so CreatePC can grant a
// starter bedroom. withLodging=false omits the lodging object to exercise the
// unlodged soft-fail path.
func newCreatePCWorld(t *testing.T, withLodging bool) *sim.World {
	t.Helper()
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)

	if _, err := w.Send(sim.Command{Fn: func(wd *sim.World) (any, error) {
		wd.Settings.Location = time.UTC
		wd.Settings.LodgingCheckOutHour = 11
		wd.Sprites = map[sim.SpriteID]*sim.Sprite{"sprite-1": {ID: "sprite-1", Name: "Woman A"}}
		if withLodging {
			wd.Structures["inn"] = &sim.Structure{
				ID: "inn", DisplayName: "The Inn", Position: sim.TilePos{X: 8, Y: 8},
				Rooms: []*sim.Room{
					{ID: 1, StructureID: "inn", Kind: sim.RoomKindCommon, Name: "common"},
					{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
				},
			}
			wd.VillageObjects["inn"] = &sim.VillageObject{
				ID: "inn", AssetID: "inn-asset", DisplayName: "The Inn", Tags: []string{"lodging"},
			}
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed create-pc world: %v", err)
	}
	return w
}

func createPC(t *testing.T, w *sim.World, login, name, sprite string) sim.CreatePCResult {
	t.Helper()
	res, err := w.Send(sim.CreatePC(login, name, sprite, time.Now().UTC()))
	if err != nil {
		t.Fatalf("CreatePC(%s): %v", login, err)
	}
	r, ok := res.(sim.CreatePCResult)
	if !ok {
		t.Fatalf("CreatePC result type = %T", res)
	}
	return r
}

func TestCreatePC_FullLodged(t *testing.T) {
	w := newCreatePCWorld(t, true)
	r := createPC(t, w, "alice-login", "Alice", "sprite-1")

	if !r.Created {
		t.Fatal("Created = false, want true for a fresh PC")
	}
	if r.LodgingStructureID != "inn" {
		t.Errorf("LodgingStructureID = %q, want inn", r.LodgingStructureID)
	}

	a := w.Published().Actors[r.ActorID]
	if a == nil {
		t.Fatalf("actor %s not in snapshot", r.ActorID)
	}
	if a.Kind != sim.KindPC || a.LoginUsername != "alice-login" || a.DisplayName != "Alice" {
		t.Errorf("identity = {kind:%v login:%q name:%q}, want PC/alice-login/Alice", a.Kind, a.LoginUsername, a.DisplayName)
	}
	if a.SpriteID != "sprite-1" {
		t.Errorf("SpriteID = %q, want sprite-1", a.SpriteID)
	}
	if a.Coins != sim.PCStarterCoins {
		t.Errorf("Coins = %d, want %d", a.Coins, sim.PCStarterCoins)
	}
	if len(a.Needs) == 0 {
		t.Error("Needs not seeded")
	}
	if a.InsideStructureID != "inn" {
		t.Errorf("InsideStructureID = %q, want inn", a.InsideStructureID)
	}
	if a.InsideRoomID != 2 {
		t.Errorf("InsideRoomID = %d, want 2 (the private bedroom)", a.InsideRoomID)
	}
	ra, ok := a.RoomAccess[sim.RoomAccessKey{RoomID: 2, Source: sim.AccessSourceLedger}]
	if !ok || ra == nil || !ra.Active {
		t.Errorf("RoomAccess on room 2 = %+v, want an active ledger grant", ra)
	}
}

func TestCreatePC_Idempotent(t *testing.T) {
	w := newCreatePCWorld(t, true)
	first := createPC(t, w, "bob-login", "Bob", "sprite-1")
	second := createPC(t, w, "bob-login", "Bobby", "")

	if second.Created {
		t.Error("Created = true on re-create, want false (update path)")
	}
	if second.ActorID != first.ActorID {
		t.Errorf("re-create id = %q, want same as first %q", second.ActorID, first.ActorID)
	}
	a := w.Published().Actors[first.ActorID]
	if a.DisplayName != "Bobby" {
		t.Errorf("display_name = %q, want Bobby (updated)", a.DisplayName)
	}
	// Empty sprite on re-create must NOT clobber the existing one.
	if a.SpriteID != "sprite-1" {
		t.Errorf("SpriteID = %q, want sprite-1 preserved (empty re-create sprite)", a.SpriteID)
	}
	// Exactly one PC for the login (no duplicate row).
	count := 0
	for _, act := range w.Published().Actors {
		if act.Kind == sim.KindPC && act.LoginUsername == "bob-login" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("PC count for bob-login = %d, want 1", count)
	}
}

func TestCreatePC_UnknownSprite(t *testing.T) {
	w := newCreatePCWorld(t, true)
	_, err := w.Send(sim.CreatePC("carol-login", "Carol", "sprite-nope", time.Now().UTC()))
	if err != sim.ErrUnknownSprite {
		t.Fatalf("err = %v, want ErrUnknownSprite", err)
	}
}

func TestCreatePC_NoLodging(t *testing.T) {
	w := newCreatePCWorld(t, false)
	r := createPC(t, w, "dave-login", "Dave", "sprite-1")

	if !r.Created {
		t.Fatal("Created = false, want true")
	}
	if r.LodgingStructureID != "" {
		t.Errorf("LodgingStructureID = %q, want empty (no lodging placed)", r.LodgingStructureID)
	}
	a := w.Published().Actors[r.ActorID]
	if a.InsideStructureID != "" {
		t.Errorf("InsideStructureID = %q, want empty (unlodged)", a.InsideStructureID)
	}
	if a.InsideRoomID != 0 {
		t.Errorf("InsideRoomID = %d, want 0", a.InsideRoomID)
	}
	if len(a.RoomAccess) != 0 {
		t.Errorf("RoomAccess = %+v, want none", a.RoomAccess)
	}
}
