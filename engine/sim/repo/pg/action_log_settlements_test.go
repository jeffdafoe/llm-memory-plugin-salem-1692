package pg

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v6"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// action_log_settlements_test.go — LLM-105: unit tests for the durable settlements
// read (LoadSettlements) over a pgxmock pool, no live DB. Covers the payload parse
// (enriched + legacy rows, nullable huddle) and the dynamic bound-param WHERE.

func settlementColumns() []string {
	return []string{"occurred_at", "actor_id", "speaker_name", "payload", "huddle_id"}
}

// An enriched row decodes the full terms; a legacy row (no ledger_id/pay_items in
// the payload, NULL huddle) degrades to zero/empty without error.
func TestLoadSettlements_ParsesEnrichedAndLegacyRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(mock.Close)
	repo := NewActionLogRepo(mock)

	at := time.Date(2026, 6, 25, 0, 5, 0, 0, time.UTC)
	enriched := []byte(`{"recipient":"John Ellis","amount":0,"for":"1 stew","consume_now":true,"ledger_id":332,"pay_items":[{"item":"skillet","qty":1}]}`)
	legacy := []byte(`{"recipient":"John Ellis","amount":3,"for":"1 bread"}`)
	rows := pgxmock.NewRows(settlementColumns()).
		AddRow(at, "ez", "Ezekiel Crane", enriched, sql.NullString{String: "hud-abc", Valid: true}).
		AddRow(at.Add(-time.Minute), "zz", "Old Row", legacy, sql.NullString{})

	mock.ExpectQuery(`action_type = 'paid'`).WithArgs(10).WillReturnRows(rows)

	got, err := repo.LoadSettlements(context.Background(), sim.SettlementFilter{}, 10)
	if err != nil {
		t.Fatalf("LoadSettlements: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}

	r0 := got[0]
	if r0.BuyerID != "ez" || r0.BuyerName != "Ezekiel Crane" || r0.SellerName != "John Ellis" ||
		r0.Amount != 0 || r0.Item != "1 stew" || !r0.ConsumeNow || r0.LedgerID != 332 || r0.HuddleID != "hud-abc" {
		t.Errorf("row0 = %+v, want the enriched eat-here settlement", r0)
	}
	if len(r0.PayItems) != 1 || r0.PayItems[0].Kind != "skillet" || r0.PayItems[0].Qty != 1 {
		t.Errorf("row0 pay_items = %+v, want [{skillet 1}]", r0.PayItems)
	}

	r1 := got[1]
	if r1.Amount != 3 || r1.LedgerID != 0 || len(r1.PayItems) != 0 || r1.HuddleID != "" {
		t.Errorf("row1 (legacy) = %+v, want amount 3 / ledger 0 / no goods / no huddle", r1)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// Every filter binds, in order: actor, since, until, ledger (numeric column), limit.
func TestLoadSettlements_AppliesAllFilters(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(mock.Close)
	repo := NewActionLogRepo(mock)

	// The actor must be a real uuid: actor_id is a uuid column, so LoadSettlements
	// short-circuits an id that could never appear there rather than binding it
	// (LLM-573) — see TestLoadSettlements_UnbindableActorMatchesNothing.
	const ezekiel = "11111111-1111-1111-1111-111111111111"
	since := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	until := since.Add(time.Hour)
	mock.ExpectQuery(`actor_id = \$1.*occurred_at >= \$2.*occurred_at < \$3.*ledger_id = \$4.*LIMIT \$5`).
		WithArgs(ezekiel, since, until, int64(332), 5).
		WillReturnRows(pgxmock.NewRows(settlementColumns()))

	if _, err := repo.LoadSettlements(context.Background(), sim.SettlementFilter{
		ActorID:  ezekiel,
		Since:    since,
		Until:    until,
		LedgerID: 332,
	}, 5); err != nil {
		t.Fatalf("LoadSettlements: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestLoadSettlements_UnbindableActorMatchesNothing: an actor id the uuid column
// cannot hold returns empty WITHOUT issuing a query. pgxmock is the right layer
// for this half — with no ExpectQuery registered, any query at all fails the
// test, which is precisely the assertion (the integration test proves the other
// half, that binding such a value against real pg would error rather than match
// nothing).
func TestLoadSettlements_UnbindableActorMatchesNothing(t *testing.T) {
	for _, bad := range []sim.ActorID{"vstr-fb50246e", "ez", "vstr-not-a-visitor"} {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("NewPool: %v", err)
		}
		got, err := NewActionLogRepo(mock).LoadSettlements(context.Background(), sim.SettlementFilter{ActorID: bad}, 5)
		if err != nil {
			t.Errorf("LoadSettlements(actor=%q): %v", bad, err)
		}
		if len(got) != 0 {
			t.Errorf("LoadSettlements(actor=%q) = %+v, want empty", bad, got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("actor=%q: %v", bad, err)
		}
		mock.Close()
	}
}

// LLM-1015: a commission deposit row carries the agreed price beside its amount,
// and a balance row names its order; both decode onto the settlement row.
func TestLoadSettlements_ParsesCommissionDepositAndBalance(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(mock.Close)
	repo := NewActionLogRepo(mock)

	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	deposit := []byte(`{"recipient":"Ezekiel Crane","amount":5,"price":15,"for":"5x shovel","ledger_id":412,"consume_now":false}`)
	balance := []byte(`{"recipient":"Ezekiel Crane","recipient_actor_id":"ez","amount":10,"for":"5x shovel","order_balance":7}`)
	rows := pgxmock.NewRows(settlementColumns()).
		AddRow(at, "el", "Elizabeth Ellis", balance, sql.NullString{}).
		AddRow(at.Add(-time.Hour), "el", "Elizabeth Ellis", deposit, sql.NullString{})
	mock.ExpectQuery(`action_type = 'paid'`).WithArgs(10).WillReturnRows(rows)

	got, err := repo.LoadSettlements(context.Background(), sim.SettlementFilter{}, 10)
	if err != nil {
		t.Fatalf("LoadSettlements: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if b := got[0]; b.Amount != 10 || b.BalanceOfOrder != 7 || b.LedgerID != 0 || b.Price != 0 {
		t.Errorf("balance row = %+v, want amount 10, order 7, no ledger, no price", b)
	}
	if d := got[1]; d.Amount != 5 || d.Price != 15 || d.LedgerID != 412 || d.BalanceOfOrder != 0 {
		t.Errorf("deposit row = %+v, want amount 5, price 15, ledger 412", d)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
