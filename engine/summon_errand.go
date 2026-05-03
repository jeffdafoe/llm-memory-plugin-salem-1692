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
	"database/sql"
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
// the ring narration, snapshots the messenger's pre-dispatch state
// (current position + inside_structure_id, so we can re-enter on
// return), stamps override, and dispatches the messenger toward the
// summon_point.
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

	// Snapshot messenger's pre-walk state. inside_structure_id is
	// captured BEFORE we clear inside/inside_structure_id below — it's
	// what we'll re-enter on the return walk arrival. NULL means the
	// messenger was in the open village; return walk just lands at
	// origin coords with no inside-flip.
	var messengerX, messengerY float64
	var messengerOriginStructure sql.NullString
	if err := app.DB.QueryRow(ctx,
		`SELECT current_x, current_y, inside_structure_id::text
		   FROM actor WHERE id = $1`, messengerID,
	).Scan(&messengerX, &messengerY, &messengerOriginStructure); err != nil {
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

	var originStructureArg interface{}
	if messengerOriginStructure.Valid && messengerOriginStructure.String != "" {
		originStructureArg = messengerOriginStructure.String
	}
	if _, err := app.DB.Exec(ctx, `
		UPDATE summon_errand
		   SET state = 'summoner_at_point',
		       messenger_origin_x = $2, messenger_origin_y = $3,
		       messenger_origin_structure_id = $4,
		       updated_at = now()
		 WHERE id = $1
	`, errandID, messengerX, messengerY, originStructureArg); err != nil {
		log.Printf("errand: state→summoner_at_point: %v", err)
		return
	}

	// Walk messenger to summon_point. Failure terminates the errand.
	if _, err := app.startNPCWalk(ctx, messengerID, pointX, pointY, defaultNPCSpeed); err != nil {
		app.failErrand(ctx, errandID, fmt.Sprintf("walk messenger to point: %v", err))
	}
}

// onMessengerAtPoint advances summoner_at_point → messenger_at_point.
// The summoner says their canned commission, the messenger replies a
// beat later, and the chat timer plays out the rest of the pause
// before tickErrands fires the walk-to-target transition.
//
// Speech sequence:
//   t+0    summoner: "Fetch <target> for me. <reason>"
//   t+~1.5s messenger: "At once."
// chat_at_summon_until = t + randomChatDuration() (4–10s); the walk
// won't start until that elapses.
func (app *App) onMessengerAtPoint(ctx context.Context, errandID string) {
	var summonerID, summonerName, messengerID, messengerName, targetName, reason string
	if err := app.DB.QueryRow(ctx, `
		SELECT e.summoner_id::text, sa.display_name,
		       e.messenger_id::text, ma.display_name,
		       e.target_name, e.reason
		  FROM summon_errand e
		  JOIN actor sa ON sa.id = e.summoner_id
		  JOIN actor ma ON ma.id = e.messenger_id
		 WHERE e.id = $1
	`, errandID).Scan(&summonerID, &summonerName, &messengerID, &messengerName,
		&targetName, &reason); err != nil {
		log.Printf("errand: load at-point row: %v", err)
		// Fall through to set the timer anyway — losing the speech
		// bubble is a UX miss, not a state-machine failure.
	} else {
		commission := fmt.Sprintf("Fetch %s for me.", targetName)
		if reason != "" {
			commission = fmt.Sprintf("Fetch %s for me. %s.", targetName, capitalize(reason))
		}
		app.broadcastNPCSpeech(summonerID, summonerName, commission)
		// Beat between commission and reply so the bubbles read in
		// order rather than overlap. Goroutine so we don't block
		// applyArrival.
		go func() {
			time.Sleep(1500 * time.Millisecond)
			app.broadcastNPCSpeech(messengerID, messengerName, "At once.")
		}()
	}

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

	// Messenger speaks the canned delivery line at the target before
	// the chat timer plays out. The target's reply comes from their
	// own LLM tick (fired in onChatAtTargetElapsed for VA targets);
	// non-VA targets just hear the line and don't react.
	var summonerName, messengerName, reason string
	if err := app.DB.QueryRow(ctx, `
		SELECT sa.display_name, ma.display_name, e.reason
		  FROM summon_errand e
		  JOIN actor sa ON sa.id = e.summoner_id
		  JOIN actor ma ON ma.id = e.messenger_id
		 WHERE e.id = $1
	`, errandID).Scan(&summonerName, &messengerName, &reason); err == nil {
		delivery := fmt.Sprintf("%s, %s summons you.", targetName, summonerName)
		if reason != "" {
			delivery = fmt.Sprintf("%s, %s summons you. %s.", targetName, summonerName, capitalize(reason))
		}
		app.broadcastNPCSpeech(messengerID, messengerName, delivery)
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
// starts the messenger's walk back to its origin (door tile when
// origin had a structure, raw coords otherwise).
func (app *App) onMessengerDeliveredRefusal(ctx context.Context, errandID, messengerID, targetName string, originX, originY float64) {
	// Pull messenger display name for the speech broadcast.
	var messengerName string
	if err := app.DB.QueryRow(ctx,
		`SELECT display_name FROM actor WHERE id = $1`, messengerID,
	).Scan(&messengerName); err != nil {
		log.Printf("errand: load messenger name: %v", err)
		messengerName = "the messenger"
	}
	app.broadcastNPCSpeech(messengerID, messengerName,
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

	_ = originX
	_ = originY
	retX, retY := app.resolveMessengerReturnWalk(ctx, errandID)
	if _, err := app.startNPCWalk(ctx, messengerID, retX, retY, defaultNPCSpeed); err != nil {
		app.failErrand(ctx, errandID, fmt.Sprintf("walk messenger home (after refusal): %v", err))
	}
}

// onMessengerReturned advances messenger_returning → done. Clears the
// messenger's override (the summoner's was already cleared at the
// va-target tick or the unavail-speech step). If the messenger had an
// origin structure when the errand started (captured at
// summoner_at_point), flip them inside that structure so their
// editor row reads correctly and Go-Home is disabled.
func (app *App) onMessengerReturned(ctx context.Context, errandID, messengerID, summonerID string) {
	var originStructure sql.NullString
	_ = app.DB.QueryRow(ctx,
		`SELECT messenger_origin_structure_id::text FROM summon_errand WHERE id = $1`,
		errandID,
	).Scan(&originStructure)

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

	if originStructure.Valid && originStructure.String != "" {
		app.setNPCInside(ctx, messengerID, true, originStructure.String)
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
//
// When the target is inside a structure, the walk-to-target leg
// resolves to one of the 8 loiter-ring slots around the structure
// rather than the target's exact tile. Without this, the pathfinder
// sometimes routed the messenger straight through the stall sprite
// and the messenger stood ON the structure rather than inside the
// yellow loiter ring.
func (app *App) onChatAtSummonElapsed(ctx context.Context, errandID, messengerID, targetName, targetKind string) {
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

	// Known target — resolve walk coords. The helper returns the
	// loiter-ring slot when the target is inside a structure, or the
	// target's tile when they're in the open village. Failure (target
	// deleted, lookup error) falls through to the refusal branch.
	walkX, walkY, ok := app.resolveMessengerTargetWalk(ctx, messengerID, targetName)
	if !ok {
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
	`, errandID, walkX, walkY); err != nil {
		log.Printf("errand: state→messenger_to_target: %v", err)
		return
	}
	if _, err := app.startNPCWalk(ctx, messengerID, walkX, walkY, defaultNPCSpeed); err != nil {
		app.failErrand(ctx, errandID, fmt.Sprintf("walk messenger to target: %v", err))
	}
}

// resolveMessengerReturnWalk picks the coords the messenger should
// walk to on a return leg. When messenger_origin_structure_id is set,
// returns the structure's door tile (the engine flips inside=true on
// arrival via onMessengerReturned). When no origin structure, returns
// the raw origin coords.
//
// Falls back to the raw origin coords on any lookup error — losing the
// door-tile snap is preferable to failing the errand.
func (app *App) resolveMessengerReturnWalk(ctx context.Context, errandID string) (float64, float64) {
	var originX, originY float64
	var originStructure sql.NullString
	if err := app.DB.QueryRow(ctx, `
		SELECT messenger_origin_x, messenger_origin_y,
		       messenger_origin_structure_id::text
		  FROM summon_errand WHERE id = $1
	`, errandID).Scan(&originX, &originY, &originStructure); err != nil {
		return originX, originY
	}
	if !originStructure.Valid || originStructure.String == "" {
		return originX, originY
	}

	const tileSize = 32.0
	var ox, oy float64
	var doorX, doorY sql.NullInt32
	if err := app.DB.QueryRow(ctx, `
		SELECT o.x, o.y, a.door_offset_x, a.door_offset_y
		  FROM village_object o
		  JOIN asset a ON a.id = o.asset_id
		 WHERE o.id = $1
	`, originStructure.String).Scan(&ox, &oy, &doorX, &doorY); err != nil {
		return originX, originY
	}
	if doorX.Valid && doorY.Valid {
		return ox + float64(doorX.Int32)*tileSize, oy + float64(doorY.Int32)*tileSize
	}
	return ox, oy
}

// resolveMessengerTargetWalk returns the coords the messenger should
// walk to in order to deliver to target. When the target is inside a
// structure, a loiter-ring slot is picked via pickVisitorSlot — same
// approach a chore visitor uses, so the messenger respects the yellow
// loiter ring instead of cutting across the structure footprint. When
// the target is in the open village, walks to the target's tile.
//
// Returns (x, y, true) on success. Returns (_, _, false) when the
// target actor doesn't exist — caller falls through to the refusal
// branch.
func (app *App) resolveMessengerTargetWalk(ctx context.Context, messengerID, targetName string) (float64, float64, bool) {
	const tileSize = 32.0

	var targetID, insideStructure sql.NullString
	var targetX, targetY float64
	if err := app.DB.QueryRow(ctx, `
		SELECT id::text, current_x, current_y, inside_structure_id::text
		  FROM actor
		 WHERE LOWER(display_name) = LOWER($1)
		 LIMIT 1
	`, targetName).Scan(&targetID, &targetX, &targetY, &insideStructure); err != nil {
		return 0, 0, false
	}

	if !insideStructure.Valid || insideStructure.String == "" {
		return targetX, targetY, true
	}

	// Target is inside a structure — pick the loiter ring slot.
	var ox, oy float64
	var loiterX, loiterY, doorX, doorY sql.NullInt32
	var footprintBottom int
	if err := app.DB.QueryRow(ctx, `
		SELECT o.x, o.y,
		       o.loiter_offset_x, o.loiter_offset_y,
		       a.door_offset_x, a.door_offset_y, a.footprint_bottom
		  FROM village_object o
		  JOIN asset a ON a.id = o.asset_id
		 WHERE o.id = $1
	`, insideStructure.String).Scan(&ox, &oy, &loiterX, &loiterY, &doorX, &doorY, &footprintBottom); err != nil {
		// Structure metadata missing — fall back to the target tile.
		return targetX, targetY, true
	}

	loiterTileX, loiterTileY := effectiveLoiterTile(loiterX, loiterY, doorX, doorY, footprintBottom)
	wx, wy := app.pickVisitorSlot(ctx, messengerID, ox, oy, loiterTileX, loiterTileY)
	_ = tileSize
	return wx, wy, true
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
		// Resolve the actual return tile — door tile when origin had
		// a structure, raw coords otherwise. Pre-state-change because
		// we want the helper to read the row we just wrote (not yet
		// done; the column it cares about is messenger_origin_structure_id
		// which doesn't change on this transition).
		_ = originX
		_ = originY
		retX, retY := app.resolveMessengerReturnWalk(ctx, errandID)
		if _, err := app.startNPCWalk(ctx, messengerID, retX, retY, defaultNPCSpeed); err != nil {
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

// broadcastNPCSpeech emits an npc_spoke event with the canonical shape
// (npc_id, name, text, at). Used for every canned line in the errand
// flow — summoner's commission to the messenger, messenger's reply,
// messenger's delivery to the target, messenger's refusal report. No
// agent_action_log row: these are mechanical / non-VA utterances.
func (app *App) broadcastNPCSpeech(actorID, actorName, text string) {
	app.Hub.Broadcast(WorldEvent{Type: "npc_spoke", Data: map[string]interface{}{
		"npc_id": actorID,
		"name":   actorName,
		"text":   text,
		"at":     time.Now().UTC().Format(time.RFC3339),
	}})
}

// capitalize uppercases the first rune of s. Used to turn LLM-supplied
// reason strings ("come share an ale") into sentence-cased fragments
// inside the canned commission and delivery lines. No-op on empty.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
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
