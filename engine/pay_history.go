package main

// pay_history — read helpers over pay_ledger for surfacing past
// transactions in perception.
//
// Used by the recovery-options perception block (ZBBS-172): when an
// NPC weighs a paid recovery option (inn night, future hostel donation,
// etc.) they see "you paid X coins last time, three days ago" only if
// they've personally bought the same item from the same vendor before.
// Without history they see "you don't know what they charge" — knowledge
// of price is earned, not free, and a fresh-arrival NPC has to either
// commit to spending an unknown amount or ask.
//
// Generalizes beyond lodging — any future paid satisfaction (donation,
// service item, etc.) routes through pay_ledger and gets the same
// recall semantics for free.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// lastPaidPrice returns the buyer's most recent accepted price from
// the seller for the given item kind.
//
// Returns ok=false (no error) when there's no prior accepted purchase
// — the caller should render a "you don't know what they charge"
// fallback. Returns an error only on actual DB failures.
//
// Index `ix_pay_ledger_buyer_seller` on (buyer_id, seller_id, item_kind,
// created_at DESC) covers the lookup; one row per call.
func (app *App) lastPaidPrice(ctx context.Context, buyerID, sellerID, itemKind string) (amount int, paidAt time.Time, ok bool, err error) {
	row := app.DB.QueryRow(ctx,
		`SELECT offered_amount, created_at
		   FROM pay_ledger
		  WHERE buyer_id  = $1
		    AND seller_id = $2
		    AND item_kind = $3
		    AND state     = 'accepted'
		  ORDER BY created_at DESC
		  LIMIT 1`,
		buyerID, sellerID, itemKind)
	if scanErr := row.Scan(&amount, &paidAt); scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return 0, time.Time{}, false, nil
		}
		return 0, time.Time{}, false, scanErr
	}
	return amount, paidAt, true, nil
}
