package main

// Order fulfillment — the second half of the v2 pay-ledger lifecycle
// added in ZBBS-129 step 2. ZBBS-128's executePay handles the payment-
// state machine (pending → accepted | declined | countered | etc.);
// this file handles the orthogonal fulfillment-state machine
// (pending → ready → delivered).
//
// Today the only path is deliver_order — every transaction we ship now
// has hours_per_unit = NULL/0 so the ledger row lands at
// fulfillment_status='ready' the moment pay-accept commits. The seller
// (typically an agent NPC keeper) then calls deliver_order(ledger_id)
// to finalize: flip the row to 'delivered', stamp delivered_on=NOW(),
// and either credit the buyer's inventory (take-home) or apply
// applyConsumption to each consumer (at-source).
//
// Why this split exists: ZBBS-128's executePayTransfer atomically moved
// coin AND item, so the buyer paid and instantly had the goods (or
// felt the consumption). The v2 design keeps coin transfer at pay-
// accept but defers the item handover to a separate seller-driven
// step. The seller has agency in the moment of delivery — "slides the
// bowl across" or "hands over the horseshoe." Without this split the
// keeper has no way to refuse to serve a known troublemaker, no
// reason for delivered_on to be a meaningful timestamp, and no
// foundation for craft items with real lead time (horseshoes, flour
// orders) where the work has to happen between order and delivery.
//
// Latency mitigation: the buyer experiences a small gap between paying
// and being fed (the keeper has to tick to call deliver_order). The
// existing ZBBS-126 post-pay reactor tick fires immediately after
// pay-accept on agent-NPC sellers (force=true bypasses the cost
// guard), giving the keeper a chance to deliver within seconds. See
// the reactor tick callsite in pc_handlers.go and agent_tick.go.
//
// Future scope flagged for follow-up commits:
//   - check_order_book / mark_order_ready tools (relevant once craft
//     items with hours_per_unit > 0 arrive — today the order book is
//     mostly already-ready rows that the keeper handles immediately).
//   - Capacity headline injection into the deliberation prompt.
//   - Lateness query + perception cues.
//   - Lodging hooks (skip inventory step for nights_stay items).

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

// deliverOrderResult mirrors payResult's tagged-union shape: Result is
// "ok" | "rejected" | "failed", and Err carries the human-readable
// detail when Result != "ok". On success, BuyerName / ItemKind / Qty
// surface what was delivered so the calling tool dispatcher can build
// a faithful tool-result message ("[OK] Delivered 1 stew to Jefferey").
type deliverOrderResult struct {
	Result    string
	Err       string
	BuyerName string
	ItemKind  string
	Qty       int
	// LedgerID echoes the input for log/audit context.
	LedgerID int64
}

// executeDeliverOrder finalizes a fulfillment-ready ledger row. Called
// from the deliver_order LLM tool dispatch (NPC sellers via agent_tick)
// or future PC-keeper UI. Atomic: validates the ledger row, transfers
// goods or applies consumption, flips fulfillment_status to 'delivered',
// and broadcasts inventory / needs / room events.
//
// Validation rejects (no DB writes performed):
//   - Ledger row missing.
//   - Seller mismatch (you can only deliver orders YOU sold).
//   - state != 'accepted' (pay was declined / countered / withdrawn /
//     pending-and-aging — nothing to deliver).
//   - fulfillment_status != 'ready' (already delivered, or still pending
//     for craft work — caller should mark_order_ready first).
//
// Engine errors during the transfer/consumption tx propagate as
// Result="failed" with a human-readable reason; the ledger row stays
// 'ready' so a retry can reattempt.
func (app *App) executeDeliverOrder(ctx context.Context, sellerID string, ledgerID int64) deliverOrderResult {
	if sellerID == "" {
		return deliverOrderResult{Result: "rejected", Err: "missing seller", LedgerID: ledgerID}
	}
	if ledgerID <= 0 {
		return deliverOrderResult{Result: "rejected", Err: "missing ledger id", LedgerID: ledgerID}
	}

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return deliverOrderResult{Result: "failed", Err: fmt.Sprintf("begin tx: %v", err), LedgerID: ledgerID}
	}
	defer tx.Rollback(ctx)

	// Lock the ledger row + load fields needed for the delivery decision.
	// FOR UPDATE prevents two concurrent deliver_order calls from racing
	// (both see 'ready', both apply consumption, double-feeding). Buyer
	// + seller display names are pulled via subselect to avoid extra
	// trips for the broadcast/narration text.
	var (
		ledgerState        string
		fulfillmentStatus  string
		ledgerSellerID     string
		buyerID            string
		buyerDisplayName   string
		itemKindNS         sql.NullString
		qtyNS              sql.NullInt32
		consumeNow         bool
		consumerActorIDs   []string
	)
	err = tx.QueryRow(ctx,
		`SELECT pl.state, pl.fulfillment_status, pl.seller_id::text,
		        pl.buyer_id::text,
		        (SELECT display_name FROM actor WHERE id = pl.buyer_id),
		        pl.item_kind, pl.qty, pl.consume_now,
		        COALESCE(pl.consumer_actor_ids, ARRAY[]::uuid[])::text[]
		   FROM pay_ledger pl
		  WHERE pl.id = $1
		  FOR UPDATE`,
		ledgerID,
	).Scan(
		&ledgerState, &fulfillmentStatus, &ledgerSellerID,
		&buyerID, &buyerDisplayName,
		&itemKindNS, &qtyNS, &consumeNow,
		&consumerActorIDs,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return deliverOrderResult{Result: "rejected", Err: fmt.Sprintf("no such ledger row %d", ledgerID), LedgerID: ledgerID}
		}
		return deliverOrderResult{Result: "failed", Err: fmt.Sprintf("lock ledger row: %v", err), LedgerID: ledgerID}
	}
	if ledgerSellerID != sellerID {
		return deliverOrderResult{Result: "rejected", Err: fmt.Sprintf("ledger row %d is not yours to deliver", ledgerID), LedgerID: ledgerID}
	}
	if ledgerState != "accepted" {
		return deliverOrderResult{Result: "rejected", Err: fmt.Sprintf("ledger row %d state is %q (need 'accepted')", ledgerID, ledgerState), LedgerID: ledgerID}
	}
	if fulfillmentStatus != "ready" {
		return deliverOrderResult{Result: "rejected", Err: fmt.Sprintf("ledger row %d fulfillment_status is %q (need 'ready')", ledgerID, fulfillmentStatus), LedgerID: ledgerID}
	}
	if !itemKindNS.Valid || itemKindNS.String == "" {
		// Pure coin transfer — there's nothing to deliver. The schema
		// allows fulfillment_status='ready' for the sake of orthogonal
		// uniformity, but deliver_order on a coin-only row is a tool
		// misuse. Surface a clean rejection.
		return deliverOrderResult{Result: "rejected", Err: fmt.Sprintf("ledger row %d carries no item to deliver", ledgerID), LedgerID: ledgerID}
	}
	itemKind := itemKindNS.String
	qty := 1
	if qtyNS.Valid {
		qty = int(qtyNS.Int32)
	}

	// Resolve the consumer set. ConsumerActorIDs being non-empty means
	// phase C (the buyer named friends at pay time). Empty/NULL means
	// the legacy single-consumer flow — the buyer is the implicit
	// consumer, used for both at-source consumption and as the
	// inventory recipient for take-home.
	var deliveryActorIDs []string
	if len(consumerActorIDs) > 0 {
		deliveryActorIDs = consumerActorIDs
	} else {
		deliveryActorIDs = []string{buyerID}
	}

	// --- Branch 1: at-source consumption (consume_now=true) ---
	//
	// For each delivery actor, snapshot their pre-need values for the
	// felt-language narration, run applyConsumption, and capture the
	// post-need values for npc_needs_changed broadcasts. Multi-attribute
	// items (ale → thirst + hunger) get applied via item_satisfies in
	// one delta per consumer. Skips silently when the item has no
	// satisfactions OR the delta is all-zeros (consume_now on a non-
	// satisfying item shouldn't reach here — the pay-accept path would
	// have rejected it — but defense-in-depth doesn't hurt).
	//
	// --- Branch 2: take-home (consume_now=false) ---
	//
	// Credit the buyer's actor_inventory by qty. INSERT ... ON CONFLICT
	// matches the v1 atomic-with-pay path. Phase C take-home isn't
	// supported (the pay-accept path rejects consumers + take-home
	// pairings); deliveryActorIDs contains exactly buyerID here.

	type postUpdate struct {
		ActorID   string
		Hunger    int
		Thirst    int
		Tiredness int
		Pre       map[string]int
		Post      map[string]int
	}
	var consumerUpdates []postUpdate

	if consumeNow {
		satisfactions, sErr := loadItemSatisfactions(ctx, tx, itemKind)
		if sErr != nil {
			return deliverOrderResult{Result: "failed", Err: fmt.Sprintf("load satisfactions: %v", sErr), LedgerID: ledgerID}
		}
		delta := applySatisfactionsToDelta(consumptionDelta{}, satisfactions, qty)
		if delta.Hunger != 0 || delta.Thirst != 0 || delta.Tiredness != 0 {
			for _, aid := range deliveryActorIDs {
				// Pre-state read — non-locking. applyConsumption locks
				// the actor row internally, so racing pre-read vs apply
				// is fine: any drift is at-most-one-tick and only
				// affects the narration text, not the persisted state.
				pre := map[string]int{"hunger": 0, "thirst": 0, "tiredness": 0}
				rows, err := tx.Query(ctx,
					`SELECT key, value FROM actor_need WHERE actor_id = $1 AND key IN ('hunger','thirst','tiredness')`,
					aid,
				)
				if err == nil {
					for rows.Next() {
						var k string
						var v int
						if err := rows.Scan(&k, &v); err == nil {
							pre[k] = v
						}
					}
					rows.Close()
				}
				res, err := app.applyConsumption(ctx, tx, aid, delta, "deliver-order")
				if err != nil {
					return deliverOrderResult{Result: "failed", Err: fmt.Sprintf("apply consumption for actor %s: %v", aid, err), LedgerID: ledgerID}
				}
				consumerUpdates = append(consumerUpdates, postUpdate{
					ActorID:   aid,
					Hunger:    res.Hunger,
					Thirst:    res.Thirst,
					Tiredness: res.Tiredness,
					Pre:       pre,
					Post:      map[string]int{"hunger": res.Hunger, "thirst": res.Thirst, "tiredness": res.Tiredness},
				})
			}
		}
	} else {
		// Take-home — credit buyer's inventory.
		if _, err := tx.Exec(ctx,
			`INSERT INTO actor_inventory (actor_id, item_kind, quantity)
			 VALUES ($1, $2, $3)
			 ON CONFLICT (actor_id, item_kind)
			 DO UPDATE SET quantity = actor_inventory.quantity + EXCLUDED.quantity`,
			buyerID, itemKind, qty,
		); err != nil {
			return deliverOrderResult{Result: "failed", Err: fmt.Sprintf("credit buyer stock: %v", err), LedgerID: ledgerID}
		}
	}

	// Flip fulfillment_status='ready' → 'delivered', stamp delivered_on.
	// Gated on the same WHERE we used to lock so a concurrent UPDATE
	// from another path (admin override, future buyer-cancel, etc.)
	// won't double-flip. RowsAffected==1 sanity check.
	tag, err := tx.Exec(ctx,
		`UPDATE pay_ledger
		    SET fulfillment_status = 'delivered',
		        delivered_on = NOW()
		  WHERE id = $1
		    AND fulfillment_status = 'ready'`,
		ledgerID,
	)
	if err != nil {
		return deliverOrderResult{Result: "failed", Err: fmt.Sprintf("update fulfillment_status: %v", err), LedgerID: ledgerID}
	}
	if tag.RowsAffected() != 1 {
		return deliverOrderResult{Result: "failed", Err: fmt.Sprintf("fulfillment update affected %d rows for ledger %d (expected 1)", tag.RowsAffected(), ledgerID), LedgerID: ledgerID}
	}

	if err := tx.Commit(ctx); err != nil {
		return deliverOrderResult{Result: "failed", Err: fmt.Sprintf("commit tx: %v", err), LedgerID: ledgerID}
	}

	// Broadcasts (post-commit so observers see authoritative state):
	//   - npc_delivered: the handover event itself.
	//   - actor_inventory_changed: buyer's stock changed (take-home).
	//   - npc_needs_changed: per-consumer needs (at-source).
	//   - private room_event: per-consumer felt-language narration
	//     (PCs render this in the brown box; NPCs ignore it).
	now := time.Now().UTC().Format(time.RFC3339)
	app.Hub.Broadcast(WorldEvent{
		Type: "npc_delivered",
		Data: map[string]interface{}{
			"ledger_id":   ledgerID,
			"seller_id":   sellerID,
			"buyer_id":    buyerID,
			"buyer":       buyerDisplayName,
			"item":        itemKind,
			"qty":         qty,
			"consume_now": consumeNow,
			"at":          now,
		},
	})

	if !consumeNow {
		app.Hub.Broadcast(WorldEvent{
			Type: "actor_inventory_changed",
			Data: map[string]any{
				"actor_id":  buyerID,
				"item_kind": itemKind,
			},
		})
	} else if len(consumerUpdates) > 0 {
		// Surface per-consumer need state and the felt-language line
		// (private room_event scoped by actor_id; the Godot client
		// filters by its PC's actor id and shows it in the brown box).
		satisfactions, _ := loadItemSatisfactions(ctx, app.DB, itemKind)
		var sellerStructure sql.NullString
		_ = app.DB.QueryRow(ctx,
			`SELECT inside_structure_id::text FROM actor WHERE id = $1`,
			sellerID,
		).Scan(&sellerStructure)
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
			if len(satisfactions) > 0 {
				if selfText := narrateConsumeSelf(itemKind, qty, satisfactions, u.Pre, u.Post); selfText != "" {
					data := map[string]interface{}{
						"actor_id":   u.ActorID,
						"actor_name": "",
						"kind":       "consume",
						"text":       selfText,
						"private":    true,
						"at":         now,
					}
					if sellerStructure.Valid && sellerStructure.String != "" {
						data["structure_id"] = sellerStructure.String
					}
					app.Hub.Broadcast(WorldEvent{Type: "room_event", Data: data})
				}
			}
		}
	}

	log.Printf("deliver_order ok (ledger=%d seller=%s item=%s qty=%d consumers=%d)",
		ledgerID, sellerID, itemKind, qty, len(deliveryActorIDs))

	return deliverOrderResult{
		Result:    "ok",
		BuyerName: buyerDisplayName,
		ItemKind:  itemKind,
		Qty:       qty,
		LedgerID:  ledgerID,
	}
}

// orderBookEntry is one outstanding ledger row from the seller's
// perspective — used by check_order_book to surface what's owed.
type orderBookEntry struct {
	LedgerID    int64
	BuyerName   string
	ItemKind    string
	Qty         int
	ReadyBy     time.Time
	ConsumeNow  bool
}

// checkOrderBookForSeller returns the seller's outstanding orders in
// the order they should be served: by ready_by ascending (oldest due
// first), then created_at ascending (FIFO within a date). Mirrors the
// partial index ix_pay_ledger_outstanding so the read is cheap.
//
// The current code path (every ledger row ships at fulfillment_status=
// 'ready') means this list is essentially "what you owe right now."
// When craft items with hours_per_unit > 0 land, the same query will
// surface 'pending' rows alongside 'ready' rows so the LLM can plan
// its day.
func (app *App) checkOrderBookForSeller(ctx context.Context, sellerID string) ([]orderBookEntry, error) {
	rows, err := app.DB.Query(ctx,
		`SELECT pl.id, pl.item_kind, pl.qty, pl.ready_by, pl.consume_now,
		        a.display_name
		   FROM pay_ledger pl
		   JOIN actor a ON a.id = pl.buyer_id
		  WHERE pl.seller_id = $1
		    AND pl.state = 'accepted'
		    AND pl.fulfillment_status <> 'delivered'
		  ORDER BY pl.ready_by ASC, pl.created_at ASC`,
		sellerID,
	)
	if err != nil {
		return nil, fmt.Errorf("query order book: %w", err)
	}
	defer rows.Close()
	var out []orderBookEntry
	for rows.Next() {
		var (
			e        orderBookEntry
			itemKind sql.NullString
			qty      sql.NullInt32
		)
		if err := rows.Scan(&e.LedgerID, &itemKind, &qty, &e.ReadyBy, &e.ConsumeNow, &e.BuyerName); err != nil {
			return nil, fmt.Errorf("scan order book row: %w", err)
		}
		if itemKind.Valid {
			e.ItemKind = itemKind.String
		}
		if qty.Valid {
			e.Qty = int(qty.Int32)
		} else {
			e.Qty = 1
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate order book: %w", err)
	}
	return out, nil
}

// formatOrderBookForLLM builds a compact human-readable string for the
// check_order_book tool result. Empty list → "No outstanding orders."
// Otherwise one line per row: "ledger_id=N: <qty> <item> for <buyer>
// (ready <date>)". The LLM uses these IDs as inputs to deliver_order.
func formatOrderBookForLLM(entries []orderBookEntry) string {
	if len(entries) == 0 {
		return "No outstanding orders."
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Outstanding orders (%d):\n", len(entries)))
	for _, e := range entries {
		fmt.Fprintf(&b, "  ledger_id=%d: %d %s for %s (ready %s)\n",
			e.LedgerID, e.Qty, e.ItemKind, e.BuyerName, e.ReadyBy.Format("2006-01-02"))
	}
	return strings.TrimRight(b.String(), "\n")
}
