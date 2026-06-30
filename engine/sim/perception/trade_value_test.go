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

// TestBuildTradeValue_NoOwnTradeNil: the cue is nil when there is no priced own ware
// to value — a buy good with no catalog recipe (ale is unpriced here) yields nothing,
// as does a nil RestockPolicy. A priced resold good IS valued now (LLM-191) — that
// positive case is TestBuildTradeValue_ResoldGoodCostBasis.
func TestBuildTradeValue_NoOwnTradeNil(t *testing.T) {
	subj := &sim.ActorSnapshot{RestockPolicy: buyPolicy("ale", 20)}
	snap := &sim.Snapshot{
		Actors:  map[sim.ActorID]*sim.ActorSnapshot{"a": subj},
		Recipes: tradeValueRecipes(),
	}
	if v := buildTradeValue(snap, "a", subj, true); v != nil {
		t.Errorf("unpriced buy good should yield nil, got %+v", v)
	}
	if v := buildTradeValue(snap, "a", &sim.ActorSnapshot{}, true); v != nil {
		t.Errorf("nil RestockPolicy should yield nil, got %+v", v)
	}
}

// TestBuildTradeValue_ResoldGoodCostBasis: a pure reseller (all-buy restock) in
// company sees its resold goods valued from the recipe range AND its own recent
// per-unit purchase cost (LLM-191). 8 coins over 4 units rounds to 2 each; with no
// sale history the "sold for" clause is absent.
func TestBuildTradeValue_ResoldGoodCostBasis(t *testing.T) {
	subj := &sim.ActorSnapshot{RestockPolicy: buyPolicy("cheese", 10)}
	published := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	buys := sim.NewRingBuffer[sim.PriceObservation](4)
	buys.Push(sim.PriceObservation{BuyerID: "josiah", Amount: 8, Qty: 4, Consumers: 1, At: published.Add(-24 * time.Hour)})
	snap := &sim.Snapshot{
		PublishedAt: published,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"josiah": subj},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"cheese": {OutputItem: "cheese", WholesalePrice: 3, RetailPrice: 6},
		},
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "ellis_farm", Item: "cheese"}: buys,
		},
	}
	v := buildTradeValue(snap, "josiah", subj, true)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("want 1 resold item, got %+v", v)
	}
	if got := v.Items[0]; got.itemKind != "cheese" || got.Low != 3 || got.High != 6 || got.PaidUnit != 2 || got.RecentUnit != 0 {
		t.Fatalf("cheese item wrong: %+v", got)
	}
	var b strings.Builder
	renderTradeValue(&b, v)
	out := b.String()
	if !strings.Contains(out, "cheese: 3 to 6 coins each; you have lately paid about 2 coins each for it.") {
		t.Errorf("missing cost-basis clause:\n%s", out)
	}
	if strings.Contains(out, "sold for") {
		t.Errorf("no sale history — should not render a sold-for clause:\n%s", out)
	}
}

// TestBuildTradeValue_ResoldGoodBothClauses: a resold good the actor has BOTH bought
// (cost basis) and sold (realized price) renders both clauses — paid then sold — so
// the pair brackets the markup (LLM-191, the Q3 decision). Buy 8/4 = 2, sale 20/4 = 5.
func TestBuildTradeValue_ResoldGoodBothClauses(t *testing.T) {
	subj := &sim.ActorSnapshot{RestockPolicy: buyPolicy("cheese", 10)}
	published := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	buys := sim.NewRingBuffer[sim.PriceObservation](4)
	buys.Push(sim.PriceObservation{BuyerID: "josiah", Amount: 8, Qty: 4, Consumers: 1, At: published.Add(-24 * time.Hour)})
	sales := sim.NewRingBuffer[sim.PriceObservation](4)
	sales.Push(sim.PriceObservation{BuyerID: "martha", Amount: 20, Qty: 4, Consumers: 1, At: published.Add(-12 * time.Hour)})
	snap := &sim.Snapshot{
		PublishedAt: published,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"josiah": subj},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"cheese": {OutputItem: "cheese", WholesalePrice: 3, RetailPrice: 6},
		},
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "ellis_farm", Item: "cheese"}: buys,  // josiah as buyer (cost)
			{SellerID: "josiah", Item: "cheese"}:     sales, // josiah as seller (realized)
		},
	}
	v := buildTradeValue(snap, "josiah", subj, true)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("want 1 item, got %+v", v)
	}
	if got := v.Items[0]; got.PaidUnit != 2 || got.RecentUnit != 5 {
		t.Fatalf("want PaidUnit=2 RecentUnit=5, got %+v", got)
	}
	var b strings.Builder
	renderTradeValue(&b, v)
	if !strings.Contains(b.String(), "you have lately paid about 2 coins each for it; of late you have sold for about 5 coins each.") {
		t.Errorf("want paid-then-sold clauses in order:\n%s", b.String())
	}
}

// TestBuildTradeValue_DualSourceProducedWins: a kind listed under BOTH produce and buy
// entries is valued ONCE, as own-production — the produced loop runs first and the
// seen-set dedupe suppresses the buy pass, so no cost-basis clause attaches even with
// a purchase history (LLM-191). Pins the loop order against an accidental flip.
func TestBuildTradeValue_DualSourceProducedWins(t *testing.T) {
	subj := &sim.ActorSnapshot{RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
		{Item: "cheese", Source: sim.RestockSourceProduce, Max: 10},
		{Item: "cheese", Source: sim.RestockSourceBuy, Max: 10},
	}}}
	published := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	buys := sim.NewRingBuffer[sim.PriceObservation](4)
	buys.Push(sim.PriceObservation{BuyerID: "josiah", Amount: 8, Qty: 4, Consumers: 1, At: published.Add(-24 * time.Hour)})
	snap := &sim.Snapshot{
		PublishedAt: published,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"josiah": subj},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"cheese": {OutputItem: "cheese", WholesalePrice: 3, RetailPrice: 6},
		},
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "ellis_farm", Item: "cheese"}: buys,
		},
	}
	v := buildTradeValue(snap, "josiah", subj, true)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("want exactly 1 deduped item, got %+v", v)
	}
	if got := v.Items[0]; got.PaidUnit != 0 {
		t.Errorf("produced-source should win → PaidUnit 0, got %+v", got)
	}
	var b strings.Builder
	renderTradeValue(&b, v)
	if strings.Contains(b.String(), "lately paid") {
		t.Errorf("produced good should carry no cost-basis clause:\n%s", b.String())
	}
}
