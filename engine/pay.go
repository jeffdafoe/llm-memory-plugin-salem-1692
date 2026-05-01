package main

// Pay tool — coin transfer between villagers, with optional goods
// transfer and immediate-consumption side-effects (ZBBS-093 collapsed
// the separate buy() tool into this).
//
// Three flows controlled by the optional `item` parameter:
//
//   pay(target, amount)
//       Generic coin transfer — tip, service, news, gift. No goods,
//       no need-drop. Optional `for` is flavor text on the audit row.
//
//   pay(target, amount, item, qty, consume_now=true)
//       At-source consumption — the tavern verb. Decrements the
//       seller's stock by qty, applies the item's satisfies_amount to
//       the buyer's matching need (hunger/thirst). No inventory
//       transfer to the buyer. Works for portable AND non-portable
//       items (you can drink ale at the bar OR eat stew there).
//
//   pay(target, amount, item, qty, consume_now=false)
//       Take-home — the merchant flow. Validates the item is
//       portable, then moves qty units from seller to buyer's
//       inventory. No consumption (the buyer can `consume()` later).
//       Non-portable items reject with a clean error.
//
// `amount` is the negotiated coin total typed by the buyer after
// dialogue. There's no static price column (ZBBS-092). Supply
// pressure produces price pressure through conversation.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// payResult captures the outcome of an attempted pay so the dispatcher can
// build the audit-row (result, errStr) pair without duplicating switch logic.
type payResult struct {
	Result        string // "ok" | "rejected" | "failed"
	Err           string // human-readable, empty when Result == "ok"
	BuyerNewCoins int    // post-transfer coin balance for log/broadcast
	// Item-flow fields. All zero/empty when no item was involved.
	ItemTransferred  bool   // true when item moved into buyer's inventory
	ItemConsumed     bool   // true when consume_now applied satisfaction
	HungerReduction  int    // amount applied (0 if not relevant)
	ThirstReduction  int    // amount applied (0 if not relevant)
	TirednessReduce  int    // amount applied (0 if not relevant)
}

// payRequest groups the pay arguments so executePay's signature stays
// readable as new optional parameters land.
type payRequest struct {
	RecipientName string
	Amount        int
	ForText       string // optional flavor text for audit
	Item          string // optional item kind name
	Qty           int    // defaults to 1 when Item is set
	ConsumeNow    bool   // tavern (true) vs take-home (false)
}

// executePay carries out the transfer and any goods/consumption side-
// effects. Returns a payResult describing what happened. Never partial:
// if any leg fails, the transaction rolls back and the buyer keeps
// their coins.
func (app *App) executePay(ctx context.Context, buyer *agentNPCRow, req payRequest) payResult {
	if req.Amount < 0 {
		return payResult{Result: "rejected", Err: "amount cannot be negative"}
	}
	recipientName := strings.TrimSpace(req.RecipientName)
	if recipientName == "" {
		return payResult{Result: "rejected", Err: "missing recipient"}
	}
	itemKind := strings.TrimSpace(strings.ToLower(req.Item))
	qty := req.Qty
	if itemKind != "" && qty <= 0 {
		qty = 1
	}

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return payResult{Result: "failed", Err: fmt.Sprintf("begin tx: %v", err)}
	}
	defer tx.Rollback(ctx)

	// Lock the buyer row so a concurrent pay from the same NPC can't
	// race us into a negative balance.
	var buyerCoins int
	if err := tx.QueryRow(ctx,
		`SELECT coins FROM actor WHERE id = $1 FOR UPDATE`,
		buyer.ID,
	).Scan(&buyerCoins); err != nil {
		return payResult{Result: "failed", Err: fmt.Sprintf("lock buyer: %v", err)}
	}
	if buyerCoins < req.Amount {
		return payResult{Result: "rejected", Err: fmt.Sprintf("insufficient coins (have %d, need %d)", buyerCoins, req.Amount)}
	}

	// Recipient lookup-and-lock. After ZBBS-084 the unified actor table
	// holds every villager.
	var recipientID string
	err = tx.QueryRow(ctx,
		`SELECT id FROM actor
		 WHERE LOWER(display_name) = LOWER($1)
		 LIMIT 1
		 FOR UPDATE`,
		recipientName).Scan(&recipientID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return payResult{Result: "rejected", Err: fmt.Sprintf("no villager named %q", recipientName)}
		}
		return payResult{Result: "failed", Err: fmt.Sprintf("lock recipient: %v", err)}
	}
	if recipientID == buyer.ID {
		return payResult{Result: "rejected", Err: "cannot pay yourself"}
	}

	// Validate item if provided. Pull capabilities + satisfies pair so
	// we can decide whether the at-source vs take-home flow is allowed.
	var (
		itemSatisfiesAttr sql.NullString
		itemSatisfiesAmt  sql.NullInt32
		itemCapabilities  []string
	)
	if itemKind != "" {
		err := tx.QueryRow(ctx,
			`SELECT satisfies_attribute, satisfies_amount, capabilities
			   FROM item_kind WHERE name = $1`,
			itemKind,
		).Scan(&itemSatisfiesAttr, &itemSatisfiesAmt, &itemCapabilities)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return payResult{Result: "rejected", Err: fmt.Sprintf("no such item %q", itemKind)}
			}
			return payResult{Result: "failed", Err: fmt.Sprintf("look up item: %v", err)}
		}

		// Take-home flow needs the item to be portable. Non-portables
		// (stew, water at present) get rejected with a clean error so
		// the LLM can either retry with consume_now=true or drop the
		// take-home framing.
		if !req.ConsumeNow && !hasCapability(itemCapabilities, "portable") {
			return payResult{Result: "rejected", Err: fmt.Sprintf("%s cannot be carried; consume at source with consume_now=true", itemKind)}
		}

		// At-source flow needs the item to be a consumable. Materials
		// (wheat, flour, iron) have NULL satisfies_attribute; you can't
		// "consume" raw flour at the merchant's stand. The buyer needs
		// to take it home (consume_now=false) and use it later.
		if req.ConsumeNow && !itemSatisfiesAttr.Valid {
			return payResult{Result: "rejected", Err: fmt.Sprintf("%s isn't consumable; set consume_now=false to take it home", itemKind)}
		}

		// Lock the seller's inventory row. NoRows = seller doesn't
		// stock this item; cleaner error than the FK noise.
		var sellerQty int
		if err := tx.QueryRow(ctx,
			`SELECT quantity FROM actor_inventory
			  WHERE actor_id = $1 AND item_kind = $2
			  FOR UPDATE`,
			recipientID, itemKind,
		).Scan(&sellerQty); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return payResult{Result: "rejected", Err: fmt.Sprintf("%s has no %s to sell", recipientName, itemKind)}
			}
			return payResult{Result: "failed", Err: fmt.Sprintf("lock seller inventory: %v", err)}
		}
		if sellerQty < qty {
			return payResult{Result: "rejected", Err: fmt.Sprintf("%s has only %d %s (asked for %d)", recipientName, sellerQty, itemKind, qty)}
		}

		newSellerQty := sellerQty - qty
		if newSellerQty == 0 {
			if _, err := tx.Exec(ctx,
				`DELETE FROM actor_inventory WHERE actor_id = $1 AND item_kind = $2`,
				recipientID, itemKind,
			); err != nil {
				return payResult{Result: "failed", Err: fmt.Sprintf("delete seller stock: %v", err)}
			}
		} else {
			if _, err := tx.Exec(ctx,
				`UPDATE actor_inventory SET quantity = $1
				  WHERE actor_id = $2 AND item_kind = $3`,
				newSellerQty, recipientID, itemKind,
			); err != nil {
				return payResult{Result: "failed", Err: fmt.Sprintf("decrement seller stock: %v", err)}
			}
		}
	}

	// Coin transfer. Two UPDATEs serialized by the FOR UPDATE locks
	// above. Skipped when amount is zero (a "free" gift / sample).
	if req.Amount > 0 {
		if _, err := tx.Exec(ctx, `UPDATE actor SET coins = coins - $1 WHERE id = $2`, req.Amount, buyer.ID); err != nil {
			return payResult{Result: "failed", Err: fmt.Sprintf("debit buyer: %v", err)}
		}
		if _, err := tx.Exec(ctx, `UPDATE actor SET coins = coins + $1 WHERE id = $2`, req.Amount, recipientID); err != nil {
			return payResult{Result: "failed", Err: fmt.Sprintf("credit recipient: %v", err)}
		}
	}

	// Item flow: take-home → credit buyer's inventory. At-source →
	// apply satisfaction.
	var (
		itemTransferred bool
		itemConsumed    bool
		consumeResult   consumptionResult
	)
	if itemKind != "" {
		if req.ConsumeNow {
			// At-source consumption. Apply satisfies_amount * qty to
			// the right need column via applyConsumption. Mirrors the
			// switch in inventory.go::executeConsume; both routes land
			// in the same chronicler nudge path.
			delta := consumptionDelta{}
			totalAmount := int(itemSatisfiesAmt.Int32) * qty
			switch itemSatisfiesAttr.String {
			case "hunger":
				delta.Hunger = -totalAmount
			case "thirst":
				delta.Thirst = -totalAmount
			case "tiredness":
				delta.Tiredness = -totalAmount
			}
			if delta.Hunger != 0 || delta.Thirst != 0 || delta.Tiredness != 0 {
				res, err := app.applyConsumption(ctx, tx, buyer.ID, delta, "pay-consume")
				if err != nil {
					return payResult{Result: "failed", Err: fmt.Sprintf("apply consumption: %v", err)}
				}
				consumeResult = res
				itemConsumed = true
			}
		} else {
			// Take-home — credit buyer's inventory.
			if _, err := tx.Exec(ctx,
				`INSERT INTO actor_inventory (actor_id, item_kind, quantity)
				 VALUES ($1, $2, $3)
				 ON CONFLICT (actor_id, item_kind)
				 DO UPDATE SET quantity = actor_inventory.quantity + EXCLUDED.quantity`,
				buyer.ID, itemKind, qty,
			); err != nil {
				return payResult{Result: "failed", Err: fmt.Sprintf("credit buyer stock: %v", err)}
			}
			itemTransferred = true
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return payResult{Result: "failed", Err: fmt.Sprintf("commit tx: %v", err)}
	}

	// Hub broadcast. Single npc_paid event covers both coin and item
	// flows; downstream consumers branch on item != "" if they care.
	// Need-after values come from the post-update consumeResult so
	// listeners don't have to recompute deltas.
	app.Hub.Broadcast(WorldEvent{
		Type: "npc_paid",
		Data: map[string]interface{}{
			"buyer":        buyer.DisplayName,
			"buyer_id":     buyer.ID,
			"recipient":    recipientName,
			"recipient_id": recipientID,
			"amount":       req.Amount,
			"for":          req.ForText,
			"item":         itemKind,
			"qty":          qty,
			"consume_now":  req.ConsumeNow,
			"at":           time.Now().UTC().Format(time.RFC3339),
		},
	})

	// Inventory broadcasts so the editor mirrors fresh state.
	if itemKind != "" {
		app.Hub.Broadcast(WorldEvent{
			Type: "actor_inventory_changed",
			Data: map[string]any{
				"actor_id":  recipientID,
				"item_kind": itemKind,
			},
		})
		if itemTransferred {
			app.Hub.Broadcast(WorldEvent{
				Type: "actor_inventory_changed",
				Data: map[string]any{
					"actor_id":  buyer.ID,
					"item_kind": itemKind,
				},
			})
		}
	}

	if itemConsumed {
		app.Hub.Broadcast(WorldEvent{
			Type: "npc_needs_changed",
			Data: map[string]interface{}{
				"id":        buyer.ID,
				"hunger":    consumeResult.Hunger,
				"thirst":    consumeResult.Thirst,
				"tiredness": consumeResult.Tiredness,
			},
		})
	}

	result := payResult{
		Result:          "ok",
		BuyerNewCoins:   buyerCoins - req.Amount,
		ItemTransferred: itemTransferred,
		ItemConsumed:    itemConsumed,
	}
	if itemConsumed {
		// Reductions are old - new for the buyer. agentNPCRow's pre-tx
		// values minus the post-applyConsumption values give the
		// effective drop, clamped at zero (defensive — applyConsumption
		// shouldn't ever return a higher value than it started, but be
		// safe).
		result.HungerReduction = positiveDelta(buyer.Hunger, consumeResult.Hunger)
		result.ThirstReduction = positiveDelta(buyer.Thirst, consumeResult.Thirst)
		result.TirednessReduce = positiveDelta(buyer.Tiredness, consumeResult.Tiredness)
	}
	return result
}

// hasCapability reports whether the given capability tag is in the
// item's capabilities array.
func hasCapability(capabilities []string, want string) bool {
	for _, c := range capabilities {
		if c == want {
			return true
		}
	}
	return false
}

// positiveDelta returns max(0, oldV - newV). Used to surface the
// effective need-drop in payResult.
func positiveDelta(oldV, newV int) int {
	if newV >= oldV {
		return 0
	}
	return oldV - newV
}
