package main

// Buy walker (ZBBS-HOME-244) — the executor that ACTS on the buy
// resolver's output. Walks an NPC to a chosen seller, performs a
// deterministic transfer (or stamps a no_stock row on empty
// arrival), walks them home, clears trip state.
//
// Per-tick algorithm:
//
//   1. Stale-trip sweep: clear actor_restock_in_progress rows older
//      than tripStaleAfter (30 min) — covers crash-mid-trip cases
//      where the walker rebooted halfway through.
//
//   2. List actors with at least one `buy` restock entry whose
//      params.restock contains a buy source.
//
//   3. For each such actor:
//        a. Skip if already on a trip (actor_restock_in_progress
//           row exists). One trip at a time per actor.
//        b. Walk the actor's restock policy in order. For each `buy`
//           entry below target, run resolveBuyCandidate. The first
//           entry that returns Reason='ok' wins; later entries wait
//           for next tick.
//        c. Start the trip: insert actor_restock_in_progress row
//           (phase='outbound', captures home coords for the return
//           leg), set break_until + agent_override_until, dispatch
//           startNPCWalk to the seller's structure walk-target.
//
// Arrival hook (called from applyArrivalSideEffects):
//   * If actor has an in-progress trip:
//     - phase='outbound' AND arrived at the expected seller structure:
//       attempt the transfer (lock seller inventory, decrement +
//       credit buyer + write pay_ledger row at deterministic price).
//       Empty seller → recordNoStockAttempt instead. Either way:
//       update phase='inbound' and start the return walk.
//     - phase='inbound' AND arrived (any structure): clear the trip
//       row, clear break_until + agent_override_until.
//
// Pricing (v1): deterministic per-item table. No LLM haggling. The
// haggling-as-visible-interaction beat is reserved for a future
// iteration that delegates the on-arrival transaction to the
// salem-vendor LLM rather than running it directly here.
//
// Take_break composition: we set break_until + agent_override_until
// directly (skipping the take_break TOOL path) because we don't want
// the agent's "I'm closing my post" speak; the engine narrates
// elsewhere via inventory broadcasts. The eviction logic from
// take_break is also skipped — restock trips are short enough that
// stranded customers can wait.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// tripStaleAfter is how long a restock_in_progress row can sit
	// before the next dispatcher pass clears it. Covers engine
	// restarts mid-trip and pathfinding failures that left the row
	// without a corresponding active walk.
	tripStaleAfter = 30 * time.Minute

	// tripBreakDuration is how long break_until is set when a trip
	// kicks off. Should comfortably exceed the round-trip walk plus
	// the on-arrival transaction time. End-of-trip clears it
	// explicitly; this is the fallback for crashes.
	tripBreakDuration = 30 * time.Minute
)

// buyDeterministicPrice returns the v1 unit price for an engine-driven
// buy. Defaults conservative — tunable per-item later. Returns 1 for
// items not specifically priced so the chain doesn't stall on missing
// data.
var buyDeterministicPrice = map[string]int{
	"cheese":  3,
	"milk":    2,
	"meat":    4,
	"carrots": 1,
	"bread":   2,
	"ale":     1,
	"water":   1,
	"berries": 1,
	"stew":    5,
}

func priceFor(item string) int {
	if p, ok := buyDeterministicPrice[item]; ok {
		return p
	}
	return 1
}

func (app *App) dispatchBuyWalker(ctx context.Context) {
	now := time.Now().UTC()

	// 1. Stale trip sweep.
	if _, err := app.DB.Exec(ctx,
		`DELETE FROM actor_restock_in_progress WHERE started_at < $1`,
		now.Add(-tripStaleAfter),
	); err != nil {
		log.Printf("buy_walker: stale sweep: %v", err)
	}

	// 2. List actors with `buy` restock entries.
	actorIDs, err := app.listActorsWithRestockEntries(ctx, RestockSourceBuy)
	if err != nil {
		log.Printf("buy_walker: list actors: %v", err)
		return
	}
	if len(actorIDs) == 0 {
		return
	}

	for _, actorID := range actorIDs {
		app.tickBuyForActor(ctx, actorID, now)
	}
}

// tickBuyForActor evaluates one actor's buy entries and (if eligible)
// starts a single trip.
func (app *App) tickBuyForActor(ctx context.Context, actorID string, now time.Time) {
	// Skip if already on a trip.
	var existingItem string
	err := app.DB.QueryRow(ctx,
		`SELECT item_kind FROM actor_restock_in_progress WHERE actor_id = $1::uuid`,
		actorID,
	).Scan(&existingItem)
	if err == nil {
		return // trip in progress
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		log.Printf("buy_walker: in-progress check %s: %v", actorID, err)
		return
	}

	// Load buyer's current position + work_structure (for the return
	// leg) + active hours. We don't gate buy on active hours strictly
	// — buyers can shop outside their working window — but if the
	// buyer is asleep, walking them off would be weird. Simplest gate:
	// must be inside their work_structure (i.e., currently working).
	var (
		buyerX, buyerY        float64
		workStructureIDStr    *string
		insideStructureIDStr  *string
	)
	err = app.DB.QueryRow(ctx,
		`SELECT current_x, current_y, work_structure_id::text, inside_structure_id::text
		   FROM actor WHERE id = $1::uuid`,
		actorID,
	).Scan(&buyerX, &buyerY, &workStructureIDStr, &insideStructureIDStr)
	if err != nil {
		log.Printf("buy_walker: load buyer %s: %v", actorID, err)
		return
	}
	if workStructureIDStr == nil || insideStructureIDStr == nil ||
		*workStructureIDStr != *insideStructureIDStr {
		// Buyer is not at their work_structure (maybe sleeping at
		// home, mid-other-trip, etc). Skip — try next tick.
		return
	}

	policy, err := app.loadActorRestockPolicy(ctx, actorID)
	if err != nil {
		log.Printf("buy_walker: load policy %s: %v", actorID, err)
		return
	}

	for _, entry := range policy.Restock {
		if entry.Source != RestockSourceBuy {
			continue
		}
		decision, err := app.resolveBuyCandidate(ctx, actorID, entry, buyerX, buyerY, now)
		if err != nil {
			log.Printf("buy_walker: resolve %s/%s: %v", actorID, entry.Item, err)
			continue
		}
		if decision.Reason != "ok" || decision.Candidate == nil {
			continue
		}

		// Found one. Start the trip.
		if err := app.startBuyTrip(ctx, actorID, entry.Item, decision.Candidate,
			buyerX, buyerY, *workStructureIDStr); err != nil {
			log.Printf("buy_walker: start trip %s for %s: %v", actorID, entry.Item, err)
		}
		return // one trip per actor per tick
	}
}

// startBuyTrip kicks off the outbound leg: persists trip state,
// stamps break/override, dispatches the walk.
func (app *App) startBuyTrip(
	ctx context.Context,
	buyerID, itemKind string,
	candidate *BuyCandidate,
	buyerHomeX, buyerHomeY float64,
	buyerWorkStructureID string,
) error {
	// Resolve seller's work_structure_id explicitly so the arrival
	// hook can match against it. Walk-target coords from the
	// candidate are derived from work_structure but we store the
	// structure id for the arrival check.
	var sellerStructureID *string
	if err := app.DB.QueryRow(ctx,
		`SELECT work_structure_id::text FROM actor WHERE id = $1::uuid`,
		candidate.ActorID,
	).Scan(&sellerStructureID); err != nil {
		return fmt.Errorf("load seller work_structure: %w", err)
	}
	if sellerStructureID == nil || *sellerStructureID == "" {
		return fmt.Errorf("seller %s has no work_structure", candidate.DisplayName)
	}

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Persist trip state. Capture buyer's current position as the
	// home coords for the return leg — they may have wandered, but
	// "where they were when they left" is the right return target.
	if _, err := tx.Exec(ctx,
		`INSERT INTO actor_restock_in_progress
		   (actor_id, seller_id, item_kind, seller_structure_id, home_x, home_y, phase)
		 VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5, $6, 'outbound')`,
		buyerID, candidate.ActorID, itemKind, *sellerStructureID, buyerHomeX, buyerHomeY,
	); err != nil {
		return fmt.Errorf("insert trip: %w", err)
	}

	// Stamp break_until + agent_override_until so the customer-facing
	// closed-shop semantics apply during the trip and the agent
	// scheduler doesn't fire LLM ticks competing with the walk.
	breakUntil := time.Now().UTC().Add(tripBreakDuration)
	if _, err := tx.Exec(ctx,
		`UPDATE actor
		    SET break_until = $2,
		        agent_override_until = $2
		  WHERE id = $1::uuid`,
		buyerID, breakUntil,
	); err != nil {
		return fmt.Errorf("set break: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Dispatch the walk. Outside the tx because startNPCWalk has its
	// own DB calls and pathfinding. Failure here leaves the trip row
	// + break stamped — the stale sweep clears them after 30 min.
	walkResult, walkErr := app.startNPCWalk(ctx, buyerID, candidate.WalkTargetX, candidate.WalkTargetY, 0)
	if walkErr != nil {
		log.Printf("buy_walker: startNPCWalk for %s -> %s: %v",
			buyerID, candidate.DisplayName, walkErr)
		// Roll back the trip state since the walk never started.
		app.cancelBuyTrip(ctx, buyerID, "walk-start failed")
		return fmt.Errorf("startNPCWalk: %w", walkErr)
	}
	app.markWalkTargetStructure(buyerID, *sellerStructureID)

	log.Printf("buy_walker: trip start actor=%s item=%s seller=%s (%.0f,%.0f), walk=%.0fs",
		buyerID, itemKind, candidate.DisplayName,
		candidate.WalkTargetX, candidate.WalkTargetY, walkResult.DurationSec)

	app.Hub.Broadcast(WorldEvent{
		Type: "actor_restock_started",
		Data: map[string]any{
			"actor_id":    buyerID,
			"seller_id":   candidate.ActorID,
			"seller_name": candidate.DisplayName,
			"item_kind":   itemKind,
		},
	})

	return nil
}

// handleBuyWalkerArrival is called from applyArrivalSideEffects on
// every NPC arrival. Returns true if it handled the arrival as part
// of an in-progress trip (so the caller can skip other arrival
// behaviors).
//
// Two cases:
//   * Outbound arrival at the expected seller structure: do the
//     transaction, flip phase to 'inbound', dispatch return walk.
//   * Inbound arrival: clear the trip row and the break stamps.
//
// Anything else is a no-op (returns false).
func (app *App) handleBuyWalkerArrival(ctx context.Context, actorID string, arrivedStructureID string) bool {
	var (
		sellerID            string
		itemKind            string
		sellerStructureID   string
		homeX, homeY        float64
		phase               string
	)
	err := app.DB.QueryRow(ctx,
		`SELECT seller_id::text, item_kind, seller_structure_id::text, home_x, home_y, phase
		   FROM actor_restock_in_progress WHERE actor_id = $1::uuid`,
		actorID,
	).Scan(&sellerID, &itemKind, &sellerStructureID, &homeX, &homeY, &phase)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		log.Printf("buy_walker: arrival check %s: %v", actorID, err)
		return false
	}

	switch phase {
	case "outbound":
		if arrivedStructureID != sellerStructureID {
			// Walked somewhere else (interrupted? wrong path?). Just
			// abandon the trip — the stale sweep would catch it later.
			app.cancelBuyTrip(ctx, actorID, "outbound arrived at unexpected structure")
			return true
		}
		app.completeOutboundLeg(ctx, actorID, sellerID, itemKind, homeX, homeY)
		return true
	case "inbound":
		app.cancelBuyTrip(ctx, actorID, "inbound arrival")
		return true
	}
	return false
}

// completeOutboundLeg attempts the transfer (or no_stock fallback),
// then dispatches the return walk and flips phase to 'inbound'.
func (app *App) completeOutboundLeg(
	ctx context.Context,
	buyerID, sellerID, itemKind string,
	homeX, homeY float64,
) {
	transferOK := app.tryDeterministicBuy(ctx, buyerID, sellerID, itemKind)
	if !transferOK {
		// Empty seller — stamp the no_stock row and apply backoff
		// on the buyer side via actor_buy_state.
		if _, err := app.recordNoStockAttempt(ctx, buyerID, sellerID, itemKind, 1, nil, nil); err != nil {
			log.Printf("buy_walker: record no_stock %s<-%s/%s: %v", buyerID, sellerID, itemKind, err)
		}
		if err := app.stampBuyFailure(ctx, buyerID, itemKind, "seller had no stock on arrival"); err != nil {
			log.Printf("buy_walker: stamp failure %s/%s: %v", buyerID, itemKind, err)
		}
	} else {
		if err := app.stampBuySuccess(ctx, buyerID, sellerID, itemKind); err != nil {
			log.Printf("buy_walker: stamp success %s/%s: %v", buyerID, itemKind, err)
		}
	}

	// Update phase + dispatch return walk.
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor_restock_in_progress SET phase = 'inbound' WHERE actor_id = $1::uuid`,
		buyerID,
	); err != nil {
		log.Printf("buy_walker: flip to inbound %s: %v", buyerID, err)
		return
	}

	if _, err := app.startNPCWalk(ctx, buyerID, homeX, homeY, 0); err != nil {
		log.Printf("buy_walker: return walk for %s: %v", buyerID, err)
		// Trip stays; stale sweep will clear it.
	}
}

// tryDeterministicBuy attempts a single-unit transfer at the
// item's deterministic price. Locks both inventories + the seller's
// stock row. Returns true if the transfer happened.
func (app *App) tryDeterministicBuy(
	ctx context.Context,
	buyerID, sellerID, itemKind string,
) bool {
	price := priceFor(itemKind)

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		log.Printf("buy_walker: begin transfer: %v", err)
		return false
	}
	defer tx.Rollback(ctx)

	// Lock seller's inventory row for this item.
	var sellerQty int
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(quantity, 0) FROM actor_inventory
		  WHERE actor_id = $1::uuid AND item_kind = $2 FOR UPDATE`,
		sellerID, itemKind,
	).Scan(&sellerQty)
	if errors.Is(err, pgx.ErrNoRows) || sellerQty <= 0 {
		return false
	}
	if err != nil {
		log.Printf("buy_walker: lock seller inv: %v", err)
		return false
	}

	// Decrement seller stock.
	if _, err := tx.Exec(ctx,
		`UPDATE actor_inventory SET quantity = quantity - 1
		  WHERE actor_id = $1::uuid AND item_kind = $2`,
		sellerID, itemKind,
	); err != nil {
		log.Printf("buy_walker: decrement seller inv: %v", err)
		return false
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM actor_inventory
		  WHERE actor_id = $1::uuid AND item_kind = $2 AND quantity <= 0`,
		sellerID, itemKind,
	); err != nil {
		log.Printf("buy_walker: cleanup zero seller inv: %v", err)
		return false
	}

	// Credit buyer inventory.
	if _, err := tx.Exec(ctx,
		`INSERT INTO actor_inventory (actor_id, item_kind, quantity)
		 VALUES ($1::uuid, $2, 1)
		 ON CONFLICT (actor_id, item_kind)
		 DO UPDATE SET quantity = actor_inventory.quantity + 1`,
		buyerID, itemKind,
	); err != nil {
		log.Printf("buy_walker: credit buyer inv: %v", err)
		return false
	}

	// Coin transfer (best-effort — buyer may not have funds; if
	// short, transfer goes through anyway. Tighten later if needed).
	if _, err := tx.Exec(ctx,
		`UPDATE actor SET coins = coins - $2 WHERE id = $1::uuid`,
		buyerID, price,
	); err != nil {
		log.Printf("buy_walker: deduct buyer coins: %v", err)
		return false
	}
	if _, err := tx.Exec(ctx,
		`UPDATE actor SET coins = coins + $2 WHERE id = $1::uuid`,
		sellerID, price,
	); err != nil {
		log.Printf("buy_walker: credit seller coins: %v", err)
		return false
	}

	// Pay_ledger row records the transaction.
	if _, err := tx.Exec(ctx,
		`INSERT INTO pay_ledger (
		    huddle_id, scene_id, buyer_id, seller_id,
		    item_kind, qty, offered_amount, quoted_unit_amount,
		    consume_now, state, fulfillment_status, ready_by,
		    created_at, resolved_at
		 ) VALUES (
		    NULL, NULL, $1::uuid, $2::uuid,
		    $3, 1, $4, $4,
		    false, 'accepted', 'delivered', CURRENT_DATE,
		    NOW(), NOW()
		 )`,
		buyerID, sellerID, itemKind, price,
	); err != nil {
		log.Printf("buy_walker: insert pay_ledger: %v", err)
		return false
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("buy_walker: commit transfer: %v", err)
		return false
	}

	app.Hub.Broadcast(WorldEvent{
		Type: "actor_inventory_changed",
		Data: map[string]any{
			"actor_id":  buyerID,
			"item_kind": itemKind,
		},
	})
	app.Hub.Broadcast(WorldEvent{
		Type: "actor_inventory_changed",
		Data: map[string]any{
			"actor_id":  sellerID,
			"item_kind": itemKind,
		},
	})
	log.Printf("buy_walker: transfer ok actor=%s item=%s seller=%s price=%d",
		buyerID, itemKind, sellerID, price)
	return true
}

// cancelBuyTrip clears the trip state + the break stamps. Called on
// inbound arrival (normal completion) and on abnormal terminations.
func (app *App) cancelBuyTrip(ctx context.Context, buyerID, reason string) {
	if _, err := app.DB.Exec(ctx,
		`DELETE FROM actor_restock_in_progress WHERE actor_id = $1::uuid`,
		buyerID,
	); err != nil {
		log.Printf("buy_walker: clear trip %s: %v", buyerID, err)
	}
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor SET break_until = NULL, agent_override_until = NULL
		  WHERE id = $1::uuid`,
		buyerID,
	); err != nil {
		log.Printf("buy_walker: clear break %s: %v", buyerID, err)
	}
	log.Printf("buy_walker: trip end actor=%s reason=%s", buyerID, reason)
}

// stampBuySuccess writes actor_buy_state on a successful purchase so
// the resolver's tiebreak chain has a sticky preference.
func (app *App) stampBuySuccess(ctx context.Context, buyerID, sellerID, itemKind string) error {
	_, err := app.DB.Exec(ctx,
		`INSERT INTO actor_buy_state
		   (actor_id, item_kind, last_bought_from, last_buy_succeeded_at)
		 VALUES ($1::uuid, $2, $3::uuid, NOW())
		 ON CONFLICT (actor_id, item_kind) DO UPDATE
		    SET last_bought_from = EXCLUDED.last_bought_from,
		        last_buy_succeeded_at = EXCLUDED.last_buy_succeeded_at,
		        last_buy_failed_at = NULL,
		        last_buy_failed_reason = NULL`,
		buyerID, itemKind, sellerID,
	)
	return err
}

// stampBuyFailure writes actor_buy_state on a no_stock arrival so
// the backoff applies and the seller's customers can hear the
// failure relayed in speak.
func (app *App) stampBuyFailure(ctx context.Context, buyerID, itemKind, reason string) error {
	_, err := app.DB.Exec(ctx,
		`INSERT INTO actor_buy_state
		   (actor_id, item_kind, last_buy_failed_at, last_buy_failed_reason)
		 VALUES ($1::uuid, $2, NOW(), $3)
		 ON CONFLICT (actor_id, item_kind) DO UPDATE
		    SET last_buy_failed_at = EXCLUDED.last_buy_failed_at,
		        last_buy_failed_reason = EXCLUDED.last_buy_failed_reason`,
		buyerID, itemKind, reason,
	)
	return err
}
