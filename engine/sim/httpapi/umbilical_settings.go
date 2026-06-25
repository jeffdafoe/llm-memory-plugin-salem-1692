package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_settings.go — the read side (LLM-110) of the live-tunable
// WorldSettings the control surface mutates. Today that is the per-need red-line
// thresholds (settings/need-threshold); this is the get that pairs with that set,
// so an operator can see the current threshold before tuning it.

// UmbilicalSettingsDTO is the GET /api/village/umbilical/settings response: the
// live, operator-tunable world settings. need_thresholds maps each configured
// need to its current red-line (the value settings/need-threshold writes). These
// are EPHEMERAL — WorldSettings is load-only (not persisted by SaveWorld), so
// they reset to the env-configured defaults on restart.
type UmbilicalSettingsDTO struct {
	ContractVersion int            `json:"contract_version"`
	NeedThresholds  map[string]int `json:"need_thresholds"`
}

// handleUmbilicalSettings serves the current live-tunable world settings. Read on
// the world goroutine via SendContext: the need-threshold control command mutates
// WorldSettings in place, so an off-goroutine read could race it. Pure read —
// mutates nothing.
func (s *Server) handleUmbilicalSettings(w http.ResponseWriter, r *http.Request) {
	res, err := s.world.SendContext(r.Context(), sim.Command{Fn: func(world *sim.World) (any, error) {
		dto := UmbilicalSettingsDTO{
			ContractVersion: ContractVersion,
			NeedThresholds:  make(map[string]int, len(world.Settings.NeedThresholds)),
		}
		for k, v := range world.Settings.NeedThresholds {
			dto.NeedThresholds[string(k)] = v
		}
		return dto, nil
	}})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	dto, ok := res.(UmbilicalSettingsDTO)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected settings result")
		return
	}
	writeJSON(w, dto)
}
