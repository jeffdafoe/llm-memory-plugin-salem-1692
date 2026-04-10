package main

import (
	"encoding/json"
	"net/http"
)

// handlePublicSettings returns all settings where is_public = true.
// Response is a flat key→value object with a "status" field.
func (app *App) handlePublicSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := app.DB.Query(r.Context(),
		`SELECT key, value FROM setting WHERE is_public = true`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	result := make(map[string]interface{})
	hasBBSName := false

	for rows.Next() {
		var key string
		var value *string
		if err := rows.Scan(&key, &value); err != nil {
			continue
		}
		if value != nil {
			result[key] = *value
		} else {
			result[key] = nil
		}
		if key == "bbs_name" {
			hasBBSName = true
		}
	}

	if hasBBSName {
		result["status"] = "ready"
	} else {
		result["status"] = "setup_required"
	}

	jsonResponse(w, http.StatusOK, result)
}

// handleListSettings returns all settings (admin only).
// Response is a flat key→value object.
func (app *App) handleListSettings(w http.ResponseWriter, r *http.Request) {
	rows, err := app.DB.Query(r.Context(),
		`SELECT key, value FROM setting ORDER BY key`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	result := make(map[string]interface{})
	for rows.Next() {
		var key string
		var value *string
		if err := rows.Scan(&key, &value); err != nil {
			continue
		}
		if value != nil {
			result[key] = *value
		} else {
			result[key] = nil
		}
	}

	jsonResponse(w, http.StatusOK, result)
}

// handleUpdateSetting updates a single setting by key (admin only).
func (app *App) handleUpdateSetting(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		jsonError(w, "Missing setting key", http.StatusBadRequest)
		return
	}

	var input struct {
		Value *string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		jsonError(w, "Missing value field.", http.StatusBadRequest)
		return
	}

	// Upsert the setting
	_, err := app.DB.Exec(r.Context(),
		`INSERT INTO setting (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = $2`,
		key, input.Value,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"key":   key,
		"value": input.Value,
	})
}
