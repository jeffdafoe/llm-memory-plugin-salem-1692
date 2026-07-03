package pg

import (
	"context"
	"testing"
	"time"
)

// orders_max_ledger_integration_test.go — real-pg validation for
// MaxPaidActionLogLedgerID (LLM-245). pgxmock proves the SQL shape but cannot
// exercise the jsonb `->>'ledger_id'` extraction, the `::bigint` cast, or the
// `~ '^[0-9]+$'` guard against the migrated schema. These are exactly the
// behaviors the allocator floor depends on, so they get a real round-trip:
// numeric paid rows count, missing-key / malformed / non-paid rows are
// ignored, and a malformed payload never wedges boot with a cast error.
func TestOrdersRepo_Integration_MaxPaidActionLogLedgerID(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// agent_action_log.actor_id is a uuid FK → actor(id); seed one actor all
	// rows can reference.
	const actorID = "22222222-2222-2222-2222-222222222222"
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO actor (id, display_name, current_x, current_y) VALUES ($1, $2, 0, 0)`,
		actorID, "Moses",
	); err != nil {
		t.Fatalf("seed actor: %v", err)
	}

	now := time.Now().UTC()
	insert := func(actionType, payload string) {
		t.Helper()
		if _, err := f.Pool.Exec(ctx,
			`INSERT INTO agent_action_log
			     (actor_id, occurred_at, source, action_type, payload, result, speaker_name, huddle_id)
			 VALUES ($1, $2, 'engine', $3, $4::jsonb, 'ok', 'Moses', NULL)`,
			actorID, now, actionType, payload,
		); err != nil {
			t.Fatalf("insert %s %s: %v", actionType, payload, err)
		}
	}

	// The true consume_now high-water mark: a paid row whose only durable trace
	// is this ledger_id (no pay_ledger row).
	insert("paid", `{"recipient":"Elizabeth","amount":3,"ledger_id":497}`)
	// A lower paid ledger_id — max() must pick 497 over this.
	insert("paid", `{"recipient":"John","amount":1,"ledger_id":123}`)
	// A paid row with NO ledger_id (the engine-charged lodger-rebook shape) —
	// the `~` guard drops it; it must not be read as 0-or-anything.
	insert("paid", `{"recipient":"Keeper","amount":2,"for":"a night's lodging"}`)
	// A paid row with a malformed ledger_id — the `~ '^[0-9]+$'` guard fences
	// the ::bigint cast off from it so boot can't wedge on a bad audit row.
	insert("paid", `{"recipient":"X","ledger_id":"unknown"}`)
	// A non-paid row carrying a higher ledger_id — the action_type filter
	// excludes it (only paid settlements consume ledger ids).
	insert("spoke", `{"text":"Good morrow","ledger_id":9999}`)

	repo := NewOrdersRepo(f.Pool)
	got, err := repo.MaxPaidActionLogLedgerID(ctx)
	if err != nil {
		t.Fatalf("MaxPaidActionLogLedgerID: %v", err)
	}
	if got != 497 {
		t.Errorf("MaxPaidActionLogLedgerID = %d, want 497 (highest numeric paid ledger_id; missing/malformed/non-paid rows ignored)", got)
	}
}
