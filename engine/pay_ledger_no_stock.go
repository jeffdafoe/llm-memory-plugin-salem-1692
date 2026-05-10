package main

// pay_ledger no_stock helper (ZBBS-HOME-241).
//
// When a buyer arrives at a seller's location seeking item X and
// finds the seller's stock empty, no pay tool fires today — so no
// pay_ledger row gets created and the cycle filter for the seller's
// later resolve has no record of the relationship. That gap lets a
// supplier later bootstrap-shop their own customer because game-state
// fallback finds the customer holding stock from a prior seed.
//
// The fix: insert a pay_ledger row anyway with state='no_stock'.
// Stamps "I was your customer for X" durably even though no goods
// moved. The cycle filter unions all approach states (accepted,
// declined, countered, no_stock) so this row blocks the cycle on the
// seller's next resolve.
//
// Caller is the buy walk-dispatcher (lands in a follow-up commit).
// Helper is exposed here so the wiring is one line at the call site.

import (
	"context"
	"database/sql"
	"fmt"
)

// recordNoStockAttempt writes a no_stock pay_ledger row representing
// "buyer arrived at seller wanting item, seller had no stock, walked
// home empty." offered_amount is 0 (no offer ever made).
//
// Single auto-commit insert — the record is the only durable side
// effect of the failed visit. Returns the new row's id for any
// audit linkage the caller may want.
func (app *App) recordNoStockAttempt(
	ctx context.Context,
	buyerID, sellerID, itemKind string,
	qty int,
	huddleID, sceneID *string,
) (int64, error) {
	if qty <= 0 {
		qty = 1
	}
	hSQL := sql.NullString{}
	if huddleID != nil {
		hSQL.String = *huddleID
		hSQL.Valid = true
	}
	sSQL := sql.NullString{}
	if sceneID != nil {
		sSQL.String = *sceneID
		sSQL.Valid = true
	}
	var id int64
	err := app.DB.QueryRow(ctx,
		`INSERT INTO pay_ledger (
		    huddle_id, scene_id, buyer_id, seller_id,
		    item_kind, qty, offered_amount, consume_now,
		    state, fulfillment_status, ready_by,
		    created_at, resolved_at
		 ) VALUES (
		    $1, $2, $3::uuid, $4::uuid,
		    $5, $6, 0, false,
		    'no_stock', 'pending', CURRENT_DATE,
		    NOW(), NOW()
		 ) RETURNING id`,
		hSQL, sSQL, buyerID, sellerID,
		itemKind, qty,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert no_stock pay_ledger: %w", err)
	}
	return id, nil
}
