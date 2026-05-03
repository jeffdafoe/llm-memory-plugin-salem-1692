package main

// Summon errand state machine — the multi-leg messenger flow that
// replaced the v1 teleport summon (ZBBS-105/107).
//
// Lifecycle (state column on summon_errand):
//
//   dispatched           — summoner walks toward nearest summon_point
//   summoner_at_point    — summoner arrived; ring narration; messenger dispatched
//   messenger_at_point   — messenger arrived at summon_point; chat timer
//   messenger_to_target  — chat elapsed; messenger walks toward target's
//                          frozen-at-this-moment position
//   messenger_at_target  — messenger arrived at target; delivery timer
//   messenger_to_summoner — only on unavailable branch; messenger walks
//                          back to summoner's current position to report
//   messenger_returning  — messenger walks back to its origin
//   done                 — terminal success
//   failed               — terminal error (no path, target gone, etc.)
//
// State is fully held in the summon_errand row. Transitions are driven
// by two hooks:
//
//   1. advanceErrandFromArrival(ctx, npcID) — called from applyArrival
//      whenever any actor arrives. Looks up active errand involving
//      that actor and advances walk-driven transitions.
//   2. tickErrands(ctx) — periodic ticker (errandTickInterval) that
//      advances timer-driven transitions (chat_at_summon_until,
//      chat_at_target_until elapsing).
//
// No long-running per-errand goroutine. Engine restart loses in-flight
// walks (matching today's behavior elsewhere) but the errand row is
// preserved for inspection and the goroutine architecture stays light.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Errand timing constants. Chat pauses are randomized within the same
// range as the task design (4–10s). Override stamping is conservative:
// 10 minutes is much longer than any plausible errand at typical walking
// speed, so we don't have to predict precise duration up front. The
// override is cleared on terminal transition.
const (
	errandTickInterval     = 5 * time.Second
	errandChatMinSeconds   = 4
	errandChatMaxSeconds   = 10
	errandOverrideDuration = 10 * time.Minute
	// errandTargetTolerance is how close the messenger has to be to
	// target_dispatch_x/y for a delivery to count as on-target. Beyond
	// this, the target has effectively moved and we branch to
	// unavailable. Generous because pathfinding rounds to tile centers.
	errandTargetTolerance = 64.0
)

// dispatchSummonErrand is the v2 entry point for the summon tool.
// Validates target, finds the nearest summon_point and messenger,
// inserts the errand row, stamps override on the summoner, and starts
// the summoner's walk.
//
// Returns a summonResult shaped the same as the v1 path so the agent
// commit code can write its audit row without knowing whether the v1
// teleport or the v2 errand chain ran.
func (app *App) dispatchSummonErrand(ctx context.Context, summoner *agentNPCRow, req summonRequest) summonResult {
	targetName := strings.TrimSpace(req.TargetName)
	if targetName == "" {
		return summonResult{Result: "rejected", Err: "missing target"}
	}

	// Resolve target. Unknown is allowed — the messenger will walk to
	// the summoner's last-known coords with a refusal speech. For a
	// known target, classify by driver field.
	var targetID, targetDisplayName string
	var targetX, targetY float64
	var targetAgent, targetUsername *string
	err := app.DB.QueryRow(ctx,
		`SELECT id, display_name, current_x, current_y, llm_memory_agent, login_username
		   FROM actor
		  WHERE LOWER(display_name) = LOWER($1)
		  LIMIT 1`,
		targetName,
	).Scan(&targetID, &targetDisplayName, &targetX, &targetY, &targetAgent, &targetUsername)
	targetKind := "unknown"
	if err == nil {
		switch {
		case targetID == summoner.ID:
			return summonResult{Result: "rejected", Err: "cannot summon yourself"}
		case targetAgent != nil && *targetAgent != "":
			targetKind = "va"
		case targetUsername != nil && *targetUsername != "":
			targetKind = "pc"
		default:
			targetKind = "nonva"
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return summonResult{Result: "failed", Err: fmt.Sprintf("look up target: %v", err)}
	} else {
		// Unknown target — keep the requested name but blank the rest.
		// Messenger walks to the summoner's current position to
		// deliver the refusal (no real target_dispatch coords).
		targetDisplayName = targetName
		targetX, targetY = summoner.CurrentX, summoner.CurrentY
	}

	// Reject if the summoner already has an active errand. Replaces
	// the v1 audit-log cooldown with a stronger active-state check.
	var existing string
	err = app.DB.QueryRow(ctx,
		`SELECT id FROM summon_errand
		  WHERE summoner_id = $1 AND state NOT IN ('done', 'failed')
		  LIMIT 1`,
		summoner.ID,
	).Scan(&existing)
	if err == nil {
		return summonResult{Result: "rejected", Err: "a messenger is still running an earlier errand for you"}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return summonResult{Result: "failed", Err: fmt.Sprintf("check active errand: %v", err)}
	}

	// Find nearest summon_point. Done up-front because if no point
	// exists the entire flow is impossible.
	pointID, pointX, pointY, err := app.findNearestSummonPoint(ctx, summoner.CurrentX, summoner.CurrentY)
	if err != nil {
		return summonResult{Result: "rejected", Err: "no summon point exists in the village"}
	}

	// Find nearest messenger excluding the summoner (in case the
	// summoner happens to carry the messenger attribute — they can't
	// run their own errand).
	messengerID, messengerX, messengerY, err := app.findNearestMessenger(ctx, summoner.CurrentX, summoner.CurrentY, summoner.ID)
	if err != nil {
		return summonResult{Result: "rejected", Err: "no messenger is available to run errands right now"}
	}

	// Insert the errand row. target_dispatch coords are filled in here
	// with the target's CURRENT position; they'll be overwritten when
	// the messenger actually starts the toward-target walk (chat
	// elapsed → messenger_to_target). For the unknown branch they stay
	// at the summoner's coords so the unavail walk is short.
	var errandID string
	if err := app.DB.QueryRow(ctx, `
		INSERT INTO summon_errand (
		    summoner_id, messenger_id, target_name, summon_point_id, reason,
		    state, target_kind,
		    messenger_origin_x, messenger_origin_y,
		    target_dispatch_x, target_dispatch_y
		) VALUES (
		    $1, $2, $3, $4, $5,
		    'dispatched', $6,
		    $7, $8,
		    $9, $10
		) RETURNING id
	`,
		summoner.ID, messengerID, targetDisplayName, pointID, strings.TrimSpace(req.Reason),
		targetKind,
		messengerX, messengerY,
		targetX, targetY,
	).Scan(&errandID); err != nil {
		return summonResult{Result: "failed", Err: fmt.Sprintf("insert errand: %v", err)}
	}

	// Override-stamp the summoner so the LLM doesn't tick again
	// mid-walk. Cleared on terminal transition. Also clear inside so
	// the client renders them stepping outside as they walk.
	overrideUntil := time.Now().Add(errandOverrideDuration)
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor
		    SET agent_override_until = $2,
		        last_shift_tick_at = $2,
		        inside = false,
		        inside_structure_id = NULL
		  WHERE id = $1`,
		summoner.ID, overrideUntil,
	); err != nil {
		log.Printf("summon-dispatch: stamp summoner override: %v", err)
	}

	// Kick off the summoner walk. Failure here flips state to failed
	// so the ticker can clean up.
	if _, err := app.startNPCWalk(ctx, summoner.ID, pointX, pointY, defaultNPCSpeed); err != nil {
		log.Printf("summon-dispatch: walk summoner to point: %v", err)
		app.failErrand(ctx, errandID, fmt.Sprintf("summoner walk: %v", err))
		return summonResult{Result: "failed", Err: "could not walk to summon point"}
	}

	return summonResult{
		Result:            "ok",
		TargetID:          targetID,
		TargetDisplayName: targetDisplayName,
	}
}

// findNearestSummonPoint returns the village_object id and tile-center
// coords of the closest summon_point-tagged placement. Errors with
// pgx.ErrNoRows when none exist.
func (app *App) findNearestSummonPoint(ctx context.Context, fromX, fromY float64) (string, float64, float64, error) {
	var id string
	var x, y float64
	err := app.DB.QueryRow(ctx, `
		SELECT o.id::text, o.x, o.y
		  FROM village_object o
		  JOIN village_object_tag t ON t.object_id = o.id
		 WHERE t.tag = 'summon_point'
		 ORDER BY ((o.x - $1) * (o.x - $1) + (o.y - $2) * (o.y - $2)) ASC
		 LIMIT 1
	`, fromX, fromY).Scan(&id, &x, &y)
	return id, x, y, err
}

// findNearestMessenger returns the closest non-VA actor carrying the
// messenger attribute, excluding the supplied actor (typically the
// summoner). Excludes any actor already running an active errand —
// the unique partial index would catch a double-assign at INSERT time
// but reading here avoids the round-trip-failure shape.
func (app *App) findNearestMessenger(ctx context.Context, fromX, fromY float64, excludeActorID string) (string, float64, float64, error) {
	var id string
	var x, y float64
	err := app.DB.QueryRow(ctx, `
		SELECT a.id::text, a.current_x, a.current_y
		  FROM actor a
		  JOIN actor_attribute aa ON aa.actor_id = a.id
		 WHERE aa.slug = 'messenger'
		   AND a.id != $3
		   AND a.llm_memory_agent IS NULL
		   AND NOT EXISTS (
		       SELECT 1 FROM summon_errand e
		        WHERE e.messenger_id = a.id
		          AND e.state NOT IN ('done', 'failed')
		   )
		 ORDER BY ((a.current_x - $1) * (a.current_x - $1) + (a.current_y - $2) * (a.current_y - $2)) ASC
		 LIMIT 1
	`, fromX, fromY, excludeActorID).Scan(&id, &x, &y)
	return id, x, y, err
}

// advanceErrandFromArrival is the walk-driven transition hook. Called
// from applyArrival for every actor arrival; cheaply returns when the
// actor isn't part of any active errand.
//
// Walk-driven transitions:
//   - dispatched              → summoner_at_point      (summoner arrived)
//   - summoner_at_point       → messenger_at_point     (messenger arrived at point)
//   - messenger_to_target     → messenger_at_target    (messenger arrived at target)
//   - messenger_to_summoner   → messenger_returning    (delivered refusal at summoner)
//   - messenger_returning     → done                   (messenger arrived at origin)
func (app *App) advanceErrandFromArrival(ctx context.Context, npcID string) {
	// Look up any active errand where this actor is the summoner OR
	// messenger. At most one for each role thanks to the partial
	// unique indexes, but we may get one row for each role if the
	// summoner is also a messenger (rejected at dispatch — defensive).
	rows, err := app.DB.Query(ctx, `
		SELECT id, state, summoner_id, messenger_id, summon_point_id,
		       target_kind, target_name, reason,
		       messenger_origin_x, messenger_origin_y,
		       target_dispatch_x, target_dispatch_y
		  FROM summon_errand
		 WHERE state NOT IN ('done', 'failed')
		   AND ($1 = summoner_id OR $1 = messenger_id)
	`, npcID)
	if err != nil {
		log.Printf("errand-advance: load: %v", err)
		return
	}
	defer rows.Close()

	type errandRow struct {
		ID                string
		State             string
		SummonerID        string
		MessengerID       string
		SummonPointID     string
		TargetKind        string
		TargetName        string
		Reason            string
		MessengerOriginX  float64
		MessengerOriginY  float64
		TargetDispatchX   float64
		TargetDispatchY   float64
	}
	var errands []errandRow
	for rows.Next() {
		var e errandRow
		if err := rows.Scan(&e.ID, &e.State, &e.SummonerID, &e.MessengerID,
			&e.SummonPointID, &e.TargetKind, &e.TargetName, &e.Reason,
			&e.MessengerOriginX, &e.MessengerOriginY,
			&e.TargetDispatchX, &e.TargetDispatchY); err != nil {
			continue
		}
		errands = append(errands, e)
	}
	rows.Close()

	for _, e := range errands {
		switch {
		case npcID == e.SummonerID && e.State == "dispatched":
			app.onSummonerAtPoint(ctx, e.ID, e.SummonerID, e.MessengerID, e.SummonPointID)
		case npcID == e.MessengerID && e.State == "summoner_at_point":
			app.onMessengerAtPoint(ctx, e.ID)
		case npcID == e.MessengerID && e.State == "messenger_to_target":
			app.onMessengerAtTarget(ctx, e.ID)
		case npcID == e.MessengerID && e.State == "messenger_to_summoner":
			app.onMessengerDeliveredRefusal(ctx, e.ID, e.MessengerID, e.TargetName, e.MessengerOriginX, e.MessengerOriginY)
		case npcID == e.MessengerID && e.State == "messenger_returning":
			app.onMessengerReturned(ctx, e.ID, e.MessengerID, e.SummonerID)
		}
	}
}

// onSummonerAtPoint advances dispatched → summoner_at_point. Fires
// the ring narration, snapshots the messenger's current position, and
// dispatches the messenger toward the summon_point.
func (app *App) onSummonerAtPoint(ctx context.Context, errandID, summonerID, messengerID, summonPointID string) {
	// Pull summoner display name and summon-point coords for narration
	// and walk targeting respectively.
	var summonerName string
	var pointX, pointY float64
	if err := app.DB.QueryRow(ctx,
		`SELECT a.display_name, o.x, o.y
		   FROM actor a, village_object o
		  WHERE a.id = $1 AND o.id = $2`,
		summonerID, summonPointID,
	).Scan(&summonerName, &pointX, &pointY); err != nil {
		app.failErrand(ctx, errandID, fmt.Sprintf("load summoner+point: %v", err))
		return
	}

	app.broadcastSummonRing(ctx, summonerID, summonerName, pointX, pointY)

	// Snapshot messenger's current position and stamp override.
	var messengerX, messengerY float64
	if err := app.DB.QueryRow(ctx,
		`SELECT current_x, current_y FROM actor WHERE id = $1`, messengerID,
	).Scan(&messengerX, &messengerY); err != nil {
		app.failErrand(ctx, errandID, fmt.Sprintf("load messenger pos: %v", err))
		return
	}
	overrideUntil := time.Now().Add(errandOverrideDuration)
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor
		    SET agent_override_until = $2,
		        last_shift_tick_at = $2,
		        inside = false,
		        inside_structure_id = NULL
		  WHERE id = $1`,
		messengerID, overrideUntil,
	); err != nil {
		log.Printf("errand: stamp messenger override: %v", err)
	}

	if _, err := app.DB.Exec(ctx, `
		UPDATE summon_errand
		   SET state = 'summoner_at_point',
		       messenger_origin_x = $2, messenger_origin_y = $3,
		       updated_at = now()
		 WHERE id = $1
	`, errandID, messengerX, messengerY); err != nil {
		log.Printf("errand: state→summoner_at_point: %v", err)
		return
	}

	// Walk messenger to summon_point. Failure terminates the errand.
	if _, err := app.startNPCWalk(ctx, messengerID, pointX, pointY, defaultNPCSpeed); err != nil {
		app.failErrand(ctx, errandID, fmt.Sprintf("walk messenger to point: %v", err))
	}
}

// onMessengerAtPoint advances summoner_at_point → messenger_at_point
// and starts the at-point chat timer. The next transition is timer-driven
// (via tickErrands).
func (app *App) onMessengerAtPoint(ctx context.Context, errandID string) {
	until := time.Now().Add(randomChatDuration())
	if _, err := app.DB.Exec(ctx, `
		UPDATE summon_errand
		   SET state = 'messenger_at_point',
		       chat_at_summon_until = $2,
		       updated_at = now()
		 WHERE id = $1
	`, errandID, until); err != nil {
		log.Printf("errand: state→messenger_at_point: %v", err)
	}
}

// onMessengerAtTarget advances messenger_to_target → messenger_at_target
// and starts the delivery timer. Like the at-point chat, the next
// transition is timer-driven (via tickErrands).
//
// First runs the tolerance check: if the target has moved more than
// errandTargetTolerance away from the dispatch coords (or is gone),
// the messenger immediately branches to refusal — there's no one
// here to deliver to, no point waiting through the chat timer.
func (app *App) onMessengerAtTarget(ctx context.Context, errandID string) {
	var dispX, dispY float64
	var targetName, targetKind string
	var summonerID, messengerID string
	if err := app.DB.QueryRow(ctx, `
		SELECT target_dispatch_x, target_dispatch_y, target_name, target_kind,
		       summoner_id::text, messenger_id::text
		  FROM summon_errand WHERE id = $1
	`, errandID).Scan(&dispX, &dispY, &targetName, &targetKind, &summonerID, &messengerID); err != nil {
		log.Printf("errand: load at-target row: %v", err)
		return
	}

	// Look up the target's current position. Missing actor = gone.
	var curX, curY float64
	stillThere := false
	if targetKind != "unknown" {
		if err := app.DB.QueryRow(ctx,
			`SELECT current_x, current_y FROM actor
			  WHERE LOWER(display_name) = LOWER($1) LIMIT 1`,
			targetName,
		).Scan(&curX, &curY); err == nil {
			dx, dy := curX-dispX, curY-dispY
			if (dx*dx)+(dy*dy) <= errandTargetTolerance*errandTargetTolerance {
				stillThere = true
			}
		}
	}

	if !stillThere {
		app.transitionToRefusalLeg(ctx, errandID, messengerID, summonerID)
		return
	}

	until := time.Now().Add(randomChatDuration())
	if _, err := app.DB.Exec(ctx, `
		UPDATE summon_errand
		   SET state = 'messenger_at_target',
		       chat_at_target_until = $2,
		       updated_at = now()
		 WHERE id = $1
	`, errandID, until); err != nil {
		log.Printf("errand: state→messenger_at_target: %v", err)
	}
}

// onMessengerDeliveredRefusal advances messenger_to_summoner →
// messenger_returning. Speaks the refusal line at the summoner's tile
// (the messenger arrived at the summoner's current position) and
// starts the messenger's walk back to its origin.
func (app *App) onMessengerDeliveredRefusal(ctx context.Context, errandID, messengerID, targetName string, originX, originY float64) {
	// Pull messenger display name for the speech broadcast.
	var messengerName string
	if err := app.DB.QueryRow(ctx,
		`SELECT display_name FROM actor WHERE id = $1`, messengerID,
	).Scan(&messengerName); err != nil {
		log.Printf("errand: load messenger name: %v", err)
		messengerName = "the messenger"
	}
	app.broadcastMessengerSpeech(messengerID, messengerName,
		fmt.Sprintf("Goodman %s is not to be found.", targetName))

	if _, err := app.DB.Exec(ctx, `
		UPDATE summon_errand
		   SET state = 'messenger_returning',
		       updated_at = now()
		 WHERE id = $1
	`, errandID); err != nil {
		log.Printf("errand: state→messenger_returning (after refusal): %v", err)
		return
	}

	if _, err := app.startNPCWalk(ctx, messengerID, originX, originY, defaultNPCSpeed); err != nil {
		app.failErrand(ctx, errandID, fmt.Sprintf("walk messenger home (after refusal): %v", err))
	}
}

// onMessengerReturned advances messenger_returning → done. Clears the
// messenger's override (the summoner's was already cleared at the
// va-target tick or the unavail-speech step).
func (app *App) onMessengerReturned(ctx context.Context, errandID, messengerID, summonerID string) {
	if _, err := app.DB.Exec(ctx, `
		UPDATE summon_errand
		   SET state = 'done', updated_at = now()
		 WHERE id = $1
	`, errandID); err != nil {
		log.Printf("errand: state→done: %v", err)
	}
	// Clear override on both actors. Cheap no-op when already cleared.
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor SET agent_override_until = NULL WHERE id = ANY($1)`,
		[]string{messengerID, summonerID},
	); err != nil {
		log.Printf("errand: clear overrides: %v", err)
	}
}

// tickErrands is the periodic timer-driven transition hook. Called
// every errandTickInterval from the engine startup goroutine. Two
// timer-elapsing transitions fire here:
//
//   messenger_at_point  + chat_at_summon_until  → messenger_to_target
//   messenger_at_target + chat_at_target_until  → either delivered_va
//                                                  (VA: trigger tick,
//                                                   clear summoner override,
//                                                   transition straight
//                                                   to messenger_returning)
//                                                  OR messenger_to_summoner
//                                                  (PC/nonva/unknown:
//                                                   walk back to summoner
//                                                   to deliver refusal).
func (app *App) tickErrands(ctx context.Context) {
	now := time.Now()

	// Chat-at-summon timer elapsed: messenger walks toward target.
	// Snapshot target's CURRENT position (no pursuit afterward).
	rows, err := app.DB.Query(ctx, `
		SELECT id, messenger_id, target_name, target_kind
		  FROM summon_errand
		 WHERE state = 'messenger_at_point'
		   AND chat_at_summon_until IS NOT NULL
		   AND chat_at_summon_until <= $1
	`, now)
	if err == nil {
		type row struct {
			ID, MessengerID, TargetName, TargetKind string
		}
		var elapsed []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.ID, &r.MessengerID, &r.TargetName, &r.TargetKind); err == nil {
				elapsed = append(elapsed, r)
			}
		}
		rows.Close()
		for _, r := range elapsed {
			app.onChatAtSummonElapsed(ctx, r.ID, r.MessengerID, r.TargetName, r.TargetKind)
		}
	}

	// Chat-at-target timer elapsed: deliver.
	rows, err = app.DB.Query(ctx, `
		SELECT id, summoner_id, messenger_id, target_name, target_kind, reason,
		       messenger_origin_x, messenger_origin_y
		  FROM summon_errand
		 WHERE state = 'messenger_at_target'
		   AND chat_at_target_until IS NOT NULL
		   AND chat_at_target_until <= $1
	`, now)
	if err == nil {
		type row struct {
			ID, SummonerID, MessengerID, TargetName, TargetKind, Reason string
			MessengerOriginX, MessengerOriginY                          float64
		}
		var elapsed []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.ID, &r.SummonerID, &r.MessengerID, &r.TargetName,
				&r.TargetKind, &r.Reason, &r.MessengerOriginX, &r.MessengerOriginY); err == nil {
				elapsed = append(elapsed, r)
			}
		}
		rows.Close()
		for _, r := range elapsed {
			app.onChatAtTargetElapsed(ctx, r.ID, r.SummonerID, r.MessengerID, r.TargetName,
				r.TargetKind, r.Reason, r.MessengerOriginX, r.MessengerOriginY)
		}
	}
}

// onChatAtSummonElapsed advances messenger_at_point → messenger_to_target.
// Snapshots target's current position fresh (the row's value was
// dispatch-time but the task design says "no pursuit, freeze at the
// moment the messenger starts walking" — that moment is now).
func (app *App) onChatAtSummonElapsed(ctx context.Context, errandID, messengerID, targetName, targetKind string) {
	var targetX, targetY float64
	if targetKind == "unknown" {
		// No actor matches; messenger walks back to summoner's current
		// position with the refusal speech. Fast-path: skip the
		// walk-to-target leg entirely and go straight to
		// messenger_to_summoner.
		var summonerX, summonerY float64
		if err := app.DB.QueryRow(ctx, `
			SELECT a.current_x, a.current_y
			  FROM summon_errand e JOIN actor a ON a.id = e.summoner_id
			 WHERE e.id = $1
		`, errandID).Scan(&summonerX, &summonerY); err != nil {
			app.failErrand(ctx, errandID, fmt.Sprintf("load summoner pos: %v", err))
			return
		}
		if _, err := app.DB.Exec(ctx, `
			UPDATE summon_errand
			   SET state = 'messenger_to_summoner', updated_at = now()
			 WHERE id = $1
		`, errandID); err != nil {
			log.Printf("errand: state→messenger_to_summoner (unknown target): %v", err)
			return
		}
		if _, err := app.startNPCWalk(ctx, messengerID, summonerX, summonerY, defaultNPCSpeed); err != nil {
			app.failErrand(ctx, errandID, fmt.Sprintf("walk to summoner (unknown target): %v", err))
		}
		return
	}

	// Known target — snapshot their current position.
	if err := app.DB.QueryRow(ctx,
		`SELECT current_x, current_y FROM actor WHERE LOWER(display_name) = LOWER($1) LIMIT 1`,
		targetName,
	).Scan(&targetX, &targetY); err != nil {
		// Target deleted between dispatch and now — fall to refusal.
		var summonerX, summonerY float64
		if err := app.DB.QueryRow(ctx, `
			SELECT a.current_x, a.current_y
			  FROM summon_errand e JOIN actor a ON a.id = e.summoner_id
			 WHERE e.id = $1
		`, errandID).Scan(&summonerX, &summonerY); err != nil {
			app.failErrand(ctx, errandID, fmt.Sprintf("load summoner pos: %v", err))
			return
		}
		if _, err := app.DB.Exec(ctx, `
			UPDATE summon_errand
			   SET state = 'messenger_to_summoner', target_kind = 'unknown', updated_at = now()
			 WHERE id = $1
		`, errandID); err != nil {
			log.Printf("errand: state→messenger_to_summoner (target gone): %v", err)
			return
		}
		if _, err := app.startNPCWalk(ctx, messengerID, summonerX, summonerY, defaultNPCSpeed); err != nil {
			app.failErrand(ctx, errandID, fmt.Sprintf("walk to summoner (target gone): %v", err))
		}
		return
	}

	if _, err := app.DB.Exec(ctx, `
		UPDATE summon_errand
		   SET state = 'messenger_to_target',
		       target_dispatch_x = $2, target_dispatch_y = $3,
		       updated_at = now()
		 WHERE id = $1
	`, errandID, targetX, targetY); err != nil {
		log.Printf("errand: state→messenger_to_target: %v", err)
		return
	}
	if _, err := app.startNPCWalk(ctx, messengerID, targetX, targetY, defaultNPCSpeed); err != nil {
		app.failErrand(ctx, errandID, fmt.Sprintf("walk messenger to target: %v", err))
	}
}

// onChatAtTargetElapsed handles delivery. For VA targets we write the
// summon audit row (which summonsTargetingPerceiver picks up), wake
// the target, fire an immediate tick, and transition the messenger
// to its return walk. For non-VA / PC / unknown we don't write a
// summon audit row (no perception consumer); we transition to
// messenger_to_summoner with a refusal speech to be delivered.
func (app *App) onChatAtTargetElapsed(ctx context.Context, errandID, summonerID, messengerID, targetName, targetKind, reason string, originX, originY float64) {
	switch targetKind {
	case "va":
		// Look up summoner display name (for the summon audit payload)
		// and target id (to wake/tick).
		var summonerName, targetID string
		if err := app.DB.QueryRow(ctx,
			`SELECT display_name FROM actor WHERE id = $1`, summonerID,
		).Scan(&summonerName); err != nil {
			summonerName = "Someone"
		}
		if err := app.DB.QueryRow(ctx,
			`SELECT id FROM actor WHERE LOWER(display_name) = LOWER($1) LIMIT 1`, targetName,
		).Scan(&targetID); err != nil {
			// Target gone between dispatch and delivery. Fall to refusal.
			app.transitionToRefusalLeg(ctx, errandID, messengerID, summonerID)
			return
		}

		// Write the summon audit row so summonsTargetingPerceiver
		// includes "A messenger has come from <summoner>..." in the
		// target's next perception. Same payload shape the v1 path
		// used so the perception render code stays unchanged.
		payload := map[string]interface{}{
			"target": targetName,
		}
		if reason != "" {
			payload["reason"] = reason
		}
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			log.Printf("errand: marshal summon payload: %v", err)
			payloadJSON = []byte("{}")
		}
		if _, err := app.DB.Exec(ctx,
			`INSERT INTO agent_action_log (actor_id, speaker_name, source, action_type, payload, result, error, huddle_id)
			 VALUES ($1, $2, 'agent', 'summon', $3, 'ok', NULL,
			         (SELECT current_huddle_id FROM actor WHERE id = $1))`,
			summonerID, summonerName, payloadJSON,
		); err != nil {
			log.Printf("errand: record summon audit: %v", err)
		}

		// Wake the target so they react inside this scene rather than
		// after their next scheduled commitment.
		if _, err := app.DB.Exec(ctx,
			`UPDATE actor
			    SET agent_override_until = NULL,
			        break_until = NULL,
			        last_agent_tick_at = NULL
			  WHERE id = $1`,
			targetID,
		); err != nil {
			log.Printf("errand: wake va target: %v", err)
		}
		go app.triggerImmediateTick(context.Background(), targetID, "summoned", false, "", "")

		// Messenger heads home; summoner override clears now (the
		// LLM-side summon arc is complete from the engine's POV).
		if _, err := app.DB.Exec(ctx,
			`UPDATE actor SET agent_override_until = NULL WHERE id = $1`,
			summonerID,
		); err != nil {
			log.Printf("errand: clear summoner override: %v", err)
		}

		if _, err := app.DB.Exec(ctx, `
			UPDATE summon_errand
			   SET state = 'messenger_returning', updated_at = now()
			 WHERE id = $1
		`, errandID); err != nil {
			log.Printf("errand: state→messenger_returning (va delivered): %v", err)
			return
		}
		if _, err := app.startNPCWalk(ctx, messengerID, originX, originY, defaultNPCSpeed); err != nil {
			app.failErrand(ctx, errandID, fmt.Sprintf("walk messenger home (va): %v", err))
		}

	default:
		// pc / nonva / unknown — no perception consumer. Messenger
		// walks back to the summoner with the canned refusal.
		app.transitionToRefusalLeg(ctx, errandID, messengerID, summonerID)
	}
}

// transitionToRefusalLeg moves the errand from messenger_at_target
// (or any state in the unavailable branch) to messenger_to_summoner
// with a fresh walk to the summoner's current position. The refusal
// speech itself is spoken on arrival via onMessengerDeliveredRefusal.
func (app *App) transitionToRefusalLeg(ctx context.Context, errandID, messengerID, summonerID string) {
	var summonerX, summonerY float64
	if err := app.DB.QueryRow(ctx,
		`SELECT current_x, current_y FROM actor WHERE id = $1`, summonerID,
	).Scan(&summonerX, &summonerY); err != nil {
		app.failErrand(ctx, errandID, fmt.Sprintf("load summoner pos for refusal: %v", err))
		return
	}
	if _, err := app.DB.Exec(ctx, `
		UPDATE summon_errand
		   SET state = 'messenger_to_summoner', updated_at = now()
		 WHERE id = $1
	`, errandID); err != nil {
		log.Printf("errand: state→messenger_to_summoner: %v", err)
		return
	}
	if _, err := app.startNPCWalk(ctx, messengerID, summonerX, summonerY, defaultNPCSpeed); err != nil {
		app.failErrand(ctx, errandID, fmt.Sprintf("walk messenger to summoner (refusal): %v", err))
	}
}

// failErrand transitions any errand into the failed terminus and
// clears overrides on its participants. Used both for hard errors
// (DB / pathfinding) and impossible flows (target gone, no path).
func (app *App) failErrand(ctx context.Context, errandID, reason string) {
	log.Printf("errand %s failed: %s", errandID, reason)
	var summonerID, messengerID string
	if err := app.DB.QueryRow(ctx,
		`UPDATE summon_errand SET state = 'failed', updated_at = now()
		  WHERE id = $1
		  RETURNING summoner_id::text, messenger_id::text`,
		errandID,
	).Scan(&summonerID, &messengerID); err != nil {
		log.Printf("errand-fail: update: %v", err)
		return
	}
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor SET agent_override_until = NULL WHERE id = ANY($1)`,
		[]string{summonerID, messengerID},
	); err != nil {
		log.Printf("errand-fail: clear overrides: %v", err)
	}
}

// broadcastSummonRing records a village_event so the ring lands on
// the village ticker / Around Town log. Open-village event (no
// structure_id) since today's summon_points are lamp posts; the
// village_event row carries x/y so future visibility-radius filtering
// can scope it.
//
// Per-tag bell flavor (Ring ring ring) is a future enhancement gated
// on tagging the summon_point object as 'bell' — for now the generic
// call-for-a-messenger line covers any summon_point.
func (app *App) broadcastSummonRing(ctx context.Context, summonerID, summonerName string, x, y float64) {
	text := fmt.Sprintf("%s calls for a messenger.", summonerName)
	app.recordVillageEvent(ctx, "summon_ring", text, summonerID, "", &x, &y)
}

// broadcastMessengerSpeech emits an npc_spoke event for the messenger.
// Used for the canned refusal delivered at the summoner's tile when
// the target is unavailable. No agent_action_log row — the messenger
// isn't a VA and this isn't an LLM-driven utterance.
func (app *App) broadcastMessengerSpeech(messengerID, messengerName, text string) {
	app.Hub.Broadcast(WorldEvent{Type: "npc_spoke", Data: map[string]interface{}{
		"npc_id": messengerID,
		"name":   messengerName,
		"text":   text,
		"at":     time.Now().UTC().Format(time.RFC3339),
	}})
}

// randomChatDuration returns a random pause between
// errandChatMinSeconds and errandChatMaxSeconds.
func randomChatDuration() time.Duration {
	span := errandChatMaxSeconds - errandChatMinSeconds
	if span <= 0 {
		return time.Duration(errandChatMinSeconds) * time.Second
	}
	return time.Duration(errandChatMinSeconds+rand.Intn(span+1)) * time.Second
}

// startErrandTicker spawns the periodic timer-driven transition
// goroutine. Called once at engine startup.
func (app *App) startErrandTicker() {
	go func() {
		ticker := time.NewTicker(errandTickInterval)
		defer ticker.Stop()
		for range ticker.C {
			app.tickErrands(context.Background())
		}
	}()
}
