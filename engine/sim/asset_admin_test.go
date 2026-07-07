package sim_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildAssetWorld seeds a world with one structure asset ("bldg") carrying an
// initial door offset (1,2) + footprint, so the SetAsset* commands have a target.
// Reuses objEventCapture (village_object_admin_test.go, same sim_test package).
func buildAssetWorld(t *testing.T) (*sim.World, *objEventCapture) {
	t.Helper()
	repo, handles := mem.NewRepository()
	dx, dy := 1, 2
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"bldg": {
			ID: "bldg", Name: "Tavern", Category: "structure", DefaultState: "default",
			FootprintLeft: 1, FootprintRight: 1, FootprintTop: 0, FootprintBottom: 2,
			DoorOffsetX: &dx, DoorOffsetY: &dy,
			States: []sim.AssetState{{ID: 1, State: "default"}},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	cap := &objEventCapture{}
	w.Subscribe(cap)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)
	return w, cap
}

// assetFromWorld reads an asset off the live catalog through the command channel
// (World.Assets isn't published in the snapshot). Returns nil if absent.
func assetFromWorld(t *testing.T, w *sim.World, id sim.AssetID) *sim.Asset {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Assets[id], nil
	}})
	if err != nil {
		t.Fatalf("read asset: %v", err)
	}
	a, _ := res.(*sim.Asset)
	return a
}

func TestSetAssetDoorOffset_SetClearAndAliasCopy(t *testing.T) {
	w, cap := buildAssetWorld(t)
	x, y := 3, 4

	res, err := w.Send(sim.SetAssetDoorOffset("bldg", &x, &y))
	if err != nil {
		t.Fatalf("set door: %v", err)
	}
	out, ok := res.(sim.AssetDoorOffsetResult)
	if !ok || out.ID != "bldg" || out.X == nil || *out.X != 3 || out.Y == nil || *out.Y != 4 {
		t.Fatalf("result = %#v, want bldg (3,4)", res)
	}
	a := assetFromWorld(t, w, "bldg")
	if a.DoorOffsetX == nil || *a.DoorOffsetX != 3 || a.DoorOffsetY == nil || *a.DoorOffsetY != 4 {
		t.Errorf("catalog door = (%v,%v), want (3,4)", a.DoorOffsetX, a.DoorOffsetY)
	}
	// Stored pointer must not alias the caller's int.
	x = 99
	if got := *assetFromWorld(t, w, "bldg").DoorOffsetX; got != 3 {
		t.Errorf("door X = %d after mutating source, want 3 (must be copied)", got)
	}

	// The change emitted AssetDoorOffsetChanged carrying the new offset.
	var found *sim.AssetDoorOffsetChanged
	for _, e := range cap.snapshot() {
		if de, ok := e.(*sim.AssetDoorOffsetChanged); ok {
			found = de
		}
	}
	if found == nil {
		t.Fatal("no AssetDoorOffsetChanged emitted")
	}
	if found.AssetID != "bldg" || found.X == nil || *found.X != 3 || found.Y == nil || *found.Y != 4 {
		t.Errorf("event = %+v, want bldg (3,4)", found)
	}

	// Clear.
	if _, err := w.Send(sim.SetAssetDoorOffset("bldg", nil, nil)); err != nil {
		t.Fatalf("clear door: %v", err)
	}
	a = assetFromWorld(t, w, "bldg")
	if a.DoorOffsetX != nil || a.DoorOffsetY != nil {
		t.Errorf("catalog door = (%v,%v), want cleared", a.DoorOffsetX, a.DoorOffsetY)
	}
}

func TestSetAssetDoorOffset_HalfPairRejected(t *testing.T) {
	w, _ := buildAssetWorld(t)
	x := 3
	if _, err := w.Send(sim.SetAssetDoorOffset("bldg", &x, nil)); !errors.Is(err, sim.ErrInvalidDoorOffset) {
		t.Fatalf("err = %v, want ErrInvalidDoorOffset", err)
	}
	// The rejected command must not have mutated the catalog.
	a := assetFromWorld(t, w, "bldg")
	if a.DoorOffsetX == nil || *a.DoorOffsetX != 1 {
		t.Errorf("door X = %v after a rejected half-pair, want unchanged (1)", a.DoorOffsetX)
	}
}

func TestSetAssetDoorOffset_NotFound(t *testing.T) {
	w, _ := buildAssetWorld(t)
	x, y := 1, 1
	if _, err := w.Send(sim.SetAssetDoorOffset("ghost", &x, &y)); !errors.Is(err, sim.ErrAssetNotFound) {
		t.Fatalf("err = %v, want ErrAssetNotFound", err)
	}
}

func TestSetAssetFootprint_SetAndNegativeRejected(t *testing.T) {
	w, _ := buildAssetWorld(t)

	res, err := w.Send(sim.SetAssetFootprint("bldg", 2, 3, 1, 4))
	if err != nil {
		t.Fatalf("set footprint: %v", err)
	}
	out, ok := res.(sim.AssetFootprintResult)
	if !ok || out.Left != 2 || out.Right != 3 || out.Top != 1 || out.Bottom != 4 {
		t.Fatalf("result = %#v, want (2,3,1,4)", res)
	}
	a := assetFromWorld(t, w, "bldg")
	if a.FootprintLeft != 2 || a.FootprintRight != 3 || a.FootprintTop != 1 || a.FootprintBottom != 4 {
		t.Errorf("catalog footprint = (%d,%d,%d,%d), want (2,3,1,4)", a.FootprintLeft, a.FootprintRight, a.FootprintTop, a.FootprintBottom)
	}

	if _, err := w.Send(sim.SetAssetFootprint("bldg", 2, -1, 0, 0)); !errors.Is(err, sim.ErrInvalidFootprint) {
		t.Fatalf("err = %v, want ErrInvalidFootprint", err)
	}
	// The negative reject left the prior (2,3,1,4) intact.
	a = assetFromWorld(t, w, "bldg")
	if a.FootprintRight != 3 {
		t.Errorf("footprint right = %d after a rejected negative, want unchanged (3)", a.FootprintRight)
	}
}

func TestSetAssetStandOffset_SetAndClear(t *testing.T) {
	w, _ := buildAssetWorld(t)
	x, y := 0, -1

	if _, err := w.Send(sim.SetAssetStandOffset("bldg", &x, &y)); err != nil {
		t.Fatalf("set stand: %v", err)
	}
	a := assetFromWorld(t, w, "bldg")
	if a.StandOffsetX == nil || *a.StandOffsetX != 0 || a.StandOffsetY == nil || *a.StandOffsetY != -1 {
		t.Errorf("catalog stand = (%v,%v), want (0,-1)", a.StandOffsetX, a.StandOffsetY)
	}

	if _, err := w.Send(sim.SetAssetStandOffset("bldg", nil, nil)); err != nil {
		t.Fatalf("clear stand: %v", err)
	}
	a = assetFromWorld(t, w, "bldg")
	if a.StandOffsetX != nil || a.StandOffsetY != nil {
		t.Errorf("catalog stand = (%v,%v), want cleared", a.StandOffsetX, a.StandOffsetY)
	}
}
