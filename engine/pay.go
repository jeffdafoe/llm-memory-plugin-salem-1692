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
	"log"
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
//
// ConsumerNames (Phase C of sales-and-gifts): optional list of display
// names for at-source group orders (consume_now=true). When non-empty,
// the seller's stock is decremented by Qty*len(consumers), each named
// consumer's matching need is dropped, and the buyer pays the full
// negotiated Amount. Default empty → the buyer is the implicit single
// consumer (legacy at-source behavior). Take-home (consume_now=false)
// ignores ConsumerNames — the items go to the buyer's inventory and
// can be redistributed via give() in a future phase.
type payRequest struct {
	RecipientName string
	Amount        int
	ForText       string   // optional flavor text for audit
	Item          string   // optional item kind name
	Qty           int      // per consumer; defaults to 1 when Item is set
	ConsumeNow    bool     // tavern (true) vs take-home (false)
	ConsumerNames []string // at-source group order; empty → buyer
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

	// Phase C: ConsumerNames is at-source only. Reject early if the
	// model paired it with take-home — the semantics ("buy take-home for
	// these other people") aren't supported and would silently fall
	// through to a buyer-credit if we let it pass.
	if len(req.ConsumerNames) > 0 && (!req.ConsumeNow || itemKind == "") {
		return payResult{
			Result: "rejected",
			Err:    "consumers is only valid for at-source group orders (consume_now=true with an item). For take-home, omit consumers — the goods go to your inventory.",
		}
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

	// Validate item if provided. Pull capabilities from item_kind, and
	// the multi-attribute satisfactions from item_satisfies (ZBBS-125).
	// Materials (wheat, flour, iron) have an item_kind row but no
	// item_satisfies rows — same "not a consumable" rejection as before.
	var (
		itemSatisfactions []itemSatisfaction
		itemCapabilities  []string
	)
	if itemKind != "" {
		err := tx.QueryRow(ctx,
			`SELECT capabilities FROM item_kind WHERE name = $1`,
			itemKind,
		).Scan(&itemCapabilities)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return payResult{Result: "rejected", Err: fmt.Sprintf("no such item %q", itemKind)}
			}
			return payResult{Result: "failed", Err: fmt.Sprintf("look up item: %v", err)}
		}
		itemSatisfactions, err = loadItemSatisfactions(ctx, tx, itemKind)
		if err != nil {
			return payResult{Result: "failed", Err: fmt.Sprintf("load satisfactions: %v", err)}
		}

		// Take-home flow needs the item to be portable. Non-portables
		// (stew, water at present) get rejected with a clean error so
		// the LLM can either retry with consume_now=true or drop the
		// take-home framing.
		if !req.ConsumeNow && !hasCapability(itemCapabilities, "portable") {
			return payResult{Result: "rejected", Err: fmt.Sprintf("%s cannot be carried; consume at source with consume_now=true", itemKind)}
		}

		// At-source flow needs the item to be a consumable. Materials
		// have an item_kind row but no item_satisfies rows; you can't
		// "consume" raw flour at the merchant's stand. The buyer needs
		// to take it home (consume_now=false) and use it later.
		if req.ConsumeNow && len(itemSatisfactions) == 0 {
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
		// Phase C: at-source group orders multiply the unit qty by the
		// number of consumers (default 1 = buyer-only). Take-home stays
		// at qty; the buyer's inventory is credited the requested
		// amount regardless of who else might eat it later.
		consumerCount := 1
		if req.ConsumeNow && len(req.ConsumerNames) > 0 {
			consumerCount = len(req.ConsumerNames)
		}
		totalQty := qty * consumerCount
		if sellerQty < totalQty {
			return payResult{Result: "rejected", Err: fmt.Sprintf("%s has only %d %s (asked for %d)", recipientName, sellerQty, itemKind, totalQty)}
		}

		// Quote enforcement (ZBBS-124). When the seller has stated a
		// per-unit price for this item in the buyer's current huddle
		// via speak's optional `price` field, reject offers that fall
		// short of that quote. No quote on file = silent accept (today's
		// behavior); the LLM-tick pass in a later phase covers the
		// no-quote case. Read outside the FOR UPDATE locks since
		// scene_quote rows are written by speak commits, not by pay —
		// no contention with this transaction.
		var buyerHuddle sql.NullString
		if err := tx.QueryRow(ctx,
			`SELECT current_huddle_id FROM actor WHERE id = $1`,
			buyer.ID,
		).Scan(&buyerHuddle); err != nil {
			return payResult{Result: "failed", Err: fmt.Sprintf("lookup buyer huddle: %v", err)}
		}
		if buyerHuddle.Valid {
			quoted, ok, err := app.lookupSceneQuote(ctx, buyerHuddle.String, recipientID, itemKind)
			if err != nil {
				// Treat a quote lookup failure as "no quote" rather
				// than failing the pay outright — the structural
				// guard is opportunistic, not authoritative.
				log.Printf("scene_quote lookup (huddle=%s recipient=%s item=%s): %v", buyerHuddle.String, recipientID, itemKind, err)
			} else if ok {
				required := quoted * totalQty
				if req.Amount < required {
					return payResult{
						Result: "rejected",
						Err:    fmt.Sprintf("%s quoted %d coin(s) per %s; for %d unit(s) the price is %d, but you offered %d. Speak to renegotiate, or pay the asked amount.", recipientName, quoted, itemKind, totalQty, required, req.Amount),
					}
				}
			}
		}

		newSellerQty := sellerQty - totalQty
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
	// apply satisfaction. For at-source group orders (Phase C), each
	// named consumer eats/drinks qty units; the buyer's coins cover
	// all of it. Default consumers = [buyer] preserves the legacy
	// single-consumer flow.
	var (
		itemTransferred  bool
		itemConsumed     bool
		consumeResult    consumptionResult // post-consume buyer state (legacy field)
		consumerUpdates  []payConsumerUpdate
	)
	if itemKind != "" {
		if req.ConsumeNow {
			// Resolve the consumer set. Default = buyer. With explicit
			// consumers, look each up by display name and verify they
			// share the buyer's huddle (you can't drink an ale for
			// someone in another room).
			consumers, rejectErr := app.resolveAtSourceConsumers(ctx, tx, buyer, req.ConsumerNames)
			if rejectErr != "" {
				return payResult{Result: "rejected", Err: rejectErr}
			}

			// At-source consumption. Apply every (attribute, amount)
			// from item_satisfies (ZBBS-125) — multi-effect items like
			// ale drop both thirst and hunger on the same purchase.
			// Mirrors the helper used in inventory.go::executeConsume.
			delta := applySatisfactionsToDelta(consumptionDelta{}, itemSatisfactions, qty)
			if delta.Hunger != 0 || delta.Thirst != 0 || delta.Tiredness != 0 {
				for _, c := range consumers {
					res, err := app.applyConsumption(ctx, tx, c.ID, delta, "pay-consume")
					if err != nil {
						return payResult{Result: "failed", Err: fmt.Sprintf("apply consumption for %s: %v", c.DisplayName, err)}
					}
					consumerUpdates = append(consumerUpdates, payConsumerUpdate{
						ActorID:     c.ID,
						DisplayName: c.DisplayName,
						Hunger:      res.Hunger,
						Thirst:      res.Thirst,
						Tiredness:   res.Tiredness,
					})
					if c.ID == buyer.ID {
						consumeResult = res
					}
				}
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
		// One npc_needs_changed broadcast per consumer (Phase C: group
		// orders feed multiple people in one pay; each one's needs UI
		// updates).
		for _, u := range consumerUpdates {
			app.Hub.Broadcast(WorldEvent{
				Type: "npc_needs_changed",
				Data: map[string]interface{}{
					"id":        u.ActorID,
					"hunger":    u.Hunger,
					"thirst":    u.Thirst,
					"tiredness": u.Tiredness,
				},
			})
		}
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

// payConsumer is one resolved consumer for an at-source pay order
// (Phase C). For the legacy single-consumer flow this is just the
// buyer; for group orders it's each named friend at the table.
type payConsumer struct {
	ID          string
	DisplayName string
}

// payConsumerUpdate carries the post-consume need state for one
// consumer, used to broadcast npc_needs_changed per-consumer after
// commit.
type payConsumerUpdate struct {
	ActorID     string
	DisplayName string
	Hunger      int
	Thirst      int
	Tiredness   int
}

// resolveAtSourceConsumers builds the consumer list for an at-source
// pay. Empty input → [buyer] (legacy single-consumer behavior). Named
// consumers must each (a) exist in the actor table, (b) share the
// buyer's current_huddle_id (you can't drink someone's ale from
// across the village). Returns ("", "") on success and ("", "err")
// on a rejection that should propagate as the payResult error.
//
// Locks each consumer row FOR UPDATE so a concurrent move-out doesn't
// slip a consumer out mid-transaction. Buyer is locked separately by
// the calling executePay above.
func (app *App) resolveAtSourceConsumers(ctx context.Context, tx pgx.Tx, buyer *agentNPCRow, names []string) ([]payConsumer, string) {
	if len(names) == 0 {
		return []payConsumer{{ID: buyer.ID, DisplayName: buyer.DisplayName}}, ""
	}

	// Buyer's huddle. Required for every named consumer to match.
	var buyerHuddle sql.NullString
	if err := tx.QueryRow(ctx,
		`SELECT current_huddle_id FROM actor WHERE id = $1`,
		buyer.ID,
	).Scan(&buyerHuddle); err != nil {
		return nil, fmt.Sprintf("lock buyer huddle: %v", err)
	}
	if !buyerHuddle.Valid {
		return nil, "consumers requires you to be in a room with them; you're not in a scene huddle right now"
	}

	// Normalize: trim, drop empties, dedupe (case-insensitive). The
	// buyer's name is allowed to appear (they're paying for themselves
	// + others) and is collapsed against the buyer's actor row.
	seen := make(map[string]bool, len(names))
	var clean []string
	for _, n := range names {
		t := strings.TrimSpace(n)
		if t == "" {
			continue
		}
		k := strings.ToLower(t)
		if seen[k] {
			continue
		}
		seen[k] = true
		clean = append(clean, t)
	}
	if len(clean) == 0 {
		return []payConsumer{{ID: buyer.ID, DisplayName: buyer.DisplayName}}, ""
	}

	resolved := make([]payConsumer, 0, len(clean))
	for _, name := range clean {
		var cid, cdn string
		var chuddle sql.NullString
		err := tx.QueryRow(ctx,
			`SELECT id, display_name, current_huddle_id
			   FROM actor
			  WHERE LOWER(display_name) = LOWER($1)
			  LIMIT 1
			  FOR UPDATE`,
			name,
		).Scan(&cid, &cdn, &chuddle)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Sprintf("no villager named %q", name)
			}
			return nil, fmt.Sprintf("lock consumer %q: %v", name, err)
		}
		if !chuddle.Valid || chuddle.String != buyerHuddle.String {
			return nil, fmt.Sprintf("%s is not in the room with you", cdn)
		}
		resolved = append(resolved, payConsumer{ID: cid, DisplayName: cdn})
	}
	return resolved, ""
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
