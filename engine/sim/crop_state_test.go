package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// crop_state_test.go — LLM-576 staged crop visuals. A crop asset carries
// 'growth-1'..'growth-N' tagged states; the highest renders whenever the plant
// has stock, the lower ones walk the regrow period while it is spent.
//
// The regen sweep (RegenObjectRefresh) is the production path that advances the
// immature stages, so these tests drive it with an explicit clock rather than
// waiting on the one-minute ticker.

// cropPeriodHours is the growth period these tests use. Four immature stages
// over 120h puts a stage boundary every 30h, which is what the stage-walk test
// steps across.
const cropPeriodHours = 120

// buildCropWorld seeds a five-stage wheat asset and one plant carrying a finite
// gatherable wheat row, plus an actor on the plant's loiter pin. available is
// the starting stock, currentState the seeded visual, lastRefresh the regrow
// anchor (nil = never harvested).
func buildCropWorld(t *testing.T, available int, currentState string, lastRefresh *time.Time) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"wheat": {Name: "wheat", Category: sim.ItemCategoryMaterial},
	})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"crop-wheat": {
			ID:           "crop-wheat",
			Name:         "Wheat",
			DefaultState: "growth-5",
			States: []sim.AssetState{
				// Deliberately NOT in stage order — growthStates sorts by the tag's
				// trailing integer, and authoring order must not decide ripeness.
				{State: "growth-3", Tags: []string{"growth-3"}},
				{State: "growth-5", Tags: []string{"growth-5"}},
				{State: "growth-1", Tags: []string{"growth-1"}},
				{State: "growth-4", Tags: []string{"growth-4"}},
				{State: "growth-2", Tags: []string{"growth-2"}},
			},
		},
	})
	zero := 0
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"wheat-plant": {
			ID: "wheat-plant", DisplayName: "Wheat", AssetID: "crop-wheat", CurrentState: currentState,
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 100, Y: 100},
			Refreshes: []*sim.ObjectRefresh{
				{
					Amount:             0,
					AvailableQuantity:  ip(available),
					MaxQuantity:        ip(3),
					RefreshMode:        sim.RefreshModePeriodic,
					RefreshPeriodHours: ip(cropPeriodHours),
					LastRefreshAt:      lastRefresh,
					GatherItem:         "wheat",
				},
			},
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"farmer": {ID: "farmer", Kind: sim.KindNPCStateful, DisplayName: "Moses James"},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// plantState reads the plant's live CurrentState off the world goroutine.
func plantState(t *testing.T, w *sim.World) string {
	t.Helper()
	s, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.VillageObjects["wheat-plant"].CurrentState, nil
	}})
	if err != nil {
		t.Fatalf("read plant state: %v", err)
	}
	return s.(string)
}

// sweep runs the regen pass at now — the production path that both regrows
// supply and recomputes the derived visual.
func sweep(t *testing.T, w *sim.World, now time.Time) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.RegenObjectRefresh(world, now), nil
	}}); err != nil {
		t.Fatalf("regen sweep: %v", err)
	}
}

// TestCropStockedPlantRendersRipe: a plant with stock renders the highest stage
// whatever it was seeded as. This is the placement case — asset_refresh_default
// seeds every new placement full, so a plant dropped in the editor is ripe on
// arrival rather than only drawn that way.
func TestCropStockedPlantRendersRipe(t *testing.T) {
	w, cancel := buildCropWorld(t, 3, "growth-1", nil)
	defer cancel()

	sweep(t, w, time.Now().UTC())
	if got := plantState(t, w); got != "growth-5" {
		t.Errorf("stocked plant state = %q, want growth-5", got)
	}
}

// TestCropHarvestGoesToFirstStage: harvesting through the real Gather command
// empties the plant, so it drops to the just-cut stage. Drives the actual tool
// path rather than poking stock, because the harvest -> visual coupling is the
// whole point.
func TestCropHarvestGoesToFirstStage(t *testing.T) {
	at := time.Now().UTC()
	w, cancel := buildCropWorld(t, 3, "growth-5", nil)
	defer cancel()

	placeAtObjectPin(t, w, "farmer", "wheat-plant")
	if _, err := w.Send(sim.Gather("farmer", 3, at)); err != nil {
		t.Fatalf("gather: %v", err)
	}
	if got := plantState(t, w); got != "growth-1" {
		t.Errorf("after harvest, plant state = %q, want growth-1", got)
	}
}

// TestCropWalksStagesAcrossThePeriod: a spent plant advances one immature stage
// per period/(N-1). Four immature stages over 120h => a boundary every 30h.
func TestCropWalksStagesAcrossThePeriod(t *testing.T) {
	cut := time.Now().UTC()
	w, cancel := buildCropWorld(t, 0, "growth-1", &cut)
	defer cancel()

	for _, tc := range []struct {
		afterHours int
		want       string
	}{
		{0, "growth-1"},
		{29, "growth-1"},
		{30, "growth-2"},
		{59, "growth-2"},
		{60, "growth-3"},
		{90, "growth-4"},
		{119, "growth-4"},
	} {
		sweep(t, w, cut.Add(time.Duration(tc.afterHours)*time.Hour))
		if got := plantState(t, w); got != tc.want {
			t.Errorf("%dh after harvest, plant state = %q, want %q", tc.afterHours, got, tc.want)
		}
	}
}

// TestCropRipensWhenSupplyReturns: once the period elapses, periodic regen
// restocks the plant and the same sweep flips it to ripe — so "looks ready" and
// "can be gathered" turn true together, never one before the other.
func TestCropRipensWhenSupplyReturns(t *testing.T) {
	cut := time.Now().UTC()
	w, cancel := buildCropWorld(t, 0, "growth-1", &cut)
	defer cancel()

	sweep(t, w, cut.Add(cropPeriodHours*time.Hour))
	if got := plantState(t, w); got != "growth-5" {
		t.Errorf("after a full period, plant state = %q, want growth-5", got)
	}
	stock, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return *world.VillageObjects["wheat-plant"].Refreshes[0].AvailableQuantity, nil
	}})
	if err != nil {
		t.Fatalf("read stock: %v", err)
	}
	if stock.(int) != 3 {
		t.Errorf("after a full period, stock = %d, want 3 (ripe visual must not outrun supply)", stock)
	}
}

// TestCropStageSelectionEdges covers the branches the world path cannot reach.
// The regen sweep that would advance the clock also restocks the plant, so an
// overdue spent plant only exists between two sweeps — the clamp that keeps it
// from indexing past the immature stages is asserted directly.
func TestCropStageSelectionEdges(t *testing.T) {
	asset := &sim.Asset{
		States: []sim.AssetState{
			{State: "growth-2", Tags: []string{"growth-2"}},
			{State: "growth-1", Tags: []string{"growth-1"}},
			{State: "growth-3", Tags: []string{"growth-3"}},
		},
	}
	cut := time.Now().UTC()
	clocked := func(period *int, anchor *time.Time) *sim.ObjectRefresh {
		return &sim.ObjectRefresh{
			AvailableQuantity:  ip(0),
			MaxQuantity:        ip(3),
			RefreshMode:        sim.RefreshModePeriodic,
			RefreshPeriodHours: period,
			LastRefreshAt:      anchor,
			GatherItem:         "wheat",
		}
	}

	for _, tc := range []struct {
		name     string
		row      *sim.ObjectRefresh
		hasStock bool
		now      time.Time
		want     string
	}{
		{"stock wins regardless of clock", clocked(ip(100), &cut), true, cut, "growth-3"},
		{"overdue clamps to last immature", clocked(ip(100), &cut), false, cut.Add(1000 * time.Hour), "growth-2"},
		{"exactly at period clamps too", clocked(ip(100), &cut), false, cut.Add(100 * time.Hour), "growth-2"},
		{"no regrow clock reads just cut", clocked(nil, &cut), false, cut.Add(50 * time.Hour), "growth-1"},
		{"no anchor reads just cut", clocked(ip(100), nil), false, cut.Add(50 * time.Hour), "growth-1"},
		{"nil row reads just cut", nil, false, cut, "growth-1"},
		{"future anchor reads just cut", clocked(ip(100), timePtr(cut.Add(10 * time.Hour))), false, cut, "growth-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sim.CropStageState(asset, tc.row, tc.hasStock, tc.now); got != tc.want {
				t.Errorf("CropStageState = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCropSingleStageAssetAlwaysRendersIt: a crop authored with one growth tag
// has no immature vocabulary, so it renders that lone variant spent or not
// rather than returning empty and leaving the visual wherever it was.
func TestCropSingleStageAssetAlwaysRendersIt(t *testing.T) {
	asset := &sim.Asset{States: []sim.AssetState{{State: "growth-1", Tags: []string{"growth-1"}}}}
	for _, hasStock := range []bool{true, false} {
		if got := sim.CropStageState(asset, nil, hasStock, time.Now().UTC()); got != "growth-1" {
			t.Errorf("hasStock=%v: CropStageState = %q, want growth-1", hasStock, got)
		}
	}
}

// TestCropMalformedGrowthTagIsSkipped: a typo'd tag drops that one variant
// instead of colliding with a real stage. 'growth-x' is not a stage, so the
// ripest remains growth-2.
func TestCropMalformedGrowthTagIsSkipped(t *testing.T) {
	asset := &sim.Asset{
		States: []sim.AssetState{
			{State: "growth-1", Tags: []string{"growth-1"}},
			{State: "growth-2", Tags: []string{"growth-2"}},
			{State: "typo", Tags: []string{"growth-x"}},
		},
	}
	if got := sim.CropStageState(asset, nil, true, time.Now().UTC()); got != "growth-2" {
		t.Errorf("ripe state = %q, want growth-2 (malformed tag must not become a stage)", got)
	}
}

// TestCropSpentPlantWithNoRegrowClockShowsJustCut drives the same no-clock case
// through the world, confirming the sweep applies the selection rather than
// leaving the visual stale.
func TestCropSpentPlantWithNoRegrowClockShowsJustCut(t *testing.T) {
	cut := time.Now().UTC()
	w, cancel := buildCropWorld(t, 0, "growth-4", &cut)
	defer cancel()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.VillageObjects["wheat-plant"].Refreshes[0].RefreshPeriodHours = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("clear regrow clock: %v", err)
	}
	sweep(t, w, cut.Add(50*time.Hour))
	if got := plantState(t, w); got != "growth-1" {
		t.Errorf("spent plant with no regrow clock = %q, want growth-1 (just cut)", got)
	}
}

// timePtr is a local &t helper — time.Time has no ip() equivalent in this package.
func timePtr(t time.Time) *time.Time { return &t }

// TestBerryBushIgnoresCropPath: the binary berries/bare vocabulary is untouched
// by the crop path — an asset with no growth tags still flips on stock alone.
// Guards the LLM-12 behaviour the crop change extends.
func TestBerryBushIgnoresCropPath(t *testing.T) {
	w, cancel := buildBerryBushWorld(t, 0, "berries", nil)
	defer cancel()

	sweep(t, w, time.Now().UTC())
	if got := bushState(t, w); got != "bare" {
		t.Errorf("empty bush state = %q, want bare", got)
	}
}
