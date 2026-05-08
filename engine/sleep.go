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
// NPC sleep (ZBBS-175): mirrors the PC mechanism. NPCs auto-sleep
// when they arrive at their home_structure_id off-shift with
// tiredness >= npc_auto_sleep_min_tiredness. Recovery flows through
// the same tiredness_recovery_sweep that handles break_until /
// sleeping_until generically (no PC gate). Wake fires via
// wakeExpiredNPCSleepers — same conditions as PCs minus the
// room_access checkout branch (NPCs don't book rooms in v1). The
// inside-structure branch of resetSleptTiredness was dropped when
// NPC sleep landed; the decorative-NPC unconditional reset stays
// because decoratives have no behavior to walk themselves home.
// Reactive ticks to sleeping NPCs are short-circuited at the top of
// runAgentTick — knock-wake awaits the entry-policy/knock task.

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
			if err := app.wakeExpiredNPCSleepers(ctx); err != nil {
				log.Printf("sleep sweep wake NPC: %v", err)
			}
			// Eviction runs AFTER wake so a sleeping checked-out lodger
			// gets woken first (housekeeping knock) and then teleported
			// to the common room. An awake checked-out lodger goes
			// straight to the eviction step. Either way they don't end
			// up squatting in a room they no longer have access to.
			if err := app.evictExpiredOccupants(ctx); err != nil {
				log.Printf("sleep sweep evict: %v", err)
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

// autoBedIdleLodgers finds connected PCs idle anywhere in a structure
// where they hold an active private room_access, walks them up to
// their bedroom (server-side), and beds them via executePCSleep.
//
// Pre-ZBBS-168 the gate required the PC to ALREADY be in the private
// room. That was a half-mechanic: if a lodger walked back down to the
// bar to chat with the keeper and idled there, nothing happened — they
// had a room they couldn't use without a client UI for /pc/move-room
// (which doesn't exist yet). The widened gate makes lodging a fully
// passive mechanic: pay → exist → eventually fall asleep upstairs.
//
// Gates:
//   - PC has an active room_access row for a private room IN their
//     current inside_structure_id (no cross-structure auto-bedding;
//     a PC at the blacksmith doesn't get teleported home).
//   - tiredness >= pc_idle_sleep_min_tiredness (default 10): a PC who
//     just sat down in the tavern while fresh isn't knocked out.
//   - last_pc_input_at older than pc_idle_sleep_minutes (default 15).
//   - sleeping_until is NULL (not already sleeping).
//
// For each candidate, executePCMoveRoom("private") fires first. It's
// idempotent — if the PC is already in their bedroom, the UPDATE
// re-applies the same room_id and pc_room_changed re-broadcasts
// (cheap; the client diffs and ignores no-ops). If the helper rejects
// (race took the PC out of the structure), the sleep call is skipped.
func (app *App) autoBedIdleLodgers(ctx context.Context) error {
	idleMinutes := app.loadIdleSleepMinutes(ctx)
	if idleMinutes <= 0 {
		// Configured to disable auto-bed; manual /sleep only.
		return nil
	}
	minTiredness := app.loadIdleSleepMinTiredness(ctx)

	rows, err := app.DB.Query(ctx,
		`SELECT a.id::text
		   FROM actor a
		   JOIN actor_need an ON an.actor_id = a.id AND an.key = 'tiredness'
		  WHERE a.login_username IS NOT NULL
		    AND a.inside_structure_id IS NOT NULL
		    AND a.sleeping_until IS NULL
		    AND a.last_pc_input_at IS NOT NULL
		    AND a.last_pc_input_at < NOW() - ($1::int * INTERVAL '1 minute')
		    AND an.value >= $2::int
		    AND EXISTS (
		      SELECT 1 FROM room_access sa
		      JOIN structure_room sr ON sr.id = sa.room_id
		       WHERE sa.actor_id = a.id
		         AND sa.active = true
		         AND sr.kind = 'private'
		         AND sr.structure_id = a.inside_structure_id
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
		moveResult, err := app.executePCMoveRoom(ctx, id, "private")
		if err != nil {
			log.Printf("auto-bed move(%s): %v", id, err)
			continue
		}
		if moveResult.Result != "ok" {
			// Race: PC left the structure between candidate query and
			// the move. Skip the sleep call — they'll be re-evaluated
			// next sweep if they're still eligible.
			log.Printf("auto-bed move(%s) rejected: %s", id, moveResult.Err)
			continue
		}
		if _, sleepErr := app.executePCSleep(ctx, id); sleepErr != nil {
			log.Printf("auto-bed executePCSleep(%s): %v", id, sleepErr)
		}
	}
	return nil
}

// loadIdleSleepMinutes reads the pc_idle_sleep_minutes setting via
// the shared loadIntSetting helper (defined in needs.go). Default 15
// (ZBBS-168 — was 5 in ZBBS-132 when auto-bed required the PC to
// already be in their bedroom; with the widened gate, idle anywhere
// in the lodger's structure now triggers, so a longer threshold
// matches "I forgot I was AFK" pacing better than "I went to the
// bathroom"). Logs + falls back to the default on parse error.
func (app *App) loadIdleSleepMinutes(ctx context.Context) int {
	return app.loadIntSetting(ctx, "pc_idle_sleep_minutes", 15)
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

	// ZBBS-169 narration: private second-person line in the PC's
	// brown box so the bed-down moment has a beat instead of just a
	// silent state flip. Same pattern as narrateConsumeSelf
	// (per-actor room_event with private=true, client filters by
	// PC's actor_id).
	app.Hub.Broadcast(WorldEvent{
		Type: "room_event",
		Data: map[string]interface{}{
			"actor_id":   actorID,
			"actor_name": "",
			"kind":       "sleep",
			"text":       "You settle into your bed and drift off.",
			"private":    true,
			"at":         time.Now().UTC().Format(time.RFC3339),
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

// =============================================================================
// NPC sleep (ZBBS-175). Mirrors PC sleep above with the differences
// documented in the file header.
// =============================================================================

// loadNPCSleepMaxDurationHours reads the npc_sleep_max_duration_hours
// setting. Default 12. Mirrors pc_sleep_max_duration_hours but kept
// as its own knob so PC and NPC sleep caps can diverge if needed.
func (app *App) loadNPCSleepMaxDurationHours(ctx context.Context) int {
	return app.loadIntSetting(ctx, "npc_sleep_max_duration_hours", 12)
}

// loadNPCAutoSleepMinTiredness reads the npc_auto_sleep_min_tiredness
// setting. Default 10 (mirrors pc_idle_sleep_min_tiredness). NPCs
// arriving home below this tiredness skip the auto-sleep trigger so
// drop-by visits don't knock them out.
func (app *App) loadNPCAutoSleepMinTiredness(ctx context.Context) int {
	return app.loadIntSetting(ctx, "npc_auto_sleep_min_tiredness", 10)
}

// maybeNPCAutoSleep evaluates auto-sleep eligibility for an NPC after
// a move commit and beds them when all gates pass. Called from
// applyArrivalSideEffects so the NPC sleeps on arrival home rather
// than via a periodic sweep — fires once per arrival, no cost while
// they're walking around.
//
// Gates:
//   - actor is an NPC (llm_memory_agent IS NOT NULL)
//   - inside_structure_id IS NOT NULL AND equals home_structure_id
//   - sleeping_until IS NULL (not already sleeping)
//   - actor_need.tiredness >= npc_auto_sleep_min_tiredness (default 10)
//   - off-shift: NPCs without a schedule are treated as always
//     off-shift; scheduled NPCs are off-shift when current
//     world-local minute-of-day is outside [start, end] (with wrap
//     handling for midnight-crossing shifts like the tavernkeeper)
//
// Errors are logged and swallowed — auto-sleep is opportunistic;
// failing to bed an NPC means they stay awake until the next arrival
// or until take_break recovers them.
func (app *App) maybeNPCAutoSleep(ctx context.Context, actorID string) {
	if actorID == "" {
		return
	}
	minTiredness := app.loadNPCAutoSleepMinTiredness(ctx)

	var (
		isNPC            bool
		atHome           bool
		alreadySleeping  bool
		tiredness        int
		hasSchedule      bool
		scheduleStartMin int
		scheduleEndMin   int
	)
	err := app.DB.QueryRow(ctx,
		`SELECT a.llm_memory_agent IS NOT NULL,
		        a.inside_structure_id IS NOT NULL
		            AND a.home_structure_id IS NOT NULL
		            AND a.inside_structure_id = a.home_structure_id,
		        a.sleeping_until IS NOT NULL,
		        COALESCE((SELECT value FROM actor_need WHERE actor_id = a.id AND key = 'tiredness'), 0)::int,
		        a.schedule_start_minute IS NOT NULL AND a.schedule_end_minute IS NOT NULL,
		        COALESCE(a.schedule_start_minute, 0)::int,
		        COALESCE(a.schedule_end_minute,   0)::int
		   FROM actor a
		  WHERE a.id = $1::uuid`,
		actorID,
	).Scan(&isNPC, &atHome, &alreadySleeping, &tiredness,
		&hasSchedule, &scheduleStartMin, &scheduleEndMin)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("maybeNPCAutoSleep(%s): load row: %v", actorID, err)
		}
		return
	}
	if !isNPC || !atHome || alreadySleeping || tiredness < minTiredness {
		return
	}

	// Off-shift check. Unscheduled NPCs (Ezekiel, Josiah at the time
	// this shipped) get treated as always-off-shift so they sleep
	// whenever they arrive home tired.
	if hasSchedule {
		cfg, err := app.loadWorldConfig(ctx)
		if err != nil {
			log.Printf("maybeNPCAutoSleep(%s): load config: %v", actorID, err)
			return
		}
		now := time.Now().In(cfg.Location)
		nowMin := now.Hour()*60 + now.Minute()
		var onShift bool
		if scheduleStartMin <= scheduleEndMin {
			onShift = nowMin >= scheduleStartMin && nowMin < scheduleEndMin
		} else {
			// Wraps midnight (e.g. 16:00–03:00 next day).
			onShift = nowMin >= scheduleStartMin || nowMin < scheduleEndMin
		}
		if onShift {
			return
		}
	}

	if _, err := app.executeNPCSleep(ctx, actorID); err != nil {
		log.Printf("maybeNPCAutoSleep(%s): executeNPCSleep: %v", actorID, err)
	}
}

// executeNPCSleep beds an NPC. NPC analog of executePCSleep — sets
// sleeping_until = NOW + npc_sleep_max_duration_hours and stamps
// last_tiredness_recovery_at = NOW so the recovery sweep starts
// crediting from the bed-down moment. Idempotent: returns silently
// when the NPC is already sleeping (the UPDATE WHERE clause guards).
// Broadcasts npc_sleep_started on a fresh transition. NPC-only via
// llm_memory_agent IS NOT NULL gate so a stray PC id can't trigger
// npc_sleep_started.
func (app *App) executeNPCSleep(ctx context.Context, actorID string) (time.Time, error) {
	maxHours := app.loadNPCSleepMaxDurationHours(ctx)
	if maxHours <= 0 || maxHours > 24 {
		maxHours = 12
	}
	wakeAt := time.Now().UTC().Add(time.Duration(maxHours) * time.Hour)

	tag, err := app.DB.Exec(ctx,
		`UPDATE actor SET
		    sleeping_until = $1,
		    last_tiredness_recovery_at = NOW()
		  WHERE id = $2::uuid
		    AND llm_memory_agent IS NOT NULL
		    AND sleeping_until IS NULL`,
		wakeAt, actorID,
	)
	if err != nil {
		return time.Time{}, fmt.Errorf("update sleeping_until: %w", err)
	}
	if tag.RowsAffected() != 1 {
		// Already sleeping or not an NPC. Soft no-op.
		return time.Time{}, nil
	}

	app.Hub.Broadcast(WorldEvent{
		Type: "npc_sleep_started",
		Data: map[string]interface{}{
			"actor_id": actorID,
			"wake_at":  wakeAt.Format(time.RFC3339),
			"at":       time.Now().UTC().Format(time.RFC3339),
		},
	})
	return wakeAt, nil
}

// wakeExpiredNPCSleepers clears sleeping_until on any NPC whose wake
// condition has fired and broadcasts npc_sleep_ended for each. Two
// wake conditions, ORed:
//
//   - sleeping_until <= NOW(): safety cap.
//   - tiredness <= 0: rested (continuous recovery via
//     tiredness_recovery_sweep brought them to 0).
//
// PC-side wakeExpiredSleepers also has a room_access checkout
// branch; NPCs don't hold room_access today (they sleep at their
// own home), so that branch is omitted here.
//
// Atomic per-row via UPDATE ... RETURNING. Broadcast reason "auto"
// distinct from a future manual wake (e.g., knock-wake from the
// entry-policy/knock task) which would carry "knock" or similar.
func (app *App) wakeExpiredNPCSleepers(ctx context.Context) error {
	rows, err := app.DB.Query(ctx,
		`UPDATE actor a
		    SET sleeping_until = NULL
		  WHERE a.llm_memory_agent IS NOT NULL
		    AND a.sleeping_until IS NOT NULL
		    AND (
		      a.sleeping_until <= NOW()
		      OR EXISTS (
		        SELECT 1 FROM actor_need an
		         WHERE an.actor_id = a.id
		           AND an.key = 'tiredness'
		           AND an.value <= 0
		      )
		    )
		 RETURNING id::text`,
	)
	if err != nil {
		return fmt.Errorf("wake expired NPC query: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("wake expired NPC scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("wake expired NPC iter: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, id := range ids {
		app.Hub.Broadcast(WorldEvent{
			Type: "npc_sleep_ended",
			Data: map[string]interface{}{
				"actor_id": id,
				"reason":   "auto",
				"at":       now,
			},
		})
	}
	return nil
}
