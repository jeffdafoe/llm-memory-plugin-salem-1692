package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_farm_upkeep.go — LLM-215. Live-tune the farm wealth-tax knobs on the
// running engine without a restart (the calibration surface for the coin-circulation
// lever — they're guesstimates tuned against the live coin balances). The change
// applies in memory immediately and persists on the next checkpoint via
// MutableWorldSettings.

// umbilicalFarmUpkeepRequest is the body of POST /api/village/umbilical/farm-upkeep/set.
// Each field is optional (a nil pointer leaves that knob unchanged); at least one must
// be supplied, and a supplied value must be >= 0. farm_upkeep_coins_per_shovel == 0
// disables the feature (the off-switch).
type umbilicalFarmUpkeepRequest struct {
	FarmUpkeepFloor          *int `json:"farm_upkeep_floor"`
	FarmUpkeepCoinsPerShovel *int `json:"farm_upkeep_coins_per_shovel"`
}

// umbilicalFarmUpkeepResponse echoes the full post-change knob set.
type umbilicalFarmUpkeepResponse struct {
	FarmUpkeepFloor          int `json:"farm_upkeep_floor"`
	FarmUpkeepCoinsPerShovel int `json:"farm_upkeep_coins_per_shovel"`
}

// handleUmbilicalFarmUpkeepSet applies a live farm-upkeep knob change. Operator-gated
// + audited like the rest of the umbilical control surface.
func (s *Server) handleUmbilicalFarmUpkeepSet(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalFarmUpkeepRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	auditUmbilical(user.Username, "farm-upkeep.set", "")

	res, err := s.world.SendContext(r.Context(), sim.SetFarmUpkeepSettings(
		req.FarmUpkeepFloor, req.FarmUpkeepCoinsPerShovel,
	))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, sim.ErrInvalidFarmUpkeepSetting) {
			writeError(w, http.StatusBadRequest, "provide at least one farm upkeep knob as a non-negative integer")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.FarmUpkeepSettingsResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected farm-upkeep result")
		return
	}
	writeJSON(w, umbilicalFarmUpkeepResponse{
		FarmUpkeepFloor:          out.FarmUpkeepFloor,
		FarmUpkeepCoinsPerShovel: out.FarmUpkeepCoinsPerShovel,
	})
}
