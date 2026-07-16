package sim

import (
	"testing"
	"time"
)

// garment_wear_test.go — LLM-422. Internal tests for the garment-wear mechanics
// (applyGarmentWear / GarmentWearMinutes), the warms-garment wear tier
// (ResolveWarmGarmentTier), the wear-eligibility gate (actorWearsGarments), the
// per-minute sweep (WearGarments), and the threadbare cold coupling
// (coldRatePerMinuteX100).

// garmentTestCatalog: a warm garment (coat, budget 600), a plain non-warms
// garment (breeches, budget 480), and a non-garment (bread). Data-driven — the
// wear mechanic keys on WearMinutes, the relief on the warms capability.
func garmentTestCatalog() map[ItemKind]*ItemKindDef {
	return map[ItemKind]*ItemKindDef{
		"coat":     {Name: "coat", DisplayLabel: "coat", Category: "clothing", Capabilities: []string{CapabilityWarms}, WearMinutes: 600},
		"breeches": {Name: "breeches", DisplayLabel: "breeches", Category: "clothing", WearMinutes: 480},
		"bread":    {Name: "bread", DisplayLabel: "bread", Category: ItemCategoryFood},
	}
}

func TestGarmentWearMinutes(t *testing.T) {
	kinds := garmentTestCatalog()
	if got := GarmentWearMinutes(kinds, "coat"); got != 600 {
		t.Errorf("coat budget = %d, want 600", got)
	}
	if got := GarmentWearMinutes(kinds, "breeches"); got != 480 {
		t.Errorf("breeches budget = %d, want 480", got)
	}
	if got := GarmentWearMinutes(kinds, "bread"); got != 0 {
		t.Errorf("non-garment bread budget = %d, want 0", got)
	}
	if got := GarmentWearMinutes(kinds, "phantom"); got != 0 {
		t.Errorf("absent kind budget = %d, want 0", got)
	}
	if got := GarmentWearMinutes(nil, "coat"); got != 0 {
		t.Errorf("nil catalog budget = %d, want 0", got)
	}
}

func TestApplyGarmentWear(t *testing.T) {
	const budget = 100

	// Partial wear on a fresh in-use unit: no spend, entry written.
	a := &Actor{Inventory: map[ItemKind]int{"coat": 1}}
	res := applyGarmentWear(a, "coat", budget, 30)
	if res.Spent || res.MinutesLeft != 70 || res.OnHand != 1 {
		t.Errorf("partial wear: %+v, want {left:70 spent:false onhand:1}", res)
	}
	if a.GarmentWear["coat"] != 70 || a.Inventory["coat"] != 1 {
		t.Errorf("partial wear state: wear=%d inv=%d", a.GarmentWear["coat"], a.Inventory["coat"])
	}

	// Wear exactly through one unit with a spare behind it: spend 1, next unit
	// taken up fresh (no entry = canonical fresh), inventory drops to the spare.
	a = &Actor{Inventory: map[ItemKind]int{"coat": 2}}
	res = applyGarmentWear(a, "coat", budget, 100)
	if !res.Spent || res.OnHand != 1 {
		t.Errorf("spend-one: %+v, want {spent:true onhand:1}", res)
	}
	if _, ok := a.GarmentWear["coat"]; ok {
		t.Errorf("spend-one left a wear entry on the fresh spare: %d", a.GarmentWear["coat"])
	}

	// Last unit worn out: inventory + wear both cleared, leftover wear lost.
	a = &Actor{Inventory: map[ItemKind]int{"coat": 1}}
	res = applyGarmentWear(a, "coat", budget, 250) // 250 > the single unit's 100
	if !res.Spent || res.OnHand != 0 {
		t.Errorf("last-unit: %+v, want {spent:true onhand:0}", res)
	}
	if _, ok := a.Inventory["coat"]; ok {
		t.Errorf("last-unit left an inventory row: %d", a.Inventory["coat"])
	}
	if _, ok := a.GarmentWear["coat"]; ok {
		t.Errorf("last-unit left a wear entry")
	}

	// Wear through two whole units and partway into a third.
	a = &Actor{Inventory: map[ItemKind]int{"coat": 3}}
	res = applyGarmentWear(a, "coat", budget, 250) // 100 + 100 + 50
	if !res.Spent || res.OnHand != 1 || res.MinutesLeft != 50 {
		t.Errorf("multi-unit: %+v, want {spent:true onhand:1 left:50}", res)
	}
	if a.Inventory["coat"] != 1 || a.GarmentWear["coat"] != 50 {
		t.Errorf("multi-unit state: inv=%d wear=%d", a.Inventory["coat"], a.GarmentWear["coat"])
	}

	// Clamp: a wear entry above the (retuned-down) budget is treated as fresh.
	a = &Actor{Inventory: map[ItemKind]int{"coat": 1}, GarmentWear: map[ItemKind]int{"coat": 150}}
	res = applyGarmentWear(a, "coat", budget, 30)
	if res.MinutesLeft != 70 {
		t.Errorf("clamp: left=%d, want 70 (150 clamped to fresh 100, then -30)", res.MinutesLeft)
	}

	// No-op guards: nothing on hand, zero budget, zero minutes.
	a = &Actor{Inventory: map[ItemKind]int{}}
	if res = applyGarmentWear(a, "coat", budget, 30); res.Spent || res.OnHand != 0 {
		t.Errorf("no-inventory: %+v, want no-op", res)
	}
	a = &Actor{Inventory: map[ItemKind]int{"coat": 1}}
	if res = applyGarmentWear(a, "coat", 0, 30); res.Spent {
		t.Errorf("zero-budget spent a unit: %+v", res)
	}
	if res = applyGarmentWear(a, "coat", budget, 0); res.Spent {
		t.Errorf("zero-minutes spent a unit: %+v", res)
	}
}

func TestResolveWarmGarmentTier(t *testing.T) {
	kinds := garmentTestCatalog()
	const frac = 20 // threadbare below 20% of budget → coat threadbare under 120

	cases := []struct {
		name      string
		inventory map[ItemKind]int
		wear      map[ItemKind]int
		want      WarmGarmentTier
	}{
		{"no warms garment", map[ItemKind]int{"bread": 3, "breeches": 1}, nil, WarmGarmentNone},
		{"fresh coat", map[ItemKind]int{"coat": 1}, nil, WarmGarmentSound},
		{"worn but above the line", map[ItemKind]int{"coat": 1}, map[ItemKind]int{"coat": 200}, WarmGarmentSound},
		{"threadbare single coat", map[ItemKind]int{"coat": 1}, map[ItemKind]int{"coat": 60}, WarmGarmentThreadbare},
		{"threadbare in-use but a fresh spare", map[ItemKind]int{"coat": 2}, map[ItemKind]int{"coat": 60}, WarmGarmentSound},
		{"spent-qty coat ignored", map[ItemKind]int{"coat": 0}, nil, WarmGarmentNone},
	}
	for _, c := range cases {
		if got := ResolveWarmGarmentTier(kinds, c.inventory, c.wear, frac); got != c.want {
			t.Errorf("%s: tier = %d, want %d", c.name, got, c.want)
		}
	}

	// Best-across-kinds: a threadbare coat but a fresh cloak → sound (he'd wear the
	// good one). Add a cloak to the catalog for this case.
	kinds["cloak"] = &ItemKindDef{Name: "cloak", Category: "clothing", Capabilities: []string{CapabilityWarms}, WearMinutes: 600}
	inv := map[ItemKind]int{"coat": 1, "cloak": 1}
	wear := map[ItemKind]int{"coat": 60} // coat threadbare, cloak fresh
	if got := ResolveWarmGarmentTier(kinds, inv, wear, frac); got != WarmGarmentSound {
		t.Errorf("threadbare coat + fresh cloak: tier = %d, want Sound", got)
	}
}

// TestActorWearsGarments pins the eligibility gate: a working actor wears, an
// idle one doesn't, and the clothing stockholders (distributor + factor) never
// wear their sale stock even while working.
func TestActorWearsGarments(t *testing.T) {
	w := &World{
		VillageObjects: map[VillageObjectID]*VillageObject{
			"store": {ID: "store", Tags: []string{TagDistributor}},
		},
	}

	working := &Actor{State: StateWorking}
	if !actorWearsGarments(w, working) {
		t.Errorf("on-shift keeper (StateWorking) should wear garments")
	}
	laboring := &Actor{State: StateLaboring}
	if !actorWearsGarments(w, laboring) {
		t.Errorf("hired laborer (StateLaboring) should wear garments")
	}
	producing := &Actor{State: StateIdle, ProductionActivity: &ProductionActivity{}}
	if !actorWearsGarments(w, producing) {
		t.Errorf("mid production cycle should wear garments")
	}
	sourcing := &Actor{State: StateIdle, SourceActivity: &SourceActivity{}}
	if !actorWearsGarments(w, sourcing) {
		t.Errorf("mid source activity should wear garments")
	}

	idle := &Actor{State: StateIdle}
	if actorWearsGarments(w, idle) {
		t.Errorf("idle actor should not wear garments")
	}
	walking := &Actor{State: StateWalking}
	if actorWearsGarments(w, walking) {
		t.Errorf("commuting actor should not wear garments")
	}

	// The distributor is on shift at his distributor-tagged store — his coats are
	// sale stock, not worn clothing, so he never wears them.
	distributor := &Actor{State: StateWorking, WorkStructureID: "store"}
	if actorWearsGarments(w, distributor) {
		t.Errorf("distributor's sale stock must not wear (working at a distributor-tagged store)")
	}
	// The visiting factor likewise holds trade stock, not worn clothing.
	factor := &Actor{State: StateWorking, VisitorState: &VisitorState{DistributorOnly: true}}
	if actorWearsGarments(w, factor) {
		t.Errorf("factor's trade stock must not wear")
	}

	if actorWearsGarments(w, nil) {
		t.Errorf("nil actor should not wear garments")
	}
}

// TestWearGarments runs the sweep: a working actor's held garment loses budget,
// an idle actor's doesn't, the distributor's stock is spared, non-garments are
// untouched, and GarmentWearPerMinute == 0 disables the whole sweep.
func TestWearGarments(t *testing.T) {
	newWorld := func(per int) (*World, *Actor, *Actor, *Actor) {
		worker := &Actor{ID: "worker", State: StateWorking, Inventory: map[ItemKind]int{"coat": 1, "bread": 2}}
		idler := &Actor{ID: "idler", State: StateIdle, Inventory: map[ItemKind]int{"coat": 1}}
		distributor := &Actor{ID: "josiah", State: StateWorking, WorkStructureID: "store", Inventory: map[ItemKind]int{"coat": 2}}
		w := &World{
			Settings:  WorldSettings{GarmentWearPerMinute: per, GarmentThreadbareFractionX100: 20},
			ItemKinds: garmentTestCatalog(),
			VillageObjects: map[VillageObjectID]*VillageObject{
				"store": {ID: "store", Tags: []string{TagDistributor}},
			},
			Actors: map[ActorID]*Actor{"worker": worker, "idler": idler, "josiah": distributor},
		}
		return w, worker, idler, distributor
	}

	// Normal sweep: 30 worked minutes drawn from the worker's coat only.
	w, worker, idler, distributor := newWorld(1)
	if _, err := WearGarments(30).Fn(w); err != nil {
		t.Fatalf("WearGarments: %v", err)
	}
	if worker.GarmentWear["coat"] != 570 { // 600 - 30
		t.Errorf("worker coat wear = %d, want 570", worker.GarmentWear["coat"])
	}
	if _, ok := worker.GarmentWear["bread"]; ok {
		t.Errorf("bread (non-garment) accrued wear")
	}
	if len(idler.GarmentWear) != 0 {
		t.Errorf("idle actor's coat wore: %v", idler.GarmentWear)
	}
	if len(distributor.GarmentWear) != 0 {
		t.Errorf("distributor's sale stock wore: %v", distributor.GarmentWear)
	}

	// Off-switch: per-minute 0 disables the sweep entirely.
	w, worker, _, _ = newWorld(0)
	if _, err := WearGarments(30).Fn(w); err != nil {
		t.Fatalf("WearGarments off: %v", err)
	}
	if len(worker.GarmentWear) != 0 {
		t.Errorf("wear accrued with GarmentWearPerMinute=0: %v", worker.GarmentWear)
	}
}

// TestColdRatePerMinuteX100_ThreadbareGarment pins the LLM-422 wear coupling: a
// SOUND coat caps outdoor storm accrual at the sound rate, a THREADBARE one at
// the worse threadbare rate, and none at the full outdoor rate.
func TestColdRatePerMinuteX100_ThreadbareGarment(t *testing.T) {
	now := time.Now().UTC()
	w, a, _ := coldTestWorld(WeatherStorm, PhaseDay)
	w.ItemKinds = garmentTestCatalog()
	w.Settings.ColdThreadbareGarmentPerMinuteX100 = DefaultColdThreadbareGarmentPerMinuteX100
	w.Settings.GarmentThreadbareFractionX100 = DefaultGarmentThreadbareFractionX100
	a.Inventory = map[ItemKind]int{"coat": 1}

	// Sound coat (no wear entry): capped at the sound garment rate.
	if got := coldRatePerMinuteX100(w, a, now); got != DefaultColdWarmGarmentPerMinuteX100 {
		t.Errorf("sound coat outdoors = %d, want %d", got, DefaultColdWarmGarmentPerMinuteX100)
	}

	// Threadbare coat (60 of 600 → under the 20% line): capped at the WORSE rate.
	a.GarmentWear = map[ItemKind]int{"coat": 60}
	if got := coldRatePerMinuteX100(w, a, now); got != DefaultColdThreadbareGarmentPerMinuteX100 {
		t.Errorf("threadbare coat outdoors = %d, want %d", got, DefaultColdThreadbareGarmentPerMinuteX100)
	}

	// Worn but above the line (200 of 600) → still sound.
	a.GarmentWear = map[ItemKind]int{"coat": 200}
	if got := coldRatePerMinuteX100(w, a, now); got != DefaultColdWarmGarmentPerMinuteX100 {
		t.Errorf("worn-but-sound coat outdoors = %d, want %d", got, DefaultColdWarmGarmentPerMinuteX100)
	}

	// No coat: full outdoor accrual.
	a.Inventory = nil
	a.GarmentWear = nil
	if got := coldRatePerMinuteX100(w, a, now); got != DefaultColdStormOutdoorsPerMinuteX100 {
		t.Errorf("no coat outdoors = %d, want %d", got, DefaultColdStormOutdoorsPerMinuteX100)
	}
}
