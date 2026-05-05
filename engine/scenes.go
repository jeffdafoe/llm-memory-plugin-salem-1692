package main

import (
	"context"
	"database/sql"
	"log"

	"github.com/jackc/pgx/v5"
)

// newScene mints a fresh scene UUID and records it in the scenes table
// alongside the structure where the cascade originates. structureID may
// be empty when the cascade isn't tied to one place — chronicler phase /
// shift-boundary fires, noticeboard content generation, admin trigger
// pokes — in which case the row stores NULL and the admin UI renders no
// location chip for that scene.
//
// All previous newUUIDv7() call sites that started a chat-bearing
// cascade should call this instead so the scene_id ↔ structure_id link
// is established at mint time. Reactor ticks inside the cascade keep
// using the inherited scene_id and don't need to insert again.
//
// Insert failure is logged but non-fatal — losing the scenes row only
// loses location attribution in the admin UI; the cascade itself still
// runs and chat rows still carry the scene_id. Better to keep the
// simulation moving than to abort a cascade because of a journaling
// hiccup.
func (app *App) newScene(ctx context.Context, structureID string) string {
	sceneID := newUUIDv7()
	var structureArg sql.NullString
	if structureID != "" {
		structureArg = sql.NullString{String: structureID, Valid: true}
	}
	if _, err := app.DB.Exec(ctx,
		`INSERT INTO scenes (scene_id, structure_id) VALUES ($1, $2)`,
		sceneID, structureArg,
	); err != nil {
		log.Printf("scenes: insert %s (structure=%q) failed: %v", sceneID, structureID, err)
	}
	return sceneID
}

// lookupSceneStructureName resolves a scene's structure name in a
// single engine-local query. Used by chat-send paths to populate the
// scene_structure stamp on memory_api's chat_message_texts row, since
// memory_api can't JOIN to the engine's scenes/village_object/asset
// tables (different database). MEM-127 / fixes ZBBS-118 cross-DB JOIN
// regression.
//
// Empty string when sceneID is empty, when no scenes row matches
// (chronicler-only / admin-trigger / noticeboard cascades store NULL
// structure_id), or when the lookup itself fails. Empty propagates to
// the API as a NULL scene_structure column, which the comms page
// renders as no chip — same as companion-mode chat.
//
// Single LEFT JOIN, single round-trip, indexed by scenes.scene_id PK.
// Negligible cost per chat send.
func (app *App) lookupSceneStructureName(ctx context.Context, sceneID string) string {
	if sceneID == "" {
		return ""
	}
	var name sql.NullString
	err := app.DB.QueryRow(ctx, `
		SELECT COALESCE(o.display_name, a.name)
		  FROM scenes sc
		  LEFT JOIN village_object o ON o.id = sc.structure_id
		  LEFT JOIN asset a ON a.id = o.asset_id
		 WHERE sc.scene_id = $1
	`, sceneID).Scan(&name)
	if err != nil {
		if err != pgx.ErrNoRows {
			log.Printf("scenes: lookup structure name for %s: %v", sceneID, err)
		}
		return ""
	}
	if !name.Valid {
		return ""
	}
	return name.String
}
