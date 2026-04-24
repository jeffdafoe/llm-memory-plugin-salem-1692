package main

// Per-instance tag management for village_object (ZBBS-069).
//
// Identity tags (laundry, notice-board, day-active, …) live on asset_state
// — they describe the asset template. Role tags (tavern, future: shop,
// mayor-house, etc.) live here because the same asset can be placed many
// times with different intended roles. This is the per-instance analog.

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// handleListObjectTags — returns the per-instance tag allowlist, sorted.
// Any authenticated user can read it; mutation is admin-only.
func (app *App) handleListObjectTags(w http.ResponseWriter, r *http.Request) {
	tags := make([]string, 0, len(allowedObjectTags))
	for tag := range allowedObjectTags {
		tags = append(tags, tag)
	}
	// Stable alphabetical order for predictable UI dropdowns.
	for i := 1; i < len(tags); i++ {
		for j := i; j > 0 && tags[j-1] > tags[j]; j-- {
			tags[j-1], tags[j] = tags[j], tags[j-1]
		}
	}
	jsonResponse(w, http.StatusOK, tags)
}

// handleAddObjectTag attaches one tag to a placed object. Admin only.
// Idempotent: the composite PK with ON CONFLICT DO NOTHING absorbs repeat
// calls. Broadcasts the full post-mutation tag set so clients don't need
// to track diffs.
func (app *App) handleAddObjectTag(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin access required", http.StatusForbidden)
		return
	}

	objectID := r.PathValue("id")
	if objectID == "" {
		jsonError(w, "Missing object id", http.StatusBadRequest)
		return
	}

	var req struct {
		Tag string `json:"tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !allowedObjectTags[req.Tag] {
		jsonError(w, "Unknown tag (see /api/village/object-tags)", http.StatusBadRequest)
		return
	}

	// Guard the tag insert on object existence — FK alone would give a
	// generic constraint error; we want a clean 404.
	var exists bool
	if err := app.DB.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM village_object WHERE id = $1)`,
		objectID,
	).Scan(&exists); err != nil || !exists {
		jsonError(w, "Object not found", http.StatusNotFound)
		return
	}

	if _, err := app.DB.Exec(r.Context(),
		`INSERT INTO village_object_tag (object_id, tag) VALUES ($1, $2)
		 ON CONFLICT (object_id, tag) DO NOTHING`,
		objectID, req.Tag,
	); err != nil {
		jsonError(w, "Failed to add tag", http.StatusInternalServerError)
		return
	}

	app.broadcastObjectTags(r.Context(), objectID)
	w.WriteHeader(http.StatusNoContent)
}

// handleRemoveObjectTag detaches one tag from a placed object. Admin only.
// No-op when the pair isn't present — DELETE matches zero rows and the
// client's mental model still converges via the post-mutation broadcast.
func (app *App) handleRemoveObjectTag(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin access required", http.StatusForbidden)
		return
	}

	objectID := r.PathValue("id")
	tag := r.PathValue("tag")
	if objectID == "" || tag == "" {
		jsonError(w, "Missing object id or tag", http.StatusBadRequest)
		return
	}

	if _, err := app.DB.Exec(r.Context(),
		`DELETE FROM village_object_tag WHERE object_id = $1 AND tag = $2`,
		objectID, tag,
	); err != nil {
		jsonError(w, "Failed to remove tag", http.StatusInternalServerError)
		return
	}

	app.broadcastObjectTags(r.Context(), objectID)
	w.WriteHeader(http.StatusNoContent)
}

// broadcastObjectTags reads the current tag set for the object and fans
// it out to every connected client. Used by both add and remove so the
// payload is always the full authoritative set.
func (app *App) broadcastObjectTags(ctx context.Context, objectID string) {
	tags, err := loadObjectTags(ctx, app.DB, objectID)
	if err != nil {
		return
	}
	app.Hub.Broadcast(WorldEvent{
		Type: "village_object_tags_updated",
		Data: map[string]any{
			"object_id": objectID,
			"tags":      tags,
		},
	})
}

// loadObjectTags — one object's current tag list in alphabetical order.
// Used by the broadcast and by the object-list response builder.
func loadObjectTags(ctx context.Context, db dbQueryExec, objectID string) ([]string, error) {
	rows, err := db.Query(ctx,
		`SELECT tag FROM village_object_tag WHERE object_id = $1 ORDER BY tag`,
		objectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := []string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err == nil {
			tags = append(tags, tag)
		}
	}
	return tags, nil
}

// dbQueryExec is the narrow subset of pgxpool.Pool we need. Declared
// locally so loadObjectTags can take either the pool directly or a tx
// if we ever need transactional reads.
type dbQueryExec interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}
