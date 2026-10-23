package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// seasonAsset builds an asset the way migration LLM-849 shapes the catalog: the
// summer states untagged (plus any stock tag), each season state tagged
// season-<look>. summer maps state name → stock tags; looks maps a season
// state name → its look.
func seasonAsset(id sim.AssetID, def string, summer map[string][]string, looks map[string]string, stock map[string][]string) *sim.Asset {
	a := &sim.Asset{ID: id, Name: string(id), DefaultState: def}
	n := 1
	for name, tags := range summer {
		a.States = append(a.States, sim.AssetState{ID: sim.AssetStateID(n), State: name, Tags: tags})
		n++
	}
	for name, look := range looks {
		tags := append([]string{sim.TagSeasonPrefix + look}, stock[name]...)
		a.States = append(a.States, sim.AssetState{ID: sim.AssetStateID(100 + n), State: name, Tags: tags})
		n++
	}
	return a
}

var (
	// A Mana Seed big tree: three autumn stages, four winter stages.
	seasonMaple = seasonAsset("maple", "default", map[string][]string{"default": nil}, map[string]string{
		"autumn-clean": "autumn-clean", "autumn-leaves": "autumn-leaves", "autumn-bare": "autumn-bare",
		"winter-clean": "winter-clean", "winter-leaves": "winter-leaves",
		"winter-snowy-top": "winter-snowy-top", "winter-snowy": "winter-snowy",
	}, nil)
	// A 16x16 forest prop: one autumn sheet, winter clean and snowy only.
	seasonFern = seasonAsset("fern", "default", map[string][]string{"default": nil}, map[string]string{
		"autumn": "autumn", "winter-clean": "winter-clean", "winter-snowy": "winter-snowy",
	}, nil)
	// A stock-tracked bush: autumn art for both halves, no winter art.
	seasonBlueberry = seasonAsset("blueberry", "berries",
		map[string][]string{"berries": {sim.TagBerries}, "bare": {sim.TagBare}},
		map[string]string{"autumn-berries": "autumn", "autumn-bare": "autumn"},
		map[string][]string{"autumn-berries": {sim.TagBerries}, "autumn-bare": {sim.TagBare}})
	// Sage: not stock-tracked, autumn art only.
	seasonSage = seasonAsset("sage", "default", map[string][]string{"default": nil}, map[string]string{"autumn": "autumn"}, nil)
	// A Tiny Swords swaying tree: one sheet per season.
	seasonSwayTree = seasonAsset("tree-3", "default", map[string][]string{"default": nil}, map[string]string{
		"autumn": "autumn", "winter": "winter", "winter-snowy": "winter-snowy",
	}, nil)
)

// TestSeasonState_MonthTable is Jeff's month table (LLM-834 comments 15053,
// 15054) and the per-asset fallbacks, asset class by asset class.
func TestSeasonState_MonthTable(t *testing.T) {
	cases := []struct {
		asset *sim.Asset
		stock string
		month time.Month
		tier  sim.SnowTier
		want  string
	}{
		{seasonMaple, "", time.April, sim.SnowTierNone, "default"},
		{seasonMaple, "", time.August, sim.SnowTierNone, "default"},
		{seasonMaple, "", time.September, sim.SnowTierNone, "autumn-clean"},
		{seasonMaple, "", time.October, sim.SnowTierNone, "autumn-leaves"},
		{seasonMaple, "", time.November, sim.SnowTierNone, "autumn-bare"},
		{seasonMaple, "", time.December, sim.SnowTierNone, "winter-leaves"},
		{seasonMaple, "", time.January, sim.SnowTierNone, "winter-clean"},
		{seasonMaple, "", time.March, sim.SnowTierNone, "winter-clean"},
		{seasonMaple, "", time.January, sim.SnowTierDusting, "winter-snowy-top"},
		{seasonMaple, "", time.January, sim.SnowTierLight, "winter-snowy"},
		{seasonMaple, "", time.February, sim.SnowTierDeep, "winter-snowy"},

		{seasonFern, "", time.September, sim.SnowTierNone, "autumn"},
		{seasonFern, "", time.November, sim.SnowTierNone, "autumn"},
		{seasonFern, "", time.December, sim.SnowTierNone, "winter-clean"},   // no winter-leaves sheet
		{seasonFern, "", time.January, sim.SnowTierDusting, "winter-snowy"}, // no snowy-top sheet
		{seasonFern, "", time.May, sim.SnowTierNone, "default"},

		{seasonBlueberry, sim.TagBerries, time.July, sim.SnowTierNone, "berries"},
		{seasonBlueberry, sim.TagBare, time.July, sim.SnowTierNone, "bare"},
		{seasonBlueberry, sim.TagBerries, time.October, sim.SnowTierNone, "autumn-berries"},
		{seasonBlueberry, sim.TagBare, time.October, sim.SnowTierNone, "autumn-bare"},
		{seasonBlueberry, sim.TagBerries, time.January, sim.SnowTierNone, "autumn-berries"}, // winter → autumn art
		{seasonBlueberry, sim.TagBare, time.February, sim.SnowTierDeep, "autumn-bare"},
		{seasonBlueberry, sim.TagBerries, time.April, sim.SnowTierNone, "berries"},

		{seasonSage, "", time.January, sim.SnowTierNone, "autumn"},
		{seasonSage, "", time.June, sim.SnowTierNone, "default"},

		{seasonSwayTree, "", time.September, sim.SnowTierNone, "autumn"},
		{seasonSwayTree, "", time.December, sim.SnowTierNone, "winter"},
		{seasonSwayTree, "", time.January, sim.SnowTierDusting, "winter-snowy"},
		{seasonSwayTree, "", time.June, sim.SnowTierNone, "default"},
	}
	for _, c := range cases {
		got := sim.SeasonState(c.asset, c.stock, sim.SeasonLookChain(c.month, c.tier))
		if got != c.want {
			t.Errorf("%s stock=%q %s tier=%d: state = %q, want %q", c.asset.ID, c.stock, c.month, c.tier, got, c.want)
		}
	}
}

// TestSeasonArt_SweepFlipsObjects runs the real one-minute sweep: a tree turns
// to its October art, a stocked bush to its autumn berries and, once picked
// bare, to its autumn bare art; an asset with no season art keeps its state.
func TestSeasonArt_SweepFlipsObjects(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tz data unavailable: %v", err)
	}
	repo, handles := mem.NewRepository()
	handles.Environment.Seed(sim.WorldEnvironment{}, sim.PhaseDay, sim.WorldSettings{Location: loc})
	pine := &sim.Asset{ID: "pine", Name: "pine", DefaultState: "default", States: []sim.AssetState{{ID: 1, State: "default"}}}
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"maple": seasonMaple, "blueberry": seasonBlueberry, "pine": pine,
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"maple": {ID: "maple", AssetID: "maple", CurrentState: "default"},
		"pine":  {ID: "pine", AssetID: "pine", CurrentState: "default"},
		"bush": {ID: "bush", AssetID: "blueberry", CurrentState: "berries",
			Refreshes: []*sim.ObjectRefresh{{GatherItem: "blueberries", AvailableQuantity: ip(3), MaxQuantity: ip(3),
				RefreshMode: sim.RefreshModePeriodic, RefreshPeriodHours: ip(168)}}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// 02:00 UTC on November 1 is still October 31 in the village.
	now := time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)
	states := func(drain bool) map[sim.VillageObjectID]string {
		got, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			if drain {
				zero := 0
				world.VillageObjects["bush"].Refreshes[0].AvailableQuantity = &zero
			}
			sim.RegenObjectRefresh(world, now)
			out := map[sim.VillageObjectID]string{}
			for id, obj := range world.VillageObjects {
				out[id] = obj.CurrentState
			}
			return out, nil
		}})
		return got.(map[sim.VillageObjectID]string)
	}

	got := states(false)
	for id, want := range map[sim.VillageObjectID]string{"maple": "autumn-leaves", "bush": "autumn-berries", "pine": "default"} {
		if got[id] != want {
			t.Errorf("%s = %q, want %q", id, got[id], want)
		}
	}
	if got := states(true)["bush"]; got != "autumn-bare" {
		t.Errorf("picked-bare bush = %q, want autumn-bare", got)
	}
}
