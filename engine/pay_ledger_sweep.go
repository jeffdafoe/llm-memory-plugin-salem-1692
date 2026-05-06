package main

// Pay-ledger aging sweep (ZBBS-128 step 4). Periodic background task
// that flips orphaned `pending` rows to `withdrawn` so the ledger
// doesn't accumulate stuck rows from engine crashes mid-deliberation.
//
// Why this exists:
//
//   - executePay's deliberation gate runs an LLM tick with no DB
//     locks held. If the engine crashes between the pending insert
//     (Tx A) and the deliberation outcome flipping the row terminal,
//     the row stays `pending` forever without the sweep.
//   - executePayTransfer can return CommitUnknown when tx.Commit
//     errors mid-flight (Postgres may have committed before the
//     connection failed). Step 2's executePay leaves the row
//     `pending` in that case for ops review; the sweep eventually
//     ages it out so dashboards / metrics don't show a growing
//     backlog of unresolved attempts.
//   - Future: a buyer-cancel UI (no current ETA) would also flip to
//     `withdrawn`, sharing this terminal state.
//
// 10-minute cutoff is wildly conservative for crash recovery.
// Deliberation has a 5s LLM timeout; a real deliberation won't
// outlive 30 seconds even with DB roundtrips. Anything still
// pending after 10 minutes is dead.

import (
	"context"
	"log"
	"time"
)

// payLedgerPendingTimeout is how long a pay_ledger row may sit in
// the `pending` state before the aging sweep flips it to withdrawn.
// Set well above any realistic deliberation duration so legitimate
// in-flight pays are never caught.
const payLedgerPendingTimeout = 10 * time.Minute

// payLedgerSweepInterval is the cadence of the sweep tick. The
// partial index ix_pay_ledger_pending makes the UPDATE cheap even
// at higher rates, but once-per-minute is plenty for the recovery
// use case — orphans aren't actively harmful, just unsightly.
const payLedgerSweepInterval = time.Minute

// runPayLedgerSweep is the long-running background goroutine that
// periodically flips aged `pending` rows to `withdrawn`. Started
// from main alongside the other periodic tasks. Idempotent on a
// quiet ledger — the partial index makes the no-op case constant
// time.
func (app *App) runPayLedgerSweep(ctx context.Context) {
	t := time.NewTicker(payLedgerSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			app.sweepPayLedgerOnce(ctx)
		}
	}
}

// sweepPayLedgerOnce runs a single aging-sweep pass. Logs the row
// count when non-zero; silent on a clean pass to avoid spamming
// "0 rows aged" every minute. Errors are logged and we move on —
// the next tick gets another shot.
func (app *App) sweepPayLedgerOnce(ctx context.Context) {
	tag, err := app.DB.Exec(ctx,
		`UPDATE pay_ledger
		    SET state       = 'withdrawn',
		        message     = 'aged out',
		        resolved_at = NOW()
		  WHERE state      = 'pending'
		    AND created_at < NOW() - ($1::int * INTERVAL '1 second')`,
		// Pass the timeout as seconds so the Go constant stays the
		// source of truth. (time.Duration.String returns "10m0s"
		// which Postgres interval literal grammar doesn't accept.)
		int(payLedgerPendingTimeout.Seconds()),
	)
	if err != nil {
		log.Printf("pay_ledger sweep: %v", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		log.Printf("pay_ledger sweep: aged out %d row(s)", n)
	}
}
