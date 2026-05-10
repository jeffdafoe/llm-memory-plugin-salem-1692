package main

// Produce tick (ZBBS-HOME-241) — per-minute handler that grows
// actor inventories per their restock policy `produce` entries.
//
// Mirrors object_refresh_tick.go's continuous-mode regen, but the
// "supply" is the actor's own inventory (not an object's
// available_quantity) and the rate comes from the recipe rather
// than the object_refresh row.
//
// Per-tick algorithm:
//
//   1. Load every item_recipe once. Cheap (~10 rows in v1).
//   2. List actors with at least one `produce` entry in their
//      restock policy. JSONB filter at the SQL layer; usually
//      single-digit row count.
//   3. For each such actor, in its own tx:
//        a. Lock the actor row (work_structure_id, inside_structure_id).
//           Skip if not at work.
//        b. For each produce entry:
//             - Look up the recipe (skip silently if missing).
//             - Lock or insert the matching actor_produce_state row.
//             - First observation: stamp anchor to now, no fill.
//             - Compute units owed since anchor (continuous regen
//               math, identical to object_refresh_tick.go).
//             - Cap at headroom (max - current_inventory).
//             - If recipe has inputs and the actor lacks ANY at the
//               required qty, skip this entry entirely (per design
//               decision #7: skip-if-any-input-short).
//             - Otherwise consume one of each input, add output_qty,
//               advance anchor by exact unit-second multiples.
//        c. Commit.
//
// Gating: produce only fires while the actor is inside their
// work_structure_id. (ZBBS-HOME-251 dropped the active-hours hour
// gate — the npc_scheduler walks workers home at end-of-shift, so
// "inside work structure" is now a sufficient proxy for "on shift."
// Sleeping actors have inside_structure_id set but produce_tick
// continues to skip them through the per-need ticker's sleep gate.)

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

func (app *App) dispatchProduceTick(ctx context.Context) {
	// Active hours on actor are stored in world-timezone hours
	// (America/New_York), not UTC. Use loadWorldConfig + time.Now().In(loc)
	// so produceTickGate compares apples to apples. Same pattern as
	// npc_scheduler.go::dispatchScheduledBehaviors.
	cfg, err := app.loadWorldConfig(ctx)
	if err != nil {
		log.Printf("produce_tick: load world config: %v", err)
		return
	}
	now := time.Now().In(cfg.Location)

	recipes, err := app.loadAllRecipes(ctx)
	if err != nil {
		log.Printf("produce_tick: load recipes: %v", err)
		return
	}
	if len(recipes) == 0 {
		return
	}

	actorIDs, err := app.listActorsWithRestockEntries(ctx, RestockSourceProduce)
	if err != nil {
		log.Printf("produce_tick: list actors: %v", err)
		return
	}
	if len(actorIDs) == 0 {
		return
	}

	for _, actorID := range actorIDs {
		policy, err := app.loadActorRestockPolicy(ctx, actorID)
		if err != nil {
			log.Printf("produce_tick: load policy for %s: %v", actorID, err)
			continue
		}
		app.runProduceTickForActor(ctx, actorID, policy, recipes, now)
	}
}

// runProduceTickForActor evaluates one actor's produce entries in a
// single transaction. Errors on individual entries log and continue;
// errors on the actor-row lock or commit roll back the whole actor's
// tick (try again next minute).
func (app *App) runProduceTickForActor(
	ctx context.Context,
	actorID string,
	policy *RestockPolicy,
	recipes map[string]*ItemRecipe,
	now time.Time,
) {
	tx, err := app.DB.Begin(ctx)
	if err != nil {
		log.Printf("produce_tick: begin tx for %s: %v", actorID, err)
		return
	}
	defer tx.Rollback(ctx)

	var (
		insideStructureID *string
		workStructureID   *string
	)
	err = tx.QueryRow(ctx,
		`SELECT inside_structure_id::text, work_structure_id::text
		   FROM actor WHERE id = $1::uuid FOR UPDATE`,
		actorID,
	).Scan(&insideStructureID, &workStructureID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("produce_tick: lock actor %s: %v", actorID, err)
		}
		return
	}

	if !produceTickGate(insideStructureID, workStructureID) {
		return
	}

	for _, entry := range policy.Restock {
		if entry.Source != RestockSourceProduce {
			continue
		}
		recipe, ok := recipes[entry.Item]
		if !ok {
			continue
		}
		if err := app.applyProduceEntry(ctx, tx, actorID, entry, recipe, now); err != nil {
			log.Printf("produce_tick: apply %s/%s: %v", actorID, entry.Item, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("produce_tick: commit %s: %v", actorID, err)
	}
}

// produceTickGate is the gate test: actor must be inside their
// work_structure. The npc_scheduler walks them home at end-of-shift,
// so "inside their work_structure_id" is now the truth of "currently
// on shift."
func produceTickGate(insideStructureID, workStructureID *string) bool {
	if workStructureID == nil || *workStructureID == "" {
		return false
	}
	if insideStructureID == nil || *insideStructureID != *workStructureID {
		return false
	}
	return true
}

// applyProduceEntry is the per-entry logic: lock or insert the
// actor_produce_state row, compute units owed since the anchor,
// check inputs, apply the production.
//
// Returns an error to roll the whole actor tx back. The caller can
// log and continue but the rollback ensures a partial state isn't
// committed.
func (app *App) applyProduceEntry(
	ctx context.Context,
	tx pgx.Tx,
	actorID string,
	entry RestockEntry,
	recipe *ItemRecipe,
	now time.Time,
) error {
	var lastProduced sql.NullTime
	err := tx.QueryRow(ctx,
		`SELECT last_produced_at FROM actor_produce_state
		  WHERE actor_id = $1::uuid AND item_kind = $2 FOR UPDATE`,
		actorID, entry.Item,
	).Scan(&lastProduced)
	if errors.Is(err, pgx.ErrNoRows) {
		// First observation — stamp anchor without filling, matches
		// object_refresh_tick.go first-pass behavior.
		_, err := tx.Exec(ctx,
			`INSERT INTO actor_produce_state (actor_id, item_kind, last_produced_at)
			 VALUES ($1::uuid, $2, $3)`,
			actorID, entry.Item, now,
		)
		return err
	}
	if err != nil {
		return err
	}
	if !lastProduced.Valid {
		// Same first-pass case via UPDATE path.
		_, err := tx.Exec(ctx,
			`UPDATE actor_produce_state SET last_produced_at = $3
			  WHERE actor_id = $1::uuid AND item_kind = $2`,
			actorID, entry.Item, now,
		)
		return err
	}

	// Continuous regen math. Same shape as
	// object_refresh_tick.go::dispatchObjectRefreshRegen continuous
	// branch. Seconds-per-unit from rate; advance anchor by exact
	// unit-second multiples so sub-unit residue carries forward.
	if recipe.RateQty <= 0 || recipe.RatePerHours <= 0 {
		return nil
	}
	periodSeconds := int64(recipe.RatePerHours) * 3600
	secondsPerUnit := periodSeconds / int64(recipe.RateQty)
	if secondsPerUnit <= 0 {
		return nil
	}
	elapsedSeconds := int64(now.Sub(lastProduced.Time).Seconds())
	if elapsedSeconds < secondsPerUnit {
		return nil
	}
	unitsOwed := elapsedSeconds / secondsPerUnit
	if unitsOwed <= 0 {
		return nil
	}

	// Cap at headroom against the entry's max. Read current inventory
	// (lock it so we can write back without race).
	var currentQty int
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(quantity, 0) FROM actor_inventory
		  WHERE actor_id = $1::uuid AND item_kind = $2 FOR UPDATE`,
		actorID, entry.Item,
	).Scan(&currentQty)
	if errors.Is(err, pgx.ErrNoRows) {
		currentQty = 0
	} else if err != nil {
		return err
	}

	if entry.Cap() > 0 && currentQty >= entry.Cap() {
		// Already at cap. Advance anchor to now to avoid back-credit
		// when consumption later opens headroom.
		_, err := tx.Exec(ctx,
			`UPDATE actor_produce_state SET last_produced_at = $3
			  WHERE actor_id = $1::uuid AND item_kind = $2`,
			actorID, entry.Item, now,
		)
		return err
	}

	// Recipe output_qty is the batch size; one batch is one execution.
	// We can run as many full executions as the time + headroom +
	// inputs allow. Convert unitsOwed (one per rate_qty period) into
	// executions: each execution mints output_qty units.
	executionsOwedByTime := unitsOwed / int64(recipe.OutputQty)
	if recipe.OutputQty <= 1 {
		executionsOwedByTime = unitsOwed
	}
	if executionsOwedByTime <= 0 {
		return nil
	}

	headroom := int64(0)
	if entry.Cap() > 0 {
		headroom = int64(entry.Cap() - currentQty)
	} else {
		// No cap configured — defensively bound to one execution per
		// tick to avoid runaway accumulation if max is missing.
		headroom = int64(recipe.OutputQty)
	}
	executionsByCap := headroom / int64(recipe.OutputQty)
	if recipe.OutputQty <= 1 {
		executionsByCap = headroom
	}

	executions := executionsOwedByTime
	if executionsByCap < executions {
		executions = executionsByCap
	}
	if executions <= 0 {
		return nil
	}

	// Recipe inputs check + lock. If ANY input is short for one
	// execution, skip the whole entry per design decision #7.
	if len(recipe.Inputs) > 0 {
		// Cap executions by input availability too — one execution
		// per "min available across inputs" still respects the
		// skip-on-any-shortage rule because executions hits zero
		// when any input has zero.
		for _, in := range recipe.Inputs {
			var have int
			err := tx.QueryRow(ctx,
				`SELECT COALESCE(quantity, 0) FROM actor_inventory
				  WHERE actor_id = $1::uuid AND item_kind = $2 FOR UPDATE`,
				actorID, in.Item,
			).Scan(&have)
			if errors.Is(err, pgx.ErrNoRows) {
				have = 0
			} else if err != nil {
				return err
			}
			if in.Qty <= 0 {
				continue
			}
			canExecute := int64(have / in.Qty)
			if canExecute < executions {
				executions = canExecute
			}
		}
		if executions <= 0 {
			// Skip-if-any-input-short. Don't advance anchor — input
			// arrival will let the next tick fire fresh accrual.
			return nil
		}

		// Consume inputs.
		for _, in := range recipe.Inputs {
			consume := in.Qty * int(executions)
			if _, err := tx.Exec(ctx,
				`UPDATE actor_inventory
				    SET quantity = quantity - $3
				  WHERE actor_id = $1::uuid AND item_kind = $2`,
				actorID, in.Item, consume,
			); err != nil {
				return err
			}
			// Clean up zero rows so perception text stays tidy
			// (matches the inventory module convention).
			if _, err := tx.Exec(ctx,
				`DELETE FROM actor_inventory
				  WHERE actor_id = $1::uuid AND item_kind = $2 AND quantity <= 0`,
				actorID, in.Item,
			); err != nil {
				return err
			}
		}
	}

	// Mint the output. INSERT or accumulate.
	totalProduced := int(executions) * recipe.OutputQty
	if _, err := tx.Exec(ctx,
		`INSERT INTO actor_inventory (actor_id, item_kind, quantity)
		 VALUES ($1::uuid, $2, $3)
		 ON CONFLICT (actor_id, item_kind)
		 DO UPDATE SET quantity = actor_inventory.quantity + EXCLUDED.quantity`,
		actorID, entry.Item, totalProduced,
	); err != nil {
		return err
	}

	// Advance anchor by exact (output_qty * executions) * seconds_per_unit
	// so sub-unit residue carries forward. With output_qty=1 this is
	// the same shape as object_refresh_tick.go.
	advanceUnits := int64(recipe.OutputQty) * executions
	if recipe.OutputQty <= 1 {
		advanceUnits = executions
	}
	advanceSeconds := advanceUnits * secondsPerUnit
	newAnchor := lastProduced.Time.Add(time.Duration(advanceSeconds) * time.Second)
	if _, err := tx.Exec(ctx,
		`UPDATE actor_produce_state SET last_produced_at = $3
		  WHERE actor_id = $1::uuid AND item_kind = $2`,
		actorID, entry.Item, newAnchor,
	); err != nil {
		return err
	}

	// Broadcast inventory change so any open UI refreshes. Same
	// pattern as gather.go and pay.go.
	app.Hub.Broadcast(WorldEvent{
		Type: "actor_inventory_changed",
		Data: map[string]any{
			"actor_id":  actorID,
			"item_kind": entry.Item,
		},
	})

	return nil
}
