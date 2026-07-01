package sim

import (
	"testing"
	"time"
)

// farm_upkeep_test.go — LLM-215. Internal (package sim) tests for the obligation
// math, the daily assessment (consume + warrant), the off-switch, and the
// scope/settings predicates. They reach the unexported assessFarmUpkeep /
// farmUpkeepWarrantEligible directly against a hand-built World, mirroring
// stall_wear_internal_test.go.

func farmTestWorld(floor, perShovel, coins, shovels int) (*World, *Actor, *VillageObject) {
	owner := &Actor{ID: "elizabeth", Kind: KindNPCShared, LLMAgent: "elizabeth", Coins: coins, Inventory: map[ItemKind]int{}}
	if shovels > 0 {
		owner.Inventory[ShovelItemKind] = shovels
	}
	farm := &VillageObject{ID: "ellis-farm", OwnerActorID: owner.ID, Tags: []string{TagFarm}}
	w := &World{
		Settings: WorldSettings{
			FarmUpkeepFloor:          floor,
			FarmUpkeepCoinsPerShovel: perShovel,
			MaxWarrantsPerActor:      16,
		},
		Actors:         map[ActorID]*Actor{owner.ID: owner},
		VillageObjects: map[VillageObjectID]*VillageObject{farm.ID: farm},
	}
	return w, owner, farm
}

func hasFarmUpkeepWarrant(a *Actor) bool {
	for _, wm := range a.Warrants {
		if _, ok := wm.Reason.(FarmUpkeepWarrantReason); ok {
			return true
		}
	}
	return false
}

func TestFarmUpkeepObligation(t *testing.T) {
	cases := []struct {
		coins, floor, perShovel, want int
	}{
		{95, 30, 20, 3},  // Elizabeth: (95-30)/20 = 3
		{49, 30, 20, 0},  // Moses: 19 above the floor, less than one band
		{30, 30, 20, 0},  // exactly at the floor
		{29, 30, 20, 0},  // below the floor
		{130, 30, 20, 5}, // (130-30)/20 = 5
		{95, 30, 0, 0},   // perShovel 0 disables (off-switch)
		{95, 30, -1, 0},  // negative disables
		{50, 0, 20, 2},   // floor 0: taxed from the first coin, 50/20 = 2
	}
	for _, c := range cases {
		if got := FarmUpkeepObligation(c.coins, c.floor, c.perShovel); got != c.want {
			t.Errorf("FarmUpkeepObligation(coins=%d,floor=%d,per=%d) = %d, want %d", c.coins, c.floor, c.perShovel, got, c.want)
		}
	}
}

func TestAssessFarmUpkeep_ConsumesAndWarrants(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	// Elizabeth: 95 coins, floor 30, band 20 → owes 3. Holds 2 worn shovels.
	w, owner, _ := farmTestWorld(30, 20, 95, 2)
	assessFarmUpkeep(w, now)
	if owner.Inventory[ShovelItemKind] != 0 {
		t.Errorf("upkeep shovels should wear out (be consumed), have %d", owner.Inventory[ShovelItemKind])
	}
	if owner.Coins != 95 {
		t.Errorf("assessment must not touch coins (only the purchase does), have %d", owner.Coins)
	}
	if !hasFarmUpkeepWarrant(owner) {
		t.Error("expected a farm-upkeep warrant when the obligation is > 0")
	}
}

func TestAssessFarmUpkeep_OffSwitchAndBelowFloor(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()

	// perShovel == 0 disables the whole feature: no consume, no warrant.
	w, owner, _ := farmTestWorld(30, 0, 95, 2)
	assessFarmUpkeep(w, now)
	if owner.Inventory[ShovelItemKind] != 2 {
		t.Errorf("off-switch must not consume shovels, have %d", owner.Inventory[ShovelItemKind])
	}
	if hasFarmUpkeepWarrant(owner) {
		t.Error("off-switch must not warrant")
	}

	// At/below the floor: shovels still wear out, but nothing is owed → no warrant.
	w2, owner2, _ := farmTestWorld(30, 20, 25, 1)
	assessFarmUpkeep(w2, now)
	if owner2.Inventory[ShovelItemKind] != 0 {
		t.Errorf("a below-floor farm's shovels still wear out, have %d", owner2.Inventory[ShovelItemKind])
	}
	if hasFarmUpkeepWarrant(owner2) {
		t.Error("a below-floor farm owes nothing → no warrant")
	}
}

func TestAssessFarmUpkeep_NonAgentOwnerNoWarrant(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	w, owner, _ := farmTestWorld(30, 20, 95, 1)
	owner.Kind = KindPC // not an agent-backed NPC
	assessFarmUpkeep(w, now)
	if owner.Inventory[ShovelItemKind] != 0 {
		t.Errorf("shovels wear out regardless of who owns the farm, have %d", owner.Inventory[ShovelItemKind])
	}
	if hasFarmUpkeepWarrant(owner) {
		t.Error("a non-agent-backed owner is never warranted")
	}
}

func TestAssessFarmUpkeep_DuplicateFarmAssessedOnce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	w, owner, _ := farmTestWorld(30, 20, 95, 3)
	// A second farm tagged to the same owner (live-tag drift breaking the one-farm
	// data convention): the pass must assess the owner once, not once per object.
	w.VillageObjects["james-farm"] = &VillageObject{ID: "james-farm", OwnerActorID: owner.ID, Tags: []string{TagFarm}}
	assessFarmUpkeep(w, now)
	if owner.Inventory[ShovelItemKind] != 0 {
		t.Errorf("shovels should be consumed once, have %d", owner.Inventory[ShovelItemKind])
	}
	count := 0
	for _, wm := range owner.Warrants {
		if _, ok := wm.Reason.(FarmUpkeepWarrantReason); ok {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 farm-upkeep warrant across duplicate farms, got %d", count)
	}
}

func TestFarmPredicates(t *testing.T) {
	owned := &VillageObject{ID: "f", OwnerActorID: "elizabeth", Tags: []string{TagFarm}}
	unowned := &VillageObject{ID: "u", Tags: []string{TagFarm}}
	untagged := &VillageObject{ID: "n", OwnerActorID: "elizabeth"}

	if !IsFarmStructure(owned) {
		t.Error("owned + tagged should be a farm structure")
	}
	if IsFarmStructure(unowned) {
		t.Error("an unowned farm is not assessable (no owner to hold coin / buy shovels)")
	}
	if IsFarmStructure(untagged) {
		t.Error("an untagged object is not a farm structure")
	}
	if IsFarmStructure(nil) {
		t.Error("nil is not a farm structure")
	}

	objects := map[VillageObjectID]*VillageObject{"f": owned, "u": unowned}
	if got := OwnedFarm(objects, "elizabeth"); got != owned {
		t.Errorf("OwnedFarm = %v, want the owned farm", got)
	}
	if got := OwnedFarm(objects, "nobody"); got != nil {
		t.Errorf("OwnedFarm for a non-owner = %v, want nil", got)
	}
}

func TestSetFarmUpkeepSettings_Validation(t *testing.T) {
	ip := func(v int) *int { return &v }
	world := func() *World {
		return &World{Settings: WorldSettings{FarmUpkeepFloor: 30, FarmUpkeepCoinsPerShovel: 20}}
	}
	bad := []struct {
		name        string
		floor, band *int
	}{
		{"none provided", nil, nil},
		{"negative floor", ip(-1), nil},
		{"negative band", nil, ip(-5)},
	}
	for _, c := range bad {
		if _, err := SetFarmUpkeepSettings(c.floor, c.band).Fn(world()); err == nil {
			t.Errorf("%s: expected rejection, got nil", c.name)
		}
	}
	// floor 0 (tax from the first coin) and band 0 (disable) are both valid.
	w := world()
	if _, err := SetFarmUpkeepSettings(ip(0), ip(0)).Fn(w); err != nil {
		t.Fatalf("floor=0/band=0 should be valid: %v", err)
	}
	if w.Settings.FarmUpkeepFloor != 0 || w.Settings.FarmUpkeepCoinsPerShovel != 0 {
		t.Errorf("settings not applied: %+v", w.Settings)
	}
	w2 := world()
	if _, err := SetFarmUpkeepSettings(ip(40), ip(25)).Fn(w2); err != nil {
		t.Fatalf("valid change rejected: %v", err)
	}
	if w2.Settings.FarmUpkeepFloor != 40 || w2.Settings.FarmUpkeepCoinsPerShovel != 25 {
		t.Errorf("settings not applied: %+v", w2.Settings)
	}
}
