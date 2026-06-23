package httpapi

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_sales.go — LLM-63. The /api/village/umbilical/sell-through operator
// read route: per-(seller, item) recent sell-through off the published snapshot's
// price book (deep-cloned at publish, so this read is lock-free + race-free). It
// surfaces the SAME demand signal the reseller restock cue reasons against
// (engine/sim/perception/restock.go — sellerRecentSalesUnits): how fast an actor
// is moving each good. The cue only renders it for low stock; this exposes it for
// every (seller, item) the price book holds, so an operator can see turnover
// without reconstructing it from /pay-ledger by hand.
//
// Source + caveat: the price book is a per-key ring of the last
// sim.PriceBookRingCapacity (20) accepted sales, seeded on boot from pay_ledger
// over the last sim.PriceBookSeedWindow (90 days). So for a very busy vendor the
// window can hold fewer than `window_hours` of history (the ring caps first); the
// counts are then "the most recent 20," not the literal window total. At v2 scale
// that bound is rarely hit.

// SellThroughRowDTO is one (seller, item) bucket's in-window aggregate. UnitsSold
// is Qty×Consumers summed over the window — the bundle's true unit count (a
// group order moves Qty per consumer), matching the price-book doc's "Total units
// sold = Qty * Consumers" and perception.observationUnits. Coins is the sales
// revenue (coins taken in); BuyCost is the same actor's spend BUYING this item over
// the window (its restocking cost) — together a per-(actor, item) weekly P&L,
// matching the in-world restock cue (perception/restock.go, LLM-63).
type SellThroughRowDTO struct {
	SellerID       string    `json:"seller_id"`
	ItemKind       string    `json:"item_kind"`
	UnitsSold      int       `json:"units_sold"`
	SalesCount     int       `json:"sales_count"`
	Coins          int       `json:"coins"`
	BuyCost        int       `json:"buy_cost"`
	DistinctBuyers int       `json:"distinct_buyers"`
	OldestAt       time.Time `json:"oldest_at"`
	NewestAt       time.Time `json:"newest_at"`
}

// UmbilicalSellThroughDTO is the GET /sell-through response. WindowHours echoes the
// effective window so the caller knows what the counts cover.
type UmbilicalSellThroughDTO struct {
	ContractVersion int                 `json:"contract_version"`
	PublishedAt     time.Time           `json:"published_at"`
	WindowHours     int                 `json:"window_hours"`
	Total           int                 `json:"total"`
	Rows            []SellThroughRowDTO `json:"rows"`
}

// sellThroughDefaultWindowHours is the fallback window — 7 days, matching the
// reseller restock cue's restockSalesWindow so the route and the in-world cue
// report the same horizon by default. Overridable per-request via window_hours.
const sellThroughDefaultWindowHours = 7 * 24

// handleUmbilicalSellThrough dumps per-(seller, item) recent sell-through off the
// published snapshot's price book. Query params (all optional): actor (filter to
// one seller id), item (filter to one item kind), window_hours (trailing window;
// default 168). A nil snapshot (nothing published yet) yields an empty list.
func (s *Server) handleUmbilicalSellThrough(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	window := parseSellThroughWindow(q.Get("window_hours"))
	writeJSON(w, umbilicalSellThroughFromSnapshot(s.world.Published(), q.Get("actor"), q.Get("item"), window))
}

// parseSellThroughWindow parses the window_hours query param into a duration,
// falling back to the default for empty / non-numeric / non-positive input and
// capping at the price-book seed window (nothing older than that is ever
// retained, so a larger window can't return more).
func parseSellThroughWindow(raw string) time.Duration {
	hours := sellThroughDefaultWindowHours
	if raw != "" {
		if h, err := strconv.Atoi(raw); err == nil && h > 0 {
			hours = h
		}
	}
	maxHours := int(sim.PriceBookSeedWindow / time.Hour)
	if hours > maxHours {
		hours = maxHours
	}
	return time.Duration(hours) * time.Hour
}

// umbilicalSellThroughFromSnapshot aggregates the published snapshot's price book
// into per-(seller, item) in-window sell-through rows, highest-throughput first.
// Pure (no Server / world access) so it's unit-testable against a hand-built
// snapshot. A nil snapshot yields an empty list, not a panic. A key with no sale
// inside the window is omitted entirely (so the result is "who's actually moving
// stock," not every key the ring has ever seen).
func umbilicalSellThroughFromSnapshot(snap *sim.Snapshot, sellerFilter, itemFilter string, window time.Duration) UmbilicalSellThroughDTO {
	out := UmbilicalSellThroughDTO{
		ContractVersion: ContractVersion,
		WindowHours:     int(window / time.Hour),
		Rows:            []SellThroughRowDTO{},
	}
	if snap == nil {
		return out
	}
	out.PublishedAt = snap.PublishedAt
	cutoff := snap.PublishedAt.Add(-window)
	for key, buf := range snap.PriceBook {
		if buf == nil || buf.Len() == 0 {
			continue
		}
		if sellerFilter != "" && string(key.SellerID) != sellerFilter {
			continue
		}
		if itemFilter != "" && string(key.Item) != itemFilter {
			continue
		}
		row := SellThroughRowDTO{SellerID: string(key.SellerID), ItemKind: string(key.Item)}
		buyers := map[sim.ActorID]struct{}{}
		// Accumulate in int64 (clamped into the int DTO fields below) so the
		// Qty×Consumers multiply and the sums can't overflow before narrowing —
		// mirroring perception.sellerRecentSales (code_review).
		var unitsSold, coins int64
		for _, obs := range buf.Snapshot() {
			if obs.At.Before(cutoff) {
				continue
			}
			// Units = Qty×Consumers (consumers floored at 1) — the bundle's true unit
			// count, mirroring perception.observationUnits / the price-book doc.
			consumers := obs.Consumers
			if consumers < 1 {
				consumers = 1
			}
			units := int64(obs.Qty) * int64(consumers)
			if units < 1 {
				continue
			}
			unitsSold += units
			coins += int64(obs.Amount)
			row.SalesCount++
			buyers[obs.BuyerID] = struct{}{}
			if row.NewestAt.IsZero() || obs.At.After(row.NewestAt) {
				row.NewestAt = obs.At
			}
			if row.OldestAt.IsZero() || obs.At.Before(row.OldestAt) {
				row.OldestAt = obs.At
			}
		}
		if row.SalesCount == 0 {
			continue
		}
		row.UnitsSold = clampInt32(unitsSold)
		row.Coins = clampInt32(coins)
		row.DistinctBuyers = len(buyers)
		// Buyer side: what this same actor PAID restocking this item over the window
		// (its purchases across every seller), so the row carries a weekly P&L.
		row.BuyCost = buyerWindowSpend(snap.PriceBook, key.SellerID, key.Item, cutoff)
		out.Rows = append(out.Rows, row)
	}
	// Highest recent throughput first, then (seller, item) for a deterministic
	// total order regardless of map-iteration order.
	sort.Slice(out.Rows, func(i, j int) bool {
		if out.Rows[i].UnitsSold != out.Rows[j].UnitsSold {
			return out.Rows[i].UnitsSold > out.Rows[j].UnitsSold
		}
		if out.Rows[i].SellerID != out.Rows[j].SellerID {
			return out.Rows[i].SellerID < out.Rows[j].SellerID
		}
		return out.Rows[i].ItemKind < out.Rows[j].ItemKind
	})
	out.Total = len(out.Rows)
	return out
}

// buyerWindowSpend totals the coins `buyer` paid buying `item` across every seller's
// ring within the window (observations no older than cutoff). The buyer-side cost
// companion to a row's seller-side sales — price knowledge is per-buyer, so this
// scans all (seller, item) rings for the buyer's own purchases.
func buyerWindowSpend(book map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation], buyer sim.ActorID, item sim.ItemKind, cutoff time.Time) int {
	var total int64
	for key, buf := range book {
		if key.Item != item || buf == nil || buf.Len() == 0 {
			continue
		}
		for _, obs := range buf.Snapshot() {
			if obs.BuyerID == buyer && !obs.At.Before(cutoff) {
				total += int64(obs.Amount)
			}
		}
	}
	return clampInt32(total)
}

// clampInt32 narrows an int64 accumulator into the int DTO fields, saturating at the
// int32 bounds. Accumulation stays in int64 so the multiply/sum can't overflow
// before this narrowing — a corrupt or outsized observation saturates rather than
// wraps. Matches the clamp posture of the perception-side aggregators.
func clampInt32(v int64) int {
	if v > int64(math.MaxInt32) {
		return math.MaxInt32
	}
	if v < int64(math.MinInt32) {
		return math.MinInt32
	}
	return int(v)
}
