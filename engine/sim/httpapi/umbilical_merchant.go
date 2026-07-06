package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_merchant.go — LLM-294. Live-tune the merchant working-capital floor on the
// running engine: the purse balance below which a keeper sitting on unsold sellable
// stock is steered to conserve coin (hold off buying, sell down its shelves) rather
// than restock. The change applies in memory immediately (the perception gate reads
// the next published snapshot, which mirrors the raw value) AND persists on the next
// checkpoint via MutableWorldSettings, so it survives restart. 0 is the off-switch.
// The read side is GET /api/village/umbilical/settings.

// umbilicalMerchantCoinFloorRequest is the body of
// POST /api/village/umbilical/settings/merchant-coin-floor. coin_floor is required and
// must be >= 0 (a single knob — there is nothing to leave unchanged; 0 disables the
// gate).
type umbilicalMerchantCoinFloorRequest struct {
	CoinFloor *int `json:"coin_floor"`
}

// umbilicalMerchantCoinFloorResponse echoes the post-change floor.
type umbilicalMerchantCoinFloorResponse struct {
	CoinFloor int `json:"coin_floor"`
}

// handleUmbilicalMerchantCoinFloor applies a live merchant working-capital floor
// change. Operator-gated + audited like the rest of the umbilical control surface. The
// audit line records the requested value (logged before the command, so even a
// rejected attempt is recorded against the operator).
func (s *Server) handleUmbilicalMerchantCoinFloor(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalMerchantCoinFloorRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	auditUmbilical(user.Username, "settings.merchant-coin-floor", merchantCoinFloorAuditDetail(req))

	res, err := s.world.SendContext(r.Context(), sim.SetMerchantCoinFloor(req.CoinFloor))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, sim.ErrInvalidMerchantCoinFloorSetting) {
			writeError(w, http.StatusBadRequest, "provide coin_floor (>=0; 0 disables)")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.MerchantCoinFloorSettingResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected merchant-coin-floor result")
		return
	}
	writeJSON(w, umbilicalMerchantCoinFloorResponse{CoinFloor: out.CoinFloor})
}

// merchantCoinFloorAuditDetail renders the requested floor for the audit log (or
// "coin_floor=<absent>" when the field was omitted, so a rejected no-op is legible).
func merchantCoinFloorAuditDetail(req umbilicalMerchantCoinFloorRequest) string {
	if req.CoinFloor == nil {
		return "coin_floor=<absent>"
	}
	return fmt.Sprintf("coin_floor=%d", *req.CoinFloor)
}
