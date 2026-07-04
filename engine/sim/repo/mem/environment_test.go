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
