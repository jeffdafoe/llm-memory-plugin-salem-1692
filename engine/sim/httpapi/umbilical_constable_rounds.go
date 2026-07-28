package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_constable_rounds.go — LLM-514. Live-tune how often the constable
// (Gideon Marsh) owes a fresh round of the businesses, without a restart. The
// change applies on the next schedule tick and persists on the next checkpoint via
// MutableWorldSettings. The read side is GET /api/village/umbilical/settings
// (constable_rounds_interval_seconds).
//
// The dwell_seconds / quiet_seconds knobs are gone (LLM-548). They paced an engine
// that walked him stop to stop; a beat dispatches nothing, so how long he stays
// anywhere is settled by the conversation rather than by a timer.

// umbilicalConstableRoundsRequest is the body of
// POST /api/village/umbilical/constable-rounds/set. interval_seconds is required
// and must be >= 0; 0 disables rounds.
type umbilicalConstableRoundsRequest struct {
	IntervalSeconds *int `json:"interval_seconds"`
}

// umbilicalConstableRoundsResponse echoes the post-change knob.
type umbilicalConstableRoundsResponse struct {
	IntervalSeconds int `json:"interval_seconds"`
}

// handleUmbilicalConstableRoundsSet applies a live constable-rounds knob change.
// Operator-gated + audited like the rest of the umbilical control surface.
func (s *Server) handleUmbilicalConstableRoundsSet(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalConstableRoundsRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	auditUmbilical(user.Username, "constable-rounds.set", "")

	res, err := s.world.SendContext(r.Context(), sim.SetConstableRoundsSettings(req.IntervalSeconds))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, sim.ErrInvalidConstableRoundsSetting) {
			writeError(w, http.StatusBadRequest, "provide interval_seconds as a non-negative integer (0 disables rounds)")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.ConstableRoundsSettingsResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected constable-rounds result")
		return
	}
	writeJSON(w, umbilicalConstableRoundsResponse{IntervalSeconds: out.IntervalSeconds})
}
