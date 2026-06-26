package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func tradeValueRecipes() map[sim.ItemKind]*sim.ItemRecipe {
	return map[sim.ItemKind]*sim.ItemRecipe{
		"nail":    {OutputItem: "nail", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
		"skillet": {OutputItem: "skillet", OutputQty: 1, RateQty: 1, RatePerHours: 3, WholesalePrice: 5, RetailPrice: 10},
		"water":   {OutputItem: "water", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 1},
		"stew":    {OutputItem: "stew", OutputQty: 10, RateQty: 30, RatePerHours: 6, WholesalePrice: 3, RetailPrice: 5},
	}
}

func tvProducePolicy(items ...sim.ItemKind) *sim.RestockPolicy {
	var entries []sim.RestockEntry
	for _, it := range items {
		entries = append(entries, sim.RestockEntry{Item: it, Source: sim.RestockSourceProduce, Max: 20})
	}
	return &sim.RestockPolicy{Restock: entries}
}

// TestBuildTradeValue_OwnTradeRangeOnly: a producer in company sees the wholesale–
// retail range for the goods of ITS OWN trade, and nothing for a priced good it
// does not produce (own-trade-only — the no-omniscience guard).
func TestBuildTradeValue_OwnTradeRangeOnly(t *testing.T) {
	subj := &sim.ActorSnapshot{RestockPolicy: tvProducePolicy("nail", "skillet")}
	snap := &sim.Snapshot{
		Actors:  map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		Recipes: tradeValueRecipes(),
	}
	v := buildTradeValue(snap, "ezekiel", subj, true)
	if v == nil || len(v.Items) != 2 {
		t.Fatalf("want 2 own-trade items, got %+v", v)
	}
	if v.Items[0].itemKind != "nail" || v.Items[0].Low != 1 || v.Items[0].High != 2 {
		t.Errorf("nail item wrong: %+v", v.Items[0])
	}
	if v.Items[1].itemKind != "skillet" || v.Items[1].Low != 5 || v.Items[1].High != 10 {
		t.Errorf("skillet item wrong: %+v", v.Items[1])
	}
	var b strings.Builder
	renderTradeValue(&b, v)
	out := b.String()
	for _, want := range []string{"## What your wares fetch", "nail: 1 to 2 coins each.", "skillet: 5 to 10 coins each."} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
	// stew is priced but not produced by this actor — must not be valued.
	if strings.Contains(out, "stew") {
		t.Errorf("stew leaked into wares-worth (not own-trade):\n%s", out)
	}
}

// TestBuildTradeValue_SinglePriceCollapse: a good whose wholesale == retail renders
// as a single amount ("1 coin each"), not a degenerate "1 to 1" range.
func TestBuildTradeValue_SinglePriceCollapse(t *testing.T) {
	subj := &sim.ActorSnapshot{RestockPolicy: tvProducePolicy("water")}
	snap := &sim.Snapshot{
		Actors:  map[sim.ActorID]*sim.ActorSnapshot{"a": subj},
		Recipes: tradeValueRecipes(),
	}
	v := buildTradeValue(snap, "a", subj, true)
	if v == nil || len(v.Items) != 1 || v.Items[0].Low != 1 || v.Items[0].High != 1 {
		t.Fatalf("want single water item 1/1, got %+v", v)
	}
	var b strings.Builder
	renderTradeValue(&b, v)
	out := b.String()
	if !strings.Contains(out, "water: 1 coin each.") {
		t.Errorf("single-price water should read '1 coin each':\n%s", out)
	}
	if strings.Contains(out, "water: 1 to") {
		t.Errorf("single-price water should not render a range:\n%s", out)
	}
}

// TestBuildTradeValue_RecentPriceClause: when the actor has its own coin sales of a
// good in the weekly window, render appends the rounded recent realized unit price.
// 7 coins over 2 units rounds to 4 each.
func TestBuildTradeValue_RecentPriceClause(t *testing.T) {
	subj := &sim.ActorSnapshot{RestockPolicy: tvProducePolicy("nail")}
	published := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	pb := sim.NewRingBuffer[sim.PriceObservation](4)
	pb.Push(sim.PriceObservation{BuyerID: "buyer", Amount: 7, Qty: 2, Consumers: 1, At: published.Add(-24 * time.Hour)})
	snap := &sim.Snapshot{
		PublishedAt: published,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": subj},
		Recipes:     tradeValueRecipes(),
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "ezekiel", Item: "nail"}: pb,
		},
	}
	v := buildTradeValue(snap, "ezekiel", subj, true)
	if v == nil || len(v.Items) != 1 || v.Items[0].RecentUnit != 4 {
		t.Fatalf("want nail recentUnit=4, got %+v", v)
	}
	var b strings.Builder
	renderTradeValue(&b, v)
	if !strings.Contains(b.String(), "of late you have sold for about 4 coins each") {
		t.Errorf("missing recent-price clause:\n%s", b.String())
	}
}

// TestBuildTradeValue_PriceNormalization locks the wholesale/retail normalization
// edge cases: a missing wholesale or retail collapses to the single configured
// price, a reversed config is sorted into a range, and a good with no price at all
// is dropped entirely.
func TestBuildTradeValue_PriceNormalization(t *testing.T) {
	tests := []struct {
		name      string
		wholesale int
		retail    int
		wantNil   bool
		wantLow   int
		wantHigh  int
	}{
		{name: "normal range", wholesale: 1, retail: 2, wantLow: 1, wantHigh: 2},
		{name: "wholesale missing", wholesale: 0, retail: 5, wantLow: 5, wantHigh: 5},
		{name: "retail missing", wholesale: 5, retail: 0, wantLow: 5, wantHigh: 5},
		{name: "reversed", wholesale: 10, retail: 5, wantLow: 5, wantHigh: 10},
		{name: "both missing", wholesale: 0, retail: 0, wantNil: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subj := &sim.ActorSnapshot{RestockPolicy: tvProducePolicy("x")}
			snap := &sim.Snapshot{
				Actors: map[sim.ActorID]*sim.ActorSnapshot{"a": subj},
				Recipes: map[sim.ItemKind]*sim.ItemRecipe{
					"x": {OutputItem: "x", WholesalePrice: tt.wholesale, RetailPrice: tt.retail},
				},
			}
			v := buildTradeValue(snap, "a", subj, true)
			if tt.wantNil {
				if v != nil {
					t.Fatalf("want nil (no priced own-trade good), got %+v", v)
				}
				return
			}
			if v == nil || len(v.Items) != 1 {
				t.Fatalf("want one item, got %+v", v)
			}
			if got := v.Items[0]; got.Low != tt.wantLow || got.High != tt.wantHigh {
				t.Fatalf("range = %d..%d, want %d..%d", got.Low, got.High, tt.wantLow, tt.wantHigh)
			}
		})
	}
}

// TestBuildTradeValue_NotInCompanyNil: alone, there is no one to trade with, so the
// cue is suppressed regardless of own-trade goods.
func TestBuildTradeValue_NotInCompanyNil(t *testing.T) {
	subj := &sim.ActorSnapshot{RestockPolicy: tvProducePolicy("nail")}
	snap := &sim.Snapshot{
		Actors:  map[sim.ActorID]*sim.ActorSnapshot{"a": subj},
		Recipes: tradeValueRecipes(),
	}
	if v := buildTradeValue(snap, "a", subj, false); v != nil {
		t.Errorf("alone (not in company) should yield nil, got %+v", v)
	}
}

// TestBuildTradeValue_NoOwnTradeNil: an actor with no produce entries (a pure buyer)
// or no RestockPolicy at all has no own trade to value.
func TestBuildTradeValue_NoOwnTradeNil(t *testing.T) {
	subj := &sim.ActorSnapshot{RestockPolicy: buyPolicy("ale", 20)}
	snap := &sim.Snapshot{
		Actors:  map[sim.ActorID]*sim.ActorSnapshot{"a": subj},
		Recipes: tradeValueRecipes(),
	}
	if v := buildTradeValue(snap, "a", subj, true); v != nil {
		t.Errorf("non-producer should yield nil, got %+v", v)
	}
	if v := buildTradeValue(snap, "a", &sim.ActorSnapshot{}, true); v != nil {
		t.Errorf("nil RestockPolicy should yield nil, got %+v", v)
	}
}
