package sim_test

import (
	"context"
	"errors"
	"strings"
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

// copyTestIntPtr copies an int pointer (sim.copyIntPtr is unexported) so a test
// never holds a pointer aliasing live World.Assets state.
func copyTestIntPtr(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// assetFromWorld reads an asset off the live catalog through the command channel
// (World.Assets isn't published in the snapshot). Returns a struct copy with its
// geometry pointer fields copied on the world goroutine, so the test holds
// nothing aliasing live world state. Returns nil if absent.
func assetFromWorld(t *testing.T, w *sim.World, id sim.AssetID) *sim.Asset {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Assets[id]
		if a == nil {
			return (*sim.Asset)(nil), nil
		}
		cp := *a
		cp.DoorOffsetX = copyTestIntPtr(cp.DoorOffsetX)
		cp.DoorOffsetY = copyTestIntPtr(cp.DoorOffsetY)
		cp.StandOffsetX = copyTestIntPtr(cp.StandOffsetX)
		cp.StandOffsetY = copyTestIntPtr(cp.StandOffsetY)
		return &cp, nil
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

// assetStateTags reads a named state's tags off the live catalog through the command
// channel, copying the slice on the world goroutine so the test holds nothing
// aliasing live world state. Returns nil for an unknown asset/state.
func assetStateTags(t *testing.T, w *sim.World, id sim.AssetID, state string) []string {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Assets[id]
		if a == nil {
			return []string(nil), nil
		}
		st := a.FindState(state)
		if st == nil {
			return []string(nil), nil
		}
		return append([]string{}, st.Tags...), nil
	}})
	if err != nil {
		t.Fatalf("read state tags: %v", err)
	}
	return res.([]string)
}

func TestSetAssetVisibleWhenInside_SetFlipAndEvent(t *testing.T) {
	w, cap := buildAssetWorld(t)

	res, err := w.Send(sim.SetAssetVisibleWhenInside("bldg", true))
	if err != nil {
		t.Fatalf("set visible-when-inside: %v", err)
	}
	out, ok := res.(sim.AssetVisibleWhenInsideResult)
	if !ok || out.ID != "bldg" || !out.VisibleWhenInside {
		t.Fatalf("result = %#v, want bldg true", res)
	}
	if a := assetFromWorld(t, w, "bldg"); !a.VisibleWhenInside {
		t.Error("catalog visible_when_inside = false, want true")
	}

	var found *sim.AssetVisibleWhenInsideChanged
	for _, e := range cap.snapshot() {
		if ve, ok := e.(*sim.AssetVisibleWhenInsideChanged); ok {
			found = ve
		}
	}
	if found == nil || found.AssetID != "bldg" || !found.VisibleWhenInside {
		t.Fatalf("event = %+v, want bldg true", found)
	}

	// Flip back off.
	if _, err := w.Send(sim.SetAssetVisibleWhenInside("bldg", false)); err != nil {
		t.Fatalf("flip off: %v", err)
	}
	if a := assetFromWorld(t, w, "bldg"); a.VisibleWhenInside {
		t.Error("catalog visible_when_inside = true, want false after flip")
	}
}

func TestSetAssetVisibleWhenInside_NotFound(t *testing.T) {
	w, _ := buildAssetWorld(t)
	if _, err := w.Send(sim.SetAssetVisibleWhenInside("ghost", true)); !errors.Is(err, sim.ErrAssetNotFound) {
		t.Fatalf("err = %v, want ErrAssetNotFound", err)
	}
}

func TestAddAssetStateTag_SortedIdempotentAndEmits(t *testing.T) {
	w, cap := buildAssetWorld(t)

	if _, err := w.Send(sim.AddAssetStateTag("bldg", "default", "occupied")); err != nil {
		t.Fatalf("add occupied: %v", err)
	}
	res, err := w.Send(sim.AddAssetStateTag("bldg", "default", "day-active"))
	if err != nil {
		t.Fatalf("add day-active: %v", err)
	}
	out, ok := res.(sim.AssetStateTagsResult)
	if !ok || out.ID != "bldg" || out.State != "default" {
		t.Fatalf("result = %#v, want bldg/default", res)
	}
	// Result and catalog both carry the full set, sorted.
	if got := strings.Join(out.Tags, ","); got != "day-active,occupied" {
		t.Errorf("result tags = %q, want \"day-active,occupied\"", got)
	}
	if got := strings.Join(assetStateTags(t, w, "bldg", "default"), ","); got != "day-active,occupied" {
		t.Errorf("catalog tags = %q, want \"day-active,occupied\"", got)
	}

	// Idempotent: re-adding a present tag does not duplicate it.
	if _, err := w.Send(sim.AddAssetStateTag("bldg", "default", "occupied")); err != nil {
		t.Fatalf("re-add occupied: %v", err)
	}
	if got := strings.Join(assetStateTags(t, w, "bldg", "default"), ","); got != "day-active,occupied" {
		t.Errorf("catalog tags after re-add = %q, want no duplicate", got)
	}

	// The latest event carries a non-nil full set (marshals as [] not null).
	var found *sim.AssetStateTagsChanged
	for _, e := range cap.snapshot() {
		if te, ok := e.(*sim.AssetStateTagsChanged); ok {
			found = te
		}
	}
	if found == nil || found.AssetID != "bldg" || found.State != "default" {
		t.Fatalf("event = %+v, want bldg/default", found)
	}
	if found.Tags == nil {
		t.Error("event tags nil, want non-nil")
	}
}

func TestAddAssetStateTag_NotFound(t *testing.T) {
	w, _ := buildAssetWorld(t)
	if _, err := w.Send(sim.AddAssetStateTag("ghost", "default", "occupied")); !errors.Is(err, sim.ErrAssetNotFound) {
		t.Fatalf("unknown asset err = %v, want ErrAssetNotFound", err)
	}
	if _, err := w.Send(sim.AddAssetStateTag("bldg", "nope", "occupied")); !errors.Is(err, sim.ErrAssetStateNotFound) {
		t.Fatalf("unknown state err = %v, want ErrAssetStateNotFound", err)
	}
}

func TestRemoveAssetStateTag_RemovesAndNoOpWhenAbsent(t *testing.T) {
	w, _ := buildAssetWorld(t)
	for _, tag := range []string{"occupied", "day-active"} {
		if _, err := w.Send(sim.AddAssetStateTag("bldg", "default", tag)); err != nil {
			t.Fatalf("seed add %s: %v", tag, err)
		}
	}

	res, err := w.Send(sim.RemoveAssetStateTag("bldg", "default", "day-active"))
	if err != nil {
		t.Fatalf("remove day-active: %v", err)
	}
	if got := strings.Join(res.(sim.AssetStateTagsResult).Tags, ","); got != "occupied" {
		t.Errorf("result tags = %q, want \"occupied\"", got)
	}
	if got := strings.Join(assetStateTags(t, w, "bldg", "default"), ","); got != "occupied" {
		t.Errorf("catalog tags = %q, want \"occupied\"", got)
	}

	// Removing an absent tag succeeds and leaves the set unchanged.
	res, err = w.Send(sim.RemoveAssetStateTag("bldg", "default", "night-active"))
	if err != nil {
		t.Fatalf("remove absent: %v", err)
	}
	if got := strings.Join(res.(sim.AssetStateTagsResult).Tags, ","); got != "occupied" {
		t.Errorf("tags after removing absent = %q, want \"occupied\"", got)
	}
}

func TestRemoveAssetStateTag_NotFound(t *testing.T) {
	w, _ := buildAssetWorld(t)
	if _, err := w.Send(sim.RemoveAssetStateTag("ghost", "default", "occupied")); !errors.Is(err, sim.ErrAssetNotFound) {
		t.Fatalf("unknown asset err = %v, want ErrAssetNotFound", err)
	}
	if _, err := w.Send(sim.RemoveAssetStateTag("bldg", "nope", "occupied")); !errors.Is(err, sim.ErrAssetStateNotFound) {
		t.Fatalf("unknown state err = %v, want ErrAssetStateNotFound", err)
	}
}
