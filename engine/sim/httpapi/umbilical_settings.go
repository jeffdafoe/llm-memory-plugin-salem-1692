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
	// HuddleLoopMaxTurns (LLM-333) is the endurance arm's no-progress turn
	// budget — spoken lines with no completed transaction, genuine membership
	// change, or player line before the huddle reads as stuck regardless of
	// wording. Reported as the EFFECTIVE value (a stored 0 resolves to the
	// default), matching the other seeded knobs.
	HuddleLoopMaxTurns int `json:"huddle_loop_max_turns"`

	// HuddleConversationWindDownSeconds (LLM-397) is the lingering arm's clock:
	// how long a conversation may run before its members are steered to close it.
	// HuddleConversationHardConcludeSeconds is that plus the persistence gate —
	// when the engine ends it if they don't. Reported as EFFECTIVE values (a
	// stored 0 resolves to the default); hard-conclude is 0 when the sweep is off,
	// meaning no conversation is ever force-ended.
	HuddleConversationWindDownSeconds     int `json:"huddle_conversation_wind_down_seconds"`
	HuddleConversationHardConcludeSeconds int `json:"huddle_conversation_hard_conclude_seconds"`

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

	// Visitor cascade (LLM-437) — the knobs driving the transient-visitor tier
	// (engine/sim/visitor.go + cascade/visitor.go + the LLM-410 factor). Unlike
	// every field above these are NOT checkpoint-persisted: they load once via
	// parseIntSetting at boot (repo/pg/environment.go) and change only by editing
	// the env config and restarting — so this read is the ONLY console window onto
	// them (eco mode pauses spawning while unwatched, so live visitor count can't
	// reveal the rate either). Reported as EFFECTIVE values: each mirrors the exact
	// clamp its dispatcher applies to world.Settings at spawn/return time, so the
	// figure shown is what the next visitor would actually use — not the raw stored
	// value. NOTE the clamps are NOT uniformly "0 → Default": the purse and
	// return-max knobs clamp to a floor/companion rather than resolving to the
	// env-loader default (see handleUmbilicalSettings), so a stored 0 there reports
	// 0 / the min, matching the engine.
	//
	// VisitorSpawnChancePermille is the master gate and is reported RAW — 0 means
	// spawning is OFF, a real operator setting, not a fall-through to a default.
	VisitorSpawnChancePermille int `json:"visitor_spawn_chance_permille"`
	VisitorMaxConcurrent       int `json:"visitor_max_concurrent"`
	VisitorTickIntervalSeconds int `json:"visitor_tick_interval_seconds"`
	VisitorReturnMinDays       int `json:"visitor_return_min_days"`
	VisitorReturnMaxDays       int `json:"visitor_return_max_days"`
	VisitorFactorPackUnits     int `json:"visitor_factor_pack_units"`
	VisitorFactorPurseMin      int `json:"visitor_factor_purse_min"`
	VisitorFactorPurseMax      int `json:"visitor_factor_purse_max"`
}

// handleUmbilicalSettings serves the current live-tunable world settings. Read on
// the world goroutine via SendContext: the need-threshold control command mutates
// WorldSettings in place, so an off-goroutine read could race it. Pure read —
// mutates nothing.
func (s *Server) handleUmbilicalSettings(w http.ResponseWriter, r *http.Request) {
	res, err := s.world.SendContext(r.Context(), sim.Command{Fn: func(world *sim.World) (any, error) {
		audience := sim.AudienceActive(world, time.Now().UTC())
		maxTurns := world.Settings.HuddleLoopMaxTurns
		if maxTurns <= 0 {
			maxTurns = sim.HuddleLoopMaxTurnsDefault
		}
		windDown := world.Settings.HuddleConversationWindDown
		if windDown <= 0 {
			windDown = sim.HuddleConversationWindDownDefault
		}
		// No hard conclude at all while the sweep is off — reporting wind_down+0
		// would advertise an ending the engine will never deliver.
		hardConclude := time.Duration(0)
		if world.Settings.HuddleLoopTimeout > 0 {
			hardConclude = windDown + world.Settings.HuddleLoopTimeout
		}
		// Visitor-cascade effective values — each line replicates the clamp the
		// owning dispatcher applies to world.Settings, so the report matches what a
		// spawn/return actually uses (see the DTO doc). Spawn-chance is reported raw
		// (0 = OFF). max-concurrent (visitor.go), tick-interval (cascade/visitor.go)
		// and return-min (recurring_visitor.go) resolve <=0 to their Default. The
		// factor pack-units resolves <1 to its Default; the purse and return-max
		// knobs instead clamp to a floor (neg->0) / their companion min, NOT to the
		// env-loader default — mirroring dispatchVisitorSpawn and scheduleReturn.
		visitorMax := world.Settings.VisitorMaxConcurrent
		if visitorMax <= 0 {
			visitorMax = sim.DefaultVisitorMaxConcurrent
		}
		visitorTick := world.Settings.VisitorTickInterval
		if visitorTick <= 0 {
			visitorTick = sim.DefaultVisitorTickInterval
		}
		// Emitted as whole seconds below (int(visitorTick / time.Second)), lossless:
		// visitor_tick_interval_seconds is an INTEGER-seconds env setting
		// (parseDurationSetting parses an int and multiplies by time.Second), so the
		// effective duration is always a whole number of seconds — same convention as
		// every other *Seconds field in this DTO.
		returnMin := world.Settings.VisitorReturnMinDays
		if returnMin <= 0 {
			returnMin = sim.DefaultVisitorReturnMinDays
		}
		returnMax := world.Settings.VisitorReturnMaxDays
		if returnMax < returnMin {
			returnMax = returnMin
		}
		factorUnits := world.Settings.VisitorFactorPackUnits
		if factorUnits < 1 {
			factorUnits = sim.DefaultVisitorFactorPackUnits
		}
		factorPurseMin := world.Settings.VisitorFactorPurseMin
		if factorPurseMin < 0 {
			factorPurseMin = 0
		}
		factorPurseMax := world.Settings.VisitorFactorPurseMax
		if factorPurseMax < factorPurseMin {
			factorPurseMax = factorPurseMin
		}
		dto := UmbilicalSettingsDTO{
			ContractVersion:                       ContractVersion,
			NeedThresholds:                        make(map[string]int, len(world.Settings.NeedThresholds)),
			HuddleLoopEnabled:                     world.Settings.HuddleLoopTimeout > 0,
			HuddleLoopTimeoutSeconds:              int(world.Settings.HuddleLoopTimeout / time.Second),
			HuddleLoopRepeatPercent:               world.Settings.HuddleLoopRepeatPercent,
			HuddleLoopSweepCadenceSeconds:         int(world.Settings.HuddleLoopSweepCadence / time.Second),
			HuddleLoopMaxTurns:                    maxTurns,
			HuddleConversationWindDownSeconds:     int(windDown / time.Second),
			HuddleConversationHardConcludeSeconds: int(hardConclude / time.Second),
			SeekWorkCoinCeiling:                   world.Settings.SeekWorkCoinCeiling,
			SeekWorkNeedYieldMargin:               world.Settings.SeekWorkNeedYieldMargin,
			FarmUpkeepFloor:                       world.Settings.FarmUpkeepFloor,
			FarmUpkeepCoinsPerShovel:              world.Settings.FarmUpkeepCoinsPerShovel,
			LaborProduceBoostPct:                  world.Settings.LaborProduceBoostPct,
			MerchantCoinFloor:                     world.Settings.MerchantCoinFloor,
			EcoEnabled:                            world.Settings.EcoEnabled,
			EcoSocialGapSeconds:                   int(world.Settings.EcoSocialGap / time.Second),
			EcoEconomyGapSeconds:                  int(world.Settings.EcoEconomyGap / time.Second),
			EcoAudienceActive:                     audience,
			EcoEngaged:                            world.Settings.EcoEnabled && !audience,
			VisitorSpawnChancePermille:            world.Settings.VisitorSpawnChancePermille,
			VisitorMaxConcurrent:                  visitorMax,
			VisitorTickIntervalSeconds:            int(visitorTick / time.Second),
			VisitorReturnMinDays:                  returnMin,
			VisitorReturnMaxDays:                  returnMax,
			VisitorFactorPackUnits:                factorUnits,
			VisitorFactorPurseMin:                 factorPurseMin,
			VisitorFactorPurseMax:                 factorPurseMax,
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
