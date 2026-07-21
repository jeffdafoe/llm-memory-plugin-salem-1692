package pg

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// action_log_ledger_id_integration_test.go — LLM-494 real-pg coverage for the
// promotion of agent_action_log.ledger_id from the jsonb payload to a typed
// column: that the migration backfill reproduces the payload extraction exactly,
// that the settlements filter is now a genuine numeric compare, and that the boot
// max query actually uses the partial index.

// runLLM494Migration re-applies the LLM-494 _up migration on the fixture DB. The
// migration is rerun-safe (ADD COLUMN IF NOT EXISTS, backfill gated on
// `ledger_id IS NULL`, CREATE INDEX IF NOT EXISTS), so replaying it over rows
// inserted with a NULL column exercises the real backfill — the same idiom the
// LLM-493 recent-prices test uses.
func runLLM494Migration(t *testing.T, f *integrationFixture) {
	t.Helper()
	dir, err := findMigrationsDir()
	if err != nil {
		t.Fatalf("findMigrationsDir: %v", err)
	}
	sqlBytes, err := os.ReadFile(filepath.Join(dir, "LLM-494-action-log-ledger-id-column_up.sql"))
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := f.Pool.Exec(context.Background(), string(sqlBytes)); err != nil {
		t.Fatalf("re-run LLM-494 migration: %v", err)
	}
}

// TestActionLog_Integration_LedgerIDBackfillMatchesExtraction is the DoD's
// central guarantee: the backfilled column equals what the payload extraction
// produced, on every row. Rows are inserted with a NULL column (the pre-migration
// shape), the migration is re-run, and each row's column is checked against an
// independently-computed expectation — including the malformed and 19+ digit
// cases that must land NULL (the CASE guard) rather than abort the migration.
func TestActionLog_Integration_LedgerIDBackfillMatchesExtraction(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const actorID = "44444444-4444-4444-4444-444444444444"
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, $2, 0, 0)`,
		actorID, "Silence",
	); err != nil {
		t.Fatalf("seed actor: %v", err)
	}

	i64 := func(v int64) *int64 { return &v }
	now := time.Now().UTC()
	cases := []struct {
		name       string
		actionType string
		payload    string
		want       *int64 // expected ledger_id column after backfill; nil = NULL
	}{
		{"paid numeric", "paid", `{"recipient":"A","amount":3,"ledger_id":100}`, i64(100)},
		// Universal mirror: a non-paid haggle beat carries the column too.
		{"non-paid numeric", "offered", `{"ledger_id":205,"item":"nail","qty":5}`, i64(205)},
		{"no ledger_id key", "paid", `{"recipient":"B","for":"a night's lodging"}`, nil},
		{"non-numeric ledger_id", "paid", `{"recipient":"C","ledger_id":"not-a-number"}`, nil},
		// A valid 19-digit id at bigint's max — the old ::bigint reader accepted
		// it, so the numeric-range guard must too (a length cap would have dropped
		// it, regressing the floor).
		{"max bigint (19 digits)", "paid", `{"recipient":"E","ledger_id":9223372036854775807}`, i64(9223372036854775807)},
		// A short digits-only value with leading zeros — ::bigint normalizes the
		// zeros, so it backfills to 42.
		{"leading zeros", "paid", `{"recipient":"F","ledger_id":"0042"}`, i64(42)},
		// Just past bigint's max: the numeric compare rejects it → NULL, no abort.
		{"bigint max + 1", "paid", `{"recipient":"G","ledger_id":9223372036854775808}`, nil},
		// 33 digits: far past range — the CASE skips the ::bigint cast, the value
		// lands NULL and the migration does NOT abort with an overflow error.
		{"oversized ledger_id", "paid", `{"recipient":"D","ledger_id":"999999999999999999999999999999999"}`, nil},
	}

	ids := make([]int64, len(cases))
	for i, c := range cases {
		// Insert WITHOUT the ledger_id column — the pre-migration row shape the
		// backfill has to repair.
		if err := f.Pool.QueryRow(ctx,
			`INSERT INTO agent_action_log
			     (actor_id, occurred_at, source, action_type, payload, result, speaker_name, huddle_id)
			 VALUES ($1, $2, 'engine', $3, $4::jsonb, 'ok', 'Silence', NULL)
			 RETURNING id`,
			actorID, now, c.actionType, c.payload,
		).Scan(&ids[i]); err != nil {
			t.Fatalf("insert %q: %v", c.name, err)
		}
	}

	// Re-run the real migration — this is what performs (and is under test) the
	// backfill. That it returns no error is the "does not abort on the oversized
	// row" assertion.
	runLLM494Migration(t, f)

	for i, c := range cases {
		var got *int64
		if err := f.Pool.QueryRow(ctx,
			`SELECT ledger_id FROM agent_action_log WHERE id = $1`, ids[i],
		).Scan(&got); err != nil {
			t.Fatalf("read back %q: %v", c.name, err)
		}
		switch {
		case c.want == nil && got != nil:
			t.Errorf("%s: ledger_id = %d, want NULL", c.name, *got)
		case c.want != nil && got == nil:
			t.Errorf("%s: ledger_id = NULL, want %d", c.name, *c.want)
		case c.want != nil && got != nil && *got != *c.want:
			t.Errorf("%s: ledger_id = %d, want %d", c.name, *got, *c.want)
		}
	}

	// And the DoD's exact claim, stated directly: NO row's column disagrees with
	// the payload extraction (the same guarded expression the migration uses).
	// IS DISTINCT FROM treats NULL == NULL, so absent/malformed rows match too.
	var mismatches int
	if err := f.Pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_action_log
		  WHERE ledger_id IS DISTINCT FROM
		        CASE
		            WHEN payload->>'ledger_id' ~ '^[0-9]{1,18}$'
		                THEN (payload->>'ledger_id')::bigint
		            WHEN payload->>'ledger_id' ~ '^[0-9]{19}$'
		             AND payload->>'ledger_id' <= '9223372036854775807'
		                THEN (payload->>'ledger_id')::bigint
		        END`,
	).Scan(&mismatches); err != nil {
		t.Fatalf("mismatch count: %v", err)
	}
	if mismatches != 0 {
		t.Errorf("%d row(s) have a ledger_id column that disagrees with the payload extraction", mismatches)
	}
}

// TestLoadSettlements_Integration_NumericLedgerFilter proves the converted filter
// is a genuine numeric compare on the typed column, not the former text match on
// payload->>'ledger_id'. The divergence row stores ledger_id as the string "042":
// the old `payload->>'ledger_id' = '42'` text compare would MISS it, but its
// column backfills to 42, so the numeric filter matches it — the exact silent-
// empty latent bug the ticket calls out. A control row (id 99) must be excluded.
func TestLoadSettlements_Integration_NumericLedgerFilter(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const actorID = "55555555-5555-5555-5555-555555555555"
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, $2, 0, 0)`,
		actorID, "Ezekiel",
	); err != nil {
		t.Fatalf("seed actor: %v", err)
	}

	r := NewActionLogRepo(f.Pool)
	now := time.Now().UTC()
	// Rows go in through the real write path, so the column is derived by the
	// production insert guard. speaker_name distinguishes the rows on read-back.
	write := func(speaker string, payload map[string]any) {
		t.Helper()
		r.writeOne(sim.DurableActionLogRow{
			ActorID:     actorID,
			OccurredAt:  now,
			ActionType:  sim.ActionTypePaid,
			Payload:     payload,
			SpeakerName: speaker,
			Source:      "agent",
		})
	}

	write("Seller-A", map[string]any{"recipient": "Seller-A", "amount": 3, "for": "1 bread", "ledger_id": sim.LedgerID(42)})
	// Divergence row: ledger_id stored as the string "042" — the old text compare
	// on '42' would miss it; the insert guard derives its column to 42, so the
	// numeric filter matches. Its payload fails the uint64 decode in
	// fillSettlementPayload (a string, not a number), so the row degrades to bare
	// columns — but it is still RETURNED, matched on the column, which is the point.
	write("Seller-B", map[string]any{"recipient": "Seller-B", "amount": 5, "for": "1 ale", "ledger_id": "042"})
	// Control: a different id that must NOT match a filter for 42.
	write("Seller-C", map[string]any{"recipient": "Seller-C", "amount": 2, "for": "1 stew", "ledger_id": sim.LedgerID(99)})

	got, err := r.LoadSettlements(ctx, sim.SettlementFilter{LedgerID: 42}, 10)
	if err != nil {
		t.Fatalf("LoadSettlements: %v", err)
	}

	names := map[string]bool{}
	for _, r := range got {
		names[r.BuyerName] = true
	}
	if len(got) != 2 || !names["Seller-A"] || !names["Seller-B"] {
		t.Errorf("filter ledger_id=42 returned %d rows %v, want exactly {Seller-A, Seller-B} (the canonical row AND the leading-zero row the text compare would have missed)", len(got), names)
	}
	if names["Seller-C"] {
		t.Error("filter ledger_id=42 wrongly returned the id-99 control row")
	}
}

// TestActionLog_Integration_MaxLedgerUsesPartialIndex is the DoD's EXPLAIN check:
// the boot allocator-floor query is served by the partial index, not a seq scan.
// enable_seqscan is disabled so the planner reveals whether the query is indexable
// in this shape independent of the tiny test table's row count (on which it would
// otherwise always seq-scan).
func TestActionLog_Integration_MaxLedgerUsesPartialIndex(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	const actorID = "66666666-6666-6666-6666-666666666666"
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, $2, 0, 0)`,
		actorID, "Moses",
	); err != nil {
		t.Fatalf("seed actor: %v", err)
	}
	now := time.Now().UTC()
	for _, id := range []int{101, 202, 303} {
		if _, err := f.Pool.Exec(ctx,
			`INSERT INTO agent_action_log
			     (actor_id, occurred_at, source, action_type, payload, result, speaker_name, huddle_id, ledger_id)
			 VALUES ($1, $2, 'engine', 'paid', jsonb_build_object('ledger_id', $3::bigint), 'ok', 'Moses', NULL, $3)`,
			actorID, now, id,
		); err != nil {
			t.Fatalf("seed paid row %d: %v", id, err)
		}
	}

	// Pin one connection so the SET and the EXPLAIN run on the same session.
	conn, err := f.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SET enable_seqscan = off"); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}

	rows, err := conn.Query(ctx, "EXPLAIN "+maxPaidActionLogLedgerIDSQL)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan line: %v", err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate plan: %v", err)
	}
	if !strings.Contains(plan.String(), "ix_agent_action_log_paid_ledger_id") {
		t.Errorf("boot max query does not use the partial index ix_agent_action_log_paid_ledger_id; plan:\n%s", plan.String())
	}
}
