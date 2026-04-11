package main

import (
	"encoding/json"
	"net/http"
)

// handleVillageMe returns the current user's village info (roles, permissions).
func (app *App) handleVillageMe(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	// Check if this user has a village_agent record with editor role
	canEdit := false
	var agentRole string
	err := app.DB.QueryRow(r.Context(),
		`SELECT role FROM village_agent WHERE llm_memory_agent = $1`,
		user.Username,
	).Scan(&agentRole)
	if err == nil {
		canEdit = agentRole == "editor" || agentRole == "admin" || agentRole == "sysop"
	}

	// Jeff (sysop) always gets editor access
	if user.Username == "jeff" {
		canEdit = true
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"agent":    user.Username,
		"can_edit": canEdit,
	})
}

// villageObject represents a placed item on the village map.
type villageObject struct {
	ID        string  `json:"id"`
	CatalogID string  `json:"catalog_id"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	PlacedBy  *string `json:"placed_by"`
	Owner     *string `json:"owner"`
}

// handleListVillageAgents returns all village agents (for owner assignment).
func (app *App) handleListVillageAgents(w http.ResponseWriter, r *http.Request) {
	rows, err := app.DB.Query(r.Context(),
		`SELECT id, name, llm_memory_agent, role, coins, is_virtual
		 FROM village_agent
		 ORDER BY name`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type agent struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Agent     string `json:"llm_memory_agent"`
		Role      string `json:"role"`
		Coins     int    `json:"coins"`
		IsVirtual bool   `json:"is_virtual"`
	}

	agents := []agent{}
	for rows.Next() {
		var a agent
		if err := rows.Scan(&a.ID, &a.Name, &a.Agent, &a.Role, &a.Coins, &a.IsVirtual); err != nil {
			continue
		}
		agents = append(agents, a)
	}

	jsonResponse(w, http.StatusOK, agents)
}

// handleListVillageObjects returns all placed objects.
func (app *App) handleListVillageObjects(w http.ResponseWriter, r *http.Request) {
	rows, err := app.DB.Query(r.Context(),
		`SELECT id, catalog_id, x, y, placed_by, owner
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
		if err := rows.Scan(&obj.ID, &obj.CatalogID, &obj.X, &obj.Y, &obj.PlacedBy, &obj.Owner); err != nil {
			continue
		}
		objects = append(objects, obj)
	}

	jsonResponse(w, http.StatusOK, objects)
}

// handleCreateVillageObject places a new object on the map.
func (app *App) handleCreateVillageObject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CatalogID string  `json:"catalog_id"`
		X         float64 `json:"x"`
		Y         float64 `json:"y"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.CatalogID == "" {
		jsonError(w, "catalog_id is required", http.StatusBadRequest)
		return
	}

	// Get the authenticated user who's placing the object
	user := getUserFromContext(r.Context())
	var placedBy *string
	if user != nil && user.Username != "" {
		placedBy = &user.Username
	}

	id := newUUIDv7()
	_, err := app.DB.Exec(r.Context(),
		`INSERT INTO village_object (id, catalog_id, x, y, placed_by)
		 VALUES ($1, $2, $3, $4, $5)`,
		id, req.CatalogID, req.X, req.Y, placedBy,
	)
	if err != nil {
		jsonError(w, "Failed to create object", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusCreated, villageObject{
		ID:        id,
		CatalogID: req.CatalogID,
		X:         req.X,
		Y:         req.Y,
		PlacedBy:  placedBy,
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

// handleBulkCreateVillageObjects places multiple objects at once (for initial village population).
func (app *App) handleBulkCreateVillageObjects(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Objects []struct {
			CatalogID string  `json:"catalog_id"`
			X         float64 `json:"x"`
			Y         float64 `json:"y"`
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

	tx, err := app.DB.Begin(r.Context())
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	created := make([]villageObject, 0, len(req.Objects))
	for _, obj := range req.Objects {
		if obj.CatalogID == "" {
			continue
		}
		id := newUUIDv7()
		_, err := tx.Exec(r.Context(),
			`INSERT INTO village_object (id, catalog_id, x, y)
			 VALUES ($1, $2, $3, $4)`,
			id, obj.CatalogID, obj.X, obj.Y,
		)
		if err != nil {
			jsonError(w, "Failed to create objects", http.StatusInternalServerError)
			return
		}
		created = append(created, villageObject{
			ID:        id,
			CatalogID: obj.CatalogID,
			X:         obj.X,
			Y:         obj.Y,
		})
	}

	if err := tx.Commit(r.Context()); err != nil {
		jsonError(w, "Failed to commit", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusCreated, created)
}
