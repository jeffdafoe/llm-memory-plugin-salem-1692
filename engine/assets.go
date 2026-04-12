package main

import (
	"net/http"
)

// TilesetPack represents a source tileset pack (e.g. from itch.io)
type TilesetPack struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	URL  *string `json:"url"`
}

// Asset represents a logical game object (tree, stall, wagon, etc.)
type Asset struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Category     string       `json:"category"`
	DefaultState string       `json:"default_state"`
	AnchorX      float64      `json:"anchor_x"`
	AnchorY      float64      `json:"anchor_y"`
	Layer        string       `json:"layer"`
	PackID       *string      `json:"pack_id"`
	Pack         *TilesetPack `json:"pack,omitempty"`
	States       []AssetState `json:"states"`
}

// AssetState is one visual variant of an asset (sprite coordinates for a specific state).
// Animated states have frame_count > 1 — frames are consecutive horizontally in the sheet.
type AssetState struct {
	State      string  `json:"state"`
	Sheet      string  `json:"sheet"`
	SrcX       int     `json:"src_x"`
	SrcY       int     `json:"src_y"`
	SrcW       int     `json:"src_w"`
	SrcH       int     `json:"src_h"`
	FrameCount int     `json:"frame_count"`
	FrameRate  float64 `json:"frame_rate"`
}

// handleListAssets returns all assets with their states and pack info.
// Used by the village client (catalog) and the asset reference panel.
func (app *App) handleListAssets(w http.ResponseWriter, r *http.Request) {
	// Fetch all tileset packs
	packs := map[string]*TilesetPack{}
	packRows, err := app.DB.Query(r.Context(),
		`SELECT id, name, url FROM tileset_pack ORDER BY name`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer packRows.Close()
	for packRows.Next() {
		var p TilesetPack
		if err := packRows.Scan(&p.ID, &p.Name, &p.URL); err != nil {
			continue
		}
		packs[p.ID] = &p
	}

	// Fetch all assets with pack_id
	assetRows, err := app.DB.Query(r.Context(),
		`SELECT id, name, category, default_state, anchor_x, anchor_y, layer, pack_id
		 FROM asset
		 ORDER BY category, name`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer assetRows.Close()

	assets := []Asset{}
	assetIndex := map[string]int{}

	for assetRows.Next() {
		var a Asset
		if err := assetRows.Scan(&a.ID, &a.Name, &a.Category, &a.DefaultState,
			&a.AnchorX, &a.AnchorY, &a.Layer, &a.PackID); err != nil {
			continue
		}
		a.States = []AssetState{}
		if a.PackID != nil {
			if pack, ok := packs[*a.PackID]; ok {
				a.Pack = pack
			}
		}
		assetIndex[a.ID] = len(assets)
		assets = append(assets, a)
	}

	// Fetch all states and attach to their parent asset
	stateRows, err := app.DB.Query(r.Context(),
		`SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate
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
			&s.SrcX, &s.SrcY, &s.SrcW, &s.SrcH, &s.FrameCount, &s.FrameRate); err != nil {
			continue
		}
		if idx, ok := assetIndex[assetID]; ok {
			assets[idx].States = append(assets[idx].States, s)
		}
	}

	jsonResponse(w, http.StatusOK, assets)
}
