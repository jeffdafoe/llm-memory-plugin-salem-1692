package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// CoinRecordsRepo seeds the per-pair coin tally (LLM-572) from agent_action_log.
//
// READ-ONLY — there is no coin_record table and nothing to checkpoint. The tally is
// derived at boot from the durable `paid` rows the engine already writes on every
// settlement, which is what lets the mechanism survive a restart without a schema
// change of its own. See engine/sim/coin_record.go for why that was chosen over a
// checkpointed aggregate.
type CoinRecordsRepo struct {
	pool Pool
}

// NewCoinRecordsRepo constructs a CoinRecordsRepo against the given pool. Normal
// wiring is pg.NewRepository.
func NewCoinRecordsRepo(pool Pool) *CoinRecordsRepo {
	return &CoinRecordsRepo{pool: pool}
}

// loadPaymentsSinceSQL pulls the settled coin payments inside the window.
//
// Both `paid` writers — handlePaidActionLog (bare coin) and
// handlePayResolvedActionLog (a pay-with-item ledger entry settling ACCEPTED) —
// store the same {recipient, amount} payload keys, so one predicate covers every
// path coin moves through. recipient_actor_id is stamped only on rows written since
// LLM-572; the caller falls back to the display name for older rows.
//
// The amount comes back as TEXT and is parsed in Go rather than cast here. The
// column is jsonb, so a single malformed value would fail the cast and take the
// whole boot query with it — and Postgres gives no ordering guarantee that would
// let a WHERE clause filter the bad row out before the SELECT list evaluates it.
// A barter settles for 0 coin and still writes a `paid` row, so the caller drops
// non-positive amounts: this is a record of coin, and no coin passed.
//
// rate_settled (LLM-607) is present only on rows whose coin discharged town-rate
// arrears, and only since that ticket. Absent on every purchase and on every
// historical row, both of which read back as an ordinary payment — the same
// forward-only shape recipient_actor_id has. Read as TEXT for the reason amount is.
//
// result = 'ok' excludes rejected/failed/declined/countered attempts — nothing
// moved on those. actor_id NOT NULL excludes the engine-authored rows that carry no
// payer.
//
// No index is needed or added: the scan is bounded by the occurred_at window and
// runs exactly once, at boot.
const loadPaymentsSinceSQL = `
SELECT actor_id,
       occurred_at,
       COALESCE(payload->>'amount', ''),
       COALESCE(payload->>'recipient_actor_id', ''),
       COALESCE(payload->>'recipient', ''),
       COALESCE(payload->>'rate_settled', '')
  FROM agent_action_log
 WHERE action_type = 'paid'
   AND result = 'ok'
   AND actor_id IS NOT NULL
   AND occurred_at >= $1
 ORDER BY occurred_at`

// LoadPaymentsSince reads the settled payments at or after `since`, oldest-first.
// Read-only restart path off the pool, the same posture as the LoadAll
// implementations.
//
// Deliberately does no resolution, filtering or validation beyond the query: the
// caller (FinalizeLoad, via rehydrateCoinRecordOnLoad) owns the clock, the actor
// index and the ambiguity rule, so those live in one testable place without a
// database.
func (r *CoinRecordsRepo) LoadPaymentsSince(ctx context.Context, since time.Time) ([]sim.CoinPaymentRow, error) {
	rows, err := r.pool.Query(ctx, loadPaymentsSinceSQL, since)
	if err != nil {
		return nil, fmt.Errorf("pg coin records LoadPaymentsSince query: %w", err)
	}
	defer rows.Close()

	var out []sim.CoinPaymentRow
	for rows.Next() {
		var (
			payerID       string
			at            time.Time
			amount        string
			recipientID   string
			recipientName string
			rateSettled   string
		)
		if err := rows.Scan(&payerID, &at, &amount, &recipientID, &recipientName, &rateSettled); err != nil {
			return nil, fmt.Errorf("pg coin records LoadPaymentsSince scan: %w", err)
		}
		out = append(out, sim.CoinPaymentRow{
			PayerID:          sim.ActorID(payerID),
			At:               at,
			Amount:           amount,
			RecipientActorID: recipientID,
			RecipientName:    recipientName,
			RateSettled:      rateSettled,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg coin records LoadPaymentsSince iter: %w", err)
	}
	return out, nil
}
