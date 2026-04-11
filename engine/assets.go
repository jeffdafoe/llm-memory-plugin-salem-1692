package main

import (
	"net/http"
)

// Asset represents a logical game object (tree, stall, wagon, etc.)
type Asset struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Category     string       `json:"category"`
	DefaultState string       `json:"default_state"`
	AnchorX      float64      `json:"anchor_x"`
	AnchorY      float64      `json:"anchor_y"`
	Layer        string       `json:"layer"`
	States       []AssetState `json:"states"`
}

// AssetState is one visual variant of an asset (sprite coordinates for a specific state)
type AssetState struct {
	State string `json:"state"`
	Sheet string `json:"sheet"`
	SrcX  int    `json:"src_x"`
	SrcY  int    `json:"src_y"`
	SrcW  int    `json:"src_w"`
	SrcH  int    `json:"src_h"`
}

// handleListAssets returns all assets with their states, grouped and ready for rendering.
// Used by both the village client (to build its catalog) and the admin reference sheet.
func (app *App) handleListAssets(w http.ResponseWriter, r *http.Request) {
	// Fetch all assets
	assetRows, err := app.DB.Query(r.Context(),
		`SELECT id, name, category, default_state, anchor_x, anchor_y, layer
		 FROM asset
		 ORDER BY category, name`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer assetRows.Close()

	assets := []Asset{}
	assetIndex := map[string]int{} // asset ID → index in assets slice

	for assetRows.Next() {
		var a Asset
		if err := assetRows.Scan(&a.ID, &a.Name, &a.Category, &a.DefaultState,
			&a.AnchorX, &a.AnchorY, &a.Layer); err != nil {
			continue
		}
		a.States = []AssetState{}
		assetIndex[a.ID] = len(assets)
		assets = append(assets, a)
	}

	// Fetch all states and attach to their parent asset
	stateRows, err := app.DB.Query(r.Context(),
		`SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h
		 FROM asset_state
		 ORDER BY asset_id, state`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer stateRows.Close()

	for stateRows.Next() {
		var assetID string
		var s AssetState
		if err := stateRows.Scan(&assetID, &s.State, &s.Sheet,
			&s.SrcX, &s.SrcY, &s.SrcW, &s.SrcH); err != nil {
			continue
		}
		if idx, ok := assetIndex[assetID]; ok {
			assets[idx].States = append(assets[idx].States, s)
		}
	}

	jsonResponse(w, http.StatusOK, assets)
}
