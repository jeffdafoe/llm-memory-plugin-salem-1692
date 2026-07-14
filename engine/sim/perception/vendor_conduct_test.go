package perception

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// vendor_conduct_test.go — LLM-413: unit coverage for keeperTradeSlow, the
// engine judgment gating the trade-conduct block's concession line. The render
// halves are covered by TestRenderVendorOperating_Gate and the goldens.

// tradeSlowSnap builds a minimal snapshot around a single keeper with the given
// price-book rings. published anchors the weekly window.
func tradeSlowSnap(published time.Time, keeper *sim.ActorSnapshot, book map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]) *sim.Snapshot {
	return &sim.Snapshot{
		PublishedAt: published,
		Actors:      map[sim.ActorID]*sim.ActorSnapshot{"keeper": keeper},
		PriceBook:   book,
	}
}

func saleRing(obs ...sim.PriceObservation) *sim.RingBuffer[sim.PriceObservation] {
	r := sim.NewRingBuffer[sim.PriceObservation](8)
	for _, o := range obs {
		r.Push(o)
	}
	return r
}

func TestKeeperTradeSlow(t *testing.T) {
	published := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	inWindow := published.Add(-2 * 24 * time.Hour)
	stale := published.Add(-9 * 24 * time.Hour)

	t.Run("nil-safe", func(t *testing.T) {
		if keeperTradeSlow(nil, "keeper", &sim.ActorSnapshot{}) {
			t.Error("nil snapshot should not judge slow (block never renders without a snapshot anyway)")
		}
		if keeperTradeSlow(&sim.Snapshot{}, "keeper", nil) {
			t.Error("nil actor should not judge slow")
		}
	})

	t.Run("no sales on record is slow", func(t *testing.T) {
		keeper := &sim.ActorSnapshot{}
		if !keeperTradeSlow(tradeSlowSnap(published, keeper, nil), "keeper", keeper) {
			t.Error("a keeper with no price book should read slow (nothing moved)")
		}
	})

	t.Run("stale sales beyond the window are slow", func(t *testing.T) {
		keeper := &sim.ActorSnapshot{}
		book := map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "keeper", Item: "cheese"}: saleRing(
				sim.PriceObservation{BuyerID: "b", Amount: 3, Qty: 2, Consumers: 1, At: stale},
			),
		}
		if !keeperTradeSlow(tradeSlowSnap(published, keeper, book), "keeper", keeper) {
			t.Error("sales older than restockSalesWindow should not count as moving trade")
		}
	})

	t.Run("a single unit sale of a resold ware is not slow", func(t *testing.T) {
		keeper := &sim.ActorSnapshot{}
		book := map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "keeper", Item: "cheese"}: saleRing(
				sim.PriceObservation{BuyerID: "b", Amount: 3, Qty: 1, Consumers: 1, At: inWindow},
			),
		}
		if keeperTradeSlow(tradeSlowSnap(published, keeper, book), "keeper", keeper) {
			t.Error("an in-window sale against the single-unit quantum should withhold the concession")
		}
	})

	t.Run("another seller's brisk week does not count", func(t *testing.T) {
		keeper := &sim.ActorSnapshot{}
		book := map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "rival", Item: "cheese"}: saleRing(
				sim.PriceObservation{BuyerID: "b", Amount: 3, Qty: 5, Consumers: 1, At: inWindow},
			),
		}
		if !keeperTradeSlow(tradeSlowSnap(published, keeper, book), "keeper", keeper) {
			t.Error("only the keeper's OWN sales feed its trade judgment")
		}
	})

	t.Run("produced goods measure against their batch", func(t *testing.T) {
		keeper := &sim.ActorSnapshot{
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: "nail", Source: sim.RestockSourceProduce, Max: 30},
			}},
		}
		book := map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "keeper", Item: "nail"}: saleRing(
				sim.PriceObservation{BuyerID: "b", Amount: 6, Qty: 3, Consumers: 1, At: inWindow},
			),
		}
		snap := tradeSlowSnap(published, keeper, book)
		snap.Recipes = map[sim.ItemKind]*sim.ItemRecipe{
			"nail": {OutputItem: "nail", OutputQty: 10, RateQty: 10, RatePerHours: 2},
		}
		// 3 sold against a batch of 10 → sub-batch movement → slow.
		if !keeperTradeSlow(snap, "keeper", keeper) {
			t.Error("3 units against a batch of 10 should read slow for the producer")
		}
		// A full batch's worth sold → steady → not slow.
		book[sim.PriceBookKey{SellerID: "keeper", Item: "nail"}] = saleRing(
			sim.PriceObservation{BuyerID: "b", Amount: 20, Qty: 10, Consumers: 1, At: inWindow},
		)
		if keeperTradeSlow(snap, "keeper", keeper) {
			t.Error("a batch's worth sold should read steady for the producer")
		}
	})

	t.Run("one moving ware is enough", func(t *testing.T) {
		keeper := &sim.ActorSnapshot{}
		book := map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "keeper", Item: "cheese"}: saleRing(
				sim.PriceObservation{BuyerID: "b", Amount: 3, Qty: 1, Consumers: 1, At: inWindow},
			),
			{SellerID: "keeper", Item: "wheat"}: saleRing(
				sim.PriceObservation{BuyerID: "b", Amount: 2, Qty: 1, Consumers: 1, At: stale},
			),
		}
		if keeperTradeSlow(tradeSlowSnap(published, keeper, book), "keeper", keeper) {
			t.Error("one steadily-moving ware anywhere in the book should withhold the concession")
		}
	})
}
