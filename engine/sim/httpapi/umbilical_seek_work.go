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
