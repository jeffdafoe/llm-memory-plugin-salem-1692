package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_huddle_loop.go — LLM-183. Live-tune the huddle conversational-loop
// sweep (LLM-159) on the running engine: flip the master enable
// (huddle_loop_timeout_seconds) and tune the repetition threshold + scan cadence
// without a restart. The change applies in memory immediately (huddleLoopEnabled
// is re-read every scan + every republish) AND persists on the next checkpoint via
// MutableWorldSettings, so it survives restart. The read side is
// GET /api/village/umbilical/settings.

// umbilicalHuddleLoopRequest is the body of POST /api/village/umbilical/settings/huddle-loop.
// Every field is optional (a nil pointer leaves that knob unchanged); at least one
// must be supplied. timeout_seconds 0 disables the whole sweep (the master
// off-switch); repeat_percent is 1-100; cadence_seconds must be > 0.
type umbilicalHuddleLoopRequest struct {
	TimeoutSeconds *int `json:"timeout_seconds"`
	RepeatPercent  *int `json:"repeat_percent"`
	CadenceSeconds *int `json:"cadence_seconds"`
}

// umbilicalHuddleLoopResponse echoes the full post-change knob set (wire units).
// Enabled is timeout_seconds > 0 — the master enable for both the sweep and the
// per-tick ConversationLooping steer.
type umbilicalHuddleLoopResponse struct {
	TimeoutSeconds int  `json:"timeout_seconds"`
	RepeatPercent  int  `json:"repeat_percent"`
	CadenceSeconds int  `json:"cadence_seconds"`
	Enabled        bool `json:"enabled"`
}

// handleUmbilicalHuddleLoop applies a live huddle loop-sweep knob change.
// Operator-gated + audited like the rest of the umbilical control surface. The
// audit line records the requested knobs (logged before the command, so even a
// rejected attempt is recorded against the operator).
func (s *Server) handleUmbilicalHuddleLoop(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalHuddleLoopRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	auditUmbilical(user.Username, "settings.huddle-loop", huddleLoopAuditDetail(req))

	res, err := s.world.SendContext(r.Context(), sim.SetHuddleLoopSettings(
		req.TimeoutSeconds, req.RepeatPercent, req.CadenceSeconds,
	))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, sim.ErrInvalidHuddleLoopSetting) {
			writeError(w, http.StatusBadRequest, "provide at least one of timeout_seconds (>=0), repeat_percent (1-100), cadence_seconds (>0)")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.HuddleLoopSettingsResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected huddle-loop result")
		return
	}
	writeJSON(w, umbilicalHuddleLoopResponse{
		TimeoutSeconds: out.TimeoutSeconds,
		RepeatPercent:  out.RepeatPercent,
		CadenceSeconds: out.CadenceSeconds,
		Enabled:        out.TimeoutSeconds > 0,
	})
}

// huddleLoopAuditDetail renders the supplied (non-nil) knobs for the audit log.
func huddleLoopAuditDetail(req umbilicalHuddleLoopRequest) string {
	parts := make([]string, 0, 3)
	if req.TimeoutSeconds != nil {
		parts = append(parts, fmt.Sprintf("timeout_seconds=%d", *req.TimeoutSeconds))
	}
	if req.RepeatPercent != nil {
		parts = append(parts, fmt.Sprintf("repeat_percent=%d", *req.RepeatPercent))
	}
	if req.CadenceSeconds != nil {
		parts = append(parts, fmt.Sprintf("cadence_seconds=%d", *req.CadenceSeconds))
	}
	return strings.Join(parts, " ")
}
