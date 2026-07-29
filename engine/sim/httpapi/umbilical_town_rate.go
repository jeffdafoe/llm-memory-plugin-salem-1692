package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_town_rate.go — LLM-557. Live-tune the constable's town rate on the
// running engine without a restart. The rate and its arrears cap are guesstimates
// sized against one constable's observed spending, so they want calibrating against
// live purses the same way the farm-upkeep levy does. The change applies in memory
// immediately and persists on the next checkpoint via MutableWorldSettings.

// umbilicalTownRateRequest is the body of POST /api/village/umbilical/town-rate/set.
// Each field is optional (a nil pointer leaves that knob unchanged); at least one
// must be supplied, and a supplied value must be >= 0. town_rate_coins_per_day == 0
// stops further accrual (the off-switch) without forgiving what is already owed;
// town_rate_max_owed == 0 means uncapped.
type umbilicalTownRateRequest struct {
	TownRateCoinsPerDay *int `json:"town_rate_coins_per_day"`
	TownRateMaxOwed     *int `json:"town_rate_max_owed"`
}

// umbilicalTownRateResponse echoes the full post-change knob set.
type umbilicalTownRateResponse struct {
	TownRateCoinsPerDay int `json:"town_rate_coins_per_day"`
	TownRateMaxOwed     int `json:"town_rate_max_owed"`
}

// handleUmbilicalTownRateSet applies a live town-rate knob change. Operator-gated +
// audited like the rest of the umbilical control surface.
func (s *Server) handleUmbilicalTownRateSet(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalTownRateRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	auditUmbilical(user.Username, "town-rate.set", "")

	res, err := s.world.SendContext(r.Context(), sim.SetTownRateSettings(
		req.TownRateCoinsPerDay, req.TownRateMaxOwed,
	))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, sim.ErrInvalidTownRateSetting) {
			writeError(w, http.StatusBadRequest, "provide at least one town rate knob as a non-negative integer")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.TownRateSettingsResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected town-rate result")
		return
	}
	writeJSON(w, umbilicalTownRateResponse{
		TownRateCoinsPerDay: out.TownRateCoinsPerDay,
		TownRateMaxOwed:     out.TownRateMaxOwed,
	})
}
