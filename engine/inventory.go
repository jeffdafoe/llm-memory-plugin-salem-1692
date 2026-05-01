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
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5"
)

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

// buyResult / consumeResult mirror payResult's shape so the dispatcher
// in executeAgentCommit can build (result, errStr) pairs without
// duplicating switch logic.
type buyResult struct {
	Result        string // "ok" | "rejected" | "failed"
	Err           string // human-readable, empty when Result == "ok"
	BuyerNewCoins int
	BuyerNewQty   int // post-transfer count of the item in buyer's inventory
	SellerNewQty  int // post-transfer count in seller's inventory
}

type consumeResult struct {
	Result      string
	Err         string
	BuyerNewQty int               // post-consumption count in buyer's inventory
	NeedsAfter  consumptionResult // empty if item is non-consumable / consume_amount was zero
}

// executeBuy moves `qty` of `itemKind` from the seller to the buyer,
// transferring `amount` coins (the negotiated total) the opposite
// direction. Atomic — either everything lands or nothing does.
//
// `amount` is the total agreed price typed by the buyer after dialogue
// negotiation, same convention as pay()'s amount. There is no static
// item price (ZBBS-092 dropped item_kind.price); supply-constrained
// pricing emerges from conversation.
//
// Failure modes mirror pay.go: missing recipient, insufficient supply,
// insufficient coins, self-trade, unknown item, non-positive amount —
// all return "rejected". Lock-and-failures return "failed". Buyer's
// coins are never partially deducted.
func (app *App) executeBuy(ctx context.Context, buyer *agentNPCRow, sellerName, itemKind string, qty, amount int) buyResult {
	if qty <= 0 {
		return buyResult{Result: "rejected", Err: "qty must be positive"}
	}
	if amount < 0 {
		return buyResult{Result: "rejected", Err: "amount cannot be negative"}
	}
	sellerName = strings.TrimSpace(sellerName)
	if sellerName == "" {
		return buyResult{Result: "rejected", Err: "missing seller"}
	}
	itemKind = strings.TrimSpace(strings.ToLower(itemKind))
	if itemKind == "" {
		return buyResult{Result: "rejected", Err: "missing item"}
	}

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return buyResult{Result: "failed", Err: fmt.Sprintf("begin tx: %v", err)}
	}
	defer tx.Rollback(ctx)

	// Validate item exists. Doesn't gate on availability — that's
	// checked when we look at the seller's inventory below.
	var itemExists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM item_kind WHERE name = $1)`,
		itemKind,
	).Scan(&itemExists); err != nil {
		return buyResult{Result: "failed", Err: fmt.Sprintf("look up item: %v", err)}
	}
	if !itemExists {
		return buyResult{Result: "rejected", Err: fmt.Sprintf("no such item %q", itemKind)}
	}
	totalCost := amount

	// Lock buyer's coin row.
	var buyerCoins int
	if err := tx.QueryRow(ctx,
		`SELECT coins FROM actor WHERE id = $1 FOR UPDATE`,
		buyer.ID,
	).Scan(&buyerCoins); err != nil {
		return buyResult{Result: "failed", Err: fmt.Sprintf("lock buyer: %v", err)}
	}
	if buyerCoins < totalCost {
		return buyResult{Result: "rejected", Err: fmt.Sprintf("insufficient coins (have %d, need %d)", buyerCoins, totalCost)}
	}

	// Resolve + lock seller. Same case-insensitive display_name match
	// pay() uses, since the LLM addresses villagers by their visible
	// name.
	var sellerID string
	if err := tx.QueryRow(ctx,
		`SELECT id FROM actor
		 WHERE LOWER(display_name) = LOWER($1)
		 LIMIT 1
		 FOR UPDATE`,
		sellerName,
	).Scan(&sellerID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return buyResult{Result: "rejected", Err: fmt.Sprintf("no villager named %q", sellerName)}
		}
		return buyResult{Result: "failed", Err: fmt.Sprintf("lock seller: %v", err)}
	}
	if sellerID == buyer.ID {
		return buyResult{Result: "rejected", Err: "cannot buy from yourself"}
	}

	// Lock seller's inventory row. NoRows = seller doesn't stock this
	// item at all, which is rejected cleanly rather than waiting on
	// the FK to surface a less helpful error.
	var sellerQty int
	if err := tx.QueryRow(ctx,
		`SELECT quantity FROM actor_inventory
		  WHERE actor_id = $1 AND item_kind = $2
		  FOR UPDATE`,
		sellerID, itemKind,
	).Scan(&sellerQty); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return buyResult{Result: "rejected", Err: fmt.Sprintf("%s has no %s to sell", sellerName, itemKind)}
		}
		return buyResult{Result: "failed", Err: fmt.Sprintf("lock seller inventory: %v", err)}
	}
	if sellerQty < qty {
		return buyResult{Result: "rejected", Err: fmt.Sprintf("%s has only %d %s (asked for %d)", sellerName, sellerQty, itemKind, qty)}
	}

	// Decrement seller's inventory; DELETE if it would hit zero so a
	// "x0" row doesn't show up in perception.
	newSellerQty := sellerQty - qty
	if newSellerQty == 0 {
		if _, err := tx.Exec(ctx,
			`DELETE FROM actor_inventory WHERE actor_id = $1 AND item_kind = $2`,
			sellerID, itemKind,
		); err != nil {
			return buyResult{Result: "failed", Err: fmt.Sprintf("delete seller stock: %v", err)}
		}
	} else {
		if _, err := tx.Exec(ctx,
			`UPDATE actor_inventory SET quantity = $1
			  WHERE actor_id = $2 AND item_kind = $3`,
			newSellerQty, sellerID, itemKind,
		); err != nil {
			return buyResult{Result: "failed", Err: fmt.Sprintf("decrement seller stock: %v", err)}
		}
	}

	// Increment buyer's inventory. ON CONFLICT for upsert — buyer may
	// or may not already hold the item.
	var buyerNewQty int
	if err := tx.QueryRow(ctx,
		`INSERT INTO actor_inventory (actor_id, item_kind, quantity)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (actor_id, item_kind)
		 DO UPDATE SET quantity = actor_inventory.quantity + EXCLUDED.quantity
		 RETURNING quantity`,
		buyer.ID, itemKind, qty,
	).Scan(&buyerNewQty); err != nil {
		return buyResult{Result: "failed", Err: fmt.Sprintf("credit buyer stock: %v", err)}
	}

	// Coin transfer.
	if _, err := tx.Exec(ctx,
		`UPDATE actor SET coins = coins - $1 WHERE id = $2`,
		totalCost, buyer.ID,
	); err != nil {
		return buyResult{Result: "failed", Err: fmt.Sprintf("debit buyer coins: %v", err)}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE actor SET coins = coins + $1 WHERE id = $2`,
		totalCost, sellerID,
	); err != nil {
		return buyResult{Result: "failed", Err: fmt.Sprintf("credit seller coins: %v", err)}
	}

	if err := tx.Commit(ctx); err != nil {
		return buyResult{Result: "failed", Err: fmt.Sprintf("commit: %v", err)}
	}

	// Hub broadcasts so the editor / admin dashboard mirror the new
	// state without a poll. Inventory and coin events fire as separate
	// messages so listeners can subscribe to whichever they care about.
	app.Hub.Broadcast(WorldEvent{
		Type: "actor_inventory_changed",
		Data: map[string]any{
			"actor_id":  buyer.ID,
			"item_kind": itemKind,
			"quantity":  buyerNewQty,
		},
	})
	app.Hub.Broadcast(WorldEvent{
		Type: "actor_inventory_changed",
		Data: map[string]any{
			"actor_id":  sellerID,
			"item_kind": itemKind,
			"quantity":  newSellerQty,
		},
	})

	return buyResult{
		Result:        "ok",
		BuyerNewCoins: buyerCoins - totalCost,
		BuyerNewQty:   buyerNewQty,
		SellerNewQty:  newSellerQty,
	}
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
