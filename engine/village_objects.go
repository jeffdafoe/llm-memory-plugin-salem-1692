package main

import (
	"encoding/json"
	"net/http"
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
	ID           string  `json:"id"`
	AssetID      string  `json:"asset_id"`
	CurrentState string  `json:"current_state"`
	X            float64 `json:"x"`
	Y            float64 `json:"y"`
	PlacedBy     *string `json:"placed_by"`
	Owner        *string `json:"owner"`
}

// handleListVillageObjects returns all placed objects.
func (app *App) handleListVillageObjects(w http.ResponseWriter, r *http.Request) {
	rows, err := app.DB.Query(r.Context(),
		`SELECT id, asset_id, current_state, x, y, placed_by, owner
		 FROM village_object
		 ORDER BY created_at`,
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
			&obj.X, &obj.Y, &obj.PlacedBy, &obj.Owner); err != nil {
			continue
		}
		objects = append(objects, obj)
	}

	jsonResponse(w, http.StatusOK, objects)
}

// handleCreateVillageObject places a new object on the map.
func (app *App) handleCreateVillageObject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssetID string  `json:"asset_id"`
		X       float64 `json:"x"`
		Y       float64 `json:"y"`
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
		`INSERT INTO village_object (id, asset_id, current_state, x, y, placed_by)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		id, req.AssetID, defaultState, req.X, req.Y, placedBy,
	)
	if err != nil {
		jsonError(w, "Failed to create object", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusCreated, villageObject{
		ID:           id,
		AssetID:      req.AssetID,
		CurrentState: defaultState,
		X:            req.X,
		Y:            req.Y,
		PlacedBy:     placedBy,
	})
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
