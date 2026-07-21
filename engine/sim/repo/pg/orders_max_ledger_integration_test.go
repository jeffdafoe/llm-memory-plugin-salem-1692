package pg

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// orders_max_ledger_integration_test.go — real-pg validation for
// MaxPaidActionLogLedgerID (LLM-245). pgxmock proves the SQL shape but cannot
// exercise the query against the migrated schema. Since LLM-494 the query reads
// the typed ledger_id column (backed by the partial index), so the behaviors the
// allocator floor depends on — numeric paid rows count, missing / malformed
// (NULL column) / non-paid rows are ignored — get a real round-trip.
//
// Rows go in through the real write path (writeOne), so the ledger_id column is
// derived by the production insert guard itself (insertActionLogSQL) rather than
// a replicated copy of it — a malformed value lands NULL exactly as it would live.
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

	r := NewActionLogRepo(f.Pool)
	now := time.Now().UTC()
	write := func(actionType sim.ActionType, payload map[string]any) {
		t.Helper()
		r.writeOne(sim.DurableActionLogRow{
			ActorID:     actorID,
			OccurredAt:  now,
			ActionType:  actionType,
			Payload:     payload,
			SpeakerName: "Moses",
			Source:      "engine",
		})
	}

	// The true consume_now high-water mark: a paid row whose only durable trace
	// is this ledger_id (no pay_ledger row).
	write(sim.ActionTypePaid, map[string]any{"recipient": "Elizabeth", "amount": 3, "ledger_id": sim.LedgerID(497)})
	// A lower paid ledger_id — max() must pick 497 over this.
	write(sim.ActionTypePaid, map[string]any{"recipient": "John", "amount": 1, "ledger_id": sim.LedgerID(123)})
	// A paid row with NO ledger_id (the engine-charged lodger-rebook shape) — its
	// column is NULL; it must not be read as 0-or-anything.
	write(sim.ActionTypePaid, map[string]any{"recipient": "Keeper", "amount": 2, "for": "a night's lodging"})
	// A paid row with a malformed ledger_id — the insert guard leaves the column
	// NULL so boot can't wedge on a bad audit row.
	write(sim.ActionTypePaid, map[string]any{"recipient": "X", "ledger_id": "unknown"})
	// A non-paid row carrying a higher ledger_id — its column is populated (the
	// universal mirror), but the action_type filter excludes it (only paid
	// settlements consume ledger ids).
	write(sim.ActionTypeSpoke, map[string]any{"text": "Good morrow", "ledger_id": sim.LedgerID(9999)})

	repo := NewOrdersRepo(f.Pool)
	got, err := repo.MaxPaidActionLogLedgerID(ctx)
	if err != nil {
		t.Fatalf("MaxPaidActionLogLedgerID: %v", err)
	}
	if got != 497 {
		t.Errorf("MaxPaidActionLogLedgerID = %d, want 497 (highest numeric paid ledger_id; missing/malformed/non-paid rows ignored)", got)
	}
}
