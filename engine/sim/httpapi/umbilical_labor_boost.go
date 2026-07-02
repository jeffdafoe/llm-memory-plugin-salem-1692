package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_labor_boost.go — LLM-224. Live-tune the per-worker produce boost on the
// running engine: each worker laboring at their employer's establishment adds this
// percent of the keeper's base rate to the produce tick (labor buys real output).
// The change applies in memory immediately (the produce tick reads w.Settings live;
// the perception hire-value cue reads the next published snapshot) AND persists on
// the next checkpoint via MutableWorldSettings, so it survives restart. 0 disables
// the boost. The read side is GET /api/village/umbilical/settings.

// umbilicalLaborBoostRequest is the body of
// POST /api/village/umbilical/settings/labor-produce-boost. boost_pct is required
// and must be >= 0 (a single knob — there is nothing to leave unchanged; 0 is the
// explicit off-switch).
type umbilicalLaborBoostRequest struct {
	BoostPct *int `json:"boost_pct"`
}

// umbilicalLaborBoostResponse echoes the post-change boost percent.
type umbilicalLaborBoostResponse struct {
	BoostPct int `json:"boost_pct"`
}

// handleUmbilicalLaborBoost applies a live labor-produce-boost change.
// Operator-gated + audited like the rest of the umbilical control surface. The audit
// line records the requested value (logged before the command, so even a rejected
// attempt is recorded against the operator).
func (s *Server) handleUmbilicalLaborBoost(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalLaborBoostRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	auditUmbilical(user.Username, "settings.labor-produce-boost", laborBoostAuditDetail(req))

	res, err := s.world.SendContext(r.Context(), sim.SetLaborProduceBoostPct(req.BoostPct))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, sim.ErrInvalidLaborProduceBoostSetting) {
			writeError(w, http.StatusBadRequest, "provide boost_pct (>=0; 0 disables the boost)")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.LaborProduceBoostSettingResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected labor-produce-boost result")
		return
	}
	writeJSON(w, umbilicalLaborBoostResponse{BoostPct: out.BoostPct})
}

// laborBoostAuditDetail renders the requested percent for the audit log (or
// "boost_pct=<absent>" when the field was omitted, so a rejected no-op is legible).
func laborBoostAuditDetail(req umbilicalLaborBoostRequest) string {
	if req.BoostPct == nil {
		return "boost_pct=<absent>"
	}
	return fmt.Sprintf("boost_pct=%d", *req.BoostPct)
}
