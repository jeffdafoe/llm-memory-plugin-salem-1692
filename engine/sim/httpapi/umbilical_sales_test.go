package httpapi

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ring builds a price-book ring buffer from the given observations.
func sellThroughRing(obs ...sim.PriceObservation) *sim.RingBuffer[sim.PriceObservation] {
	b := sim.NewRingBuffer[sim.PriceObservation](sim.PriceBookRingCapacity)
	for _, o := range obs {
		b.Push(o)
	}
	return b
}

func TestUmbilicalSellThroughFromSnapshot_NilSnapshot(t *testing.T) {
	out := umbilicalSellThroughFromSnapshot(nil, "", "", 7*24*time.Hour)
	if out.Total != 0 || len(out.Rows) != 0 {
		t.Fatalf("nil snapshot should yield empty rows, got %+v", out)
	}
	if out.Rows == nil {
		t.Error("Rows should be a non-nil empty slice (clean JSON [])")
	}
	if out.ContractVersion != ContractVersion {
		t.Errorf("ContractVersion = %d, want %d", out.ContractVersion, ContractVersion)
	}
}

// TestUmbilicalSellThroughFromSnapshot_Aggregates: per-key in-window aggregation —
// Qty×Consumers units, sale count, coins, distinct buyers — with stale (out-of-
// window) observations excluded and rows sorted highest-throughput first.
func TestUmbilicalSellThroughFromSnapshot_Aggregates(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	snap := &sim.Snapshot{
		PublishedAt: now,
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "josiah", Item: "milk"}: sellThroughRing(
				sim.PriceObservation{BuyerID: "old", Amount: 99, Qty: 99, Consumers: 1, At: now.Add(-10 * 24 * time.Hour)}, // stale
				sim.PriceObservation{BuyerID: "b1", Amount: 12, Qty: 2, Consumers: 3, At: now.Add(-2 * 24 * time.Hour)},    // 6 units
				sim.PriceObservation{BuyerID: "b2", Amount: 8, Qty: 4, Consumers: 1, At: now.Add(-1 * time.Hour)},          // 4 units
			),
			{SellerID: "elizabeth", Item: "milk"}: sellThroughRing(
				sim.PriceObservation{BuyerID: "b3", Amount: 5, Qty: 1, Consumers: 1, At: now.Add(-3 * time.Hour)}, // 1 unit
			),
		},
	}
	out := umbilicalSellThroughFromSnapshot(snap, "", "", 7*24*time.Hour)
	if out.WindowHours != 168 {
		t.Errorf("WindowHours = %d, want 168", out.WindowHours)
	}
	if out.Total != 2 || len(out.Rows) != 2 {
		t.Fatalf("want 2 rows, got %+v", out.Rows)
	}
	// Highest throughput first → josiah's milk (10 units) leads elizabeth's (1).
	j := out.Rows[0]
	if j.SellerID != "josiah" || j.ItemKind != "milk" {
		t.Fatalf("row[0] = %+v, want josiah/milk leading", j)
	}
	if j.UnitsSold != 10 {
		t.Errorf("UnitsSold = %d, want 10 (6+4 in-window, stale 99 excluded)", j.UnitsSold)
	}
	if j.SalesCount != 2 {
		t.Errorf("SalesCount = %d, want 2", j.SalesCount)
	}
	if j.Coins != 20 {
		t.Errorf("Coins = %d, want 20 (12+8)", j.Coins)
	}
	if j.DistinctBuyers != 2 {
		t.Errorf("DistinctBuyers = %d, want 2 (b1, b2)", j.DistinctBuyers)
	}
	if !j.NewestAt.Equal(now.Add(-1*time.Hour)) || !j.OldestAt.Equal(now.Add(-2*24*time.Hour)) {
		t.Errorf("span = [%s..%s], want [-2d..-1h]", j.OldestAt, j.NewestAt)
	}
}

// TestUmbilicalSellThroughFromSnapshot_Filters: the actor (seller) and item query
// filters narrow the result; a key with no in-window sale is omitted.
func TestUmbilicalSellThroughFromSnapshot_Filters(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	snap := &sim.Snapshot{
		PublishedAt: now,
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "josiah", Item: "milk"}: sellThroughRing(
				sim.PriceObservation{BuyerID: "b1", Amount: 8, Qty: 4, Consumers: 1, At: now.Add(-1 * time.Hour)},
			),
			{SellerID: "josiah", Item: "salt"}: sellThroughRing(
				sim.PriceObservation{BuyerID: "b2", Amount: 3, Qty: 1, Consumers: 1, At: now.Add(-1 * time.Hour)},
			),
			{SellerID: "elizabeth", Item: "milk"}: sellThroughRing(
				sim.PriceObservation{BuyerID: "b3", Amount: 5, Qty: 1, Consumers: 1, At: now.Add(-1 * time.Hour)},
			),
		},
	}
	out := umbilicalSellThroughFromSnapshot(snap, "josiah", "milk", 7*24*time.Hour)
	if out.Total != 1 || len(out.Rows) != 1 {
		t.Fatalf("seller+item filter should yield 1 row, got %+v", out.Rows)
	}
	if out.Rows[0].SellerID != "josiah" || out.Rows[0].ItemKind != "milk" {
		t.Errorf("filtered row = %+v, want josiah/milk", out.Rows[0])
	}
}

// TestUmbilicalSellThroughFromSnapshot_AllStaleOmitted: a key whose every
// observation predates the window is dropped, not reported with a zero row.
func TestUmbilicalSellThroughFromSnapshot_AllStaleOmitted(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	snap := &sim.Snapshot{
		PublishedAt: now,
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: "josiah", Item: "milk"}: sellThroughRing(
				sim.PriceObservation{BuyerID: "old", Amount: 8, Qty: 4, Consumers: 1, At: now.Add(-30 * 24 * time.Hour)},
			),
		},
	}
	out := umbilicalSellThroughFromSnapshot(snap, "", "", 7*24*time.Hour)
	if out.Total != 0 || len(out.Rows) != 0 {
		t.Fatalf("a key with only stale sales should be omitted, got %+v", out.Rows)
	}
}

func TestParseSellThroughWindow(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", sellThroughDefaultWindowHours * time.Hour},
		{"24", 24 * time.Hour},
		{"0", sellThroughDefaultWindowHours * time.Hour},   // non-positive → default
		{"abc", sellThroughDefaultWindowHours * time.Hour}, // non-numeric → default
		{"999999", sim.PriceBookSeedWindow},                // capped at the seed window
	}
	for _, c := range cases {
		if got := parseSellThroughWindow(c.raw); got != c.want {
			t.Errorf("parseSellThroughWindow(%q) = %s, want %s", c.raw, got, c.want)
		}
	}
}
