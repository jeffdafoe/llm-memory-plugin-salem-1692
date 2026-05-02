package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"hash/fnv"
	"log"
	"math"
	"math/rand"
	"net/http"
	"time"
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
	ID           string   `json:"id"`
	AssetID      string   `json:"asset_id"`
	CurrentState string   `json:"current_state"`
	X            float64  `json:"x"`
	Y            float64  `json:"y"`
	PlacedBy     *string  `json:"placed_by"`
	Owner        *string  `json:"owner"`
	DisplayName  *string  `json:"display_name"`
	AttachedTo   *string  `json:"attached_to"`
	// EntryPolicy (ZBBS-101) — who can enter this placed structure.
	// 'none' = no entry, 'owner' = only actors with this structure as
	// home_structure_id or work_structure_id, 'anyone' = public access.
	// Per-instance: the same house asset can be a private home at one
	// placement and a public tavern at another.
	EntryPolicy string `json:"entry_policy"`
	// Per-instance tags (ZBBS-069) — role tags applied to THIS placed
	// object. Always a (possibly empty) array, never null, so client code
	// can iterate without a nil check.
	Tags []string `json:"tags"`
	// Per-instance loiter offset (ZBBS-075) — tile-unit offset from this
	// placement's origin where visiting NPCs stand. NULL means "no
	// override; fall back to the asset's door_offset." Editor renders a
	// draggable pin on the placement when set.
	LoiterOffsetX *int `json:"loiter_offset_x"`
	LoiterOffsetY *int `json:"loiter_offset_y"`
	// Effective loiter offset — the position the editor renders the green
	// dot at AND the agent walk-resolver targets. Computed via
	// effectiveLoiterTile from the per-instance override + asset door +
	// footprint_bottom. Always populated. Single source of truth: both
	// editor and engine consume this rather than reimplementing the
	// fallback formula.
	EffectiveLoiterX int `json:"effective_loiter_offset_x"`
	EffectiveLoiterY int `json:"effective_loiter_offset_y"`
}

// effectiveLoiterTile returns the canonical loiter offset (tile units)
// for a placement. The editor renders the green loiter marker at this
// position; the agent walk resolver targets it for visitor moves.
//
// Resolution order:
//
//  1. Per-instance loiter_offset, when set.
//  2. Asset door_offset + 1 tile south, when door is set. This is the
//     "natural" loiter spot — visitors stand just below the door tile,
//     out of the way of arriving/leaving traffic.
//  3. Anchor-relative (0, footprint_bottom + 2) — the absolute fallback
//     when an asset has neither door nor configured loiter. Two tiles
//     below the bottom of the visible footprint.
//
// Single source of truth: keep this function the only implementation
// of the formula. Both pickWalkTarget (engine-internal) and
// handleListVillageObjects (API to editor) call it.
func effectiveLoiterTile(loiterX, loiterY, doorX, doorY sql.NullInt32, footprintBottom int) (int, int) {
	if loiterX.Valid && loiterY.Valid {
		return int(loiterX.Int32), int(loiterY.Int32)
	}
	if doorX.Valid && doorY.Valid {
		return int(doorX.Int32), int(doorY.Int32) + 1
	}
	return 0, footprintBottom + 2
}

// visitorSlotOffsets is the king's-move neighborhood of a loiter pin
// tile — the 8 tiles where visiting NPCs can stand. The pin tile itself
// is intentionally NOT a slot: the pin marks the gathering CENTER, not
// where any single NPC stands. Order is canonical so any reordering
// happens via the per-NPC hash shuffle, not here.
var visitorSlotOffsets = [8][2]int{
	{-1, -1}, {0, -1}, {1, -1},
	{-1, 0}, {1, 0},
	{-1, 1}, {0, 1}, {1, 1},
}

// pickVisitorSlot picks a tile center near a loiter pin for one visiting
// NPC. The 8 surrounding tiles are walked in an order shuffled per-NPC
// (deterministic from npcID), and the first slot that's free is taken.
//
// Free = not covered by any is_obstacle placement footprint, and not
// currently occupied by another outside actor. When all 8 slots are
// blocked, falls back to the loiter tile center itself; the admin should
// move the pin further from obstructions in that case.
//
// Returns world-pixel coordinates of the chosen tile's CENTER. Tile-aligned
// targets sidestep the arrival snap that defeated the old jitter approach.
func (app *App) pickVisitorSlot(ctx context.Context, npcID string, anchorX, anchorY float64, loiterTileX, loiterTileY int) (float64, float64) {
	const tileSize = 32.0

	anchorTileX := int(math.Floor(anchorX / tileSize))
	anchorTileY := int(math.Floor(anchorY / tileSize))
	centerTileX := anchorTileX + loiterTileX
	centerTileY := anchorTileY + loiterTileY

	// Hash the NPC id to seed a shuffle of the 8 slot indices. fnv32a is
	// fast and good enough — this is purely for spreading visitors across
	// slots, not security.
	h := fnv.New32a()
	h.Write([]byte(npcID))
	rng := rand.New(rand.NewSource(int64(h.Sum32())))
	indices := []int{0, 1, 2, 3, 4, 5, 6, 7}
	rng.Shuffle(len(indices), func(i, j int) {
		indices[i], indices[j] = indices[j], indices[i]
	})

	blocked := app.loadBlockedSlotTiles(ctx, npcID, centerTileX, centerTileY)

	for _, idx := range indices {
		tx := centerTileX + visitorSlotOffsets[idx][0]
		ty := centerTileY + visitorSlotOffsets[idx][1]
		if !blocked[tileKey(tx, ty)] {
			return tileCenterPx(tx), tileCenterPx(ty)
		}
	}

	// All 8 blocked. Falling back to the loiter tile itself isn't ideal
	// (visitors stack on the pin), but it gets the NPC somewhere reachable.
	log.Printf("pickVisitorSlot: all 8 slots blocked at tile (%d,%d) for npc %s; using loiter tile", centerTileX, centerTileY, npcID)
	return tileCenterPx(centerTileX), tileCenterPx(centerTileY)
}

func tileCenterPx(tile int) float64 {
	return float64(tile)*32.0 + 16.0
}

// tileKey packs (x, y) tile coords into a single int64 for map lookup.
// Both x and y can be negative; we cast y through uint32 to keep the
// low 32 bits intact.
func tileKey(x, y int) int64 {
	return int64(x)<<32 | int64(uint32(int32(y)))
}

// loadBlockedSlotTiles returns the set of tile keys (covering at least
// the 3x3 ring centered at cx,cy) that are blocked. A tile is blocked if
// (a) it falls inside some is_obstacle placement's footprint, OR (b) it
// currently holds another outside actor. One SQL round-trip per source
// keeps this cheap even though pickVisitorSlot is on the per-tick path.
func (app *App) loadBlockedSlotTiles(ctx context.Context, npcID string, cx, cy int) map[int64]bool {
	blocked := make(map[int64]bool)

	// Obstacle footprints. A placement's footprint covers tiles in a
	// rectangle (anchorTile - left .. anchorTile + right, anchorTile - top
	// .. anchorTile + bottom). We pull every obstacle whose anchor tile
	// could possibly cover any tile in our 3x3 ring, then expand each
	// footprint into the blocked set.
	rows, err := app.DB.Query(ctx,
		`SELECT FLOOR(o.x / 32.0)::int, FLOOR(o.y / 32.0)::int,
		        a.footprint_left, a.footprint_right, a.footprint_top, a.footprint_bottom
		 FROM village_object o JOIN asset a ON a.id = o.asset_id
		 WHERE a.is_obstacle = true
		   AND ABS(FLOOR(o.x / 32.0)::int - $1) <= a.footprint_left + a.footprint_right + 1
		   AND ABS(FLOOR(o.y / 32.0)::int - $2) <= a.footprint_top + a.footprint_bottom + 1`,
		cx, cy)
	if err == nil {
		for rows.Next() {
			var px, py, fl, fr, ft, fb int
			if err := rows.Scan(&px, &py, &fl, &fr, &ft, &fb); err != nil {
				continue
			}
			for dy := -ft; dy <= fb; dy++ {
				for dx := -fl; dx <= fr; dx++ {
					blocked[tileKey(px+dx, py+dy)] = true
				}
			}
		}
		rows.Close()
	} else {
		log.Printf("loadBlockedSlotTiles obstacles: %v", err)
	}

	// Other outside actors near the ring. inside=true actors don't count
	// (they're not on the world map for collision purposes).
	rows2, err := app.DB.Query(ctx,
		`SELECT FLOOR(current_x / 32.0)::int, FLOOR(current_y / 32.0)::int
		 FROM actor
		 WHERE id::text != $1
		   AND inside = false
		   AND ABS(FLOOR(current_x / 32.0)::int - $2) <= 1
		   AND ABS(FLOOR(current_y / 32.0)::int - $3) <= 1`,
		npcID, cx, cy)
	if err == nil {
		for rows2.Next() {
			var tx, ty int
			if err := rows2.Scan(&tx, &ty); err == nil {
				blocked[tileKey(tx, ty)] = true
			}
		}
		rows2.Close()
	} else {
		log.Printf("loadBlockedSlotTiles actors: %v", err)
	}

	return blocked
}

// handleListVillageObjects returns all placed objects.
// LEFT JOIN LATERAL keeps the one-row-per-object shape while folding the
// object's tag set into a PG array in a single round-trip.
func (app *App) handleListVillageObjects(w http.ResponseWriter, r *http.Request) {
	rows, err := app.DB.Query(r.Context(),
		`SELECT o.id, o.asset_id, o.current_state, o.x, o.y,
		        o.placed_by, o.owner, o.display_name, o.attached_to,
		        o.entry_policy,
		        COALESCE(t.tags, ARRAY[]::varchar[]),
		        o.loiter_offset_x, o.loiter_offset_y,
		        a.door_offset_x, a.door_offset_y, a.footprint_bottom
		 FROM village_object o
		 JOIN asset a ON a.id = o.asset_id
		 LEFT JOIN LATERAL (
		     SELECT array_agg(tag ORDER BY tag) AS tags
		     FROM village_object_tag
		     WHERE object_id = o.id
		 ) t ON TRUE
		 ORDER BY o.created_at`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	objects := []villageObject{}
	for rows.Next() {
		var obj villageObject
		var doorX, doorY sql.NullInt32
		var footprintBottom int
		var rawLoiterX, rawLoiterY sql.NullInt32
		if err := rows.Scan(&obj.ID, &obj.AssetID, &obj.CurrentState,
			&obj.X, &obj.Y, &obj.PlacedBy, &obj.Owner, &obj.DisplayName, &obj.AttachedTo,
			&obj.EntryPolicy,
			&obj.Tags,
			&rawLoiterX, &rawLoiterY,
			&doorX, &doorY, &footprintBottom); err != nil {
			continue
		}
		if rawLoiterX.Valid {
			v := int(rawLoiterX.Int32)
			obj.LoiterOffsetX = &v
		}
		if rawLoiterY.Valid {
			v := int(rawLoiterY.Int32)
			obj.LoiterOffsetY = &v
		}
		obj.EffectiveLoiterX, obj.EffectiveLoiterY = effectiveLoiterTile(rawLoiterX, rawLoiterY, doorX, doorY, footprintBottom)
		objects = append(objects, obj)
	}

	jsonResponse(w, http.StatusOK, objects)
}

// handleCreateVillageObject places a new object on the map.
func (app *App) handleCreateVillageObject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AssetID    string  `json:"asset_id"`
		X          float64 `json:"x"`
		Y          float64 `json:"y"`
		AttachedTo *string `json:"attached_to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.AssetID == "" {
		jsonError(w, "asset_id is required", http.StatusBadRequest)
		return
	}

	// Look up default state and door coords. Door presence is what gates
	// the default entry policy: assets with a configured doorway default
	// to 'anyone' (the placement is enterable on creation), assets without
	// to 'none' (decorative — the editor can override per instance).
	var defaultState string
	var doorX, doorY sql.NullInt32
	err := app.DB.QueryRow(r.Context(),
		`SELECT default_state, door_offset_x, door_offset_y FROM asset WHERE id = $1`, req.AssetID,
	).Scan(&defaultState, &doorX, &doorY)
	if err != nil {
		jsonError(w, "Unknown asset_id", http.StatusBadRequest)
		return
	}

	entryPolicy := "none"
	if doorX.Valid && doorY.Valid {
		entryPolicy = "anyone"
	}

	// Get the authenticated user who's placing the object
	user := getUserFromContext(r.Context())
	var placedBy *string
	if user != nil && user.Username != "" {
		placedBy = &user.Username
	}

	id := newUUIDv7()
	_, err = app.DB.Exec(r.Context(),
		`INSERT INTO village_object (id, asset_id, current_state, x, y, placed_by, attached_to, entry_policy)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, req.AssetID, defaultState, req.X, req.Y, placedBy, req.AttachedTo, entryPolicy,
	)
	if err != nil {
		jsonError(w, "Failed to create object", http.StatusInternalServerError)
		return
	}

	obj := villageObject{
		ID:           id,
		AssetID:      req.AssetID,
		CurrentState: defaultState,
		X:            req.X,
		Y:            req.Y,
		PlacedBy:     placedBy,
		AttachedTo:   req.AttachedTo,
		EntryPolicy:  entryPolicy,
	}
	jsonResponse(w, http.StatusCreated, obj)
	app.Hub.Broadcast(WorldEvent{Type: "object_created", Data: obj})
}

// handleDeleteVillageObject removes an object from the map.
//
// Eviction: any actor flagged as inside this structure must have
// inside / inside_structure_id / current_huddle_id cleared in the same
// transaction as the DELETE. The actor.inside_structure_id FK is
// ON DELETE SET NULL, but that alone leaves the inside boolean stale
// (see the Grace Edwards orphan: inside=true with NULL structure ref
// after one of two co-located homes was deleted, 2026-04). Doing the
// eviction explicitly here keeps the invariant intact regardless of
// which delete path runs.
func (app *App) handleDeleteVillageObject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "Missing object ID", http.StatusBadRequest)
		return
	}

	tx, err := app.DB.Begin(r.Context())
	if err != nil {
		jsonError(w, "Failed to delete object", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context()) // safe after commit (no-op)

	// Evict in a single UPDATE ... RETURNING so the broadcast list
	// matches exactly the rows we actually changed — no race window
	// between a separate SELECT and the UPDATE.
	rows, err := tx.Query(r.Context(),
		`UPDATE actor
		    SET inside = false,
		        inside_structure_id = NULL,
		        current_huddle_id = NULL
		  WHERE inside_structure_id = $1
		  RETURNING id`, id,
	)
	if err != nil {
		jsonError(w, "Failed to delete object", http.StatusInternalServerError)
		return
	}
	var evicted []string
	for rows.Next() {
		var aid string
		if err := rows.Scan(&aid); err != nil {
			rows.Close()
			jsonError(w, "Failed to delete object", http.StatusInternalServerError)
			return
		}
		evicted = append(evicted, aid)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		jsonError(w, "Failed to delete object", http.StatusInternalServerError)
		return
	}
	rows.Close()

	result, err := tx.Exec(r.Context(),
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

	if err := tx.Commit(r.Context()); err != nil {
		jsonError(w, "Failed to delete object", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{Type: "object_deleted", Data: map[string]string{"id": id}})
	for _, aid := range evicted {
		app.Hub.Broadcast(WorldEvent{
			Type: "npc_inside_changed",
			Data: map[string]any{
				"id":                  aid,
				"inside":              false,
				"inside_structure_id": nil,
			},
		})
	}
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
	app.Hub.Broadcast(WorldEvent{Type: "object_owner_changed", Data: map[string]interface{}{"id": id, "owner": req.Owner}})
}

// handleSetVillageObjectEntryPolicy changes who can enter a placed
// structure (ZBBS-101). Three values, validated against the same CHECK
// constraint the schema enforces. Editor-side validation refuses the
// 'owner' policy when no actor has this structure as their home or work
// — the structure would otherwise be silently inaccessible — and the
// server enforces the same rule so a stale editor can't slip past.
//
// Broadcasts object_entry_policy_changed so other connected editors
// re-render any policy badges they're showing.
func (app *App) handleSetVillageObjectEntryPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "Missing object ID", http.StatusBadRequest)
		return
	}

	var req struct {
		EntryPolicy string `json:"entry_policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.EntryPolicy != "none" && req.EntryPolicy != "owner" && req.EntryPolicy != "anyone" {
		jsonError(w, "entry_policy must be 'none', 'owner', or 'anyone'", http.StatusBadRequest)
		return
	}

	// Setting 'owner' on a structure with no associated actor would lock
	// the structure with no key-holder. Refuse rather than silently produce
	// an unenterable building.
	if req.EntryPolicy == "owner" {
		var associatedCount int
		if err := app.DB.QueryRow(r.Context(),
			`SELECT COUNT(*) FROM actor
			  WHERE home_structure_id::text = $1 OR work_structure_id::text = $1`,
			id,
		).Scan(&associatedCount); err != nil {
			jsonError(w, "Failed to validate entry policy", http.StatusInternalServerError)
			return
		}
		if associatedCount == 0 {
			jsonError(w, "Cannot set entry_policy='owner' on a structure with no associated actor — assign an NPC's home or work to this structure first", http.StatusBadRequest)
			return
		}
	}

	result, err := app.DB.Exec(r.Context(),
		`UPDATE village_object SET entry_policy = $1 WHERE id = $2`,
		req.EntryPolicy, id,
	)
	if err != nil {
		jsonError(w, "Failed to update entry policy", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		jsonError(w, "Object not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{
		Type: "object_entry_policy_changed",
		Data: map[string]any{"id": id, "entry_policy": req.EntryPolicy},
	})
}

// handleSetVillageObjectDisplayName assigns or changes the display name of an object.
func (app *App) handleSetVillageObjectDisplayName(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "Missing object ID", http.StatusBadRequest)
		return
	}

	var req struct {
		DisplayName *string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	result, err := app.DB.Exec(r.Context(),
		`UPDATE village_object SET display_name = $1 WHERE id = $2`,
		req.DisplayName, id,
	)
	if err != nil {
		jsonError(w, "Failed to update display name", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		jsonError(w, "Object not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{Type: "object_display_name_changed", Data: map[string]interface{}{"id": id, "display_name": req.DisplayName}})
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
	app.Hub.Broadcast(WorldEvent{Type: "object_state_changed", Data: map[string]string{"id": id, "state": req.State}})
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
	app.Hub.Broadcast(WorldEvent{Type: "object_moved", Data: map[string]interface{}{"id": id, "x": req.X, "y": req.Y}})
}

// handleSetVillageObjectLoiterOffset (ZBBS-075) updates the per-instance
// loiter offset where visiting NPCs stand outside this placement. Both
// values are tile-unit integers; null clears the override and reverts to
// asset.door_offset behavior.
//
// Body: { loiter_offset_x: int|null, loiter_offset_y: int|null }
//
// "Both or neither": if one is set the other must be too. The agent
// resolver always reads them together so a mixed state would be ambiguous.
//
// Side effect: NPCs currently standing at the OLD loiter position get
// re-walked to the NEW one. Without this, an admin moving a placement's
// loiter pin while NPCs are visiting would leave them stuck in the
// original (now likely visually wrong) spot — e.g. inside the well sprite
// after the loiter pin moves to the adjacent tile. Owners of this
// placement (home or work) are skipped — their position is governed by
// the scheduler/inside-structure flow, not the loiter pin.
func (app *App) handleSetVillageObjectLoiterOffset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "Missing object ID", http.StatusBadRequest)
		return
	}

	var req struct {
		LoiterOffsetX *int `json:"loiter_offset_x"`
		LoiterOffsetY *int `json:"loiter_offset_y"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if (req.LoiterOffsetX == nil) != (req.LoiterOffsetY == nil) {
		jsonError(w, "loiter_offset_x and loiter_offset_y must both be set or both null", http.StatusBadRequest)
		return
	}

	// Read OLD offset + asset door + footprint_bottom BEFORE the UPDATE so
	// we can compute the position visitors would have walked to. Asset
	// fields feed effectiveLoiterTile for both the old (pre-update) and new
	// (post-update) targets.
	var oldLoiterX, oldLoiterY sql.NullInt32
	var doorX, doorY sql.NullInt32
	var anchorX, anchorY float64
	var footprintBottom int
	if err := app.DB.QueryRow(r.Context(),
		`SELECT o.loiter_offset_x, o.loiter_offset_y,
		        a.door_offset_x, a.door_offset_y, a.footprint_bottom,
		        o.x, o.y
		 FROM village_object o JOIN asset a ON a.id = o.asset_id
		 WHERE o.id = $1`,
		id).Scan(&oldLoiterX, &oldLoiterY, &doorX, &doorY, &footprintBottom, &anchorX, &anchorY); err != nil {
		if err == sql.ErrNoRows {
			jsonError(w, "Object not found", http.StatusNotFound)
			return
		}
		jsonError(w, "Failed to read placement", http.StatusInternalServerError)
		return
	}

	result, err := app.DB.Exec(r.Context(),
		`UPDATE village_object SET loiter_offset_x = $1, loiter_offset_y = $2 WHERE id = $3`,
		req.LoiterOffsetX, req.LoiterOffsetY, id,
	)
	if err != nil {
		jsonError(w, "Failed to update loiter offset", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		jsonError(w, "Object not found", http.StatusNotFound)
		return
	}

	// Compute new effective values for the broadcast — clients (editor)
	// render the green dot at the effective position regardless of whether
	// loiter_offset itself is set, so the WS payload carries both.
	newRawX, newRawY := intPtrToNullInt32(req.LoiterOffsetX), intPtrToNullInt32(req.LoiterOffsetY)
	effX, effY := effectiveLoiterTile(newRawX, newRawY, doorX, doorY, footprintBottom)

	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{
		Type: "object_loiter_offset_changed",
		Data: map[string]interface{}{
			"id":                        id,
			"loiter_offset_x":           req.LoiterOffsetX,
			"loiter_offset_y":           req.LoiterOffsetY,
			"effective_loiter_offset_x": effX,
			"effective_loiter_offset_y": effY,
		},
	})

	// Fire-and-forget the visitor relocate; failures are logged but don't
	// fail the PATCH response (the offset itself is already saved).
	go app.relocateVisitorsAfterLoiterChange(
		context.Background(), id, anchorX, anchorY,
		oldLoiterX, oldLoiterY, newRawX, newRawY, doorX, doorY, footprintBottom,
	)
}

// intPtrToNullInt32 converts a *int (the JSON-decoded shape used in PATCH
// bodies) into a sql.NullInt32 (the shape effectiveLoiterTile expects).
// nil pointer becomes Valid=false; otherwise Valid=true with the int value.
func intPtrToNullInt32(p *int) sql.NullInt32 {
	if p == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: int32(*p), Valid: true}
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

	// Pre-fetch default states + door presence for all referenced assets.
	// Door presence drives the seeded entry_policy: with a door, the
	// placement is enterable on creation ('anyone'); without, decorative
	// ('none'). Editor can override per instance afterwards.
	type assetMeta struct {
		state    string
		hasDoor  bool
	}
	assetsMeta := map[string]assetMeta{}
	stateRows, err := app.DB.Query(r.Context(),
		`SELECT id, default_state, door_offset_x IS NOT NULL AND door_offset_y IS NOT NULL FROM asset`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer stateRows.Close()
	for stateRows.Next() {
		var id, state string
		var hasDoor bool
		if err := stateRows.Scan(&id, &state, &hasDoor); err != nil {
			continue
		}
		assetsMeta[id] = assetMeta{state: state, hasDoor: hasDoor}
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
		meta, ok := assetsMeta[obj.AssetID]
		if !ok {
			continue // skip unknown assets
		}
		entryPolicy := "none"
		if meta.hasDoor {
			entryPolicy = "anyone"
		}
		id := newUUIDv7()
		_, err := tx.Exec(r.Context(),
			`INSERT INTO village_object (id, asset_id, current_state, x, y, entry_policy)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			id, obj.AssetID, meta.state, obj.X, obj.Y, entryPolicy,
		)
		if err != nil {
			jsonError(w, "Failed to create objects", http.StatusInternalServerError)
			return
		}
		created = append(created, villageObject{
			ID:           id,
			AssetID:      obj.AssetID,
			CurrentState: meta.state,
			X:            obj.X,
			Y:            obj.Y,
			EntryPolicy:  entryPolicy,
		})
	}

	if err := tx.Commit(r.Context()); err != nil {
		jsonError(w, "Failed to commit", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusCreated, created)
}

// relocateVisitorsAfterLoiterChange walks any NPC currently standing
// near the OLD loiter position to a fresh slot around the NEW one when
// an admin moves a placement's loiter pin. Owners (this placement is
// their home or work) are skipped — their position belongs to the
// scheduler/inside-structure flow, not the loiter pin.
//
// "Near OLD" is defined as within 2 tiles of the old loiter pin's tile
// center — the slot ring sits ~1 tile out (cardinal) to ~1.41 out
// (diagonal), and 2 tiles gives margin for arrival fuzz.
//
// Each relocate is dispatched via startReturnWalk with enterOnArrival=false
// (visitors stay outside) and stamps agent_override_until so the scheduler
// doesn't yank the NPC back mid-walk. The new target is picked per-NPC
// via pickVisitorSlot, so a cluster of visitors at the moved spot
// distributes across the new slot ring.
func (app *App) relocateVisitorsAfterLoiterChange(
	ctx context.Context, objectID string, anchorX, anchorY float64,
	oldLoiterX, oldLoiterY sql.NullInt32,
	newLoiterX, newLoiterY sql.NullInt32,
	doorX, doorY sql.NullInt32,
	footprintBottom int,
) {
	const tileSize = 32.0
	const nearRadius = 2.0 * tileSize
	const nearRadiusSq = nearRadius * nearRadius

	anchorTileX := int(math.Floor(anchorX / tileSize))
	anchorTileY := int(math.Floor(anchorY / tileSize))

	oldLx, oldLy := effectiveLoiterTile(oldLoiterX, oldLoiterY, doorX, doorY, footprintBottom)
	oldCenterPx := tileCenterPx(anchorTileX + oldLx)
	oldCenterPy := tileCenterPx(anchorTileY + oldLy)

	newLx, newLy := effectiveLoiterTile(newLoiterX, newLoiterY, doorX, doorY, footprintBottom)

	// Find candidates: NPCs inside the near-radius of OLD that don't own
	// this placement. Exclude NPCs already walking — startReturnWalk on a
	// moving NPC stomps the in-flight walk; we'd rather let it land first
	// and (if relevant) re-relocate on a subsequent admin move.
	rows, err := app.DB.Query(ctx,
		`SELECT id, current_x, current_y FROM actor
		 WHERE login_username IS NULL
		   AND (current_x - $1) * (current_x - $1) + (current_y - $2) * (current_y - $2) < $3
		   AND (home_structure_id IS NULL OR home_structure_id::text != $4)
		   AND (work_structure_id IS NULL OR work_structure_id::text != $4)`,
		oldCenterPx, oldCenterPy, nearRadiusSq, objectID)
	if err != nil {
		log.Printf("relocateVisitors: query: %v", err)
		return
	}
	defer rows.Close()

	type candidate struct {
		ID         string
		CurX, CurY float64
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ID, &c.CurX, &c.CurY); err == nil {
			candidates = append(candidates, c)
		}
	}

	for _, c := range candidates {
		targetX, targetY := app.pickVisitorSlot(ctx, c.ID, anchorX, anchorY, newLx, newLy)
		npc := &behaviorNPC{ID: c.ID, CurX: c.CurX, CurY: c.CurY}
		app.interpolateCurrentPos(npc)
		if err := app.startReturnWalk(ctx, npc, targetX, targetY, objectID, "loiter-relocate", false); err != nil {
			log.Printf("relocateVisitors: startReturnWalk %s: %v", c.ID, err)
			continue
		}
		overrideUntil := time.Now().Add(30 * time.Minute)
		if _, err := app.DB.Exec(ctx,
			`UPDATE actor SET agent_override_until = $2, last_shift_tick_at = $2 WHERE id = $1`,
			c.ID, overrideUntil,
		); err != nil {
			log.Printf("relocateVisitors: stamp override %s: %v", c.ID, err)
		}
	}
}
