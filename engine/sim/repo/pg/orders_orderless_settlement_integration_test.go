package pg

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// orders_orderless_settlement_integration_test.go — real-pg validation for
// WriteOrderlessSettlement (LLM-246). The behaviors under test are exactly
// the cross-query contracts a mock can't prove against the migrated schema:
// the inserted row must satisfy pay_ledger's CHECK constraints, be visible
// to loadRecentPricesSQL (the price-book restart seed), stay INVISIBLE to
// LoadAll's Order-resurrection filter, and — for the bundle shape — fall
// out of the seed via its NULL item_kind.
func TestOrdersRepo_Integration_WriteOrderlessSettlement(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// pay_ledger.item_kind carries a real FK to the item_kind reference
	// table; seed the kind the settlement sells.
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO item_kind (name, display_label, category) VALUES ('stew', 'Stew', 'food')`); err != nil {
		t.Fatalf("seed item_kind: %v", err)
	}

	repo := NewOrdersRepo(f.Pool)
	created := time.Date(2026, 7, 3, 18, 0, 0, 0, time.UTC)
	at := created.Add(45 * time.Second)

	// A consume_now eat-here single — the LLM-246 headline case.
	if err := repo.WriteOrderlessSettlement(ctx, &sim.PayLedgerEntry{
		ID: 501, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 2, ConsumeNow: true, Amount: 8,
		ConsumerIDs: []sim.ActorID{"alice", "carol"},
		State:       sim.PayLedgerStateAccepted,
		CreatedAt:   created,
	}, at); err != nil {
		t.Fatalf("WriteOrderlessSettlement (consume_now): %v", err)
	}

	// A bundle take — no single item, item_kind/qty land NULL.
	if err := repo.WriteOrderlessSettlement(ctx, &sim.PayLedgerEntry{
		ID: 502, BuyerID: "alice", SellerID: "bob",
		ConsumeNow: true, Amount: 6,
		ConsumerIDs: []sim.ActorID{"alice"},
		Lines:       []sim.QuoteLine{{ItemKind: "stew", Qty: 1}},
		State:       sim.PayLedgerStateAccepted,
		CreatedAt:   created,
	}, at); err != nil {
		t.Fatalf("WriteOrderlessSettlement (bundle): %v", err)
	}

	// Row shape: state/fulfillment keep it out of every Order lifecycle
	// query; consume_now + timestamps round-trip.
	var (
		state, fulfillment string
		consumeNow         bool
		itemKind           *string
		qty                *int
		resolvedAt         time.Time
		deliveredOn        *time.Time
	)
	if err := f.Pool.QueryRow(ctx,
		`SELECT state, fulfillment_status, consume_now, item_kind, qty, resolved_at, delivered_on
		   FROM pay_ledger WHERE id = 501`).
		Scan(&state, &fulfillment, &consumeNow, &itemKind, &qty, &resolvedAt, &deliveredOn); err != nil {
		t.Fatalf("read settlement row: %v", err)
	}
	if state != "accepted" || fulfillment != "delivered" || !consumeNow {
		t.Errorf("row = state %q fulfillment %q consume_now %v, want accepted/delivered/true", state, fulfillment, consumeNow)
	}
	if itemKind == nil || *itemKind != "stew" || qty == nil || *qty != 2 {
		t.Errorf("row goods = item %v qty %v, want stew/2", itemKind, qty)
	}
	if !resolvedAt.Equal(at) || deliveredOn == nil || !deliveredOn.Equal(at) {
		t.Errorf("row times = resolved %v delivered %v, want both %v", resolvedAt, deliveredOn, at)
	}
	var bundleItem *string
	var bundleQty *int
	if err := f.Pool.QueryRow(ctx,
		`SELECT item_kind, qty FROM pay_ledger WHERE id = 502`).Scan(&bundleItem, &bundleQty); err != nil {
		t.Fatalf("read bundle row: %v", err)
	}
	if bundleItem != nil || bundleQty != nil {
		t.Errorf("bundle row goods = item %v qty %v, want NULL/NULL", bundleItem, bundleQty)
	}

	// The restart seed sees the consume_now settlement — the LLM-246 fix —
	// and only it (the bundle row falls out via item_kind IS NOT NULL).
	records, err := repo.LoadRecentPrices(ctx, created.Add(-time.Hour), 8)
	if err != nil {
		t.Fatalf("LoadRecentPrices: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("seed records = %d, want 1 (consume_now row in, bundle row out)", len(records))
	}
	rec := records[0]
	if rec.Key.SellerID != "bob" || rec.Key.Item != "stew" {
		t.Errorf("seed key = %+v, want bob/stew", rec.Key)
	}
	if rec.Observation.BuyerID != "alice" || rec.Observation.Amount != 8 ||
		rec.Observation.Qty != 2 || rec.Observation.Consumers != 2 {
		t.Errorf("seed observation = %+v, want alice/8/qty=2/consumers=2", rec.Observation)
	}

	// The defensive shape guard rejects a take-home single — that row is
	// the Order checkpoint's to write; an eager 'delivered' insert here
	// would collide on the id or hide the order from the restart filters.
	if err := repo.WriteOrderlessSettlement(ctx, &sim.PayLedgerEntry{
		ID: 503, BuyerID: "alice", SellerID: "bob",
		ItemKind: "stew", Qty: 1, Amount: 4,
		State:     sim.PayLedgerStateAccepted,
		CreatedAt: created,
	}, at); err == nil {
		t.Error("WriteOrderlessSettlement accepted a take-home single shape, want shape-guard error")
	}

	// LoadAll must NOT resurrect either settlement as an in-flight Order.
	orders, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(orders) != 0 {
		t.Errorf("LoadAll = %d orders, want 0 (delivered settlements are not in-flight)", len(orders))
	}
}
