package main

// Player-character (PC) HTTP handlers (M6.7).
//
// PCs are salem-realm llm-memory users who walk around the village,
// join scene huddles alongside NPCs, and converse with them. Their
// presence and position live in pc_position; their identity comes
// from the authenticated session.
//
// Two identities per PC:
//   - login_username: the llm-memory username, stable system identity.
//     Used for session lookup, chat_send sender attribution.
//   - character_name: the in-world identity NPCs perceive. Period-
//     appropriate, set by the player on first login. NPCs greet by
//     character_name; audit log records speech with character_name.
//
// Endpoints:
//
//   - POST /api/village/pc/me      → state read for the talk panel:
//                                     character_name, position, huddle
//                                     members, sprite. Returns
//                                     exists=false when the PC has
//                                     never been created (client
//                                     bootstrap pops the sprite picker
//                                     to drive a /pc/create call).
//   - POST /api/village/pc/create  → first-time creation. Body:
//                                     {character_name, sprite_id?}.
//                                     Auto-assigns home to the nearest
//                                     tavern. Idempotent on the
//                                     login_username (re-running
//                                     updates the name and, when
//                                     sprite_id is provided, the
//                                     sprite).
//   - POST /api/village/pc/sprite  → swap the PC's render sprite. Body:
//                                     {sprite_id}. Distinct from /create
//                                     so the picker can drive a sprite
//                                     change without re-asserting the
//                                     character_name.
//   - POST /api/village/pc/move    → click-to-walk. Body:
//                                     {target_x, target_y, speed?}.
//                                     Resolves session→actor.id and
//                                     defers to startNPCWalk; the
//                                     existing npc_walking /
//                                     npc_arrived broadcasts cover the
//                                     PC because they key on actor id,
//                                     not driver kind.
//   - POST /api/village/pc/say     → 1:1 whisper to one NPC. Proxies
//                                     to /v1/chat/send with the user's
//                                     auth header.
//   - POST /api/village/pc/speak   → broadcast to current huddle.
//                                     agent_action_log row with
//                                     speaker_name=character_name,
//                                     source='player'. Co-located NPCs
//                                     get an event tick.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type pcMeResponse struct {
	LoginUsername     string             `json:"login_username"`
	Exists            bool               `json:"exists"`
	CharacterName     string             `json:"character_name,omitempty"`
	X                 float64            `json:"x"`
	Y                 float64            `json:"y"`
	InsideStructureID *string            `json:"inside_structure_id,omitempty"`
	StructureName     string             `json:"structure_name,omitempty"`
	HomeStructureID   *string            `json:"home_structure_id,omitempty"`
	HomeName          string             `json:"home_name,omitempty"`
	CurrentHuddleID   *string            `json:"current_huddle_id,omitempty"`
	HuddleMembers     []pcHuddleMember   `json:"huddle_members"`
	RecentSpeech      []pcRecentSpeech   `json:"recent_speech,omitempty"`
	// SpriteID is null until the player picks one. Client bootstrap uses
	// the null state as the trigger to open the sprite picker on first
	// login. Sprite is the inlined catalog row (sheet, frame dims,
	// animations, pack) so a freshly-arrived client can render the PC
	// without a follow-up catalog fetch — same shape as NPC.Sprite.
	SpriteID *string    `json:"sprite_id,omitempty"`
	Sprite   *NPCSprite `json:"sprite,omitempty"`
}

type pcHuddleMember struct {
	Kind        string  `json:"kind"` // "npc" or "pc"
	Name        string  `json:"name"` // display_name (NPC) or character_name (PC)
	Role        *string `json:"role,omitempty"`
	TargetAgent *string `json:"target_agent,omitempty"` // llm_memory_agent for NPCs (chat_send recipient)
}

// pcRecentSpeech is one historical conversational/narrative event at the
// player's current inside_structure_id, surfaced so the talk panel can
// backload room context when opened. The room metaphor: walk in, you
// hear what's been happening here lately.
//
// Kind discriminates how the client renders the entry:
//   - "speech_npc" / "speech_player" — quoted dialogue, color-coded
//   - "act"                         — italic narration ("X poured ale.")
//   - "departure"                   — italic narration ("X left for home.")
//
// Text is pre-rendered server-side so the client doesn't have to know
// the verb-phrase grammar or destination wording.
type pcRecentSpeech struct {
	SpeakerName string    `json:"speaker_name"`
	Text        string    `json:"text"`
	Kind        string    `json:"kind"`
	OccurredAt  time.Time `json:"occurred_at"`
}

const pcRecentSpeechLimit = 20
const pcRecentSpeechCutoff = 24 * time.Hour

func (app *App) handlePCMe(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	resp := pcMeResponse{
		LoginUsername: user.Username,
		HuddleMembers: []pcHuddleMember{},
	}

	var x, y float64
	var charName, insideID, huddleID, structureName, homeID, homeName, spriteID sql.NullString
	err := app.DB.QueryRow(r.Context(),
		`SELECT pc.display_name, pc.current_x, pc.current_y,
		        pc.inside_structure_id::text,
		        pc.current_huddle_id::text,
		        COALESCE(o.display_name, a.name) AS structure_name,
		        pc.home_structure_id::text,
		        COALESCE(ho.display_name, ha.name) AS home_name,
		        pc.sprite_id::text
		   FROM actor pc
		   LEFT JOIN village_object o ON o.id = pc.inside_structure_id
		   LEFT JOIN asset a ON a.id = o.asset_id
		   LEFT JOIN village_object ho ON ho.id = pc.home_structure_id
		   LEFT JOIN asset ha ON ha.id = ho.asset_id
		  WHERE pc.login_username = $1`,
		user.Username,
	).Scan(&charName, &x, &y, &insideID, &huddleID, &structureName, &homeID, &homeName, &spriteID)
	if err == sql.ErrNoRows {
		resp.Exists = false
		jsonResponse(w, http.StatusOK, resp)
		return
	}
	if err != nil {
		log.Printf("pc/me query: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}

	resp.Exists = true
	if charName.Valid {
		resp.CharacterName = charName.String
	}
	resp.X, resp.Y = x, y
	if insideID.Valid {
		s := insideID.String
		resp.InsideStructureID = &s
	}
	if structureName.Valid {
		resp.StructureName = structureName.String
	}
	if homeID.Valid {
		s := homeID.String
		resp.HomeStructureID = &s
	}
	if homeName.Valid {
		resp.HomeName = homeName.String
	}
	if spriteID.Valid {
		s := spriteID.String
		resp.SpriteID = &s
		// Inline the catalog row so the client can render the PC without
		// a follow-up GET /api/village/npc-sprites — same shape NPCs use.
		// loadNPCSprites is the same lookup the NPC list endpoint uses;
		// the cached map is small, so the per-request fetch is cheap.
		if sprites, err := app.loadNPCSprites(r.Context()); err == nil {
			if sp, ok := sprites[s]; ok {
				resp.Sprite = sp
			}
		}
	}

	if huddleID.Valid {
		s := huddleID.String
		resp.CurrentHuddleID = &s
		// Co-located actors in the same huddle. Single-table query after
		// ZBBS-084 — kind is implicit in which login column is populated.
		// PC excludes self via login_username.
		rows, err := app.DB.Query(r.Context(),
			`SELECT CASE WHEN login_username IS NOT NULL THEN 'pc' ELSE 'npc' END AS kind,
			        display_name, role, llm_memory_agent
			   FROM actor
			  WHERE current_huddle_id::text = $1
			    AND (login_username IS NULL OR login_username != $2)
			  ORDER BY display_name`,
			huddleID.String, user.Username)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var kind, name string
				var role, llmAgent sql.NullString
				if err := rows.Scan(&kind, &name, &role, &llmAgent); err != nil {
					continue
				}
				m := pcHuddleMember{Kind: kind, Name: name}
				if role.Valid {
					rs := role.String
					m.Role = &rs
				}
				if llmAgent.Valid {
					la := llmAgent.String
					m.TargetAgent = &la
				}
				resp.HuddleMembers = append(resp.HuddleMembers, m)
			}
		}
	}

	// Recent speech at the player's current location — backloads the
	// talk panel so a freshly-opened panel shows what the room has been
	// saying lately. Returned oldest→newest so the client can append in
	// natural reading order. Limited to pcRecentSpeechLimit rows in the
	// last pcRecentSpeechCutoff (24h) so a quiet room doesn't dredge up
	// week-old chatter and an active one only surfaces the recent thread.
	if insideID.Valid {
		resp.RecentSpeech = app.loadRecentSpeechAtStructure(r.Context(), insideID.String)
	}

	jsonResponse(w, http.StatusOK, resp)
}

// loadRecentSpeechAtStructure pulls the last N narration-worthy events at
// a structure: speech (dialogue), acts (verb-phrase narration), and
// move_to commits (departures). Filtered by payload->>'structure_id' which
// the speak/act/move_to commits stash at write time (see agent_tick.go's
// executeAgentCommit) so a single SQL filter scopes the whole room log.
//
// Result='ok' filters out rejected/empty attempts so the panel doesn't
// surface failed actions to the player. Text is pre-rendered into a
// readable line per row so the client renders strings, not grammar.
func (app *App) loadRecentSpeechAtStructure(ctx context.Context, structureID string) []pcRecentSpeech {
	cutoff := time.Now().Add(-pcRecentSpeechCutoff)
	// LEFT JOIN actor so move_to rows can rebuild the same departure
	// narration the live broadcast emits — narrateMoveDeparture needs
	// the speaker's home / work structure IDs to render "retired for
	// the evening" when home == work, otherwise "left for home". For
	// non-move_to rows the joined columns are unused.
	rows, err := app.DB.Query(ctx, `
		SELECT al.speaker_name, al.action_type, al.source, al.payload, al.occurred_at,
		       ac.home_structure_id, ac.work_structure_id
		FROM agent_action_log al
		LEFT JOIN actor ac ON ac.id = al.actor_id
		WHERE al.action_type IN ('speak', 'act', 'move_to')
		  AND al.result = 'ok'
		  AND al.payload->>'structure_id' = $1
		  AND al.occurred_at > $2
		ORDER BY al.occurred_at DESC
		LIMIT $3
	`, structureID, cutoff, pcRecentSpeechLimit)
	if err != nil {
		log.Printf("recent events: %v", err)
		return nil
	}
	defer rows.Close()

	// Collect newest-first, then reverse for natural reading order.
	var recent []pcRecentSpeech
	for rows.Next() {
		var speakerName, actionType, source string
		var payloadJSON []byte
		var occurredAt time.Time
		var homeStructureID, workStructureID sql.NullString
		if err := rows.Scan(&speakerName, &actionType, &source, &payloadJSON, &occurredAt,
			&homeStructureID, &workStructureID); err != nil {
			continue
		}
		var payload map[string]interface{}
		_ = json.Unmarshal(payloadJSON, &payload)

		entry := pcRecentSpeech{SpeakerName: speakerName, OccurredAt: occurredAt}

		switch actionType {
		case "speak":
			text, _ := payload["text"].(string)
			if text == "" {
				continue
			}
			entry.Text = text
			if source == "player" {
				entry.Kind = "speech_player"
			} else {
				entry.Kind = "speech_npc"
			}
		case "act":
			verb, _ := payload["verb_phrase"].(string)
			if verb == "" {
				continue
			}
			entry.Text = fmt.Sprintf("%s %s.", speakerName, verb)
			entry.Kind = "act"
		case "move_to":
			dest, _ := payload["destination"].(string)
			if dest == "" {
				continue
			}
			entry.Text = app.narrateMoveDeparture(ctx, speakerName, homeStructureID, workStructureID, dest)
			entry.Kind = "departure"
		default:
			continue
		}
		recent = append(recent, entry)
	}
	for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
		recent[i], recent[j] = recent[j], recent[i]
	}
	return recent
}

// handlePCCreate — first-time PC creation. Sets character_name and
// auto-assigns home_structure_id to the nearest tavern. Idempotent on
// re-call: updates character_name to the new value (lets a player
// rename mid-game if they want — UX decision deferred). Initial
// position is the home tavern's anchor (or village center fallback).
func (app *App) handlePCCreate(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	var req struct {
		CharacterName string  `json:"character_name"`
		SpriteID      *string `json:"sprite_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	req.CharacterName = strings.TrimSpace(req.CharacterName)
	if req.CharacterName == "" {
		jsonError(w, "character_name is required", http.StatusBadRequest)
		return
	}
	if len(req.CharacterName) > 100 {
		jsonError(w, "character_name too long", http.StatusBadRequest)
		return
	}
	// sprite_id is optional at creation. When provided, validate it exists
	// in the catalog so the FK update doesn't surface as a generic 500.
	// Empty string is treated the same as omitted — clients sometimes send
	// "" for a not-yet-picked field.
	var spriteID string
	if req.SpriteID != nil {
		spriteID = strings.TrimSpace(*req.SpriteID)
	}
	if spriteID != "" {
		var spriteCount int
		if err := app.DB.QueryRow(r.Context(),
			`SELECT COUNT(*) FROM npc_sprite WHERE id = $1`, spriteID,
		).Scan(&spriteCount); err != nil || spriteCount == 0 {
			jsonError(w, "Unknown sprite_id", http.StatusBadRequest)
			return
		}
	}

	// Travelers lodge at the Inn. The Inn tag identifies multi-tenant
	// lodging (distinct from a tavern, which is a workplace whose
	// keeper happens to live above the bar). Falls back to tavern if
	// no inn is placed — historically taverns and inns were often the
	// same establishment ('ordinary'), so a tavern-only village still
	// makes sense.
	var homeID sql.NullString
	var homeX, homeY sql.NullFloat64
	if err := app.DB.QueryRow(r.Context(),
		`SELECT o.id::text, o.x, o.y
		   FROM village_object o
		   JOIN village_object_tag vot ON vot.object_id = o.id
		  WHERE vot.tag IN ('inn', 'tavern')
		  ORDER BY (CASE WHEN vot.tag = 'inn' THEN 0 ELSE 1 END), o.created_at ASC
		  LIMIT 1`,
	).Scan(&homeID, &homeX, &homeY); err != nil && err != sql.ErrNoRows {
		log.Printf("pc/create lodging lookup: %v", err)
	}

	// Default starting position: the home tavern's anchor, or (0,0)
	// when no tavern is placed yet (test environments).
	var startX, startY float64
	if homeX.Valid {
		startX = homeX.Float64
		startY = homeY.Float64
	}

	// Upsert. ON CONFLICT lets re-runs update display_name without
	// disturbing position. Post-ZBBS-084: display_name is the unified
	// in-world identity column (was character_name on pc_position),
	// current_x/current_y are the position columns (were x/y).
	//
	// sprite_id COALESCE: when the request supplied a sprite, the new
	// value wins; when it didn't, the existing value (if any) survives
	// the upsert. NULLIF($6, '')::uuid converts the empty fast-path to
	// SQL NULL so the COALESCE picks up the existing column.
	var actorID, prevSpriteID sql.NullString
	if err := app.DB.QueryRow(r.Context(),
		`INSERT INTO actor (login_username, display_name, current_x, current_y, home_structure_id, sprite_id)
		 VALUES ($1, $2, $3, $4, NULLIF($5, '')::uuid, NULLIF($6, '')::uuid)
		 ON CONFLICT (login_username) DO UPDATE
		   SET display_name = EXCLUDED.display_name,
		       home_structure_id = COALESCE(EXCLUDED.home_structure_id, actor.home_structure_id),
		       sprite_id = COALESCE(EXCLUDED.sprite_id, actor.sprite_id)
		 RETURNING id::text, sprite_id::text`,
		user.Username, req.CharacterName, startX, startY,
		homeStringValue(homeID), spriteID,
	).Scan(&actorID, &prevSpriteID); err != nil {
		log.Printf("pc/create insert: %v", err)
		jsonError(w, "Failed to create PC", http.StatusInternalServerError)
		return
	}

	// Broadcast pc_appeared when the create landed a sprite (either fresh
	// or re-set). Other connected clients render the PC from this single
	// event. Skipped when sprite_id is still null — nothing to render.
	if prevSpriteID.Valid {
		app.broadcastPCAppeared(r.Context(), actorID.String, prevSpriteID.String, req.CharacterName)
	}

	log.Printf("pc/create %s -> '%s' (home tavern %v, sprite %v)", user.Username, req.CharacterName, homeID.String, prevSpriteID.String)
	w.WriteHeader(http.StatusNoContent)
}

// handlePCSprite — set or change the PC's render sprite. Same shape as
// the admin-only handleSetNPCSprite, but scoped to the authenticated
// player's own row (no admin role required, no path id — the session
// identifies which actor to update).
//
// Body: {sprite_id}. Validates the sprite exists, updates actor, and
// broadcasts pc_sprite_changed so every connected client re-renders the
// PC without a follow-up fetch.
func (app *App) handlePCSprite(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
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

	// Catalog FK pre-check so a typo returns 400 not 500.
	var spriteCount int
	if err := app.DB.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM npc_sprite WHERE id = $1`, req.SpriteID,
	).Scan(&spriteCount); err != nil || spriteCount == 0 {
		jsonError(w, "Unknown sprite_id", http.StatusBadRequest)
		return
	}

	// Update + return id + display_name in one round-trip so the broadcast
	// payload is complete without a second query. Errors on missing PC row
	// (player called /sprite before /create) — the bootstrap should always
	// /create first, so this surfaces a client-side bug rather than
	// silently no-op'ing.
	var actorID, charName sql.NullString
	if err := app.DB.QueryRow(r.Context(),
		`UPDATE actor SET sprite_id = $1
		 WHERE login_username = $2
		 RETURNING id::text, display_name`,
		req.SpriteID, user.Username,
	).Scan(&actorID, &charName); err != nil {
		if err == sql.ErrNoRows {
			jsonError(w, "PC not found — call /pc/create first", http.StatusNotFound)
			return
		}
		log.Printf("pc/sprite update: %v", err)
		jsonError(w, "Failed to update sprite", http.StatusInternalServerError)
		return
	}

	app.broadcastPCAppeared(r.Context(), actorID.String, req.SpriteID, charName.String)
	log.Printf("pc/sprite %s -> %s", user.Username, req.SpriteID)
	w.WriteHeader(http.StatusNoContent)
}

// handlePCMove — click-to-walk endpoint for the village viewer. Body:
// {target_x, target_y, speed?, target_structure_id?}. Resolves the
// session to the PC's actor.id, then walks. Two modes:
//
//   - Raw coords: {target_x, target_y}. Walk to that tile, no inside
//     flip on arrival. Used when the click lands on open ground.
//
//   - Structure: {target_structure_id}. Walk to the structure's door
//     (entry allowed) or loiter slot (knocked or no-entry). On arrival,
//     setNPCInside fires only when the policy permits this PC to enter
//     so the PC joins the scene_huddle and the talk panel can open.
//     Owner-only structures the PC isn't associated with resolve as a
//     knock — the response carries knock_narration the client renders
//     in the talk panel. Used when the client hit-detects a structure
//     under the click. target_x/y are ignored when target_structure_id
//     is set.
//
// Structure mode routes through startReturnWalk so the existing
// arrival-hook (advanceBehavior) handles the inside flip — same path
// NPC scheduler arrivals take. Raw mode stays on startNPCWalk since
// no post-arrival inside flip is needed.
func (app *App) handlePCMove(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	var req struct {
		TargetX           float64 `json:"target_x"`
		TargetY           float64 `json:"target_y"`
		Speed             float64 `json:"speed,omitempty"`
		TargetStructureID string  `json:"target_structure_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	var actorID string
	var curX, curY float64
	var pcInside bool
	var pcInsideID sql.NullString
	var pcCurrentHuddle sql.NullString
	var pcDisplayName string
	if err := app.DB.QueryRow(r.Context(),
		`SELECT id::text, current_x, current_y, inside,
		        inside_structure_id::text,
		        current_huddle_id::text,
		        display_name
		   FROM actor WHERE login_username = $1`,
		user.Username,
	).Scan(&actorID, &curX, &curY, &pcInside, &pcInsideID, &pcCurrentHuddle, &pcDisplayName); err != nil {
		if err == sql.ErrNoRows {
			jsonError(w, "PC not found — call /pc/create first", http.StatusNotFound)
			return
		}
		log.Printf("pc/move actor lookup: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Departure narration. If the PC is currently inside a structure and
	// is about to walk somewhere else, broadcast a room_event of kind
	// "departure" to the room they're leaving. Symmetric with the agent
	// move_to commit's departure broadcast in executeAgentCommit. Without
	// this, a PC walking out of the tavern leaves no trace in the room
	// log other agents (and other PCs in the same room) can see.
	if pcInside && pcInsideID.Valid {
		structName := app.lookupStructureName(r.Context(), pcInsideID.String)
		if structName == "" {
			structName = "the building"
		}
		text := fmt.Sprintf("%s left the %s.", pcDisplayName, structName)
		app.Hub.Broadcast(WorldEvent{
			Type: "room_event",
			Data: map[string]interface{}{
				"actor_id":     actorID,
				"actor_name":   pcDisplayName,
				"kind":         "departure",
				"text":         text,
				"structure_id": pcInsideID.String,
				"at":           time.Now().UTC().Format(time.RFC3339),
			},
		})
	}

	// Service-huddle cleanup (ZBBS-101). A PC who isn't physically inside
	// any structure but holds a current_huddle_id is in a "service huddle"
	// joined via knock — clear it on every new walk so the conversation
	// dissolves when the PC chooses to walk away. PC moves that arrive
	// inside a structure (entry_policy='anyone' or owner-self) re-form
	// the huddle through the existing setNPCInside path on arrival, so
	// this cleanup doesn't tear down a normal indoor huddle.
	if !pcInside && pcCurrentHuddle.Valid {
		app.leaveHuddleForPC(r.Context(), user.Username)
	}

	speed := req.Speed
	if speed <= 0 {
		speed = defaultNPCSpeed
	}

	// Structure mode: resolve the click to a door tile (enter) or a
	// loiter slot (stand outside / knock). Resolution by entry_policy
	// (ZBBS-101):
	//   - 'none'   → loiter slot, no inside flip.
	//   - 'anyone' → door tile, inside flip on arrival.
	//   - 'owner'  → if the PC's actor has this structure as home or
	//                work, treat as 'anyone' (door + enter). Otherwise
	//                walk to the loiter slot; the response carries
	//                knocked=true so the client can render the knock
	//                affordance.
	if req.TargetStructureID != "" {
		var ox, oy float64
		var loiterX, loiterY sql.NullInt32
		var doorX, doorY sql.NullInt32
		var footprintBottom int
		var entryPolicy string
		err := app.DB.QueryRow(r.Context(),
			`SELECT o.x, o.y,
			        o.loiter_offset_x, o.loiter_offset_y,
			        a.door_offset_x, a.door_offset_y, a.footprint_bottom,
			        o.entry_policy
			   FROM village_object o
			   JOIN asset a ON a.id = o.asset_id
			  WHERE o.id::text = $1`,
			req.TargetStructureID,
		).Scan(&ox, &oy, &loiterX, &loiterY, &doorX, &doorY, &footprintBottom, &entryPolicy)
		if err != nil {
			if err == sql.ErrNoRows {
				jsonError(w, "Structure not found", http.StatusNotFound)
				return
			}
			log.Printf("pc/move structure lookup: %v", err)
			jsonError(w, "Internal error", http.StatusInternalServerError)
			return
		}

		// Owner check for 'owner' policy. The same actor.id linkage
		// (home_structure_id / work_structure_id) is used for NPCs in
		// agentMoveShouldEnter — keep the rule single-sourced in
		// concept even though the queries are duplicated for context.
		isAssociated := false
		if entryPolicy == "owner" {
			var n int
			if err := app.DB.QueryRow(r.Context(),
				`SELECT COUNT(*) FROM actor
				  WHERE id::text = $1
				    AND (home_structure_id::text = $2 OR work_structure_id::text = $2)`,
				actorID, req.TargetStructureID,
			).Scan(&n); err != nil {
				log.Printf("pc/move owner check: %v", err)
				jsonError(w, "Internal error", http.StatusInternalServerError)
				return
			}
			isAssociated = n > 0
		}

		const tileSize = 32.0
		var walkX, walkY float64
		canEnter := (entryPolicy == "anyone" || (entryPolicy == "owner" && isAssociated)) && doorX.Valid && doorY.Valid
		knocked := entryPolicy == "owner" && !isAssociated
		log.Printf("knock-trace pc=%s structure=%s entry_policy=%s isAssociated=%v canEnter=%v knocked=%v",
			actorID, req.TargetStructureID, entryPolicy, isAssociated, canEnter, knocked)
		if canEnter {
			walkX = ox + float64(doorX.Int32)*tileSize
			walkY = oy + float64(doorY.Int32)*tileSize
		} else {
			lx, ly := effectiveLoiterTile(loiterX, loiterY, doorX, doorY, footprintBottom)
			walkX, walkY = app.pickVisitorSlot(r.Context(), actorID, ox, oy, lx, ly)
		}

		npc := &behaviorNPC{ID: actorID, CurX: curX, CurY: curY}
		if err := app.startReturnWalk(r.Context(), npc, walkX, walkY, req.TargetStructureID, "pc-move", canEnter); err != nil {
			if err.Error() == "no path" {
				jsonError(w, "No path to target", http.StatusBadRequest)
				return
			}
			log.Printf("pc/move structure walk: %v", err)
			jsonError(w, "Internal error", http.StatusInternalServerError)
			return
		}

		// Knock (ZBBS-101): PC clicked an owner-only structure they don't
		// belong to. Compose narration now and, when an associated NPC is
		// currently inside, join the PC into the structure's scene_huddle
		// so the talk panel becomes visible with the vendor as an
		// addressee. The PC stays physically outside (inside=false) — the
		// huddle is the conversational scope, not a presence flip. PC's
		// next /pc/move dissolves it via the service-huddle cleanup at
		// the top of this handler.
		var knockNarration string
		var knockHuddleJoined bool
		if knocked {
			var insideAssociated int
			if err := app.DB.QueryRow(r.Context(),
				`SELECT COUNT(*) FROM actor
				  WHERE inside = true
				    AND inside_structure_id::text = $1
				    AND (home_structure_id::text = $1 OR work_structure_id::text = $1)`,
				req.TargetStructureID,
			).Scan(&insideAssociated); err != nil {
				log.Printf("pc/move knock huddle precheck: %v", err)
			} else if insideAssociated > 0 {
				log.Printf("knock-trace precheck pc=%s structure=%s insideAssociated=%d (proceed to join)", actorID, req.TargetStructureID, insideAssociated)
				if huddleID, err := app.joinOrCreateHuddleForPC(r.Context(), user.Username, req.TargetStructureID); err != nil {
					log.Printf("pc/move knock huddle join: %v", err)
				} else {
					knockHuddleJoined = true
					log.Printf("knock-trace huddle-joined pc=%s structure=%s huddle=%s", actorID, req.TargetStructureID, huddleID)
					// Sync any inside-the-structure actors into this huddle.
					// Covers two cases: (1) the historical pgx.ErrNoRows bug
					// prevented joinOrCreateHuddle from creating huddles for
					// NPCs who entered before the fix — they sit inside with
					// current_huddle_id=NULL and need adoption now;
					// (2) defense in depth so the talk-panel huddle_members
					// query is never empty when a vendor is physically there.
					if _, err := app.DB.Exec(r.Context(),
						`UPDATE actor SET current_huddle_id = $1::uuid
						  WHERE inside = true
						    AND inside_structure_id::text = $2
						    AND (current_huddle_id IS NULL OR current_huddle_id::text != $1)`,
						huddleID, req.TargetStructureID,
					); err != nil {
						log.Printf("pc/move knock huddle sync inside actors: %v", err)
					}
					app.fireKnockPerception(r.Context(), actorID, huddleID, req.TargetStructureID)
				}
			}
			if !knockHuddleJoined {
				log.Printf("knock-trace no-huddle pc=%s structure=%s (insideAssociated=0 or join failed) — narration only", actorID, req.TargetStructureID)
				knockNarration = app.composeKnockNarration(r.Context(), req.TargetStructureID)
			}
		}

		jsonResponse(w, http.StatusOK, map[string]any{
			"ok":              true,
			"structure":       true,
			"knocked":         knocked,
			"knock_narration": knockNarration,
			"huddle_joined":   knockHuddleJoined,
		})
		return
	}

	// Raw-coord mode (clicking on open ground): walk to the tile, no
	// arrival inside-flip. startNPCWalk surfaces typed-string errors
	// that the HTTP layer translates to user-readable codes.
	result, err := app.startNPCWalk(r.Context(), actorID, req.TargetX, req.TargetY, speed)
	if err != nil {
		if err.Error() == "npc not found" {
			jsonError(w, "PC actor missing", http.StatusInternalServerError)
			return
		}
		if err.Error() == "no path" {
			jsonError(w, "No path to target", http.StatusBadRequest)
			return
		}
		log.Printf("pc/move walk: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, result)
}

// fireKnockPerception writes an act-style perception row to
// agent_action_log and triggers immediate ticks on every agentized NPC
// inside the structure (typically the lone vendor). The row is shaped
// as an `act` so the perception loader at agent_tick.go:1219 renders
// it as "Mary approaches Ezekiel's Stall and waits at the door" in
// the vendor's next perception block — they get a concrete cue to
// react to without the PC having to type first.
//
// force=true on the immediate tick: PC-initiated, bypass the agent
// cost guard. sceneID is fresh per knock — the cascade is the knock
// itself; subsequent PC speech in the same conversation gets its own
// scene from handlePCSay's existing path.
func (app *App) fireKnockPerception(ctx context.Context, pcActorID, huddleID, structureID string) {
	var pcName, structureName, assetName string
	if err := app.DB.QueryRow(ctx,
		`SELECT a.display_name, COALESCE(o.display_name, ''), ast.name
		   FROM actor a, village_object o JOIN asset ast ON ast.id = o.asset_id
		  WHERE a.id::text = $1 AND o.id::text = $2`,
		pcActorID, structureID,
	).Scan(&pcName, &structureName, &assetName); err != nil {
		log.Printf("knock-perception lookup: %v", err)
		return
	}
	name := structureName
	if name == "" {
		name = assetName
	}

	payload, err := json.Marshal(map[string]interface{}{
		"text":         fmt.Sprintf("approaches %s and waits at the door", name),
		"structure_id": structureID,
	})
	if err != nil {
		log.Printf("knock-perception marshal: %v", err)
		return
	}
	if _, err := app.DB.Exec(ctx,
		`INSERT INTO agent_action_log (actor_id, speaker_name, source, action_type, payload, result, huddle_id)
		 VALUES ($1, $2, 'player', 'act', $3, 'ok', $4::uuid)`,
		pcActorID, pcName, payload, huddleID,
	); err != nil {
		log.Printf("knock-perception audit insert: %v", err)
	}

	sceneID := newUUIDv7()
	log.Printf("knock-trace fireKnockPerception pc=%s structure=%s huddle=%s scene=%s — triggering co-located ticks",
		pcActorID, structureID, huddleID, sceneID)
	app.triggerCoLocatedTicks(ctx, structureID, "", "pc-knocked", true, sceneID, pcActorID)
}

// composeKnockNarration returns talk-panel text for a PC knock that
// produced no live conversation — i.e. nobody associated with the
// structure is currently inside. When someone IS inside, the click
// handler joins the PC into a service huddle instead and the talk
// panel surfaces the vendor as an addressee, so this narration is
// suppressed (returns ""). Only the unattended path needs explanatory
// text.
func (app *App) composeKnockNarration(ctx context.Context, structureID string) string {
	var displayName, assetName string
	if err := app.DB.QueryRow(ctx,
		`SELECT COALESCE(o.display_name, ''), a.name
		   FROM village_object o JOIN asset a ON a.id = o.asset_id
		  WHERE o.id::text = $1`,
		structureID,
	).Scan(&displayName, &assetName); err != nil {
		return ""
	}
	name := displayName
	if name == "" {
		name = assetName
	}

	// Break-aware variant: if a would-be associated NPC (this structure is
	// their home or work) is currently on a take_break (break_until in the
	// future), surface that. ZBBS-102 split break_until from
	// agent_override_until so a routine move_to (which bumps override)
	// doesn't misrepresent the vendor as on break.
	var vendorName sql.NullString
	var breakUntil sql.NullTime
	if err := app.DB.QueryRow(ctx,
		`SELECT a.display_name, a.break_until
		   FROM actor a
		  WHERE (a.home_structure_id::text = $1 OR a.work_structure_id::text = $1)
		    AND a.break_until IS NOT NULL
		    AND a.break_until > NOW()
		  ORDER BY a.break_until DESC
		  LIMIT 1`,
		structureID,
	).Scan(&vendorName, &breakUntil); err == nil && breakUntil.Valid {
		who := strings.TrimSpace(vendorName.String)
		if who == "" {
			who = "the keeper"
		}
		return fmt.Sprintf("%s has stepped away — expected back around %s.",
			who, breakUntil.Time.Local().Format("3:04 PM"))
	}

	return fmt.Sprintf("%s stands unattended. No one is here.", name)
}

// broadcastPCAppeared emits pc_appeared with the full sprite catalog row
// and current world position inlined, so a fresh client can render the
// PC's AnimatedSprite2D without a follow-up REST fetch. Same event fires
// for first-time appearance and subsequent sprite swaps — the client's
// pc_appeared handler treats "already rendered" as a sprite swap and
// "not yet rendered" as a fresh add.
//
// Best-effort: a missing catalog row or position lookup logs and ships
// the partial payload; the next /pc/me poll or WS reconnect resync will
// fill any gaps.
func (app *App) broadcastPCAppeared(ctx context.Context, actorID, spriteID, characterName string) {
	// display_name (rather than character_name) so the broadcast lines up
	// with the npc_created shape that world.gd's add_npc_from_broadcast
	// already consumes — letting one client-side render path handle both.
	data := map[string]interface{}{
		"id":           actorID,
		"sprite_id":    spriteID,
		"display_name": characterName,
	}
	if sprites, err := app.loadNPCSprites(ctx); err == nil {
		if sp, ok := sprites[spriteID]; ok {
			data["sprite"] = sp
		}
	} else {
		log.Printf("broadcastPCAppeared: sprite catalog load failed: %v", err)
	}
	// Position + presence so a fresh client renders at the right tile
	// with the right facing on the first frame. inside_structure_id
	// drives the inside-hide logic in world.gd's NPC renderer.
	var x, y float64
	var facing string
	var inside bool
	var insideID sql.NullString
	if err := app.DB.QueryRow(ctx,
		`SELECT current_x, current_y, facing, inside, inside_structure_id::text
		   FROM actor WHERE id = $1`,
		actorID,
	).Scan(&x, &y, &facing, &inside, &insideID); err == nil {
		data["current_x"] = x
		data["current_y"] = y
		data["facing"] = facing
		data["inside"] = inside
		if insideID.Valid {
			data["inside_structure_id"] = insideID.String
		}
	} else {
		log.Printf("broadcastPCAppeared: position lookup failed: %v", err)
	}
	app.Hub.Broadcast(WorldEvent{Type: "pc_appeared", Data: data})
}

func homeStringValue(s sql.NullString) string {
	if s.Valid {
		return s.String
	}
	return ""
}

// handlePCSay — addressed speech. Two writes happen:
//   1. /v1/chat/send to the addressee for the inline LLM reply.
//   2. agent_action_log + WS broadcast so others in the room
//      overhear (same path as /pc/speak's broadcast).
// This is NOT a private whisper — that name was misleading. It's
// directed in-room speech.
func (app *App) handlePCSay(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	var req struct {
		Target string `json:"target"`
		Text   string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Target == "" || req.Text == "" {
		jsonError(w, "target and text are required", http.StatusBadRequest)
		return
	}

	// Verify the target is an agentized NPC. Non-agent villagers (npc rows
	// without llm_memory_agent) are physically present but conversationally
	// invisible — addressing them silently fails because there's no virtual
	// agent on the memory-api side to receive the chat.
	var hasAgent bool
	if err := app.DB.QueryRow(r.Context(),
		`SELECT EXISTS(SELECT 1 FROM actor WHERE llm_memory_agent = $1)`,
		req.Target,
	).Scan(&hasAgent); err != nil {
		log.Printf("pc/say target check: %v", err)
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if !hasAgent {
		jsonError(w, "target is not addressable", http.StatusBadRequest)
		return
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		jsonError(w, "Missing Authorization header", http.StatusUnauthorized)
		return
	}

	// Look up PC's actor.id + display_name + structure for the audit-log
	// overhear. After ZBBS-084 the PC has its own actor row, so the audit
	// trail can record actor_id properly (was NULL pre-refactor when PCs
	// lived in pc_position and weren't rows in npc).
	var actorID, charName, structureID sql.NullString
	if err := app.DB.QueryRow(r.Context(),
		`SELECT id::text, display_name, inside_structure_id::text
		   FROM actor WHERE login_username = $1`,
		user.Username,
	).Scan(&actorID, &charName, &structureID); err != nil {
		log.Printf("pc/say lookup actor: %v", err)
	}

	// Audit-log entry for the room to overhear. Includes target_name in
	// payload so future perception passes can render "Jefferey said to
	// John Ellis: '...'" instead of an unaddressed line. Best-effort:
	// failure here doesn't block the chat.
	if charName.Valid && structureID.Valid {
		audit, _ := json.Marshal(map[string]interface{}{
			"text":         req.Text,
			"structure_id": structureID.String,
			"target_name":  req.Target,
		})
		if _, err := app.DB.Exec(r.Context(),
			`INSERT INTO agent_action_log (actor_id, speaker_name, source, action_type, payload, result, huddle_id)
			 VALUES ($1, $2, 'player', 'speak', $3, 'ok',
			         (SELECT current_huddle_id FROM actor WHERE id = $1))`,
			actorID, charName.String, audit,
		); err != nil {
			log.Printf("pc/say audit insert: %v", err)
		}
		app.Hub.Broadcast(WorldEvent{
			Type: "npc_spoke",
			Data: map[string]interface{}{
				"npc_id": actorID,
				"name":   charName.String,
				"text":   req.Text,
				"at":     time.Now().UTC().Format(time.RFC3339),
				"kind":   "pc",
			},
		})
	}

	// from_agent is required for user-session auth (not auto-derived
	// like it is for agent API key auth). Set it to the authenticated
	// user's actor name so the chat is recorded as from the player.
	body, _ := json.Marshal(map[string]interface{}{
		"from_agent": user.Username,
		"to_agents":  []string{req.Target},
		"message":    req.Text,
		"wait":       true,
	})

	upstreamURL := strings.TrimRight(app.LLMMemoryURL, "/") + "/v1/chat/send"
	httpReq, err := http.NewRequestWithContext(r.Context(), "POST", upstreamURL, bytes.NewReader(body))
	if err != nil {
		jsonError(w, "Internal error", http.StatusInternalServerError)
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", authHeader)

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		jsonError(w, "Upstream chat unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBytes)

	log.Printf("pc/say %s -> %s: %.60q (status %d)", user.Username, req.Target, req.Text, resp.StatusCode)
}

// handlePCSpeak — broadcast to everyone in the PC's current huddle.
// Records as agent_action_log row with source='player', action_type='speak',
// speaker_name=character_name. Triggers event-tick on co-located
// agentized NPCs (subject to 5-min cost guard).
func (app *App) handlePCSpeak(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Text == "" {
		jsonError(w, "text is required", http.StatusBadRequest)
		return
	}

	var actorID, charName, structureID sql.NullString
	err := app.DB.QueryRow(r.Context(),
		`SELECT pc.id::text, pc.display_name, pc.inside_structure_id::text
		   FROM actor pc
		  WHERE pc.login_username = $1 AND pc.current_huddle_id IS NOT NULL`,
		user.Username,
	).Scan(&actorID, &charName, &structureID)
	if err != nil || !structureID.Valid || !charName.Valid {
		jsonError(w, "Not in a huddle — nobody to hear you", http.StatusBadRequest)
		return
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"text":         req.Text,
		"structure_id": structureID.String,
	})

	if _, err := app.DB.Exec(r.Context(),
		`INSERT INTO agent_action_log (actor_id, speaker_name, source, action_type, payload, result, huddle_id)
		 VALUES ($1, $2, 'player', 'speak', $3, 'ok',
		         (SELECT current_huddle_id FROM actor WHERE id = $1))`,
		actorID, charName.String, payload,
	); err != nil {
		log.Printf("pc/speak audit insert: %v", err)
		jsonError(w, "Failed to log speech", http.StatusInternalServerError)
		return
	}

	app.Hub.Broadcast(WorldEvent{
		Type: "npc_spoke",
		Data: map[string]interface{}{
			"npc_id": actorID,
			"name":   charName.String,
			"text":   req.Text,
			"at":     time.Now().UTC().Format(time.RFC3339),
			"kind":   "pc",
		},
	})

	// PC-initiated → force=true so cost guard doesn't suppress
	// reactions. Storm risk is bounded by human typing speed.
	//
	// Cascade origin (MEM-121): mint a fresh scene UUID. Every NPC's
	// reaction tick to this PC speech, every nested speak fan-out from
	// those ticks, will inherit the same UUID. Walks initiated during
	// reactions don't carry it forward — when the NPC arrives somewhere
	// later, that arrival is its own new scene.
	app.triggerCoLocatedTicks(context.Background(), structureID.String, "", fmt.Sprintf("pc-spoke (%s)", charName.String), true, newUUIDv7(), actorID.String)

	// Cascade origin — fire the chronicler alongside the co-located
	// reactor ticks. Fire-and-forget; chronicler runs in a goroutine
	// so it doesn't block the WebSocket response. Only fires once per
	// scene-start (here), not for in-cascade NPC reactions.
	app.cascadeOriginFireChronicler(fmt.Sprintf("pc-spoke (%s)", charName.String), structureID.String)

	w.WriteHeader(http.StatusNoContent)
}
