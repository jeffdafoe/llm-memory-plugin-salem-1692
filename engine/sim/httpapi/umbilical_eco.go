package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_eco.go — LLM-313. Live-tune eco mode on the running engine: the
// master switch and the per-bucket pacing floors the reactor applies to
// social/economy warrant cycles while no player character has a fresh presence
// stamp. The change applies on the next reactor scan AND persists on the next
// checkpoint via MutableWorldSettings, so it survives restart. The read side is
// GET /api/village/umbilical/settings, which also reports whether the throttles
// are engaged at this instant.

// umbilicalEcoModeRequest is the body of
// POST /api/village/umbilical/settings/eco-mode. All fields optional but at
// least one must be present (nil = leave unchanged). Gaps are seconds, >= 0;
// a zero gap disables that bucket's throttle, enabled=false kills the feature.
type umbilicalEcoModeRequest struct {
	Enabled           *bool `json:"enabled"`
	SocialGapSeconds  *int  `json:"social_gap_seconds"`
	EconomyGapSeconds *int  `json:"economy_gap_seconds"`
}

// umbilicalEcoModeResponse echoes the post-change knobs plus the live
// engagement state (enabled AND no fresh player presence), so cause and effect
// land in one response.
type umbilicalEcoModeResponse struct {
	Enabled           bool `json:"enabled"`
	SocialGapSeconds  int  `json:"social_gap_seconds"`
	EconomyGapSeconds int  `json:"economy_gap_seconds"`
	AudienceActive    bool `json:"audience_active"`
	Engaged           bool `json:"engaged"`
}

// handleUmbilicalEcoMode applies a live eco mode change. Operator-gated +
// audited like the rest of the umbilical control surface. The audit line
// records the requested values (logged before the command, so even a rejected
// attempt is recorded against the operator).
func (s *Server) handleUmbilicalEcoMode(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalEcoModeRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	auditUmbilical(user.Username, "settings.eco-mode", ecoModeAuditDetail(req))

	res, err := s.world.SendContext(r.Context(), sim.SetEcoMode(req.Enabled, req.SocialGapSeconds, req.EconomyGapSeconds))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, sim.ErrInvalidEcoModeSetting) {
			writeError(w, http.StatusBadRequest, "provide at least one of enabled / social_gap_seconds / economy_gap_seconds (gaps >= 0 and below the warrant stale horizon, default 90s; 0 disables that throttle). To bound how long a conversation runs, see settings/huddle-loop huddle_conversation_wind_down_seconds")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.EcoModeSettingsResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected eco-mode result")
		return
	}
	writeJSON(w, umbilicalEcoModeResponse{
		Enabled:           out.Enabled,
		SocialGapSeconds:  out.SocialGapSeconds,
		EconomyGapSeconds: out.EconomyGapSeconds,
		AudienceActive:    out.AudienceActive,
		Engaged:           out.Engaged,
	})
}

// ecoModeAuditDetail renders the requested knobs for the audit log, with
// <absent> markers so a partial or rejected update is legible.
func ecoModeAuditDetail(req umbilicalEcoModeRequest) string {
	enabled := "<absent>"
	if req.Enabled != nil {
		enabled = fmt.Sprintf("%t", *req.Enabled)
	}
	social := "<absent>"
	if req.SocialGapSeconds != nil {
		social = fmt.Sprintf("%d", *req.SocialGapSeconds)
	}
	economy := "<absent>"
	if req.EconomyGapSeconds != nil {
		economy = fmt.Sprintf("%d", *req.EconomyGapSeconds)
	}
	return fmt.Sprintf("enabled=%s social_gap_seconds=%s economy_gap_seconds=%s", enabled, social, economy)
}
