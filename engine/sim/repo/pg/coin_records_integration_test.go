package pg

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
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
	if rows[0].Amount != "1" || rows[0].CounterpartyName != "Constable Gideon Marsh" || rows[0].CounterpartyActorID != "" {
		t.Errorf("historical row = %+v, want amount 1 / name-only recipient", rows[0])
	}
	if rows[1].Amount != "4" || rows[1].CounterpartyActorID != otherID {
		t.Errorf("stamped row = %+v, want amount 4 and the recipient id", rows[1])
	}
	if string(rows[0].ActorID) != payerID {
		t.Errorf("ActorID = %q, want the payer %q", rows[0].ActorID, payerID)
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

// Marker precedence needs BOTH markers to reach the classifier off one row, and
// whether they do is a property of this SQL (code_review, LLM-615). No live path
// writes a due beside a goods marker today, so this pins the extraction against a
// future one that does.
//
// The precedence itself — the due winning — is asserted in package sim, where
// coinPaymentKindFromRow lives and is unexported. That split is the layering, not a
// gap: this file owns getting the keys out of jsonb, sim owns what they mean. Read
// the two together; neither is the whole invariant.
//
// Why the due must win: it is what stops a levy reading as an order placed and never
// filled. Reading a levy as a purchase tells an NPC goods are owed him that never
// were, which is the LLM-607 defect; reading a purchase as a due only leaves it
// unqualified. The asymmetry is deliberate, not a tie-break.
func TestCoinRecordsRepo_Integration_ReadsBothMarkersOffOneRow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const payerID = "55555555-5555-5555-5555-555555555555"
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, 'Ezekiel Crane', 0, 0)`,
		payerID,
	); err != nil {
		t.Fatalf("seed actor: %v", err)
	}

	base := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO agent_action_log (actor_id, occurred_at, source, action_type, payload, result, speaker_name)
		 VALUES ($1, $2, 'engine', 'paid', $3::jsonb, 'ok', 'seed')`,
		payerID, base.Add(-time.Hour),
		`{"recipient": "John Ellis", "amount": 4, "for": "a night's lodging", "lodging_grant": "tavern", "rate_settled": 1}`,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}

	rows, err := NewCoinRecordsRepo(f.Pool).LoadPaymentsSince(ctx, base.Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("LoadPaymentsSince: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d row(s), want 1: %+v", len(rows), rows)
	}
	if rows[0].RateSettled != "1" || rows[0].LodgingGrant != "tavern" {
		t.Errorf("row = %+v, want BOTH markers extracted (RateSettled 1, LodgingGrant tavern) so the classifier can rank them", rows[0])
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
	if string(rows[0].ActorID) != residentID {
		t.Errorf("ActorID = %q, want the resident — the visitor row must not be seeded", rows[0].ActorID)
	}
}

// A `labored` row is a payment too, and it comes back under the other set of payload
// keys (LLM-613).
//
// This is SQL and belongs here for the reason the kind extraction does: the
// counterparty column is chosen by action_type, so the query has to read the right
// key for each shape. TestCoinRecordsRepo_Integration_RowTypeWinsOverAStrayKey covers
// what happens when a payload carries both.
//
// It also pins the direction discriminator. `labored` and `paid` come back through
// one query and one struct, so the action_type column is the only thing telling the
// caller that a wage row's actor_id is the PAYEE — get that wrong and every seeded
// wage reverses, with nothing failing.
func TestCoinRecordsRepo_Integration_SelectsCompletedWages(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const (
		workerID   = "33333333-3333-3333-3333-333333333333"
		employerID = "44444444-4444-4444-4444-444444444444"
	)
	for _, a := range []struct{ id, name string }{
		{workerID, "Lewis Walker"},
		{employerID, "Josiah Thorne"},
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

	// The historical wage shape: employer by display name only, no id stamped.
	insert(workerID, base.Add(-3*time.Hour), "labored", "ok",
		`{"employer": "Josiah Thorne", "amount": 4, "duration_min": 30, "labor_id": 9}`)
	// The shape written since this ticket.
	insert(workerID, base.Add(-2*time.Hour), "labored", "ok",
		`{"employer": "Josiah Thorne", "employer_actor_id": "`+employerID+`", "amount": 4, "labor_id": 11}`)
	// A purchase the other way, so both shapes are in one result set.
	insert(workerID, base.Add(-time.Hour), "paid", "ok",
		`{"recipient": "Josiah Thorne", "recipient_actor_id": "`+employerID+`", "amount": 10, "ledger_id": 2053}`)
	// A contract that was accepted but never settled moves no coin.
	insert(workerID, base.Add(-time.Hour), "hired", "ok",
		`{"worker": "Lewis Walker", "amount": 4}`)

	rows, err := NewCoinRecordsRepo(f.Pool).LoadPaymentsSince(ctx, base.Add(-4*time.Hour))
	if err != nil {
		t.Fatalf("LoadPaymentsSince: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d row(s), want the two wages and the purchase: %+v", len(rows), rows)
	}
	for i, want := range []struct {
		source          sim.CoinPaymentSource
		counterpartyID  string
		counterpartyNam string
	}{
		{sim.CoinPaymentSourceLabored, "", "Josiah Thorne"},
		{sim.CoinPaymentSourceLabored, employerID, "Josiah Thorne"},
		{sim.CoinPaymentSourcePaid, employerID, "Josiah Thorne"},
	} {
		if rows[i].Source != want.source {
			t.Errorf("row %d source = %v, want %v", i, rows[i].Source, want.source)
		}
		if rows[i].CounterpartyActorID != want.counterpartyID {
			t.Errorf("row %d counterparty id = %q, want %q", i, rows[i].CounterpartyActorID, want.counterpartyID)
		}
		if rows[i].CounterpartyName != want.counterpartyNam {
			t.Errorf("row %d counterparty name = %q, want %q", i, rows[i].CounterpartyName, want.counterpartyNam)
		}
		// Every row is the worker's, whichever direction its coin went.
		if string(rows[i].ActorID) != workerID {
			t.Errorf("row %d actor = %q, want the worker %q", i, rows[i].ActorID, workerID)
		}
	}
	// The wage carries no goods marker — its row type is its classification.
	if rows[1].LedgerID != "" || rows[1].RateSettled != "" {
		t.Errorf("a wage row carries a paid-path marker: %+v", rows[1])
	}
}

// A payload carrying BOTH shapes' counterparty keys is read by its row type, not by
// whichever key the query happens to look at first (LLM-613, code_review).
//
// No live row does this — of 3,708 `paid` and `labored` rows, none carries both — and
// no current writer can produce one. The case is here because the alternative query
// shape (COALESCE across the two key names) reads correctly ONLY while that holds,
// and would fail silently the day it stopped: every wage would be attributed to the
// `recipient` field, giving a plausible record with the wrong counterparty. Selecting
// by action_type makes the row type the authority, and this pins that it is.
//
// The assertion is deliberately about which key WINS rather than about rejecting the
// row. A stray key is a writer bug to fix at the writer; dropping the row here would
// turn it into missing money, and understating is the one direction this seed must
// not fail in.
func TestCoinRecordsRepo_Integration_RowTypeWinsOverAStrayKey(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const (
		workerID   = "55555555-5555-5555-5555-555555555555"
		employerID = "66666666-6666-6666-6666-666666666666"
		strayID    = "77777777-7777-7777-7777-777777777777"
	)
	for _, a := range []struct{ id, name string }{
		{workerID, "Lewis Walker"},
		{employerID, "Josiah Thorne"},
		{strayID, "Hannah Boggs"},
	} {
		if _, err := f.Pool.Exec(ctx,
			`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, $2, 0, 0)`,
			a.id, a.name,
		); err != nil {
			t.Fatalf("seed actor %s: %v", a.name, err)
		}
	}

	base := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO agent_action_log (actor_id, occurred_at, source, action_type, payload, result, speaker_name)
		 VALUES ($1, $2, 'agent', 'labored', $3::jsonb, 'ok', 'seed')`,
		workerID, base.Add(-time.Hour),
		`{"employer": "Josiah Thorne", "employer_actor_id": "`+employerID+`",
		  "recipient": "Hannah Boggs", "recipient_actor_id": "`+strayID+`", "amount": 4}`,
	); err != nil {
		t.Fatalf("insert conflicting payload: %v", err)
	}

	rows, err := NewCoinRecordsRepo(f.Pool).LoadPaymentsSince(ctx, base.Add(-4*time.Hour))
	if err != nil {
		t.Fatalf("LoadPaymentsSince: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d row(s), want 1 — a stray key must not drop the payment: %+v", len(rows), rows)
	}
	if rows[0].CounterpartyActorID != employerID || rows[0].CounterpartyName != "Josiah Thorne" {
		t.Errorf("counterparty = %q / %q, want the EMPLOYER — a `labored` row's counterparty is never the recipient field",
			rows[0].CounterpartyActorID, rows[0].CounterpartyName)
	}
}
