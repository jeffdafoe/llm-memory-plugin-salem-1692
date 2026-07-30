package pg

import (
	"context"
	"testing"
	"time"
)

// coin_records_integration_test.go — real-pg coverage for the coin-record boot seed
// (LLM-572). Run against embedded Postgres with the full prod-baseline schema +
// post-baseline migrations applied; skipped under `go test -short`.
//
// This proves what the sim-side unit tests cannot: that the query actually selects
// the right rows out of the real agent_action_log. Everything interesting about it
// is SQL — jsonb text extraction, the COALESCE that keeps a missing key from
// failing the scan, the result/action_type/NULL-actor filters, and the occurred_at
// window and ordering. The seed is a NEGATIVE-claim source ("no coin has passed
// between you"), so a predicate that silently drops rows produces a confident lie
// in an NPC's scene rather than a visible failure.

func TestCoinRecordsRepo_Integration_SelectsSettledPayments(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const (
		payerID = "11111111-1111-1111-1111-111111111111"
		otherID = "22222222-2222-2222-2222-222222222222"
	)
	for _, a := range []struct{ id, name string }{
		{payerID, "Moses James"},
		{otherID, "Hannah Boggs"},
	} {
		if _, err := f.Pool.Exec(ctx,
			`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, $2, 0, 0)`,
			a.id, a.name,
		); err != nil {
			t.Fatalf("seed actor %s: %v", a.name, err)
		}
	}

	base := time.Now().UTC().Truncate(time.Microsecond)
	insert := func(actorID any, at time.Time, actionType, result, payload string) {
		t.Helper()
		if _, err := f.Pool.Exec(ctx,
			`INSERT INTO agent_action_log (actor_id, occurred_at, source, action_type, payload, result, speaker_name)
			 VALUES ($1, $2, 'agent', $3, $4::jsonb, $5, 'seed')`,
			actorID, at, actionType, payload, result,
		); err != nil {
			t.Fatalf("insert %s/%s: %v", actionType, result, err)
		}
	}

	// Two rows that must come back, oldest first. The second carries
	// recipient_actor_id, the shape written since this ticket; the first is the
	// historical name-only shape.
	insert(payerID, base.Add(-3*time.Hour), "paid", "ok",
		`{"recipient": "Constable Gideon Marsh", "amount": 1, "for": "Day's rate on the James Farm"}`)
	insert(payerID, base.Add(-2*time.Hour), "paid", "ok",
		`{"recipient": "Hannah Boggs", "recipient_actor_id": "`+otherID+`", "amount": 4}`)

	// Rows that must NOT come back.
	insert(payerID, base.Add(-time.Hour), "paid", "rejected",
		`{"recipient": "Hannah Boggs", "amount": 9}`) // nothing moved
	insert(payerID, base.Add(-time.Hour), "spoke", "ok",
		`{"text": "Good morrow."}`) // not a payment
	insert(nil, base.Add(-time.Hour), "paid", "ok",
		`{"recipient": "Hannah Boggs", "amount": 9}`) // engine-authored, no payer
	insert(payerID, base.Add(-48*time.Hour), "paid", "ok",
		`{"recipient": "Hannah Boggs", "amount": 9}`) // outside the window

	rows, err := NewCoinRecordsRepo(f.Pool).LoadPaymentsSince(ctx, base.Add(-4*time.Hour))
	if err != nil {
		t.Fatalf("LoadPaymentsSince: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d row(s), want 2: %+v", len(rows), rows)
	}
	// Oldest first — the seed replays them in order, and appendCoinPayment's window
	// pruning selects by position on a chronological trail.
	if !rows[0].At.Before(rows[1].At) {
		t.Errorf("rows are not oldest-first: %+v", rows)
	}
	if rows[0].Amount != "1" || rows[0].RecipientName != "Constable Gideon Marsh" || rows[0].RecipientActorID != "" {
		t.Errorf("historical row = %+v, want amount 1 / name-only recipient", rows[0])
	}
	if rows[1].Amount != "4" || rows[1].RecipientActorID != otherID {
		t.Errorf("stamped row = %+v, want amount 4 and the recipient id", rows[1])
	}
	if string(rows[0].PayerID) != payerID {
		t.Errorf("PayerID = %q, want %q", rows[0].PayerID, payerID)
	}
}

// A `paid` row with no amount key at all must scan rather than blow up the boot
// query. COALESCE is what makes that true, and the caller drops the row on the
// parse — which is the correct outcome, since a payment with no amount is not a
// coin record. A failed boot query would take the whole engine down with it.
func TestCoinRecordsRepo_Integration_MissingAmountDoesNotFailTheScan(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const payerID = "11111111-1111-1111-1111-111111111111"
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, 'Moses James', 0, 0)`,
		payerID,
	); err != nil {
		t.Fatalf("seed actor: %v", err)
	}
	base := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO agent_action_log (actor_id, occurred_at, source, action_type, payload, result, speaker_name)
		 VALUES ($1, $2, 'agent', 'paid', '{"recipient": "Hannah Boggs"}'::jsonb, 'ok', 'seed')`,
		payerID, base.Add(-time.Hour),
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
	rows, err := NewCoinRecordsRepo(f.Pool).LoadPaymentsSince(ctx, base.Add(-4*time.Hour))
	if err != nil {
		t.Fatalf("LoadPaymentsSince: %v", err)
	}
	if len(rows) != 1 || rows[0].Amount != "" {
		t.Fatalf("got %+v, want one row with an empty amount", rows)
	}
}
