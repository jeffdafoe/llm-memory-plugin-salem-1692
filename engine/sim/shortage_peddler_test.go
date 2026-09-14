package sim_test

import (
	"math/rand"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// shortage_peddler_test.go — LLM-656, the spawn path end to end: a standing
// shortage takes the visitor tick's spawn ahead of the rolls, the traveler
// arrives as a peddler carrying the missing good and bound to the short
// keeper's shop, and the shortage's cooldown stamp stops a second one.

// seedShortKeeper places the Tavern with John inside, making stew (2 meat a
// batch) and holding one cut — the live shape. Recipes and kinds ride with it.
func (vw *visitorWorld) seedShortKeeper(t *testing.T) (sim.StructureID, sim.ActorID) {
	t.Helper()
	const shop = sim.StructureID("tavern")
	vw.seedTavern(t) // the neutral anchor + the shop itself, tagged tavern
	const keeperID = sim.ActorID("john")
	vw.handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		keeperID: {
			ID: keeperID, DisplayName: "John Ellis", Kind: sim.KindNPCStateful, State: sim.StateIdle,
			WorkStructureID: shop, InsideStructureID: shop, Pos: sim.WorldPos{X: 320, Y: 320}.Tile(),
			Needs:              map[sim.NeedKey]int{},
			Inventory:          map[sim.ItemKind]int{"meat": 1, "water": 10},
			BusinessownerState: &sim.BusinessownerState{Flavor: "tavernkeeper"},
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: "stew", Source: sim.RestockSourceProduce, Max: 30},
				{Item: "meat", Source: sim.RestockSourceBuy, Max: 12},
			}},
		},
	})
	vw.handles.Recipes.Seed(map[sim.ItemKind]*sim.ItemRecipe{
		"stew": {OutputItem: "stew", OutputQty: 6, RateQty: 30, RatePerHours: 6,
			Inputs: []sim.RecipeInput{{Item: "meat", Qty: 2}, {Item: "water", Qty: 3}}},
	})
	vw.handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"meat":  {Name: "meat", DisplayLabel: "Meat", DisplayLabelSingular: "cut of meat", DisplayLabelPlural: "cuts of meat", Capabilities: []string{"portable"}},
		"water": {Name: "water", DisplayLabel: "Water", Capabilities: []string{"portable"}},
		"stew":  {Name: "stew", DisplayLabel: "Stew"},
	})
	return shop, keeperID
}

func TestTickVisitorCascade_ShortagePeddler(t *testing.T) {
	vw := newVisitorWorld()
	shop, _ := vw.seedShortKeeper(t)
	w, cancel := vw.load(t)
	defer cancel()

	now := visitorSpawnDaytime
	// Every roll off, so only the shortage can spawn; the shortage has stood the
	// threshold; room for two visitors so the cooldown, not the cap, is what
	// stops the second.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.VisitorMerchantTrickleChancePermille = 0
		world.Settings.VisitorMerchantCorrectionChancePermille = 0
		world.Settings.VisitorPasserSpawnChancePermille = 0
		world.Settings.VisitorMaxConcurrent = 2
		world.Settings.ShortagePeddlerDays = 3
		world.Settings.ShortagePeddlerBatches = 2
		world.Environment.InputShortages = []sim.InputShortage{
			{KeeperID: "john", Item: "meat", Days: 3, LastSeenAt: now.Add(-16 * time.Hour)},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	res, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now, Rand: rand.New(rand.NewSource(42))}))
	if err != nil {
		t.Fatalf("TickVisitorCascade: %v", err)
	}
	tm := res.(sim.VisitorCascadeTelemetry)
	if tm.Spawned != 1 || tm.SpawnedPeddler != 1 {
		t.Fatalf("spawned = %d peddler = %d (%s), want 1/1", tm.Spawned, tm.SpawnedPeddler, tm.SpawnSkipReason)
	}

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		var peddler *sim.Actor
		for _, a := range world.Actors {
			if a.VisitorState != nil {
				peddler = a
			}
		}
		if peddler == nil {
			t.Fatal("no visitor after the spawn")
		}
		tr := peddler.VisitorState.Trade
		if tr == nil || !tr.Peddler || tr.Direction != sim.TradeDirectionSell || tr.Good != "meat" || tr.Counterparty != shop || tr.Keeper != "john" {
			t.Errorf("errand = %+v, want a peddler sell of meat bound to %q for john", tr, shop)
		}
		if tr != nil && tr.ShipmentQty != 4 {
			t.Errorf("ShipmentQty = %d, want 4 (2 batches × 2 a batch)", tr.ShipmentQty)
		}
		if len(peddler.Inventory) != 1 || peddler.Inventory["meat"] != 4 {
			t.Errorf("pack = %v, want exactly 4 meat", peddler.Inventory)
		}
		if peddler.Coins < 30 || peddler.Coins > 50 || peddler.VisitorState.SpendBudget != peddler.Coins {
			t.Errorf("purse = %d budget = %d, want a traveler's 30..50 with budget = purse", peddler.Coins, peddler.VisitorState.SpendBudget)
		}
		if peddler.VisitorState.Archetype != "meat-peddler" {
			t.Errorf("archetype = %q, want meat-peddler", peddler.VisitorState.Archetype)
		}
		if peddler.VisitorState.Origin == sim.FactorOrigin {
			t.Errorf("origin = %q — a peddler is a country dealer, not a city factor", peddler.VisitorState.Origin)
		}
		if peddler.MoveIntent == nil || peddler.MoveIntent.Destination.StructureID == nil ||
			*peddler.MoveIntent.Destination.StructureID != shop {
			t.Errorf("walk-in = %+v, want straight to %q", peddler.MoveIntent, shop)
		}
		s := world.Environment.InputShortages
		if len(s) != 1 || !s[0].LastPeddlerAt.Equal(now) {
			t.Errorf("shortage stamp = %+v, want LastPeddlerAt = now", s)
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("inspect: %v", err)
	}

	// A minute later the same shortage is on cooldown: no second peddler.
	res, err = w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now.Add(time.Minute), Rand: rand.New(rand.NewSource(43))}))
	if err != nil {
		t.Fatalf("second TickVisitorCascade: %v", err)
	}
	if tm = res.(sim.VisitorCascadeTelemetry); tm.Spawned != 0 {
		t.Errorf("second tick spawned = %d (%s), want 0 — the shortage is on cooldown", tm.Spawned, tm.SpawnSkipReason)
	}
}

// TestTickVisitorCascade_ShortagePeddlerOff — the off-switch, the threshold, and
// a shortage the village resolved since the sweep: none of them spawns, and the
// resolved one is dropped from the record on the spot (code_review).
func TestTickVisitorCascade_ShortagePeddlerOff(t *testing.T) {
	for _, tc := range []struct {
		name     string
		days     int
		age      int
		meat     int // the keeper's meat at tick time (1 = still short of a 2-a-batch stew)
		wantKept int // stored shortages after the tick
	}{
		{"off switch", 0, 5, 1, 1},
		{"under threshold", 3, 2, 1, 1},
		{"resolved since the sweep", 3, 5, 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vw := newVisitorWorld()
			vw.seedShortKeeper(t)
			w, cancel := vw.load(t)
			defer cancel()
			now := visitorSpawnDaytime
			if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
				world.Settings.VisitorMerchantTrickleChancePermille = 0
				world.Settings.VisitorMerchantCorrectionChancePermille = 0
				world.Settings.VisitorPasserSpawnChancePermille = 0
				world.Settings.ShortagePeddlerDays = tc.days
				world.Actors["john"].Inventory["meat"] = tc.meat
				world.Environment.InputShortages = []sim.InputShortage{
					{KeeperID: "john", Item: "meat", Days: tc.age, LastSeenAt: now.Add(-16 * time.Hour)},
				}
				return nil, nil
			}}); err != nil {
				t.Fatalf("seed settings: %v", err)
			}
			res, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: now, Rand: rand.New(rand.NewSource(1))}))
			if err != nil {
				t.Fatalf("TickVisitorCascade: %v", err)
			}
			if tm := res.(sim.VisitorCascadeTelemetry); tm.Spawned != 0 {
				t.Errorf("spawned = %d, want 0", tm.Spawned)
			}
			if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
				if got := len(world.Environment.InputShortages); got != tc.wantKept {
					t.Errorf("stored shortages after the tick = %d, want %d", got, tc.wantKept)
				}
				return nil, nil
			}}); err != nil {
				t.Fatalf("inspect: %v", err)
			}
		})
	}
}
