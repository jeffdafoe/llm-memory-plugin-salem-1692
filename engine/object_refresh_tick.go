package main

// Object-refresh regeneration tick (ZBBS-090).
//
// applyObjectRefreshAtArrival decrements available_quantity when an actor
// drinks/eats/rests at an object. This tick handler regrows the supply
// over time per the row's refresh_mode and refresh_period_hours.
//
// Two modes:
//
//   continuous — water/well/berry-bush style. One unit accrues every
//                refresh_period_hours / max_quantity wall-clock hours.
//                Smooth ramp anchored on last_refresh_at; on each tick we
//                compute how many full units have accrued since the
//                anchor, add them up to max_quantity, and advance the
//                anchor by exactly (units * unit_seconds) so the residual
//                sub-unit time carries forward.
//
//   periodic   — crop/harvest style. available_quantity sits at zero (or
//                wherever) until refresh_period_hours has elapsed since
//                last_refresh_at, then jumps to max_quantity in one step.
//                Anchor advances by period_hours (not to now), so a
//                missed tick during a long server outage doesn't compound
//                multiple harvests on the next pass.
//
// Rows with NULL available_quantity (infinite supply) skip the loop
// entirely. Rows with available_quantity set but NULL last_refresh_at
// (just-edited or freshly-inserted) get their anchor stamped on this
// pass with no regen — the next tick is the first that can regen.
//
// Cadence: registered in runServerTickOnce, which fires every minute.
// Each pass is one SELECT + zero-to-N UPDATEs; cheap when no row is
// behind.

import (
	"context"
	"log"
	"time"
)

func (app *App) dispatchObjectRefreshRegen(ctx context.Context) {
	now := time.Now().UTC()

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		log.Printf("object_refresh_regen: begin tx: %v", err)
		return
	}
	defer tx.Rollback(ctx)

	// FOR UPDATE because we're going to write back to these rows. The
	// available_quantity decrement on arrival also FOR UPDATEs the same
	// rows, so the lock ordering is consistent and the two paths can't
	// interleave inconsistently.
	rows, err := tx.Query(ctx, `
		SELECT object_id::text, attribute, available_quantity, max_quantity,
		       refresh_mode, refresh_period_hours, last_refresh_at
		  FROM object_refresh
		 WHERE available_quantity IS NOT NULL
		   AND refresh_period_hours IS NOT NULL
		 FOR UPDATE
	`)
	if err != nil {
		log.Printf("object_refresh_regen: select rows: %v", err)
		return
	}
	type rowSpec struct {
		objectID    string
		attribute   string
		avail       int
		maxQ        int
		mode        string
		periodHours int
		lastRefresh *time.Time
	}
	var specs []rowSpec
	for rows.Next() {
		var rs rowSpec
		var lastAt *time.Time
		if err := rows.Scan(&rs.objectID, &rs.attribute, &rs.avail, &rs.maxQ, &rs.mode, &rs.periodHours, &lastAt); err != nil {
			rows.Close()
			log.Printf("object_refresh_regen: scan row: %v", err)
			return
		}
		rs.lastRefresh = lastAt
		specs = append(specs, rs)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("object_refresh_regen: iterate rows: %v", err)
		return
	}

	type update struct {
		objectID  string
		attribute string
		newAvail  int
		newAnchor time.Time
	}
	var updates []update

	for _, rs := range specs {
		// First pass for this row: stamp anchor, no regen yet. The row is
		// already locked by the SELECT FOR UPDATE so no concurrent edit
		// can clobber the stamp.
		if rs.lastRefresh == nil {
			updates = append(updates, update{
				objectID:  rs.objectID,
				attribute: rs.attribute,
				newAvail:  rs.avail,
				newAnchor: now,
			})
			continue
		}

		switch rs.mode {
		case "continuous":
			// Already at cap — nothing to regen, but keep the anchor moving
			// forward so a future drink that drops below max regenerates
			// from "now" rather than from a stale early-morning timestamp
			// that would unfairly back-credit hours of accrual.
			if rs.avail >= rs.maxQ {
				updates = append(updates, update{
					objectID:  rs.objectID,
					attribute: rs.attribute,
					newAvail:  rs.avail,
					newAnchor: now,
				})
				continue
			}
			// Seconds per unit of regen — period_hours * 3600 / max_quantity.
			// Integer division is fine: the residue is bounded to <1 second
			// per unit, which never compounds because we advance the anchor
			// by exact unit-second multiples below.
			periodSeconds := int64(rs.periodHours) * 3600
			secondsPerUnit := periodSeconds / int64(rs.maxQ)
			if secondsPerUnit <= 0 {
				// Defensive — would mean max_quantity > period_hours*3600,
				// i.e. >1 unit per second of regen. Constraint allows it
				// but it's nonsense; log once per occurrence and skip.
				log.Printf("object_refresh_regen: seconds_per_unit <= 0 for %s/%s (max=%d, period_hours=%d); skipping",
					rs.objectID, rs.attribute, rs.maxQ, rs.periodHours)
				continue
			}
			elapsedSeconds := int64(now.Sub(*rs.lastRefresh).Seconds())
			if elapsedSeconds < secondsPerUnit {
				continue // less than one unit's worth of time
			}
			regen := elapsedSeconds / secondsPerUnit
			headroom := int64(rs.maxQ - rs.avail)
			if regen > headroom {
				regen = headroom
			}
			if regen <= 0 {
				continue
			}
			newAvail := rs.avail + int(regen)
			newAnchor := rs.lastRefresh.Add(time.Duration(regen*secondsPerUnit) * time.Second)
			updates = append(updates, update{
				objectID:  rs.objectID,
				attribute: rs.attribute,
				newAvail:  newAvail,
				newAnchor: newAnchor,
			})

		case "periodic":
			// Refills in one step once the full period has elapsed. The
			// anchor advances by exactly period_hours (not to now), so a
			// long outage doesn't compound multiple harvests on the next
			// pass — at most one refill per tick. Operators who want a
			// catch-up can edit the row.
			period := time.Duration(rs.periodHours) * time.Hour
			elapsed := now.Sub(*rs.lastRefresh)
			if elapsed < period {
				continue
			}
			updates = append(updates, update{
				objectID:  rs.objectID,
				attribute: rs.attribute,
				newAvail:  rs.maxQ,
				newAnchor: rs.lastRefresh.Add(period),
			})

		default:
			log.Printf("object_refresh_regen: unknown mode %q for %s/%s; skipping",
				rs.mode, rs.objectID, rs.attribute)
		}
	}

	if len(updates) == 0 {
		// Nothing to do this tick. Still commit (no-op) so the lock
		// releases promptly.
		_ = tx.Commit(ctx)
		return
	}

	for _, u := range updates {
		if _, err := tx.Exec(ctx,
			`UPDATE object_refresh
			    SET available_quantity = $1,
			        last_refresh_at    = $2
			  WHERE object_id = $3 AND attribute = $4`,
			u.newAvail, u.newAnchor, u.objectID, u.attribute,
		); err != nil {
			log.Printf("object_refresh_regen: update %s/%s: %v", u.objectID, u.attribute, err)
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("object_refresh_regen: commit: %v", err)
		return
	}
}
