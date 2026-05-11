package main

// pay_ledger helpers (ZBBS-128 step 2). See migrations/ZBBS-128-pay-
// ledger_up.sql for the schema. The ledger captures every pay attempt
// with resolved participants — the row is inserted in its own short
// tx BEFORE the transfer tx opens, then updated to a terminal state
// (accepted | declined | failed) AFTER the transfer tx commits or
// rolls back. Counter chains (state=countered, parent_id, depth) are
// emitted by step 3's deliberation path; aging-sweep withdrawals come
// from step 4.
//
// ZBBS-129 step 2 added the fulfillment-status orthogonal lifecycle
// (pending → ready → delivered). Ready_by + fulfillment_status are
// populated at insert; transitions to 'delivered' happen via
// executeDeliverOrder in engine/order_fulfillment.go.
// Item-bearing inserts set ready_by = CURRENT_DATE and
// fulfillment_status = 'ready' — current item_kind rows all carry
// hours_per_unit = NULL/0 (immediate). When craft items with
// hours_per_unit > 0 arrive, this will branch on the item's lead time:
// pending for crafts, ready for immediate goods.
//
// ZBBS-HOME-260: coin-only pays (item_kind IS NULL — tips, gifts,
// condolences, news payments) insert with fulfillment_status =
// 'delivered'. Coins move atomically inside the same transfer tx, so
// there is nothing left for executeDeliverOrder to ship. Without this
// branch the row sits at 'ready' forever and surfaces in the seller's
// "outstanding orders" perception (readyOrdersForSeller filters
// fulfillment_status='ready'), prompting endless deliver_order(N)
// rejections of "ledger row N carries no item to deliver."

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
//
// ParentID + Depth (ZBBS-128 step 3) carry the counter-chain link.
// ParentID.Valid is true only when this pay is the buyer's response to
// a prior `countered` row — the optional `in_response_to` argument the
// pay tool surface accepts. Depth on the new row is parent.depth + 1;
// on a root pay attempt with no ParentID, Depth is 0.
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
	ParentID         sql.NullInt64
	Depth            int
	// ConsumerActorIDs (ZBBS-130) carries the resolved actor IDs for a
	// phase-C at-source group order. Empty for the legacy single-
	// consumer flow (the buyer is the implicit consumer); non-empty
	// when the buyer named friends in the consumers list. Pre-resolved
	// at pay-accept (display name → actor.id, with co-location check
	// against the buyer's huddle) so deliver_order doesn't have to
	// re-resolve names that may have drifted.
	ConsumerActorIDs []string
}

// insertPayLedgerPending writes a pending row via a single auto-commit
// statement and returns the assigned bigserial id. Called BEFORE the
// transfer tx so every pay attempt with resolved participants is
// captured durably. If the engine crashes between this insert and the
// transfer outcome, the aging sweep (step 4) eventually flips the
// orphaned row to withdrawn.
func (app *App) insertPayLedgerPending(ctx context.Context, p payLedgerInsert) (int64, error) {
	// pgx maps a Go []string into a Postgres uuid[] when the column type
	// is uuid[] — values are validated as UUIDs by the driver. Pass nil
	// for the single-consumer flow so the column stays NULL rather than
	// landing an empty array.
	var consumerIDs interface{}
	if len(p.ConsumerActorIDs) > 0 {
		consumerIDs = p.ConsumerActorIDs
	}
	// ZBBS-HOME-260: coin-only transfers (no item_kind) finalize at
	// 'delivered' — see file header.
	fulfillmentStatus := "ready"
	if !p.ItemKind.Valid {
		fulfillmentStatus = "delivered"
	}
	var id int64
	err := app.DB.QueryRow(ctx,
		`INSERT INTO pay_ledger (
		    huddle_id, scene_id, buyer_id, seller_id,
		    item_kind, qty, offered_amount, quoted_unit_amount,
		    consume_now, parent_id, depth, state,
		    ready_by, fulfillment_status, consumer_actor_ids
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'pending',
		           CURRENT_DATE, $12, $13)
		 RETURNING id`,
		p.HuddleID, p.SceneID, p.BuyerID, p.SellerID,
		p.ItemKind, p.Qty, p.OfferedAmount, p.QuotedUnitAmount,
		p.ConsumeNow, p.ParentID, p.Depth, fulfillmentStatus, consumerIDs,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert pay_ledger: %w", err)
	}
	return id, nil
}

// updatePayLedger flips a pending row to a terminal state and stamps
// resolved_at. Called AFTER the transfer tx commits or rolls back, or
// directly from the deliberation path when the recipient declines or
// counters without ever opening Tx B. State must be one of accepted |
// declined | countered | withdrawn | failed (the schema CHECK enforces
// this; we don't double-validate).
//
// counterAmount is populated only when state == 'countered' (the
// recipient's proposed new total in coins). For every other terminal
// state it should be a zero-Valid sql.NullInt32 so the column stays
// NULL — counter_amount on a non-countered row would be misleading
// audit data.
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
func (app *App) updatePayLedger(ctx context.Context, ledgerID int64, state, message string, counterAmount sql.NullInt32) error {
	var msg sql.NullString
	if message != "" {
		msg = sql.NullString{String: message, Valid: true}
	}
	tag, err := app.DB.Exec(ctx,
		`UPDATE pay_ledger
		    SET state = $2,
		        message = $3,
		        counter_amount = $4,
		        resolved_at = NOW()
		  WHERE id = $1
		    AND state = 'pending'`,
		ledgerID, state, msg, counterAmount,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("pay_ledger update affected %d rows for id=%d (expected 1; row missing or already terminal)", tag.RowsAffected(), ledgerID)
	}
	return nil
}
