package sim_test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

func TestParseWinterRegrowFactors_DefaultRoundTrips(t *testing.T) {
	factors, err := sim.ParseWinterRegrowFactors(sim.DefaultWinterRegrowFactors)
	if err != nil {
		t.Fatalf("default does not parse: %v", err)
	}
	want := map[sim.ItemKind]int{"apples": 125, "blueberries": 200, "carrots": 150, "raspberries": 200, "sage": 200, "wheat": 200}
	if len(factors) != len(want) {
		t.Fatalf("factors = %v, want %v", factors, want)
	}
	for item, pct := range want {
		if factors[item] != pct {
			t.Errorf("%s = %d, want %d", item, factors[item], pct)
		}
	}
	if got := sim.FormatWinterRegrowFactors(factors); got != sim.DefaultWinterRegrowFactors {
		t.Errorf("format = %q, want %q", got, sim.DefaultWinterRegrowFactors)
	}
}

func TestParseWinterRegrowFactors_TrimsAndFolds(t *testing.T) {
	factors, err := sim.ParseWinterRegrowFactors(" Sage = 300 ,, apples=100 ")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if factors["sage"] != 300 || factors["apples"] != 100 || len(factors) != 2 {
		t.Errorf("factors = %v, want sage 300, apples 100", factors)
	}
}

func TestParseWinterRegrowFactors_Rejects(t *testing.T) {
	for _, raw := range []string{
		"sage",              // no percent
		"=200",              // no item
		"sage=abc",          // not a number
		"sage=99",           // winter never speeds a regrow
		"sage=1001",         // past the cap
		"sage=200,sage=300", // named twice
	} {
		if _, err := sim.ParseWinterRegrowFactors(raw); err == nil {
			t.Errorf("ParseWinterRegrowFactors(%q) accepted, want an error", raw)
		}
	}
}

// TestRegen_WinterFactorScalesOnlyListedItems is the LLM-848 regrow pin: on a
// winter date the listed items take their factor times as long to regrow — both
// rows of an eat-and-pick bush, the periodic and the continuous mode — and every
// other row, and every row on a summer date, regrows at its stored period.
func TestRegen_WinterFactorScalesOnlyListedItems(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tz data unavailable: %v", err)
	}
	factors, err := sim.ParseWinterRegrowFactors(sim.DefaultWinterRegrowFactors)
	if err != nil {
		t.Fatalf("default factors: %v", err)
	}

	// Each row's anchor sits between its stored period and its winter period, so
	// it refills on a summer date and must NOT refill on a winter one. The water
	// row is unlisted and refills on both.
	type rowCase struct {
		id         sim.VillageObjectID
		row        sim.ObjectRefresh
		elapsed    time.Duration
		wantSummer int // AvailableQuantity after one regen pass
		wantWinter int
	}
	periodic := func(item sim.ItemKind, attr sim.NeedKey, hours, max int) sim.ObjectRefresh {
		return sim.ObjectRefresh{Attribute: attr, GatherItem: item, AvailableQuantity: ip(0), MaxQuantity: ip(max),
			RefreshMode: sim.RefreshModePeriodic, RefreshPeriodHours: ip(hours)}
	}
	cases := []rowCase{
		// Blueberry carry row: 168 h, ×2 in winter.
		{id: "blueberry-carry", row: periodic("blueberries", "", 168, 10), elapsed: 200 * time.Hour, wantSummer: 10, wantWinter: 0},
		// Blueberry eat row: the hunger row yields the same item, so it is scaled too.
		{id: "blueberry-eat", row: periodic("blueberries", "hunger", 24, 3), elapsed: 30 * time.Hour, wantSummer: 3, wantWinter: 0},
		// Apple: 168 h × 1.25 = 210 h.
		{id: "apple", row: periodic("apples", "", 168, 3), elapsed: 200 * time.Hour, wantSummer: 3, wantWinter: 0},
		// Carrots: 168 h × 1.5 = 252 h.
		{id: "carrot", row: periodic("carrots", "", 168, 2), elapsed: 240 * time.Hour, wantSummer: 2, wantWinter: 0},
		// Water is not listed: 2 h in every season.
		{id: "well", row: periodic("water", "", 2, 20), elapsed: 3 * time.Hour, wantSummer: 20, wantWinter: 20},
		// Sage is continuous: 24 h / 10 = 2.4 h a unit, 4.8 h in winter.
		{id: "sage", row: sim.ObjectRefresh{GatherItem: "sage", AvailableQuantity: ip(0), MaxQuantity: ip(10),
			RefreshMode: sim.RefreshModeContinuous, RefreshPeriodHours: ip(24)}, elapsed: 3 * time.Hour, wantSummer: 1, wantWinter: 0},
	}

	run := func(t *testing.T, now time.Time, winter bool) {
		repo, handles := mem.NewRepository()
		handles.Environment.Seed(sim.WorldEnvironment{}, sim.PhaseDay,
			sim.WorldSettings{Location: loc, WinterRegrowFactors: factors})
		objects := map[sim.VillageObjectID]*sim.VillageObject{}
		for _, c := range cases {
			row := c.row
			row.LastRefreshAt = tp(now.Add(-c.elapsed))
			objects[c.id] = &sim.VillageObject{ID: c.id, AssetID: "asset-" + sim.AssetID(c.id), CurrentState: "default",
				Refreshes: []*sim.ObjectRefresh{&row}}
		}
		handles.VillageObjects.Seed(objects)
		w, err := sim.LoadWorld(context.Background(), repo)
		if err != nil {
			t.Fatalf("LoadWorld: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go w.Run(ctx)

		got, _ := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			sim.RegenObjectRefresh(world, now)
			out := map[sim.VillageObjectID]int{}
			for id, obj := range world.VillageObjects {
				out[id] = *obj.Refreshes[0].AvailableQuantity
			}
			return out, nil
		}})
		avail := got.(map[sim.VillageObjectID]int)
		for _, c := range cases {
			want := c.wantSummer
			if winter {
				want = c.wantWinter
			}
			if avail[c.id] != want {
				t.Errorf("%s: available = %d, want %d", c.id, avail[c.id], want)
			}
		}
	}

	// 03:00 UTC on April 1 is still March 31 in the village: winter.
	t.Run("winter", func(t *testing.T) { run(t, time.Date(2027, 4, 1, 3, 0, 0, 0, time.UTC), true) })
	t.Run("summer", func(t *testing.T) { run(t, time.Date(2027, 7, 15, 16, 0, 0, 0, time.UTC), false) })
	// April 1 in the village is spring: every row back at its stored period.
	t.Run("first day of spring", func(t *testing.T) { run(t, time.Date(2027, 4, 1, 16, 0, 0, 0, time.UTC), false) })
}

// TestScaledRegrowPeriod_Bounds: scaling by 100% accepts every period an
// unscaled one did, the largest product that fits succeeds exactly, and the next
// hour up is refused rather than wrapping (code_review, LLM-848).
func TestScaledRegrowPeriod_Bounds(t *testing.T) {
	maxUnscaled := int(math.MaxInt64 / int64(time.Hour))
	if got, ok := sim.ScaledRegrowPeriod(maxUnscaled, 100); !ok || got != time.Duration(maxUnscaled)*time.Hour {
		t.Errorf("pct 100 at the unscaled max: got %v ok=%v, want %v", got, ok, time.Duration(maxUnscaled)*time.Hour)
	}
	const hundredthHour = time.Hour / 100
	for _, pct := range []int{125, 200, 1000} {
		limit := int(math.MaxInt64 / int64(hundredthHour) / int64(pct))
		got, ok := sim.ScaledRegrowPeriod(limit, pct)
		if want := time.Duration(limit) * hundredthHour * time.Duration(pct); !ok || got != want || got <= 0 {
			t.Errorf("pct %d at the limit: got %v ok=%v, want %v", pct, got, ok, want)
		}
		if got, ok := sim.ScaledRegrowPeriod(limit+1, pct); ok {
			t.Errorf("pct %d past the limit: accepted %v, want refused", pct, got)
		}
	}
	if got, ok := sim.ScaledRegrowPeriod(168, 125); !ok || got != 210*time.Hour {
		t.Errorf("168 h × 125%%: got %v ok=%v, want 210h", got, ok)
	}
	for _, in := range [][2]int{{0, 100}, {-1, 100}, {24, 0}} {
		if _, ok := sim.ScaledRegrowPeriod(in[0], in[1]); ok {
			t.Errorf("ScaledRegrowPeriod(%d, %d) accepted, want refused", in[0], in[1])
		}
	}
}

// TestCropStage_WinterFactorStretchesStages: a spent crop's growth stages are
// dated off the same scaled period the regrow uses, so in winter the plant
// walks its stages more slowly instead of sitting at the last one for weeks.
func TestCropStage_WinterFactorStretchesStages(t *testing.T) {
	asset := &sim.Asset{ID: "wheat", States: []sim.AssetState{
		{ID: 1, State: "growth-1", Tags: []string{"growth-1"}},
		{ID: 2, State: "growth-2", Tags: []string{"growth-2"}},
		{ID: 3, State: "growth-3", Tags: []string{"growth-3"}},
	}}
	cut := time.Date(2027, 1, 10, 12, 0, 0, 0, time.UTC)
	row := &sim.ObjectRefresh{GatherItem: "wheat", AvailableQuantity: ip(0), MaxQuantity: ip(3),
		RefreshMode: sim.RefreshModePeriodic, RefreshPeriodHours: ip(100), LastRefreshAt: tp(cut)}
	at := cut.Add(60 * time.Hour) // past half of 100 h, short of half of 200 h

	if got := sim.CropStageStateAtPct(asset, row, false, at, 100); got != "growth-2" {
		t.Errorf("stored period: stage = %q, want growth-2", got)
	}
	if got := sim.CropStageStateAtPct(asset, row, false, at, 200); got != "growth-1" {
		t.Errorf("winter ×2: stage = %q, want growth-1", got)
	}
	if got := sim.CropStageStateAtPct(asset, row, true, at, 200); got != "growth-3" {
		t.Errorf("stocked: stage = %q, want growth-3 (ripe whatever the season)", got)
	}
}

// TestWinterRegrowFactorsSetting_LiveWrite: the umbilical write path replaces the
// map and reads back in the stored format.
func TestWinterRegrowFactorsSetting_LiveWrite(t *testing.T) {
	spec, ok := sim.SettingSpecByKey("winter_regrow_factors")
	if !ok {
		t.Fatal("winter_regrow_factors is not registered")
	}
	var ws sim.WorldSettings
	if err := spec.Apply(&ws, "wheat=300, sage=150"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := spec.Read(&ws); got != "sage=150,wheat=300" {
		t.Errorf("Read = %q, want sage=150,wheat=300", got)
	}
	if err := spec.Apply(&ws, "wheat=50"); err == nil || !strings.Contains(err.Error(), "100") {
		t.Errorf("Apply(wheat=50) err = %v, want a range error", err)
	}
	if ws.WinterRegrowFactors["wheat"] != 300 {
		t.Errorf("a refused write changed the live value: %v", ws.WinterRegrowFactors)
	}
}
