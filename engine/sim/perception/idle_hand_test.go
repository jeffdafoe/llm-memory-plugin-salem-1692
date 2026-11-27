package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// idleHandFixture is a two-good producer (nails from iron, shovels from iron):
// enough to exercise the per-good full/short split and the "any craftable good
// means no line" rule.
func idleHandFixture(inv map[sim.ItemKind]int) (*sim.Snapshot, *sim.ActorSnapshot) {
	smith := &sim.ActorSnapshot{
		DisplayName: "Ezekiel Crane",
		Inventory:   inv,
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "nail", Source: sim.RestockSourceProduce, Max: 20},
			{Item: "shovel", Source: sim.RestockSourceProduce, Max: 4},
			{Item: "iron", Source: sim.RestockSourceBuy, Max: 10},
		}},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"ezekiel": smith},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"nail":   {OutputItem: "nail", OutputQty: 10, RateQty: 10, RatePerHours: 1, Inputs: []sim.RecipeInput{{Item: "iron", Qty: 1}}},
			"shovel": {OutputItem: "shovel", OutputQty: 2, RateQty: 1, RatePerHours: 1, Inputs: []sim.RecipeInput{{Item: "iron", Qty: 2}}},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"nail":   {Name: "nail", DisplayLabel: "Nail", DisplayLabelSingular: "nail", DisplayLabelPlural: "nails"},
			"shovel": {Name: "shovel", DisplayLabel: "Shovel", DisplayLabelSingular: "shovel", DisplayLabelPlural: "shovels"},
			"iron":   {Name: "iron", DisplayLabel: "iron"},
		},
	}
	return snap, smith
}

// wornStallFixture is a business object owned by owner at the given wear — the
// minimum sim.IsWearableStall needs (an owner and the business tag).
func wornStallFixture(owner sim.ActorID, wear int) *sim.VillageObject {
	return &sim.VillageObject{ID: "stall-1", OwnerActorID: owner, Tags: []string{sim.TagBusiness}, Wear: wear}
}

func renderIdleHandText(v *IdleHandView) string {
	var b strings.Builder
	renderIdleHand(&b, v)
	return b.String()
}

func TestBuildIdleHand_AnyCraftableGoodMeansNoLine(t *testing.T) {
	// Nails full (15 + 10 > 20), but a shovel batch fits (2 + 2 <= 4) and iron is on hand.
	snap, smith := idleHandFixture(map[sim.ItemKind]int{"nail": 15, "shovel": 2, "iron": 5})
	if v := buildIdleHand(snap, "ezekiel", smith); v != nil {
		t.Fatalf("a shovel batch can start, want nil, got %+v", v)
	}
}

func TestBuildIdleHand_AllFull(t *testing.T) {
	snap, smith := idleHandFixture(map[sim.ItemKind]int{"nail": 15, "shovel": 3, "iron": 5})
	got := renderIdleHandText(buildIdleHand(snap, "ezekiel", smith))
	want := "Your stores of nails and shovels stand full, with no room for another batch until some is sold — a hand at your post would have nothing to hurry.\n"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestBuildIdleHand_FullAndShortJoin(t *testing.T) {
	// Nails full; shovels have room but need 2 iron and there is 1.
	snap, smith := idleHandFixture(map[sim.ItemKind]int{"nail": 15, "shovel": 0, "iron": 1})
	got := renderIdleHandText(buildIdleHand(snap, "ezekiel", smith))
	want := "Your stores of nails stand full, with no room for another batch until some is sold, and you have not the iron for another batch of shovels — a hand at your post would have nothing to hurry.\n"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestBuildIdleHand_BatchRunningMeansNoLine(t *testing.T) {
	snap, smith := idleHandFixture(map[sim.ItemKind]int{"nail": 15, "shovel": 3, "iron": 5})
	smith.ProductionItem = "nail"
	if v := buildIdleHand(snap, "ezekiel", smith); v != nil {
		t.Fatalf("a running batch is what the boost hurries, want nil, got %+v", v)
	}
}

func TestBuildIdleHand_NonProducerMeansNoLine(t *testing.T) {
	snap, smith := idleHandFixture(map[sim.ItemKind]int{"iron": 5})
	smith.RestockPolicy = &sim.RestockPolicy{Restock: []sim.RestockEntry{{Item: "iron", Source: sim.RestockSourceBuy, Max: 10}}}
	if v := buildIdleHand(snap, "ezekiel", smith); v != nil {
		t.Fatalf("a reseller makes nothing, want nil, got %+v", v)
	}
}

func TestBuildIdleHand_UncappedGoodAlwaysHasRoom(t *testing.T) {
	snap, smith := idleHandFixture(map[sim.ItemKind]int{"nail": 500, "iron": 5})
	smith.RestockPolicy = &sim.RestockPolicy{Restock: []sim.RestockEntry{{Item: "nail", Source: sim.RestockSourceProduce}}}
	if v := buildIdleHand(snap, "ezekiel", smith); v != nil {
		t.Fatalf("an uncapped good never fills, want nil, got %+v", v)
	}
}

func TestBuildIdleHand_WornStallMeansNoLine(t *testing.T) {
	// Full stores, but the smith's own stall is worn to the repair threshold: a
	// hired hand mends the employer's stall, so there is work for one.
	snap, smith := idleHandFixture(map[sim.ItemKind]int{"nail": 15, "shovel": 3, "iron": 5})
	stall := wornStallFixture("ezekiel", 200)
	snap.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{stall.ID: stall}
	snap.StallWearRepairThreshold = 180
	snap.StallWearDegradeThreshold = 270
	if !sim.StallRepairable(stall, snap.StallWearRepairThreshold, snap.StallWearDegradeThreshold) ||
		sim.OwnedWearableStall(snap.VillageObjects, "ezekiel") == nil {
		t.Fatal("fixture: the stall must be the smith's own and worn to the repair threshold")
	}
	if v := buildIdleHand(snap, "ezekiel", smith); v != nil {
		t.Fatalf("a worn stall is work for a hand, want nil, got %+v", v)
	}
	// The same stall below the threshold no longer suppresses.
	stall.Wear = 100
	if v := buildIdleHand(snap, "ezekiel", smith); v == nil {
		t.Fatal("a sound stall leaves the hand nothing to do, want the line")
	}
}
