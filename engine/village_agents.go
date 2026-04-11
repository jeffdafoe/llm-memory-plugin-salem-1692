package main

import (
	"encoding/json"
	"net/http"
)

type villageAgent struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	LLMMemoryAgent   string   `json:"llm_memory_agent"`
	Role             string   `json:"role"`
	Coins            int      `json:"coins"`
	IsVirtual        bool     `json:"is_virtual"`
	LocationType     string   `json:"location_type"`
	LocationObjectID *string  `json:"location_object_id"`
	LocationX        *float64 `json:"location_x"`
	LocationY        *float64 `json:"location_y"`
}

// handleListVillageAgents returns all village agents with their locations.
func (app *App) handleListVillageAgents(w http.ResponseWriter, r *http.Request) {
	rows, err := app.DB.Query(r.Context(),
		`SELECT id, name, llm_memory_agent, role, coins, is_virtual,
		        location_type, location_object_id, location_x, location_y
		 FROM village_agent
		 ORDER BY name`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	agents := []villageAgent{}
	for rows.Next() {
		var a villageAgent
		if err := rows.Scan(&a.ID, &a.Name, &a.LLMMemoryAgent, &a.Role, &a.Coins, &a.IsVirtual,
			&a.LocationType, &a.LocationObjectID, &a.LocationX, &a.LocationY); err != nil {
			continue
		}
		agents = append(agents, a)
	}

	jsonResponse(w, http.StatusOK, agents)
}

// handleMoveAgent updates an agent's location.
// Accepts: { "type": "inside", "object_id": "..." }
//       or { "type": "outdoor", "x": 123.0, "y": 456.0 }
//       or { "type": "off-map" }
func (app *App) handleMoveAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if agentID == "" {
		jsonError(w, "Missing agent ID", http.StatusBadRequest)
		return
	}

	var req struct {
		Type     string   `json:"type"`
		ObjectID *string  `json:"object_id"`
		X        *float64 `json:"x"`
		Y        *float64 `json:"y"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	switch req.Type {
	case "inside":
		if req.ObjectID == nil || *req.ObjectID == "" {
			jsonError(w, "object_id required for inside location", http.StatusBadRequest)
			return
		}
		// Verify the object exists
		var exists bool
		err := app.DB.QueryRow(r.Context(),
			`SELECT EXISTS(SELECT 1 FROM village_object WHERE id = $1)`,
			*req.ObjectID,
		).Scan(&exists)
		if err != nil || !exists {
			jsonError(w, "Object not found", http.StatusNotFound)
			return
		}
		_, err = app.DB.Exec(r.Context(),
			`UPDATE village_agent
			 SET location_type = 'inside', location_object_id = $1, location_x = NULL, location_y = NULL
			 WHERE id = $2`,
			*req.ObjectID, agentID,
		)
		if err != nil {
			jsonError(w, "Failed to update location", http.StatusInternalServerError)
			return
		}

	case "outdoor":
		if req.X == nil || req.Y == nil {
			jsonError(w, "x and y required for outdoor location", http.StatusBadRequest)
			return
		}
		_, err := app.DB.Exec(r.Context(),
			`UPDATE village_agent
			 SET location_type = 'outdoor', location_object_id = NULL, location_x = $1, location_y = $2
			 WHERE id = $3`,
			*req.X, *req.Y, agentID,
		)
		if err != nil {
			jsonError(w, "Failed to update location", http.StatusInternalServerError)
			return
		}

	case "off-map":
		_, err := app.DB.Exec(r.Context(),
			`UPDATE village_agent
			 SET location_type = 'off-map', location_object_id = NULL, location_x = NULL, location_y = NULL
			 WHERE id = $1`,
			agentID,
		)
		if err != nil {
			jsonError(w, "Failed to update location", http.StatusInternalServerError)
			return
		}

	default:
		jsonError(w, "type must be 'inside', 'outdoor', or 'off-map'", http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
