package main

// PC sleep mechanic — ZBBS-132 + ZBBS-150 redesign.
//
// Three responsibilities:
//
//   1. touchPCInput — every action-shaped PC handler entry (move,
//      say, speak, pay, move-room). Stamps last_pc_input_at AND
//      input-wakes any sleeping PC (ZBBS-150). /pc/sleep and /pc/wake
//      handlers manage state directly without touchPCInput so manual
//      bed/wake transitions get their authoritative reason on the
//      broadcast.
//
//   2. runSleepSweep — long-lived goroutine started from main. Each
//      tick:
//        a) Wakes sleepers when ANY of: sleeping_until reached
//           (safety cap), tiredness <= 0 (rested), or
//           room_access expired (housekeeping at checkout).
//        b) Auto-beds idle lodger PCs whose room is 'private' and
//           tiredness >= pc_idle_sleep_min_tiredness. The bedroom-
//           only gate (ZBBS-149) keeps the sweep from sleep-darting
//           a PC sitting in the bar; the tiredness gate keeps a
//           freshly-rested PC who briefly steps into their room from
//           getting knocked out.
//
//   3. executePCSleep / executePCWake — the manual-control entrypoints
//      called from the /pc/sleep and /pc/wake HTTP handlers, and
//      reused internally by the auto-bed path. Both broadcast the
//      corresponding world event so connected clients can fade in / out
//      of sleep state.
//
// Wake target (ZBBS-150): NOW + pc_sleep_max_duration_hours (default
// 12) is a SAFETY CAP on sleeping_until. The actual wake usually
// fires earlier via the recovery- or checkout-driven branches in
// wakeExpiredSleepers. Recovery rate reuses
// take_break.tiredness_recovery_per_minute (0.1/min default) so a
// max-tiredness PC wakes at ~4h wall-clock; the cap guards against
// stuck rows if the recovery sweep is wedged.
//
// resetSleptTiredness in world_rotation.go is preserved as the NPC
// backstop — vendors get the dawn snap-to-zero. PCs use the
// continuous recovery model.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

const sleepSweepInterval = time.Minute

// touchPCInput stamps actor.last_pc_input_at = NOW and, if the PC is
// currently sleeping, clears sleeping_until + broadcasts a
// pc_sleep_ended event with reason "input" (ZBBS-150 input-wake).
// Called from every PC HTTP action handler (move, say, speak, pay,
// move-room). NOT called from /pc/sleep or /pc/wake — those
// manage sleep state directly so the broadcast carries the
// authoritative reason ("manual" wake, fresh "started" sleep).
//
// PC-only: gated on login_username IS NOT NULL inside the SQL so a
// stray non-PC actor ID (admin slip, future caller bug) doesn't
// silently wake an NPC and broadcast a pc_* event for it. The
// broadcast also requires the row was actually sleeping at the
// moment of update (CTE pattern below) so a benign double-call
// doesn't produce a stale "input" wake event.
//
// Errors logged but not propagated — failing to record an input
// timestamp is a soft failure (worst case: PC gets auto-bedded
// earlier than expected; can /wake).
func (app *App) touchPCInput(ctx context.Context, actorID string) {
	if actorID == "" {
		return
	}
	// Single-statement CTE: capture the pre-UPDATE sleeping_until state,
	// run the UPDATE, return whether the actor WAS sleeping. Atomic
	// per-row; the broadcast only fires when the UPDATE actually flipped
	// a sleeping PC awake. Returns no rows for non-PC actors (gated by
	// login_username IS NOT NULL in the WITH clause).
	var wasSleeping bool
	err := app.DB.QueryRow(ctx,
		`WITH before AS (
		    SELECT id, sleeping_until IS NOT NULL AS was_sleeping
		      FROM actor
		     WHERE id = $1::uuid
		       AND login_username IS NOT NULL
		 ),
		 upd AS (
		    UPDATE actor a
		       SET last_pc_input_at = NOW(),
		           sleeping_until = NULL
		      FROM before b
		     WHERE a.id = b.id
		    RETURNING b.was_sleeping
		 )
		 SELECT was_sleeping FROM upd`,
		actorID,
	).Scan(&wasSleeping)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Not a PC (or no such actor). Soft no-op.
			return
		}
		log.Printf("touchPCInput(%s): %v", actorID, err)
		return
	}
	if wasSleeping {
		app.Hub.Broadcast(WorldEvent{
			Type: "pc_sleep_ended",
			Data: map[string]interface{}{
				"actor_id": actorID,
				"reason":   "input",
				"at":       time.Now().UTC().Format(time.RFC3339),
			},
		})
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
			// expireRoomAccess (ZBBS-163) runs first so rooms freed
			// this minute are available to new lodgers before the
			// downstream queries observe state. wakeExpiredSleepers
			// reads expires_at directly so its timing is unaffected
			// by ordering, but auto-bed and assignBedroom-via-
			// deliver_order benefit from the active flag being
			// current.
			if err := app.expireRoomAccess(ctx); err != nil {
				log.Printf("sleep sweep expire room access: %v", err)
			}
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
// condition has fired, and broadcasts pc_sleep_ended for each. Three
// wake conditions, ORed (ZBBS-150):
//
//   - sleeping_until <= NOW(): safety cap reached. Pre-ZBBS-150 this
//     was the only branch (with sleeping_until pinned to nextDawnAt).
//     Now it's a backstop — the cap is NOW + pc_sleep_max_duration_hours.
//
//   - tiredness <= 0: rested wake. Continuous recovery in
//     tiredness_recovery_sweep.go decrements actor_need.tiredness
//     while sleeping_until is set; once it hits 0 the PC is fully
//     rested and the sweep wakes them naturally.
//
//   - room_access expired for the PC's current bedroom:
//     housekeeping knock at checkout. Lodger_until rolls into
//     room_access.expires_at via assignBedroomForLodger; when it
//     passes, the PC is no longer welcome in that room and gets
//     woken so they can leave. Note: the PC's lodger status (via
//     isLodger) materializes from the same ledger row, so this fires
//     in lockstep with the lodger losing room rights.
//
// Atomic per-row via the UPDATE...RETURNING. Broadcast reason
// "auto" — distinct from manual /wake (reason "manual") and input-
// wake (reason "input"); future refinement could split this into
// rested/checkout/cap if clients want differentiated UX.
func (app *App) wakeExpiredSleepers(ctx context.Context) error {
	// PC-only via login_username IS NOT NULL gate. The pc_sleep_ended
	// broadcast is PC-shaped and the new wake conditions (tiredness,
	// room_access) are PC-specific; without the gate, an NPC with
	// sleeping_until set (from a future feature or admin edit) would
	// erroneously emit a PC sleep event.
	rows, err := app.DB.Query(ctx,
		`UPDATE actor a
		    SET sleeping_until = NULL
		  WHERE a.login_username IS NOT NULL
		    AND a.sleeping_until IS NOT NULL
		    AND (
		      a.sleeping_until <= NOW()
		      OR EXISTS (
		        SELECT 1 FROM actor_need an
		         WHERE an.actor_id = a.id
		           AND an.key = 'tiredness'
		           AND an.value <= 0
		      )
		      OR (
		        a.inside_room_id IS NOT NULL
		        AND EXISTS (
		          SELECT 1 FROM room_access sa
		           WHERE sa.actor_id = a.id
		             AND sa.room_id = a.inside_room_id
		             AND sa.expires_at IS NOT NULL
		             AND sa.expires_at <= NOW()
		        )
		      )
		    )
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
				"reason":   "auto",
				"at":       now,
			},
		})
	}
	return nil
}

// autoBedIdleLodgers finds connected PCs idle in their bedroom
// room and beds them via executePCSleep. ZBBS-150 tightens the
// pre-ZBBS-149 always-fires-anywhere behavior with two new gates:
//
//   - room_kind = 'private': PC must be in their bedroom, not
//     the bar (common room). Without this, a 5-min AFK at the
//     bar pinned the PC for ~22h — the bug Jeff hit on 2026-05-07.
//
//   - tiredness >= pc_idle_sleep_min_tiredness (default 10): a PC
//     who just walked into their bedroom while fresh isn't auto-
//     knocked-out. Sleep is for the tired.
//
// A private room alone is not sufficient; the EXISTS clause also
// requires an unexpired room_access row so admin overrides or
// stale placement (PC still in room_id after lodger_until passed)
// don't auto-bed someone who lost their room rights. This is the
// authoritative runtime materialization of "is a lodger here" —
// the explicit isLodger ledger check is no longer needed because
// room_access is the SAME ledger row's effects, just queried
// directly.
func (app *App) autoBedIdleLodgers(ctx context.Context) error {
	idleMinutes := app.loadIdleSleepMinutes(ctx)
	if idleMinutes <= 0 {
		// Configured to disable auto-bed; manual /sleep only.
		return nil
	}
	minTiredness := app.loadIdleSleepMinTiredness(ctx)

	// Explicit room_access check. Earlier draft asserted "private
	// room implies access" by construction, but that holds only
	// when assignBedroomForLodger is the sole path that places a PC
	// in a private room AND the access row hasn't expired since.
	// Code review pointed out that admin overrides, expired access
	// (lodger_until passed but PC still in inside_room_id), or
	// stale state can violate the implication. Enforcing the access
	// row here makes the gate trustworthy. ZBBS-163: active=true
	// instead of expires_at — kept in sync by expireRoomAccess.
	rows, err := app.DB.Query(ctx,
		`SELECT a.id::text
		   FROM actor a
		   JOIN structure_room ss ON ss.id = a.inside_room_id
		   JOIN actor_need an ON an.actor_id = a.id AND an.key = 'tiredness'
		  WHERE a.login_username IS NOT NULL
		    AND a.inside_structure_id IS NOT NULL
		    AND a.inside_room_id IS NOT NULL
		    AND a.sleeping_until IS NULL
		    AND a.last_pc_input_at IS NOT NULL
		    AND a.last_pc_input_at < NOW() - ($1::int * INTERVAL '1 minute')
		    AND ss.kind = 'private'
		    AND an.value >= $2::int
		    AND EXISTS (
		      SELECT 1 FROM room_access sa
		       WHERE sa.actor_id = a.id
		         AND sa.room_id = a.inside_room_id
		         AND sa.active = true
		    )`,
		idleMinutes, minTiredness,
	)
	if err != nil {
		return fmt.Errorf("auto-bed candidates query: %w", err)
	}
	var candidateIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("auto-bed scan: %w", err)
		}
		candidateIDs = append(candidateIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("auto-bed iter: %w", err)
	}

	for _, id := range candidateIDs {
		if _, sleepErr := app.executePCSleep(ctx, id); sleepErr != nil {
			log.Printf("auto-bed executePCSleep(%s): %v", id, sleepErr)
		}
	}
	return nil
}

// loadIdleSleepMinutes reads the pc_idle_sleep_minutes setting via
// the shared loadIntSetting helper (defined in needs.go). Default 5.
// Logs + falls back to the default on parse error.
func (app *App) loadIdleSleepMinutes(ctx context.Context) int {
	return app.loadIntSetting(ctx, "pc_idle_sleep_minutes", 5)
}

// loadIdleSleepMinTiredness reads the pc_idle_sleep_min_tiredness
// setting (ZBBS-150). Default 10. Auto-bed will not fire on a PC
// whose tiredness is below this value, even if every other gate
// passes.
func (app *App) loadIdleSleepMinTiredness(ctx context.Context) int {
	return app.loadIntSetting(ctx, "pc_idle_sleep_min_tiredness", 10)
}

// loadPCSleepMaxDurationHours reads the pc_sleep_max_duration_hours
// setting (ZBBS-150). Default 12. executePCSleep uses this to set
// sleeping_until = NOW + N hours as a safety cap; recovery and
// checkout typically wake before this.
func (app *App) loadPCSleepMaxDurationHours(ctx context.Context) int {
	return app.loadIntSetting(ctx, "pc_sleep_max_duration_hours", 12)
}

// pcSleepResult carries the outcome of an executePCSleep call, returned
// to the manual /pc/sleep handler so the response can include the
// computed wake time.
type pcSleepResult struct {
	WakeAt time.Time // UTC; zero when result wasn't a fresh transition
}

// executePCSleep beds the PC. Sets sleeping_until = NOW +
// pc_sleep_max_duration_hours (safety cap; ZBBS-150 dropped the
// dawn-anchor) and stamps last_tiredness_recovery_at = NOW so the
// recovery sweep starts crediting from the bed-down moment.
// Broadcasts pc_sleep_started. Idempotent — returns silently when
// the PC is already sleeping (UPDATE ... WHERE sleeping_until IS NULL
// guards against the race). Manual handlers should pre-validate
// lodger status before calling; the auto-bed path validates inline
// (see autoBedIdleLodgers).
//
// Wake target is normally hit via the rested or checkout branches in
// wakeExpiredSleepers, not the safety cap. A max-tiredness PC reaches
// 0 in ~4 wall-clock hours at the default 0.1/min rate; the 12h cap
// is a backstop, not the expected wake.
func (app *App) executePCSleep(ctx context.Context, actorID string) (pcSleepResult, error) {
	// Defensive clamp: a misconfigured setting (zero, negative, absurd)
	// mustn't produce an immediately-expired sleep_until or a
	// multi-day pin. loadIntSetting falls back on parse errors but
	// doesn't validate range.
	maxHours := app.loadPCSleepMaxDurationHours(ctx)
	if maxHours <= 0 || maxHours > 24 {
		maxHours = 12
	}
	wakeAt := time.Now().UTC().Add(time.Duration(maxHours) * time.Hour)

	tag, err := app.DB.Exec(ctx,
		`UPDATE actor SET
		    sleeping_until = $1,
		    last_tiredness_recovery_at = NOW()
		  WHERE id = $2::uuid
		    AND login_username IS NOT NULL
		    AND sleeping_until IS NULL`,
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
// PC-only via login_username gate so an NPC id can't trigger a
// pc_sleep_ended broadcast.
func (app *App) executePCWake(ctx context.Context, actorID string) error {
	tag, err := app.DB.Exec(ctx,
		`UPDATE actor SET sleeping_until = NULL
		  WHERE id = $1::uuid
		    AND login_username IS NOT NULL
		    AND sleeping_until IS NOT NULL`,
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

// nextDawnAt was the pre-ZBBS-150 wake-target calculator. Removed
// when the recovery-based wake replaced the dawn anchor; sleep
// duration tracks tiredness recovery + checkout, not the world clock.
// resetSleptTiredness in world_rotation.go still fires at dawn for
// inside-structure NPCs, unchanged.
