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
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
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
//
// HomeStructureID and WorkStructureID are optional references to
// village_object rows (category=structure). When HomeStructureID is set,
// behavior routes use the structure's coords for the return-walk leg;
// HomeX/HomeY are kept as a fallback for NPCs without an assigned house.
// WorkStructureID is data-only as of Milestone 5 — reserved for future
// idle/daytime behaviors.
type NPC struct {
	ID              string     `json:"id"`
	DisplayName     string     `json:"display_name"`
	SpriteID        string     `json:"sprite_id"`
	HomeX           float64    `json:"home_x"`
	HomeY           float64    `json:"home_y"`
	CurrentX        float64    `json:"current_x"`
	CurrentY        float64    `json:"current_y"`
	Facing          string     `json:"facing"`
	Behavior        *string    `json:"behavior"`
	LLMMemoryAgent  *string    `json:"llm_memory_agent"`
	HomeStructureID *string    `json:"home_structure_id"`
	WorkStructureID *string    `json:"work_structure_id"`
	// Inside is true while the villager is "home" (arrived at their home
	// door tile after a behavior cycle). Client hides the sprite when set;
	// the next cycle flips it back on exit.
	Inside          bool       `json:"inside"`
	// InsideStructureID points at the specific village_object the villager
	// is currently inside. Nullable; only meaningful when Inside=true.
	// Used to drive occupancy-sensitive state flipping (market stall
	// open/closed) and future "who's in this building" UIs.
	InsideStructureID *string  `json:"inside_structure_id"`
	// ScheduleStartMinute / ScheduleEndMinute are the per-NPC absolute work
	// window in minutes-of-day (1-min precision). Only the worker behavior
	// reads them. Both NULL = inherit dawn/dusk at evaluation time. The
	// schedule_window_all_or_none DB CHECK guarantees they travel as a pair.
	// Window wraps midnight when start > end (tavernkeeper 17:00–05:00).
	// ZBBS-071 replaced the older schedule_offset_minutes scalar.
	ScheduleStartMinute *int `json:"schedule_start_minute"`
	ScheduleEndMinute   *int `json:"schedule_end_minute"`
	// ScheduleIntervalHours + ActiveStartHour + ActiveEndHour are the
	// per-NPC cadence knobs for interval behaviors (washerwoman,
	// town_crier). All three must be set together or all three left null
	// (enforced at the DB level). Null falls back to the legacy
	// world_rotation_time trigger for those behaviors.
	ScheduleIntervalHours *int `json:"schedule_interval_hours"`
	ActiveStartHour       *int `json:"active_start_hour"`
	ActiveEndHour         *int `json:"active_end_hour"`
	// LatenessWindowMinutes fuzzes scheduled behavior firing times in
	// an asymmetric window after the nominal boundary. The per-boundary
	// offset is deterministic (hash of npc_id + boundary) so it's
	// stable across ticks and restarts. 0 = deterministic boundary
	// firing (ZBBS-064 behavior). Capped at 180.
	LatenessWindowMinutes int `json:"lateness_window_minutes"`
	// SocialTag + SocialStartMinute + SocialEndMinute are the per-NPC
	// social hour knobs (ZBBS-068, minute-precision since ZBBS-071).
	// Orthogonal to `behavior`: any NPC can opt in. All three set together
	// or all three null (enforced by the social_all_or_none DB CHECK).
	SocialTag         *string    `json:"social_tag"`
	SocialStartMinute *int       `json:"social_start_minute"`
	SocialEndMinute   *int       `json:"social_end_minute"`
	// Needs (ZBBS-082) — current values in [0, attributeMax]. The editor
	// surfaces these as a read-out plus a "top up" admin action; the
	// engine uses them via the chronicler distress section and
	// applyConsumption.
	Hunger    int        `json:"hunger"`
	Thirst    int        `json:"thirst"`
	Tiredness int        `json:"tiredness"`
	Sprite    *NPCSprite `json:"sprite,omitempty"`
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
		        current_x, current_y, facing, behavior, llm_memory_agent,
		        home_structure_id, work_structure_id, inside, inside_structure_id,
		        schedule_start_minute, schedule_end_minute,
		        schedule_interval_hours,
		        active_start_hour, active_end_hour,
		        lateness_window_minutes,
		        social_tag, social_start_minute, social_end_minute,
		        hunger, thirst, tiredness
		 FROM actor
		 WHERE login_username IS NULL
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
			&n.HomeX, &n.HomeY, &n.CurrentX, &n.CurrentY, &n.Facing, &n.Behavior, &n.LLMMemoryAgent,
			&n.HomeStructureID, &n.WorkStructureID, &n.Inside, &n.InsideStructureID,
			&n.ScheduleStartMinute, &n.ScheduleEndMinute,
			&n.ScheduleIntervalHours,
			&n.ActiveStartHour, &n.ActiveEndHour,
			&n.LatenessWindowMinutes,
			&n.SocialTag, &n.SocialStartMinute, &n.SocialEndMinute,
			&n.Hunger, &n.Thirst, &n.Tiredness); err != nil {
			continue
		}
		if s, ok := sprites[n.SpriteID]; ok {
			n.Sprite = s
		}
		// Interpolate for active walks so a client loading mid-walk sees
		// the NPC at their currently-visible position rather than the
		// pre-walk DB snapshot.
		app.NPCMovement.mu.Lock()
		if w := app.NPCMovement.active[n.ID]; w != nil {
			n.CurrentX, n.CurrentY = w.currentPosition()
		}
		app.NPCMovement.mu.Unlock()
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
		`INSERT INTO actor (id, display_name, sprite_id, home_x, home_y,
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

// NPCBehavior is a row from npc_behavior — the allowed values for npc.behavior.
// The editor panel fetches this list to populate the behavior dropdown.
type NPCBehavior struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

// handleListNPCBehaviors returns all behaviors that can be assigned to an NPC.
// Public to any authenticated salem user — the catalog is not sensitive and
// non-admins who can see NPC details may want to know what behaviors exist.
func (app *App) handleListNPCBehaviors(w http.ResponseWriter, r *http.Request) {
	rows, err := app.DB.Query(r.Context(),
		`SELECT slug, display_name FROM npc_behavior ORDER BY display_name`,
	)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	behaviors := []NPCBehavior{}
	for rows.Next() {
		var b NPCBehavior
		if err := rows.Scan(&b.Slug, &b.DisplayName); err != nil {
			continue
		}
		behaviors = append(behaviors, b)
	}

	jsonResponse(w, http.StatusOK, behaviors)
}

// handleSetNPCDisplayName renames a placed NPC. Admin only. Broadcasts
// npc_display_name_changed so every client refreshes the villager's label.
// Blank names are rejected — use the create-time default "Villager" instead
// of letting a rename clear the label.
func (app *App) handleSetNPCDisplayName(w http.ResponseWriter, r *http.Request) {
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

	var req struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.DisplayName == "" {
		jsonError(w, "display_name is required", http.StatusBadRequest)
		return
	}

	result, err := app.DB.Exec(r.Context(),
		`UPDATE actor SET display_name = $1 WHERE id = $2`,
		req.DisplayName, id,
	)
	if err != nil {
		jsonError(w, "Failed to update display name", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		jsonError(w, "NPC not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{Type: "npc_display_name_changed", Data: map[string]interface{}{
		"id":           id,
		"display_name": req.DisplayName,
	}})
}

// handleSetNPCSprite swaps the sprite sheet used to render an NPC. Admin only.
// Useful for fixing a placement-time mismatch (e.g. tavernkeeper got placed
// with a generic villager sprite). Body: {sprite_id}. Broadcasts
// npc_sprite_changed with the full sprite (sheet + animations) inlined so
// every client can rebuild the AnimatedSprite2D without a follow-up fetch —
// same shape as handleCreateNPC's broadcast.
func (app *App) handleSetNPCSprite(w http.ResponseWriter, r *http.Request) {
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

	var req struct {
		SpriteID string `json:"sprite_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	req.SpriteID = strings.TrimSpace(req.SpriteID)
	if req.SpriteID == "" {
		jsonError(w, "sprite_id is required", http.StatusBadRequest)
		return
	}

	// Verify the sprite exists so the FK update returns a friendly error
	// rather than a generic 500.
	var spriteCount int
	if err := app.DB.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM npc_sprite WHERE id = $1`, req.SpriteID,
	).Scan(&spriteCount); err != nil || spriteCount == 0 {
		jsonError(w, "Unknown sprite_id", http.StatusBadRequest)
		return
	}

	result, err := app.DB.Exec(r.Context(),
		`UPDATE actor SET sprite_id = $1 WHERE id = $2`,
		req.SpriteID, id,
	)
	if err != nil {
		jsonError(w, "Failed to update sprite", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		jsonError(w, "NPC not found", http.StatusNotFound)
		return
	}

	// Inline the full sprite so the client can rebuild without re-fetching.
	sprites, err := app.loadNPCSprites(r.Context())
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	data := map[string]interface{}{
		"id":        id,
		"sprite_id": req.SpriteID,
	}
	if s, ok := sprites[req.SpriteID]; ok {
		data["sprite"] = s
	}

	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{Type: "npc_sprite_changed", Data: data})
}

// handleSetNPCBehavior assigns or clears the behavior of an NPC. Admin only.
// A null/empty behavior clears the field. The FK on npc.behavior enforces
// validity against npc_behavior.slug, but we pre-check to return a clean 400
// rather than a generic 500 on invalid input.
//
// Live-route note: changing behavior mid-route does not interrupt an ongoing
// walk. The current walk-to AfterFunc still fires and applyArrival runs the
// normal lamplighter chain if the behavior was lamplighter at walk START.
// The next phase transition will look up the current behavior fresh and pick
// whoever is currently tagged.
func (app *App) handleSetNPCBehavior(w http.ResponseWriter, r *http.Request) {
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

	var req struct {
		Behavior *string `json:"behavior"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	// Normalize empty string to null so the client can send either.
	if req.Behavior != nil {
		trimmed := strings.TrimSpace(*req.Behavior)
		if trimmed == "" {
			req.Behavior = nil
		} else {
			req.Behavior = &trimmed
		}
	}

	if req.Behavior != nil {
		var exists bool
		if err := app.DB.QueryRow(r.Context(),
			`SELECT EXISTS(SELECT 1 FROM npc_behavior WHERE slug = $1)`,
			*req.Behavior,
		).Scan(&exists); err != nil {
			jsonError(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		if !exists {
			jsonError(w, "Unknown behavior", http.StatusBadRequest)
			return
		}
	}

	result, err := app.DB.Exec(r.Context(),
		`UPDATE actor SET behavior = $1 WHERE id = $2`,
		req.Behavior, id,
	)
	if err != nil {
		jsonError(w, "Failed to update behavior", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		jsonError(w, "NPC not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{Type: "npc_behavior_changed", Data: map[string]interface{}{
		"id":       id,
		"behavior": req.Behavior,
	}})
}

// handleSetNPCAgent links or unlinks the llm_memory_agent for an NPC.
// Admin only. Broadcasts npc_agent_changed. A null/empty value unlinks.
// The agent slug must match a row in village_agent — this scopes the picker
// to characters registered for this village rather than any global llm-memory
// actor.
func (app *App) handleSetNPCAgent(w http.ResponseWriter, r *http.Request) {
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

	var req struct {
		LLMMemoryAgent *string `json:"llm_memory_agent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.LLMMemoryAgent != nil {
		trimmed := strings.TrimSpace(*req.LLMMemoryAgent)
		if trimmed == "" {
			req.LLMMemoryAgent = nil
		} else {
			req.LLMMemoryAgent = &trimmed
		}
	}

	if req.LLMMemoryAgent != nil {
		var exists bool
		if err := app.DB.QueryRow(r.Context(),
			`SELECT EXISTS(SELECT 1 FROM actor WHERE llm_memory_agent = $1)`,
			*req.LLMMemoryAgent,
		).Scan(&exists); err != nil {
			jsonError(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		if !exists {
			jsonError(w, "Unknown agent", http.StatusBadRequest)
			return
		}
	}

	result, err := app.DB.Exec(r.Context(),
		`UPDATE actor SET llm_memory_agent = $1 WHERE id = $2`,
		req.LLMMemoryAgent, id,
	)
	if err != nil {
		jsonError(w, "Failed to update agent", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		jsonError(w, "NPC not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{Type: "npc_agent_changed", Data: map[string]interface{}{
		"id":               id,
		"llm_memory_agent": req.LLMMemoryAgent,
	}})
}

// handleSetNPCSchedule updates the per-NPC scheduling knobs in one atomic
// PATCH. Admin only. Accepts:
//
//	schedule_start_minute / schedule_end_minute — both null or both set
//	  (int 0–1439, minutes-of-day). Worker behavior reads this absolute
//	  window; others ignore. Both null = inherit dawn/dusk at evaluation
//	  time. Window wraps midnight when start > end (ZBBS-071).
//	schedule_interval_hours, active_start_hour, active_end_hour — all
//	  three or none. The DB CHECK constraint schedule_all_or_none
//	  enforces this; the handler pre-validates to return a clean 400.
//	lateness_window_minutes — optional. Omit to keep the current value.
//
// Clears last_shift_tick_at so the new schedule re-evaluates on the next
// server tick rather than waiting up to 12h for the following boundary.
// Broadcasts npc_schedule_changed with the full new schedule payload.
func (app *App) handleSetNPCSchedule(w http.ResponseWriter, r *http.Request) {
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

	var req struct {
		ScheduleStartMinute   *int `json:"schedule_start_minute"`
		ScheduleEndMinute     *int `json:"schedule_end_minute"`
		ScheduleIntervalHours *int `json:"schedule_interval_hours"`
		ActiveStartHour       *int `json:"active_start_hour"`
		ActiveEndHour         *int `json:"active_end_hour"`
		LatenessWindowMinutes *int `json:"lateness_window_minutes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// All-or-none for the absolute window pair.
	startSet := req.ScheduleStartMinute != nil
	endSet := req.ScheduleEndMinute != nil
	if startSet != endSet {
		jsonError(w, "schedule_start_minute and schedule_end_minute must be set together (both null to inherit dawn/dusk)", http.StatusBadRequest)
		return
	}
	if startSet {
		if *req.ScheduleStartMinute < 0 || *req.ScheduleStartMinute > 1439 {
			jsonError(w, "schedule_start_minute must be between 0 and 1439", http.StatusBadRequest)
			return
		}
		if *req.ScheduleEndMinute < 0 || *req.ScheduleEndMinute > 1439 {
			jsonError(w, "schedule_end_minute must be between 0 and 1439", http.StatusBadRequest)
			return
		}
	}

	// lateness_window_minutes is optional on PATCH — a client can omit it
	// to keep the current value. Default for new NPCs is 0 (DB default).
	// When provided, range-check against the same bounds the DB enforces.
	if req.LatenessWindowMinutes != nil {
		if *req.LatenessWindowMinutes < 0 || *req.LatenessWindowMinutes > 180 {
			jsonError(w, "lateness_window_minutes must be between 0 and 180", http.StatusBadRequest)
			return
		}
	}

	// All-or-none for the cadence triple.
	cadenceSet := 0
	if req.ScheduleIntervalHours != nil {
		cadenceSet++
	}
	if req.ActiveStartHour != nil {
		cadenceSet++
	}
	if req.ActiveEndHour != nil {
		cadenceSet++
	}
	if cadenceSet != 0 && cadenceSet != 3 {
		jsonError(w, "schedule_interval_hours, active_start_hour, and active_end_hour must be set together", http.StatusBadRequest)
		return
	}
	if req.ScheduleIntervalHours != nil && (*req.ScheduleIntervalHours < 1 || *req.ScheduleIntervalHours > 24) {
		jsonError(w, "schedule_interval_hours must be between 1 and 24", http.StatusBadRequest)
		return
	}
	if req.ActiveStartHour != nil && (*req.ActiveStartHour < 0 || *req.ActiveStartHour > 23) {
		jsonError(w, "active_start_hour must be between 0 and 23", http.StatusBadRequest)
		return
	}
	if req.ActiveEndHour != nil && (*req.ActiveEndHour < 0 || *req.ActiveEndHour > 23) {
		jsonError(w, "active_end_hour must be between 0 and 23", http.StatusBadRequest)
		return
	}

	// COALESCE on lateness_window_minutes lets a PATCH that omits the
	// field keep the existing value — existing clients that only send
	// the schedule-triple continue to work unchanged. RETURNING reads
	// back the effective value so the broadcast carries ground truth
	// for every field.
	var effectiveLateness int
	err := app.DB.QueryRow(r.Context(),
		`UPDATE actor SET
		    schedule_start_minute = $2,
		    schedule_end_minute = $3,
		    schedule_interval_hours = $4,
		    active_start_hour = $5,
		    active_end_hour = $6,
		    lateness_window_minutes = COALESCE($7, lateness_window_minutes),
		    last_shift_tick_at = NULL
		 WHERE id = $1
		 RETURNING lateness_window_minutes`,
		id,
		req.ScheduleStartMinute, req.ScheduleEndMinute,
		req.ScheduleIntervalHours, req.ActiveStartHour, req.ActiveEndHour,
		req.LatenessWindowMinutes,
	).Scan(&effectiveLateness)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			jsonError(w, "NPC not found", http.StatusNotFound)
			return
		}
		jsonError(w, "Failed to update schedule", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{Type: "npc_schedule_changed", Data: map[string]interface{}{
		"id":                      id,
		"schedule_start_minute":   req.ScheduleStartMinute,
		"schedule_end_minute":     req.ScheduleEndMinute,
		"schedule_interval_hours": req.ScheduleIntervalHours,
		"active_start_hour":       req.ActiveStartHour,
		"active_end_hour":         req.ActiveEndHour,
		"lateness_window_minutes": effectiveLateness,
	}})
}

// handleSetNPCSocial updates the per-NPC social-hour schedule (ZBBS-068,
// minute-precision since ZBBS-071). Admin only. All three fields must be
// set together (all or none), matching the social_all_or_none DB CHECK.
// Clears social_last_boundary_at so the new window evaluates on the next
// tick rather than waiting out the current boundary. Broadcasts
// npc_social_updated.
//
// Payload:
//
//	social_tag           — string. One of the allowed object tags. null
//	                       or empty to clear the schedule.
//	social_start_minute  — int [0,1439]. Minutes-of-day, inclusive start.
//	social_end_minute    — int [0,1439]. Window wraps midnight when
//	                       start > end (late-night gatherings).
func (app *App) handleSetNPCSocial(w http.ResponseWriter, r *http.Request) {
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

	// All three fields are pointers so we can distinguish "omitted" from
	// "null" from "value". Clients clear the schedule by sending all three
	// as null (or an empty tag string).
	var req struct {
		SocialTag         *string `json:"social_tag"`
		SocialStartMinute *int    `json:"social_start_minute"`
		SocialEndMinute   *int    `json:"social_end_minute"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Normalize empty-string tag to null-tag so admins can clear by sending
	// an empty string from a form field.
	tagEmpty := req.SocialTag == nil || *req.SocialTag == ""
	startMissing := req.SocialStartMinute == nil
	endMissing := req.SocialEndMinute == nil

	// All-or-none: either everything's set or everything's cleared.
	allSet := !tagEmpty && !startMissing && !endMissing
	allClear := tagEmpty && startMissing && endMissing
	if !allSet && !allClear {
		jsonError(w, "social_tag, social_start_minute, and social_end_minute must be set together (or all cleared)", http.StatusBadRequest)
		return
	}

	if allSet {
		// social_tag is a per-instance object tag (ZBBS-069), validated
		// against allowedObjectTags — not the asset-state allowlist,
		// which describes identity-level tags.
		if !allowedObjectTags[*req.SocialTag] {
			jsonError(w, "Unknown social_tag (see /api/village/object-tags)", http.StatusBadRequest)
			return
		}
		if *req.SocialStartMinute < 0 || *req.SocialStartMinute > 1439 {
			jsonError(w, "social_start_minute must be between 0 and 1439", http.StatusBadRequest)
			return
		}
		if *req.SocialEndMinute < 0 || *req.SocialEndMinute > 1439 {
			jsonError(w, "social_end_minute must be between 0 and 1439", http.StatusBadRequest)
			return
		}
	}

	var tagParam interface{}
	var startParam interface{}
	var endParam interface{}
	if allSet {
		tagParam = *req.SocialTag
		startParam = *req.SocialStartMinute
		endParam = *req.SocialEndMinute
	} else {
		tagParam = nil
		startParam = nil
		endParam = nil
	}

	tag, err := app.DB.Exec(r.Context(),
		`UPDATE actor SET
		    social_tag = $2,
		    social_start_minute = $3,
		    social_end_minute = $4,
		    social_last_boundary_at = NULL
		 WHERE id = $1`,
		id, tagParam, startParam, endParam,
	)
	if err != nil {
		jsonError(w, "Failed to update social schedule", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		jsonError(w, "NPC not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{Type: "npc_social_updated", Data: map[string]interface{}{
		"id":                  id,
		"social_tag":          req.SocialTag,
		"social_start_minute": req.SocialStartMinute,
		"social_end_minute":   req.SocialEndMinute,
	}})
}

// handleSetNPCHomeStructure links or clears the NPC's home structure.
// Admin only. A null / empty payload clears the link, falling the NPC back
// to its scalar home_x/home_y. When set, the structure must reference an
// existing village_object row (any category — the editor constrains the
// dropdown to category=structure, but the server doesn't enforce it).
// Broadcasts npc_home_structure_changed.
func (app *App) handleSetNPCHomeStructure(w http.ResponseWriter, r *http.Request) {
	app.patchNPCStructure(w, r, "home_structure_id", "npc_home_structure_changed", "home_structure_id")
}

// handleSetNPCWorkStructure links or clears the NPC's work structure.
// Admin only. Milestone 5 ships this as data only — no behavior reads it
// yet. Broadcasts npc_work_structure_changed.
func (app *App) handleSetNPCWorkStructure(w http.ResponseWriter, r *http.Request) {
	app.patchNPCStructure(w, r, "work_structure_id", "npc_work_structure_changed", "work_structure_id")
}

// patchNPCStructure is the shared implementation for the home/work
// structure PATCH endpoints. column is the npc column to update; event is
// the WS event type; field is the JSON field name in the request body and
// broadcast data.
func (app *App) patchNPCStructure(w http.ResponseWriter, r *http.Request, column, event, field string) {
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

	// Request body is a single field matching `field` (e.g. home_structure_id).
	// Decode into a generic map rather than a struct so both handlers share code.
	var raw map[string]*string
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	val := raw[field]
	if val != nil {
		trimmed := strings.TrimSpace(*val)
		if trimmed == "" {
			val = nil
		} else {
			val = &trimmed
		}
	}

	// Verify the structure id references a real object when set.
	if val != nil {
		var exists bool
		if err := app.DB.QueryRow(r.Context(),
			`SELECT EXISTS(SELECT 1 FROM village_object WHERE id = $1)`, *val,
		).Scan(&exists); err != nil {
			jsonError(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		if !exists {
			jsonError(w, "Unknown structure", http.StatusBadRequest)
			return
		}
	}

	// Column is fixed per-caller, not user input — safe to interpolate.
	result, err := app.DB.Exec(r.Context(),
		`UPDATE actor SET `+column+` = $1 WHERE id = $2`,
		val, id,
	)
	if err != nil {
		jsonError(w, "Failed to update structure link", http.StatusInternalServerError)
		return
	}
	if result.RowsAffected() == 0 {
		jsonError(w, "NPC not found", http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
	app.Hub.Broadcast(WorldEvent{Type: event, Data: map[string]interface{}{
		"id":  id,
		field: val,
	}})
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
		`DELETE FROM actor WHERE id = $1`, id,
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

// handleResetNPCNeeds zeroes the selected NPC's hunger, thirst, and
// tiredness. Admin only. Routes through applyConsumption so the
// chronicler dispatch is fed needs_resolved events for any need that
// crossed the red threshold during this call — letting the chronicler
// nudge an agent NPC who had abandoned a post back to work the same
// way the future well mechanic will.
//
// PC guard: this endpoint targets NPC rows only (login_username IS
// NULL), matching the rest of the /api/village/npcs/* routes.
// Resetting a PC's needs would bypass the player-driven path and
// confuse the client.
//
// Response: 200 with the post-update {hunger, thirst, tiredness} values
// read back from the row so the editor panel reflects what's actually
// persisted (not an assumed zero). Broadcasts an npc_needs_changed WS
// event so other open editor sessions reflect the reset live.
func (app *App) handleResetNPCNeeds(w http.ResponseWriter, r *http.Request) {
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

	ctx := r.Context()
	tx, err := app.DB.Begin(ctx)
	if err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(ctx)

	// PC guard. Done as a separate preflight (rather than baked into
	// applyConsumption) because applyConsumption is a domain helper
	// usable from contexts where the caller already knows the row is
	// an NPC (pay.go's buyer was already validated). Run inside the
	// same txn so an admin who somehow manages to flip the row in
	// between can't slip through.
	var isNPC bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM actor WHERE id = $1 AND login_username IS NULL)`,
		id,
	).Scan(&isNPC); err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !isNPC {
		jsonError(w, "NPC not found", http.StatusNotFound)
		return
	}

	// -attributeMax saturates the clamp regardless of the current
	// value, so this is the "fully top up" delta. Source "admin"
	// suppresses the chronicler perception's source hint.
	result, err := app.applyConsumption(ctx, tx, id, consumptionDelta{
		Hunger:    -attributeMax,
		Thirst:    -attributeMax,
		Tiredness: -attributeMax,
	}, "admin")
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			jsonError(w, "NPC not found", http.StatusNotFound)
			return
		}
		jsonError(w, "Failed to reset needs", http.StatusInternalServerError)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"id":        id,
		"hunger":    result.Hunger,
		"thirst":    result.Thirst,
		"tiredness": result.Tiredness,
	}
	jsonResponse(w, http.StatusOK, resp)
	app.Hub.Broadcast(WorldEvent{Type: "npc_needs_changed", Data: resp})
}
