package main

// PC sleep mechanic — stage B of the lodging design.
//
// Three responsibilities:
//
//   1. touchPCInput — write actor.last_pc_input_at on every PC HTTP
//      handler entry. The auto-bed scan reads this to decide whether
//      a connected PC has been idle long enough to bed.
//
//   2. runSleepSweep — long-lived goroutine started from main. Each
//      tick:
//        a) Wakes anyone whose sleeping_until has arrived (broadcast
//           pc_sleep_ended).
//        b) Scans for idle lodger PCs (has lodger status at their
//           current structure, idle past the threshold, not already
//           sleeping). Auto-beds them.
//
//   3. executePCSleep / executePCWake — the manual-control entrypoints
//      called from the /pc/sleep and /pc/wake HTTP handlers, and
//      reused internally by the auto-bed path. Both broadcast the
//      corresponding world event so connected clients can fade in / out
//      of sleep state.
//
// Wake target: next dawn (computed from world_dawn_time setting). If a
// PC is already past today's dawn time when they bed, they sleep
// through to tomorrow's dawn. Tiredness reset already happens at
// world rotation (resetSleptTiredness in world_rotation.go) for
// any actor inside a structure — sleep doesn't have to do that itself.
//
// Early-wake penalty: none in v1. PC who wakes before world rotation
// keeps whatever tiredness they had at sleep time (the rotation reset
// hasn't fired yet). That's the natural penalty without bespoke logic.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"
)

const sleepSweepInterval = time.Minute

// touchPCInput stamps actor.last_pc_input_at = NOW for the given PC.
// Called from every PC HTTP handler entry. Errors logged but not
// propagated — failing to record an input timestamp is a soft failure
// (worst case: PC gets auto-bedded earlier than expected; can /wake).
func (app *App) touchPCInput(ctx context.Context, actorID string) {
	if actorID == "" {
		return
	}
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor SET last_pc_input_at = NOW() WHERE id = $1::uuid`,
		actorID,
	); err != nil {
		log.Printf("touchPCInput(%s): %v", actorID, err)
	}
}

// runSleepSweep is the long-lived goroutine started from main.go. Wakes
// expired sleepers + auto-beds idle lodgers, once per minute. Returns
// when ctx cancels.
func (app *App) runSleepSweep(ctx context.Context) {
	ticker := time.NewTicker(sleepSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := app.wakeExpiredSleepers(ctx); err != nil {
				log.Printf("sleep sweep wake: %v", err)
			}
			if err := app.autoBedIdleLodgers(ctx); err != nil {
				log.Printf("sleep sweep auto-bed: %v", err)
			}
		}
	}
}

// wakeExpiredSleepers clears sleeping_until on any actor whose wake
// time has arrived, and broadcasts pc_sleep_ended for each. Atomic
// per-row (the UPDATE...RETURNING captures the snapshot of woken
// actors) so a concurrent manual /wake doesn't double-broadcast.
func (app *App) wakeExpiredSleepers(ctx context.Context) error {
	rows, err := app.DB.Query(ctx,
		`UPDATE actor
		    SET sleeping_until = NULL
		  WHERE sleeping_until IS NOT NULL
		    AND sleeping_until <= NOW()
		 RETURNING id::text`,
	)
	if err != nil {
		return fmt.Errorf("wake expired query: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("wake expired scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("wake expired iter: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, id := range ids {
		app.Hub.Broadcast(WorldEvent{
			Type: "pc_sleep_ended",
			Data: map[string]interface{}{
				"actor_id": id,
				"reason":   "dawn",
				"at":       now,
			},
		})
	}
	return nil
}

// autoBedIdleLodgers finds connected PCs with active lodger status at
// their current structure who've been idle past the threshold, and
// beds them via executePCSleep. Each candidate is checked individually
// against isLodger so the auto-bed only fires when the lodging
// contract is genuinely active.
func (app *App) autoBedIdleLodgers(ctx context.Context) error {
	idleMinutes, err := app.loadIdleSleepMinutes(ctx)
	if err != nil {
		return err
	}
	if idleMinutes <= 0 {
		// Configured to disable auto-bed; manual /sleep only.
		return nil
	}

	rows, err := app.DB.Query(ctx,
		`SELECT a.id::text, a.inside_structure_id::text
		   FROM actor a
		  WHERE a.login_username IS NOT NULL
		    AND a.inside_structure_id IS NOT NULL
		    AND a.sleeping_until IS NULL
		    AND a.last_pc_input_at IS NOT NULL
		    AND a.last_pc_input_at < NOW() - ($1::int * INTERVAL '1 minute')`,
		idleMinutes,
	)
	if err != nil {
		return fmt.Errorf("auto-bed candidates query: %w", err)
	}
	type candidate struct{ ID, StructureID string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ID, &c.StructureID); err != nil {
			rows.Close()
			return fmt.Errorf("auto-bed scan: %w", err)
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("auto-bed iter: %w", err)
	}

	for _, c := range candidates {
		ok, err := app.isLodger(ctx, c.ID, c.StructureID)
		if err != nil {
			log.Printf("auto-bed isLodger(%s, %s): %v", c.ID, c.StructureID, err)
			continue
		}
		if !ok {
			continue
		}
		if _, sleepErr := app.executePCSleep(ctx, c.ID); sleepErr != nil {
			log.Printf("auto-bed executePCSleep(%s): %v", c.ID, sleepErr)
		}
	}
	return nil
}

// loadIdleSleepMinutes reads the pc_idle_sleep_minutes setting. Returns
// 5 when the row is missing (default), and a parse error if the value
// is malformed.
func (app *App) loadIdleSleepMinutes(ctx context.Context) (int, error) {
	var raw sql.NullString
	if err := app.DB.QueryRow(ctx,
		`SELECT value FROM setting WHERE key = 'pc_idle_sleep_minutes'`,
	).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 5, nil
		}
		return 0, fmt.Errorf("load pc_idle_sleep_minutes: %w", err)
	}
	if !raw.Valid {
		return 5, nil
	}
	var n int
	if _, err := fmt.Sscanf(raw.String, "%d", &n); err != nil {
		return 0, fmt.Errorf("parse pc_idle_sleep_minutes %q: %w", raw.String, err)
	}
	return n, nil
}

// pcSleepResult carries the outcome of an executePCSleep call, returned
// to the manual /pc/sleep handler so the response can include the
// computed wake time.
type pcSleepResult struct {
	WakeAt time.Time // UTC; zero when result wasn't a fresh transition
}

// executePCSleep beds the PC. Sets sleeping_until = next dawn, broadcasts
// pc_sleep_started. Idempotent — returns silently when the PC is already
// sleeping (UPDATE ... WHERE sleeping_until IS NULL guards against the
// race). Manual handlers should pre-validate lodger status before
// calling; the auto-bed path validates inline (see autoBedIdleLodgers).
func (app *App) executePCSleep(ctx context.Context, actorID string) (pcSleepResult, error) {
	cfg, err := app.loadWorldConfig(ctx)
	if err != nil {
		return pcSleepResult{}, fmt.Errorf("load world config: %w", err)
	}
	dawnH, dawnM, err := parseHM(cfg.DawnTime)
	if err != nil {
		return pcSleepResult{}, fmt.Errorf("parse dawn time: %w", err)
	}
	now := time.Now().In(cfg.Location)
	wakeAt := nextDawnAt(now, dawnH, dawnM).UTC()

	tag, err := app.DB.Exec(ctx,
		`UPDATE actor SET sleeping_until = $1
		  WHERE id = $2::uuid AND sleeping_until IS NULL`,
		wakeAt, actorID,
	)
	if err != nil {
		return pcSleepResult{}, fmt.Errorf("update sleeping_until: %w", err)
	}
	if tag.RowsAffected() != 1 {
		// Already sleeping (or no such actor). Soft no-op.
		return pcSleepResult{}, nil
	}

	app.Hub.Broadcast(WorldEvent{
		Type: "pc_sleep_started",
		Data: map[string]interface{}{
			"actor_id": actorID,
			"wake_at":  wakeAt.Format(time.RFC3339),
			"at":       time.Now().UTC().Format(time.RFC3339),
		},
	})
	return pcSleepResult{WakeAt: wakeAt}, nil
}

// executePCWake clears sleeping_until and broadcasts pc_sleep_ended
// (reason "manual"). Idempotent — silent no-op when PC isn't asleep.
func (app *App) executePCWake(ctx context.Context, actorID string) error {
	tag, err := app.DB.Exec(ctx,
		`UPDATE actor SET sleeping_until = NULL
		  WHERE id = $1::uuid AND sleeping_until IS NOT NULL`,
		actorID,
	)
	if err != nil {
		return fmt.Errorf("update sleeping_until: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return nil
	}
	app.Hub.Broadcast(WorldEvent{
		Type: "pc_sleep_ended",
		Data: map[string]interface{}{
			"actor_id": actorID,
			"reason":   "manual",
			"at":       time.Now().UTC().Format(time.RFC3339),
		},
	})
	return nil
}

// nextDawnAt computes the next-occurring dawn moment from `now`. If
// today's dawn is still in the future, returns today's dawn; otherwise
// returns tomorrow's. Uses now.Location() so configured world timezone
// is respected.
func nextDawnAt(now time.Time, dawnH, dawnM int) time.Time {
	loc := now.Location()
	y, mo, d := now.Date()
	todayDawn := time.Date(y, mo, d, dawnH, dawnM, 0, 0, loc)
	if todayDawn.After(now) {
		return todayDawn
	}
	return todayDawn.Add(24 * time.Hour)
}
