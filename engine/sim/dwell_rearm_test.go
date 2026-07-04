package sim

import (
	"testing"
	"time"
)

// dwell_rearm_test.go — LLM-281. rearmDwellAtSource re-applies the arrival
// drink (burst + open-ended dwell credit) for a present, red, credit-less actor
// standing on a refresh source's loiter pin, so an actor that never fired an
// ActorArrived (placed / checkpoint-loaded on the pin) — or one whose credit
// terminated at the floor while it stayed put — still recovers instead of
// sitting at the fountain forever. Driven through the public ApplyDwellTick
// command against a hand-built world, reusing intptr from npc_sleep_test.go.

// rearmTestWorld seeds a world with an infinite thirst well and a multi-attribute
// oak (tiredness + hunger, no thirst), both with a zero loiter offset so each
// pin lands on the object's anchor tile. The returned actor is a shared VA parked
// ON the well pin, red on thirst (13 ≥ 12) with no dwell credits — the trapped
// shape from the ticket. Callers mutate the actor/objects to exercise each guard.
func rearmTestWorld() (*World, *Actor, *VillageObject) {
	zero := 0
	well := &VillageObject{
		ID: "well", DisplayName: "Well", AssetID: "well-stone", CurrentState: "default",
		LoiterOffsetX: &zero, LoiterOffsetY: &zero,
		Pos: WorldPos{X: 100, Y: 100},
		Refreshes: []*ObjectRefresh{
			{
				Attribute:          "thirst",
				Amount:             -8, // arrival burst
				DwellDelta:         intptr(-2),
				DwellPeriodMinutes: intptr(30),
			},
		},
	}
	oak := &VillageObject{
		ID: "oak", DisplayName: "Oak", AssetID: "tree-oak", CurrentState: "default",
		LoiterOffsetX: &zero, LoiterOffsetY: &zero,
		Pos: WorldPos{X: 500, Y: 500},
		Refreshes: []*ObjectRefresh{
			{Attribute: "tiredness", Amount: -8, DwellDelta: intptr(-1), DwellPeriodMinutes: intptr(20)},
			{Attribute: "hunger", Amount: -4, DwellDelta: intptr(-1), DwellPeriodMinutes: intptr(20)},
		},
	}
	actor := &Actor{
		ID:       "moses",
		Kind:     KindNPCShared,
		LLMAgent: "salem-vendor",
		Needs:    map[NeedKey]int{"hunger": 5, "thirst": 13, "tiredness": 5},
		Pos:      well.Pos.Tile(), // standing on the well pin (Chebyshev 0)
	}
	w := &World{
		Settings: WorldSettings{Location: time.UTC, MaxWarrantsPerActor: 16},
		Actors:   map[ActorID]*Actor{actor.ID: actor},
		VillageObjects: map[VillageObjectID]*VillageObject{
			well.ID: well, oak.ID: oak,
		},
		Assets: map[AssetID]*Asset{
			"well-stone": {ID: "well-stone"},
			"tree-oak":   {ID: "tree-oak"},
		},
	}
	return w, actor, well
}

func thirstCreditFor(actor *Actor, objID VillageObjectID) (*DwellCredit, bool) {
	c, ok := actor.DwellCredits[DwellCreditKey{ObjectID: objID, Attribute: "thirst", Source: DwellSourceObject}]
	return c, ok
}

// TestDwellRearm_ParkedRedCreditlessActor_BurstAndCredit: the core repair. An
// actor parked on the well pin, red on thirst, with NO prior arrival (no credit)
// gets the burst applied (thirst -8) AND an open-ended dwell credit stamped —
// identical to walking up and drinking.
func TestDwellRearm_ParkedRedCreditlessActor_BurstAndCredit(t *testing.T) {
	w, actor, _ := rearmTestWorld()
	now := time.Unix(1_700_000_000, 0).UTC()

	if _, err := ApplyDwellTick(now).Fn(w); err != nil {
		t.Fatalf("ApplyDwellTick: %v", err)
	}
	if got := actor.Needs["thirst"]; got != 5 {
		t.Errorf("thirst = %d, want 5 (13 - 8 arrival burst)", got)
	}
	c, ok := thirstCreditFor(actor, "well")
	if !ok {
		t.Fatal("no thirst dwell credit stamped after re-arm")
	}
	if !c.LastCreditedAt.Equal(now) {
		t.Errorf("credit LastCreditedAt = %v, want %v", c.LastCreditedAt, now)
	}
	if c.DwellDelta != -2 || c.DwellPeriodMinutes != 30 {
		t.Errorf("credit dwell config = (%d, %d), want (-2, 30)", c.DwellDelta, c.DwellPeriodMinutes)
	}
}

// TestDwellRearm_ThenDripsToFloor: after the re-arm burst, the open-ended credit
// drains the actor on subsequent ripe ticks until floor-hit clears and deletes
// it — the "drains over time with no prior arrival" coverage the ticket asks for.
func TestDwellRearm_ThenDripsToFloor(t *testing.T) {
	w, actor, _ := rearmTestWorld()
	t0 := time.Unix(1_700_000_000, 0).UTC()

	// t0: burst 13 -> 5, credit stamped (LastCreditedAt = t0).
	if _, err := ApplyDwellTick(t0).Fn(w); err != nil {
		t.Fatalf("ApplyDwellTick t0: %v", err)
	}
	if got := actor.Needs["thirst"]; got != 5 {
		t.Fatalf("after burst thirst = %d, want 5", got)
	}

	// The credit drips -2 every 30 min; walk it down past the floor.
	for i, step := range []struct {
		at        time.Duration
		wantValue int
	}{
		{30 * time.Minute, 3}, // 5 -> 3
		{60 * time.Minute, 1}, // 3 -> 1
		{90 * time.Minute, 0}, // 1 -> 0 (floor-hit terminates the credit)
	} {
		if _, err := ApplyDwellTick(t0.Add(step.at)).Fn(w); err != nil {
			t.Fatalf("ApplyDwellTick step %d: %v", i, err)
		}
		if got := actor.Needs["thirst"]; got != step.wantValue {
			t.Errorf("step %d (t0+%s): thirst = %d, want %d", i, step.at, got, step.wantValue)
		}
	}
	if _, ok := thirstCreditFor(actor, "well"); ok {
		t.Error("thirst credit should be deleted after floor-hit termination")
	}
}

// TestDwellRearm_NoRearmWhileMoving: a mover is passing through, not dwelling —
// it must not re-drink (it will arrive properly when its move completes).
func TestDwellRearm_NoRearmWhileMoving(t *testing.T) {
	w, actor, _ := rearmTestWorld()
	actor.MoveIntent = &MoveIntent{}
	now := time.Unix(1_700_000_000, 0).UTC()

	if _, err := ApplyDwellTick(now).Fn(w); err != nil {
		t.Fatalf("ApplyDwellTick: %v", err)
	}
	if got := actor.Needs["thirst"]; got != 13 {
		t.Errorf("thirst = %d, want 13 (a mover must not re-drink)", got)
	}
	if _, ok := thirstCreditFor(actor, "well"); ok {
		t.Error("a mover must not be granted a dwell credit")
	}
}

// TestDwellRearm_NoRearmWhileBusyAtSource: an in-flight SourceActivity (the ~3s
// drink window) stamps the credit itself on completion — re-arming here would
// double the burst, so it must be skipped while busy.
func TestDwellRearm_NoRearmWhileBusyAtSource(t *testing.T) {
	w, actor, _ := rearmTestWorld()
	now := time.Unix(1_700_000_000, 0).UTC()
	actor.SourceActivity = &SourceActivity{
		Kind: SourceActivityRefresh, ObjectID: "well",
		StartedAt: now, Until: now.Add(3 * time.Second),
	}

	if _, err := ApplyDwellTick(now).Fn(w); err != nil {
		t.Fatalf("ApplyDwellTick: %v", err)
	}
	if got := actor.Needs["thirst"]; got != 13 {
		t.Errorf("thirst = %d, want 13 (must not re-drink while busy at the source)", got)
	}
	if _, ok := thirstCreditFor(actor, "well"); ok {
		t.Error("must not stamp a credit while a drink window is already in flight")
	}
}

// TestDwellRearm_NoRearmWhenNotRed: thirst below its red threshold is not a
// pressing need — no re-drink (matches the warrant predicate the re-arm shares).
func TestDwellRearm_NoRearmWhenNotRed(t *testing.T) {
	w, actor, _ := rearmTestWorld()
	actor.Needs["thirst"] = 11 // below the 12 red threshold
	now := time.Unix(1_700_000_000, 0).UTC()

	if _, err := ApplyDwellTick(now).Fn(w); err != nil {
		t.Fatalf("ApplyDwellTick: %v", err)
	}
	if got := actor.Needs["thirst"]; got != 11 {
		t.Errorf("thirst = %d, want 11 (not red → no re-drink)", got)
	}
	if _, ok := thirstCreditFor(actor, "well"); ok {
		t.Error("must not re-arm a below-threshold need")
	}
}

// TestDwellRearm_NoRearmAtWrongSource: red on thirst but parked at the oak (which
// eases tiredness + hunger, not thirst). The actor should walk to a well
// (move_to is not no-op'd there), so there is no trap — no re-drink, and the oak
// must not drop thirst.
func TestDwellRearm_NoRearmAtWrongSource(t *testing.T) {
	w, actor, _ := rearmTestWorld()
	oak := w.VillageObjects["oak"]
	actor.Pos = oak.Pos.Tile() // stand at the oak, still red on thirst

	now := time.Unix(1_700_000_000, 0).UTC()
	if _, err := ApplyDwellTick(now).Fn(w); err != nil {
		t.Fatalf("ApplyDwellTick: %v", err)
	}
	if got := actor.Needs["thirst"]; got != 13 {
		t.Errorf("thirst = %d, want 13 (the oak does not quench thirst)", got)
	}
	if _, ok := thirstCreditFor(actor, "oak"); ok {
		t.Error("must not stamp a thirst credit at a source that does not ease thirst")
	}
}

// TestDwellRearm_LiveCreditSuppressesWarrantWhileStillRed: the headline symptom.
// A very thirsty (22) parked actor is still RED after the -8 burst (14 ≥ 12), but
// the freshly-stamped credit makes actorActionableRedNeed skip thirst — so the
// need_threshold warrant that fired every few seconds ("Moses sits at the
// fountain") is silenced by the credit itself, not merely by the value dropping.
func TestDwellRearm_LiveCreditSuppressesWarrantWhileStillRed(t *testing.T) {
	w, actor, _ := rearmTestWorld()
	actor.Needs["thirst"] = 22 // stays red (>= 12) even after the -8 burst
	now := time.Unix(1_700_000_000, 0).UTC()
	nowMinute := localMinuteOfDay(w, now)

	if _, ok := actorActionableRedNeed(w, actor, now, nowMinute); !ok {
		t.Fatal("precondition: a red, credit-less actor should be warrant-actionable before the tick")
	}
	if _, err := ApplyDwellTick(now).Fn(w); err != nil {
		t.Fatalf("ApplyDwellTick: %v", err)
	}
	if got := actor.Needs["thirst"]; got != 14 {
		t.Fatalf("thirst = %d, want 14 (22 - 8 burst, still red)", got)
	}
	if _, ok := thirstCreditFor(actor, "well"); !ok {
		t.Fatal("expected a live thirst credit after re-arm")
	}
	if need, ok := actorActionableRedNeed(w, actor, now, nowMinute); ok {
		t.Errorf("actor still warrant-actionable on %q after re-arm — the live credit must suppress it", need)
	}
}

// TestDwellRearm_NoRearmAtOtherOwnedSource: a source owned by someone else is
// off-limits (LLM-50 D2) — mirror the arrival owner-gate, no free drink.
func TestDwellRearm_NoRearmAtOtherOwnedSource(t *testing.T) {
	w, actor, well := rearmTestWorld()
	well.OwnerActorID = "someone-else"
	now := time.Unix(1_700_000_000, 0).UTC()

	if _, err := ApplyDwellTick(now).Fn(w); err != nil {
		t.Fatalf("ApplyDwellTick: %v", err)
	}
	if got := actor.Needs["thirst"]; got != 13 {
		t.Errorf("thirst = %d, want 13 (no drink at another's source)", got)
	}
	if _, ok := thirstCreditFor(actor, "well"); ok {
		t.Error("must not re-drink at a source owned by someone else")
	}
}

// TestDwellRearm_NoReburstWhenStaleCreditHeld: the explicit credit-less guard
// (code_review LLM-281). A dwell period longer than the 60-min needs-tick
// freshness window means a ripe-but-stale credit is NOT skipped by
// actorActionableRedNeed, so without the guard rearmDwellAtSource would re-apply
// the arrival burst on top of the scheduled drip. With the guard the already-held
// credit just drips normally.
func TestDwellRearm_NoReburstWhenStaleCreditHeld(t *testing.T) {
	w, actor, well := rearmTestWorld()
	well.Refreshes[0].DwellPeriodMinutes = intptr(90) // > 60-min freshness window
	now := time.Unix(1_700_000_000, 0).UTC()
	actor.DwellCredits = map[DwellCreditKey]*DwellCredit{
		{ObjectID: "well", Attribute: "thirst", Source: DwellSourceObject}: {
			ObjectID: "well", Attribute: "thirst", Source: DwellSourceObject,
			LastCreditedAt: now.Add(-90 * time.Minute), DwellDelta: -1, DwellPeriodMinutes: 90,
		},
	}

	if _, err := ApplyDwellTick(now).Fn(w); err != nil {
		t.Fatalf("ApplyDwellTick: %v", err)
	}
	// Scheduled drip only: 13 - 1 = 12. A spurious re-burst would give 13 - 8 = 5.
	if got := actor.Needs["thirst"]; got != 12 {
		t.Errorf("thirst = %d, want 12 (drip only; a re-burst of the -8 arrival amount would give 5)", got)
	}
	c, ok := thirstCreditFor(actor, "well")
	if !ok {
		t.Fatal("the held credit should survive")
	}
	if !c.LastCreditedAt.Equal(now) {
		t.Errorf("credit LastCreditedAt = %v, want %v (advanced one period by the drip)", c.LastCreditedAt, now)
	}
}

// TestDwellRearm_NoRearmAtDepletedFiniteSource: a depleted finite row gives
// nothing on arrival (applyObjectRefreshEffect skips it), so the re-drink must
// mirror that — no burst and no zero-stock credit.
func TestDwellRearm_NoRearmAtDepletedFiniteSource(t *testing.T) {
	w, actor, well := rearmTestWorld()
	well.Refreshes[0].AvailableQuantity = intptr(0) // dry
	well.Refreshes[0].MaxQuantity = intptr(5)
	well.Refreshes[0].RefreshMode = RefreshModeContinuous
	well.Refreshes[0].RefreshPeriodHours = intptr(24)
	now := time.Unix(1_700_000_000, 0).UTC()

	if _, err := ApplyDwellTick(now).Fn(w); err != nil {
		t.Fatalf("ApplyDwellTick: %v", err)
	}
	if got := actor.Needs["thirst"]; got != 13 {
		t.Errorf("thirst = %d, want 13 (a dry well gives nothing)", got)
	}
	if _, ok := thirstCreditFor(actor, "well"); ok {
		t.Error("must not stamp a credit at a depleted source")
	}
}
