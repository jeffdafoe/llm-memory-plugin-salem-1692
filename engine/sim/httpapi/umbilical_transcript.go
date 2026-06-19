package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_transcript.go — the GET /api/village/umbilical/transcript read route
// (LLM-35). The durable companion to the live /huddle ring: /huddle and /chat are
// retention-bounded in-memory buffers that silently drop a conversation's older
// turns, so there was no umbilical path to the COMPLETE past transcript of a
// conversation. That full trail lives only in the durable agent_action_log table
// (every participant's committed rows, keyed by huddle_id), which this route
// reads oldest-first across all sources — agent NPCs, the human player, the
// engine.
//
// Unlike the snapshot-backed /actions tail (the in-memory ActionLog ring filtered
// to one actor), this is a real durable read off the engine's pg pool — the same
// table the daily sim-conversation push reads. It is gated like every umbilical
// route (requireOperator) and answers 503 when no transcript store is wired
// (e.g. a headless engine with no pg pool), mirroring the /turns 503 posture.

// HuddleTranscriptStore reads the durable committed-action transcript for one
// huddle. *pg.ActionLogRepo satisfies it (LoadHuddleTranscript); the interface
// lives here so the httpapi package depends on a narrow seam rather than the pg
// package, the same dependency-inversion the Authenticator seam uses.
type HuddleTranscriptStore interface {
	LoadHuddleTranscript(ctx context.Context, huddleID string, limit int) ([]sim.HuddleTranscriptRow, error)
}

// transcriptMaxRows caps a single transcript read. A huddle is a bounded
// conversation (dozens of rows), so this is a safety ceiling, not an expected
// boundary; has_more reports if it is ever hit, so truncation is never silent —
// the same anti-silent-truncation contract LLM-35 fixes on the /turns side.
const transcriptMaxRows = 2000

// HuddleTranscriptEntryDTO is one committed action on the wire: when it happened,
// who acted (source + speaker), what they did (action_type), and the spoken or
// narrated line (text, omitted for textless actions like a bare paid/delivered).
type HuddleTranscriptEntryDTO struct {
	OccurredAt  time.Time `json:"occurred_at"`
	Source      string    `json:"source"`
	SpeakerName string    `json:"speaker_name,omitempty"`
	ActionType  string    `json:"action_type"`
	Text        string    `json:"text,omitempty"`
}

// UmbilicalTranscriptDTO is the GET /umbilical/transcript response: the complete
// committed transcript of one huddle, oldest-first. Returned is the row count;
// has_more is true only if the huddle exceeded transcriptMaxRows (the read was
// capped), so a partial transcript is never silent.
type UmbilicalTranscriptDTO struct {
	ContractVersion int                        `json:"contract_version"`
	HuddleID        string                     `json:"huddle_id"`
	Returned        int                        `json:"returned"`
	HasMore         bool                       `json:"has_more"`
	Transcript      []HuddleTranscriptEntryDTO `json:"transcript"`
}

// handleUmbilicalTranscript serves the durable, complete committed-action
// transcript for one huddle (query param `huddle`, required), oldest-first. 400
// when huddle is missing; 503 when no transcript store is wired. Over-fetches one
// row past the cap to set has_more without a COUNT, then trims — the same
// truncation-signal trick /turns uses on the memory-api side.
func (s *Server) handleUmbilicalTranscript(w http.ResponseWriter, r *http.Request) {
	huddle := r.URL.Query().Get("huddle")
	if huddle == "" {
		writeError(w, http.StatusBadRequest, "huddle is required")
		return
	}
	if s.transcript == nil {
		writeError(w, http.StatusServiceUnavailable, "transcript store not configured")
		return
	}

	rows, err := s.transcript.LoadHuddleTranscript(r.Context(), huddle, transcriptMaxRows+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load huddle transcript")
		return
	}

	hasMore := len(rows) > transcriptMaxRows
	if hasMore {
		rows = rows[:transcriptMaxRows]
	}

	out := UmbilicalTranscriptDTO{
		ContractVersion: ContractVersion,
		HuddleID:        huddle,
		Returned:        len(rows),
		HasMore:         hasMore,
		Transcript:      make([]HuddleTranscriptEntryDTO, 0, len(rows)),
	}
	for _, row := range rows {
		out.Transcript = append(out.Transcript, HuddleTranscriptEntryDTO{
			OccurredAt:  row.OccurredAt,
			Source:      row.Source,
			SpeakerName: row.SpeakerName,
			ActionType:  string(row.ActionType),
			Text:        row.Text,
		})
	}
	writeJSON(w, out)
}
