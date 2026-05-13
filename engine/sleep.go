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
// NPC sleep (ZBBS-175 + ZBBS-HOME-204): mirrors the PC mechanism.
// NPCs auto-sleep when they arrive at their home_structure_id
// off-shift, regardless of current tiredness — home is the resting
// state by default (HOME-204 dropped the threshold gate so an NPC
// who arrives home pre-rested still beds down for the night, instead
// of standing in the house while the needs ticker quietly ticks
// them back to tired). Recovery flows through
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
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math/rand"
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
			// ZBBS-HOME-281: NPC backstop for home==work vendors who
			// never trigger maybeNPCAutoSleep via the arrival path
			// (they never leave their structure). Without this they
			// accumulate tiredness from needs_tick indefinitely.
			if err := app.autoBedAtHomeNPCs(ctx); err != nil {
				log.Printf("sleep sweep auto-bed NPC: %v", err)
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
		// ZBBS-HOME-266: morning descent. If the waker is still a lodger
		// (active, non-expired room_access for their current room), walk
		// them down to the structure's common room — they'd realistically
		// stroll down for the morning rather than sit on their bed. PCs
		// whose room_access expired (checkout wake) skip this branch and
		// fall through to evictExpiredOccupants, which has its own
		// teleport-to-common-with-eviction narration.
		app.maybeWalkWokenLodgerToCommon(ctx, id)
	}
	return nil
}

// maybeWalkWokenLodgerToCommon is the morning-descent half of the
// lodger day cycle (ZBBS-HOME-266). When wakeExpiredSleepers fires the
// auto-wake for a PC, this function checks whether the PC just woke in
// a private bedroom they STILL have valid room_access for (i.e. mid-
// stay rested or safety-cap wake, not checkout eviction). If so, it
// moves them to the structure's common room and broadcasts a private
// room_event narration so the chat panel renders "you head downstairs"
// flavor.
//
// Gates (all must hold to fire the move):
//   - PC is inside a structure, in a structure_room of kind='private'.
//   - PC has an active room_access row for that exact room.
//   - The room_access row's expires_at is in the future (rules out
//     checkout-wake; eviction handles those).
//
// If the PC is in a non-private room (e.g. /pc/sleep at home in a
// non-lodging structure that only has a common room), do nothing —
// they're already where they should be.
//
// PCs who manually entered their bedroom without sleeping aren't
// affected; this function only runs from the wakeExpiredSleepers loop,
// which only iterates PCs that just woke from sleep.
func (app *App) maybeWalkWokenLodgerToCommon(ctx context.Context, actorID string) {
	var insideStructure sql.NullString
	var roomKind sql.NullString
	var accessActive bool
	var accessFuture bool
	err := app.DB.QueryRow(ctx, `
		SELECT a.inside_structure_id::text,
		       sr.kind,
		       COALESCE(ra.active, false) AS access_active,
		       COALESCE(ra.expires_at > NOW(), false) AS access_future
		  FROM actor a
		  LEFT JOIN structure_room sr ON sr.id = a.inside_room_id
		  LEFT JOIN room_access ra ON ra.actor_id = a.id AND ra.room_id = a.inside_room_id
		 WHERE a.id::text = $1
	`, actorID).Scan(&insideStructure, &roomKind, &accessActive, &accessFuture)
	if err != nil {
		log.Printf("maybeWalkWokenLodgerToCommon(%s): query: %v", actorID, err)
		return
	}
	if !insideStructure.Valid || roomKind.String != "private" || !accessActive || !accessFuture {
		return
	}
	moveResult, err := app.executePCMoveRoom(ctx, actorID, "common")
	if err != nil {
		log.Printf("maybeWalkWokenLodgerToCommon(%s): move: %v", actorID, err)
		return
	}
	if moveResult.Result != "ok" {
		log.Printf("maybeWalkWokenLodgerToCommon(%s): move rejected: %s", actorID, moveResult.Err)
		return
	}
	text := pickMorningDescentNarration()
	app.Hub.Broadcast(WorldEvent{
		Type: "room_event",
		Data: map[string]interface{}{
			"actor_id":     actorID,
			"actor_name":   "",
			"kind":         "morning_descent",
			"text":         text,
			"private":      true,
			"structure_id": insideStructure.String,
			"at":           time.Now().UTC().Format(time.RFC3339),
		},
	})
}

// morningDescentNarrations holds the engine-authored prose pool for
// the morning descent room_event. Random selection per fire; keep the
// pool small but varied so a player who sees several wakes doesn't
// read the same line twice in a row. Period-appropriate Salem voice.
var morningDescentNarrations = []string{
	"You rise from your bed, gather yourself, and make your way down to the common room.",
	"Morning finds you rested. You dress and head downstairs to see who is about.",
	"You wake to the day's first light, draw breath, and descend to the common floor.",
	"Stretching the stiffness from your bones, you head downstairs to greet the morning.",
	"You stir, splash water on your face, and make your way down to the common room.",
}

func pickMorningDescentNarration() string {
	return morningDescentNarrations[rand.Intn(len(morningDescentNarrations))]
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
		// Capture pre-move structure_id so the narration room_event tags
		// the right structure. The move itself is within-structure
		// (common→private inside the same lodging), so the structure_id
		// is invariant; reading before the move avoids a re-query after.
		var preMoveStructure sql.NullString
		if err := app.DB.QueryRow(ctx,
			`SELECT inside_structure_id::text FROM actor WHERE id::text = $1`,
			id,
		).Scan(&preMoveStructure); err != nil {
			log.Printf("auto-bed pre-move structure lookup(%s): %v", id, err)
			continue
		}
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
			continue
		}
		// ZBBS-HOME-266: auto-retire narration. Mirror of the morning
		// descent room_event, fires the climb-the-stairs flavor when
		// the engine moves an idle tired lodger from the common room
		// to their private bedroom and beds them down.
		if preMoveStructure.Valid {
			text := pickAutoRetireNarration()
			app.Hub.Broadcast(WorldEvent{
				Type: "room_event",
				Data: map[string]interface{}{
					"actor_id":     id,
					"actor_name":   "",
					"kind":         "auto_retire",
					"text":         text,
					"private":      true,
					"structure_id": preMoveStructure.String,
					"at":           time.Now().UTC().Format(time.RFC3339),
				},
			})
		}
	}
	return nil
}

// autoRetireNarrations holds the engine-authored prose pool for the
// evening retire room_event. Mirror of morningDescentNarrations on the
// other side of the lodger day cycle.
var autoRetireNarrations = []string{
	"Weariness settles in. You climb the stairs to your room and turn in for the night.",
	"You feel the day catching up to you. You make your way upstairs to bed.",
	"Your eyes grow heavy. You retreat to your room and lie down for the night.",
	"The pull of sleep grows strong. You head up to your room and settle into bed.",
	"You've had enough of the day. You climb the stairs and turn in for the night.",
}

func pickAutoRetireNarration() string {
	return autoRetireNarrations[rand.Intn(len(autoRetireNarrations))]
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

// maybeNPCAutoSleep evaluates auto-sleep eligibility for an NPC after
// a move commit and beds them when all gates pass. Called from
// applyArrivalSideEffects so the NPC sleeps on arrival home rather
// than via a periodic sweep — fires once per arrival, no cost while
// they're walking around.
//
// Gates:
//   - actor is an NPC (llm_memory_agent IS NOT NULL)
//   - inside_structure_id IS NOT NULL AND equals home_structure_id
//     OR inside_structure_id is a structure where the actor holds
//     active lodger status (ZBBS-WORK-204). Lodger boarders have
//     home_structure_id NULL but rest at their lodging structure;
//     the lodger-status branch keeps Ezekiel-the-blacksmith asleep
//     at the inn after he becomes a boarder rather than standing
//     awake all night because home is no longer a column-level
//     truth for him.
//   - sleeping_until IS NULL (not already sleeping)
//   - off-shift: NPCs without a schedule are treated as always
//     off-shift; scheduled NPCs are off-shift when current
//     world-local minute-of-day is outside [start, end] (with wrap
//     handling for midnight-crossing shifts like the tavernkeeper).
//     The on-shift branch is what protects a vendor briefly stepping
//     home for lunch from getting sleep-darted; the threshold-on-
//     tiredness gate that used to back this up was dropped in
//     ZBBS-HOME-204 because off-shift + at home should bed the NPC
//     unconditionally — the body rests at home by default.
//
// Errors are logged and swallowed — auto-sleep is opportunistic;
// failing to bed an NPC means they stay awake until the next arrival
// or until take_break recovers them.
func (app *App) maybeNPCAutoSleep(ctx context.Context, actorID string) {
	if actorID == "" {
		return
	}

	var (
		isNPC             bool
		atHome            bool
		alreadySleeping   bool
		hasSchedule       bool
		scheduleStartMin  int
		scheduleEndMin    int
		homeStructureID   sql.NullString
		insideStructureID sql.NullString
	)
	err := app.DB.QueryRow(ctx,
		`SELECT a.llm_memory_agent IS NOT NULL,
		        a.inside_structure_id IS NOT NULL
		            AND a.home_structure_id IS NOT NULL
		            AND a.inside_structure_id = a.home_structure_id,
		        a.sleeping_until IS NOT NULL,
		        a.schedule_start_minute IS NOT NULL AND a.schedule_end_minute IS NOT NULL,
		        COALESCE(a.schedule_start_minute, 0)::int,
		        COALESCE(a.schedule_end_minute,   0)::int,
		        a.home_structure_id::text,
		        a.inside_structure_id::text
		   FROM actor a
		  WHERE a.id = $1::uuid`,
		actorID,
	).Scan(&isNPC, &atHome, &alreadySleeping,
		&hasSchedule, &scheduleStartMin, &scheduleEndMin,
		&homeStructureID, &insideStructureID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("maybeNPCAutoSleep(%s): load row: %v", actorID, err)
		}
		return
	}
	if !isNPC || alreadySleeping {
		return
	}
	// Resting-place predicate: home OR lodger at inside_structure_id.
	// home is the cheap check (column equality); lodger is the
	// pay_ledger materialization (nights_stay row + lodger_until in
	// the future). Skip the lodger query when the home branch already
	// passes — most NPC arrivals home should not pay the extra round
	// trip.
	if !atHome {
		if !insideStructureID.Valid {
			return
		}
		isBoarderHere, err := app.isLodger(ctx, actorID, insideStructureID.String)
		if err != nil {
			log.Printf("maybeNPCAutoSleep(%s): isLodger: %v", actorID, err)
			return
		}
		if !isBoarderHere {
			return
		}
	}

	// Off-shift check. Unscheduled NPCs (Ezekiel, Josiah at the time
	// ZBBS-175 shipped) get treated as always-off-shift so they sleep
	// on every arrival home. Scheduled NPCs sleep only when off-shift
	// — the on-shift return below is what stops a vendor's quick stop
	// home for lunch from sleeping them.
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
		return
	}

	// ZBBS-176: refresh occupancy on the structure we just bedded down
	// in. The COUNT excludes sleeping_until > NOW() actors, so any
	// home-is-work case (e.g., a future tavernkeeper who lodges above
	// the bar) flips to unoccupied at sleep onset. Most homes don't
	// have occupied/unoccupied state machines, so this is a no-op
	// there — safe to call unconditionally.
	//
	// ZBBS-WORK-204: lodger boarders sleep at inside_structure_id
	// (their lodging structure), not home_structure_id (which is
	// NULL for them). Prefer inside when set so the refresh hits
	// the structure they actually bedded down in regardless of
	// which gate let them sleep.
	if insideStructureID.Valid {
		app.refreshStructureOccupancyState(ctx, insideStructureID.String)
	} else if homeStructureID.Valid {
		app.refreshStructureOccupancyState(ctx, homeStructureID.String)
	}
}

// autoBedAtHomeNPCs is the periodic backstop for the NPC sleep
// mechanism (ZBBS-HOME-281). maybeNPCAutoSleep fires only from
// applyArrivalSideEffects (npc_movement.go:701) — i.e. when an NPC
// physically arrives at a structure. Home==work vendors (John Ellis
// the tavernkeeper, Hannah Boggs the innkeeper — anyone tagged
// "Your home and work: X" in their prompt) never leave their
// structure during a normal day, so they never have an arrival
// event, so the arrival-triggered auto-sleep path never fires for
// them. Without this sweep, their tiredness accumulates from
// needs_tick (+1/hour, clamped at needMax=24, "exhausted" tier)
// with no offsetting sleep recovery. Symptom in prod 2026-05-13:
// village-wide exhaustion among home==work vendors.
//
// Home!=work NPCs (Josiah Thorne, Prudence Ward) are also caught
// here as defense-in-depth; the arrival path handles them in the
// normal case so this sweep usually finds them already asleep.
//
// Gates:
//   - actor is NPC (llm_memory_agent IS NOT NULL)
//   - inside_structure_id IS NOT NULL AND home_structure_id IS NOT NULL
//     AND inside_structure_id = home_structure_id
//   - sleeping_until IS NULL
//   - agent_override_until IS NULL OR <= NOW() (excludes vendors on
//     take_break, summon errands, and any reactor activity that
//     legitimately holds the actor; break_until sets override for
//     its duration so a vendor on break stays awake)
//   - off-shift: unscheduled NPCs are always eligible; scheduled
//     NPCs are eligible when local minute-of-day is OUTSIDE the
//     shift window. The CASE handles wrap-midnight shifts (e.g.
//     tavernkeeper 16:00–03:00).
//
// Errors logged but not propagated; the sweep continues.
func (app *App) autoBedAtHomeNPCs(ctx context.Context) error {
	cfg, err := app.loadWorldConfig(ctx)
	if err != nil {
		return fmt.Errorf("auto-bed NPC: load world config: %w", err)
	}
	loc := cfg.Location
	if loc == nil {
		loc = time.UTC
	}
	nowLocal := time.Now().In(loc)
	nowMinute := nowLocal.Hour()*60 + nowLocal.Minute()

	rows, err := app.DB.Query(ctx,
		`SELECT a.id::text, a.inside_structure_id::text
		   FROM actor a
		  WHERE a.llm_memory_agent IS NOT NULL
		    AND a.inside_structure_id IS NOT NULL
		    AND a.home_structure_id IS NOT NULL
		    AND a.inside_structure_id = a.home_structure_id
		    AND a.sleeping_until IS NULL
		    AND (a.agent_override_until IS NULL OR a.agent_override_until <= NOW())
		    AND (
		      a.schedule_start_minute IS NULL
		      OR a.schedule_end_minute IS NULL
		      -- Off-shift: complement of the in-shift predicate in
		      -- wakeExpiredNPCSleepers. Same CASE shape so the two
		      -- expressions stay aligned if a future change touches
		      -- the wrap-midnight handling.
		      OR CASE
		          WHEN a.schedule_start_minute <= a.schedule_end_minute THEN
		              NOT ($1::int >= a.schedule_start_minute
		                   AND $1::int <  a.schedule_end_minute)
		          ELSE
		              NOT ($1::int >= a.schedule_start_minute
		                   OR  $1::int <  a.schedule_end_minute)
		      END
		    )`,
		nowMinute,
	)
	if err != nil {
		return fmt.Errorf("auto-bed NPC candidates query: %w", err)
	}
	type candidate struct {
		ID          string
		StructureID string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ID, &c.StructureID); err != nil {
			rows.Close()
			return fmt.Errorf("auto-bed NPC scan: %w", err)
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("auto-bed NPC iter: %w", err)
	}

	for _, c := range candidates {
		if _, err := app.executeNPCSleep(ctx, c.ID); err != nil {
			log.Printf("auto-bed NPC executeNPCSleep(%s): %v", c.ID, err)
			continue
		}
		// Refresh structure occupancy. Mirror of maybeNPCAutoSleep's
		// tail — keeps any home==work structure's occupied/unoccupied
		// visual state in sync now that the keeper is asleep.
		app.refreshStructureOccupancyState(ctx, c.StructureID)
	}
	return nil
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
// condition has fired and broadcasts npc_sleep_ended for each. Three
// wake conditions, ORed:
//
//   - sleeping_until <= NOW(): safety cap.
//   - tiredness <= 0: rested (continuous recovery via
//     tiredness_recovery_sweep brought them to 0).
//   - ZBBS-HOME-262: current local minute-of-day is in the NPC's
//     shift window. executeNPCSleep sets sleeping_until = NOW + 12h
//     regardless of how close the actor's shift start is. An NPC
//     who beds down ~an hour before dawn (the common case for
//     keepers and morning-shift villagers) will otherwise sleep
//     through almost their entire shift because tiredness recovery
//     at 0.04/min only reaches 0 from a ~30 starting value over
//     ~12.5h — strictly slower than the 12h cap, so the safety cap
//     and tiredness floor both miss the shift-start boundary.
//     Observed in prod 2026-05-11: Elizabeth Ellis (6:00-19:00
//     shift) bed down at 5:19am EDT and Josiah Thorne (9:00-17:00
//     shift) bed down at 8:49am EDT; both were still asleep at
//     9:15am with tiredness already recovered to 2 and 8 respectively
//     but stuck because neither cap had fired yet. Wake-at-shift
//     fixes both forward (future sleeps cap naturally at shift
//     start via this OR clause) and backward (already-asleep NPCs
//     get woken at the next sweep tick).
//
// PC-side wakeExpiredSleepers also has a room_access checkout
// branch; NPCs don't hold room_access today (they sleep at their
// own home), so that branch is omitted here.
//
// Atomic per-row via UPDATE ... RETURNING. Broadcast reason "auto"
// distinct from a future manual wake (e.g., knock-wake from the
// entry-policy/knock task) which would carry "knock" or similar.
//
// Shift-window math: schedule_start_minute / schedule_end_minute
// are minute-of-day integers in the world timezone (America/New_York).
// The CASE handles wrap-midnight shifts (e.g., tavernkeeper
// 16:00-03:00 has start=960, end=180; an NPC sleeping at 22:00 with
// local_min=1320 should be considered on-shift because 1320 >= 960
// OR 1320 < 180 → first disjunct true). The world timezone is fixed
// at the application level (defaultTimezone in world_phase.go); we
// derive local minute-of-day in Go and pass it as a single int64 param
// to keep business logic out of SQL.
func (app *App) wakeExpiredNPCSleepers(ctx context.Context) error {
	cfg, err := app.loadWorldConfig(ctx)
	if err != nil {
		return fmt.Errorf("wake NPC: load world config: %w", err)
	}
	loc := cfg.Location
	if loc == nil {
		loc = time.UTC
	}
	nowLocal := time.Now().In(loc)
	nowMinute := nowLocal.Hour()*60 + nowLocal.Minute()
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
		      OR (
		        a.schedule_start_minute IS NOT NULL
		        AND a.schedule_end_minute IS NOT NULL
		        AND CASE
		            WHEN a.schedule_start_minute <= a.schedule_end_minute THEN
		                $1::int >= a.schedule_start_minute
		                AND $1::int < a.schedule_end_minute
		            ELSE
		                $1::int >= a.schedule_start_minute
		                OR  $1::int < a.schedule_end_minute
		        END
		      )
		    )
		 RETURNING id::text, inside_structure_id::text`,
		nowMinute,
	)
	if err != nil {
		return fmt.Errorf("wake expired NPC query: %w", err)
	}
	defer rows.Close()
	type woken struct {
		id          string
		structureID sql.NullString
	}
	var wakes []woken
	for rows.Next() {
		var w woken
		if err := rows.Scan(&w.id, &w.structureID); err != nil {
			return fmt.Errorf("wake expired NPC scan: %w", err)
		}
		wakes = append(wakes, w)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("wake expired NPC iter: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, w := range wakes {
		app.Hub.Broadcast(WorldEvent{
			Type: "npc_sleep_ended",
			Data: map[string]interface{}{
				"actor_id": w.id,
				"reason":   "auto",
				"at":       now,
			},
		})
		// ZBBS-176: refresh occupancy on the structure the NPC woke up
		// in. Mirrors the auto-sleep refresh — the COUNT now sees the
		// actor as not-sleeping and flips the structure's visual state
		// back if it had been driven by sleep alone.
		if w.structureID.Valid {
			app.refreshStructureOccupancyState(ctx, w.structureID.String)
		}
	}
	return nil
}
