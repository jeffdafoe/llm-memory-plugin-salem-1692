package sim_test

import (
	"context"
	"errors"
	"strings"
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

// objectDisplayName reads a placed object's DisplayName through the command channel.
func objectDisplayName(t *testing.T, w *sim.World, id sim.VillageObjectID) string {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		obj := world.VillageObjects[id]
		if obj == nil {
			return "", nil
		}
		return obj.DisplayName, nil
	}})
	if err != nil {
		t.Fatalf("read object display name: %v", err)
	}
	return res.(string)
}

// configWarningsFor runs the LLM-60 advisory audit over the live world and returns
// the warnings naming id — the check a nameless source used to trip.
func configWarningsFor(t *testing.T, w *sim.World, id sim.VillageObjectID) []string {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		var mine []string
		for _, warn := range sim.ConfigWarnings(world.VillageObjects) {
			if strings.Contains(warn, string(id)) {
				mine = append(mine, warn)
			}
		}
		return mine, nil
	}})
	if err != nil {
		t.Fatalf("read config warnings: %v", err)
	}
	return res.([]string)
}

// sageForageDefaults is the asset-level template that makes a placement a working
// yield-only sage source (the shape the live Sage Bush asset carries).
func sageForageDefaults() []*sim.ObjectRefresh {
	return []*sim.ObjectRefresh{{
		Amount: 0, GatherItem: "sage",
		AvailableQuantity: intPtr(10), MaxQuantity: intPtr(10),
		RefreshMode: sim.RefreshModeContinuous, RefreshPeriodHours: intPtr(24),
	}}
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

// TestCreateVillageObject_NamesSourceFromAsset is the core LLM-398 behavior: a
// placement the asset template turns into a SOURCE takes the asset's catalog name.
// Without it the source is unreachable — resolveLoiteringObject skips nameless
// objects, so the supply regenerates where no actor can ever gather it.
func TestCreateVillageObject_NamesSourceFromAsset(t *testing.T) {
	sage := sageAsset()
	sage.RefreshDefaults = sageForageDefaults()
	w := buildRefreshDefaultWorld(t, map[sim.AssetID]*sim.Asset{"sage": sage})

	res, err := w.Send(sim.CreateVillageObject("sage", 32, 64, "", "tester"))
	if err != nil {
		t.Fatalf("create object: %v", err)
	}
	id := res.(sim.CreateObjectResult).Object.ID

	if got := objectDisplayName(t, w, id); got != "Sage Bush" {
		t.Errorf("display name = %q, want %q (the asset catalog name)", got, "Sage Bush")
	}
	// The named source must also be free of the config-warning audit it used to trip.
	if warns := configWarningsFor(t, w, id); len(warns) != 0 {
		t.Errorf("config warnings = %v, want none for a named source", warns)
	}
}

// TestCreateVillageObject_LeavesDecorationNameless pins the deliberate scope limit:
// a placement with NO refresh rows is decoration (a tree, a fence) and stays
// nameless. Naming it would make it loiter-attributable AND — since
// ResolveLoiteringObject seeds ResolveGatherSource — let scenery shadow a real bush,
// killing the gather cue near it.
func TestCreateVillageObject_LeavesDecorationNameless(t *testing.T) {
	w := buildRefreshDefaultWorld(t, map[sim.AssetID]*sim.Asset{"sage": sageAsset()})
	res, err := w.Send(sim.CreateVillageObject("sage", 32, 64, "", "tester"))
	if err != nil {
		t.Fatalf("create object: %v", err)
	}
	id := res.(sim.CreateObjectResult).Object.ID
	if got := objectDisplayName(t, w, id); got != "" {
		t.Errorf("display name = %q, want empty (decoration stays nameless)", got)
	}
}

// TestCreateVillageObject_SourceIsReachableOnceNamed is the end-to-end point of the
// fix, and the assertion that would have caught the original defect: a forageable
// placed through the authoring path must be RESOLVABLE by the gather path. Before
// the name seeding this failed silently — the object existed and its supply
// regenerated on schedule, but ResolveGatherSource skipped it forever, so an actor
// standing on top of the bush could never harvest it.
func TestCreateVillageObject_SourceIsReachableOnceNamed(t *testing.T) {
	sage := sageAsset()
	sage.RefreshDefaults = sageForageDefaults()
	w := buildRefreshDefaultWorld(t, map[sim.AssetID]*sim.Asset{"sage": sage})

	res, err := w.Send(sim.CreateVillageObject("sage", 32, 64, "", "tester"))
	if err != nil {
		t.Fatalf("create object: %v", err)
	}
	id := res.(sim.CreateObjectResult).Object.ID

	// Resolve the way an actor standing AT the bush does — that means standing on its
	// loiter pin (anchor + the effective loiter offset), not on its anchor tile, which
	// is LoiterAttributionTiles+1 away from the pin.
	got, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		obj := world.VillageObjects[id]
		offX, offY := sim.EffectiveLoiterOffset(obj, world.Assets[obj.AssetID])
		anchor := obj.Pos.Tile()
		pin := sim.TilePos{X: anchor.X + offX, Y: anchor.Y + offY}

		resolvedID, _, row := sim.ResolveGatherSource(
			world.VillageObjects, world.Assets, pin, "forager", "", nil)
		if row == nil {
			return sim.VillageObjectID(""), nil
		}
		return resolvedID, nil
	}})
	if err != nil {
		t.Fatalf("resolve gather source: %v", err)
	}
	if got.(sim.VillageObjectID) != id {
		t.Errorf("gather resolved to %q, want the placed bush %q — a nameless source is unreachable", got, id)
	}
}

// TestSetVillageObjectRefreshes_NamesSourceOnGainingRows: the other authoring path
// into the same hole — an admin adds a refresh policy to an existing nameless
// decoration, turning it into a source. It must be named there too, or set-refresh
// re-creates exactly the dead gatherable CreateVillageObject now prevents.
func TestSetVillageObjectRefreshes_NamesSourceOnGainingRows(t *testing.T) {
	w := buildRefreshDefaultWorld(t, map[sim.AssetID]*sim.Asset{"sage": sageAsset()})
	res, err := w.Send(sim.CreateVillageObject("sage", 32, 64, "", "tester"))
	if err != nil {
		t.Fatalf("create object: %v", err)
	}
	id := res.(sim.CreateObjectResult).Object.ID
	if got := objectDisplayName(t, w, id); got != "" {
		t.Fatalf("precondition: display name = %q, want empty", got)
	}

	if _, err := w.Send(sim.SetVillageObjectRefreshes(id, sageForageDefaults())); err != nil {
		t.Fatalf("set refreshes: %v", err)
	}
	if got := objectDisplayName(t, w, id); got != "Sage Bush" {
		t.Errorf("display name = %q, want %q after gaining refresh rows", got, "Sage Bush")
	}
}

// TestCreateVillageObject_InvalidAssetNameLeavesSourceNamelessAndWarns pins the
// deliberate no-truncate / no-sanitize decision. A catalog name that is not a valid
// object name (control chars, over the length limit — only reachable from a corrupt
// asset row) is left UNAPPLIED rather than quietly trimmed to fit: truncating would
// hide the bad catalog data behind a plausible-looking name. The object stays
// nameless, so ConfigWarnings surfaces it as the genuine, un-derivable data defect
// it is — which is exactly what that advisory audit is for.
func TestCreateVillageObject_InvalidAssetNameLeavesSourceNamelessAndWarns(t *testing.T) {
	for _, tc := range []struct {
		label string
		name  string
	}{
		{"control char", "Sage\x00Bush"},
		{"over the length limit", strings.Repeat("s", sim.MaxVillageObjectDisplayNameLen+1)},
		{"blank", "   "},
	} {
		t.Run(tc.label, func(t *testing.T) {
			asset := sageAsset()
			asset.Name = tc.name
			asset.RefreshDefaults = sageForageDefaults()
			w := buildRefreshDefaultWorld(t, map[sim.AssetID]*sim.Asset{"sage": asset})

			res, err := w.Send(sim.CreateVillageObject("sage", 32, 64, "", "tester"))
			if err != nil {
				t.Fatalf("create object: %v", err)
			}
			id := res.(sim.CreateObjectResult).Object.ID

			if got := objectDisplayName(t, w, id); got != "" {
				t.Errorf("display name = %q, want empty (an invalid asset name is not applied)", got)
			}
			if warns := configWarningsFor(t, w, id); len(warns) != 1 {
				t.Errorf("config warnings = %v, want exactly 1 flagging the un-nameable source", warns)
			}
		})
	}
}

// TestSetVillageObjectRefreshes_KeepsExplicitName: the fallback fills a GAP, it does
// not override intent. An operator who named the object keeps that name when its
// refresh policy is later edited.
func TestSetVillageObjectRefreshes_KeepsExplicitName(t *testing.T) {
	sage := sageAsset()
	sage.RefreshDefaults = sageForageDefaults()
	w := buildRefreshDefaultWorld(t, map[sim.AssetID]*sim.Asset{"sage": sage})

	res, err := w.Send(sim.CreateVillageObject("sage", 32, 64, "", "tester"))
	if err != nil {
		t.Fatalf("create object: %v", err)
	}
	id := res.(sim.CreateObjectResult).Object.ID
	if _, err := w.Send(sim.SetVillageObjectDisplayName(id, "Prudence's Sage Patch")); err != nil {
		t.Fatalf("set display name: %v", err)
	}

	if _, err := w.Send(sim.SetVillageObjectRefreshes(id, sageForageDefaults())); err != nil {
		t.Fatalf("set refreshes: %v", err)
	}
	if got := objectDisplayName(t, w, id); got != "Prudence's Sage Patch" {
		t.Errorf("display name = %q, want the operator's explicit name preserved", got)
	}
}
