package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// handleVillageMe returns the current user's info and permissions.
// Edit access is determined by the llm-memory admin role — admin users
// who are in the salem realm can edit. Regular realm members can view.
func (app *App) handleVillageMe(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	// For now, edit permission comes from the verify response.
	// Admin users (web session login) get edit access.
	// This will be refined when we add proper role management.
	canEdit := user.hasRole("ROLE_SALEM_ADMIN")

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"agent":    user.Username,
		"can_edit": canEdit,
	})
}

// villageObject represents a placed item on the village map.
type villageObject struct {
	ID           string   `json:"id"`
	AssetID      string   `json:"asset_id"`
	CurrentState string   `json:"current_state"`
	X            float64  `json:"x"`
	Y            float64  `json:"y"`
	PlacedBy     *string  `json:"placed_by"`
	Owner        *string  `json:"owner"`
	DisplayName  *string  `json:"display_name"`
	AttachedTo   *string  `json:"attached_to"`
	// Per-instance tags (ZBBS-069) — role tags applied to THIS placed
	// object. Always a (possibly empty) array, never null, so client code
	// can iterate without a nil check.
	Tags []string `json:"tags"`
	// Per-instance loiter offset (ZBBS-075) — tile-unit offset from this
	// placement's origin where visiting NPCs stand. NULL means "no
	// override; fall back to the asset's door_offset." Editor renders a
	// draggable pin on the placement when set.
	LoiterOffsetX *int `json:"loiter_offset_x"`
	LoiterOffsetY *int `json:"loiter_offset_y"`
}

// handleListVillageObjects returns all placed objects.
// LEFT JOIN LATERAL keeps the one-row-per-object shape while folding the
// object's tag set into a PG array in a single round-trip.
func (app *App) handleListVillageObjects(w http.ResponseWriter, r *http.Request) {
	rows, err := app.DB.Query(r.Context(),
		`SELECT o.id, o.asset_id, o.current_state, o.x, o.y,
		        o.placed_by, o.owner, o.display_name, o.attached_to,
		        COALESCE(t.tags, ARRAY[]::varchar[]),
		        o.loiter_offset_x, o.loiter_offset_y
		 FROM village_object o
		 LEFT JOIN LATERAL (
		     SELECT array_agg(tag ORDER BY tag) AS tags
		     FROM village_object_tag
		     WHERE object_id = o.id
		 ) t ON TRUE
		 ORDER BY o.created_at`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	objects := []villageObject{}
	for rows.Next() {
		var obj villageObject
		if err := rows.Scan(&obj.ID, &obj.AssetID, &obj.CurrentState,
			&obj.X, &obj.Y, &obj.PlacedBy, &obj.Owner, &obj.DisplayName, &obj.AttachedTo,
			&obj.Tags,
			&obj.LoiterOffsetX, &obj.LoiterOffsetY); err != nil {
			continue
		}
		objects = append(objects, obj)
	}

	jsonResponse(w, http.StatusOK, objects)
}

// handleCreateVillageObject places a new object on the map.
func (app *App) handleCreateVillageObject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssetID    string  `json:"asset_id"`
		X          float64 `json:"x"`
		Y          float64 `json:"y"`
		AttachedTo *string `json:"attached_to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.AssetID == "" {
		jsonError(w, "asset_id is required", http.StatusBadRequest)
		return
	}

	// Look up the asset's default state
	var defaultState string
	err := app.DB.QueryRow(r.Context(),
		`SELECT default_state FROM asset WHERE id = $1`, req.AssetID,
	).Scan(&defaultState)
	if err != nil {
		jsonError(w, "Unknown asset_id", http.StatusBadRequest)
		return
	}

	// Get the authenticated user who's placing the object
	user := getUserFromContext(r.Context())
	var placedBy *string
	if user != nil && user.Username != "" {
		placedBy = &user.Username
	}

	id := newUUIDv7()
	_, err = app.DB.Exec(r.Context(),
		`INSERT INTO village_object (id, asset_id, current_state, x, y, placed_by, attached_to)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, req.AssetID, defaultState, req.X, req.Y, placedBy, req.AttachedTo,
	)
	if err != nil {
		jsonError(w, "Failed to create object", http.StatusInternalServerError)
		return
	}

	obj := villageObject{
		ID:           id,
		AssetID:      req.AssetID,
		CurrentState: defaultState,
		X:            req.X,
		Y:            req.Y,
		PlacedBy:     placedBy,
		AttachedTo:   req.AttachedTo,
	}
	jsonResponse(w, http.StatusCreated, obj)
	app.Hub.Broadcast(WorldEvent{Type: "object_created", Data: obj})
}

// handleDeleteVillageObject removes an object from the map.
func (app *App) handleDeleteVillageObject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "Missing object ID", http.StatusBadRequest)
		return
	}

	result, err := app.DB.Exec(r.Context(),
		`DELETE FROM village_object WHERE id = $1`, id,
	)
	if err != nil {
		jsonError(w, "Failed to delete object", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		jsonError(w, "Object not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{Type: "object_deleted", Data: map[string]string{"id": id}})
}

// handleSetVillageObjectOwner assigns or changes the owner of an object.
func (app *App) handleSetVillageObjectOwner(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "Missing object ID", http.StatusBadRequest)
		return
	}

	var req struct {
		Owner *string `json:"owner"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	result, err := app.DB.Exec(r.Context(),
		`UPDATE village_object SET owner = $1 WHERE id = $2`,
		req.Owner, id,
	)
	if err != nil {
		jsonError(w, "Failed to update owner", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		jsonError(w, "Object not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{Type: "object_owner_changed", Data: map[string]interface{}{"id": id, "owner": req.Owner}})
}

// handleSetVillageObjectDisplayName assigns or changes the display name of an object.
func (app *App) handleSetVillageObjectDisplayName(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "Missing object ID", http.StatusBadRequest)
		return
	}

	var req struct {
		DisplayName *string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	result, err := app.DB.Exec(r.Context(),
		`UPDATE village_object SET display_name = $1 WHERE id = $2`,
		req.DisplayName, id,
	)
	if err != nil {
		jsonError(w, "Failed to update display name", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		jsonError(w, "Object not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{Type: "object_display_name_changed", Data: map[string]interface{}{"id": id, "display_name": req.DisplayName}})
}

// handleSetVillageObjectState changes the current state of a placed object.
func (app *App) handleSetVillageObjectState(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "Missing object ID", http.StatusBadRequest)
		return
	}

	var req struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.State == "" {
		jsonError(w, "state is required", http.StatusBadRequest)
		return
	}

	// Verify the state exists for this object's asset
	var exists bool
	err := app.DB.QueryRow(r.Context(),
		`SELECT EXISTS(
			SELECT 1 FROM asset_state s
			JOIN village_object o ON o.asset_id = s.asset_id
			WHERE o.id = $1 AND s.state = $2
		)`, id, req.State,
	).Scan(&exists)
	if err != nil || !exists {
		jsonError(w, "Invalid state for this asset", http.StatusBadRequest)
		return
	}

	result, err := app.DB.Exec(r.Context(),
		`UPDATE village_object SET current_state = $1 WHERE id = $2`,
		req.State, id,
	)
	if err != nil {
		jsonError(w, "Failed to update state", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		jsonError(w, "Object not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{Type: "object_state_changed", Data: map[string]string{"id": id, "state": req.State}})
}

// handleMoveVillageObject updates the position of a placed object.
func (app *App) handleMoveVillageObject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "Missing object ID", http.StatusBadRequest)
		return
	}

	var req struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	result, err := app.DB.Exec(r.Context(),
		`UPDATE village_object SET x = $1, y = $2 WHERE id = $3`,
		req.X, req.Y, id,
	)
	if err != nil {
		jsonError(w, "Failed to move object", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		jsonError(w, "Object not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{Type: "object_moved", Data: map[string]interface{}{"id": id, "x": req.X, "y": req.Y}})
}

// handleSetVillageObjectLoiterOffset (ZBBS-075) updates the per-instance
// loiter offset where visiting NPCs stand outside this placement. Both
// values are tile-unit integers; null clears the override and reverts to
// asset.door_offset behavior.
//
// Body: { loiter_offset_x: int|null, loiter_offset_y: int|null }
//
// "Both or neither": if one is set the other must be too. The agent
// resolver always reads them together so a mixed state would be ambiguous.
//
// Side effect: NPCs currently standing at the OLD loiter position get
// re-walked to the NEW one. Without this, an admin moving a placement's
// loiter pin while NPCs are visiting would leave them stuck in the
// original (now likely visually wrong) spot — e.g. inside the well sprite
// after the loiter pin moves to the adjacent tile. Owners of this
// placement (home or work) are skipped — their position is governed by
// the scheduler/inside-structure flow, not the loiter pin.
func (app *App) handleSetVillageObjectLoiterOffset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "Missing object ID", http.StatusBadRequest)
		return
	}

	var req struct {
		LoiterOffsetX *int `json:"loiter_offset_x"`
		LoiterOffsetY *int `json:"loiter_offset_y"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if (req.LoiterOffsetX == nil) != (req.LoiterOffsetY == nil) {
		jsonError(w, "loiter_offset_x and loiter_offset_y must both be set or both null", http.StatusBadRequest)
		return
	}

	// Read OLD offset + asset door fallback BEFORE the UPDATE so we can
	// compute the position visitors would have walked to. Same fallback
	// chain pickWalkTarget uses (loiter → door → anchor).
	var oldLoiterX, oldLoiterY sql.NullInt32
	var doorX, doorY sql.NullInt32
	var anchorX, anchorY float64
	if err := app.DB.QueryRow(r.Context(),
		`SELECT o.loiter_offset_x, o.loiter_offset_y,
		        a.door_offset_x, a.door_offset_y,
		        o.x, o.y
		 FROM village_object o JOIN asset a ON a.id = o.asset_id
		 WHERE o.id = $1`,
		id).Scan(&oldLoiterX, &oldLoiterY, &doorX, &doorY, &anchorX, &anchorY); err != nil {
		if err == sql.ErrNoRows {
			jsonError(w, "Object not found", http.StatusNotFound)
			return
		}
		jsonError(w, "Failed to read placement", http.StatusInternalServerError)
		return
	}

	result, err := app.DB.Exec(r.Context(),
		`UPDATE village_object SET loiter_offset_x = $1, loiter_offset_y = $2 WHERE id = $3`,
		req.LoiterOffsetX, req.LoiterOffsetY, id,
	)
	if err != nil {
		jsonError(w, "Failed to update loiter offset", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		jsonError(w, "Object not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{
		Type: "object_loiter_offset_changed",
		Data: map[string]interface{}{
			"id":              id,
			"loiter_offset_x": req.LoiterOffsetX,
			"loiter_offset_y": req.LoiterOffsetY,
		},
	})

	// Fire-and-forget the visitor relocate; failures are logged but don't
	// fail the PATCH response (the offset itself is already saved).
	go app.relocateVisitorsAfterLoiterChange(
		context.Background(), id, anchorX, anchorY,
		oldLoiterX, oldLoiterY, req.LoiterOffsetX, req.LoiterOffsetY, doorX, doorY,
	)
}

// handleBulkCreateVillageObjects places multiple objects at once (for initial village population).
func (app *App) handleBulkCreateVillageObjects(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Objects []struct {
			AssetID string  `json:"asset_id"`
			X       float64 `json:"x"`
			Y       float64 `json:"y"`
		} `json:"objects"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.Objects) == 0 {
		jsonError(w, "No objects provided", http.StatusBadRequest)
		return
	}

	// Pre-fetch default states for all referenced assets
	defaultStates := map[string]string{}
	stateRows, err := app.DB.Query(r.Context(),
		`SELECT id, default_state FROM asset`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer stateRows.Close()
	for stateRows.Next() {
		var id, state string
		if err := stateRows.Scan(&id, &state); err != nil {
			continue
		}
		defaultStates[id] = state
	}

	tx, err := app.DB.Begin(r.Context())
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	created := make([]villageObject, 0, len(req.Objects))
	for _, obj := range req.Objects {
		if obj.AssetID == "" {
			continue
		}
		state, ok := defaultStates[obj.AssetID]
		if !ok {
			continue // skip unknown assets
		}
		id := newUUIDv7()
		_, err := tx.Exec(r.Context(),
			`INSERT INTO village_object (id, asset_id, current_state, x, y)
			 VALUES ($1, $2, $3, $4, $5)`,
			id, obj.AssetID, state, obj.X, obj.Y,
		)
		if err != nil {
			jsonError(w, "Failed to create objects", http.StatusInternalServerError)
			return
		}
		created = append(created, villageObject{
			ID:           id,
			AssetID:      obj.AssetID,
			CurrentState: state,
			X:            obj.X,
			Y:            obj.Y,
		})
	}

	if err := tx.Commit(r.Context()); err != nil {
		jsonError(w, "Failed to commit", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusCreated, created)
}

// relocateVisitorsAfterLoiterChange walks any NPC currently standing
// near the OLD loiter position to the NEW one when an admin moves a
// placement's loiter pin. Owners (this placement is their home or work)
// are skipped — their position belongs to the scheduler/inside-structure
// flow, not the loiter pin.
//
// "Near" is defined as within 1.5 tiles of the OLD position — covers the
// loiterJitter spread (~half tile) plus the typical walk-arrival fuzz.
//
// Each relocate is dispatched as an independent walk via startReturnWalk,
// stamping agent_override_until so the scheduler doesn't yank the NPC
// back mid-walk. NEW position is jittered per-NPC so a cluster of
// visitors at the moved spot stays spread out.
func (app *App) relocateVisitorsAfterLoiterChange(
	ctx context.Context, objectID string, anchorX, anchorY float64,
	oldLoiterX, oldLoiterY sql.NullInt32,
	newOffsetX, newOffsetY *int,
	doorX, doorY sql.NullInt32,
) {
	const tileSize = 32.0
	const nearRadius = 1.5 * tileSize
	const nearRadiusSq = nearRadius * nearRadius

	// OLD pixel position — same fallback chain as pickWalkTarget.
	oldPx, oldPy := anchorX, anchorY
	switch {
	case oldLoiterX.Valid && oldLoiterY.Valid:
		oldPx = anchorX + float64(oldLoiterX.Int32)*tileSize
		oldPy = anchorY + float64(oldLoiterY.Int32)*tileSize
	case doorX.Valid && doorY.Valid:
		oldPx = anchorX + float64(doorX.Int32)*tileSize
		oldPy = anchorY + float64(doorY.Int32)*tileSize
	}

	// NEW pixel position — same fallback chain.
	newPx, newPy := anchorX, anchorY
	switch {
	case newOffsetX != nil && newOffsetY != nil:
		newPx = anchorX + float64(*newOffsetX)*tileSize
		newPy = anchorY + float64(*newOffsetY)*tileSize
	case doorX.Valid && doorY.Valid:
		newPx = anchorX + float64(doorX.Int32)*tileSize
		newPy = anchorY + float64(doorY.Int32)*tileSize
	}

	// Find candidates: NPCs inside the near-radius of OLD that don't own
	// this placement. Exclude NPCs already walking — startReturnWalk on a
	// moving NPC stomps the in-flight walk; we'd rather let it land first
	// and (if relevant) re-relocate on a subsequent admin move.
	rows, err := app.DB.Query(ctx,
		`SELECT id, current_x, current_y FROM npc
		 WHERE (current_x - $1) * (current_x - $1) + (current_y - $2) * (current_y - $2) < $3
		   AND (home_structure_id IS NULL OR home_structure_id::text != $4)
		   AND (work_structure_id IS NULL OR work_structure_id::text != $4)`,
		oldPx, oldPy, nearRadiusSq, objectID)
	if err != nil {
		log.Printf("relocateVisitors: query: %v", err)
		return
	}
	defer rows.Close()

	type candidate struct {
		ID  string
		CurX, CurY float64
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ID, &c.CurX, &c.CurY); err == nil {
			candidates = append(candidates, c)
		}
	}

	for _, c := range candidates {
		jx, jy := loiterJitter()
		targetX, targetY := newPx+jx, newPy+jy
		npc := &behaviorNPC{ID: c.ID, CurX: c.CurX, CurY: c.CurY}
		app.interpolateCurrentPos(npc)
		if err := app.startReturnWalk(ctx, npc, targetX, targetY, objectID, "loiter-relocate"); err != nil {
			log.Printf("relocateVisitors: startReturnWalk %s: %v", c.ID, err)
			continue
		}
		overrideUntil := time.Now().Add(30 * time.Minute)
		if _, err := app.DB.Exec(ctx,
			`UPDATE npc SET agent_override_until = $2, last_shift_tick_at = $2 WHERE id = $1`,
			c.ID, overrideUntil,
		); err != nil {
			log.Printf("relocateVisitors: stamp override %s: %v", c.ID, err)
		}
	}
}
