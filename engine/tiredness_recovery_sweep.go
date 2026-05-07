package main

// Tiredness recovery sweep — ZBBS-141 follow-up to the ZBBS-133
// take_break redesign, extended in ZBBS-150 to also cover sleeping
// PCs.
//
// The original recovery path lived inside needs_tick, which fires
// hourly: a 30-min break that opened and closed between two ticks
// would miss recovery entirely, while breaks spanning a tick got a
// flat hour's worth regardless of actual elapsed minutes. Vendors
// realistically take 15-30 min breaks (per Jeff's expectation), so
// coarse-grained recovery defeats the mechanic.
//
// This sweep fires every minute. For each actor whose break_until OR
// sleeping_until is still ahead of last_tiredness_recovery_at (the
// cursor stamped at recovery-window-start by either the take_break
// dispatcher or executePCSleep), it computes elapsed minutes between
// the cursor and min(NOW, GREATEST(break_until, sleeping_until)),
// converts to integer recovery units via the
// take_break.tiredness_recovery_per_minute setting (default 0.1),
// decrements actor_need.tiredness by that many points (floored),
// then advances the cursor by exactly units / rate minutes. Leftover
// fractional minutes carry forward to the next sweep.
//
// Single rate setting for both modes — same physiological recovery
// per wall-clock minute whether the vendor is on break or the PC is
// sleeping. The setting key still reads "take_break." for backward
// compatibility; conceptually it's "tiredness recovery while resting."
//
// Cost: one indexed query per minute returning 0–N rows (typically
// 0–3 — usually nobody is recovering). Per-actor UPDATEs only fire
// when >= 1 unit has accrued (every ~10 min at the default 0.1/min
// rate). No-op when no actors match.

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"time"
)

const (
	tirednessRecoverySweepInterval = time.Minute
	// recoveryEpsilon nudges floor(elapsed*rate) past exact-boundary
	// values that float arithmetic represents as 9.99999998 instead of
	// 10. Smaller than any plausible recovery rate (default 0.1/min,
	// minimum sensible ~0.001/min) so it can't accidentally promote a
	// genuinely-fractional accrual into an integer unit.
	recoveryEpsilon = 1e-9
)

// runTirednessRecoverySweep is the long-lived goroutine started from
// main.go. Returns when ctx cancels.
func (app *App) runTirednessRecoverySweep(ctx context.Context) {
	ticker := time.NewTicker(tirednessRecoverySweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := app.sweepTirednessRecoveryOnce(ctx); err != nil {
				log.Printf("tiredness recovery sweep: %v", err)
			}
		}
	}
}

// sweepTirednessRecoveryOnce processes one pass over all actors with
// an open recovery window. Wraps the per-actor work in a single tx so
// the FOR UPDATE locks held during the SELECT cover the subsequent
// per-actor UPDATE statements; concurrent take_break commits and
// other actor writes wait until the sweep tx commits.
func (app *App) sweepTirednessRecoveryOnce(ctx context.Context) error {
	rate := app.loadTirednessRecoveryPerMinute(ctx)
	if rate <= 0 {
		// Setting explicitly disabled (value "0") — no work to do.
		return nil
	}

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Find actors with an open recovery window. Two windows possible:
	//   - vendor on take_break: break_until > last_tiredness_recovery_at
	//   - PC sleeping: sleeping_until > last_tiredness_recovery_at
	//
	// GREATEST(break_until, sleeping_until) yields the later endpoint;
	// Postgres GREATEST silently ignores NULLs, so an actor with one
	// of the two set works without separate branches. The cursor and
	// recovery_to are clamped at NOW() so we don't credit unfired
	// future minutes.
	//
	// Actors whose cursor is NULL are skipped — they predate ZBBS-141
	// and the next take_break commit / executePCSleep call will
	// populate it.
	rows, err := tx.Query(ctx, `
		SELECT id::text, last_tiredness_recovery_at,
		       LEAST(NOW(), GREATEST(break_until, sleeping_until)) AS recovery_to
		  FROM actor
		 WHERE (break_until IS NOT NULL OR sleeping_until IS NOT NULL)
		   AND last_tiredness_recovery_at IS NOT NULL
		   AND last_tiredness_recovery_at <
		       LEAST(NOW(), GREATEST(break_until, sleeping_until))
		 ORDER BY id
		 FOR UPDATE
	`)
	if err != nil {
		return fmt.Errorf("query candidates: %w", err)
	}
	type candidate struct {
		ID         string
		Cursor     time.Time
		RecoveryTo time.Time
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ID, &c.Cursor, &c.RecoveryTo); err != nil {
			rows.Close()
			return fmt.Errorf("scan candidate: %w", err)
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iter candidates: %w", err)
	}

	for _, c := range candidates {
		elapsedMinutes := c.RecoveryTo.Sub(c.Cursor).Minutes()
		if elapsedMinutes <= 0 {
			continue
		}
		// Tiny epsilon nudge so exact boundary minutes (10.000…something
		// like 9.99999998 due to float representation) cross the floor()
		// instead of waiting one more sweep. Below any real recovery rate
		// so it can't promote a genuinely-fractional accrual.
		units := int(math.Floor(elapsedMinutes*rate + recoveryEpsilon))
		if units <= 0 {
			// Less than a full unit accrued. Leave the cursor where it is
			// so the next sweep sees the full elapsed window — fractional
			// minutes carry forward until they cross 1.0.
			continue
		}
		// Advance the cursor by exactly the time represented by `units`.
		// units / rate minutes is the wall-clock time covered; the
		// remaining fractional minutes (elapsedMinutes - units/rate) stay
		// in the next sweep's window.
		advanceMinutes := float64(units) / rate
		newCursor := c.Cursor.Add(time.Duration(advanceMinutes * float64(time.Minute)))

		tag, err := tx.Exec(ctx,
			`UPDATE actor_need SET value = GREATEST(0, value - $1::int)
			  WHERE actor_id = $2::uuid AND key = 'tiredness'`,
			units, c.ID,
		)
		if err != nil {
			return fmt.Errorf("decrement tiredness for %s: %w", c.ID, err)
		}
		// If the actor has no tiredness row in actor_need, the UPDATE
		// silently affects 0 rows. Advancing the cursor anyway would
		// permanently lose that recovery window AND hide the integrity
		// gap. Surface it as an error so the sweep retries next minute
		// and journalctl shows the missing row.
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("decrement tiredness for %s: expected 1 row, affected %d", c.ID, tag.RowsAffected())
		}
		if _, err := tx.Exec(ctx,
			`UPDATE actor SET last_tiredness_recovery_at = $1 WHERE id = $2::uuid`,
			newCursor, c.ID,
		); err != nil {
			return fmt.Errorf("advance cursor for %s: %w", c.ID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// loadTirednessRecoveryPerMinute reads the
// take_break.tiredness_recovery_per_minute setting (default 0.1).
// Bad / negative values log and fall back to 0.1, matching the
// pre-ZBBS-141 behavior in needs_tick. A value of "0" is honored —
// the operator can disable break-recovery entirely without removing
// the take_break tool.
func (app *App) loadTirednessRecoveryPerMinute(ctx context.Context) float64 {
	raw := app.loadSetting(ctx, "take_break.tiredness_recovery_per_minute", "0.1")
	rate, err := strconv.ParseFloat(raw, 64)
	if err != nil || rate < 0 {
		log.Printf("tiredness recovery sweep: bad take_break.tiredness_recovery_per_minute %q (using 0.1)", raw)
		return 0.1
	}
	return rate
}
