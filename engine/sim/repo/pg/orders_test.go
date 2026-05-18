package pg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pgxmock-based tests for OrdersRepo. They assert the SQL shape +
// arg bindings + scan mapping. A real-DB smoke would land at cutover
// when main.go wires the pool; for Slice 5 the mock is sufficient.

// newMockPool returns a pgxmock pool. PgxPoolIface satisfies pg.Pool
// directly (Begin / Query / QueryRow / Exec all match the surface
// OrdersRepo needs).
func newMockPool(t *testing.T) (pgxmock.PgxPoolIface, *OrdersRepo) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(mock.Close)
	return mock, NewOrdersRepo(mock)
}

// --- LoadAll happy path ---------------------------------------------------

func TestOrdersRepo_LoadAll_HappyPath(t *testing.T) {
	mock, repo := newMockPool(t)

	now := time.Now().UTC()
	delivered := now.Add(time.Minute)
	expires := now.Add(15 * time.Minute)

	rows := pgxmock.NewRows([]string{
		"id", "buyer_id", "seller_id", "item_kind", "qty",
		"offered_amount", "consumer_actor_ids", "fulfillment_status",
		"created_at", "delivered_on", "expires_at",
	}).
		AddRow(int64(1), "alice", "bob", "stew", 2,
			6, []string{"alice", "carol"}, "ready",
			now, (*time.Time)(nil), &expires).
		AddRow(int64(2), "dave", "bob", "ale", 1,
			3, []string{}, "pending",
			now, (*time.Time)(nil), &expires).
		AddRow(int64(3), "eve", "bob", "bread", 1,
			2, []string{}, "ready",
			now, &delivered, (*time.Time)(nil)) // legacy row: NULL expires_at

	mock.ExpectQuery(`SELECT[\s\S]+FROM pay_ledger[\s\S]+WHERE state = 'accepted'[\s\S]+ORDER BY id`).
		WillReturnRows(rows)

	got, err := repo.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("loaded %d orders, want 3", len(got))
	}

	o1 := got[1]
	if o1.State != sim.OrderStateReady {
		t.Errorf("o1.State = %q, want ready", o1.State)
	}
	if o1.BuyerID != "alice" || o1.SellerID != "bob" {
		t.Errorf("o1 buyer/seller = %q/%q", o1.BuyerID, o1.SellerID)
	}
	if len(o1.ConsumerIDs) != 2 || o1.ConsumerIDs[0] != "alice" {
		t.Errorf("o1.ConsumerIDs = %v", o1.ConsumerIDs)
	}
	if !o1.ExpiresAt.Equal(expires) {
		t.Errorf("o1.ExpiresAt = %v, want %v", o1.ExpiresAt, expires)
	}

	// Pending maps to Ready in the runtime view (per orders.go mapping
	// docstring — v2 has no Pending state yet but the schema does).
	if got[2].State != sim.OrderStateReady {
		t.Errorf("o2 (Pending row) → State = %q, want ready", got[2].State)
	}

	// Legacy row with NULL expires_at: defaults to far-future.
	if got[3].ExpiresAt.Year() != 9999 {
		t.Errorf("o3 ExpiresAt year = %d, want 9999 (NULL → far-future)", got[3].ExpiresAt.Year())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestOrdersRepo_LoadAll_QueryError(t *testing.T) {
	mock, repo := newMockPool(t)
	mock.ExpectQuery(`SELECT[\s\S]+FROM pay_ledger`).WillReturnError(errors.New("conn closed"))
	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestOrdersRepo_LoadAll_SkipsUnknownStatus(t *testing.T) {
	mock, repo := newMockPool(t)

	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	rows := pgxmock.NewRows([]string{
		"id", "buyer_id", "seller_id", "item_kind", "qty",
		"offered_amount", "consumer_actor_ids", "fulfillment_status",
		"created_at", "delivered_on", "expires_at",
	}).
		AddRow(int64(1), "alice", "bob", "stew", 1,
			3, []string{}, "ready",
			now, (*time.Time)(nil), &expires).
		AddRow(int64(2), "eve", "bob", "ale", 1,
			2, []string{}, "weirdo_status",
			now, (*time.Time)(nil), &expires)

	mock.ExpectQuery(`SELECT[\s\S]+FROM pay_ledger`).WillReturnRows(rows)

	got, err := repo.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("loaded %d, want 1 (anomaly row skipped, not error)", len(got))
	}
	if _, ok := got[1]; !ok {
		t.Error("order 1 missing")
	}
	if _, ok := got[2]; ok {
		t.Error("order 2 with unknown status should have been skipped")
	}
}

// --- SaveSnapshot ---------------------------------------------------------

// fakeTx wraps a pgxmock.PgxConnIface as sim.Tx so we can drive
// SaveSnapshot without spinning a real txAdapter. The mock recognizes
// Exec calls against itself.
type fakeTx struct {
	mock pgxmock.PgxPoolIface
}

func (f fakeTx) Exec(ctx context.Context, sql string, args ...any) (sim.CommandTag, error) {
	ct, err := f.mock.Exec(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return cmdTagAdapter{ct: ct}, nil
}
func (fakeTx) Query(_ context.Context, _ string, _ ...any) (sim.Rows, error) { return nil, nil }
func (fakeTx) QueryRow(_ context.Context, _ string, _ ...any) sim.Row        { return nil }
func (fakeTx) Commit(_ context.Context) error                                { return nil }
func (fakeTx) Rollback(_ context.Context) error                              { return nil }

func TestOrdersRepo_SaveSnapshot_UpsertsEachOrder(t *testing.T) {
	mock, repo := newMockPool(t)
	tx := fakeTx{mock: mock}

	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	delivered := now.Add(time.Minute)

	mock.ExpectExec(`INSERT INTO pay_ledger`).
		WithArgs(
			int64(1), "alice", "bob", "stew", 2, 6,
			[]string{"alice"}, "ready",
			now, expires, now, (*time.Time)(nil),
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	mock.ExpectExec(`INSERT INTO pay_ledger`).
		WithArgs(
			int64(2), "dave", "bob", "ale", 1, 3,
			[]string{}, "delivered",
			now, expires, now, &delivered,
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	orders := map[sim.OrderID]*sim.Order{
		1: {
			ID:          1,
			State:       sim.OrderStateReady,
			BuyerID:     "alice",
			SellerID:    "bob",
			Item:        "stew",
			Qty:         2,
			Amount:      6,
			ConsumerIDs: []sim.ActorID{"alice"},
			LedgerID:    1,
			CreatedAt:   now,
			ExpiresAt:   expires,
		},
		2: {
			ID:          2,
			State:       sim.OrderStateDelivered,
			BuyerID:     "dave",
			SellerID:    "bob",
			Item:        "ale",
			Qty:         1,
			Amount:      3,
			ConsumerIDs: []sim.ActorID{},
			LedgerID:    2,
			CreatedAt:   now,
			DeliveredAt: &delivered,
			ExpiresAt:   expires,
		},
	}
	mock.MatchExpectationsInOrder(false) // map iteration order is undefined

	if err := repo.SaveSnapshot(context.Background(), tx, orders); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestOrdersRepo_SaveSnapshot_NilTx(t *testing.T) {
	_, repo := newMockPool(t)
	err := repo.SaveSnapshot(context.Background(), nil, map[sim.OrderID]*sim.Order{
		1: {ID: 1, State: sim.OrderStateReady},
	})
	if err == nil {
		t.Fatal("expected error for nil tx")
	}
}

func TestOrdersRepo_SaveSnapshot_NilOrderSkipped(t *testing.T) {
	mock, repo := newMockPool(t)
	tx := fakeTx{mock: mock}
	// Expect no Exec calls — nil entries are silently skipped.
	err := repo.SaveSnapshot(context.Background(), tx, map[sim.OrderID]*sim.Order{1: nil})
	if err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestOrdersRepo_SaveSnapshot_UnknownStateErrors(t *testing.T) {
	mock, repo := newMockPool(t)
	tx := fakeTx{mock: mock}
	err := repo.SaveSnapshot(context.Background(), tx, map[sim.OrderID]*sim.Order{
		1: {ID: 1, State: sim.OrderState("garbage")},
	})
	if err == nil {
		t.Fatal("expected error for unknown OrderState")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestOrdersRepo_SaveSnapshot_EmptyMapIsNoop(t *testing.T) {
	mock, repo := newMockPool(t)
	tx := fakeTx{mock: mock}
	err := repo.SaveSnapshot(context.Background(), tx, map[sim.OrderID]*sim.Order{})
	if err != nil {
		t.Fatalf("SaveSnapshot empty: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// --- state mapping --------------------------------------------------------

func TestFulfillmentToOrderState_Mapping(t *testing.T) {
	cases := []struct {
		in    string
		want  sim.OrderState
		isErr bool
	}{
		{"ready", sim.OrderStateReady, false},
		{"pending", sim.OrderStateReady, false}, // v2 has no Pending yet
		{"delivered", sim.OrderStateDelivered, false},
		{"expired", sim.OrderStateExpired, false},
		{"weirdo", "", true},
		{"", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := fulfillmentToOrderState(tc.in)
			if (err != nil) != tc.isErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.isErr)
			}
			if !tc.isErr && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOrderStateToFulfillment_Mapping(t *testing.T) {
	cases := []struct {
		in    sim.OrderState
		want  string
		isErr bool
	}{
		{sim.OrderStateReady, "ready", false},
		{sim.OrderStateDelivered, "delivered", false},
		{sim.OrderStateExpired, "expired", false},
		{sim.OrderState("garbage"), "", true},
		{sim.OrderState(""), "", true},
	}
	for _, tc := range cases {
		t.Run(string(tc.in), func(t *testing.T) {
			got, err := orderStateToFulfillment(tc.in)
			if (err != nil) != tc.isErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.isErr)
			}
			if !tc.isErr && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
