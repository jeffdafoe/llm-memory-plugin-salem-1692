package main

// Player-character (PC) HTTP handlers (M6.7).
//
// PCs are salem-realm llm-memory users who walk around the village,
// join scene huddles alongside NPCs, and converse with them. Their
// presence and position live in pc_position; their identity comes
// from the authenticated session.
//
// Two identities per PC:
//   - actor_name: the llm-memory username, stable system identity.
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
//                                     Idempotent on the actor_name
//                                     (re-running updates the name).
//   - POST /api/village/pc/say     → 1:1 whisper to one NPC. Proxies
//                                     to /v1/chat/send with the user's
//                                     auth header.
//   - POST /api/village/pc/speak   → broadcast to current huddle.
//                                     agent_action_log row with
//                                     speaker_name=character_name,
//                                     source='pc'. Co-located NPCs
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
	ActorName         string             `json:"actor_name"`
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
}

type pcHuddleMember struct {
	Kind        string  `json:"kind"` // "npc" or "pc"
	Name        string  `json:"name"` // display_name (NPC) or character_name (PC)
	Role        *string `json:"role,omitempty"`
	TargetAgent *string `json:"target_agent,omitempty"` // llm_memory_agent for NPCs (chat_send recipient)
}

func (app *App) handlePCMe(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Not authenticated", http.StatusUnauthorized)
		return
	}

	resp := pcMeResponse{
		ActorName:     user.Username,
		HuddleMembers: []pcHuddleMember{},
	}

	var x, y float64
	var charName, insideID, huddleID, structureName, homeID, homeName sql.NullString
	err := app.DB.QueryRow(r.Context(),
		`SELECT pc.character_name, pc.x, pc.y,
		        pc.inside_structure_id::text,
		        pc.current_huddle_id::text,
		        COALESCE(o.display_name, a.name) AS structure_name,
		        pc.home_structure_id::text,
		        COALESCE(ho.display_name, ha.name) AS home_name
		   FROM pc_position pc
		   LEFT JOIN village_object o ON o.id = pc.inside_structure_id
		   LEFT JOIN asset a ON a.id = o.asset_id
		   LEFT JOIN village_object ho ON ho.id = pc.home_structure_id
		   LEFT JOIN asset ha ON ha.id = ho.asset_id
		  WHERE pc.actor_name = $1`,
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
		rows, err := app.DB.Query(r.Context(),
			`SELECT 'npc' AS kind, n.display_name, va.role, n.llm_memory_agent
			   FROM npc n
			   LEFT JOIN village_agent va ON va.llm_memory_agent = n.llm_memory_agent
			  WHERE n.current_huddle_id::text = $1
			 UNION ALL
			 SELECT 'pc' AS kind, pc.character_name, NULL::varchar, NULL::varchar
			   FROM pc_position pc
			  WHERE pc.current_huddle_id::text = $1
			    AND pc.actor_name != $2
			  ORDER BY 2`,
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

	jsonResponse(w, http.StatusOK, resp)
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

	// Upsert. ON CONFLICT lets re-runs update character_name without
	// disturbing position.
	if _, err := app.DB.Exec(r.Context(),
		`INSERT INTO pc_position (actor_name, character_name, x, y, home_structure_id)
		 VALUES ($1, $2, $3, $4, NULLIF($5, '')::uuid)
		 ON CONFLICT (actor_name) DO UPDATE
		   SET character_name = EXCLUDED.character_name,
		       home_structure_id = COALESCE(EXCLUDED.home_structure_id, pc_position.home_structure_id)`,
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

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		jsonError(w, "Missing Authorization header", http.StatusUnauthorized)
		return
	}

	body, _ := json.Marshal(map[string]interface{}{
		"to_agents": []string{req.Target},
		"message":   req.Text,
		"wait":      true,
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
// Records as agent_action_log row with source='pc', action_type='speak',
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

	var charName, structureID sql.NullString
	err := app.DB.QueryRow(r.Context(),
		`SELECT pc.character_name, pc.inside_structure_id::text
		   FROM pc_position pc
		  WHERE pc.actor_name = $1 AND pc.current_huddle_id IS NOT NULL`,
		user.Username,
	).Scan(&charName, &structureID)
	if err != nil || !structureID.Valid || !charName.Valid {
		jsonError(w, "Not in a huddle — nobody to hear you", http.StatusBadRequest)
		return
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"text":         req.Text,
		"structure_id": structureID.String,
	})

	if _, err := app.DB.Exec(r.Context(),
		`INSERT INTO agent_action_log (npc_id, speaker_name, source, action_type, payload, result)
		 VALUES (NULL, $1, 'pc', 'speak', $2, 'ok')`,
		charName.String, payload,
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

	app.triggerCoLocatedTicks(context.Background(), structureID.String, "", fmt.Sprintf("pc-spoke (%s)", charName.String))

	w.WriteHeader(http.StatusNoContent)
}
