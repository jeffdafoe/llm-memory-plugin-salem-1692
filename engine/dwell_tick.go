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
//      its next tick yet. This is a non-locking pre-flight scan to
//      identify candidates; the apply tx re-checks ripeness against
//      the locked row.
//
//   2. For each candidate, open a tx and:
//        a. SELECT ... FOR UPDATE the credit row, requiring it still
//           matches the candidate's last_credited_at AND is still
//           ripe. Skip if the row was refreshed by a concurrent
//           arrival/consume (stale candidate) or claimed by an
//           overlapping tick.
//        b. SELECT ... FOR UPDATE the actor row to read the current
//           position. Locking the actor row serializes against
//           movement commits.
//        c. Resolve loiter structure from the locked coordinates. If
//           it doesn't match the credit's object_id, the actor walked
//           off — DELETE the credit and commit.
//        d. Otherwise apply dwell_delta via applyConsumption (which
//           handles threshold crossings and the chronicler dispatch),
//           then advance last_credited_at by exactly
//           dwell_period_minutes (NOT to NOW), so residual sub-period
//           time carries forward and a slow tick doesn't shift the
//           dwell phase. Decrement remaining_ticks for item credits;
//           when it hits zero, delete the row (meal complete).
//
//   3. Post-commit: emit completion narration for PCs (private
//      room_event) when the meal exhausts or the dwelt-on need crosses
//      the floor.
//
// Locking the credit row before applyConsumption (rather than after)
// means an overlapping tick that selected the same candidate finds
// either the freshly-advanced row or zero rows — either way the
// double-credit is impossible.
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
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

func (app *App) dispatchObjectRefreshDwell(ctx context.Context) {
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
		dwellDelta     int
		dwellPeriod    int
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		var rt sql.NullInt32 // unused at the dispatch layer; the apply tx re-reads
		if err := rows.Scan(&c.actorID, &c.objectID, &c.attribute, &c.source,
			&c.lastCreditedAt, &rt, &c.dwellDelta, &c.dwellPeriod); err != nil {
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
		if err := app.applyDwellCredit(ctx, c.actorID, c.objectID, c.attribute, c.source,
			c.dwellDelta, c.dwellPeriod, c.lastCreditedAt); err != nil {
			log.Printf("dwell_tick: apply credit %s/%s/%s/%s: %v",
				c.actorID, c.objectID, c.attribute, c.source, err)
		}
	}
}

// applyDwellCredit runs one credit's apply + bookkeeping in a single
// short tx. Locks the credit row first (with the candidate anchor as
// a guard against stale processing), then locks the actor row to
// snapshot position and PC marker, then applies the dwell delta and
// advances/deletes the credit. Post-commit, emits PC narration on
// completion conditions.
//
// Returns nil for the "credit was claimed by someone else / refreshed
// in flight" case — those aren't errors, just races that happen to
// resolve cleanly.
func (app *App) applyDwellCredit(
	ctx context.Context,
	actorID, objectID, attribute, source string,
	dwellDelta, dwellPeriodMinutes int,
	candidateLastCreditedAt time.Time,
) error {
	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// Lock the credit row, requiring (a) the anchor still matches the
	// candidate scan (rules out a fresh upsert from a re-arrival) and
	// (b) the row is still ripe (rules out an overlapping tick that
	// already advanced the anchor).
	var currentRemainingTicks sql.NullInt32
	if err := tx.QueryRow(ctx,
		`SELECT remaining_ticks
		   FROM actor_dwell_credit
		  WHERE actor_id  = $1
		    AND object_id = $2
		    AND attribute = $3
		    AND source    = $4
		    AND last_credited_at = $5
		    AND last_credited_at + (dwell_period_minutes || ' minutes')::interval <= NOW()
		  FOR UPDATE`,
		actorID, objectID, attribute, source, candidateLastCreditedAt,
	).Scan(&currentRemainingTicks); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Stale candidate or claimed by overlap — silent skip.
			return tx.Commit(ctx)
		}
		return fmt.Errorf("lock credit: %w", err)
	}

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

	// Lock the actor row and read position + PC marker. The lock
	// serializes against movement commits, so the position we read
	// here is what's authoritative for this dwell tick.
	var (
		actorX, actorY    float64
		loginUsername     sql.NullString
		displayName       string
		insideStructureID sql.NullString
		preNeed           int
	)
	if err := tx.QueryRow(ctx,
		`SELECT a.current_x, a.current_y, a.login_username, a.display_name,
		        a.inside_structure_id::text,
		        COALESCE((SELECT value FROM actor_need
		                   WHERE actor_id = a.id AND key = $2), 0)
		   FROM actor a
		  WHERE a.id = $1
		  FOR UPDATE`,
		actorID, attribute,
	).Scan(&actorX, &actorY, &loginUsername, &displayName, &insideStructureID, &preNeed); err != nil {
		return fmt.Errorf("lock actor: %w", err)
	}

	// Position check now sees authoritative coordinates from the
	// locked row. resolveLoiteringStructure reads village_object data
	// via app.DB (committed state); that's fine because village_object
	// rows are operator-edited, not actor-driven.
	currentObjectID, _ := app.resolveLoiteringStructure(ctx, actorX, actorY)
	if currentObjectID != objectID {
		if _, err := tx.Exec(ctx,
			`DELETE FROM actor_dwell_credit
			  WHERE actor_id = $1 AND object_id = $2 AND attribute = $3 AND source = $4`,
			actorID, objectID, attribute, source,
		); err != nil {
			return fmt.Errorf("delete departed credit: %w", err)
		}
		return tx.Commit(ctx)
	}

	res, err := app.applyConsumption(ctx, tx, actorID, delta)
	if err != nil {
		return err
	}

	var postNeed int
	switch attribute {
	case "hunger":
		postNeed = res.Hunger
	case "thirst":
		postNeed = res.Thirst
	case "tiredness":
		postNeed = res.Tiredness
	}

	// Use the LOCKED remaining_ticks (currentRemainingTicks), not the
	// candidate-scan value, so a concurrent decrement we missed
	// doesn't cause us to underflow past zero.
	itemExhausted := source == "item" && currentRemainingTicks.Valid && currentRemainingTicks.Int32 <= 1

	if itemExhausted {
		if tag, err := tx.Exec(ctx,
			`DELETE FROM actor_dwell_credit
			  WHERE actor_id = $1 AND object_id = $2 AND attribute = $3 AND source = $4`,
			actorID, objectID, attribute, source,
		); err != nil {
			return err
		} else if tag.RowsAffected() != 1 {
			return fmt.Errorf("expected 1 deleted credit, got %d", tag.RowsAffected())
		}
	} else {
		// ZBBS-HOME-219: cast $5 to text explicitly. Without the cast,
		// pgx binds dwellPeriodMinutes as int, and Postgres's `||`
		// concatenation operator can't implicitly coerce that to text
		// in this context — the encode plan errors with "unable to
		// encode N into text format for text (OID 25)". Result was
		// every dwell update failing silently in the goroutine,
		// stranding actor_dwell_credit rows past their freshness
		// window and breaking all dwell-based recovery (Shade Tree
		// tiredness, Well thirst, etc).
		if tag, err := tx.Exec(ctx,
			`UPDATE actor_dwell_credit
			    SET last_credited_at = last_credited_at + ($5::text || ' minutes')::interval,
			        remaining_ticks  = CASE WHEN source = 'item' THEN remaining_ticks - 1
			                                ELSE remaining_ticks
			                           END
			  WHERE actor_id = $1 AND object_id = $2 AND attribute = $3 AND source = $4`,
			actorID, objectID, attribute, source, dwellPeriodMinutes,
		); err != nil {
			return err
		} else if tag.RowsAffected() != 1 {
			return fmt.Errorf("expected 1 updated credit, got %d", tag.RowsAffected())
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
			return "You feel a little less tired than before."
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
