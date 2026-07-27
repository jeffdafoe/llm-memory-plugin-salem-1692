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

	ms := sim.MutableWorldSettings{
		ZoomMinAdmin:                  0.3,
		ZoomMinRegular:                0.6,
		AgentTicksPaused:              true,
		StallWearPerCoin:              2,
		StallWearRepairThreshold:      300,
		StallWearDegradeThreshold:     900,
		StallNailsPerRepair:           7,
		StallRepairDurationSeconds:    120,
		HuddleLoopTimeoutSeconds:      90,
		HuddleLoopRepeatPercent:       70,
		HuddleLoopSweepCadenceSeconds: 20,
		SeekWorkCoinCeiling:           33,
		SeekWorkNeedYieldMargin:       9,
		LaborProduceBoostPct:          75,
	}
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

	t.Run("concrete_values_round_trip", func(t *testing.T) {
		repo := mem.NewEnvironmentRepo()
		ms := sim.MutableWorldSettings{
			ConstableRoundsIntervalSeconds: 7200,
			ConstableRoundsDwellSeconds:    45,
			ConstableRoundsQuietSeconds:    120,
		}
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
		if settings.ConstableRoundsDwell != 45*time.Second {
			t.Errorf("dwell = %v, want 45s", settings.ConstableRoundsDwell)
		}
		if settings.ConstableRoundsQuiet != 2*time.Minute {
			t.Errorf("quiet = %v, want 2m", settings.ConstableRoundsQuiet)
		}
	})

	t.Run("interval_off_and_dwell_quiet_default", func(t *testing.T) {
		repo := mem.NewEnvironmentRepo()
		ms := sim.MutableWorldSettings{
			ConstableRoundsIntervalSeconds: 0,
			ConstableRoundsDwellSeconds:    0,
			ConstableRoundsQuietSeconds:    0,
		}
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
		if settings.ConstableRoundsDwell != 0 {
			t.Errorf("dwell raw = %v, want 0 (stored), default applied only at read", settings.ConstableRoundsDwell)
		}
		if settings.ConstableRoundsQuiet != 0 {
			t.Errorf("quiet raw = %v, want 0 (stored), default applied only at read", settings.ConstableRoundsQuiet)
		}
		w := &sim.World{Settings: settings}
		if got := sim.EffectiveConstableRoundsDwell(w); got != sim.DefaultConstableRoundsDwell {
			t.Errorf("effective dwell = %v, want default %v", got, sim.DefaultConstableRoundsDwell)
		}
		if got := sim.EffectiveConstableRoundsQuiet(w); got != sim.DefaultConstableRoundsQuiet {
			t.Errorf("effective quiet = %v, want default %v", got, sim.DefaultConstableRoundsQuiet)
		}
	})
}
