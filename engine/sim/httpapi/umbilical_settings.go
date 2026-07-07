package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_settings.go — the read side (LLM-110) of the live-tunable
// WorldSettings the control surface mutates: the per-need red-line thresholds
// (settings/need-threshold) and the huddle loop-sweep knobs (settings/huddle-loop,
// LLM-183). The get that pairs with those sets, so an operator can see the current
// values before tuning them.

// UmbilicalSettingsDTO is the GET /api/village/umbilical/settings response: the
// live, operator-tunable world settings.
//
// need_thresholds maps each configured need to its current red-line (the value
// settings/need-threshold writes). These are EPHEMERAL — those keys are not
// persisted by SaveWorld, so they reset to the env-configured defaults on restart.
//
// The huddle_loop_* fields are the loop-sweep knobs settings/huddle-loop writes.
// huddle_loop_enabled is timeout > 0 — the master enable for both the sweep and
// the per-tick ConversationLooping steer. Unlike the need thresholds these PERSIST
// (the checkpoint writes them back to the setting table), so they survive restart.
type UmbilicalSettingsDTO struct {
	ContractVersion int            `json:"contract_version"`
	NeedThresholds  map[string]int `json:"need_thresholds"`

	HuddleLoopEnabled             bool `json:"huddle_loop_enabled"`
	HuddleLoopTimeoutSeconds      int  `json:"huddle_loop_timeout_seconds"`
	HuddleLoopRepeatPercent       int  `json:"huddle_loop_repeat_percent"`
	HuddleLoopSweepCadenceSeconds int  `json:"huddle_loop_sweep_cadence_seconds"`

	// SeekWorkCoinCeiling (LLM-194) is the coin balance at/above which a workless
	// worker stops seeking/soliciting work (settings/seek-work-ceiling writes it). Like
	// the huddle_loop_* knobs it PERSISTS (the checkpoint writes it back to the setting
	// table), so it survives restart. The live engine resolves a 0 here to the default;
	// the loaded value is seeded to the default, so this reports a concrete figure.
	SeekWorkCoinCeiling int `json:"seek_work_coin_ceiling"`

	// SeekWorkNeedYieldMargin (LLM-276) is the width, below each need's red-line, of
	// the upper-felt band in which a workless idle worker with a resolvable hunger/
	// thirst is redirected to eat/drink instead of seeking work (settings/seek-work-
	// need-margin writes it). Persisted (checkpoint writes it back), so it survives
	// restart; seeded to the default, so this reports a concrete figure.
	SeekWorkNeedYieldMargin int `json:"seek_work_need_yield_margin"`

	// FarmUpkeepFloor / FarmUpkeepCoinsPerShovel (LLM-215) are the farm wealth-tax
	// knobs the farm-upkeep/set route writes: a farm owner owes one upkeep shovel per
	// FarmUpkeepCoinsPerShovel coins held above FarmUpkeepFloor. Persisted (checkpoint
	// writes them back), so they survive restart. FarmUpkeepCoinsPerShovel == 0 means
	// the feature is disabled. The GET half of no-blind-tuning symmetry.
	FarmUpkeepFloor          int `json:"farm_upkeep_floor"`
	FarmUpkeepCoinsPerShovel int `json:"farm_upkeep_coins_per_shovel"`

	// LaborProduceBoostPct (LLM-224) is the per-worker produce-rate boost a laboring
	// worker adds at their employer's establishment (settings/labor-produce-boost
	// writes it). Persisted (checkpoint writes it back), so it survives restart.
	// 0 means the boost is disabled.
	LaborProduceBoostPct int `json:"labor_produce_boost_pct"`

	// MerchantCoinFloor (LLM-294) is the working-capital floor below which a keeper
	// sitting on unsold sellable stock is steered to conserve coin rather than restock
	// (settings/merchant-coin-floor writes it). Persisted (checkpoint writes it back),
	// so it survives restart. 0 means the gate is disabled.
	MerchantCoinFloor int `json:"merchant_coin_floor"`

	// Eco mode (LLM-313) — the knobs settings/eco-mode writes, persisted like the
	// huddle_loop_* group. eco_audience_active / eco_engaged are LIVE state, not
	// settings: whether any PC's presence stamp is fresh at this instant, and
	// whether the throttles are consequently applying (enabled AND no audience) —
	// the pair makes a "why is/isn't the village slowed right now" read one call.
	EcoEnabled           bool `json:"eco_enabled"`
	EcoSocialGapSeconds  int  `json:"eco_social_gap_seconds"`
	EcoEconomyGapSeconds int  `json:"eco_economy_gap_seconds"`
	EcoAudienceActive    bool `json:"eco_audience_active"`
	EcoEngaged           bool `json:"eco_engaged"`
}

// handleUmbilicalSettings serves the current live-tunable world settings. Read on
// the world goroutine via SendContext: the need-threshold control command mutates
// WorldSettings in place, so an off-goroutine read could race it. Pure read —
// mutates nothing.
func (s *Server) handleUmbilicalSettings(w http.ResponseWriter, r *http.Request) {
	res, err := s.world.SendContext(r.Context(), sim.Command{Fn: func(world *sim.World) (any, error) {
		audience := sim.AudienceActive(world, time.Now().UTC())
		dto := UmbilicalSettingsDTO{
			ContractVersion:               ContractVersion,
			NeedThresholds:                make(map[string]int, len(world.Settings.NeedThresholds)),
			HuddleLoopEnabled:             world.Settings.HuddleLoopTimeout > 0,
			HuddleLoopTimeoutSeconds:      int(world.Settings.HuddleLoopTimeout / time.Second),
			HuddleLoopRepeatPercent:       world.Settings.HuddleLoopRepeatPercent,
			HuddleLoopSweepCadenceSeconds: int(world.Settings.HuddleLoopSweepCadence / time.Second),
			SeekWorkCoinCeiling:           world.Settings.SeekWorkCoinCeiling,
			SeekWorkNeedYieldMargin:       world.Settings.SeekWorkNeedYieldMargin,
			FarmUpkeepFloor:               world.Settings.FarmUpkeepFloor,
			FarmUpkeepCoinsPerShovel:      world.Settings.FarmUpkeepCoinsPerShovel,
			LaborProduceBoostPct:          world.Settings.LaborProduceBoostPct,
			MerchantCoinFloor:             world.Settings.MerchantCoinFloor,
			EcoEnabled:                    world.Settings.EcoEnabled,
			EcoSocialGapSeconds:           int(world.Settings.EcoSocialGap / time.Second),
			EcoEconomyGapSeconds:          int(world.Settings.EcoEconomyGap / time.Second),
			EcoAudienceActive:             audience,
			EcoEngaged:                    world.Settings.EcoEnabled && !audience,
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
