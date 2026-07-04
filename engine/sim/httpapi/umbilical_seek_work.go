package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_seek_work.go — LLM-194. Live-tune the seek-work coin ceiling on the
// running engine: the coin balance at/above which a workless worker stops
// seeking/soliciting work and drains its purse via ordinary consumption. The change
// applies in memory immediately (the warrant gate reads w.Settings live; the
// perception gate reads the next published snapshot, which copies the effective value)
// AND persists on the next checkpoint via MutableWorldSettings, so it survives restart.
// The read side is GET /api/village/umbilical/settings.

// umbilicalSeekWorkCeilingRequest is the body of
// POST /api/village/umbilical/settings/seek-work-ceiling. coin_ceiling is required and
// must be >= 1 (a single knob — there is nothing to leave unchanged).
type umbilicalSeekWorkCeilingRequest struct {
	CoinCeiling *int `json:"coin_ceiling"`
}

// umbilicalSeekWorkCeilingResponse echoes the post-change ceiling.
type umbilicalSeekWorkCeilingResponse struct {
	CoinCeiling int `json:"coin_ceiling"`
}

// handleUmbilicalSeekWorkCeiling applies a live seek-work coin ceiling change.
// Operator-gated + audited like the rest of the umbilical control surface. The audit
// line records the requested value (logged before the command, so even a rejected
// attempt is recorded against the operator).
func (s *Server) handleUmbilicalSeekWorkCeiling(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalSeekWorkCeilingRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	auditUmbilical(user.Username, "settings.seek-work-ceiling", seekWorkCeilingAuditDetail(req))

	res, err := s.world.SendContext(r.Context(), sim.SetSeekWorkCoinCeiling(req.CoinCeiling))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, sim.ErrInvalidSeekWorkCeilingSetting) {
			writeError(w, http.StatusBadRequest, "provide coin_ceiling (>=1)")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.SeekWorkCeilingSettingResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected seek-work-ceiling result")
		return
	}
	writeJSON(w, umbilicalSeekWorkCeilingResponse{CoinCeiling: out.CoinCeiling})
}

// seekWorkCeilingAuditDetail renders the requested ceiling for the audit log (or
// "coin_ceiling=<absent>" when the field was omitted, so a rejected no-op is legible).
func seekWorkCeilingAuditDetail(req umbilicalSeekWorkCeilingRequest) string {
	if req.CoinCeiling == nil {
		return "coin_ceiling=<absent>"
	}
	return fmt.Sprintf("coin_ceiling=%d", *req.CoinCeiling)
}

// umbilicalSeekWorkNeedMarginRequest is the body of
// POST /api/village/umbilical/settings/seek-work-need-margin. margin is required and
// must be >= 1 (a single knob — there is nothing to leave unchanged).
type umbilicalSeekWorkNeedMarginRequest struct {
	Margin *int `json:"margin"`
}

// umbilicalSeekWorkNeedMarginResponse echoes the post-change margin.
type umbilicalSeekWorkNeedMarginResponse struct {
	Margin int `json:"margin"`
}

// handleUmbilicalSeekWorkNeedMargin applies a live LLM-276 seek-work→eat redirect
// margin change: the width below each need's red-line in which a workless idle worker
// with a resolvable hunger/thirst is steered to eat instead of seeking work. The
// change applies in memory immediately (the backstop reads w.Settings live) AND
// persists on the next checkpoint via MutableWorldSettings. Read side: GET /settings.
func (s *Server) handleUmbilicalSeekWorkNeedMargin(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalSeekWorkNeedMarginRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	auditUmbilical(user.Username, "settings.seek-work-need-margin", seekWorkNeedMarginAuditDetail(req))

	res, err := s.world.SendContext(r.Context(), sim.SetSeekWorkNeedYieldMargin(req.Margin))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, sim.ErrInvalidSeekWorkNeedMarginSetting) {
			writeError(w, http.StatusBadRequest, "provide margin (>=1)")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.SeekWorkNeedMarginSettingResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected seek-work-need-margin result")
		return
	}
	writeJSON(w, umbilicalSeekWorkNeedMarginResponse{Margin: out.Margin})
}

// seekWorkNeedMarginAuditDetail renders the requested margin for the audit log (or
// "margin=<absent>" when the field was omitted, so a rejected no-op is legible).
func seekWorkNeedMarginAuditDetail(req umbilicalSeekWorkNeedMarginRequest) string {
	if req.Margin == nil {
		return "margin=<absent>"
	}
	return fmt.Sprintf("margin=%d", *req.Margin)
}
