package sim

import "testing"

// stall_wares_test.go — LLM-753 coverage of the stall shelf: which held goods
// are wares, and how much of each is spare.

func stallWaresSnapshot() *Snapshot {
	return &Snapshot{
		ItemKinds: map[ItemKind]*ItemKindDef{
			"nail":    {Name: "nail"},
			"water":   {Name: "water", Category: ItemCategoryDrink},
			"iron":    {Name: "iron"},
			"flour":   {Name: "flour", Category: ItemCategoryFood},
			"cheese":  {Name: "cheese", Category: ItemCategoryFood},
			"mending": {Name: "mending", Capabilities: []string{"service"}},
			"salt":    {Name: "salt"},
			"vest":    {Name: "vest"},
		},
		Recipes: map[ItemKind]*ItemRecipe{
			// Each nail batch takes one water: a reorder floor of two batches.
			"nail": {OutputItem: "nail", OutputQty: 5, Inputs: []RecipeInput{{Item: "water", Qty: 1}}},
		},
		VillageObjects: map[VillageObjectID]*VillageObject{
			"smithy": {ID: "smithy"},
			"store":  {ID: "store", Tags: []string{TagDistributor}},
		},
		Actors: map[ActorID]*ActorSnapshot{
			"smith": {
				Kind: KindNPCStateful, WorkStructureID: "smithy",
				RestockPolicy: &RestockPolicy{Restock: []RestockEntry{
					{Item: "nail", Source: RestockSourceProduce},
					{Item: "water", Source: RestockSourceForage},
					{Item: "mending", Source: RestockSourceProduce},
					{Item: "iron", Source: RestockSourceBuy, Max: 10},
				}},
				Inventory: map[ItemKind]int{
					"nail":    16, // made here: all spare
					"water":   21, // gathered here, a nail input: 21 less the floor of 2
					"iron":    4,  // bought in to work with: not a ware
					"flour":   20, // carried, not his trade: not a ware
					"mending": 3,  // a service: never on a shelf
				},
			},
			// A second hand at the smithy who also makes nails: the shelf sums both.
			"apprentice": {
				Kind: KindNPCShared, WorkStructureID: "smithy",
				RestockPolicy: &RestockPolicy{Restock: []RestockEntry{{Item: "nail", Source: RestockSourceProduce}}},
				Inventory:     map[ItemKind]int{"nail": 2},
			},
			// A player who works there is not a keeper.
			"player": {
				Kind: KindPC, WorkStructureID: "smithy",
				RestockPolicy: &RestockPolicy{Restock: []RestockEntry{{Item: "nail", Source: RestockSourceProduce}}},
				Inventory:     map[ItemKind]int{"nail": 50},
			},
			// The distributor sells everything he holds, with or without a line.
			"shopkeeper": {
				Kind: KindNPCStateful, WorkStructureID: "store",
				RestockPolicy: &RestockPolicy{Restock: []RestockEntry{{Item: "cheese", Source: RestockSourceBuy, Max: 20}}},
				Inventory:     map[ItemKind]int{"cheese": 6, "salt": 16, "vest": 1, "mending": 2},
			},
		},
	}
}

func TestStallWares_MakerSellsWhatHeMakesAndGathersAboveHisFloor(t *testing.T) {
	got := StallWares(stallWaresSnapshot(), "smithy")
	want := []StallWare{{Kind: "nail", Spare: 18}, {Kind: "water", Spare: 19}}
	if len(got) != len(want) {
		t.Fatalf("StallWares(smithy) = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("StallWares(smithy)[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestStallWares_WaterAtTheFloorIsNotAWare(t *testing.T) {
	snap := stallWaresSnapshot()
	snap.Actors["smith"].Inventory["water"] = 2
	for _, ware := range StallWares(snap, "smithy") {
		if ware.Kind == "water" {
			t.Fatalf("water at the reorder floor is on the shelf: %+v", ware)
		}
	}
}

func TestStallWares_DistributorSellsEverythingHeHolds(t *testing.T) {
	got := StallWares(stallWaresSnapshot(), "store")
	want := []StallWare{{Kind: "cheese", Spare: 6}, {Kind: "salt", Spare: 16}, {Kind: "vest", Spare: 1}}
	if len(got) != len(want) {
		t.Fatalf("StallWares(store) = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("StallWares(store)[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestStallWares_UnknownOrEmptyStructure(t *testing.T) {
	snap := stallWaresSnapshot()
	if got := StallWares(snap, "nowhere"); len(got) != 0 {
		t.Errorf("StallWares(nowhere) = %+v, want none", got)
	}
	if got := StallWares(snap, ""); got != nil {
		t.Errorf("StallWares(\"\") = %+v, want nil", got)
	}
	if got := StallWares(nil, "smithy"); got != nil {
		t.Errorf("StallWares(nil snapshot) = %+v, want nil", got)
	}
}

func TestFirstHandSupplier(t *testing.T) {
	objects := stallWaresSnapshot().VillageObjects
	maker := &RestockPolicy{Restock: []RestockEntry{
		{Item: "nail", Source: RestockSourceProduce},
		{Item: "water", Source: RestockSourceForage},
		{Item: "iron", Source: RestockSourceBuy},
	}}
	cases := []struct {
		name string
		work StructureID
		p    *RestockPolicy
		kind ItemKind
		want bool
	}{
		{"produce line", "smithy", maker, "nail", true},
		{"forage line", "smithy", maker, "water", true},
		{"buy line", "smithy", maker, "iron", false},
		{"no line", "smithy", maker, "flour", false},
		{"distributor, no line", "store", nil, "flour", true},
		{"no policy, no shop", "smithy", nil, "nail", false},
	}
	for _, c := range cases {
		if got := FirstHandSupplier(objects, c.work, c.p, c.kind); got != c.want {
			t.Errorf("%s: FirstHandSupplier = %v, want %v", c.name, got, c.want)
		}
	}
}
