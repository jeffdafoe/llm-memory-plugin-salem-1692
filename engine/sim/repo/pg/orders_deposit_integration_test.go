package pg

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// orders_deposit_integration_test.go — LLM-357 real-pg validation for the
// partial-payment deposit column. pgxmock proves the SQL shape; this proves the
// migration's deposit_amount column and its CHECK constraint against a real
// server, and that LoadAll reads the deposit back — including COALESCE(NULL, 0)
// for a legacy / full-prepay row.
func TestOrdersRepo_Integration_DepositRoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// pay_ledger.item_kind FKs to item_kind(name); seed the good under test.
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO item_kind (name, display_label, category) VALUES ('shovel', 'Shovel', 'material')`,
	); err != nil {
		t.Fatalf("seed item_kind: %v", err)
	}

	insertOrder := func(id int64, deposit any) error {
		_, err := f.Pool.Exec(ctx,
			`INSERT INTO pay_ledger
			     (id, buyer_id, seller_id, item_kind, qty, offered_amount,
			      state, fulfillment_status, ready_by, resolved_at, created_at, deposit_amount)
			 VALUES ($1, 'alice', 'bob', 'shovel', 3, 15,
			      'accepted', 'ready', CURRENT_DATE, now(), now(), $2)`,
			id, deposit)
		return err
	}

	// A partial-payment order (5 of 15 down) and a legacy / full-prepay order
	// (deposit_amount NULL).
	if err := insertOrder(1, 5); err != nil {
		t.Fatalf("insert partial order: %v", err)
	}
	if err := insertOrder(2, nil); err != nil {
		t.Fatalf("insert full-prepay order: %v", err)
	}

	repo := NewOrdersRepo(f.Pool)
	got, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	o1, ok := got[1]
	if !ok || o1.Deposit != 5 || o1.Amount != 15 {
		t.Errorf("order 1 = %+v (ok=%v), want Deposit 5 / Amount 15", o1, ok)
	}
	if bal := sim.OrderBalanceDue(o1); bal != 10 {
		t.Errorf("order 1 balance = %d, want 10 (15 total - 5 deposit)", bal)
	}
	o2, ok := got[2]
	if !ok || o2.Deposit != 0 {
		t.Errorf("order 2 = %+v (ok=%v), want Deposit 0 (NULL deposit_amount => full prepay)", o2, ok)
	}

	// The CHECK constraint fences off a corrupt deposit: negative, or more than
	// the total price it is a fraction of.
	if err := insertOrder(3, -1); err == nil {
		t.Errorf("insert with deposit -1 should violate the CHECK constraint, got nil")
	}
	if err := insertOrder(4, 20); err == nil {
		t.Errorf("insert with deposit 20 > offered_amount 15 should violate the CHECK constraint, got nil")
	}
}
