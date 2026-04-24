package main

import (
	"context"
	"encoding/json"
	"net/http"
)

// TilesetPack represents a source tileset pack (e.g. from itch.io)
type TilesetPack struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	URL  *string `json:"url"`
}

// AssetSlot defines a named attachment point on an asset (e.g., campfire has a "top" slot).
// Overlay assets declare which slot they fit via FitsSlot.
type AssetSlot struct {
	SlotName string `json:"slot_name"`
	OffsetX  int    `json:"offset_x"`
	OffsetY  int    `json:"offset_y"`
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
	FitsSlot     *string      `json:"fits_slot"`
	ZIndex       int          `json:"z_index"` // Godot CanvasItem z; <0 renders below NPCs (bridges, ground decals)
	IsObstacle   bool         `json:"is_obstacle"`
	IsPassage    bool         `json:"is_passage"`
	// Per-side footprint counts (tiles from anchor in each cardinal
	// direction, anchor tile always included). Editable from the editor's
	// drag-resize border. See pathfind.go for how these are stamped.
	FootprintLeft   int          `json:"footprint_left"`
	FootprintRight  int          `json:"footprint_right"`
	FootprintTop    int          `json:"footprint_top"`
	FootprintBottom int          `json:"footprint_bottom"`
	// Door tile offset in tiles from the placement origin. NULL for
	// non-structures or structures that haven't had a door placed yet;
	// home-routing falls back to findPathToAdjacent in that case.
	DoorOffsetX     *int         `json:"door_offset_x"`
	DoorOffsetY     *int         `json:"door_offset_y"`
	// Enterable controls whether an NPC can "go inside" this asset — i.e.
	// whether the editor shows a door marker, whether it's a valid home/
	// work target, and whether home-routing picks up its door offset.
	// Orthogonal to category so tents (category=tent) can still be homes.
	Enterable       bool         `json:"enterable"`
	// VisibleWhenInside controls client-side rendering. Default false
	// hides the villager sprite when inside=true (plain houses). True for
	// see-through structures like market stalls where the admin wants the
	// NPC visible at the door tile.
	VisibleWhenInside bool       `json:"visible_when_inside"`
	// StandOffsetX/Y is a pure-render position offset for NPCs who are
	// inside a visible_when_inside structure. The NPC still walks to
	// the door tile (pathfinding target); on arrival the client
	// repositions them to structure_anchor + stand_offset * tile_size
	// so they render behind the counter rather than in the doorway.
	// NULL falls back to door position (NPC stays at the door tile).
	StandOffsetX    *int         `json:"stand_offset_x"`
	StandOffsetY    *int         `json:"stand_offset_y"`
	Pack            *TilesetPack `json:"pack,omitempty"`
	States          []AssetState `json:"states"`
	Slots           []AssetSlot  `json:"slots"`
}

// AssetState is one visual variant of an asset (sprite coordinates for a specific state).
// Animated states have frame_count > 1 — frames are consecutive horizontally in the sheet.
// Tags come from asset_state_tag and drive scheduled behaviors (rotatable,
// day-active / night-active, lamplighter-target, laundry, notice-board,
// occupied / unoccupied). Empty slice when no tags are set.
type AssetState struct {
	State      string      `json:"state"`
	Sheet      string      `json:"sheet"`
	SrcX       int         `json:"src_x"`
	SrcY       int         `json:"src_y"`
	SrcW       int         `json:"src_w"`
	SrcH       int         `json:"src_h"`
	FrameCount int         `json:"frame_count"`
	FrameRate  float64     `json:"frame_rate"`
	Tags       []string    `json:"tags"`
	Light      *AssetLight `json:"light,omitempty"`
}

// AssetLight describes the PointLight2D parameters for a light-emitting state.
// Only lit states (e.g. 'lit' variants of lamps/torches/campfires) have a row
// in asset_state_light and therefore a populated Light field. The client reads
// this and attaches a PointLight2D to the sprite at runtime.
type AssetLight struct {
	Color            string  `json:"color"`             // hex #RRGGBB
	Radius           int     `json:"radius"`            // world pixels
	Energy           float64 `json:"energy"`            // brightness multiplier
	OffsetX          int     `json:"offset_x"`          // light center offset from sprite origin
	OffsetY          int     `json:"offset_y"`
	FlickerAmplitude float64 `json:"flicker_amplitude"` // 0 = steady
	FlickerPeriodMs  int     `json:"flicker_period_ms"`
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

	// Fetch all assets with pack_id and fits_slot
	assetRows, err := app.DB.Query(r.Context(),
		`SELECT id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, fits_slot,
		        z_index, is_obstacle, is_passage,
		        footprint_left, footprint_right, footprint_top, footprint_bottom,
		        door_offset_x, door_offset_y, enterable, visible_when_inside,
		        stand_offset_x, stand_offset_y
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
			&a.AnchorX, &a.AnchorY, &a.Layer, &a.PackID, &a.FitsSlot,
			&a.ZIndex, &a.IsObstacle, &a.IsPassage,
			&a.FootprintLeft, &a.FootprintRight, &a.FootprintTop, &a.FootprintBottom,
			&a.DoorOffsetX, &a.DoorOffsetY, &a.Enterable, &a.VisibleWhenInside,
			&a.StandOffsetX, &a.StandOffsetY); err != nil {
			continue
		}
		a.States = []AssetState{}
		a.Slots = []AssetSlot{}
		if a.PackID != nil {
			if pack, ok := packs[*a.PackID]; ok {
				a.Pack = pack
			}
		}
		assetIndex[a.ID] = len(assets)
		assets = append(assets, a)
	}

	// Fetch all states, LEFT JOIN asset_state_light so lit states carry their
	// light params inline. Most rows come back with NULL light columns.
	// Tags aggregated via array_agg so each state ships with its tag set
	// in one trip — FILTER drops the NULL row array_agg emits for states
	// without any tags.
	stateRows, err := app.DB.Query(r.Context(),
		`SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h,
		        s.frame_count, s.frame_rate,
		        l.color, l.radius, l.energy, l.offset_x, l.offset_y,
		        l.flicker_amplitude, l.flicker_period_ms,
		        COALESCE(array_agg(t.tag) FILTER (WHERE t.tag IS NOT NULL), '{}')
		 FROM asset_state s
		 LEFT JOIN asset_state_light l ON l.state_id = s.id
		 LEFT JOIN asset_state_tag t ON t.state_id = s.id
		 GROUP BY s.id, l.state_id
		 ORDER BY s.asset_id, s.state`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer stateRows.Close()

	for stateRows.Next() {
		var assetID string
		var s AssetState
		var lightColor *string
		var lightRadius *int
		var lightEnergy *float64
		var lightOffsetX, lightOffsetY *int
		var lightFlickerAmp *float64
		var lightFlickerPeriod *int
		if err := stateRows.Scan(&assetID, &s.State, &s.Sheet,
			&s.SrcX, &s.SrcY, &s.SrcW, &s.SrcH, &s.FrameCount, &s.FrameRate,
			&lightColor, &lightRadius, &lightEnergy,
			&lightOffsetX, &lightOffsetY,
			&lightFlickerAmp, &lightFlickerPeriod,
			&s.Tags); err != nil {
			continue
		}
		if s.Tags == nil {
			s.Tags = []string{}
		}
		if lightColor != nil {
			s.Light = &AssetLight{
				Color:            *lightColor,
				Radius:           *lightRadius,
				Energy:           *lightEnergy,
				OffsetX:          *lightOffsetX,
				OffsetY:          *lightOffsetY,
				FlickerAmplitude: *lightFlickerAmp,
				FlickerPeriodMs:  *lightFlickerPeriod,
			}
		}
		if idx, ok := assetIndex[assetID]; ok {
			assets[idx].States = append(assets[idx].States, s)
		}
	}

	// Fetch all slots and attach to their parent asset
	slotRows, err := app.DB.Query(r.Context(),
		`SELECT asset_id, slot_name, offset_x, offset_y
		 FROM asset_slot
		 ORDER BY asset_id, slot_name`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer slotRows.Close()

	for slotRows.Next() {
		var assetID string
		var s AssetSlot
		if err := slotRows.Scan(&assetID, &s.SlotName, &s.OffsetX, &s.OffsetY); err != nil {
			continue
		}
		if idx, ok := assetIndex[assetID]; ok {
			assets[idx].Slots = append(assets[idx].Slots, s)
		}
	}

	jsonResponse(w, http.StatusOK, assets)
}

// handlePatchAssetFootprint updates the per-side footprint counts for one
// asset. Drag-resize from the editor sends {left, right, top, bottom};
// server validates non-negative, writes, and broadcasts asset_updated so
// every connected client refreshes its catalog and any visible borders.
func (app *App) handlePatchAssetFootprint(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin access required", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "Missing asset id", http.StatusBadRequest)
		return
	}

	var req struct {
		Left   *int `json:"left"`
		Right  *int `json:"right"`
		Top    *int `json:"top"`
		Bottom *int `json:"bottom"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	// All four required so the client always sends a fully-specified
	// footprint and nothing is implicit. Negative values rejected.
	if req.Left == nil || req.Right == nil || req.Top == nil || req.Bottom == nil {
		jsonError(w, "All four sides required", http.StatusBadRequest)
		return
	}
	if *req.Left < 0 || *req.Right < 0 || *req.Top < 0 || *req.Bottom < 0 {
		jsonError(w, "Footprint values must be non-negative", http.StatusBadRequest)
		return
	}

	tag, err := app.DB.Exec(r.Context(),
		`UPDATE asset SET footprint_left = $1, footprint_right = $2,
		                  footprint_top = $3, footprint_bottom = $4
		 WHERE id = $5`,
		*req.Left, *req.Right, *req.Top, *req.Bottom, id,
	)
	if err != nil {
		jsonError(w, "Failed to update footprint", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		jsonError(w, "Asset not found", http.StatusNotFound)
		return
	}

	// Broadcast so every connected client updates its local catalog and
	// re-renders any visible footprint border for this asset.
	app.Hub.Broadcast(WorldEvent{
		Type: "asset_footprint_updated",
		Data: map[string]any{
			"asset_id": id,
			"left":     *req.Left,
			"right":    *req.Right,
			"top":      *req.Top,
			"bottom":   *req.Bottom,
		},
	})

	w.WriteHeader(http.StatusNoContent)
}

// handlePatchAssetDoor updates the door tile offset for a structure asset.
// Drag-to-place from the editor sends {x, y} in tile units relative to the
// placement origin. Passing both as null clears the door (back to the
// "nearest adjacent walkable" fallback).
func (app *App) handlePatchAssetDoor(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin access required", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "Missing asset id", http.StatusBadRequest)
		return
	}

	var req struct {
		X *int `json:"x"`
		Y *int `json:"y"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	// Both-or-neither: either both coords are set (pick a tile) or both are
	// null (clear the door). Partial is a client bug.
	if (req.X == nil) != (req.Y == nil) {
		jsonError(w, "x and y must both be set or both null", http.StatusBadRequest)
		return
	}

	tag, err := app.DB.Exec(r.Context(),
		`UPDATE asset SET door_offset_x = $1, door_offset_y = $2 WHERE id = $3`,
		req.X, req.Y, id,
	)
	if err != nil {
		jsonError(w, "Failed to update door", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		jsonError(w, "Asset not found", http.StatusNotFound)
		return
	}

	app.Hub.Broadcast(WorldEvent{
		Type: "asset_door_updated",
		Data: map[string]any{
			"asset_id": id,
			"x":        req.X,
			"y":        req.Y,
		},
	})

	w.WriteHeader(http.StatusNoContent)
}

// handlePatchAssetStand updates the stand-offset for visible_when_inside
// assets — the tile offset where an NPC renders when they're "inside" a
// see-through structure like a market stall. Distinct from door offset:
// door is a walkable pathfind target; stand is a pure-render position
// (usually inside the unwalkable footprint). Pass null / null to clear
// (NPC falls back to the door tile on arrival, current behavior).
func (app *App) handlePatchAssetStand(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin access required", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "Missing asset id", http.StatusBadRequest)
		return
	}

	var req struct {
		X *int `json:"x"`
		Y *int `json:"y"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if (req.X == nil) != (req.Y == nil) {
		jsonError(w, "x and y must both be set or both null", http.StatusBadRequest)
		return
	}

	tag, err := app.DB.Exec(r.Context(),
		`UPDATE asset SET stand_offset_x = $1, stand_offset_y = $2 WHERE id = $3`,
		req.X, req.Y, id,
	)
	if err != nil {
		jsonError(w, "Failed to update stand offset", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		jsonError(w, "Asset not found", http.StatusNotFound)
		return
	}

	app.Hub.Broadcast(WorldEvent{
		Type: "asset_stand_updated",
		Data: map[string]any{
			"asset_id": id,
			"x":        req.X,
			"y":        req.Y,
		},
	})

	w.WriteHeader(http.StatusNoContent)
}

// handlePatchAssetEnterable toggles the per-asset enterable flag. Flipping
// this on for, say, the Tent asset makes every placed tent a valid home/work
// target and shows the door marker in the editor.
func (app *App) handlePatchAssetEnterable(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin access required", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "Missing asset id", http.StatusBadRequest)
		return
	}

	var req struct {
		Enterable *bool `json:"enterable"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Enterable == nil {
		jsonError(w, "enterable is required", http.StatusBadRequest)
		return
	}

	tag, err := app.DB.Exec(r.Context(),
		`UPDATE asset SET enterable = $1 WHERE id = $2`,
		*req.Enterable, id,
	)
	if err != nil {
		jsonError(w, "Failed to update enterable", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		jsonError(w, "Asset not found", http.StatusNotFound)
		return
	}

	app.Hub.Broadcast(WorldEvent{
		Type: "asset_enterable_updated",
		Data: map[string]any{
			"asset_id":  id,
			"enterable": *req.Enterable,
		},
	})

	w.WriteHeader(http.StatusNoContent)
}

// handlePatchAssetVisibleWhenInside toggles whether the villager stays
// visible when inside a structure of this asset. See-through buildings
// (market stall) set this true so the NPC renders at the door tile
// rather than being hidden.
func (app *App) handlePatchAssetVisibleWhenInside(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin access required", http.StatusForbidden)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "Missing asset id", http.StatusBadRequest)
		return
	}

	var req struct {
		VisibleWhenInside *bool `json:"visible_when_inside"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.VisibleWhenInside == nil {
		jsonError(w, "visible_when_inside is required", http.StatusBadRequest)
		return
	}

	tag, err := app.DB.Exec(r.Context(),
		`UPDATE asset SET visible_when_inside = $1 WHERE id = $2`,
		*req.VisibleWhenInside, id,
	)
	if err != nil {
		jsonError(w, "Failed to update visibility", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		jsonError(w, "Asset not found", http.StatusNotFound)
		return
	}

	app.Hub.Broadcast(WorldEvent{
		Type: "asset_visible_when_inside_updated",
		Data: map[string]any{
			"asset_id":            id,
			"visible_when_inside": *req.VisibleWhenInside,
		},
	})

	w.WriteHeader(http.StatusNoContent)
}

// allowedStateTags enumerates the tag vocabulary the admin UI can set.
// New tags require a code change so the vocabulary stays coherent —
// free-form typing would fragment the set (typos, "lamplighter_target" vs
// "lamplighter-target"). Mirror of the constants in world_phase.go /
// world_rotation.go / npc_behaviors.go.
var allowedStateTags = map[string]bool{
	"rotatable":          true,
	"day-active":         true,
	"night-active":       true,
	"lamplighter-target": true,
	"laundry":            true,
	"notice-board":       true,
	"occupied":           true,
	"unoccupied":         true,
	"tavern":             true,
}

// handleListStateTags returns the tag allowlist so the admin UI can
// populate its picker from server truth rather than a hard-coded client
// list. Any authenticated user can read it.
func (app *App) handleListStateTags(w http.ResponseWriter, r *http.Request) {
	tags := make([]string, 0, len(allowedStateTags))
	for tag := range allowedStateTags {
		tags = append(tags, tag)
	}
	// Stable alphabetical order so the UI dropdown is predictable.
	for i := 1; i < len(tags); i++ {
		for j := i; j > 0 && tags[j-1] > tags[j]; j-- {
			tags[j-1], tags[j] = tags[j], tags[j-1]
		}
	}
	jsonResponse(w, http.StatusOK, tags)
}

// handleAddStateTag adds one tag to an asset state. Idempotent via
// ON CONFLICT DO NOTHING on the composite PK. Admin only.
// Broadcasts asset_state_tags_updated so every client refreshes its
// copy of the state's tag set.
func (app *App) handleAddStateTag(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin access required", http.StatusForbidden)
		return
	}

	assetID := r.PathValue("id")
	state := r.PathValue("state")
	if assetID == "" || state == "" {
		jsonError(w, "Missing asset id or state", http.StatusBadRequest)
		return
	}

	var req struct {
		Tag string `json:"tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !allowedStateTags[req.Tag] {
		jsonError(w, "Unknown tag", http.StatusBadRequest)
		return
	}

	// Resolve state_id here rather than requiring the client to know the
	// integer PK — asset id + state name is the stable pair the editor
	// already holds.
	var stateID int
	err := app.DB.QueryRow(r.Context(),
		`SELECT id FROM asset_state WHERE asset_id = $1 AND state = $2`,
		assetID, state,
	).Scan(&stateID)
	if err != nil {
		jsonError(w, "State not found", http.StatusNotFound)
		return
	}

	if _, err := app.DB.Exec(r.Context(),
		`INSERT INTO asset_state_tag (state_id, tag) VALUES ($1, $2)
		 ON CONFLICT (state_id, tag) DO NOTHING`,
		stateID, req.Tag,
	); err != nil {
		jsonError(w, "Failed to add tag", http.StatusInternalServerError)
		return
	}

	app.broadcastStateTags(r.Context(), assetID, state, stateID)
	w.WriteHeader(http.StatusNoContent)
}

// handleRemoveStateTag removes one tag from an asset state. Admin only.
// No-op if the pair wasn't present — the DELETE matches zero rows but
// the client's mental model still converges. Broadcasts regardless.
func (app *App) handleRemoveStateTag(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin access required", http.StatusForbidden)
		return
	}

	assetID := r.PathValue("id")
	state := r.PathValue("state")
	tag := r.PathValue("tag")
	if assetID == "" || state == "" || tag == "" {
		jsonError(w, "Missing asset id, state, or tag", http.StatusBadRequest)
		return
	}

	var stateID int
	err := app.DB.QueryRow(r.Context(),
		`SELECT id FROM asset_state WHERE asset_id = $1 AND state = $2`,
		assetID, state,
	).Scan(&stateID)
	if err != nil {
		jsonError(w, "State not found", http.StatusNotFound)
		return
	}

	if _, err := app.DB.Exec(r.Context(),
		`DELETE FROM asset_state_tag WHERE state_id = $1 AND tag = $2`,
		stateID, tag,
	); err != nil {
		jsonError(w, "Failed to remove tag", http.StatusInternalServerError)
		return
	}

	app.broadcastStateTags(r.Context(), assetID, state, stateID)
	w.WriteHeader(http.StatusNoContent)
}

// broadcastStateTags reads the current tag set for the given state and
// fans it out to all clients. Used by both add and remove so the payload
// is always the full authoritative set, not a diff.
func (app *App) broadcastStateTags(ctx context.Context, assetID, state string, stateID int) {
	rows, err := app.DB.Query(ctx,
		`SELECT tag FROM asset_state_tag WHERE state_id = $1 ORDER BY tag`,
		stateID,
	)
	if err != nil {
		return
	}
	defer rows.Close()
	tags := []string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err == nil {
			tags = append(tags, tag)
		}
	}
	app.Hub.Broadcast(WorldEvent{
		Type: "asset_state_tags_updated",
		Data: map[string]any{
			"asset_id": assetID,
			"state":    state,
			"tags":     tags,
		},
	})
}
