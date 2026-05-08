package main

// item_satisfies — one-to-many item-effect mapping (ZBBS-125).
//
// Replaces the legacy satisfies_attribute / satisfies_amount columns
// on item_kind with a relation that lets a single item ease multiple
// needs at different magnitudes (e.g. ale sates a little hunger
// alongside its thirst). This file holds the read helpers; the writes
// are handled by the migration plus future seeding paths.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// itemSatisfaction describes one effect an item has on a need —
// "consume this item, this attribute drops by this amount per unit."
//
// Dwell fields (ZBBS-172) are optional and ride alongside the
// immediate Amount: when all three are non-zero, consuming this item
// at a structure pins a per-tick recovery to that structure's loiter
// pin. The actor receives DwellAmount / DwellPeriodMinutes for
// DwellTotalTicks ticks while remaining at the structure. Walking
// away ends the meal early — the dwell tick deletes the credit row
// when the actor's loiter resolution no longer matches.
type itemSatisfaction struct {
	Attribute          string
	Amount             int
	DwellAmount        int // 0 = no dwell; positive = magnitude per tick (engine negates on apply)
	DwellPeriodMinutes int // 0 = no dwell
	DwellTotalTicks    int // 0 = no dwell; countdown stamped onto actor_dwell_credit
}

// pgQuerier abstracts pgxpool.Pool / pgx.Tx so the lookup helpers can
// be called from in-tx callsites (pay, consume) and from out-of-tx
// callsites (perception/satiation) using one signature.
type pgQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// loadItemSatisfactions returns every (attribute, amount) row recorded
// for the given item, sorted by amount DESC then attribute ASC so the
// most magnitudinal effect appears first — narration callers that
// need a single "primary" attribute can take the head of the slice.
//
// Returns an empty slice (no error) when the item exists but has no
// satisfactions (materials like wheat / iron). Returns an empty slice
// for unknown items too — callers that need an "exists" check should
// use a separate item_kind lookup. Errors only on actual DB failures.
func loadItemSatisfactions(ctx context.Context, q pgQuerier, itemKind string) ([]itemSatisfaction, error) {
	rows, err := q.Query(ctx, `
		SELECT attribute, amount,
		       COALESCE(dwell_amount, 0),
		       COALESCE(dwell_period_minutes, 0),
		       COALESCE(dwell_total_ticks, 0)
		  FROM item_satisfies
		 WHERE item_kind = $1
		 ORDER BY amount DESC, attribute ASC
	`, itemKind)
	if err != nil {
		return nil, fmt.Errorf("query item_satisfies for %q: %w", itemKind, err)
	}
	defer rows.Close()
	var out []itemSatisfaction
	for rows.Next() {
		var s itemSatisfaction
		if err := rows.Scan(&s.Attribute, &s.Amount,
			&s.DwellAmount, &s.DwellPeriodMinutes, &s.DwellTotalTicks); err != nil {
			return nil, fmt.Errorf("scan item_satisfies for %q: %w", itemKind, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter item_satisfies for %q: %w", itemKind, err)
	}
	return out, nil
}

// applySatisfactionsToDelta accumulates a slice of itemSatisfaction
// entries into a consumptionDelta, multiplying each by qty. Unknown
// attribute names are silently skipped so an attribute landed in
// item_satisfies without engine support doesn't corrupt the consume
// dispatch — the slice still gets dispatched as far as the engine
// understands it (defense in depth that mirrors the old switch in
// inventory.go). Returns the modified delta to make call-site
// chaining easier.
func applySatisfactionsToDelta(delta consumptionDelta, satisfactions []itemSatisfaction, qty int) consumptionDelta {
	for _, s := range satisfactions {
		amt := s.Amount * qty
		switch s.Attribute {
		case "hunger":
			delta.Hunger -= amt
		case "thirst":
			delta.Thirst -= amt
		case "tiredness":
			delta.Tiredness -= amt
			// Unknown attribute: skip. Defense in depth — if a future
			// migration adds a new attribute the engine doesn't yet
			// handle, the consume still decrements inventory and the
			// known attributes still drop, instead of silently corrupting
			// state.
		}
	}
	return delta
}

// primarySatisfactionAttribute returns the attribute name with the
// largest amount, or "" when the item has no satisfactions. Used by
// narration paths that anchor on a single "main" effect — "drinks
// ale" reads from this even though ale also satisfies hunger. When
// two attributes tie on amount, the alphabetically-first one wins
// (matches the ORDER BY in loadItemSatisfactions).
func primarySatisfactionAttribute(satisfactions []itemSatisfaction) string {
	if len(satisfactions) == 0 {
		return ""
	}
	return satisfactions[0].Attribute
}
