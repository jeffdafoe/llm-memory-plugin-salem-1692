package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
)

// village_agent legacy endpoints — surfaced to the admin dashboard.
//
// Post-ZBBS-084 these read/write the unified `actor` table. The endpoint
// names and JSON shape are kept compatible with what the admin dashboard
// already consumes; some columns dropped by the migration are reported as
// derived values (e.g., location_type is computed from inside_structure_id).

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

// handleListVillageAgents returns all LLM-driven actors with their locations.
// Decorative NPCs (no llm_memory_agent) and PCs are excluded — this endpoint
// historically scoped to "agentized" villagers only.
func (app *App) handleListVillageAgents(w http.ResponseWriter, r *http.Request) {
	rows, err := app.DB.Query(r.Context(),
		`SELECT id, display_name, llm_memory_agent, role, coins,
		        inside_structure_id, current_x, current_y
		 FROM actor
		 WHERE llm_memory_agent IS NOT NULL
		 ORDER BY display_name`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	agents := []villageAgent{}
	for rows.Next() {
		var a villageAgent
		var insideID *string
		var x, y *float64
		if err := rows.Scan(&a.ID, &a.Name, &a.LLMMemoryAgent, &a.Role, &a.Coins,
			&insideID, &x, &y); err != nil {
			continue
		}
		// All agentized actors are virtual by definition (LLM-driven).
		a.IsVirtual = true
		// Derive location_type from inside_structure_id presence.
		switch {
		case insideID != nil:
			a.LocationType = "inside"
			a.LocationObjectID = insideID
		case x != nil && y != nil:
			a.LocationType = "outdoor"
			a.LocationX = x
			a.LocationY = y
		default:
			a.LocationType = "off-map"
		}
		agents = append(agents, a)
	}

	jsonResponse(w, http.StatusOK, agents)
}

// handleMoveAgent updates an agent's location. Writes through to the
// canonical actor.inside_structure_id / current_x/y / inside columns.
//
// Accepts: { "type": "inside", "object_id": "..." }
//       or { "type": "outdoor", "x": 123.0, "y": 456.0 }
//       or { "type": "off-map" }
//
// "off-map" is preserved for compatibility with the admin dashboard but
// post-refactor it just clears inside_structure_id and zeroes the position
// — actor rows always need x/y, so off-map renders as (0, 0). If the admin
// UI never used off-map in practice this is a no-op concern.
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
			`UPDATE actor SET inside = true, inside_structure_id = $1 WHERE id = $2`,
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
			`UPDATE actor SET inside = false, inside_structure_id = NULL,
			                  current_x = $1, current_y = $2
			 WHERE id = $3`,
			*req.X, *req.Y, agentID,
		)
		if err != nil {
			jsonError(w, "Failed to update location", http.StatusInternalServerError)
			return
		}

	case "off-map":
		_, err := app.DB.Exec(r.Context(),
			`UPDATE actor SET inside = false, inside_structure_id = NULL,
			                  current_x = 0, current_y = 0
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

// handleTriggerAgentTick fires an immediate tick on a single agentized
// NPC. Admin / debug — useful when an NPC is stranded, when testing a
// new tool the model should attempt, or when nudging behavior without
// the side effects of a fake speak event. force=true to bypass the
// 5-min cost guard; the operator clicked it on purpose. New scene UUID
// so the tick stands alone in any later transcript grouping.
func (app *App) handleTriggerAgentTick(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if agentID == "" {
		jsonError(w, "Missing agent ID", http.StatusBadRequest)
		return
	}

	// Validate the actor exists and is agentized — triggering a non-
	// agentized actor (decorative NPC, PC) is a no-op that confuses
	// callers. Reject up front with a clear error.
	var displayName sql.NullString
	var isAgent bool
	err := app.DB.QueryRow(r.Context(),
		`SELECT display_name, llm_memory_agent IS NOT NULL
		   FROM actor WHERE id = $1`,
		agentID,
	).Scan(&displayName, &isAgent)
	if err != nil {
		if err == sql.ErrNoRows {
			jsonError(w, "Agent not found", http.StatusNotFound)
			return
		}
		jsonError(w, "Failed to look up agent", http.StatusInternalServerError)
		return
	}
	if !isAgent {
		jsonError(w, "Actor is not LLM-driven", http.StatusBadRequest)
		return
	}

	go app.triggerImmediateTick(context.Background(), agentID, "admin-trigger", true, newUUIDv7(), agentID)
	w.WriteHeader(http.StatusAccepted)
}
