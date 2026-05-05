package main

// scene_quote — structural price tracking (ZBBS-124).
//
// When an NPC's speak tool emits an optional `price` integer alongside
// `mentions`, the engine inserts a scene_quote row per mentioned item
// keyed to the speaker's current huddle. The pay handler later reads
// these rows to reject buyer offers that underpay the seller's stated
// price.
//
// Storage and lifecycle live in the migration ZBBS-124-scene-quote_up.
// This file holds the read/write helpers the speak and pay paths call.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// normalizeQuotePrice coerces tc.Input["price"] into a non-negative int.
// Models tend to return integers as float64 through JSON; accept both
// shapes plus json.Number-via-string. Anything else (negative, non-
// numeric, NaN, missing) returns ok=false so the caller skips quoting
// without rejecting the speak.
func normalizeQuotePrice(raw interface{}) (int, bool) {
	switch v := raw.(type) {
	case nil:
		return 0, false
	case float64:
		// Reject NaN/inf and fractional values; the tool description
		// says whole-number coins.
		if v != v || v < 0 || v != float64(int(v)) {
			return 0, false
		}
		return int(v), true
	case int:
		if v < 0 {
			return 0, false
		}
		return v, true
	case int64:
		if v < 0 {
			return 0, false
		}
		return int(v), true
	default:
		return 0, false
	}
}

// upsertSceneQuotes records the same per-unit price for every mentioned
// item in the speaker's current huddle. No-op when the speaker isn't
// in a huddle (e.g. mid-walk speak with no scene to anchor the quote).
// Called from the speak commit handler — fire-and-forget at the call
// site, errors are logged not propagated since the spoken text already
// landed and the absence of a structured quote just means the pay
// handler falls through to the legacy accept-all behavior for that
// item.
func (app *App) upsertSceneQuotes(ctx context.Context, speakerID string, items []string, unitPrice int) error {
	if len(items) == 0 {
		return nil
	}
	// One round-trip per item keeps the SQL straightforward and matches
	// the upsert idiom used elsewhere in the engine. Mentions arrays
	// rarely exceed two or three entries in practice.
	for _, item := range items {
		_, err := app.DB.Exec(ctx, `
			INSERT INTO scene_quote (huddle_id, from_actor_id, item_kind, unit_price)
			SELECT a.current_huddle_id, a.id, $2, $3
			  FROM actor a
			 WHERE a.id = $1
			   AND a.current_huddle_id IS NOT NULL
			ON CONFLICT (huddle_id, from_actor_id, item_kind)
			DO UPDATE SET
			    unit_price = EXCLUDED.unit_price,
			    quoted_at  = NOW()
		`, speakerID, item, unitPrice)
		if err != nil {
			return fmt.Errorf("upsert scene_quote (item %q): %w", item, err)
		}
	}
	return nil
}

// lookupSceneQuote fetches the current per-unit quote a recipient is
// holding for a given item in a given huddle. Returns (price, true)
// when a quote is on file, (0, false) when no quote exists. Used by
// the pay handler to enforce buyer offers honor the seller's stated
// price.
//
// Reads outside the pay transaction's FOR UPDATE locks since the quote
// table isn't updated during a pay (only via speak), and a
// concurrent speak racing a pay is fine — the worst case is a fresh
// quote that the pay didn't see, which means the engine accepts the
// transfer per the older quote. That's the same semantics as a
// real-life conversation where a price is updated mid-handshake.
func (app *App) lookupSceneQuote(ctx context.Context, huddleID, recipientID, itemKind string) (int, bool, error) {
	var price int
	err := app.DB.QueryRow(ctx, `
		SELECT unit_price
		  FROM scene_quote
		 WHERE huddle_id = $1
		   AND from_actor_id = $2
		   AND item_kind = $3
	`, huddleID, recipientID, itemKind).Scan(&price)
	if err != nil {
		// pgx.ErrNoRows surfaces as the no-quote-on-file case — callers
		// treat (0, false, nil) as "no quote, fall through to legacy
		// accept-all behavior."
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return price, true, nil
}
