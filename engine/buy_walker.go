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
	"database/sql"
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

// buyResult tells completeOutboundLeg why a buy attempt did not
// transfer goods — so a "seller had no stock" outcome (worth taking
// an order for) is not conflated with a transient DB / lock / funds
// failure (which should not create a phantom order).
type buyResult int

const (
	buyTransferred buyResult = iota
	buyNoStock                // seller has zero inventory for the item — take an order
	buyFailed                 // DB error, lock failure, etc — do not create an order
)

// buyerCap returns the unified personal-carry cap (ZBBS-HOME-249)
// the buyer's restock policy declares for itemKind. Returns 0 when
// the buyer has no `buy` entry for the item.
//
// Note: this does NOT read inventory or compute headroom — actual
// remaining-capacity is computed inside tryDeterministicBuy under
// row locks so concurrent inventory mutations can't push the
// buyer over cap.
func (app *App) buyerCap(ctx context.Context, buyerID, itemKind string) int {
	policy, err := app.loadActorRestockPolicy(ctx, buyerID)
	if err != nil || policy == nil {
		return 0
	}
	for _, entry := range policy.Restock {
		if entry.Source == RestockSourceBuy && entry.Item == itemKind {
			return entry.Cap()
		}
	}
	return 0
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

	// Return walk target = buyer's CURRENT pixel (which is inside
	// their work_structure by virtue of the gate above). Walking back
	// to the exact pre-trip spot puts them at their stand_offset /
	// behind the counter / wherever they were. The cancelBuyTrip
	// inbound handler restores inside_structure_id via footprint check.
	//
	// Defensive: confirm the buyer's pre-trip pixel actually sits
	// inside the work_structure's asset footprint. inside_structure_id
	// could in theory be set without footprint validation (legacy
	// rows, manual placement, etc.); if it is, the inbound restore
	// would fail and the buyer would come back stuck outside the
	// stall — same shape as the HOME-244 bug we just patched.
	var preTripInsideFootprint bool
	if err := app.DB.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1
		    FROM village_object vo
		    JOIN asset s ON s.id = vo.asset_id
		   WHERE vo.id = $1::uuid
		     AND $2::float8 BETWEEN vo.x - s.footprint_left  * 32 AND vo.x + s.footprint_right  * 32
		     AND $3::float8 BETWEEN vo.y - s.footprint_top   * 32 AND vo.y + s.footprint_bottom * 32
		)`,
		*workStructureIDStr, buyerX, buyerY,
	).Scan(&preTripInsideFootprint); err != nil {
		log.Printf("buy_walker: footprint check %s: %v", actorID, err)
		return
	}
	if !preTripInsideFootprint {
		log.Printf("buy_walker: skip trip — buyer %s position (%.0f,%.0f) outside work_structure footprint %s",
			actorID, buyerX, buyerY, *workStructureIDStr)
		return
	}
	homeReturnX := buyerX
	homeReturnY := buyerY

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

		// Found one. Start the trip with pre-trip coords as the
		// return-leg destination.
		if err := app.startBuyTrip(ctx, actorID, entry.Item, decision.Candidate,
			homeReturnX, homeReturnY, *workStructureIDStr); err != nil {
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
		// ZBBS-HOME-276: fire engine-authored greet from the seller
		// when they're a businessowner at-post-off-break. The
		// buy_walker bypasses applyArrivalSideEffects' joinOrCreateHuddle
		// path (the buyer never lands in a huddle on a goods-fetch
		// trip), so the standard HOME-273 greet hook on
		// joinOrCreateHuddle never sees this arrival. This call
		// brings the same hospitality cadence to buy_walker entries.
		//
		// Attribution: speak event lands in the seller's
		// current_huddle_id (the keeper IS at-post and has a huddle
		// for their structure). Buyer doesn't have a huddle; that's
		// fine — the WS broadcast is structure-scoped, so the bubble
		// reaches anyone in the room.
		app.fireBuyWalkerGreet(ctx, actorID, sellerID, sellerStructureID)
		app.completeOutboundLeg(ctx, actorID, sellerID, itemKind, homeX, homeY)
		return true
	case "inbound":
		app.cancelBuyTrip(ctx, actorID, "inbound arrival")
		return true
	}
	return false
}

// fireBuyWalkerGreet is the buy_walker-side hook for ZBBS-HOME-276.
// Pulls the seller's current_huddle_id (if any) and delegates to
// maybeFireGreetOnEntry, which runs the standard gate stack (entering
// actor isn't a businessowner, seller is at-post-not-on-break-not-asleep,
// cooldown row absent/expired). No-op when the seller doesn't have a
// huddle (rare — the seller is inside their own structure on every
// successful buy_walker outbound arrival, so joinOrCreateHuddle has
// already minted one at their entry).
//
// Kept tiny so the buy_walker call site reads cleanly; the predicate
// gates live in businessowner.go where the rest of the businessowner
// logic is.
func (app *App) fireBuyWalkerGreet(ctx context.Context, buyerID, sellerID, sellerStructureID string) {
	var sellerHuddleID sql.NullString
	_ = app.DB.QueryRow(ctx,
		`SELECT current_huddle_id::text FROM actor WHERE id = $1`,
		sellerID,
	).Scan(&sellerHuddleID)
	if !sellerHuddleID.Valid || sellerHuddleID.String == "" {
		return
	}
	app.maybeFireGreetOnEntry(ctx, buyerID, sellerStructureID, sellerHuddleID.String)
}

// completeOutboundLeg attempts the transaction at the seller's stall.
// Two paths:
//   * Has stock → tryDeterministicBuy at the seller's tier price +
//     dialogue + stamp success. Cycle filter applies via pay_ledger.
//   * Empty → record an ORDER (pay_ledger row state='accepted',
//     fulfillment_status='pending', no transfer yet) + dialogue +
//     stamp backoff. The new fulfill_orders_walker will dispatch the
//     seller to deliver once their own restock fills the gap.
//
// Either way: phase → 'inbound', dispatch return walk.
func (app *App) completeOutboundLeg(
	ctx context.Context,
	buyerID, sellerID, itemKind string,
	homeX, homeY float64,
) {
	// Pull the buyer's declared cap from policy. Actual remaining
	// capacity is computed inside tryDeterministicBuy under row
	// locks — passing cap (not headroom) lets the tx see current
	// buyer inventory while serialized against concurrent buys.
	cap := app.buyerCap(ctx, buyerID, itemKind)
	if cap <= 0 {
		// Buyer has no `buy` entry for this item — shouldn't have
		// dispatched. Skip the transfer attempt; walk home.
		log.Printf("buy_walker: no buy cap for actor=%s item=%s, skipping transfer",
			buyerID, itemKind)
	} else if atWork, err := app.sellerIsActivelyAtWork(ctx, sellerID); err != nil {
		// Lookup failure — fail closed (don't transact). Buyer walks
		// home without inventory; backoff applies via stampBuyFailure.
		log.Printf("buy_walker: sellerIsActivelyAtWork %s: %v", sellerID, err)
		if err := app.stampBuyFailure(ctx, buyerID, itemKind, "seller presence check failed"); err != nil {
			log.Printf("buy_walker: stamp failure %s/%s: %v", buyerID, itemKind, err)
		}
	} else if !atWork {
		// ZBBS-HOME-259: seller went home (or took a break, or fell
		// asleep) between dispatch and arrival. Don't transact — the
		// previous behavior had the buyer transfer goods out of the
		// seller's persistent inventory while the engine spoke
		// "Here's your X" as the absent seller. Walk home with
		// backoff; next pass will pick a different seller or wait.
		log.Printf("buy_walker: seller %s not actively at work on arrival — aborting", sellerID)
		if err := app.stampBuyFailure(ctx, buyerID, itemKind, "seller not at their stall on arrival"); err != nil {
			log.Printf("buy_walker: stamp failure %s/%s: %v", buyerID, itemKind, err)
		}
	} else {
		result, qty := app.tryDeterministicBuy(ctx, buyerID, sellerID, itemKind, cap)
		switch result {
		case buyTransferred:
			if err := app.stampBuySuccess(ctx, buyerID, sellerID, itemKind); err != nil {
				log.Printf("buy_walker: stamp success %s/%s: %v", buyerID, itemKind, err)
			}
			_ = qty
		case buyNoStock:
			// Seller has nothing. Take an order to cap; fulfill_walker
			// delivers later. Order qty is the buyer's outstanding need
			// at the moment of order, computed against the current
			// (post-walk) inventory. We do an extra read here rather
			// than threading qty out of tryDeterministicBuy because
			// the locked transaction was rolled back when it returned
			// buyNoStock.
			orderQty := cap
			var current int
			if err := app.DB.QueryRow(ctx,
				`SELECT COALESCE(quantity, 0) FROM actor_inventory
				  WHERE actor_id = $1::uuid AND item_kind = $2`,
				buyerID, itemKind,
			).Scan(&current); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				log.Printf("buy_walker: read buyer current for order qty: %v", err)
			}
			orderQty = cap - current
			if orderQty <= 0 {
				// Already at cap somehow — skip order recording.
				log.Printf("buy_walker: skip order, buyer at cap actor=%s item=%s",
					buyerID, itemKind)
			} else {
				if err := app.recordOrderTaking(ctx, buyerID, sellerID, itemKind, orderQty); err != nil {
					log.Printf("buy_walker: record order %s<-%s/%s: %v", buyerID, sellerID, itemKind, err)
				}
				if err := app.stampBuyFailure(ctx, buyerID, itemKind, "seller had no stock — order taken"); err != nil {
					log.Printf("buy_walker: stamp failure %s/%s: %v", buyerID, itemKind, err)
				}
			}
		case buyFailed:
			// Transient/operational error inside the transfer attempt
			// (DB issue, insufficient funds, buyer already at cap,
			// lock failure). Already logged. Do NOT create an order —
			// we don't know whether the seller had stock. Buyer walks
			// home empty; backoff applies via the existing cycle
			// filter on the next pass.
			if err := app.stampBuyFailure(ctx, buyerID, itemKind, "transfer failed (transient)"); err != nil {
				log.Printf("buy_walker: stamp failure %s/%s: %v", buyerID, itemKind, err)
			}
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

// tryDeterministicBuy attempts a multi-unit transfer up to the
// buyer's policy-declared cap at the seller's tier price. Actual
// qty = min(cap-currentBuyerQty, sellerStock, buyer-can-afford).
// The seller's role determines wholesale vs retail (producer
// charges wholesale; merchant charges retail).
//
// Locks taken inside the tx:
//   - actor row for the buyer (serializes concurrent buys for this
//     buyer + reads coins). Required so the (cap - current_qty)
//     check below sees a stable buyer state.
//   - actor_inventory row for the seller's stock (serializes
//     concurrent sells from this seller for this item).
//
// The buyer actor lock also covers the "no inventory row yet"
// case where FOR UPDATE on actor_inventory wouldn't lock anything
// — concurrent INSERT ON CONFLICT credits can't race in.
//
// Returns (result, qtyTransferred):
//   - buyTransferred, qty > 0 when goods + coins moved and the
//     pay_ledger row was written.
//   - buyNoStock, 0 when the seller's inventory row is missing or
//     zero for this item. Caller may take an order.
//   - buyFailed, 0 for any DB error, lock failure, buyer at-or-over
//     cap, or insufficient funds. Caller must NOT take an order —
//     the failure is either transient or "buyer already covered."
func (app *App) tryDeterministicBuy(
	ctx context.Context,
	buyerID, sellerID, itemKind string,
	cap int,
) (buyResult, int) {
	if cap <= 0 {
		return buyFailed, 0
	}
	price := app.priceForSeller(ctx, sellerID, itemKind)
	if price <= 0 {
		// Misconfigured price — treat as transient failure rather
		// than silently giving away free goods or recording an order.
		log.Printf("buy_walker: zero price for seller=%s item=%s", sellerID, itemKind)
		return buyFailed, 0
	}

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		log.Printf("buy_walker: begin transfer: %v", err)
		return buyFailed, 0
	}
	defer tx.Rollback(ctx)

	// Lock the buyer's actor row first — this serializes concurrent
	// buys for the same buyer and lets us read coins + compute
	// remaining headroom under a stable lock even when the buyer
	// has no actor_inventory row yet for the item.
	var buyerCoins int
	if err := tx.QueryRow(ctx,
		`SELECT coins FROM actor WHERE id = $1::uuid FOR UPDATE`,
		buyerID,
	).Scan(&buyerCoins); err != nil {
		log.Printf("buy_walker: lock buyer actor: %v", err)
		return buyFailed, 0
	}

	// Lock the seller's stock row.
	var sellerQty int
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(quantity, 0) FROM actor_inventory
		  WHERE actor_id = $1::uuid AND item_kind = $2 FOR UPDATE`,
		sellerID, itemKind,
	).Scan(&sellerQty)
	if errors.Is(err, pgx.ErrNoRows) || sellerQty <= 0 {
		return buyNoStock, 0
	}
	if err != nil {
		log.Printf("buy_walker: lock seller inv: %v", err)
		return buyFailed, 0
	}

	// Lock the buyer's inventory row itself (when it exists) so no
	// concurrent UPDATE / ON CONFLICT DO UPDATE on the same row can
	// land between this read and our credit below. ErrNoRows = 0
	// quantity; the buyer actor lock taken above covers the
	// no-row-yet case against competing buy paths that also take
	// the actor lock. (Non-buy credit paths — deliveries, gifts,
	// produce_tick on a different item — can still touch the row
	// without taking the actor lock; closing that gap fully would
	// require routing all actor_inventory mutations through a
	// helper that takes a common serialization lock, which is out
	// of scope here.)
	var buyerCurrent int
	err = tx.QueryRow(ctx,
		`SELECT quantity FROM actor_inventory
		  WHERE actor_id = $1::uuid AND item_kind = $2 FOR UPDATE`,
		buyerID, itemKind,
	).Scan(&buyerCurrent)
	if errors.Is(err, pgx.ErrNoRows) {
		buyerCurrent = 0
	} else if err != nil {
		log.Printf("buy_walker: lock buyer inv: %v", err)
		return buyFailed, 0
	}
	remaining := cap - buyerCurrent
	if remaining <= 0 {
		// Buyer is already at or over the cap (raced with a
		// delivery / gift / produce path between dispatch and
		// arrival). Don't transfer; don't take an order.
		log.Printf("buy_walker: buyer %s at cap for %s (current=%d cap=%d) — skipping",
			buyerID, itemKind, buyerCurrent, cap)
		return buyFailed, 0
	}

	qty := remaining
	if sellerQty < qty {
		qty = sellerQty
	}
	affordable := buyerCoins / price
	if affordable <= 0 {
		log.Printf("buy_walker: buyer %s can't afford any %s (coins=%d, unit=%d)",
			buyerID, itemKind, buyerCoins, price)
		return buyFailed, 0
	}
	if affordable < qty {
		qty = affordable
	}
	total := price * qty

	// Coins first — conditional on the buyer being able to afford
	// the total. RowsAffected != 1 means a race shaved the balance
	// below the threshold since the read above.
	debit, err := tx.Exec(ctx,
		`UPDATE actor SET coins = coins - $2
		  WHERE id = $1::uuid AND coins >= $2`,
		buyerID, total,
	)
	if err != nil {
		log.Printf("buy_walker: deduct buyer coins: %v", err)
		return buyFailed, 0
	}
	if debit.RowsAffected() != 1 {
		log.Printf("buy_walker: buyer %s short of coins (need %d for %d %s)",
			buyerID, total, qty, itemKind)
		return buyFailed, 0
	}
	if _, err := tx.Exec(ctx,
		`UPDATE actor SET coins = coins + $2 WHERE id = $1::uuid`,
		sellerID, total,
	); err != nil {
		log.Printf("buy_walker: credit seller coins: %v", err)
		return buyFailed, 0
	}

	// Decrement seller stock by qty. ZBBS-HOME-258: DELETE-then-UPDATE
	// so buying out the seller's full stock doesn't trip
	// actor_inventory's CHECK (quantity > 0). Seller availability is
	// validated upstream of this commit, so qty <= existing quantity.
	if _, err := tx.Exec(ctx,
		`DELETE FROM actor_inventory
		  WHERE actor_id = $1::uuid AND item_kind = $2 AND quantity <= $3`,
		sellerID, itemKind, qty,
	); err != nil {
		log.Printf("buy_walker: cleanup zero seller inv: %v", err)
		return buyFailed, 0
	}
	if _, err := tx.Exec(ctx,
		`UPDATE actor_inventory SET quantity = quantity - $3
		  WHERE actor_id = $1::uuid AND item_kind = $2`,
		sellerID, itemKind, qty,
	); err != nil {
		log.Printf("buy_walker: decrement seller inv: %v", err)
		return buyFailed, 0
	}

	// Credit buyer inventory by qty.
	if _, err := tx.Exec(ctx,
		`INSERT INTO actor_inventory (actor_id, item_kind, quantity)
		 VALUES ($1::uuid, $2, $3)
		 ON CONFLICT (actor_id, item_kind)
		 DO UPDATE SET quantity = actor_inventory.quantity + EXCLUDED.quantity`,
		buyerID, itemKind, qty,
	); err != nil {
		log.Printf("buy_walker: credit buyer inv: %v", err)
		return buyFailed, 0
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
		    $3, $4, $5, $6,
		    false, 'accepted', 'delivered', CURRENT_DATE,
		    NOW(), NOW()
		 )`,
		buyerID, sellerID, itemKind, qty, total, price,
	); err != nil {
		log.Printf("buy_walker: insert pay_ledger: %v", err)
		return buyFailed, 0
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("buy_walker: commit transfer: %v", err)
		return buyFailed, 0
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

	// Visible dialogue beat — seller speaks to acknowledge the sale.
	var buyerName string
	_ = app.DB.QueryRow(ctx,
		`SELECT display_name FROM actor WHERE id = $1::uuid`, buyerID,
	).Scan(&buyerName)
	if buyerName != "" {
		var text string
		if qty == 1 {
			text = fmt.Sprintf("Here's your %s, %s. That'll be %d coin%s.",
				itemKind, buyerName, total, pluralCoins(total))
		} else {
			text = fmt.Sprintf("Here's your %d %s, %s. That'll be %d coin%s.",
				qty, itemKind, buyerName, total, pluralCoins(total))
		}
		app.broadcastSellerSpoke(ctx, sellerID, text, []string{itemKind}, price)
	}

	log.Printf("buy_walker: transfer ok actor=%s item=%s qty=%d seller=%s unit_price=%d total=%d",
		buyerID, itemKind, qty, sellerID, price, total)
	return buyTransferred, qty
}

func pluralCoins(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// cancelBuyTrip clears the trip state + the break stamps. Called on
// inbound arrival (normal completion) and on abnormal terminations.
//
// Also restores inside_structure_id to work_structure_id when the
// buyer is at (or very near) their work_structure. The return walk
// targets the work_structure walk-target, but the existing arrival
// pipeline doesn't always flip inside=true for non-owner moves to
// stalls. Without this, the buyer returns to "outside" the stall
// and produce_tick + the next buy dispatch both gate them out.
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
	// Footprint-based inside_structure_id restore. The buyer must be
	// physically within the asset's footprint, not just nearby. Avoids
	// the bad case where a buyer at the visitor loiter slot OUTSIDE
	// the building gets flipped to inside.
	//
	// ZBBS-HOME-259: also writes inside_room_id = common in the same
	// UPDATE. Pre-fix this restore set inside + inside_structure_id
	// but bypassed setNPCInside (which would have paired the room
	// column). The result was the cascade-receiver corruption
	// HOME-256 chased — buyer keepers had NULL inside_room_id while
	// inside their own structure, silently missing PC-arrival
	// cascades. Pair the room here so the row stays consistent for
	// any cascade-receiver query relying on a non-NULL room.
	if _, err := app.DB.Exec(ctx, `
		UPDATE actor a
		   SET inside_structure_id = a.work_structure_id,
		       inside_room_id = (
		         SELECT id FROM structure_room
		          WHERE structure_id = a.work_structure_id AND kind = 'common'
		          LIMIT 1
		       ),
		       inside = TRUE
		  FROM village_object vo
		  JOIN asset s ON s.id = vo.asset_id
		 WHERE a.id = $1::uuid
		   AND a.work_structure_id IS NOT NULL
		   AND vo.id = a.work_structure_id
		   AND a.current_x BETWEEN vo.x - s.footprint_left * 32 AND vo.x + s.footprint_right * 32
		   AND a.current_y BETWEEN vo.y - s.footprint_top  * 32 AND vo.y + s.footprint_bottom * 32
	`, buyerID); err != nil {
		log.Printf("buy_walker: restore inside %s: %v", buyerID, err)
	}
	log.Printf("buy_walker: trip end actor=%s reason=%s", buyerID, reason)
}

// priceForSeller returns the price the seller charges for the item.
// Rule: if the seller has a `buy` restock entry for this item they
// are a reseller of it → retail price. Otherwise → wholesale.
// (A producer with no `buy` entry falls into wholesale; an actor
// with no restock config at all also defaults to wholesale, which
// keeps engine-driven trades moving rather than stalling on missing
// role data.) Falls back to 1 when the recipe has no prices.
func (app *App) priceForSeller(ctx context.Context, sellerID, itemKind string) int {
	recipe, err := app.loadItemRecipe(ctx, itemKind)
	if err != nil || recipe == nil {
		return 1
	}
	var hasBuy bool
	_ = app.DB.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM actor_attribute
		   WHERE actor_id = $1::uuid
		     AND jsonb_path_exists(params,
		           '$.restock[*] ? (@.source == "buy" && @.item == $i)',
		           jsonb_build_object('i', $2::text))
		)`, sellerID, itemKind,
	).Scan(&hasBuy)
	if hasBuy && recipe.RetailPrice > 0 {
		return recipe.RetailPrice
	}
	if recipe.WholesalePrice > 0 {
		return recipe.WholesalePrice
	}
	if recipe.RetailPrice > 0 {
		return recipe.RetailPrice
	}
	return 1
}

// recordOrderTaking inserts a pay_ledger row with state='accepted'
// and fulfillment_status='pending' to record an order the seller
// owes the buyer. The fulfill_orders walker (fulfill_walker.go)
// picks this up later once the seller has the goods, walks to the
// buyer, and completes the transfer at the door.
//
// Price is locked in NOW at the seller's tier — protects against
// later price changes mid-fulfillment.
//
// The partial unique index idx_pay_ledger_pending_order_once limits
// a (buyer, seller, item) to a single outstanding pending order:
// repeat no-stock arrivals do not stack up duplicate orders, and
// the seller's dialogue still narrates the empty-stall beat. Once
// the existing order flips to 'delivered' the predicate no longer
// matches and a fresh order can be recorded next time.
func (app *App) recordOrderTaking(
	ctx context.Context,
	buyerID, sellerID, itemKind string,
	qty int,
) error {
	if qty <= 0 {
		return nil
	}
	price := app.priceForSeller(ctx, sellerID, itemKind)
	total := price * qty
	tag, err := app.DB.Exec(ctx,
		`INSERT INTO pay_ledger (
		    huddle_id, scene_id, buyer_id, seller_id,
		    item_kind, qty, offered_amount, quoted_unit_amount,
		    consume_now, state, fulfillment_status, ready_by,
		    created_at, resolved_at
		 ) VALUES (
		    NULL, NULL, $1::uuid, $2::uuid,
		    $3, $4, $5, $6,
		    false, 'accepted', 'pending', CURRENT_DATE,
		    NOW(), NOW()
		 )
		 ON CONFLICT (buyer_id, seller_id, item_kind)
		   WHERE state = 'accepted' AND fulfillment_status = 'pending'
		   DO NOTHING`,
		buyerID, sellerID, itemKind, qty, total, price,
	)
	if err != nil {
		return fmt.Errorf("insert order pay_ledger: %w", err)
	}
	inserted := tag.RowsAffected() == 1
	if inserted {
		var text string
		if qty == 1 {
			text = fmt.Sprintf("I'm out of %s right now, but I'll have some for you when I can. I'll bring it 'round.", itemKind)
		} else {
			text = fmt.Sprintf("I'm out of %s right now. I'll bring %d 'round when I can.", itemKind, qty)
		}
		app.broadcastSellerSpoke(ctx, sellerID, text, []string{itemKind}, 0)
	} else {
		// A pending order from this (buyer, seller, item) already
		// exists. Stay quiet — narrating again would be repetitive
		// and might prompt the buyer's perception to act on a fresh
		// promise that's actually just the same outstanding order.
		log.Printf("buy_walker: order already pending buyer=%s seller=%s item=%s",
			buyerID, sellerID, itemKind)
	}
	return nil
}

// broadcastSellerSpoke is the engine's "speak on behalf of NPC"
// helper. Composes an npc_spoke event with sensible defaults
// (speaker name, current structure scope) so the talk panel renders.
func (app *App) broadcastSellerSpoke(
	ctx context.Context,
	speakerID, text string,
	mentions []string,
	price int,
) {
	var (
		speakerName string
		structureID sql.NullString
	)
	_ = app.DB.QueryRow(ctx,
		`SELECT display_name, inside_structure_id::text FROM actor WHERE id = $1::uuid`,
		speakerID,
	).Scan(&speakerName, &structureID)
	if speakerName == "" {
		return
	}
	spokeData := map[string]any{
		"actor_id":   speakerID,
		"actor_name": speakerName,
		"text":       text,
		"mentions":   mentions,
	}
	if price > 0 {
		spokeData["price"] = price
	}
	if structureID.Valid && structureID.String != "" {
		spokeData["structure_id"] = structureID.String
	}
	app.Hub.Broadcast(WorldEvent{Type: "npc_spoke", Data: spokeData})
	log.Printf("buy_walker: npc_spoke speaker=%s text=%q", speakerName, text)
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
