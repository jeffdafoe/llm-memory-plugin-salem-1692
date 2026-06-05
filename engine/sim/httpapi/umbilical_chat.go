package httpapi

import (
	"net/http"
	"strconv"
	"time"
)

// umbilical_chat.go — GET /api/village/umbilical/chat, ZBBS-HOME-382.
//
// Returns one scene's engine<->model chat exchange — the rendered perception the
// engine SENT (tx) and the model's RESPONSES + tool calls it got back (rx) —
// straight off the per-scene chat ring (engine/sim/chatlog). This is the
// operator's window into "what was this NPC told, and what did it say back,"
// without logging into llm-memory's Communications->Chat (which can't even
// filter by scene). Operator-gated like every umbilical route. Chat capture is
// on only when the umbilical is enabled (cmd/engine wires the ring as the
// harness ChatSink); when off, s.chat is nil and this returns an empty list
// rather than erroring.

// ChatRecordDTO is one captured exchange entry on the wire.
type ChatRecordDTO struct {
	At        time.Time `json:"at"`
	SceneID   string    `json:"scene_id"`
	ActorID   string    `json:"actor_id"`
	AttemptID string    `json:"attempt_id"`
	Model     string    `json:"model"`
	Direction string    `json:"direction"` // "perception" (tx) or "response" (rx)
	Content   string    `json:"content"`
	ToolCalls string    `json:"tool_calls,omitempty"`
}

// UmbilicalChatDTO is the GET /umbilical/chat response.
type UmbilicalChatDTO struct {
	ContractVersion int             `json:"contract_version"`
	Scene           string          `json:"scene"`
	Returned        int             `json:"returned"`
	Messages        []ChatRecordDTO `json:"messages"`
}

// handleUmbilicalChat serves one scene's exchange, oldest first. Query param
// `scene` (required); `limit` (optional, <=0 or absent -> all retained for that
// scene). 400 missing scene, 200 otherwise (unknown scene -> an empty list, not
// 404 — the ring simply has nothing for it).
func (s *Server) handleUmbilicalChat(w http.ResponseWriter, r *http.Request) {
	scene := r.URL.Query().Get("scene")
	if scene == "" {
		writeError(w, http.StatusBadRequest, "scene is required")
		return
	}
	limit := 0 // 0 -> all retained (Recent treats <=0 as "everything")
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}

	out := UmbilicalChatDTO{
		ContractVersion: ContractVersion,
		Scene:           scene,
		Messages:        make([]ChatRecordDTO, 0),
	}
	// s.chat is nil when chat capture wasn't wired (umbilical read-only without
	// the ring) — report an empty list rather than failing.
	if s.chat != nil {
		for _, rec := range s.chat.Recent(scene, limit) {
			out.Messages = append(out.Messages, ChatRecordDTO{
				At:        rec.At,
				SceneID:   rec.SceneID,
				ActorID:   string(rec.ActorID),
				AttemptID: string(rec.AttemptID),
				Model:     rec.Model,
				Direction: rec.Direction,
				Content:   rec.Content,
				ToolCalls: rec.ToolCalls,
			})
		}
	}
	out.Returned = len(out.Messages)
	writeJSON(w, out)
}
