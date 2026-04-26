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
	"log"
)

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
	if err == sql.ErrNoRows {
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
		`UPDATE npc SET current_huddle_id = $2
		 WHERE id = $1 AND (current_huddle_id IS DISTINCT FROM $2)`,
		npcID, huddleID,
	); err != nil {
		return huddleID, err
	}

	// Record acquaintance with anyone else already in this huddle.
	// Both directions written so the symmetry holds. ON CONFLICT
	// DO NOTHING preserves the original first_interacted_at timestamp.
	if _, err := app.DB.Exec(ctx,
		`INSERT INTO npc_acquaintance (npc_id, other_name)
		 SELECT $1, n.display_name
		   FROM npc n
		  WHERE n.current_huddle_id::text = $2
		    AND n.id != $1
		 ON CONFLICT DO NOTHING`,
		npcID, huddleID,
	); err != nil {
		log.Printf("scene-huddle: record acquaintance %s: %v", npcID, err)
	}
	if _, err := app.DB.Exec(ctx,
		`INSERT INTO npc_acquaintance (npc_id, other_name)
		 SELECT n.id, me.display_name
		   FROM npc n, npc me
		  WHERE n.current_huddle_id::text = $1
		    AND me.id = $2
		    AND n.id != $2
		 ON CONFLICT DO NOTHING`,
		huddleID, npcID,
	); err != nil {
		log.Printf("scene-huddle: record reverse acquaintance for %s: %v", npcID, err)
	}

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
		`SELECT current_huddle_id::text FROM npc WHERE id = $1`,
		npcID,
	).Scan(&huddleID); err != nil {
		log.Printf("scene-huddle: read current_huddle_id for %s: %v", npcID, err)
		return
	}
	if !huddleID.Valid {
		return // not in a huddle — nothing to leave
	}

	if _, err := app.DB.Exec(ctx,
		`UPDATE npc SET current_huddle_id = NULL WHERE id = $1`,
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
		`SELECT
		     (SELECT COUNT(*) FROM npc WHERE current_huddle_id::text = $1) +
		     (SELECT COUNT(*) FROM pc_position WHERE current_huddle_id::text = $1)`,
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
// update goes there instead. Acquaintance recording also runs both
// directions: every NPC currently in the huddle becomes acquainted
// with the PC by name, and the PC's first-meeting timestamp gets
// stamped against each NPC.
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
	if err == sql.ErrNoRows {
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
		`UPDATE pc_position SET current_huddle_id = $2
		 WHERE actor_name = $1 AND (current_huddle_id IS DISTINCT FROM $2)`,
		actorName, huddleID,
	); err != nil {
		return huddleID, err
	}

	// Each NPC in this huddle now knows the PC by name (one-way; PC's
	// own awareness is UI-side).
	if _, err := app.DB.Exec(ctx,
		`INSERT INTO npc_acquaintance (npc_id, other_name)
		 SELECT n.id, $1
		   FROM npc n
		  WHERE n.current_huddle_id::text = $2
		 ON CONFLICT DO NOTHING`,
		actorName, huddleID,
	); err != nil {
		log.Printf("scene-huddle: record PC->NPC acquaintance: %v", err)
	}

	return huddleID, nil
}

// leaveHuddleForPC mirrors leaveHuddle for PC actors. Clears the PC's
// current_huddle_id and concludes the huddle if the count drops to 0.
func (app *App) leaveHuddleForPC(ctx context.Context, actorName string) {
	var huddleID sql.NullString
	if err := app.DB.QueryRow(ctx,
		`SELECT current_huddle_id::text FROM pc_position WHERE actor_name = $1`,
		actorName,
	).Scan(&huddleID); err != nil {
		log.Printf("scene-huddle: read PC current_huddle_id for %s: %v", actorName, err)
		return
	}
	if !huddleID.Valid {
		return
	}

	if _, err := app.DB.Exec(ctx,
		`UPDATE pc_position SET current_huddle_id = NULL WHERE actor_name = $1`,
		actorName,
	); err != nil {
		log.Printf("scene-huddle: clear PC current_huddle_id for %s: %v", actorName, err)
		return
	}

	var remaining int
	if err := app.DB.QueryRow(ctx,
		`SELECT
		     (SELECT COUNT(*) FROM npc WHERE current_huddle_id::text = $1) +
		     (SELECT COUNT(*) FROM pc_position WHERE current_huddle_id::text = $1)`,
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
