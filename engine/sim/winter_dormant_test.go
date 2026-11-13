package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// seasonApple is the Apple Tree as migrations LLM-623/624 and LLM-967e shape it:
// berries / bare in summer, both again in autumn, and four winter looks that
// exist only picked (tagged bare), because apples are none in winter.
var seasonApple = seasonAsset("apple", "berries",
	map[string][]string{"berries": {sim.TagBerries}, "bare": {sim.TagBare}},
	map[string]string{
		"autumn-berries": "autumn", "autumn-bare": "autumn",
		"winter-clean": "winter-clean", "winter-leaves": "winter-leaves",
		"winter-snowy-top": "winter-snowy-top", "winter-snowy": "winter-snowy",
	},
	map[string][]string{
		"autumn-berries": {sim.TagBerries}, "autumn-bare": {sim.TagBare},
		"winter-clean": {sim.TagBare}, "winter-leaves": {sim.TagBare},
		"winter-snowy-top": {sim.TagBare}, "winter-snowy": {sim.TagBare},
	})

// TestWinterDormant_AppleTreeThroughTheYear is the LLM-967e pin: with the default
// factors (apples=0) a full apple tree keeps its apples through November, loses
// them on the first regen tick of December and shows its bare winter look, stays
// empty all winter even when stock is put on it, and in spring regrows a full
// period from the last winter tick, not from its last picking.
func TestWinterDormant_AppleTreeThroughTheYear(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tz data unavailable: %v", err)
	}
	factors, err := sim.ParseWinterRegrowFactors(sim.DefaultWinterRegrowFactors)
	if err != nil {
		t.Fatalf("default factors: %v", err)
	}
	repo, handles := mem.NewRepository()
	handles.Environment.Seed(sim.WorldEnvironment{}, sim.PhaseDay,
		sim.WorldSettings{Location: loc, WinterRegrowFactors: factors})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{"apple": seasonApple, "blueberry": seasonBlueberry})
	picked := time.Date(2026, 11, 20, 17, 0, 0, 0, time.UTC)
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"apple": {ID: "apple", AssetID: "apple", CurrentState: "autumn-berries",
			Refreshes: []*sim.ObjectRefresh{{GatherItem: "apples", AvailableQuantity: ip(3), MaxQuantity: ip(3),
				RefreshMode: sim.RefreshModePeriodic, RefreshPeriodHours: ip(168), LastRefreshAt: tp(picked)}}},
		// Blueberries are slowed in winter (200), not stopped: the bush keeps its stock.
		"bush": {ID: "bush", AssetID: "blueberry", CurrentState: "autumn-berries",
			Refreshes: []*sim.ObjectRefresh{{GatherItem: "blueberries", AvailableQuantity: ip(3), MaxQuantity: ip(3),
				RefreshMode: sim.RefreshModePeriodic, RefreshPeriodHours: ip(168), LastRefreshAt: tp(picked)}}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	type seen struct {
		apples, berries int
		state, bush     string
		anchor          time.Time
	}
	// tick runs one regen pass at now; put (when >= 0) first sets the apple stock,
	// as an umbilical grant or a hand edit would.
	tick := func(now time.Time, put int) seen {
		got, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			row := world.VillageObjects["apple"].Refreshes[0]
			if put >= 0 {
				row.AvailableQuantity = &put
			}
			sim.RegenObjectRefresh(world, now)
			return seen{
				apples:  *row.AvailableQuantity,
				berries: *world.VillageObjects["bush"].Refreshes[0].AvailableQuantity,
				state:   world.VillageObjects["apple"].CurrentState,
				bush:    world.VillageObjects["bush"].CurrentState,
				anchor:  *row.LastRefreshAt,
			}, nil
		}})
		return got.(seen)
	}

	// 04:00 UTC on December 1 is still November 30 in the village: autumn.
	if s := tick(time.Date(2026, 12, 1, 4, 0, 0, 0, time.UTC), -1); s.apples != 3 || s.state != "autumn-berries" {
		t.Errorf("Nov 30: apples %d state %q, want 3 autumn-berries", s.apples, s.state)
	}
	dec1 := time.Date(2026, 12, 1, 17, 0, 0, 0, time.UTC)
	s := tick(dec1, -1)
	if s.apples != 0 || s.state != "winter-leaves" || !s.anchor.Equal(dec1) {
		t.Errorf("Dec 1: apples %d state %q anchor %v, want 0 winter-leaves %v", s.apples, s.state, s.anchor, dec1)
	}
	if s.berries != 3 || s.bush != "autumn-berries" {
		t.Errorf("Dec 1 bush: berries %d state %q, want 3 autumn-berries (slowed, not stopped)", s.berries, s.bush)
	}
	// Stock put on a tree in winter is gone on the next tick.
	if s := tick(time.Date(2027, 1, 10, 17, 0, 0, 0, time.UTC), 2); s.apples != 0 || s.state != "winter-clean" {
		t.Errorf("Jan 10 with 2 put on: apples %d state %q, want 0 winter-clean", s.apples, s.state)
	}
	// The last winter tick: 03:00 UTC on April 1 is March 31 in the village.
	lastWinter := time.Date(2027, 4, 1, 3, 0, 0, 0, time.UTC)
	tick(lastWinter, -1)
	// Spring: picked summer look, no apples until a full period after that tick.
	if s := tick(lastWinter.Add(167*time.Hour), -1); s.apples != 0 || s.state != "bare" {
		t.Errorf("a week into April, short of the period: apples %d state %q, want 0 bare", s.apples, s.state)
	}
	if s := tick(lastWinter.Add(168*time.Hour), -1); s.apples != 3 || s.state != "berries" {
		t.Errorf("a full period into spring: apples %d state %q, want 3 berries", s.apples, s.state)
	}
}
