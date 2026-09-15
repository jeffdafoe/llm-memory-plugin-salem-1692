package sim_test

import (
	"math/rand"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// shortage_peddler_buyline_test.go — the cascade end of LLM-657: a standing
// buy-line shortage spawns a peddler bound to the keeper who owns his post,
// carrying the bought good alone.

// seedWright places Lewis Walker at a business-tagged, wright-tagged workshop he
// OWNS (OwnerActorID) and works, with no businessowner attribute — the live
// shape — keeping one buy line (whetstone, cap 4) and holding none.
func (vw *visitorWorld) seedWright(t *testing.T) (sim.StructureID, sim.ActorID) {
	t.Helper()
	vw.seedTavern(t) // the neutral anchor
	const shop = sim.StructureID("workshop")
	const keeperID = sim.ActorID("lewis")
	vw.handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"tavern": {ID: "tavern", AssetID: "tavern-asset", Pos: sim.WorldPos{X: 320, Y: 320},
			EntryPolicy: sim.EntryPolicyOpen, Tags: []string{sim.VisitorTagTavern}},
		sim.VillageObjectID(shop): {ID: sim.VillageObjectID(shop), AssetID: "tavern-asset", Pos: sim.WorldPos{X: 640, Y: 640},
			EntryPolicy: sim.EntryPolicyOpen, Tags: []string{sim.TagBusiness, sim.TagWright}, OwnerActorID: keeperID},
	})
	vw.handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "The Tavern"},
		shop:     {ID: shop, DisplayName: "Lewis's Workshop"},
	})
	vw.handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		keeperID: {
			ID: keeperID, DisplayName: "Lewis Walker", Kind: sim.KindNPCShared, State: sim.StateIdle,
			WorkStructureID: shop, InsideStructureID: shop, Pos: sim.WorldPos{X: 640, Y: 640}.Tile(),
			Needs:     map[sim.NeedKey]int{},
			Inventory: map[sim.ItemKind]int{"nail": 6},
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: sim.WhetstoneKind, Source: sim.RestockSourceBuy, Max: 4},
			}},
		},
	})
	vw.handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		sim.WhetstoneKind: {Name: sim.WhetstoneKind, DisplayLabel: "Whetstone", DisplayLabelSingular: "whetstone",
			DisplayLabelPlural: "whetstones", Capabilities: []string{"portable"}},
		"nail": {Name: "nail", DisplayLabel: "Nail", Capabilities: []string{"portable"}},
	})
	return shop, keeperID
}

func TestTickVisitorCascade_WhetstonePeddlerForTheWright(t *testing.T) {
	vw := newVisitorWorld()
	shop, keeperID := vw.seedWright(t)
	w, cancel := vw.load(t)
	defer cancel()

	now := visitorSpawnDaytime
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.VisitorMerchantTrickleChancePermille = 0
		world.Settings.VisitorMerchantCorrectionChancePermille = 0
		world.Settings.VisitorPasserSpawnChancePermille = 0
		world.Settings.VisitorMaxConcurrent = 2
		world.Settings.ShortagePeddlerDays = 3
		world.Settings.ShortagePeddlerBatches = 2
		world.Environment.InputShortages = []sim.InputShortage{
			{KeeperID: keeperID, Item: sim.WhetstoneKind, Days: 3, LastSeenAt: now.Add(-16 * time.Hour)},
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
		if tr == nil || !tr.Peddler || tr.Direction != sim.TradeDirectionSell || tr.Good != sim.WhetstoneKind || tr.Counterparty != shop || tr.Keeper != keeperID {
			t.Errorf("errand = %+v, want a peddler sell of whetstone bound to %q for %s", tr, shop, keeperID)
		}
		if tr != nil && tr.ShipmentQty != 2 {
			t.Errorf("ShipmentQty = %d, want 2 (2 batches × one a batch, under the line's cap of 4)", tr.ShipmentQty)
		}
		if len(peddler.Inventory) != 1 || peddler.Inventory[sim.WhetstoneKind] != 2 {
			t.Errorf("pack = %v, want exactly 2 whetstone", peddler.Inventory)
		}
		if peddler.VisitorState.Archetype != "whetstone-peddler" {
			t.Errorf("archetype = %q, want whetstone-peddler", peddler.VisitorState.Archetype)
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
}
