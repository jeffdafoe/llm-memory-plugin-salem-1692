package main

// NPC catalog + instance endpoints.
//
// Milestone 1a exposes GET /api/village/npcs — returns every placed NPC with
// its sprite metadata and animation rows inlined, so the client can render
// them from a single round-trip after auth. Future milestones layer movement
// (waypoint broadcast via WS), editor placement (POST /npcs), and the LLM
// agent linkage on top.

import (
	"net/http"
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

// handleListNPCs returns every NPC with its sprite + animations inlined.
// Sprite pack info is resolved per sprite. Same shape as the asset catalog
// endpoint to keep the client side consistent.
func (app *App) handleListNPCs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Packs first so we can attach them to sprites below.
	packs := map[string]*TilesetPack{}
	packRows, err := app.DB.Query(ctx, `SELECT id, name, url FROM tileset_pack`)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	for packRows.Next() {
		var p TilesetPack
		if err := packRows.Scan(&p.ID, &p.Name, &p.URL); err == nil {
			packs[p.ID] = &p
		}
	}
	packRows.Close()

	// Sprites.
	sprites := map[string]*NPCSprite{}
	spriteRows, err := app.DB.Query(ctx,
		`SELECT id, name, sheet, frame_width, frame_height, pack_id
		 FROM npc_sprite
		 ORDER BY name`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
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

	// Animations attached to their sprite.
	animRows, err := app.DB.Query(ctx,
		`SELECT sprite_id, direction, animation, row_index, frame_count, frame_rate
		 FROM npc_sprite_animation
		 ORDER BY sprite_id, direction, animation`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
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

	// NPCs + inline sprite pointer.
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
