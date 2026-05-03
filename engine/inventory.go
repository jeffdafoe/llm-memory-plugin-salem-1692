package main

// Inventory and trade — Phase 1 (ZBBS-091).
//
// Two new agent actions sitting alongside pay(): buy() transfers coin
// + items between actors atomically; consume() decrements the buyer's
// inventory and applies the item's configured satisfaction to the
// linked actor need.
//
// pay() stays unchanged. It remains the "drink at the bar" verb —
// instant gratification with no inventory step. The buy/consume pair
// is for non-tavern flow: take-home goods, supply chain, eventually
// recipes (Phase 2).
//
// Wire convention: item_kind.satisfies_amount is positive in storage
// ("amount restored when consumed"); applyConsumption takes a negative
// delta to reduce the need. Negation happens at the consume site.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5"
)

// normalizeMentions converts the raw mentions value from a speak tool
// call into a deduped, lowercased, trimmed []string. Tolerates the
// common LLM variants — []interface{} of strings, a bare string, or
// a JSON-encoded array string ("[\"cheese\",\"ale\"]"). Non-string
// elements are dropped silently. Empty input → nil. Phase C of
// sales-and-gifts.
func normalizeMentions(raw interface{}) []string {
	if raw == nil {
		return nil
	}
	var collected []string
	switch v := raw.(type) {
	case []interface{}:
		for _, x := range v {
			if s, ok := x.(string); ok {
				collected = append(collected, s)
			}
		}
	case []string:
		collected = append(collected, v...)
	case string:
		t := strings.TrimSpace(v)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			// JSON-array-as-string variant — same defensive parse the
			// serve dispatcher does for recipients.
			var parsed []string
			if err := json.Unmarshal([]byte(t), &parsed); err == nil {
				collected = parsed
			} else {
				collected = []string{t}
			}
		} else if t != "" {
			collected = []string{t}
		}
	}
	if len(collected) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(collected))
	out := make([]string, 0, len(collected))
	for _, s := range collected {
		k := strings.TrimSpace(strings.ToLower(s))
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validateMentionsAgainstInventory returns the subset of mentions that
// are NOT present in the speaker's actor_inventory (or don't exist in
// item_kind at all — both fail the same predicate). Empty result means
// every mention is valid. Used by the speak tool to reject speech
// referencing goods the speaker doesn't have, so the customer's
// pay-dropdown population is grounded in real stock.
func (app *App) validateMentionsAgainstInventory(ctx context.Context, actorID string, mentions []string) ([]string, error) {
	if len(mentions) == 0 {
		return nil, nil
	}
	rows, err := app.DB.Query(ctx, `
		SELECT m.name
		  FROM unnest($1::text[]) AS m(name)
		 WHERE NOT EXISTS (
		     SELECT 1 FROM actor_inventory ai
		      WHERE ai.actor_id = $2 AND ai.item_kind = m.name
		 )
	`, mentions, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bogus []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		bogus = append(bogus, name)
	}
	return bogus, rows.Err()
}

// inventoryLine builds the "ale x3, bread x1" comma-separated string
// surfaced in agent perception. Returns "" when the actor carries
// nothing — the caller suppresses the whole "Your inventory:" line in
// that case rather than rendering "nothing." Ordered by item_kind's
// configured sort_order so categories cluster (drinks before food
// before materials).
func (app *App) inventoryLine(ctx context.Context, actorID string) string {
	rows, err := app.DB.Query(ctx,
		`SELECT k.name, ai.quantity
		   FROM actor_inventory ai
		   JOIN item_kind k ON k.name = ai.item_kind
		  WHERE ai.actor_id = $1
		  ORDER BY k.sort_order, k.name`,
		actorID,
	)
	if err != nil {
		log.Printf("inventory: load %s: %v", actorID, err)
		return ""
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var name string
		var qty int
		if err := rows.Scan(&name, &qty); err != nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s x%d", name, qty))
	}
	return strings.Join(parts, ", ")
}

// consumeResult mirrors payResult's shape so the dispatcher in
// executeAgentCommit can build (result, errStr) pairs without
// duplicating switch logic.
type consumeResult struct {
	Result      string
	Err         string
	BuyerNewQty int               // post-consumption count in buyer's inventory
	NeedsAfter  consumptionResult // empty if item is non-consumable / consume_amount was zero
}


// executeConsume decrements the buyer's stock of `itemKind` by `qty`
// and applies the configured satisfaction to the linked need via
// applyConsumption. Items with NULL satisfies_attribute (materials)
// are rejected with a clear message — you can't eat raw wheat.
func (app *App) executeConsume(ctx context.Context, buyer *agentNPCRow, itemKind string, qty int) consumeResult {
	if qty <= 0 {
		return consumeResult{Result: "rejected", Err: "qty must be positive"}
	}
	itemKind = strings.TrimSpace(strings.ToLower(itemKind))
	if itemKind == "" {
		return consumeResult{Result: "rejected", Err: "missing item"}
	}

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return consumeResult{Result: "failed", Err: fmt.Sprintf("begin tx: %v", err)}
	}
	defer tx.Rollback(ctx)

	// Look up satisfaction first so we fail fast on materials.
	var satisfiesAttr sql.NullString
	var satisfiesAmt sql.NullInt32
	if err := tx.QueryRow(ctx,
		`SELECT satisfies_attribute, satisfies_amount
		   FROM item_kind WHERE name = $1`,
		itemKind,
	).Scan(&satisfiesAttr, &satisfiesAmt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return consumeResult{Result: "rejected", Err: fmt.Sprintf("no such item %q", itemKind)}
		}
		return consumeResult{Result: "failed", Err: fmt.Sprintf("look up item: %v", err)}
	}
	if !satisfiesAttr.Valid {
		return consumeResult{Result: "rejected", Err: fmt.Sprintf("%s isn't a consumable", itemKind)}
	}

	// Lock buyer's inventory row.
	var qtyHave int
	if err := tx.QueryRow(ctx,
		`SELECT quantity FROM actor_inventory
		  WHERE actor_id = $1 AND item_kind = $2
		  FOR UPDATE`,
		buyer.ID, itemKind,
	).Scan(&qtyHave); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return consumeResult{Result: "rejected", Err: fmt.Sprintf("you have no %s", itemKind)}
		}
		return consumeResult{Result: "failed", Err: fmt.Sprintf("lock inventory: %v", err)}
	}
	if qtyHave < qty {
		return consumeResult{Result: "rejected", Err: fmt.Sprintf("you have only %d %s (tried to consume %d)", qtyHave, itemKind, qty)}
	}

	newQty := qtyHave - qty
	if newQty == 0 {
		if _, err := tx.Exec(ctx,
			`DELETE FROM actor_inventory WHERE actor_id = $1 AND item_kind = $2`,
			buyer.ID, itemKind,
		); err != nil {
			return consumeResult{Result: "failed", Err: fmt.Sprintf("delete row: %v", err)}
		}
	} else {
		if _, err := tx.Exec(ctx,
			`UPDATE actor_inventory SET quantity = $1
			  WHERE actor_id = $2 AND item_kind = $3`,
			newQty, buyer.ID, itemKind,
		); err != nil {
			return consumeResult{Result: "failed", Err: fmt.Sprintf("decrement row: %v", err)}
		}
	}

	// Map attribute → consumptionDelta field. Switch mirrors the one
	// in object_refresh.go so adding a new attribute hits the same
	// runbook (shared/notes/codebase/salem/refresh-attributes).
	delta := consumptionDelta{}
	totalAmount := int(satisfiesAmt.Int32) * qty
	switch satisfiesAttr.String {
	case "hunger":
		delta.Hunger = -totalAmount
	case "thirst":
		delta.Thirst = -totalAmount
	case "tiredness":
		delta.Tiredness = -totalAmount
	default:
		// Unknown attribute landed in item_kind without engine support
		// — defense in depth (the runbook should be followed but isn't
		// always). Inventory still decrements; satisfaction is logged
		// and skipped rather than silently corrupting state.
		// fall through to commit without applying
	}

	var needsAfter consumptionResult
	if delta.Hunger != 0 || delta.Thirst != 0 || delta.Tiredness != 0 {
		needsAfter, err = app.applyConsumption(ctx, tx, buyer.ID, delta, "consume")
		if err != nil {
			return consumeResult{Result: "failed", Err: fmt.Sprintf("apply consumption: %v", err)}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return consumeResult{Result: "failed", Err: fmt.Sprintf("commit: %v", err)}
	}

	app.Hub.Broadcast(WorldEvent{
		Type: "actor_inventory_changed",
		Data: map[string]any{
			"actor_id":  buyer.ID,
			"item_kind": itemKind,
			"quantity":  newQty,
		},
	})
	if delta.Hunger != 0 || delta.Thirst != 0 || delta.Tiredness != 0 {
		app.Hub.Broadcast(WorldEvent{
			Type: "npc_needs_changed",
			Data: map[string]any{
				"id":        buyer.ID,
				"hunger":    needsAfter.Hunger,
				"thirst":    needsAfter.Thirst,
				"tiredness": needsAfter.Tiredness,
			},
		})
	}

	return consumeResult{
		Result:      "ok",
		BuyerNewQty: newQty,
		NeedsAfter:  needsAfter,
	}
}
