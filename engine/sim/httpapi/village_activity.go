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
	"strconv"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// VillageActivityEntryDTO is one rendered action-log row. Line is always
// non-empty and ready to display; the raw fields ride along so the client can
// style by kind / action_type without re-parsing prose. Seq is the paging
// cursor — the client echoes its newest rendered seq back as ?since_seq=.
type VillageActivityEntryDTO struct {
	Seq        uint64    `json:"seq"`
	OccurredAt time.Time `json:"occurred_at"`
	ActorID    string    `json:"actor_id"`
	ActorName  string    `json:"actor_name,omitempty"` // empty when the actor no longer resolves in the snapshot
	ActionType string    `json:"action_type"`
	Kind       string    `json:"kind"` // speech_npc | speech_player | act | raw
	Line       string    `json:"line"`
}

// VillageActivityDTO is the GET /api/village/activity/recent response:
// chronological (oldest-first) slice of the committed-action log. Total is
// the full log size before the since_seq/limit cuts; Returned is what this
// response carries. LatestSeq is the newest seq in the FULL log (0 when
// empty) — a client whose cursor exceeds it knows the seq counter reset
// (engine restart) and must drop its cursor and re-backload.
type VillageActivityDTO struct {
	ContractVersion int                       `json:"contract_version"`
	Total           int                       `json:"total"`
	Returned        int                       `json:"returned"`
	LatestSeq       uint64                    `json:"latest_seq"`
	Entries         []VillageActivityEntryDTO `json:"entries"`
}

// handleVillageActivity serves the Village tab's poll. Query params:
//
//   - `since_seq` (optional uint): only entries with Seq strictly greater.
//     Seq, not a timestamp — OccurredAt is only approximately monotonic and
//     can collide within a world-goroutine batch, which would make a
//     time-based cursor drop same-instant stragglers. Seq is total-ordered
//     by construction.
//   - `limit` (optional — same default/cap as the umbilical actions view via
//     parseActionsLimit). Which end gets cut depends on the mode: a cursor
//     poll keeps the OLDEST limit entries (catch-up — the client converges
//     over successive polls without ever skipping a row), the cursorless
//     backload keeps the NEWEST (an admin opening the tab wants recent
//     activity, not 48h of history).
func (s *Server) handleVillageActivity(w http.ResponseWriter, r *http.Request) {
	snap := s.world.Published()
	if snap == nil {
		writeJSON(w, VillageActivityDTO{ContractVersion: ContractVersion, Entries: []VillageActivityEntryDTO{}})
		return
	}
	log := snap.ActionLog
	total := len(log)
	var latestSeq uint64
	if total > 0 {
		latestSeq = log[total-1].Seq
	}

	q := r.URL.Query()
	limit := parseActionsLimit(q.Get("limit"))
	if raw := q.Get("since_seq"); raw != "" {
		sinceSeq, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since_seq must be an unsigned integer")
			return
		}
		// Slice order == Seq order, so the first entry past the cursor
		// starts the result; everything before it is already rendered
		// client-side.
		i := 0
		for i < len(log) && log[i].Seq <= sinceSeq {
			i++
		}
		log = log[i:]
		if len(log) > limit {
			log = log[:limit] // catch-up: oldest first, never skip
		}
	} else if len(log) > limit {
		log = log[len(log)-limit:] // backload: newest tail
	}

	out := VillageActivityDTO{
		ContractVersion: ContractVersion,
		Total:           total,
		Returned:        len(log),
		LatestSeq:       latestSeq,
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
		Seq:        e.Seq,
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
