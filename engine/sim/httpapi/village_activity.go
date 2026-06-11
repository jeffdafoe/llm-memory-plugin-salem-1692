package httpapi

// village_activity.go — ZBBS-WORK-399: feed for the talk panel's admin-only
// Village tab. GET /api/village/activity/recent serves a tail of the world's
// committed action log (lock-free read off the published snapshot) rendered
// as ready-to-display lines.
//
// Gate: requireOperator at the mux (plugins/administer) — the same capability
// pc/me's can_edit mirrors, so tab visibility and route access share one
// trust signal. Deliberately NOT adminCommand/actor.IsAdmin: this is a
// snapshot read that never touches the world goroutine, and keying it off
// can_edit's capability keeps "you can see the tab" and "the tab loads"
// inseparable.
//
// Distinct from the umbilical /actions view: that one is raw entries for
// work's remote console and registers only when the umbilical is enabled.
// This route is always registered and serves prerendered prose for the
// in-client troubleshooting lens. Content notes inherited from the log's
// design (action_log.go): happy-path-only (rejections never land — raw turns
// via the umbilical stay the deep lens) and in-memory (restart clears it).

import (
	"net/http"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// VillageActivityEntryDTO is one rendered action-log row. Line is always
// non-empty and ready to display; the raw fields ride along so the client can
// style by kind / action_type without re-parsing prose.
type VillageActivityEntryDTO struct {
	OccurredAt time.Time `json:"occurred_at"`
	ActorID    string    `json:"actor_id"`
	ActorName  string    `json:"actor_name,omitempty"` // empty when the actor no longer resolves in the snapshot
	ActionType string    `json:"action_type"`
	Kind       string    `json:"kind"` // speech_npc | speech_player | act | raw
	Line       string    `json:"line"`
}

// VillageActivityDTO is the GET /api/village/activity/recent response:
// chronological (oldest-first) tail of the committed-action log. Total is the
// full log size before the since/limit cuts; Returned is what this response
// carries.
type VillageActivityDTO struct {
	ContractVersion int                       `json:"contract_version"`
	Total           int                       `json:"total"`
	Returned        int                       `json:"returned"`
	Entries         []VillageActivityEntryDTO `json:"entries"`
}

// handleVillageActivity serves the Village tab's poll. Query params: `since`
// (optional RFC3339 — only entries strictly after it, so the client can poll
// incrementally off its newest rendered occurred_at), `limit` (optional —
// same default/cap as the umbilical actions view via parseActionsLimit).
func (s *Server) handleVillageActivity(w http.ResponseWriter, r *http.Request) {
	snap := s.world.Published()
	if snap == nil {
		writeJSON(w, VillageActivityDTO{ContractVersion: ContractVersion, Entries: []VillageActivityEntryDTO{}})
		return
	}
	log := snap.ActionLog
	total := len(log)

	q := r.URL.Query()
	if raw := q.Get("since"); raw != "" {
		cutoff, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be RFC3339")
			return
		}
		filtered := make([]sim.ActionLogEntry, 0, len(log))
		for _, e := range log {
			if e.OccurredAt.After(cutoff) {
				filtered = append(filtered, e)
			}
		}
		log = filtered
	}

	limit := parseActionsLimit(q.Get("limit"))
	if len(log) > limit {
		log = log[len(log)-limit:]
	}

	out := VillageActivityDTO{
		ContractVersion: ContractVersion,
		Total:           total,
		Returned:        len(log),
		Entries:         make([]VillageActivityEntryDTO, 0, len(log)),
	}
	for i := range log {
		out.Entries = append(out.Entries, villageActivityEntry(snap, log[i]))
	}
	writeJSON(w, out)
}

// villageActivityEntry renders one log entry for the Village tab. Unlike the
// Room backload (which skips renderActionLogEntry's ok=false cases), a
// troubleshooting view must never drop entries: an actor missing from the
// snapshot renders under its raw id (an orphan id IS the signal an admin is
// looking for), and ActionTypes the talk-panel renderer doesn't phrase
// (stayed_open, summoned, future additions) fall back to a raw
// "name — action_type: text" line with kind "raw".
func villageActivityEntry(snap *sim.Snapshot, e sim.ActionLogEntry) VillageActivityEntryDTO {
	name := ""
	if a := snap.Actors[e.ActorID]; a != nil {
		name = a.DisplayName
	}
	dto := VillageActivityEntryDTO{
		OccurredAt: e.OccurredAt,
		ActorID:    string(e.ActorID),
		ActorName:  name,
		ActionType: string(e.ActionType),
	}

	if speaker, text, kind, ok := renderActionLogEntry(snap, e); ok {
		dto.Kind = kind
		if kind == "act" {
			// Act narration already embeds the actor's name.
			dto.Line = text
		} else {
			dto.Line = speaker + ": " + text
		}
		return dto
	}

	label := name
	if label == "" {
		label = string(e.ActorID)
	}
	dto.Kind = "raw"
	dto.Line = label + " — " + string(e.ActionType)
	if e.Text != "" {
		dto.Line += ": " + e.Text
	}
	return dto
}
