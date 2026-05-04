package main

import (
	"context"
	"database/sql"
	"log"
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
