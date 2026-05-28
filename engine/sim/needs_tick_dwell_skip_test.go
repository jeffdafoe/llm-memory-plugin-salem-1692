package sim

import (
	"testing"
	"time"
)

// needs_tick_dwell_skip_test.go — ZBBS-WORK-346. Per-attribute dwell-credit
// skip in IncrementNeedsTick, ported from v1 ZBBS-HOME-214's
// actor_dwell_credit NOT EXISTS predicate. Reuses sleepTestWorld /
// agentNPCWithNeeds / hasNeedThresholdWarrant / warrantKinds from
// needs_threshold_test.go + npc_sleep_test.go (same package).

// dwellCreditFor returns a single-entry DwellCredits map with one credit on
// the given attribute, source, and LastCreditedAt. ObjectID is canned so
// the entry shape is complete (the skip path doesn't inspect it).
func dwellCreditFor(attr NeedKey, source DwellCreditSource, lastCredited time.Time) map[DwellCreditKey]*DwellCredit {
	key := DwellCreditKey{ObjectID: "obj1", Attribute: attr, Source: source}
	return map[DwellCreditKey]*DwellCredit{
		key: {
			ObjectID:           "obj1",
			Attribute:          attr,
			Source:             source,
			LastCreditedAt:     lastCredited,
			DwellDelta:         -1,
			DwellPeriodMinutes: 10,
		},
	}
}

// TestNeedsTickDwellSkip_FreshObjectSourceSkips: an actor resting under a
// Shade Tree (source="object") gets the tiredness increment suppressed
// for the hour while hunger and thirst accrue normally.
func TestNeedsTickDwellSkip_FreshObjectSourceSkips(t *testing.T) {
	a := agentNPCWithNeeds("n", 5, 5, 5)
	a.DwellCredits = dwellCreditFor("tiredness", DwellSourceObject, time.Now().UTC().Add(-30*time.Minute))
	w := needsTickWorld(1, a)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if got := a.Needs["tiredness"]; got != 5 {
		t.Errorf("tiredness = %d, want 5 (skipped by fresh dwell credit)", got)
	}
	if got := a.Needs["hunger"]; got != 6 {
		t.Errorf("hunger = %d, want 6 (no dwell credit on hunger)", got)
	}
	if got := a.Needs["thirst"]; got != 6 {
		t.Errorf("thirst = %d, want 6 (no dwell credit on thirst)", got)
	}
}

// TestNeedsTickDwellSkip_FreshItemSourceSkips: same skip behavior when the
// credit is item-sourced (still digesting stew). Source-agnostic — v1's
// SQL predicate didn't discriminate either.
func TestNeedsTickDwellSkip_FreshItemSourceSkips(t *testing.T) {
	a := agentNPCWithNeeds("n", 5, 5, 5)
	a.DwellCredits = dwellCreditFor("hunger", DwellSourceItem, time.Now().UTC().Add(-15*time.Minute))
	w := needsTickWorld(1, a)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if got := a.Needs["hunger"]; got != 5 {
		t.Errorf("hunger = %d, want 5 (skipped by fresh item-source dwell credit)", got)
	}
	if got := a.Needs["thirst"]; got != 6 {
		t.Errorf("thirst = %d, want 6", got)
	}
	if got := a.Needs["tiredness"]; got != 6 {
		t.Errorf("tiredness = %d, want 6", got)
	}
}

// TestNeedsTickDwellSkip_StaleCreditDoesNotSkip: a credit whose
// LastCreditedAt is older than NeedsTickDwellSkipWindow does NOT suppress
// the increment. v1's predicate used `> NOW() - 1 hour`; ports verbatim.
func TestNeedsTickDwellSkip_StaleCreditDoesNotSkip(t *testing.T) {
	a := agentNPCWithNeeds("n", 5, 5, 5)
	a.DwellCredits = dwellCreditFor("tiredness", DwellSourceObject, time.Now().UTC().Add(-90*time.Minute))
	w := needsTickWorld(1, a)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if got := a.Needs["tiredness"]; got != 6 {
		t.Errorf("tiredness = %d, want 6 (stale credit does not skip)", got)
	}
}

// TestNeedsTickDwellSkip_PerAttributeGranularity: with one fresh credit
// (tiredness) and one stale credit (thirst), the skip applies only to
// tiredness; thirst and hunger accrue normally.
func TestNeedsTickDwellSkip_PerAttributeGranularity(t *testing.T) {
	a := agentNPCWithNeeds("n", 5, 5, 5)
	now := time.Now().UTC()
	freshKey := DwellCreditKey{ObjectID: "obj1", Attribute: "tiredness", Source: DwellSourceObject}
	staleKey := DwellCreditKey{ObjectID: "obj2", Attribute: "thirst", Source: DwellSourceObject}
	a.DwellCredits = map[DwellCreditKey]*DwellCredit{
		freshKey: {
			ObjectID: "obj1", Attribute: "tiredness", Source: DwellSourceObject,
			LastCreditedAt: now.Add(-20 * time.Minute), DwellDelta: -1, DwellPeriodMinutes: 10,
		},
		staleKey: {
			ObjectID: "obj2", Attribute: "thirst", Source: DwellSourceObject,
			LastCreditedAt: now.Add(-90 * time.Minute), DwellDelta: -1, DwellPeriodMinutes: 10,
		},
	}
	w := needsTickWorld(1, a)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if got := a.Needs["tiredness"]; got != 5 {
		t.Errorf("tiredness = %d, want 5 (fresh credit skips)", got)
	}
	if got := a.Needs["thirst"]; got != 6 {
		t.Errorf("thirst = %d, want 6 (stale credit does not skip)", got)
	}
	if got := a.Needs["hunger"]; got != 6 {
		t.Errorf("hunger = %d, want 6 (no credit at all)", got)
	}
}

// TestNeedsTickDwellSkip_SleepingActorUnaffected: a sleeping actor is
// filtered at the whole-actor level before the per-need loop runs; the
// dwell-skip branch never fires. Regression guard so the per-need skip
// can't accidentally tick a sleeping actor.
func TestNeedsTickDwellSkip_SleepingActorUnaffected(t *testing.T) {
	a := agentNPCWithNeeds("n", 5, 5, 5)
	until := time.Now().UTC().Add(time.Hour)
	a.SleepingUntil = &until
	a.DwellCredits = dwellCreditFor("tiredness", DwellSourceObject, time.Now().UTC().Add(-30*time.Minute))
	w := needsTickWorld(1, a)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	// All needs unchanged because the whole-actor sleeping filter ran
	// before the per-need loop.
	if got := a.Needs["hunger"]; got != 5 {
		t.Errorf("hunger = %d, want 5 (sleeping actor)", got)
	}
	if got := a.Needs["thirst"]; got != 5 {
		t.Errorf("thirst = %d, want 5 (sleeping actor)", got)
	}
	if got := a.Needs["tiredness"]; got != 5 {
		t.Errorf("tiredness = %d, want 5 (sleeping actor)", got)
	}
}

// TestNeedsTickDwellSkip_NoCrossingWhenSkipped: hunger is one below its red
// threshold and has a fresh dwell credit. Without the skip, +1 bump would
// cross 18 and stamp a warrant; with the skip, no increment and no warrant.
func TestNeedsTickDwellSkip_NoCrossingWhenSkipped(t *testing.T) {
	a := agentNPCWithNeeds("n", 17, 5, 5)
	a.DwellCredits = dwellCreditFor("hunger", DwellSourceItem, time.Now().UTC().Add(-10*time.Minute))
	w := needsTickWorld(1, a)

	if _, err := IncrementNeedsTick(1).Fn(w); err != nil {
		t.Fatalf("IncrementNeedsTick: %v", err)
	}
	if got := a.Needs["hunger"]; got != 17 {
		t.Errorf("hunger = %d, want 17 (skip suppresses bump)", got)
	}
	if a.WarrantedSince != nil {
		t.Errorf("WarrantedSince set despite skipped need; should not stamp")
	}
	if hasNeedThresholdWarrant(a) {
		t.Errorf("need_threshold warrant stamped despite skip; kinds = %v", warrantKinds(a))
	}
}
