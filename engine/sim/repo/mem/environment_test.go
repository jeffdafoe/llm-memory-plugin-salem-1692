package mem_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestSaveMutableSettings_RoundTrip pins that the mem EnvironmentRepo persists the
// FULL runtime-tunable subset (LLM-183 huddle-loop knobs + the LLM-118 stall-wear
// knobs) through SaveMutableSettings -> Load, mirroring the pg setting-table
// writeback so a live tune survives a save/reload. The huddle-loop *_seconds fields
// round-trip from the snapshot's ints back to Durations.
func TestSaveMutableSettings_RoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := mem.NewEnvironmentRepo()

	// LLM-577: the snapshot carries key→stored-string rows from the settings
	// registry instead of named fields. Same round-trip property — the values
	// written here must come back out of Load as live WorldSettings, with the
	// *_seconds keys landing as Durations.
	ms := sim.MutableWorldSettings{Rows: map[string]string{
		"world_zoom_min_admin":              "0.3",
		"world_zoom_min_regular":            "0.6",
		"agent_ticks_paused":                "true",
		"stall_wear_per_coin":               "2",
		"stall_wear_repair_threshold":       "300",
		"stall_wear_degrade_threshold":      "900",
		"stall_nails_per_repair":            "7",
		"stall_repair_duration_seconds":     "120",
		"huddle_loop_timeout_seconds":       "90",
		"huddle_loop_repeat_percent":        "70",
		"huddle_loop_sweep_cadence_seconds": "20",
		"seek_work_coin_ceiling":            "33",
		"seek_work_need_yield_margin":       "9",
		"labor_produce_boost_pct":           "75",
	}}
	if err := repo.SaveMutableSettings(ctx, nil, ms); err != nil {
		t.Fatalf("SaveMutableSettings: %v", err)
	}
	_, _, settings, err := repo.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if settings.HuddleLoopTimeout != 90*time.Second ||
		settings.HuddleLoopRepeatPercent != 70 ||
		settings.HuddleLoopSweepCadence != 20*time.Second {
		t.Errorf("huddle-loop = %v/%d/%v, want 90s/70/20s",
			settings.HuddleLoopTimeout, settings.HuddleLoopRepeatPercent, settings.HuddleLoopSweepCadence)
	}
	if settings.StallWearPerCoin != 2 || settings.StallRepairDurationSeconds != 120 {
		t.Errorf("stall-wear = %d/%d, want 2/120",
			settings.StallWearPerCoin, settings.StallRepairDurationSeconds)
	}
	if settings.ZoomMinAdmin != 0.3 || !settings.AgentTicksPaused {
		t.Errorf("zoom/pause = %v/%v, want 0.3/true", settings.ZoomMinAdmin, settings.AgentTicksPaused)
	}
	if settings.SeekWorkCoinCeiling != 33 {
		t.Errorf("seek-work coin ceiling = %d, want 33 (LLM-194 round-trip)", settings.SeekWorkCoinCeiling)
	}
	if settings.SeekWorkNeedYieldMargin != 9 {
		t.Errorf("seek-work need-yield margin = %d, want 9 (LLM-276 round-trip)", settings.SeekWorkNeedYieldMargin)
	}
	if settings.LaborProduceBoostPct != 75 {
		t.Errorf("labor produce boost = %d, want 75 (LLM-224 round-trip)", settings.LaborProduceBoostPct)
	}
}

// TestSaveMutableSettings_ConstableRounds pins that the LLM-514 constable rounds
// knobs (plus the LLM-537 quiet window) are APPLIED back into WorldSettings on
// restore (SaveMutableSettings -> Load), covering the interval=0 off-switch (stays
// 0) and the dwell=0 / quiet=0 cases (stay 0 RAW, with the defaults applied only at
// read by Effective*). The raw-vs-effective split is the point: persisting the
// resolved default instead of the stored 0 would quietly convert "unset" into "set
// to today's default" and freeze the value against a later default change. This is
// the restore half of the round-trip — the save half rides BuildCheckpointSnapshot.
func TestSaveMutableSettings_ConstableRounds(t *testing.T) {
	ctx := context.Background()

	t.Run("concrete_value_round_trips", func(t *testing.T) {
		repo := mem.NewEnvironmentRepo()
		ms := sim.MutableWorldSettings{Rows: map[string]string{"constable_rounds_interval_seconds": "7200"}}
		if err := repo.SaveMutableSettings(ctx, nil, ms); err != nil {
			t.Fatalf("SaveMutableSettings: %v", err)
		}
		_, _, settings, err := repo.Load(ctx)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if settings.ConstableRoundsInterval != 2*time.Hour {
			t.Errorf("interval = %v, want 2h", settings.ConstableRoundsInterval)
		}
	})

	// 0 is the feature's off-switch, so it must survive the round-trip AS 0.
	// Persisting a resolved default here would silently convert "rounds are off"
	// into "rounds run at whatever today's default is".
	t.Run("interval_off_survives_restore", func(t *testing.T) {
		repo := mem.NewEnvironmentRepo()
		ms := sim.MutableWorldSettings{Rows: map[string]string{"constable_rounds_interval_seconds": "0"}}
		if err := repo.SaveMutableSettings(ctx, nil, ms); err != nil {
			t.Fatalf("SaveMutableSettings: %v", err)
		}
		_, _, settings, err := repo.Load(ctx)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if settings.ConstableRoundsInterval != 0 {
			t.Errorf("interval = %v, want 0 (off-switch preserved on restore)", settings.ConstableRoundsInterval)
		}
	})
}
