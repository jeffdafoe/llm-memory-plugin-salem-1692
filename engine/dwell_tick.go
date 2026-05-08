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
	"database/sql"
	"fmt"
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
//
// Completion narration (ZBBS-172, follow-up): for PCs, emits a
// private room_event after commit when (a) the dwelt-on need hits 0
// for the first time on this credit, or (b) an item credit's last
// tick fires (remaining_ticks reaches 0). NPCs don't get the room
// event — their next perception build picks up the change via the
// audit log + needs snapshot.
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

	// Pre-credit snapshot for completion narration. We need the prior
	// value of the dwelt-on need to detect a transition to zero (this
	// tick clamps a positive value to the floor). Also pull the
	// actor's PC marker + display name + scope for the room_event.
	// Cheap: one row, indexed by PK.
	var (
		preNeed       int
		loginUsername sql.NullString
		displayName   string
		insideStructureID sql.NullString
	)
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE((SELECT value FROM actor_need WHERE actor_id = a.id AND key = $2), 0),
		        a.login_username, a.display_name, a.inside_structure_id::text
		   FROM actor a
		  WHERE a.id = $1`,
		actorID, attribute,
	).Scan(&preNeed, &loginUsername, &displayName, &insideStructureID); err != nil {
		return fmt.Errorf("snapshot pre-need for narration: %w", err)
	}

	res, err := app.applyConsumption(ctx, tx, actorID, delta, "dwell-"+source)
	if err != nil {
		return err
	}

	// Read the post-credit value of the dwelt-on need from the
	// applyConsumption result. Used both for the floor-hit narration
	// and to suppress the item-exhausted narration in the case where
	// the meal is over but the need was already at 0 going in
	// (eating-while-full pathological case — no narration warranted).
	var postNeed int
	switch attribute {
	case "hunger":
		postNeed = res.Hunger
	case "thirst":
		postNeed = res.Thirst
	case "tiredness":
		postNeed = res.Tiredness
	}

	itemExhausted := source == "item" && remainingTicks != nil && *remainingTicks <= 1

	if itemExhausted {
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
	} else {
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
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Post-commit narration. Floor-hit and item-exhausted are the two
	// completion events; both render a single room_event for PCs.
	// NPCs (no login_username) don't get the broadcast — their next
	// perception build sees the audit row + npc_needs_changed.
	if loginUsername.Valid && loginUsername.String != "" {
		floorHit := preNeed > 0 && postNeed == 0
		if itemExhausted || floorHit {
			text := dwellCompletionNarration(attribute, source, itemExhausted, floorHit)
			if text != "" {
				scope := ""
				if insideStructureID.Valid {
					scope = insideStructureID.String
				}
				app.Hub.Broadcast(WorldEvent{
					Type: "room_event",
					Data: map[string]interface{}{
						"actor_id":     actorID,
						"actor_name":   displayName,
						"kind":         "dwell_complete",
						"text":         text,
						"structure_id": scope,
						"private":      true,
						"at":           time.Now().UTC().Format(time.RFC3339),
					},
				})
			}
		}
	}

	return nil
}

// dwellCompletionNarration composes the felt-language line for a
// dwell completion. Item-exhausted takes precedence over floor-hit
// (more specific event); both can be true if the last bite of stew
// also crosses the hunger floor, but the meal-finished phrasing
// already implies satiation. Returns "" for unknown attribute or
// unhandled combinations.
func dwellCompletionNarration(attribute, source string, itemExhausted, floorHit bool) string {
	if itemExhausted {
		switch attribute {
		case "hunger":
			return "You finish the last bite, satisfied."
		case "thirst":
			return "You drain the last drop."
		case "tiredness":
			return "You ease back, the last of it gone."
		default:
			return "You finish what you had."
		}
	}
	if floorHit {
		switch attribute {
		case "hunger":
			return "You feel full."
		case "thirst":
			return "Your thirst is quenched."
		case "tiredness":
			return "You feel rested."
		}
	}
	return ""
}
