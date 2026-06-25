package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// fakeSettlementStore is an in-test SettlementStore: it records the filter + limit
// it was called with and returns canned rows (or an error), so the handler runs
// without a pg pool.
type fakeSettlementStore struct {
	rows      []sim.SettlementRow
	err       error
	gotFilter sim.SettlementFilter
	gotLimit  int
}

func (f *fakeSettlementStore) LoadSettlements(_ context.Context, filter sim.SettlementFilter, limit int) ([]sim.SettlementRow, error) {
	f.gotFilter = filter
	f.gotLimit = limit
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// settlementsServer builds an umbilical-enabled Server whose /settlements route is
// backed by store. A nil store is left unwired so the nil-store 503 path is
// reachable.
func settlementsServer(t *testing.T, store SettlementStore) http.Handler {
	t.Helper()
	srv := NewServer(seededWorld(t), permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	if store != nil {
		srv.SetSettlementStore(store)
	}
	return srv.Handler()
}

// TestUmbilicalSettlements_MapsRowsAndFlags: the happy path. The handler over-fetches
// (cap+1), maps each row to the wire DTO, and derives free / has_legacy correctly —
// a give-away (0 coins, no goods) is free; a 0-coin barter is NOT; a row with no
// ledger id (pre-LLM-105) is has_legacy.
func TestUmbilicalSettlements_MapsRowsAndFlags(t *testing.T) {
	t0 := time.Date(2026, 6, 25, 0, 5, 0, 0, time.UTC)
	store := &fakeSettlementStore{rows: []sim.SettlementRow{
		// give-away: 0 coins, no goods, has a ledger id → free + not legacy.
		{OccurredAt: t0, BuyerID: "ez", BuyerName: "Ezekiel Crane", SellerName: "John Ellis", Amount: 0, Item: "1 stew", LedgerID: 332, ConsumeNow: true},
		// barter: 0 coins but goods given → NOT free.
		{OccurredAt: t0.Add(-time.Minute), BuyerID: "ez", BuyerName: "Ezekiel Crane", SellerName: "John Ellis", Amount: 0, Item: "1 stew", PayItems: []sim.ItemKindQty{{Kind: "skillet", Qty: 1}}, LedgerID: 331},
		// plain coin sale.
		{OccurredAt: t0.Add(-2 * time.Minute), BuyerID: "hn", BuyerName: "Hannah", SellerName: "John Ellis", Amount: 5, Item: "1 ale", LedgerID: 330},
		// legacy row: no ledger id recorded → has_legacy, free untrustworthy.
		{OccurredAt: t0.Add(-3 * time.Minute), BuyerID: "zz", BuyerName: "Old Row", SellerName: "John Ellis", Amount: 0, Item: "1 bread"},
	}}

	h := settlementsServer(t, store)
	rec := req(t, h, "/api/village/umbilical/settlements", "operator-tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("settlements = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var out UmbilicalSettlementsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if out.Returned != 4 || out.HasMore {
		t.Fatalf("returned/has_more = %d/%v, want 4/false", out.Returned, out.HasMore)
	}
	// give-away
	if g := out.Settlements[0]; !g.Free || g.HasLegacy || g.Amount != 0 || len(g.PayItems) != 0 || !g.ConsumeNow || g.LedgerID != 332 {
		t.Errorf("row[0] (give-away) = %+v, want free/!legacy/0 coins/no goods/consume_now/ledger 332", g)
	}
	// barter — 0 coins but goods → not free
	if b := out.Settlements[1]; b.Free || len(b.PayItems) != 1 || b.PayItems[0].Item != "skillet" || b.PayItems[0].Qty != 1 {
		t.Errorf("row[1] (barter) = %+v, want NOT free with 1 skillet", b)
	}
	// coin sale
	if c := out.Settlements[2]; c.Free || c.Amount != 5 || c.SellerName != "John Ellis" {
		t.Errorf("row[2] (coin sale) = %+v, want NOT free, 5 coins", c)
	}
	// legacy
	if l := out.Settlements[3]; !l.HasLegacy {
		t.Errorf("row[3] (legacy) = %+v, want has_legacy", l)
	}
}

// TestUmbilicalSettlements_ParsesFilters: actor/since/until/ledger reach the store;
// the over-fetch is limit+1.
func TestUmbilicalSettlements_ParsesFilters(t *testing.T) {
	store := &fakeSettlementStore{}
	h := settlementsServer(t, store)
	rec := req(t, h, "/api/village/umbilical/settlements?actor=ez&since=2026-06-25T00:00:00Z&until=2026-06-25T01:00:00Z&ledger=332&limit=10", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("settlements = %d, want 200", rec.Code)
	}
	f := store.gotFilter
	if string(f.ActorID) != "ez" || uint64(f.LedgerID) != 332 {
		t.Errorf("filter actor/ledger = %q/%d, want ez/332", f.ActorID, f.LedgerID)
	}
	if f.Since.IsZero() || f.Until.IsZero() || !f.Until.After(f.Since) {
		t.Errorf("filter since/until = %v/%v, want a valid window", f.Since, f.Until)
	}
	if store.gotLimit != 11 {
		t.Errorf("store limit = %d, want 11 (cap+1 over-fetch for limit=10)", store.gotLimit)
	}
}

// TestUmbilicalSettlements_BadFilters: an unparseable since/until/ledger is a 400
// before the store is touched.
func TestUmbilicalSettlements_BadFilters(t *testing.T) {
	for _, q := range []string{"since=nope", "until=nope", "ledger=abc", "ledger=-1"} {
		store := &fakeSettlementStore{}
		h := settlementsServer(t, store)
		rec := req(t, h, "/api/village/umbilical/settlements?"+q, "tok")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("settlements?%s = %d, want 400", q, rec.Code)
		}
		if store.gotLimit != 0 {
			t.Errorf("settlements?%s touched the store (limit=%d)", q, store.gotLimit)
		}
	}
}

// TestUmbilicalSettlements_Truncates: a result past the cap reports has_more and
// returns exactly the cap — truncation is never silent.
func TestUmbilicalSettlements_Truncates(t *testing.T) {
	rows := make([]sim.SettlementRow, 3) // limit=2 → over-fetch 3 → has_more, trim to 2
	store := &fakeSettlementStore{rows: rows}
	h := settlementsServer(t, store)
	rec := req(t, h, "/api/village/umbilical/settlements?limit=2", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("settlements = %d, want 200", rec.Code)
	}
	var out UmbilicalSettlementsDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.HasMore || out.Returned != 2 {
		t.Errorf("returned/has_more = %d/%v, want 2/true", out.Returned, out.HasMore)
	}
}

// TestUmbilicalSettlements_NotConfigured: the route registers without a store but
// can't serve → 503.
func TestUmbilicalSettlements_NotConfigured(t *testing.T) {
	h := settlementsServer(t, nil)
	rec := req(t, h, "/api/village/umbilical/settlements", "tok")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("settlements with no store = %d, want 503", rec.Code)
	}
}

// TestUmbilicalSettlements_StoreError: a store failure is an honest 500.
func TestUmbilicalSettlements_StoreError(t *testing.T) {
	h := settlementsServer(t, &fakeSettlementStore{err: errors.New("pg down")})
	rec := req(t, h, "/api/village/umbilical/settlements", "tok")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("settlements with store error = %d, want 500", rec.Code)
	}
}

// TestUmbilicalSettlements_Gated: shares the read-surface gate — 404 umbilical-off,
// 403 non-operator, 401 no token.
func TestUmbilicalSettlements_Gated(t *testing.T) {
	const path = "/api/village/umbilical/settlements"

	off := NewServer(seededWorld(t), permAuth{operatorPerms}).Handler()
	if rec := req(t, off, path, "tok"); rec.Code != http.StatusNotFound {
		t.Errorf("settlements umbilical-off = %d, want 404", rec.Code)
	}
	nonOp := NewServer(seededWorld(t), permAuth{nil})
	nonOp.SetTelemetry(telemetry.New(4))
	nonOp.SetSettlementStore(&fakeSettlementStore{})
	hNonOp := nonOp.Handler()
	if rec := req(t, hNonOp, path, "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("settlements non-operator = %d, want 403", rec.Code)
	}
	if rec := req(t, hNonOp, path, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("settlements no token = %d, want 401", rec.Code)
	}
}
