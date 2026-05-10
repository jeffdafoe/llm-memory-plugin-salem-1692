package main

// Fulfill walker (ZBBS-HOME-247) — sellers walking pending orders
// to their customers. Mirror of buy_walker but the direction is
// reversed: the seller leaves their stall, walks to the customer's
// work_structure, completes the order at the door, walks home.
//
// Per-tick algorithm:
//
//   1. Stale-trip sweep on actor_delivery_in_progress.
//   2. List sellers with at least one pending pay_ledger order
//      (state='accepted', fulfillment_status='pending') AND enough
//      stock to fulfill it.
//   3. For each seller (skip if already on a delivery trip):
//        a. Find their oldest pending order they can fulfill.
//        b. Insert actor_delivery_in_progress row, stamp break +
//           override, walk to customer's work_structure.
//
// Arrival hook (called from applyArrivalSideEffects after the
// buy_walker hook):
//   * If seller has a delivery in progress and arrived at the
//     expected customer structure: complete the order — transfer
//     goods + retail-price coins, flip pay_ledger to delivered,
//     dialogue, walk seller home.
//   * Inbound: clear trip + restore inside flag (footprint check).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

func (app *App) dispatchFulfillWalker(ctx context.Context) {
	now := time.Now().UTC()

	// 1. Stale sweep.
	if _, err := app.DB.Exec(ctx,
		`DELETE FROM actor_delivery_in_progress WHERE started_at < $1`,
		now.Add(-tripStaleAfter),
	); err != nil {
		log.Printf("fulfill_walker: stale sweep: %v", err)
	}

	// 2. Find sellers with fulfillable pending orders. A seller is
	//    fulfillable for item X when they have a pending order for X
	//    AND their inventory.X >= the order qty. Use a single SQL
	//    query that returns one (seller_id, oldest_order_id, item, qty,
	//    customer_id) per fulfillable seller.
	rows, err := app.DB.Query(ctx, `
		WITH fulfillable AS (
		    SELECT pl.id AS order_id,
		           pl.seller_id,
		           pl.buyer_id AS customer_id,
		           pl.item_kind,
		           pl.qty,
		           pl.created_at,
		           ROW_NUMBER() OVER (
		               PARTITION BY pl.seller_id
		               ORDER BY pl.created_at
		           ) AS rn
		      FROM pay_ledger pl
		      JOIN actor_inventory ai
		        ON ai.actor_id = pl.seller_id
		       AND ai.item_kind = pl.item_kind
		     WHERE pl.state = 'accepted'
		       AND pl.fulfillment_status = 'pending'
		       AND ai.quantity >= pl.qty
		)
		SELECT order_id, seller_id::text, customer_id::text, item_kind, qty
		  FROM fulfillable
		 WHERE rn = 1
		   AND seller_id NOT IN (
		       SELECT actor_id FROM actor_delivery_in_progress
		   )
	`)
	if err != nil {
		log.Printf("fulfill_walker: list fulfillable: %v", err)
		return
	}
	defer rows.Close()

	type row struct {
		orderID    int64
		sellerID   string
		customerID string
		itemKind   string
		qty        int
	}
	var fulfillable []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.orderID, &r.sellerID, &r.customerID, &r.itemKind, &r.qty); err != nil {
			log.Printf("fulfill_walker: scan: %v", err)
			continue
		}
		fulfillable = append(fulfillable, r)
	}
	rows.Close()

	for _, r := range fulfillable {
		app.startDeliveryTrip(ctx, r.sellerID, r.customerID, r.itemKind, r.qty, r.orderID)
	}
}

// startDeliveryTrip kicks off the seller's outbound walk to the
// customer's work_structure with the goods.
func (app *App) startDeliveryTrip(
	ctx context.Context,
	sellerID, customerID, itemKind string,
	qty int,
	orderID int64,
) {
	// Resolve customer's work_structure walk-target.
	var (
		customerWorkStructureID *string
		objX, objY              *float64
		loiterX, loiterY        *float64
		sellerX, sellerY        float64
	)
	const tileSize = 32.0

	if err := app.DB.QueryRow(ctx, `
		SELECT a.work_structure_id::text, vo.x, vo.y, vo.loiter_offset_x, vo.loiter_offset_y
		  FROM actor a
		  LEFT JOIN village_object vo ON vo.id = a.work_structure_id
		 WHERE a.id = $1::uuid
	`, customerID).Scan(&customerWorkStructureID, &objX, &objY, &loiterX, &loiterY); err != nil {
		log.Printf("fulfill_walker: load customer %s work: %v", customerID, err)
		return
	}
	if customerWorkStructureID == nil || objX == nil {
		// Customer has no work_structure — can't deliver. Skip; the
		// order stays pending.
		log.Printf("fulfill_walker: customer %s has no work_structure, skipping order %d",
			customerID, orderID)
		return
	}

	walkTargetX := *objX
	walkTargetY := *objY
	if loiterX != nil {
		walkTargetX += *loiterX * tileSize
	}
	if loiterY != nil {
		walkTargetY += *loiterY * tileSize
	}

	// Capture seller's pre-trip coords for the return leg.
	if err := app.DB.QueryRow(ctx,
		`SELECT current_x, current_y FROM actor WHERE id = $1::uuid`,
		sellerID,
	).Scan(&sellerX, &sellerY); err != nil {
		log.Printf("fulfill_walker: load seller %s pos: %v", sellerID, err)
		return
	}

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		log.Printf("fulfill_walker: begin tx: %v", err)
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`INSERT INTO actor_delivery_in_progress
		   (actor_id, customer_id, item_kind, qty, pay_ledger_id,
		    customer_structure_id, home_x, home_y, phase)
		 VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6::uuid, $7, $8, 'outbound')`,
		sellerID, customerID, itemKind, qty, orderID,
		*customerWorkStructureID, sellerX, sellerY,
	); err != nil {
		log.Printf("fulfill_walker: insert delivery row: %v", err)
		return
	}

	breakUntil := time.Now().UTC().Add(tripBreakDuration)
	if _, err := tx.Exec(ctx,
		`UPDATE actor SET break_until = $2, agent_override_until = $2
		  WHERE id = $1::uuid`,
		sellerID, breakUntil,
	); err != nil {
		log.Printf("fulfill_walker: set break: %v", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("fulfill_walker: commit start: %v", err)
		return
	}

	walkResult, walkErr := app.startNPCWalk(ctx, sellerID, walkTargetX, walkTargetY, 0)
	if walkErr != nil {
		log.Printf("fulfill_walker: walk-start for %s: %v", sellerID, walkErr)
		app.cancelDeliveryTrip(ctx, sellerID, "walk-start failed")
		return
	}
	app.markWalkTargetStructure(sellerID, *customerWorkStructureID)

	// Seller speaks as they leave: "Off to deliver to <customer>."
	var customerName string
	_ = app.DB.QueryRow(ctx,
		`SELECT display_name FROM actor WHERE id = $1::uuid`, customerID,
	).Scan(&customerName)
	if customerName != "" {
		app.broadcastSellerSpoke(ctx, sellerID, fmt.Sprintf(
			"Taking %d %s 'round to %s.", qty, itemKind, customerName,
		), []string{itemKind}, 0)
	}

	log.Printf("fulfill_walker: trip start seller=%s customer=%s item=%s qty=%d order=%d, walk=%.0fs",
		sellerID, customerName, itemKind, qty, orderID, walkResult.DurationSec)
}

// handleFulfillWalkerArrival is called from applyArrivalSideEffects.
// Returns true if it handled the arrival.
func (app *App) handleFulfillWalkerArrival(ctx context.Context, actorID string, arrivedStructureID string) bool {
	var (
		customerID            string
		itemKind              string
		qty                   int
		orderID               int64
		customerStructureID   string
		homeX, homeY          float64
		phase                 string
	)
	err := app.DB.QueryRow(ctx,
		`SELECT customer_id::text, item_kind, qty, pay_ledger_id,
		        customer_structure_id::text, home_x, home_y, phase
		   FROM actor_delivery_in_progress WHERE actor_id = $1::uuid`,
		actorID,
	).Scan(&customerID, &itemKind, &qty, &orderID,
		&customerStructureID, &homeX, &homeY, &phase)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		log.Printf("fulfill_walker: arrival check %s: %v", actorID, err)
		return false
	}

	switch phase {
	case "outbound":
		if arrivedStructureID != customerStructureID {
			app.cancelDeliveryTrip(ctx, actorID, "outbound arrived at unexpected structure")
			return true
		}
		app.completeDeliveryLeg(ctx, actorID, customerID, itemKind, qty, orderID, homeX, homeY)
		return true
	case "inbound":
		app.cancelDeliveryTrip(ctx, actorID, "inbound arrival")
		return true
	}
	return false
}

// completeDeliveryLeg performs the door-to-door transfer + dialogue,
// flips the pay_ledger row to delivered, dispatches the return walk,
// and flips phase to 'inbound'.
func (app *App) completeDeliveryLeg(
	ctx context.Context,
	sellerID, customerID, itemKind string,
	qty int,
	orderID int64,
	homeX, homeY float64,
) {
	delivered := app.tryDeliverOrder(ctx, sellerID, customerID, itemKind, qty, orderID)
	if delivered {
		// Customer-facing dialogue at the door.
		var customerName string
		_ = app.DB.QueryRow(ctx,
			`SELECT display_name FROM actor WHERE id = $1::uuid`, customerID,
		).Scan(&customerName)
		var unitPrice int
		_ = app.DB.QueryRow(ctx,
			`SELECT quoted_unit_amount FROM pay_ledger WHERE id = $1`, orderID,
		).Scan(&unitPrice)
		if customerName != "" {
			text := fmt.Sprintf("Here's your %s, %s. That'll be %d coin%s.",
				itemKind, customerName, unitPrice*qty, pluralCoins(unitPrice*qty))
			app.broadcastSellerSpoke(ctx, sellerID, text, []string{itemKind}, unitPrice)
		}
	} else {
		// Couldn't deliver (out of stock by the time we arrived?).
		// Order stays pending — fulfill_walker re-evaluates next tick.
		log.Printf("fulfill_walker: delivery failed seller=%s order=%d (will retry)",
			sellerID, orderID)
	}

	// Flip phase, dispatch return walk back to seller's pre-trip pos.
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor_delivery_in_progress SET phase = 'inbound' WHERE actor_id = $1::uuid`,
		sellerID,
	); err != nil {
		log.Printf("fulfill_walker: flip to inbound: %v", err)
		return
	}
	if _, err := app.startNPCWalk(ctx, sellerID, homeX, homeY, 0); err != nil {
		log.Printf("fulfill_walker: return walk: %v", err)
	}
}

// tryDeliverOrder transfers goods + coins for the order, flips
// pay_ledger to delivered. Returns false if seller no longer has
// stock (raced with someone else taking it).
func (app *App) tryDeliverOrder(
	ctx context.Context,
	sellerID, customerID, itemKind string,
	qty int,
	orderID int64,
) bool {
	// Read the locked-in unit price from pay_ledger.
	var unitPrice int
	if err := app.DB.QueryRow(ctx,
		`SELECT quoted_unit_amount FROM pay_ledger WHERE id = $1`,
		orderID,
	).Scan(&unitPrice); err != nil {
		log.Printf("fulfill_walker: load order price: %v", err)
		return false
	}
	totalPrice := unitPrice * qty

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		log.Printf("fulfill_walker: begin transfer: %v", err)
		return false
	}
	defer tx.Rollback(ctx)

	// Lock seller stock.
	var sellerQty int
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(quantity, 0) FROM actor_inventory
		  WHERE actor_id = $1::uuid AND item_kind = $2 FOR UPDATE`,
		sellerID, itemKind,
	).Scan(&sellerQty)
	if errors.Is(err, pgx.ErrNoRows) || sellerQty < qty {
		return false
	}
	if err != nil {
		log.Printf("fulfill_walker: lock seller inv: %v", err)
		return false
	}

	if _, err := tx.Exec(ctx,
		`UPDATE actor_inventory SET quantity = quantity - $3
		  WHERE actor_id = $1::uuid AND item_kind = $2`,
		sellerID, itemKind, qty,
	); err != nil {
		log.Printf("fulfill_walker: decrement seller: %v", err)
		return false
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM actor_inventory
		  WHERE actor_id = $1::uuid AND item_kind = $2 AND quantity <= 0`,
		sellerID, itemKind,
	); err != nil {
		log.Printf("fulfill_walker: cleanup zero: %v", err)
		return false
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO actor_inventory (actor_id, item_kind, quantity)
		 VALUES ($1::uuid, $2, $3)
		 ON CONFLICT (actor_id, item_kind)
		 DO UPDATE SET quantity = actor_inventory.quantity + EXCLUDED.quantity`,
		customerID, itemKind, qty,
	); err != nil {
		log.Printf("fulfill_walker: credit customer: %v", err)
		return false
	}
	// Coins: customer pays seller. Best-effort (coins may go negative).
	if _, err := tx.Exec(ctx,
		`UPDATE actor SET coins = coins - $2 WHERE id = $1::uuid`,
		customerID, totalPrice,
	); err != nil {
		log.Printf("fulfill_walker: deduct customer coins: %v", err)
		return false
	}
	if _, err := tx.Exec(ctx,
		`UPDATE actor SET coins = coins + $2 WHERE id = $1::uuid`,
		sellerID, totalPrice,
	); err != nil {
		log.Printf("fulfill_walker: credit seller coins: %v", err)
		return false
	}
	// Flip pay_ledger to delivered.
	if _, err := tx.Exec(ctx,
		`UPDATE pay_ledger
		    SET fulfillment_status = 'delivered',
		        delivered_on = NOW(),
		        offered_amount = $2
		  WHERE id = $1`,
		orderID, totalPrice,
	); err != nil {
		log.Printf("fulfill_walker: flip pay_ledger: %v", err)
		return false
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("fulfill_walker: commit transfer: %v", err)
		return false
	}

	app.Hub.Broadcast(WorldEvent{
		Type: "actor_inventory_changed",
		Data: map[string]any{"actor_id": customerID, "item_kind": itemKind},
	})
	app.Hub.Broadcast(WorldEvent{
		Type: "actor_inventory_changed",
		Data: map[string]any{"actor_id": sellerID, "item_kind": itemKind},
	})
	log.Printf("fulfill_walker: delivered seller=%s customer=%s item=%s qty=%d total=%d",
		sellerID, customerID, itemKind, qty, totalPrice)
	return true
}

// cancelDeliveryTrip clears delivery state + break + restores inside
// flag if the seller is back inside their work_structure footprint.
func (app *App) cancelDeliveryTrip(ctx context.Context, sellerID, reason string) {
	if _, err := app.DB.Exec(ctx,
		`DELETE FROM actor_delivery_in_progress WHERE actor_id = $1::uuid`,
		sellerID,
	); err != nil {
		log.Printf("fulfill_walker: clear trip: %v", err)
	}
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor SET break_until = NULL, agent_override_until = NULL
		  WHERE id = $1::uuid`,
		sellerID,
	); err != nil {
		log.Printf("fulfill_walker: clear break: %v", err)
	}
	if _, err := app.DB.Exec(ctx, `
		UPDATE actor a
		   SET inside_structure_id = a.work_structure_id,
		       inside = TRUE
		  FROM village_object vo
		  JOIN asset s ON s.id = vo.asset_id
		 WHERE a.id = $1::uuid
		   AND a.work_structure_id IS NOT NULL
		   AND vo.id = a.work_structure_id
		   AND a.current_x BETWEEN vo.x - s.footprint_left * 32 AND vo.x + s.footprint_right * 32
		   AND a.current_y BETWEEN vo.y - s.footprint_top  * 32 AND vo.y + s.footprint_bottom * 32
	`, sellerID); err != nil {
		log.Printf("fulfill_walker: restore inside: %v", err)
	}
	log.Printf("fulfill_walker: trip end seller=%s reason=%s", sellerID, reason)
}

// Silence unused-import warning if any of the database/sql usages
// disappear during refactors. (Kept here so future edits don't break.)
var _ = sql.NullString{}
