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
//                                     members. Returns exists=false
//                                     when the PC has never been
//                                     created (talk panel shows the
//                                     create-character modal).
//   - POST /api/village/pc/create  → first-time creation. Body:
//                                     {character_name}. Auto-assigns
//                                     home to the nearest tavern.
//                                     Idempotent on the login_username
//                                     (re-running updates the name).
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
}

type pcHuddleMember struct {
	Kind        string  `json:"kind"` // "npc" or "pc"
	Name        string  `json:"name"` // display_name (NPC) or character_name (PC)
	Role        *string `json:"role,omitempty"`
	TargetAgent *string `json:"target_agent,omitempty"` // llm_memory_agent for NPCs (chat_send recipient)
}

// pcRecentSpeech is one historical speak event at the player's current
// inside_structure_id, surfaced so the talk panel can backload room
// context when opened. The room metaphor: walk in, you hear the
// last 12 things said here in the past 24 hours.
//
// Kind mirrors the WS npc_spoke event's kind ("npc" | "player") so the
// client can render player-vs-NPC color treatment consistently between
// backload and live stream.
type pcRecentSpeech struct {
	SpeakerName string    `json:"speaker_name"`
	Text        string    `json:"text"`
	Kind        string    `json:"kind"`        // "npc" | "player"
	OccurredAt  time.Time `json:"occurred_at"` // wall-clock
}

const pcRecentSpeechLimit = 12
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
	var charName, insideID, huddleID, structureName, homeID, homeName sql.NullString
	err := app.DB.QueryRow(r.Context(),
		`SELECT pc.display_name, pc.current_x, pc.current_y,
		        pc.inside_structure_id::text,
		        pc.current_huddle_id::text,
		        COALESCE(o.display_name, a.name) AS structure_name,
		        pc.home_structure_id::text,
		        COALESCE(ho.display_name, ha.name) AS home_name
		   FROM actor pc
		   LEFT JOIN village_object o ON o.id = pc.inside_structure_id
		   LEFT JOIN asset a ON a.id = o.asset_id
		   LEFT JOIN village_object ho ON ho.id = pc.home_structure_id
		   LEFT JOIN asset ha ON ha.id = ho.asset_id
		  WHERE pc.login_username = $1`,
		user.Username,
	).Scan(&charName, &x, &y, &insideID, &huddleID, &structureName, &homeID, &homeName)
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

// loadRecentSpeechAtStructure pulls the last N speak events whose payload
// references this structure_id. Reads from agent_action_log.payload's
// embedded structure_id (the speak commit stashes inside_structure_id
// there at write time — see agent_tick.go's executeAgentCommit).
//
// Filters to action_type='speak' and result='ok' so rejected/empty speech
// attempts don't pollute the room's memory. source='player' rows render
// as kind='player' for the talk panel's color treatment; everything else
// is kind='npc' (agent ticks, magistrate-driven speech if it ever lands).
func (app *App) loadRecentSpeechAtStructure(ctx context.Context, structureID string) []pcRecentSpeech {
	cutoff := time.Now().Add(-pcRecentSpeechCutoff)
	rows, err := app.DB.Query(ctx, `
		SELECT speaker_name, payload->>'text' AS text, source, occurred_at
		FROM agent_action_log
		WHERE action_type = 'speak'
		  AND result = 'ok'
		  AND payload->>'structure_id' = $1
		  AND occurred_at > $2
		ORDER BY occurred_at DESC
		LIMIT $3
	`, structureID, cutoff, pcRecentSpeechLimit)
	if err != nil {
		log.Printf("recent speech: %v", err)
		return nil
	}
	defer rows.Close()

	// Collect newest-first, then reverse for natural reading order.
	var recent []pcRecentSpeech
	for rows.Next() {
		var r pcRecentSpeech
		var source string
		if err := rows.Scan(&r.SpeakerName, &r.Text, &source, &r.OccurredAt); err != nil {
			continue
		}
		if source == "player" {
			r.Kind = "player"
		} else {
			r.Kind = "npc"
		}
		recent = append(recent, r)
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
		CharacterName string `json:"character_name"`
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
	if _, err := app.DB.Exec(r.Context(),
		`INSERT INTO actor (login_username, display_name, current_x, current_y, home_structure_id)
		 VALUES ($1, $2, $3, $4, NULLIF($5, '')::uuid)
		 ON CONFLICT (login_username) DO UPDATE
		   SET display_name = EXCLUDED.display_name,
		       home_structure_id = COALESCE(EXCLUDED.home_structure_id, actor.home_structure_id)`,
		user.Username, req.CharacterName, startX, startY,
		homeStringValue(homeID),
	); err != nil {
		log.Printf("pc/create insert: %v", err)
		jsonError(w, "Failed to create PC", http.StatusInternalServerError)
		return
	}

	log.Printf("pc/create %s -> '%s' (home tavern %v)", user.Username, req.CharacterName, homeID.String)
	w.WriteHeader(http.StatusNoContent)
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
			`INSERT INTO agent_action_log (actor_id, speaker_name, source, action_type, payload, result)
			 VALUES ($1, $2, 'player', 'speak', $3, 'ok')`,
			actorID, charName.String, audit,
		); err != nil {
			log.Printf("pc/say audit insert: %v", err)
		}
		app.Hub.Broadcast(WorldEvent{
			Type: "npc_spoke",
			Data: map[string]interface{}{
				"name": charName.String,
				"text": req.Text,
				"at":   time.Now().UTC().Format(time.RFC3339),
				"kind": "pc",
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
		`INSERT INTO agent_action_log (actor_id, speaker_name, source, action_type, payload, result)
		 VALUES ($1, $2, 'player', 'speak', $3, 'ok')`,
		actorID, charName.String, payload,
	); err != nil {
		log.Printf("pc/speak audit insert: %v", err)
		jsonError(w, "Failed to log speech", http.StatusInternalServerError)
		return
	}

	app.Hub.Broadcast(WorldEvent{
		Type: "npc_spoke",
		Data: map[string]interface{}{
			"name": charName.String,
			"text": req.Text,
			"at":   time.Now().UTC().Format(time.RFC3339),
			"kind": "pc",
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
	app.triggerCoLocatedTicks(context.Background(), structureID.String, "", fmt.Sprintf("pc-spoke (%s)", charName.String), true, newUUIDv7())

	// Cascade origin — fire the chronicler alongside the co-located
	// reactor ticks. Fire-and-forget; chronicler runs in a goroutine
	// so it doesn't block the WebSocket response. Only fires once per
	// scene-start (here), not for in-cascade NPC reactions.
	app.cascadeOriginFireChronicler(fmt.Sprintf("pc-spoke (%s)", charName.String), structureID.String)

	w.WriteHeader(http.StatusNoContent)
}
