package main

import (
	"net/http"
)

// handleVillageBuildings returns all buildings with their residents.
// Authenticated via llm-memory session token.
func (app *App) handleVillageBuildings(w http.ResponseWriter, r *http.Request) {
	// Fetch buildings
	buildingRows, err := app.DB.Query(r.Context(),
		`SELECT id, tile_x, tile_y, building_style, building_variant
		 FROM village_building
		 ORDER BY created_at`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer buildingRows.Close()

	type resident struct {
		Name           string `json:"name"`
		LLMMemoryAgent string `json:"llm_memory_agent"`
		Role           string `json:"role"`
		Coins          int    `json:"coins"`
		IsVirtual      bool   `json:"is_virtual"`
	}

	type building struct {
		ID              string     `json:"id"`
		TileX           int        `json:"tile_x"`
		TileY           int        `json:"tile_y"`
		BuildingStyle   string     `json:"building_style"`
		BuildingVariant int        `json:"building_variant"`
		Residents       []resident `json:"residents"`
	}

	// Collect buildings and track order
	var buildings []building
	buildingIndex := make(map[string]int) // id → index in buildings slice

	for buildingRows.Next() {
		var b building
		if err := buildingRows.Scan(&b.ID, &b.TileX, &b.TileY, &b.BuildingStyle, &b.BuildingVariant); err != nil {
			continue
		}
		b.Residents = []resident{}
		buildingIndex[b.ID] = len(buildings)
		buildings = append(buildings, b)
	}

	if len(buildings) == 0 {
		jsonResponse(w, http.StatusOK, []building{})
		return
	}

	// Fetch all residents with their building assignments
	residentRows, err := app.DB.Query(r.Context(),
		`SELECT vbr.building_id, va.name, va.llm_memory_agent, va.role, va.coins, va.is_virtual
		 FROM village_building_resident vbr
		 JOIN village_agent va ON va.id = vbr.agent_id
		 ORDER BY vbr.moved_in_at`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer residentRows.Close()

	for residentRows.Next() {
		var buildingID string
		var res resident
		if err := residentRows.Scan(&buildingID, &res.Name, &res.LLMMemoryAgent, &res.Role, &res.Coins, &res.IsVirtual); err != nil {
			continue
		}
		if idx, ok := buildingIndex[buildingID]; ok {
			buildings[idx].Residents = append(buildings[idx].Residents, res)
		}
	}

	jsonResponse(w, http.StatusOK, buildings)
}
