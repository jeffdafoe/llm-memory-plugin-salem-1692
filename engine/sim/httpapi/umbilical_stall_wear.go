package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_stall_wear.go — LLM-118. Live-tune the stall wear knobs on the running
// engine without a restart (the demand-loop calibration surface — they're
// guesstimates tuned against the smith's real nail output). The change applies in
// memory immediately and persists on the next checkpoint via MutableWorldSettings.

// umbilicalStallWearRequest is the body of POST /api/village/umbilical/stall-wear/set.
// Every field is optional (a nil pointer leaves that knob unchanged); at least one
// must be supplied, and a supplied value must be >= 0.
type umbilicalStallWearRequest struct {
	StallWearPerCoin           *int `json:"stall_wear_per_coin"`
	StallWearRepairThreshold   *int `json:"stall_wear_repair_threshold"`
	StallWearDegradeThreshold  *int `json:"stall_wear_degrade_threshold"`
	StallNailsPerRepair        *int `json:"stall_nails_per_repair"`
	StallRepairDurationSeconds *int `json:"stall_repair_duration_seconds"`
}

// umbilicalStallWearResponse echoes the full post-change knob set.
type umbilicalStallWearResponse struct {
	StallWearPerCoin           int `json:"stall_wear_per_coin"`
	StallWearRepairThreshold   int `json:"stall_wear_repair_threshold"`
	StallWearDegradeThreshold  int `json:"stall_wear_degrade_threshold"`
	StallNailsPerRepair        int `json:"stall_nails_per_repair"`
	StallRepairDurationSeconds int `json:"stall_repair_duration_seconds"`
}

// handleUmbilicalStallWearSet applies a live stall-wear knob change. Operator-gated
// + audited like the rest of the umbilical control surface.
func (s *Server) handleUmbilicalStallWearSet(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalStallWearRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	auditUmbilical(user.Username, "stall-wear.set", "")

	res, err := s.world.SendContext(r.Context(), sim.SetStallWearSettings(
		req.StallWearPerCoin, req.StallWearRepairThreshold, req.StallWearDegradeThreshold,
		req.StallNailsPerRepair, req.StallRepairDurationSeconds,
	))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if errors.Is(err, sim.ErrInvalidStallWearSetting) {
			writeError(w, http.StatusBadRequest, "provide at least one stall wear knob as a non-negative integer")
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	out, ok := res.(sim.StallWearSettingsResult)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected stall-wear result")
		return
	}
	writeJSON(w, umbilicalStallWearResponse{
		StallWearPerCoin:           out.StallWearPerCoin,
		StallWearRepairThreshold:   out.StallWearRepairThreshold,
		StallWearDegradeThreshold:  out.StallWearDegradeThreshold,
		StallNailsPerRepair:        out.StallNailsPerRepair,
		StallRepairDurationSeconds: out.StallRepairDurationSeconds,
	})
}
