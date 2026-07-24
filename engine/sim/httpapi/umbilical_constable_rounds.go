package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_constable_rounds.go — LLM-514. Live-tune the constable rounds cadence
// on the running engine without a restart: how often the constable (Gideon Marsh)
// leaves his post to walk the businesses, and how long he pauses at each. The
// change applies on the next schedule tick / arrival and persists on the next
// checkpoint via MutableWorldSettings. The read side is GET
// /api/village/umbilical/settings (constable_rounds_*_seconds).

// umbilicalConstableRoundsRequest is the body of
// POST /api/village/umbilical/constable-rounds/set. Both fields optional (a nil
// pointer leaves that knob unchanged); at least one must be supplied, and a
// supplied value must be >= 0 seconds. interval_seconds == 0 disables rounds;
// dwell_seconds == 0 resolves to the default at read.
type umbilicalConstableRoundsRequest struct {
	IntervalSeconds *int `json:"interval_seconds"`
	DwellSeconds    *int `json:"dwell_seconds"`
}

// umbilicalConstableRoundsResponse echoes the post-change knobs (dwell is the
// EFFECTIVE value — a stored 0 reports the default).
type umbilicalConstableRoundsResponse struct {
	IntervalSeconds int `json:"interval_seconds"`
	DwellSeconds    int `json:"dwell_seconds"`
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

	res, err := s.world.SendContext(r.Context(), sim.SetConstableRoundsSettings(req.IntervalSeconds, req.DwellSeconds))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, sim.ErrInvalidConstableRoundsSetting) {
			writeError(w, http.StatusBadRequest, "provide at least one of interval_seconds / dwell_seconds as a non-negative integer (interval_seconds 0 disables rounds)")
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
	writeJSON(w, umbilicalConstableRoundsResponse{
		IntervalSeconds: out.IntervalSeconds,
		DwellSeconds:    out.DwellSeconds,
	})
}
