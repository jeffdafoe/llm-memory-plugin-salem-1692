package sim_test

import (
	"math/rand"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// carter_test.go — the carter's spawn path end to end (sim/carter.go): residue
// at the dairy and a miller with coin and a wheat line bring a carter ahead of
// the rolls, empty-packed, with the route's coin, bound to his first leg; the
// cooldown stamp stops a second; and a standing shortage the village can cover
// goes to the carter, not the peddler.

// seedResidueVillage places the Tavern (the neutral anchor), a dairy where
// Elizabeth holds 25 wheat she has no line for, and a mill where Joseph buys
// wheat, holds 3, and has coin — the live 2026-09-17 shape.
func (vw *visitorWorld) seedResidueVillage(t *testing.T) {
	t.Helper()
	vw.seedTavern(t)
	vw.handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"farm": {ID: "farm", AssetID: "tavern-asset", Pos: sim.WorldPos{X: 160, Y: 160}, EntryPolicy: sim.EntryPolicyOpen, Tags: []string{sim.TagBusiness}},
		"mill": {ID: "mill", AssetID: "tavern-asset", Pos: sim.WorldPos{X: 480, Y: 480}, EntryPolicy: sim.EntryPolicyOpen, Tags: []string{sim.TagBusiness}},
	})
	vw.handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"farm": {ID: "farm", DisplayName: "Ellis Farm"},
		"mill": {ID: "mill", DisplayName: "Mill"},
	})
	vw.handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"liz": {
			ID: "liz", DisplayName: "Elizabeth Ellis", Kind: sim.KindNPCShared, State: sim.StateIdle,
			WorkStructureID: "farm", InsideStructureID: "farm", Pos: sim.WorldPos{X: 160, Y: 160}.Tile(),
			Needs: map[sim.NeedKey]int{}, Coins: 1,
			Inventory:          map[sim.ItemKind]int{"wheat": 25, "milk": 2},
			BusinessownerState: &sim.BusinessownerState{Flavor: "dairykeeper"},
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: "milk", Source: sim.RestockSourceProduce, Max: 30},
			}},
		},
		"joseph": {
			ID: "joseph", DisplayName: "Joseph Scott", Kind: sim.KindNPCShared, State: sim.StateIdle,
			WorkStructureID: "mill", InsideStructureID: "mill", Pos: sim.WorldPos{X: 480, Y: 480}.Tile(),
			Needs: map[sim.NeedKey]int{}, Coins: 700,
			Inventory:          map[sim.ItemKind]int{"wheat": 3, "flour": 10},
			BusinessownerState: &sim.BusinessownerState{Flavor: "miller"},
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: "flour", Source: sim.RestockSourceProduce, Max: 20},
				{Item: "wheat", Source: sim.RestockSourceBuy, Max: 50},
			}},
		},
	})
	vw.handles.Recipes.Seed(map[sim.ItemKind]*sim.ItemRecipe{
		"flour": {OutputItem: "flour", OutputQty: 5, RateQty: 4, RatePerHours: 1, WholesalePrice: 3, RetailPrice: 4,
			Inputs: []sim.RecipeInput{{Item: "wheat", Qty: 5}}},
		"wheat": {OutputItem: "wheat", OutputQty: 1, RateQty: 3, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
		"milk":  {OutputItem: "milk", OutputQty: 4, RateQty: 4, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
	})
	vw.handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"wheat": {Name: "wheat", DisplayLabel: "Wheat", DisplayLabelSingular: "sheaf of wheat", DisplayLabelPlural: "sheaves of wheat", Capabilities: []string{"portable"}},
		"flour": {Name: "flour", DisplayLabel: "Flour", Capabilities: []string{"portable"}},
		"milk":  {Name: "milk", DisplayLabel: "Milk", Capabilities: []string{"portable"}},
	})
}

// rollsOff turns every chance roll off so only the carter or the peddler can
// spawn, and gives the residue village its live reorder rule.
func rollsOff(world *sim.World) {
	world.Settings.VisitorMerchantTrickleChancePermille = 0
	world.Settings.VisitorMerchantCorrectionChancePermille = 0
	world.Settings.VisitorPasserSpawnChancePermille = 0
	world.Settings.VisitorMaxConcurrent = 2
	world.Settings.RestockReorderPct = 25
	world.Settings.CarterDays = 3
}

func TestTickVisitorCascade_Carter(t *testing.T) {
	vw := newVisitorWorld()
	vw.seedResidueVillage(t)
	w, cancel := vw.load(t)
	defer cancel()

	now := visitorSpawnDaytime
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		rollsOff(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	res, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now, Rand: rand.New(rand.NewSource(42))}))
	if err != nil {
		t.Fatalf("TickVisitorCascade: %v", err)
	}
	tm := res.(sim.VisitorCascadeTelemetry)
	if tm.Spawned != 1 || tm.SpawnedCarter != 1 || tm.SpawnedPeddler != 0 {
		t.Fatalf("spawned = %d carter = %d peddler = %d (%s), want 1/1/0", tm.Spawned, tm.SpawnedCarter, tm.SpawnedPeddler, tm.SpawnSkipReason)
	}

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		var carter *sim.Actor
		for _, a := range world.Actors {
			if a.VisitorState != nil {
				carter = a
			}
		}
		if carter == nil {
			t.Fatal("no visitor after the spawn")
		}
		tr := carter.VisitorState.Trade
		if tr == nil || !tr.Carter || tr.Direction != sim.TradeDirectionSell || tr.Peddler {
			t.Fatalf("errand = %+v, want a carter sell errand", tr)
		}
		// Joseph has room for 47 under his cap and coin for far more; the lot is
		// Elizabeth's 25. A buy leg at the farm, then a sell leg at the mill.
		if len(tr.Legs) != 2 || !tr.Legs[0].Buy || tr.Legs[0].Good != "wheat" || tr.Legs[0].Qty != 25 ||
			tr.Legs[0].Counterparty != "farm" || tr.Legs[0].Keeper != "liz" || tr.Legs[0].Unit != 1 ||
			tr.Legs[1].Buy || tr.Legs[1].Good != "wheat" || tr.Legs[1].Qty != 25 || tr.Legs[1].Counterparty != "mill" || tr.Legs[1].Keeper != "joseph" {
			t.Errorf("legs = %+v, want buy 25 wheat at farm from liz, sell 25 wheat at mill to joseph", tr.Legs)
		}
		// The first leg is projected: the arrival target and the cues key on it.
		if tr.Good != "wheat" || tr.Counterparty != "farm" || tr.Keeper != "liz" || tr.ShipmentQty != 0 {
			t.Errorf("projected errand = good %q counterparty %q keeper %q shipment %d, want wheat/farm/liz/0", tr.Good, tr.Counterparty, tr.Keeper, tr.ShipmentQty)
		}
		if len(carter.Inventory) != 0 {
			t.Errorf("pack = %v, want empty — everything he carries, he buys here", carter.Inventory)
		}
		if carter.Coins != 45 || carter.VisitorState.SpendBudget != 45 {
			t.Errorf("purse = %d budget = %d, want 45 (25 for the wheat + 20 travel reserve)", carter.Coins, carter.VisitorState.SpendBudget)
		}
		if carter.VisitorState.Archetype != sim.CarterArchetype {
			t.Errorf("archetype = %q, want carter", carter.VisitorState.Archetype)
		}
		if carter.MoveIntent == nil || carter.MoveIntent.Destination.StructureID == nil ||
			*carter.MoveIntent.Destination.StructureID != "farm" {
			t.Errorf("walk-in = %+v, want straight to the farm", carter.MoveIntent)
		}
		if !world.Environment.LastCarterAt.Equal(now) {
			t.Errorf("LastCarterAt = %v, want %v", world.Environment.LastCarterAt, now)
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("inspect: %v", err)
	}

	// A minute later the carter is on cooldown: no second one.
	res, err = w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now.Add(time.Minute), Rand: rand.New(rand.NewSource(43))}))
	if err != nil {
		t.Fatalf("second TickVisitorCascade: %v", err)
	}
	if tm = res.(sim.VisitorCascadeTelemetry); tm.Spawned != 0 {
		t.Errorf("second tick spawned = %d (%s), want 0 — the carter is on cooldown", tm.Spawned, tm.SpawnSkipReason)
	}
}

// TestTickVisitorCascade_CarterSupersedesPeddler — a standing shortage the
// village's own shelves can cover goes to the carter, cooldown or not, and the
// shortage's peddler stamp is set so neither comes twice for it.
func TestTickVisitorCascade_CarterSupersedesPeddler(t *testing.T) {
	vw := newVisitorWorld()
	vw.seedResidueVillage(t)
	w, cancel := vw.load(t)
	defer cancel()

	now := visitorSpawnDaytime
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		rollsOff(world)
		world.Settings.ShortagePeddlerDays = 3
		// Joseph is broke: no matched leg would be planned for him, and the
		// carter came yesterday — only the shortage brings one now.
		world.Actors["joseph"].Coins = 0
		world.Environment.LastCarterAt = now.Add(-24 * time.Hour)
		world.Environment.InputShortages = []sim.InputShortage{
			{KeeperID: "joseph", Item: "wheat", Days: 3, LastSeenAt: now.Add(-16 * time.Hour)},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now, Rand: rand.New(rand.NewSource(7))}))
	if err != nil {
		t.Fatalf("TickVisitorCascade: %v", err)
	}
	tm := res.(sim.VisitorCascadeTelemetry)
	if tm.Spawned != 1 || tm.SpawnedCarter != 1 || tm.SpawnedPeddler != 0 {
		t.Fatalf("spawned = %d carter = %d peddler = %d (%s), want the carter, not the peddler", tm.Spawned, tm.SpawnedCarter, tm.SpawnedPeddler, tm.SpawnSkipReason)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		s := world.Environment.InputShortages
		if len(s) != 1 || !s[0].LastPeddlerAt.Equal(now) {
			t.Errorf("shortage stamp = %+v, want LastPeddlerAt = now — the carter's run counts as the peddler's", s)
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("inspect: %v", err)
	}

	// With the carter off, the same shortage brings the peddler as before.
	vw2 := newVisitorWorld()
	vw2.seedResidueVillage(t)
	w2, cancel2 := vw2.load(t)
	defer cancel2()
	if _, err := w2.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		rollsOff(world)
		world.Settings.CarterDays = 0
		world.Settings.ShortagePeddlerDays = 3
		world.Actors["joseph"].Coins = 0
		world.Environment.InputShortages = []sim.InputShortage{
			{KeeperID: "joseph", Item: "wheat", Days: 3, LastSeenAt: now.Add(-16 * time.Hour)},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err = w2.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now, Rand: rand.New(rand.NewSource(7))}))
	if err != nil {
		t.Fatalf("TickVisitorCascade: %v", err)
	}
	if tm = res.(sim.VisitorCascadeTelemetry); tm.Spawned != 1 || tm.SpawnedPeddler != 1 || tm.SpawnedCarter != 0 {
		t.Errorf("carter off: spawned = %d peddler = %d carter = %d (%s), want the peddler", tm.Spawned, tm.SpawnedPeddler, tm.SpawnedCarter, tm.SpawnSkipReason)
	}
}
