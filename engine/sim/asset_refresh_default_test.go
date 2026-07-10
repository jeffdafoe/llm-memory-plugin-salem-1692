package sim_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildRefreshDefaultWorld seeds a running world from the given asset catalog so
// the SetAssetRefreshDefaults command and CreateVillageObject seeding have targets.
func buildRefreshDefaultWorld(t *testing.T, assets map[sim.AssetID]*sim.Asset) *sim.World {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Assets.Seed(assets)
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)
	return w
}

func intPtr(v int) *int { return &v }

// refreshSnap is a race-free copy of one refresh row's asserted fields, computed on
// the world goroutine so the test never aliases live world/catalog pointers.
type refreshSnap struct {
	attribute      string
	amount         int
	gatherItem     string
	available      *int
	max            *int
	lastRefreshNil bool
}

func snapRefreshes(rows []*sim.ObjectRefresh) []refreshSnap {
	out := make([]refreshSnap, 0, len(rows))
	for _, r := range rows {
		if r == nil {
			continue
		}
		out = append(out, refreshSnap{
			attribute:      string(r.Attribute),
			amount:         r.Amount,
			gatherItem:     string(r.GatherItem),
			available:      copyTestIntPtr(r.AvailableQuantity),
			max:            copyTestIntPtr(r.MaxQuantity),
			lastRefreshNil: r.LastRefreshAt == nil,
		})
	}
	return out
}

// assetDefaults reads an asset's RefreshDefaults off the live catalog through the
// command channel, snapshotting the asserted fields on the world goroutine.
func assetDefaults(t *testing.T, w *sim.World, id sim.AssetID) []refreshSnap {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Assets[id]
		if a == nil {
			return []refreshSnap(nil), nil
		}
		return snapRefreshes(a.RefreshDefaults), nil
	}})
	if err != nil {
		t.Fatalf("read asset defaults: %v", err)
	}
	return res.([]refreshSnap)
}

// objectRefreshSnaps reads a placed object's Refreshes through the command channel.
func objectRefreshSnaps(t *testing.T, w *sim.World, id sim.VillageObjectID) []refreshSnap {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		obj := world.VillageObjects[id]
		if obj == nil {
			return []refreshSnap(nil), nil
		}
		return snapRefreshes(obj.Refreshes), nil
	}})
	if err != nil {
		t.Fatalf("read object refreshes: %v", err)
	}
	return res.([]refreshSnap)
}

// sageAsset is a forage-to-sell asset (yield-only sage source) with no default set.
func sageAsset() *sim.Asset {
	return &sim.Asset{
		ID: "sage", Name: "Sage Bush", Category: "nature", DefaultState: "default",
		States: []sim.AssetState{{ID: 1, State: "default"}},
	}
}

// TestSetAssetRefreshDefaults_StoresFullSupply authors a default from a DEPLETED
// source (available 2 of 10) and asserts the stored template is normalized to a
// full supply (available == max) with no regen anchor — the "start full" invariant.
func TestSetAssetRefreshDefaults_StoresFullSupply(t *testing.T) {
	w := buildRefreshDefaultWorld(t, map[sim.AssetID]*sim.Asset{"sage": sageAsset()})

	rows := []*sim.ObjectRefresh{{
		Amount: 0, GatherItem: "sage",
		AvailableQuantity: intPtr(2), MaxQuantity: intPtr(10),
		RefreshMode: sim.RefreshModeContinuous, RefreshPeriodHours: intPtr(24),
	}}
	res, err := w.Send(sim.SetAssetRefreshDefaults("sage", rows))
	if err != nil {
		t.Fatalf("set defaults: %v", err)
	}
	out := res.(sim.AssetRefreshDefaultsResult)
	if out.ID != "sage" || len(out.Rows) != 1 {
		t.Fatalf("result = %+v, want sage + 1 row", out)
	}
	got := snapRefreshes(out.Rows)[0]
	if got.gatherItem != "sage" || got.available == nil || *got.available != 10 ||
		got.max == nil || *got.max != 10 || !got.lastRefreshNil {
		t.Errorf("result row = %+v, want gather sage available/max 10/10 anchor nil", got)
	}

	// The live catalog carries the same normalized template.
	stored := assetDefaults(t, w, "sage")
	if len(stored) != 1 || stored[0].available == nil || *stored[0].available != 10 {
		t.Errorf("stored defaults = %+v, want 1 row available 10", stored)
	}
}

// TestSetAssetRefreshDefaults_NotFound: an unknown asset id → ErrAssetNotFound.
func TestSetAssetRefreshDefaults_NotFound(t *testing.T) {
	w := buildRefreshDefaultWorld(t, map[sim.AssetID]*sim.Asset{"sage": sageAsset()})
	_, err := w.Send(sim.SetAssetRefreshDefaults("ghost", []*sim.ObjectRefresh{{
		Amount: 0, GatherItem: "sage", AvailableQuantity: intPtr(1), MaxQuantity: intPtr(1),
		RefreshMode: sim.RefreshModeContinuous,
	}}))
	if !errors.Is(err, sim.ErrAssetNotFound) {
		t.Fatalf("err = %v, want ErrAssetNotFound", err)
	}
}

// TestSetAssetRefreshDefaults_Invalid: a positive amount fails validation → ErrInvalidRefresh.
func TestSetAssetRefreshDefaults_Invalid(t *testing.T) {
	w := buildRefreshDefaultWorld(t, map[sim.AssetID]*sim.Asset{"sage": sageAsset()})
	_, err := w.Send(sim.SetAssetRefreshDefaults("sage", []*sim.ObjectRefresh{{
		Attribute: "thirst", Amount: 5,
	}}))
	if !errors.Is(err, sim.ErrInvalidRefresh) {
		t.Fatalf("err = %v, want ErrInvalidRefresh", err)
	}
}

// TestSetAssetRefreshDefaults_Clears: an empty set clears the asset's defaults, so a
// later placement seeds nothing (back to the pre-LLM-363 inert drop).
func TestSetAssetRefreshDefaults_Clears(t *testing.T) {
	sage := sageAsset()
	sage.RefreshDefaults = []*sim.ObjectRefresh{{
		Amount: 0, GatherItem: "sage", AvailableQuantity: intPtr(10), MaxQuantity: intPtr(10),
		RefreshMode: sim.RefreshModeContinuous, RefreshPeriodHours: intPtr(24),
	}}
	w := buildRefreshDefaultWorld(t, map[sim.AssetID]*sim.Asset{"sage": sage})

	if _, err := w.Send(sim.SetAssetRefreshDefaults("sage", nil)); err != nil {
		t.Fatalf("clear defaults: %v", err)
	}
	if got := assetDefaults(t, w, "sage"); len(got) != 0 {
		t.Errorf("defaults after clear = %+v, want empty", got)
	}
}

// TestCreateVillageObject_SeedsRefreshFromAssetDefault is the core LLM-363 behavior:
// placing an asset that carries a default template seeds the new object's refresh
// set — a full supply with no regen anchor — so it drops in working.
func TestCreateVillageObject_SeedsRefreshFromAssetDefault(t *testing.T) {
	sage := sageAsset()
	sage.RefreshDefaults = []*sim.ObjectRefresh{{
		Amount: 0, GatherItem: "sage",
		AvailableQuantity: intPtr(4), MaxQuantity: intPtr(10), // partial → normalized to full on seed
		RefreshMode: sim.RefreshModeContinuous, RefreshPeriodHours: intPtr(24),
	}}
	w := buildRefreshDefaultWorld(t, map[sim.AssetID]*sim.Asset{"sage": sage})

	res, err := w.Send(sim.CreateVillageObject("sage", 32, 64, "", "tester"))
	if err != nil {
		t.Fatalf("create object: %v", err)
	}
	id := res.(sim.CreateObjectResult).Object.ID

	got := objectRefreshSnaps(t, w, id)
	if len(got) != 1 {
		t.Fatalf("seeded refreshes = %+v, want 1 row", got)
	}
	r := got[0]
	if r.gatherItem != "sage" || r.amount != 0 ||
		r.available == nil || *r.available != 10 || r.max == nil || *r.max != 10 ||
		!r.lastRefreshNil {
		t.Errorf("seeded row = %+v, want sage yield full (10/10) with nil anchor", r)
	}
}

// TestCreateVillageObject_NoDefaultsLeavesInert: an asset without defaults places an
// inert object (no refresh rows) — the unchanged pre-LLM-363 behavior.
func TestCreateVillageObject_NoDefaultsLeavesInert(t *testing.T) {
	w := buildRefreshDefaultWorld(t, map[sim.AssetID]*sim.Asset{"sage": sageAsset()})
	res, err := w.Send(sim.CreateVillageObject("sage", 32, 64, "", "tester"))
	if err != nil {
		t.Fatalf("create object: %v", err)
	}
	id := res.(sim.CreateObjectResult).Object.ID
	if got := objectRefreshSnaps(t, w, id); len(got) != 0 {
		t.Errorf("refreshes = %+v, want none (inert placement)", got)
	}
}
