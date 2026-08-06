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

// The payment KIND comes out of the payload, so the extraction is SQL and belongs
// here (LLM-612). Three keys carry it and they do not behave alike:
//
//   - rate_settled is forward-only — written only since LLM-607, only when a rate
//     actually settled;
//   - lodging_grant is forward-only too — written by lodger_rebook only since
//     LLM-615, so the lodging rows already in the table carry no marker;
//   - ledger_id is NOT — handlePayResolvedActionLog has stamped it unconditionally
//     since LLM-105, so it is on every ledger-settled row in the table's history.
//
// That asymmetry is the whole reason the goods classification reaches backwards, and
// it holds only if the query really does read the key off a row that predates the
// classification. The payloads below are the exact shapes the writers produce.
func TestCoinRecordsRepo_Integration_ReadsTheKindMarkers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const payerID = "33333333-3333-3333-3333-333333333333"
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, 'Josiah Thorne', 0, 0)`,
		payerID,
	); err != nil {
		t.Fatalf("seed actor: %v", err)
	}

	base := time.Now().UTC().Truncate(time.Microsecond)
	insert := func(at time.Time, payload string) {
		t.Helper()
		if _, err := f.Pool.Exec(ctx,
			`INSERT INTO agent_action_log (actor_id, occurred_at, source, action_type, payload, result, speaker_name)
			 VALUES ($1, $2, 'agent', 'paid', $3::jsonb, 'ok', 'seed')`,
			payerID, at, payload,
		); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// Oldest first. A ledger settlement from before either classification existed —
	// no rate_settled key, no recipient_actor_id, and ledger_id present all the same.
	insert(base.Add(-3*time.Hour),
		`{"recipient": "Elizabeth Ellis", "amount": 12, "for": "4x cheese", "ledger_id": 881, "consume_now": false}`)
	// A bare pay that settled a town rate.
	insert(base.Add(-2*time.Hour),
		`{"recipient": "Constable Gideon Marsh", "amount": 1, "for": "Day's rate on the General Store", "rate_settled": 1}`)
	// A bare pay that settled nothing the engine can name — no marker at all.
	insert(base.Add(-time.Hour),
		`{"recipient": "Lewis Walker", "amount": 4, "for": "wages for splitting wood"}`)
	// A lodging auto-charge, the shape lodger_rebook writes. The structure id is a
	// bare string where ledger_id is an integer, which is why neither value is ever
	// parsed — only its presence is read.
	insert(base.Add(-30*time.Minute),
		`{"recipient": "John Ellis", "recipient_actor_id": "44444444-4444-4444-4444-444444444444", "amount": 4, "for": "a night's lodging", "lodging_grant": "tavern"}`)
	// A lodging charge from BEFORE LLM-615 — same row minus the marker. It must read
	// back unclassified: the forward-only half of the asymmetry above, and the state
	// every one of the rows already in production is in.
	insert(base.Add(-20*time.Minute),
		`{"recipient": "John Ellis", "amount": 4, "for": "a night's lodging"}`)

	rows, err := NewCoinRecordsRepo(f.Pool).LoadPaymentsSince(ctx, base.Add(-4*time.Hour))
	if err != nil {
		t.Fatalf("LoadPaymentsSince: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("got %d row(s), want 5: %+v", len(rows), rows)
	}
	if rows[0].LedgerID != "881" || rows[0].RateSettled != "" || rows[0].LodgingGrant != "" {
		t.Errorf("historical ledger row = %+v, want LedgerID 881 and no other marker", rows[0])
	}
	if rows[1].RateSettled != "1" || rows[1].LedgerID != "" || rows[1].LodgingGrant != "" {
		t.Errorf("rate row = %+v, want RateSettled 1 and no other marker", rows[1])
	}
	if rows[2].LedgerID != "" || rows[2].RateSettled != "" || rows[2].LodgingGrant != "" {
		t.Errorf("unclassified row = %+v, want no marker", rows[2])
	}
	if rows[3].LodgingGrant != "tavern" || rows[3].LedgerID != "" || rows[3].RateSettled != "" {
		t.Errorf("lodging row = %+v, want LodgingGrant tavern and no other marker", rows[3])
	}
	if rows[4].LodgingGrant != "" || rows[4].LedgerID != "" || rows[4].RateSettled != "" {
		t.Errorf("pre-LLM-615 lodging row = %+v, want no marker at all", rows[4])
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

// A visitor-authored payment reaches agent_action_log with a BLANKED actor_id since
// LLM-573 — the row is kept for the dream pipeline, but the column FKs to actor(id)
// and a visitor lives in the separate visitor table, so it carries no payer.
//
// The seed must skip it. An unattributed payment cannot be keyed to a pair, and
// admitting it would either error the scan or invent an attribution. This is the SQL
// half of the coupling perception's traveler gate rests on; the cascade half is
// cascade.TestHandlePaidActionLog_VisitorPaymentIsTalliedButNotAttributable.
func TestCoinRecordsRepo_Integration_SkipsUnattributedVisitorRows(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const residentID = "11111111-1111-1111-1111-111111111111"
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, 'Hannah Boggs', 0, 0)`,
		residentID,
	); err != nil {
		t.Fatalf("seed actor: %v", err)
	}
	base := time.Now().UTC().Truncate(time.Microsecond)

	// The visitor's payment, as LLM-573 now writes it: real row, NULL actor_id, and
	// the author preserved only as a display name.
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO agent_action_log (actor_id, occurred_at, source, action_type, payload, result, speaker_name)
		 VALUES (NULL, $1, 'agent', 'paid', $2::jsonb, 'ok', 'Elias Drum the peddler')`,
		base.Add(-2*time.Hour),
		`{"recipient": "Hannah Boggs", "recipient_actor_id": "`+residentID+`", "amount": 3}`,
	); err != nil {
		t.Fatalf("insert visitor row: %v", err)
	}
	// Positive control from the same window.
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO agent_action_log (actor_id, occurred_at, source, action_type, payload, result, speaker_name)
		 VALUES ($1, $2, 'agent', 'paid', '{"recipient": "Bob", "amount": 2}'::jsonb, 'ok', 'Hannah Boggs')`,
		residentID, base.Add(-time.Hour),
	); err != nil {
		t.Fatalf("insert resident row: %v", err)
	}

	rows, err := NewCoinRecordsRepo(f.Pool).LoadPaymentsSince(ctx, base.Add(-4*time.Hour))
	if err != nil {
		t.Fatalf("LoadPaymentsSince: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d row(s), want only the attributed one: %+v", len(rows), rows)
	}
	if string(rows[0].PayerID) != residentID {
		t.Errorf("PayerID = %q, want the resident — the visitor row must not be seeded", rows[0].PayerID)
	}
}
