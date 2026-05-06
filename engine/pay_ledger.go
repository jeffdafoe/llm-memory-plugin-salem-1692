package main

// pay_ledger helpers (ZBBS-128 step 2). See migrations/ZBBS-128-pay-
// ledger_up.sql for the schema. The ledger captures every pay attempt
// with resolved participants — the row is inserted in its own short
// tx BEFORE the transfer tx opens, then updated to a terminal state
// (accepted | declined | failed) AFTER the transfer tx commits or
// rolls back. Counter chains (state=countered, parent_id, depth) are
// emitted by step 3's deliberation path; aging-sweep withdrawals come
// from step 4.

import (
	"context"
	"database/sql"
	"fmt"
)

// payLedgerInsert holds the fields for an initial pending row. The
// schema's required columns are buyer_id, seller_id, offered_amount,
// consume_now, state — everything else is nullable to cover the full
// pay surface (pure coin transfers have no item_kind/qty, untracked-
// quote pays have no quoted_unit_amount, PC pay has no scene_id at
// the moment of the pay since the cascade UUID is minted afterward).
type payLedgerInsert struct {
	BuyerID          string
	SellerID         string
	HuddleID         sql.NullString
	SceneID          sql.NullString
	ItemKind         sql.NullString
	Qty              sql.NullInt32
	OfferedAmount    int
	QuotedUnitAmount sql.NullInt32
	ConsumeNow       bool
}

// insertPayLedgerPending writes a pending row via a single auto-commit
// statement and returns the assigned bigserial id. Called BEFORE the
// transfer tx so every pay attempt with resolved participants is
// captured durably. If the engine crashes between this insert and the
// transfer outcome, the aging sweep (step 4) eventually flips the
// orphaned row to withdrawn.
func (app *App) insertPayLedgerPending(ctx context.Context, p payLedgerInsert) (int64, error) {
	var id int64
	err := app.DB.QueryRow(ctx,
		`INSERT INTO pay_ledger (
		    huddle_id, scene_id, buyer_id, seller_id,
		    item_kind, qty, offered_amount, quoted_unit_amount,
		    consume_now, state
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'pending')
		 RETURNING id`,
		p.HuddleID, p.SceneID, p.BuyerID, p.SellerID,
		p.ItemKind, p.Qty, p.OfferedAmount, p.QuotedUnitAmount,
		p.ConsumeNow,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert pay_ledger: %w", err)
	}
	return id, nil
}

// updatePayLedger flips a pending row to a terminal state and stamps
// resolved_at. Called AFTER the transfer tx commits or rolls back.
// State must be one of accepted | declined | countered | withdrawn |
// failed (the schema CHECK enforces this; we don't double-validate).
//
// The UPDATE is gated on `state = 'pending'` — a single statement,
// auto-commit. The pending guard plus a RowsAffected==1 check
// prevents accidental double-resolves and surfaces missing rows
// (cascade-deleted seller, prior aging sweep, etc.) as an explicit
// error rather than a silent success. If this fails, the caller
// logs and proceeds with the actual transfer outcome: bookkeeping
// inconsistency (row stuck pending, eventually flipped to withdrawn
// by the aging sweep) is preferable to telling the caller a
// successful transfer failed.
func (app *App) updatePayLedger(ctx context.Context, ledgerID int64, state, message string) error {
	var msg sql.NullString
	if message != "" {
		msg = sql.NullString{String: message, Valid: true}
	}
	tag, err := app.DB.Exec(ctx,
		`UPDATE pay_ledger
		    SET state = $2,
		        message = $3,
		        resolved_at = NOW()
		  WHERE id = $1
		    AND state = 'pending'`,
		ledgerID, state, msg,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("pay_ledger update affected %d rows for id=%d (expected 1; row missing or already terminal)", tag.RowsAffected(), ledgerID)
	}
	return nil
}
