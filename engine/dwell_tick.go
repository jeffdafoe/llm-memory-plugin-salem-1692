package main

// Dwell tick (ZBBS-172) — per-minute handler that converts dwell
// credits into actual need recovery for actors still present at the
// pinned object. See dwell.go for the upsert sites and the credit
// model overview.
//
// Per-tick algorithm:
//
//   1. SELECT credit rows whose last_credited_at is at least
//      dwell_period_minutes in the past. Anything newer hasn't earned
//      its next tick yet.
//
//   2. For each candidate, resolve the actor's current loiter
//      structure. If it doesn't match the credit's object_id, the
//      actor walked away — delete the row (interrupted meal, vacated
//      tree). No attempt to "credit on departure"; the per-tick
//      anchor model means an in-progress tick is forfeit when you
//      leave.
//
//   3. Apply dwell_delta via applyConsumption — same path arrival
//      uses, so threshold-crossing chronicler dispatch and the audit
//      trail come along for free.
//
//   4. Advance last_credited_at by exactly dwell_period_minutes (NOT
//      to NOW), so residual sub-period time carries forward and a
//      slow tick doesn't shift the dwell phase. Decrement
//      remaining_ticks for item credits; when it hits zero, delete
//      the row (meal complete).
//
// One credit per row per tick. An actor parked under a tree for
// 32 minutes with dwell_period_minutes=15 gets two credits (at min 15
// and min 30, with the 31st-min pass picking up the residual). The
// 'continuous' anchor advance uses exact period multiples like
// dispatchObjectRefreshRegen.
//
// Errors are logged and swallowed per-row. The tick proceeds; one
// crashing actor doesn't stall the rest of the village.

import (
	"context"
	"log"
	"time"
)

func (app *App) dispatchObjectRefreshDwell(ctx context.Context) {
	now := time.Now().UTC()

	// Pre-flight: load ripe candidates outside any tx so the per-row
	// applyConsumption can take its own short tx. The candidate set
	// is small (only credits whose anchor is past the period) and
	// staleness within one tick is fine — the dwell fires next pass
	// if it slips.
	rows, err := app.DB.Query(ctx, `
		SELECT actor_id::text, object_id::text, attribute, source,
		       last_credited_at, remaining_ticks,
		       dwell_delta, dwell_period_minutes
		  FROM actor_dwell_credit
		 WHERE last_credited_at + (dwell_period_minutes || ' minutes')::interval <= NOW()
	`)
	if err != nil {
		log.Printf("dwell_tick: select candidates: %v", err)
		return
	}

	type candidate struct {
		actorID        string
		objectID       string
		attribute      string
		source         string
		lastCreditedAt time.Time
		remainingTicks *int
		dwellDelta     int
		dwellPeriod    int
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.actorID, &c.objectID, &c.attribute, &c.source,
			&c.lastCreditedAt, &c.remainingTicks, &c.dwellDelta, &c.dwellPeriod); err != nil {
			rows.Close()
			log.Printf("dwell_tick: scan candidate: %v", err)
			return
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("dwell_tick: iterate candidates: %v", err)
		return
	}
	if len(candidates) == 0 {
		return
	}

	for _, c := range candidates {
		// Position check: load actor's current x/y, resolve loiter
		// structure, compare to the credited object_id. Mismatch =
		// actor walked off; delete the credit and move on.
		var actorX, actorY float64
		if err := app.DB.QueryRow(ctx,
			`SELECT current_x, current_y FROM actor WHERE id = $1`,
			c.actorID,
		).Scan(&actorX, &actorY); err != nil {
			log.Printf("dwell_tick: load actor %s: %v", c.actorID, err)
			continue
		}
		currentObjectID, _ := app.resolveLoiteringStructure(ctx, actorX, actorY)
		if currentObjectID != c.objectID {
			if _, err := app.DB.Exec(ctx,
				`DELETE FROM actor_dwell_credit
				  WHERE actor_id = $1 AND object_id = $2 AND attribute = $3 AND source = $4`,
				c.actorID, c.objectID, c.attribute, c.source,
			); err != nil {
				log.Printf("dwell_tick: delete departed credit %s/%s/%s/%s: %v",
					c.actorID, c.objectID, c.attribute, c.source, err)
			}
			continue
		}

		// Per-row tx so applyConsumption gets its own commit boundary
		// and one failure doesn't roll back the rest of the tick.
		if err := app.applyDwellCredit(ctx, c.actorID, c.objectID, c.attribute, c.source, c.dwellDelta, c.dwellPeriod, c.remainingTicks); err != nil {
			log.Printf("dwell_tick: apply credit %s/%s/%s/%s: %v",
				c.actorID, c.objectID, c.attribute, c.source, err)
		}
	}

	_ = now
}

// applyDwellCredit runs one credit's apply + bookkeeping in a single
// short tx. Builds the consumptionDelta from dwellDelta + attribute,
// runs applyConsumption (which handles threshold crossings and the
// chronicler dispatch), then advances the credit's anchor by exactly
// dwell_period_minutes and decrements remaining_ticks for item
// credits. Item credits whose remaining_ticks would hit zero are
// deleted instead of updated.
func (app *App) applyDwellCredit(
	ctx context.Context,
	actorID, objectID, attribute, source string,
	dwellDelta, dwellPeriodMinutes int,
	remainingTicks *int,
) error {
	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	delta := consumptionDelta{}
	switch attribute {
	case "hunger":
		delta.Hunger = dwellDelta
	case "thirst":
		delta.Thirst = dwellDelta
	case "tiredness":
		delta.Tiredness = dwellDelta
	default:
		// Unknown attribute on the credit row. Defense in depth — the
		// FK to refresh_attribute should prevent this, but if a future
		// attribute lands without engine support we should drop the
		// stale credit rather than no-op forever.
		if _, err := tx.Exec(ctx,
			`DELETE FROM actor_dwell_credit
			  WHERE actor_id = $1 AND object_id = $2 AND attribute = $3 AND source = $4`,
			actorID, objectID, attribute, source,
		); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	if _, err := app.applyConsumption(ctx, tx, actorID, delta, "dwell-"+source); err != nil {
		return err
	}

	if source == "item" && remainingTicks != nil && *remainingTicks <= 1 {
		// Last tick of the meal — delete the credit so the dwell
		// loop forgets about this row. The actor still gets the
		// final tick's recovery (already applied above).
		if _, err := tx.Exec(ctx,
			`DELETE FROM actor_dwell_credit
			  WHERE actor_id = $1 AND object_id = $2 AND attribute = $3 AND source = $4`,
			actorID, objectID, attribute, source,
		); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	// Advance the anchor and (for items) decrement the countdown.
	// Anchor advance uses exact dwell_period_minutes so a delayed
	// tick doesn't shift the dwell phase forward.
	if _, err := tx.Exec(ctx,
		`UPDATE actor_dwell_credit
		    SET last_credited_at = last_credited_at + ($5 || ' minutes')::interval,
		        remaining_ticks  = CASE WHEN source = 'item' THEN remaining_ticks - 1
		                                ELSE remaining_ticks
		                           END
		  WHERE actor_id = $1 AND object_id = $2 AND attribute = $3 AND source = $4`,
		actorID, objectID, attribute, source, dwellPeriodMinutes,
	); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
