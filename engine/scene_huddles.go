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
