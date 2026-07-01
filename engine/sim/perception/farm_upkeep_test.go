package perception

import (
	"testing"
)

// farm_upkeep_test.go — LLM-215. Documents the intended DYNAMIC-TARGET semantics of
// the "## Farm upkeep" cue: the shortfall is re-derived from CURRENT coins each build,
// so as the owner buys shovels (coins fall, held rises) the cue shrinks and then
// clears — a self-limiting upkeep target, NOT a fixed daily debt (the greenlit stock-
// based, no-accumulator design). Reuses the golden fixture builder farmUpkeepSnapshot.
func TestFarmUpkeepCue_DynamicTargetShrinksAsBought(t *testing.T) {
	// floor 30, band 20; walk the buy sequence at the real shovel retail (12/shovel).
	steps := []struct {
		coins, held, wantShort int // wantShort 0 => cue absent
	}{
		{95, 0, 3}, // daily boundary: owes floor((95-30)/20) = 3
		{83, 1, 1}, // bought 1 (paid 12): owes floor((83-30)/20) = 2, short 1
		{71, 2, 0}, // bought another: owes floor((71-30)/20) = 2, held 2 => cue clears
	}
	for _, s := range steps {
		snap, actorID, _ := farmUpkeepSnapshot(s.coins, s.held, 30, 20)
		v := buildFarmUpkeep(snap, actorID, snap.Actors[actorID])
		if s.wantShort == 0 {
			if v != nil {
				t.Errorf("coins=%d held=%d: expected no cue, got shortfall %d", s.coins, s.held, v.ShovelsShort)
			}
			continue
		}
		if v == nil {
			t.Fatalf("coins=%d held=%d: expected cue with shortfall %d, got nil", s.coins, s.held, s.wantShort)
		}
		if v.ShovelsShort != s.wantShort {
			t.Errorf("coins=%d held=%d: shortfall = %d, want %d", s.coins, s.held, v.ShovelsShort, s.wantShort)
		}
	}
}

// A farm owner below the floor gets no cue at all (owes nothing), and the feature
// off-switch (coinsPerShovel 0) silences the cue even for a rich owner.
func TestFarmUpkeepCue_BelowFloorAndOffSwitch(t *testing.T) {
	if snap, id, _ := farmUpkeepSnapshot(25, 0, 30, 20); buildFarmUpkeep(snap, id, snap.Actors[id]) != nil {
		t.Error("a below-floor farm owner should get no upkeep cue")
	}
	if snap, id, _ := farmUpkeepSnapshot(95, 0, 30, 0); buildFarmUpkeep(snap, id, snap.Actors[id]) != nil {
		t.Error("coinsPerShovel=0 (off-switch) should silence the cue even for a rich owner")
	}
}
