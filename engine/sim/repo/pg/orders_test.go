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

// --- MaxLedgerID ----------------------------------------------------------

// TestOrdersRepo_MaxLedgerID reports the largest pay_ledger id (ZBBS-HOME-394).
// FinalizeLoad seeds the LedgerID allocator from it so a post-restart mint
// can't reuse an id and clobber a historical row.
func TestOrdersRepo_MaxLedgerID(t *testing.T) {
	mock, repo := newMockPool(t)
	mock.ExpectQuery(`SELECT COALESCE\(max\(id\), 0\) FROM pay_ledger`).
		WillReturnRows(pgxmock.NewRows([]string{"max"}).AddRow(int64(187)))
	got, err := repo.MaxLedgerID(context.Background())
	if err != nil {
		t.Fatalf("MaxLedgerID: %v", err)
	}
	if got != 187 {
		t.Errorf("MaxLedgerID = %d, want 187", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// --- MaxPaidActionLogLedgerID ---------------------------------------------

// TestOrdersRepo_MaxPaidActionLogLedgerID reports the largest ledger_id on any
// `paid` agent_action_log row (LLM-245). consume_now settlements mint a
// LedgerID but write no pay_ledger row, so this is the only durable trace of
// their ids; FinalizeLoad floors the allocator from GREATEST(MaxLedgerID,
// this) so a restart can't re-mint one and corrupt the audit join.
func TestOrdersRepo_MaxPaidActionLogLedgerID(t *testing.T) {
	mock, repo := newMockPool(t)
	mock.ExpectQuery(`SELECT COALESCE\(max\(\(payload->>'ledger_id'\)::bigint\), 0\) FROM agent_action_log WHERE action_type = 'paid' AND payload->>'ledger_id' ~ '\^\[0-9\]\+\$'`).
		WillReturnRows(pgxmock.NewRows([]string{"max"}).AddRow(int64(497)))
	got, err := repo.MaxPaidActionLogLedgerID(context.Background())
	if err != nil {
		t.Fatalf("MaxPaidActionLogLedgerID: %v", err)
	}
	if got != 497 {
		t.Errorf("MaxPaidActionLogLedgerID = %d, want 497", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// --- LoadAll happy path ---------------------------------------------------

func TestOrdersRepo_LoadAll_HappyPath(t *testing.T) {
	mock, repo := newMockPool(t)

	now := time.Now().UTC()
	delivered := now.Add(time.Minute)
	expires := now.Add(15 * time.Minute)
	// ready_by round-trips as midnight UTC of the booked date. o1 is an
	// advance booking (2 days ahead) so we can prove ReadyBy is loaded
	// distinctly from created_at. ZBBS-HOME-403.
	readyBy := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 2)

	rows := pgxmock.NewRows([]string{
		"id", "buyer_id", "seller_id", "item_kind", "qty",
		"offered_amount", "consumer_actor_ids", "fulfillment_status",
		"created_at", "delivered_on", "expires_at", "ready_by", "deposit_amount",
	}).
		AddRow(int64(1), "alice", "bob", "stew", 2,
			6, []string{"alice", "carol"}, "ready",
			now, (*time.Time)(nil), &expires, readyBy, 4). // partial-payment deposit
		AddRow(int64(2), "dave", "bob", "ale", 1,
			3, []string{}, "pending",
			now, (*time.Time)(nil), &expires, readyBy, 0).
		AddRow(int64(3), "eve", "bob", "bread", 1,
			2, []string{}, "ready",
			now, &delivered, (*time.Time)(nil), readyBy, 0) // legacy row: NULL expires_at

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
	if !o1.ReadyBy.Equal(readyBy) {
		t.Errorf("o1.ReadyBy = %v, want %v", o1.ReadyBy, readyBy)
	}
	if o1.Deposit != 4 {
		t.Errorf("o1.Deposit = %d, want 4 (partial-payment deposit loaded)", o1.Deposit)
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

// TestOrdersRepo_LoadAll_UnknownStatusErrors — loadAllSQL filters to
// 'ready'/'pending' so unknown values should never be returned by the
// real DB, but we defend the mapping anyway. If a row with an
// unexpected fulfillment_status DOES surface (schema drift, manual
// SQL), LoadAll must error rather than silently drop data.
func TestOrdersRepo_LoadAll_UnknownStatusErrors(t *testing.T) {
	mock, repo := newMockPool(t)

	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	rows := pgxmock.NewRows([]string{
		"id", "buyer_id", "seller_id", "item_kind", "qty",
		"offered_amount", "consumer_actor_ids", "fulfillment_status",
		"created_at", "delivered_on", "expires_at", "ready_by", "deposit_amount",
	}).
		AddRow(int64(1), "alice", "bob", "stew", 1,
			3, []string{}, "weirdo_status",
			now, (*time.Time)(nil), &expires, now, 0)

	mock.ExpectQuery(`SELECT[\s\S]+FROM pay_ledger`).WillReturnRows(rows)

	_, err := repo.LoadAll(context.Background())
	if err == nil {
		t.Fatal("expected error for unknown fulfillment_status")
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
func (f fakeTx) QueryRow(ctx context.Context, sql string, args ...any) sim.Row {
	// Forward to the mock pool so SaveSnapshot implementations that
	// use QueryRow (e.g., Slice 9's nextval sequence call) can be
	// driven from pgxmock fixtures.
	return rowAdapter{row: f.mock.QueryRow(ctx, sql, args...)}
}
func (fakeTx) Commit(_ context.Context) error   { return nil }
func (fakeTx) Rollback(_ context.Context) error { return nil }

func TestOrdersRepo_SaveSnapshot_UpsertsEachOrder(t *testing.T) {
	mock, repo := newMockPool(t)
	tx := fakeTx{mock: mock}

	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	delivered := now.Add(time.Minute)
	// o1 is an advance booking: its ready_by ($9) is a distinct future date,
	// so this proves the upsert binds o.ReadyBy (not o.CreatedAt) to ready_by.
	// o2 books for today, so its ready_by == now. ZBBS-HOME-403.
	readyBy := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 2)

	// SaveSnapshot now does two steps inside the Tx: expire absent
	// rows first, then upsert. The expire-absent UPDATE accepts the
	// snapshot's IDs and is matched without a strict arg check (map
	// iteration order varies). The two upserts then run in either
	// order.
	mock.ExpectExec(`UPDATE pay_ledger[\s\S]+SET fulfillment_status = 'expired'`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 0"))

	mock.ExpectExec(`INSERT INTO pay_ledger`).
		WithArgs(
			int64(1), "alice", "bob", "stew", 2, 6,
			[]string{"alice"}, "ready",
			readyBy, expires, now, (*time.Time)(nil), 4,
		).
		WillReturnResult(pgconn.NewCommandTag("INSERT 0 1"))

	mock.ExpectExec(`INSERT INTO pay_ledger`).
		WithArgs(
			int64(2), "dave", "bob", "ale", 1, 3,
			[]string{}, "delivered",
			now, expires, now, &delivered, 0,
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
			Deposit:     4,
			ConsumerIDs: []sim.ActorID{"alice"},
			LedgerID:    1,
			CreatedAt:   now,
			ReadyBy:     readyBy,
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
			ReadyBy:     now,
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

// TestOrdersRepo_SaveSnapshot_ExpiresAbsentRows — even when the
// snapshot map is empty, the expire-absent UPDATE runs (with an
// empty ID list, expiring all in-flight rows). This is the snapshot
// contract: "this is everything in-flight" → empty means "no
// in-flight orders."
func TestOrdersRepo_SaveSnapshot_ExpiresAbsentRows_EmptyMap(t *testing.T) {
	mock, repo := newMockPool(t)
	tx := fakeTx{mock: mock}

	mock.ExpectExec(`UPDATE pay_ledger[\s\S]+SET fulfillment_status = 'expired'`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 0"))

	if err := repo.SaveSnapshot(context.Background(), tx, map[sim.OrderID]*sim.Order{}); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestOrdersRepo_SaveSnapshot_LedgerIDMismatchRejected — if a caller
// constructs an Order whose LedgerID disagrees with ID, the upsert
// would silently use ID and drop the LedgerID information. We
// detect and refuse instead.
func TestOrdersRepo_SaveSnapshot_LedgerIDMismatchRejected(t *testing.T) {
	mock, repo := newMockPool(t)
	tx := fakeTx{mock: mock}

	mock.ExpectExec(`UPDATE pay_ledger[\s\S]+SET fulfillment_status = 'expired'`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 0"))

	now := time.Now().UTC()
	bad := map[sim.OrderID]*sim.Order{
		1: {
			ID:        1,
			State:     sim.OrderStateReady,
			BuyerID:   "alice",
			SellerID:  "bob",
			Item:      "stew",
			Qty:       1,
			Amount:    1,
			LedgerID:  999, // mismatched
			CreatedAt: now,
			ExpiresAt: now.Add(time.Hour),
		},
	}
	if err := repo.SaveSnapshot(context.Background(), tx, bad); err == nil {
		t.Fatal("expected error for LedgerID/ID mismatch")
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
	// nil entries are silently skipped — the expire-absent UPDATE
	// still runs (with an empty ID slice; expires-all-in-flight).
	mock.ExpectExec(`UPDATE pay_ledger[\s\S]+SET fulfillment_status = 'expired'`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 0"))
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
	// expire-absent UPDATE runs first; mapping error surfaces on
	// the first upsert attempt.
	mock.ExpectExec(`UPDATE pay_ledger[\s\S]+SET fulfillment_status = 'expired'`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 0"))
	err := repo.SaveSnapshot(context.Background(), tx, map[sim.OrderID]*sim.Order{
		1: {ID: 1, State: sim.OrderState("garbage")},
	})
	if err == nil {
		t.Fatal("expected error for unknown OrderState")
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

// --- WriteTerminal (Slice 6) ---------------------------------------------

// TestOrdersRepo_WriteTerminal_Delivered — happy path. Stamps
// fulfillment_status='delivered' + delivered_on; expires_at preserved.
func TestOrdersRepo_WriteTerminal_Delivered(t *testing.T) {
	mock, repo := newMockPool(t)

	now := time.Now().UTC()
	delivered := now.Add(time.Minute)
	mock.ExpectExec(`UPDATE pay_ledger[\s\S]+SET fulfillment_status = \$2`).
		WithArgs(int64(1), "delivered", &delivered).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))

	o := &sim.Order{
		ID:          1,
		State:       sim.OrderStateDelivered,
		Item:        "stew",
		Qty:         1,
		ConsumerIDs: []sim.ActorID{"alice"},
		LedgerID:    1,
		DeliveredAt: &delivered,
	}
	if err := repo.WriteTerminal(context.Background(), o); err != nil {
		t.Fatalf("WriteTerminal: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestOrdersRepo_WriteTerminal_Expired — same path with state='expired'
// and DeliveredAt=nil. The CASE expression in SQL restamps expires_at
// to NOW(), preserved as part of the SQL shape (the mock doesn't
// evaluate the CASE; we just verify the binding shape).
func TestOrdersRepo_WriteTerminal_Expired(t *testing.T) {
	mock, repo := newMockPool(t)

	mock.ExpectExec(`UPDATE pay_ledger[\s\S]+SET fulfillment_status = \$2`).
		WithArgs(int64(7), "expired", (*time.Time)(nil)).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 1"))

	o := &sim.Order{
		ID:          7,
		State:       sim.OrderStateExpired,
		Item:        "stew",
		Qty:         1,
		ConsumerIDs: []sim.ActorID{"alice"},
		LedgerID:    7,
	}
	if err := repo.WriteTerminal(context.Background(), o); err != nil {
		t.Fatalf("WriteTerminal: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

// TestOrdersRepo_WriteTerminal_RejectsNonTerminal — caller-bug guard.
func TestOrdersRepo_WriteTerminal_RejectsNonTerminal(t *testing.T) {
	_, repo := newMockPool(t)
	o := &sim.Order{ID: 1, State: sim.OrderStateReady}
	if err := repo.WriteTerminal(context.Background(), o); err == nil {
		t.Fatal("expected error for non-terminal state")
	}
}

// TestOrdersRepo_WriteTerminal_RejectsNil — nil guard.
func TestOrdersRepo_WriteTerminal_RejectsNil(t *testing.T) {
	_, repo := newMockPool(t)
	if err := repo.WriteTerminal(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil order")
	}
}

// TestOrdersRepo_WriteTerminal_RowsAffectedZero — the row doesn't exist
// in pay_ledger. Surface as an error so finalizeOrderTerminal logs +
// retains the entry for the next checkpoint to reconcile.
func TestOrdersRepo_WriteTerminal_RowsAffectedZero(t *testing.T) {
	mock, repo := newMockPool(t)

	mock.ExpectExec(`UPDATE pay_ledger`).
		WillReturnResult(pgconn.NewCommandTag("UPDATE 0"))

	o := &sim.Order{
		ID: 999, State: sim.OrderStateDelivered, Item: "stew", Qty: 1,
		ConsumerIDs: []sim.ActorID{"alice"}, LedgerID: 999,
	}
	if err := repo.WriteTerminal(context.Background(), o); err == nil {
		t.Fatal("expected error for RowsAffected=0")
	}
}

// TestOrdersRepo_WriteTerminal_ExecError — pg-side error surfaces to
// the caller (finalizeOrderTerminal logs + retains).
func TestOrdersRepo_WriteTerminal_ExecError(t *testing.T) {
	mock, repo := newMockPool(t)

	mock.ExpectExec(`UPDATE pay_ledger`).
		WillReturnError(errors.New("conn lost"))

	o := &sim.Order{
		ID: 1, State: sim.OrderStateDelivered, Item: "stew", Qty: 1,
		ConsumerIDs: []sim.ActorID{"alice"}, LedgerID: 1,
	}
	if err := repo.WriteTerminal(context.Background(), o); err == nil {
		t.Fatal("expected error for pool Exec failure")
	}
}

// --- LoadRecentPrices ----------------------------------------------------

func TestOrdersRepo_LoadRecentPrices_HappyPath(t *testing.T) {
	mock, repo := newMockPool(t)

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	since := base.Add(-30 * 24 * time.Hour)

	// Mock returns rows in chronological order per (seller, item) — the
	// outer ORDER BY in loadRecentPricesSQL is what guarantees this in
	// production; the mock just hands back whatever we hand it.
	rows := pgxmock.NewRows([]string{
		"seller_id", "item_kind", "buyer_id", "offered_amount", "qty",
		"cardinality", "created_at",
	}).
		AddRow("bob", "ale", "alice", 2, 1, intPtr(1), base.Add(-2*time.Hour)).
		AddRow("bob", "ale", "carol", 3, 1, intPtr(1), base.Add(-1*time.Hour)).
		AddRow("bob", "stew", "alice", 4, 2, intPtr(2), base) // multi-consumer

	mock.ExpectQuery(`ROW_NUMBER\(\) OVER`).
		WithArgs(since, 20).
		WillReturnRows(rows)

	got, err := repo.LoadRecentPrices(context.Background(), since, 20)
	if err != nil {
		t.Fatalf("LoadRecentPrices: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("loaded %d rows, want 3", len(got))
	}

	if got[0].Key.SellerID != "bob" || got[0].Key.Item != "ale" {
		t.Errorf("row 0 key = %+v", got[0].Key)
	}
	if got[0].Observation.BuyerID != "alice" || got[0].Observation.Amount != 2 {
		t.Errorf("row 0 observation = %+v", got[0].Observation)
	}
	if got[2].Observation.Consumers != 2 {
		t.Errorf("row 2 Consumers = %d, want 2 (cardinality round-trip)", got[2].Observation.Consumers)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("expectations: %v", err)
	}
}

func TestOrdersRepo_LoadRecentPrices_NullConsumersDefaultsTo1(t *testing.T) {
	mock, repo := newMockPool(t)

	now := time.Now().UTC()
	since := now.Add(-time.Hour)

	rows := pgxmock.NewRows([]string{
		"seller_id", "item_kind", "buyer_id", "offered_amount", "qty",
		"cardinality", "created_at",
	}).
		// Legacy row with NULL consumer_actor_ids → cardinality NULL → scan into *int = nil.
		AddRow("bob", "ale", "alice", 2, 1, (*int)(nil), now)

	mock.ExpectQuery(`ROW_NUMBER\(\) OVER`).WithArgs(since, 5).WillReturnRows(rows)

	got, err := repo.LoadRecentPrices(context.Background(), since, 5)
	if err != nil {
		t.Fatalf("LoadRecentPrices: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("loaded %d rows, want 1", len(got))
	}
	if got[0].Observation.Consumers != 1 {
		t.Errorf("NULL cardinality should default Consumers to 1, got %d", got[0].Observation.Consumers)
	}
}

func TestOrdersRepo_LoadRecentPrices_InvalidPerKeyCapErrors(t *testing.T) {
	_, repo := newMockPool(t)

	for _, n := range []int{0, -1} {
		if _, err := repo.LoadRecentPrices(context.Background(), time.Now(), n); err == nil {
			t.Errorf("LoadRecentPrices(perKeyCap=%d) should error", n)
		}
	}
}

func TestOrdersRepo_LoadRecentPrices_QueryError(t *testing.T) {
	mock, repo := newMockPool(t)
	now := time.Now()
	mock.ExpectQuery(`ROW_NUMBER\(\) OVER`).WithArgs(now, 10).WillReturnError(errors.New("conn lost"))

	if _, err := repo.LoadRecentPrices(context.Background(), now, 10); err == nil {
		t.Fatal("expected error from pool.Query failure")
	}
}

// intPtr returns &i; helper for cardinality column mock values.
func intPtr(i int) *int { return &i }
