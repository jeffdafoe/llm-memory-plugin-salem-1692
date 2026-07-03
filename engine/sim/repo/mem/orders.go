package mem

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// OrdersRepo is an in-memory implementation of sim.OrdersRepo, same
// shape as ActorsRepo / HuddlesRepo. Replaces the notImplOrders stub
// (Slice 5).
//
// Mirrors how mem repos behave for tests: Seed populates initial
// state, LoadAll deep-clones via CloneOrder, SaveSnapshot replaces
// the map wholesale so the in-memory "checkpoint" matches what a
// real pg SaveSnapshot would express semantically (a complete
// re-statement of the persisted set).
//
// The prices slice is independent of the orders map — pay_ledger
// history (the source of LoadRecentPrices in pg) lives separately
// from the in-flight Order working set. Tests seed prices explicitly
// via SeedPrices; production wires the pg impl (which reads pay_ledger
// directly) instead.
type OrdersRepo struct {
	orders map[sim.OrderID]*sim.Order
	prices []sim.PriceBookSeedRecord
	// paidActionLogMaxLedgerID stands in for the pg backend's
	// agent_action_log query — the mem backend has no action_log table, so
	// tests stage the consume_now ledger high-water mark here via
	// SeedPaidActionLogMax. LLM-245.
	paidActionLogMaxLedgerID int64
}

func NewOrdersRepo() *OrdersRepo {
	return &OrdersRepo{orders: make(map[sim.OrderID]*sim.Order)}
}

func (r *OrdersRepo) Seed(orders map[sim.OrderID]*sim.Order) {
	for id, o := range orders {
		r.orders[id] = sim.CloneOrder(o)
	}
}

// SeedPrices stages the price-history observations the next
// LoadRecentPrices call returns. Used by tests to set up the
// expected price-book state without going through the cascade
// subscriber. Pass records in any order; LoadRecentPrices sorts
// to the chronological-per-key shape SeedPriceBook expects.
func (r *OrdersRepo) SeedPrices(records []sim.PriceBookSeedRecord) {
	r.prices = append(r.prices[:0:0], records...)
}

// SeedPaidActionLogMax stages the value the next MaxPaidActionLogLedgerID call
// returns. The mem backend has no agent_action_log table — paid action-log
// history lives separately from the orders map, mirroring how SeedPrices
// stages pay_ledger price history — so tests set the consume_now ledger
// high-water mark explicitly here. LLM-245.
func (r *OrdersRepo) SeedPaidActionLogMax(maxLedgerID int64) {
	r.paidActionLogMaxLedgerID = maxLedgerID
}

func (r *OrdersRepo) LoadAll(_ context.Context) (map[sim.OrderID]*sim.Order, error) {
	out := make(map[sim.OrderID]*sim.Order, len(r.orders))
	for id, o := range r.orders {
		out[id] = sim.CloneOrder(o)
	}
	return out, nil
}

func (r *OrdersRepo) SaveSnapshot(_ context.Context, _ sim.Tx, orders map[sim.OrderID]*sim.Order) error {
	r.orders = make(map[sim.OrderID]*sim.Order, len(orders))
	for id, o := range orders {
		r.orders[id] = sim.CloneOrder(o)
	}
	return nil
}

// WriteTerminal persists a single Order at its terminal state — the
// write-through half of the Slice 6 write-through-then-prune the World
// drives through its TerminalOrderSink. The mem equivalent of the pg
// row write is an upsert into the backing map (deep-cloned, same as
// Seed/SaveSnapshot), so a test that wires this repo as the sink sees
// the terminal order persisted and survives a subsequent LoadAll.
func (r *OrdersRepo) WriteTerminal(_ context.Context, o *sim.Order) error {
	if o == nil {
		return fmt.Errorf("mem orders WriteTerminal: nil order")
	}
	r.orders[o.ID] = sim.CloneOrder(o)
	return nil
}

// MaxLedgerID returns the largest id in the in-memory orders map (0 when
// empty). The mem backend has no separate pay_ledger history — its orders
// map IS the durable set — so the map max is the high-water mark, mirroring
// the pg impl's role for FinalizeLoad's payLedgerSeq floor. ZBBS-HOME-394.
func (r *OrdersRepo) MaxLedgerID(_ context.Context) (int64, error) {
	var maxID int64
	for id := range r.orders {
		if int64(id) > maxID {
			maxID = int64(id)
		}
	}
	return maxID, nil
}

// MaxPaidActionLogLedgerID returns the staged paid-action-log ledger
// high-water mark (0 unless a test set it via SeedPaidActionLogMax). The mem
// backend has no agent_action_log table; production wires the pg impl, which
// reads it directly. LLM-245.
func (r *OrdersRepo) MaxPaidActionLogLedgerID(_ context.Context) (int64, error) {
	return r.paidActionLogMaxLedgerID, nil
}

// LoadRecentPrices filters the seeded slice by `since` (At >= since),
// then per-(seller, item) caps at perKeyCap most-recent entries, and
// returns the result in chronological (oldest-first) order per key —
// matching the pg impl's contract so SeedPriceBook behaves identically
// across both backends.
func (r *OrdersRepo) LoadRecentPrices(_ context.Context, since time.Time, perKeyCap int) ([]sim.PriceBookSeedRecord, error) {
	// Symmetric with pg: perKeyCap <= 0 is a caller bug. Surface as
	// error so tests that pass it accidentally fail loudly instead of
	// silently shipping an empty seed. (R1 code_review finding.)
	if perKeyCap <= 0 {
		return nil, fmt.Errorf("mem orders LoadRecentPrices: perKeyCap must be > 0, got %d", perKeyCap)
	}
	// Filter by window first so the per-key cap operates on the
	// post-filter set (matches the pg WHERE/window-function order).
	filtered := make([]sim.PriceBookSeedRecord, 0, len(r.prices))
	for _, p := range r.prices {
		if p.Observation.At.Before(since) {
			continue
		}
		filtered = append(filtered, p)
	}
	// Group by key, sort each bucket by At ascending, take last
	// perKeyCap entries (most recent). The pg impl ranks DESC and
	// keeps the top N; the in-memory equivalent is sort ASC and
	// keep the trailing N. Both produce the same set.
	byKey := make(map[sim.PriceBookKey][]sim.PriceBookSeedRecord)
	for _, p := range filtered {
		byKey[p.Key] = append(byKey[p.Key], p)
	}
	out := make([]sim.PriceBookSeedRecord, 0, len(filtered))
	for key, bucket := range byKey {
		sort.Slice(bucket, func(i, j int) bool {
			return bucket[i].Observation.At.Before(bucket[j].Observation.At)
		})
		if len(bucket) > perKeyCap {
			bucket = bucket[len(bucket)-perKeyCap:]
		}
		for _, p := range bucket {
			out = append(out, p)
		}
		_ = key
	}
	// Stable cross-key ordering for deterministic test assertions.
	// Mirrors the pg impl's `ORDER BY seller_id, item_kind, created_at ASC`.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Key.SellerID != out[j].Key.SellerID {
			return out[i].Key.SellerID < out[j].Key.SellerID
		}
		if out[i].Key.Item != out[j].Key.Item {
			return out[i].Key.Item < out[j].Key.Item
		}
		return out[i].Observation.At.Before(out[j].Observation.At)
	})
	return out, nil
}
