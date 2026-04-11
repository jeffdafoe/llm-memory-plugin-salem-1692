package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
)

// terrainResponse is the JSON shape for terrain data.
type terrainResponse struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Data   string `json:"data"` // base64-encoded byte array, one byte per cell
}

// handleGetTerrain returns the saved terrain grid, or 404 if none exists.
// The client falls back to procedural generation when no saved terrain is found.
func (app *App) handleGetTerrain(w http.ResponseWriter, r *http.Request) {
	var width, height int
	var data []byte

	err := app.DB.QueryRow(r.Context(),
		`SELECT width, height, data FROM village_terrain WHERE id = 1`,
	).Scan(&width, &height, &data)
	if err != nil {
		// No saved terrain — client should use procedural generation
		jsonError(w, "No terrain data", http.StatusNotFound)
		return
	}

	jsonResponse(w, http.StatusOK, terrainResponse{
		Width:  width,
		Height: height,
		Data:   base64.StdEncoding.EncodeToString(data),
	})
}

// handleSaveTerrain saves the terrain grid. Uses upsert so the first save
// creates the row and subsequent saves update it.
func (app *App) handleSaveTerrain(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	// Only admins can edit terrain
	if !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Forbidden", http.StatusForbidden)
		return
	}

	var req terrainResponse
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Width <= 0 || req.Height <= 0 {
		jsonError(w, "Width and height must be positive", http.StatusBadRequest)
		return
	}

	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		jsonError(w, "Invalid base64 data", http.StatusBadRequest)
		return
	}

	expectedSize := req.Width * req.Height
	if len(data) != expectedSize {
		jsonError(w, "Data size does not match width * height", http.StatusBadRequest)
		return
	}

	// Upsert: insert if no row exists, update if it does
	_, err = app.DB.Exec(r.Context(),
		`INSERT INTO village_terrain (id, width, height, data, updated_by, updated_at)
		 VALUES (1, $1, $2, $3, $4, NOW())
		 ON CONFLICT (id) DO UPDATE
		 SET width = $1, height = $2, data = $3, updated_by = $4, updated_at = NOW()`,
		req.Width, req.Height, data, user.Username,
	)
	if err != nil {
		jsonError(w, "Failed to save terrain", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
