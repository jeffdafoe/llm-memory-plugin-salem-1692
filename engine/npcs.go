package main

// NPC catalog + instance endpoints.
//
// Milestone 1a exposes GET /api/village/npcs — returns every placed NPC with
// its sprite metadata and animation rows inlined, so the client can render
// them from a single round-trip after auth. Future milestones layer movement
// (waypoint broadcast via WS), editor placement (POST /npcs), and the LLM
// agent linkage on top.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// NPCSprite describes a character sprite sheet. One row = one character
// variant (Woman A v00, Old Man B v02, etc). Sheets are served by nginx at
// npc_sprite.sheet paths like /tilesets/mana-seed/npc/woman_A_v00.png.
type NPCSprite struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Sheet       string             `json:"sheet"`
	FrameWidth  int                `json:"frame_width"`
	FrameHeight int                `json:"frame_height"`
	PackID      *string            `json:"pack_id"`
	Pack        *TilesetPack       `json:"pack,omitempty"`
	Animations  []NPCSpriteAnim    `json:"animations"`
}

// NPCSpriteAnim is one (direction, animation) mapping into the sprite sheet.
// row_index is the 0-indexed row in the sheet; frames run left-to-right from
// column 0 to frame_count - 1. frame_rate is frames per second.
type NPCSpriteAnim struct {
	Direction  string  `json:"direction"`
	Animation  string  `json:"animation"`
	RowIndex   int     `json:"row_index"`
	FrameCount int     `json:"frame_count"`
	FrameRate  float64 `json:"frame_rate"`
}

// NPC is a placed NPC instance. current_x/y is the last persisted position
// (updated on waypoint arrival in later milestones, not per tick). facing is
// the direction the sprite should render.
type NPC struct {
	ID             string     `json:"id"`
	DisplayName    string     `json:"display_name"`
	SpriteID       string     `json:"sprite_id"`
	HomeX          float64    `json:"home_x"`
	HomeY          float64    `json:"home_y"`
	CurrentX       float64    `json:"current_x"`
	CurrentY       float64    `json:"current_y"`
	Facing         string     `json:"facing"`
	Behavior       *string    `json:"behavior"`
	LLMMemoryAgent *string    `json:"llm_memory_agent"`
	Sprite         *NPCSprite `json:"sprite,omitempty"`
}

// loadNPCSprites returns the sprite catalog as a map keyed by sprite id,
// with animations and pack metadata attached. Shared by the sprite-list
// endpoint (catalog lookup for placement) and the NPC-list endpoint (which
// inlines each NPC's sprite for rendering).
func (app *App) loadNPCSprites(ctx context.Context) (map[string]*NPCSprite, error) {
	packs := map[string]*TilesetPack{}
	packRows, err := app.DB.Query(ctx, `SELECT id, name, url FROM tileset_pack`)
	if err != nil {
		return nil, err
	}
	for packRows.Next() {
		var p TilesetPack
		if err := packRows.Scan(&p.ID, &p.Name, &p.URL); err == nil {
			packs[p.ID] = &p
		}
	}
	packRows.Close()

	sprites := map[string]*NPCSprite{}
	spriteRows, err := app.DB.Query(ctx,
		`SELECT id, name, sheet, frame_width, frame_height, pack_id
		 FROM npc_sprite
		 ORDER BY name`,
	)
	if err != nil {
		return nil, err
	}
	for spriteRows.Next() {
		var s NPCSprite
		if err := spriteRows.Scan(&s.ID, &s.Name, &s.Sheet, &s.FrameWidth, &s.FrameHeight, &s.PackID); err != nil {
			continue
		}
		s.Animations = []NPCSpriteAnim{}
		if s.PackID != nil {
			if p, ok := packs[*s.PackID]; ok {
				s.Pack = p
			}
		}
		sprites[s.ID] = &s
	}
	spriteRows.Close()

	animRows, err := app.DB.Query(ctx,
		`SELECT sprite_id, direction, animation, row_index, frame_count, frame_rate
		 FROM npc_sprite_animation
		 ORDER BY sprite_id, direction, animation`,
	)
	if err != nil {
		return nil, err
	}
	for animRows.Next() {
		var spriteID string
		var a NPCSpriteAnim
		if err := animRows.Scan(&spriteID, &a.Direction, &a.Animation,
			&a.RowIndex, &a.FrameCount, &a.FrameRate); err != nil {
			continue
		}
		if s, ok := sprites[spriteID]; ok {
			s.Animations = append(s.Animations, a)
		}
	}
	animRows.Close()

	return sprites, nil
}

// handleListNPCSprites returns the sprite catalog (templates available for
// placement), ordered by name. Used by the editor panel to render an NPC
// placement catalog analogous to the asset catalog.
func (app *App) handleListNPCSprites(w http.ResponseWriter, r *http.Request) {
	sprites, err := app.loadNPCSprites(r.Context())
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	out := make([]*NPCSprite, 0, len(sprites))
	for _, s := range sprites {
		out = append(out, s)
	}
	// Stable alphabetical order so the editor panel's render is deterministic.
	// loadNPCSprites orders the SQL, but the map iteration loses it.
	sortNPCSpritesByName(out)
	jsonResponse(w, http.StatusOK, out)
}

// handleListNPCs returns every NPC with its sprite + animations inlined.
// Same shape as the asset catalog endpoint to keep the client side consistent.
func (app *App) handleListNPCs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sprites, err := app.loadNPCSprites(ctx)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	npcRows, err := app.DB.Query(ctx,
		`SELECT id, display_name, sprite_id, home_x, home_y,
		        current_x, current_y, facing, behavior, llm_memory_agent
		 FROM npc
		 ORDER BY display_name`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer npcRows.Close()

	npcs := []NPC{}
	for npcRows.Next() {
		var n NPC
		if err := npcRows.Scan(&n.ID, &n.DisplayName, &n.SpriteID,
			&n.HomeX, &n.HomeY, &n.CurrentX, &n.CurrentY, &n.Facing, &n.Behavior, &n.LLMMemoryAgent); err != nil {
			continue
		}
		if s, ok := sprites[n.SpriteID]; ok {
			n.Sprite = s
		}
		npcs = append(npcs, n)
	}

	jsonResponse(w, http.StatusOK, npcs)
}

// sortNPCSpritesByName is an insertion sort — the list is always small
// (a handful of character sheets), no need for a generic sort import.
func sortNPCSpritesByName(s []*NPCSprite) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1].Name > s[j].Name; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// handleCreateNPC places a new NPC on the map. Admin only. Both home_x/y and
// current_x/y are initialized to the placement point so the villager "lives"
// where they're placed. behavior and llm_memory_agent stay null at creation —
// linking to an agent or assigning a routine is a separate admin action.
//
// Broadcasts npc_created so other connected clients render the new NPC.
func (app *App) handleCreateNPC(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin role required", http.StatusForbidden)
		return
	}

	var req struct {
		Name     string  `json:"name"`
		SpriteID string  `json:"sprite_id"`
		X        float64 `json:"x"`
		Y        float64 `json:"y"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		req.Name = "Villager"
	}
	if req.SpriteID == "" {
		jsonError(w, "sprite_id is required", http.StatusBadRequest)
		return
	}

	// Verify the sprite exists so the FK insert returns a friendly error
	// rather than a generic 500.
	var spriteCount int
	if err := app.DB.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM npc_sprite WHERE id = $1`, req.SpriteID,
	).Scan(&spriteCount); err != nil || spriteCount == 0 {
		jsonError(w, "Unknown sprite_id", http.StatusBadRequest)
		return
	}

	id := newUUIDv7()
	_, err := app.DB.Exec(r.Context(),
		`INSERT INTO npc (id, display_name, sprite_id, home_x, home_y,
		                  current_x, current_y, facing)
		 VALUES ($1, $2, $3, $4, $5, $4, $5, 'south')`,
		id, req.Name, req.SpriteID, req.X, req.Y,
	)
	if err != nil {
		jsonError(w, "Failed to create NPC", http.StatusInternalServerError)
		return
	}

	// Build the response with the full sprite inlined so the client can
	// render immediately without a follow-up fetch — same shape as
	// handleListNPCs returns per-NPC.
	sprites, err := app.loadNPCSprites(r.Context())
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	npc := NPC{
		ID:          id,
		DisplayName: req.Name,
		SpriteID:    req.SpriteID,
		HomeX:       req.X,
		HomeY:       req.Y,
		CurrentX:    req.X,
		CurrentY:    req.Y,
		Facing:      "south",
	}
	if s, ok := sprites[req.SpriteID]; ok {
		npc.Sprite = s
	}

	jsonResponse(w, http.StatusCreated, npc)
	app.Hub.Broadcast(WorldEvent{Type: "npc_created", Data: npc})
}

// handleDeleteNPC removes a placed NPC. Admin only. Broadcasts npc_deleted
// so every connected client removes the sprite.
func (app *App) handleDeleteNPC(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin role required", http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, "Missing NPC ID", http.StatusBadRequest)
		return
	}
	result, err := app.DB.Exec(r.Context(),
		`DELETE FROM npc WHERE id = $1`, id,
	)
	if err != nil {
		jsonError(w, "Failed to delete NPC", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		jsonError(w, "NPC not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{Type: "npc_deleted", Data: map[string]string{"id": id}})
}
