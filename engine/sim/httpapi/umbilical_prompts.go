package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_prompts.go — GET /api/village/umbilical/agent/prompts, ZBBS-HOME-360.
//
// Returns one actor's recent RENDERED DELIBERATION PROMPTS — the raw text the
// harness sent to the model per tick — straight off the per-actor prompt ring
// (engine/sim/promptlog). This is the operator's window into "what did this NPC
// actually perceive when it made that decision," the thing the redacted
// telemetry ring deliberately can't show. Operator-gated like every umbilical
// route. Prompt capture is on only when the umbilical is enabled (cmd/engine
// wires the ring as the harness PromptSink); when off, s.prompts is nil and
// this returns an empty list rather than erroring.

// PromptRecordDTO is one captured prompt on the wire.
type PromptRecordDTO struct {
	At        time.Time `json:"at"`
	ActorID   string    `json:"actor_id"`
	AttemptID string    `json:"attempt_id"`
	Prompt    string    `json:"prompt"`
}

// UmbilicalAgentPromptsDTO is the GET /umbilical/agent/prompts response.
type UmbilicalAgentPromptsDTO struct {
	ContractVersion int               `json:"contract_version"`
	ID              string            `json:"id"`
	Returned        int               `json:"returned"`
	Prompts         []PromptRecordDTO `json:"prompts"`
}

// handleUmbilicalAgentPrompts serves an actor's recent rendered prompts, oldest
// first. Query param `id` (required); `limit` (optional, <=0 or absent → all
// retained for that actor). 400 missing id, 200 otherwise (unknown actor → an
// empty list, not 404 — the ring simply has nothing for it).
func (s *Server) handleUmbilicalAgentPrompts(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	limit := 0 // 0 → all retained (Recent treats <=0 as "everything")
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}

	out := UmbilicalAgentPromptsDTO{
		ContractVersion: ContractVersion,
		ID:              id,
		Prompts:         make([]PromptRecordDTO, 0),
	}
	// s.prompts is nil when prompt capture wasn't wired (umbilical read-only
	// without the ring) — report an empty list rather than failing.
	if s.prompts != nil {
		for _, rec := range s.prompts.Recent(sim.ActorID(id), limit) {
			out.Prompts = append(out.Prompts, PromptRecordDTO{
				At:        rec.At,
				ActorID:   string(rec.ActorID),
				AttemptID: string(rec.AttemptID),
				Prompt:    rec.Prompt,
			})
		}
	}
	out.Returned = len(out.Prompts)
	writeJSON(w, out)
}
