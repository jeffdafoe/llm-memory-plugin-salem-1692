package main

// Scene huddles — per-structure conversational scopes.
//
// A "huddle" represents a group of NPCs (and eventually PCs) sharing the
// same conversational scene at a structure. One row per scene; the row
// is created when the first occupant arrives and concluded when the
// last one leaves.
//
// Phase 1 (this file): one active huddle per structure. NPCs in the
// same building share one huddle. Solo occupants still get a huddle
// row — useful for transcript scoping ("Ezekiel was alone at the
// Smithy from 09:00 to 11:00") even when nothing was said.
//
// Phase 2 (deferred): proximity-based splinter. Multiple concurrent
// huddles per structure, joined by NPC distance. Schema already
// supports it (no UNIQUE on structure_id); only the join logic
// changes to "nearest huddle within radius" instead of "active huddle
// for this structure."
//
// Why a Salem-side table instead of llm-memory-api discussions: the
// discussion creator-conflict guard prevents salem-engine from
// spawning more than one active discussion at a time. Casual ambient
// scenes don't need discussions' formal participant management or
// voting; agent_action_log already records speech with structure_id.
// scene_huddle just adds membership scoping on top.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"

	"github.com/jackc/pgx/v5"
)

// emitEnterHuddleAudit writes a row into agent_action_log with
// action_type='enter_huddle' and the new huddle_id stamped. ZBBS-094:
// the sim-conversation distiller's loadDayEvents discovers an actor's
// huddle membership for the day from huddle_id stamps on their own
// audit rows. Without this presence marker, an actor who joins a huddle
// and sits silently (no speak/act/chore that hour) leaves no row in
// the huddle, and the cross-actor pull misses other speakers' lines
// while they were present.
//
// speaker_name is sourced via subquery on the actor row so this works
// for both NPCs (display_name) and PCs (display_name set in setup).
// Best-effort — failure here is logged but doesn't unwind the join.
// The narrative side (api distiller) maps action_type='enter_huddle'
// to null so this row drives membership only and doesn't produce a
// transcript line.
func (app *App) emitEnterHuddleAudit(ctx context.Context, actorID, huddleID, structureID string) {
	payload, err := json.Marshal(map[string]interface{}{
		"structure_id": structureID,
	})
	if err != nil {
		log.Printf("scene-huddle: marshal enter_huddle payload for %s: %v", actorID, err)
		return
	}
	if _, err := app.DB.Exec(ctx,
		`INSERT INTO agent_action_log
		    (actor_id, speaker_name, source, action_type, payload, result, error, huddle_id)
		 SELECT $1, COALESCE(display_name, ''), 'engine', 'enter_huddle',
		        $2::jsonb, 'ok', NULL, $3::uuid
		   FROM actor
		  WHERE id = $1`,
		actorID, payload, huddleID,
	); err != nil {
		log.Printf("scene-huddle: emit enter_huddle audit for %s: %v", actorID, err)
	}
}

// joinOrCreateHuddle places the NPC into the active huddle for the
// given structure, creating one if none exists. Updates npc.current
// huddle_id and returns the huddle UUID. Idempotent: if the NPC is
// already in the structure's active huddle, no change.
//
// On join, also records acquaintance with everyone else already in
// the huddle (M6.4.5). Co-presence in a structure constitutes an
// introduction — even before either party speaks, they've at least
// been in each other's space and can recognize each other on sight.
//
// Called from setNPCInside whenever an NPC's inside_structure_id flips
// to a non-null value.
func (app *App) joinOrCreateHuddle(ctx context.Context, npcID, structureID string) (string, error) {
	// Look for an active huddle at this structure. Partial index covers
	// the WHERE concluded_at IS NULL predicate.
	var huddleID string
	err := app.DB.QueryRow(ctx,
		`SELECT id::text FROM scene_huddle
		 WHERE structure_id = $1 AND concluded_at IS NULL
		 ORDER BY created_at DESC LIMIT 1`,
		structureID,
	).Scan(&huddleID)
	if errors.Is(err, pgx.ErrNoRows) {
		// No active huddle — create one. UUID generated server-side; we
		// scan it back to use as the NPC's current_huddle_id.
		err = app.DB.QueryRow(ctx,
			`INSERT INTO scene_huddle (structure_id) VALUES ($1) RETURNING id::text`,
			structureID,
		).Scan(&huddleID)
		if err != nil {
			return "", err
		}
		log.Printf("scene-huddle: created %s at structure %s", huddleID, structureID)
	} else if err != nil {
		return "", err
	}

	// Set the NPC's current_huddle_id. Skips the UPDATE if already
	// matching to avoid a redundant write on idempotent calls.
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor SET current_huddle_id = $2
		 WHERE id = $1 AND (current_huddle_id IS DISTINCT FROM $2)`,
		npcID, huddleID,
	); err != nil {
		return huddleID, err
	}

	// Record acquaintance with anyone else already in this huddle.
	// Both directions written so the symmetry holds. ON CONFLICT
	// DO NOTHING preserves the original first_interacted_at timestamp.
	if _, err := app.DB.Exec(ctx,
		`INSERT INTO npc_acquaintance (actor_id, other_name)
		 SELECT $1, n.display_name
		   FROM actor n
		  WHERE n.current_huddle_id::text = $2
		    AND n.id != $1
		 ON CONFLICT DO NOTHING`,
		npcID, huddleID,
	); err != nil {
		log.Printf("scene-huddle: record acquaintance %s: %v", npcID, err)
	}
	if _, err := app.DB.Exec(ctx,
		`INSERT INTO npc_acquaintance (actor_id, other_name)
		 SELECT n.id, me.display_name
		   FROM actor n, actor me
		  WHERE n.current_huddle_id::text = $1
		    AND me.id = $2
		    AND n.id != $2
		 ON CONFLICT DO NOTHING`,
		huddleID, npcID,
	); err != nil {
		log.Printf("scene-huddle: record reverse acquaintance for %s: %v", npcID, err)
	}

	app.emitEnterHuddleAudit(ctx, npcID, huddleID, structureID)

	// ZBBS-HOME-273: engine-authored greet from co-located keepers when
	// a non-businessowner NPC walks in. Cooldown + flavor pools live in
	// businessowner.go; this call is a no-op when no keepers are at-post
	// or when the entering actor IS a businessowner.
	app.maybeFireGreetOnEntry(ctx, npcID, structureID, huddleID)

	return huddleID, nil
}

// leaveHuddle clears the NPC's current_huddle_id and concludes the
// huddle if no participants remain. Called from setNPCInside when the
// NPC's inside_structure_id flips to null OR to a different structure.
//
// Concluded huddles are kept (not deleted) so the transcript-generation
// flow can read them later. The partial index on concluded_at IS NULL
// keeps the active-huddle lookup fast as concluded rows accumulate.
func (app *App) leaveHuddle(ctx context.Context, npcID string) {
	// Read the NPC's current huddle BEFORE clearing — we need it to
	// check the participant count after.
	var huddleID sql.NullString
	if err := app.DB.QueryRow(ctx,
		`SELECT current_huddle_id::text FROM actor WHERE id = $1`,
		npcID,
	).Scan(&huddleID); err != nil {
		log.Printf("scene-huddle: read current_huddle_id for %s: %v", npcID, err)
		return
	}
	if !huddleID.Valid {
		return // not in a huddle — nothing to leave
	}

	// ZBBS-HOME-274: engine-authored farewell from any co-located
	// keeper BEFORE the huddle membership clears, so the speak event
	// is attributed to the room being left rather than the cleared
	// state. No-op when no keeper qualifies or when the leaver IS a
	// businessowner.
	app.maybeFireFarewellOnExit(ctx, npcID, huddleID.String)

	if _, err := app.DB.Exec(ctx,
		`UPDATE actor SET current_huddle_id = NULL WHERE id = $1`,
		npcID,
	); err != nil {
		log.Printf("scene-huddle: clear current_huddle_id for %s: %v", npcID, err)
		return
	}

	// If the huddle now has zero participants (NPCs + PCs), conclude it.
	// Counts both populations since PCs and NPCs are equally citizens of
	// a scene huddle.
	var remaining int
	if err := app.DB.QueryRow(ctx,
		`SELECT COUNT(*) FROM actor WHERE current_huddle_id::text = $1`,
		huddleID.String,
	).Scan(&remaining); err != nil {
		log.Printf("scene-huddle: count remaining for %s: %v", huddleID.String, err)
		return
	}
	if remaining == 0 {
		if _, err := app.DB.Exec(ctx,
			`UPDATE scene_huddle SET concluded_at = NOW()
			 WHERE id::text = $1 AND concluded_at IS NULL`,
			huddleID.String,
		); err != nil {
			log.Printf("scene-huddle: conclude %s: %v", huddleID.String, err)
			return
		}
		log.Printf("scene-huddle: concluded %s (last participant left)", huddleID.String)
	}
}

// joinOrCreateHuddleForPC mirrors joinOrCreateHuddle but for PC actors.
// PCs are tracked in pc_position rather than npc, so the membership
// update goes there instead. NPCs become acquainted with the PC's
// character_name (the in-world identity, not the login_username system
// identity) — that's the name they greet by in their perception.
//
// PCs don't have their own npc_acquaintance row (they aren't NPCs);
// only NPCs track who they know. The PC's view of "do I know X?" is
// instead handled UI-side — the village viewer's chat panel knows
// who the PC has spoken to.
func (app *App) joinOrCreateHuddleForPC(ctx context.Context, actorName, structureID string) (string, error) {
	var huddleID string
	err := app.DB.QueryRow(ctx,
		`SELECT id::text FROM scene_huddle
		 WHERE structure_id = $1 AND concluded_at IS NULL
		 ORDER BY created_at DESC LIMIT 1`,
		structureID,
	).Scan(&huddleID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = app.DB.QueryRow(ctx,
			`INSERT INTO scene_huddle (structure_id) VALUES ($1) RETURNING id::text`,
			structureID,
		).Scan(&huddleID)
		if err != nil {
			return "", err
		}
		log.Printf("scene-huddle: created %s at structure %s (PC %s)", huddleID, structureID, actorName)
	} else if err != nil {
		return "", err
	}

	if _, err := app.DB.Exec(ctx,
		`UPDATE actor SET current_huddle_id = $2
		 WHERE login_username = $1 AND (current_huddle_id IS DISTINCT FROM $2)`,
		actorName, huddleID,
	); err != nil {
		return huddleID, err
	}

	// Each NPC in this huddle now knows the PC by character_name —
	// the in-world identity. The acquaintance is one-way; PC's own
	// awareness is UI-side.
	if _, err := app.DB.Exec(ctx,
		`INSERT INTO npc_acquaintance (actor_id, other_name)
		 SELECT n.id, pc.display_name
		   FROM actor n, actor pc
		  WHERE n.current_huddle_id::text = $1
		    AND pc.login_username = $2
		    AND n.llm_memory_agent IS NOT NULL
		    AND n.id != pc.id
		 ON CONFLICT DO NOTHING`,
		huddleID, actorName,
	); err != nil {
		log.Printf("scene-huddle: record PC->NPC acquaintance: %v", err)
	}

	// ZBBS-094 presence audit row — see emitEnterHuddleAudit comment.
	// Resolve login_username → actor.id for the helper. Best-effort.
	var actorID string
	if err := app.DB.QueryRow(ctx,
		`SELECT id::text FROM actor WHERE login_username = $1`,
		actorName,
	).Scan(&actorID); err == nil {
		app.emitEnterHuddleAudit(ctx, actorID, huddleID, structureID)
		// ZBBS-HOME-273: engine-authored greet from co-located keepers
		// when a PC walks in. PC is never a businessowner (gate 1 in
		// the dispatcher will short-circuit non-keeper customers
		// directly to the keeper-scan path).
		app.maybeFireGreetOnEntry(ctx, actorID, structureID, huddleID)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		log.Printf("scene-huddle: resolve PC actor_id for %s: %v", actorName, err)
	}

	return huddleID, nil
}

// leaveHuddleForPC mirrors leaveHuddle for PC actors. Clears the PC's
// current_huddle_id and concludes the huddle if the count drops to 0.
func (app *App) leaveHuddleForPC(ctx context.Context, actorName string) {
	var huddleID sql.NullString
	if err := app.DB.QueryRow(ctx,
		`SELECT current_huddle_id::text FROM actor WHERE login_username = $1`,
		actorName,
	).Scan(&huddleID); err != nil {
		log.Printf("scene-huddle: read PC current_huddle_id for %s: %v", actorName, err)
		return
	}
	if !huddleID.Valid {
		return
	}

	// ZBBS-HOME-274: same farewell hook as the NPC leaveHuddle. Resolve
	// login_username → actor.id for the dispatcher. Best-effort; a
	// failed lookup just skips the farewell, not the leave itself.
	var leavingActorID string
	if err := app.DB.QueryRow(ctx,
		`SELECT id::text FROM actor WHERE login_username = $1`,
		actorName,
	).Scan(&leavingActorID); err == nil {
		app.maybeFireFarewellOnExit(ctx, leavingActorID, huddleID.String)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		log.Printf("scene-huddle: resolve PC actor_id for farewell %s: %v", actorName, err)
	}

	if _, err := app.DB.Exec(ctx,
		`UPDATE actor SET current_huddle_id = NULL WHERE login_username = $1`,
		actorName,
	); err != nil {
		log.Printf("scene-huddle: clear PC current_huddle_id for %s: %v", actorName, err)
		return
	}

	var remaining int
	if err := app.DB.QueryRow(ctx,
		`SELECT COUNT(*) FROM actor WHERE current_huddle_id::text = $1`,
		huddleID.String,
	).Scan(&remaining); err != nil {
		log.Printf("scene-huddle: count remaining for %s: %v", huddleID.String, err)
		return
	}
	if remaining == 0 {
		if _, err := app.DB.Exec(ctx,
			`UPDATE scene_huddle SET concluded_at = NOW()
			 WHERE id::text = $1 AND concluded_at IS NULL`,
			huddleID.String,
		); err != nil {
			log.Printf("scene-huddle: conclude %s: %v", huddleID.String, err)
			return
		}
		log.Printf("scene-huddle: concluded %s (last participant — PC %s left)", huddleID.String, actorName)
	}
}

// maybeAdoptWaitingPCsAtArrival pulls PCs waiting at a structure's loiter
// slot into the structure's active huddle when the structure's keeper
// arrives and goes inside. Inverse of the PC-side loiter-huddle path in
// handlePCMove: there, the PC's arrival triggers huddle formation when
// NPCs are already at the ring; here, the keeper's arrival picks up PCs
// already waiting at the door.
//
// Without this, a PC who walked to a closed business (knock fired,
// narration only) and stayed at the loiter slot is invisible to the
// returning keeper's perception block — they're outside, not in any
// huddle yet, so the keeper's arrival tick sees an empty scene and the
// LLM returns done. Adopting the PC into the freshly-created structure
// huddle (joined via setNPCInside → joinOrCreateHuddle moments earlier
// in advanceBehavior) gives the keeper a concrete cue to react to.
//
// Conservative gate: only the keeper's arrival pulls PCs in. A lodger
// returning to their tavern, or a customer dropping in to another
// vendor's shop while a different PC happens to be at this keeper's
// door — neither triggers adoption. The signal is
// `actor.work_structure_id = structureID AND actor.inside = true AND
// inside_structure_id = structureID`.
//
// Loiter math mirrors handlePCMove's predicate exactly so a PC standing
// legitimately at the slot matches the proximity bound.
//
// PCs already in some other huddle (mid-conversation at an adjacent
// loiter pin) are left alone via the current_huddle_id IS NULL filter.
func (app *App) maybeAdoptWaitingPCsAtArrival(ctx context.Context, npcID, structureID string) {
	if npcID == "" || structureID == "" {
		return
	}

	// Confirm this NPC is the keeper AND went inside on this arrival.
	// Visitor entries (lodger to tavern, drop-in customer) and refused-
	// entry walks (canEnter=false) get filtered here in one round-trip.
	var isKeeperInside bool
	if err := app.DB.QueryRow(ctx,
		`SELECT EXISTS (
		    SELECT 1 FROM actor
		     WHERE id::text                  = $1
		       AND work_structure_id::text   = $2
		       AND inside                    = true
		       AND inside_structure_id::text = $2
		 )`,
		npcID, structureID,
	).Scan(&isKeeperInside); err != nil {
		log.Printf("greet-on-arrival keeper check %s/%s: %v", npcID, structureID, err)
		return
	}
	if !isKeeperInside {
		return
	}

	// Resolve the structure's loiter slot world-pixel center. Same query
	// shape and effectiveLoiterTile derivation handlePCMove uses to land
	// a knocked PC at the visitor slot — predicate has to match for an
	// actually-standing-there PC to be picked up.
	var ox, oy float64
	var loiterX, loiterY sql.NullInt32
	var doorX, doorY sql.NullInt32
	var footprintBottom int
	if err := app.DB.QueryRow(ctx,
		`SELECT o.x, o.y,
		        o.loiter_offset_x, o.loiter_offset_y,
		        a.door_offset_x, a.door_offset_y, a.footprint_bottom
		   FROM village_object o
		   JOIN asset a ON a.id = o.asset_id
		  WHERE o.id::text = $1`,
		structureID,
	).Scan(&ox, &oy, &loiterX, &loiterY, &doorX, &doorY, &footprintBottom); err != nil {
		log.Printf("greet-on-arrival structure read %s: %v", structureID, err)
		return
	}
	if !loiterX.Valid || !loiterY.Valid {
		// No configured loiter — the structure has no defined waiting
		// ring. Nothing to adopt against.
		return
	}
	lx, ly := effectiveLoiterTile(loiterX, loiterY, doorX, doorY, footprintBottom)
	loiterCenterX := ox + float64(lx)*tileSize
	loiterCenterY := oy + float64(ly)*tileSize

	// Locate the active structure huddle. setNPCInside should have
	// created or joined it on the keeper's inside-flag flip just above
	// us; bail with a log if missing.
	var huddleID string
	if err := app.DB.QueryRow(ctx,
		`SELECT id::text FROM scene_huddle
		  WHERE structure_id = $1 AND concluded_at IS NULL
		  ORDER BY created_at DESC LIMIT 1`,
		structureID,
	).Scan(&huddleID); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("greet-on-arrival huddle lookup %s: %v", structureID, err)
		}
		return
	}

	// PCs at the loiter slot, currently in no huddle, not already
	// inside any structure. login_username is the join key for
	// joinOrCreateHuddleForPC, and actor.id is the param shape
	// fireKnockPerception expects.
	rows, err := app.DB.Query(ctx,
		`SELECT id::text, login_username FROM actor
		  WHERE login_username    IS NOT NULL
		    AND inside_structure_id IS NULL
		    AND current_huddle_id   IS NULL
		    AND GREATEST(ABS(current_x - $1), ABS(current_y - $2)) <= 64`,
		loiterCenterX, loiterCenterY,
	)
	if err != nil {
		log.Printf("greet-on-arrival pc query %s: %v", structureID, err)
		return
	}
	defer rows.Close()

	type pcMatch struct {
		ActorID, Username string
	}
	var pcs []pcMatch
	for rows.Next() {
		var pc pcMatch
		if err := rows.Scan(&pc.ActorID, &pc.Username); err != nil {
			log.Printf("greet-on-arrival pc scan: %v", err)
			continue
		}
		pcs = append(pcs, pc)
	}
	if len(pcs) == 0 {
		return
	}

	// Adopt each PC: join the structure huddle (acquaintance row +
	// presence audit fall out of joinOrCreateHuddleForPC) and fire
	// the same knock-style perception cue the PC-initiated knock path
	// uses, so the keeper's next tick perceives "Mary approaches the
	// Tavern and waits at the door."
	for _, pc := range pcs {
		if _, err := app.joinOrCreateHuddleForPC(ctx, pc.Username, structureID); err != nil {
			log.Printf("greet-on-arrival join %s into %s: %v", pc.Username, structureID, err)
			continue
		}
		app.fireKnockPerception(ctx, pc.ActorID, huddleID, structureID)
		log.Printf("greet-on-arrival pc=%s structure=%s huddle=%s — adopted into keeper-arrival huddle",
			pc.ActorID, structureID, huddleID)
	}
}

// adoptVisitorLoiterHuddle joins a just-arrived NPC into the destination
// structure's active scene_huddle when the actor's arrival position is
// within Chebyshev 64 px of the structure's effective loiter pin.
// Mirrors the PC-side loiter-huddle adoption in handlePCMove for NPC
// visitor arrivals where setNPCInside isn't called — owner-policy
// non-owner targets, none-policy decoratives, and agent-visitor
// anyone-policy walks with EnterOnArrival=false.
//
// Without this, an NPC who walks across town to converse with a
// shopkeeper has no huddle scope on arrival: speak() broadcasts to
// the wrong scope (or none) and the keeper never perceives them.
//
// Returns the joined huddle id (or "" when the actor isn't on the
// ring or the join failed) and the count of OTHER actors already in
// that huddle. The caller uses the count to decide whether to fire a
// self-tick — empty huddles don't need an immediate decision.
//
// 64 px tolerance matches actorStructureScope and triggerCoLocatedTicks
// — keep the three predicates in sync.
func (app *App) adoptVisitorLoiterHuddle(ctx context.Context, npcID, structureID string, arrivalX, arrivalY float64) (string, int) {
	if npcID == "" || structureID == "" {
		return "", 0
	}
	var ox, oy float64
	var loiterX, loiterY sql.NullInt32
	var doorX, doorY sql.NullInt32
	var footprintBottom int
	if err := app.DB.QueryRow(ctx,
		`SELECT o.x, o.y,
		        o.loiter_offset_x, o.loiter_offset_y,
		        a.door_offset_x, a.door_offset_y, a.footprint_bottom
		   FROM village_object o
		   JOIN asset a ON a.id = o.asset_id
		  WHERE o.id::text = $1`,
		structureID,
	).Scan(&ox, &oy, &loiterX, &loiterY, &doorX, &doorY, &footprintBottom); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("visitor-loiter-huddle structure read %s: %v", structureID, err)
		}
		return "", 0
	}
	lx, ly := effectiveLoiterTile(loiterX, loiterY, doorX, doorY, footprintBottom)
	pinX := ox + float64(lx)*tileSize
	pinY := oy + float64(ly)*tileSize
	dx := arrivalX - pinX
	if dx < 0 {
		dx = -dx
	}
	dy := arrivalY - pinY
	if dy < 0 {
		dy = -dy
	}
	if max(dx, dy) > 64 {
		return "", 0
	}
	huddleID, err := app.joinOrCreateHuddle(ctx, npcID, structureID)
	if err != nil {
		log.Printf("visitor-loiter-huddle join %s/%s: %v", npcID, structureID, err)
		return "", 0
	}
	var others int
	if err := app.DB.QueryRow(ctx,
		`SELECT COUNT(*) FROM actor
		  WHERE current_huddle_id::text = $1
		    AND id::text != $2`,
		huddleID, npcID,
	).Scan(&others); err != nil {
		log.Printf("visitor-loiter-huddle others count %s: %v", huddleID, err)
		return huddleID, 0
	}
	return huddleID, others
}
