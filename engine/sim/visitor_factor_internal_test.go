package sim

import (
	"math/rand"
	"testing"
)

// visitor_factor_internal_test.go — wholesale factor spawn internals: the coin-valve direction
// (LLM-455), the factor pack seed, the sell-errand binding, and the arrival picker. Package-
// internal (these helpers are unexported); the end-to-end spawn wiring is in visitor_factor_test.go.

// TestChooseVisitorTradeDirection — the coin-valve (LLM-455): a configured band forces a seller
// when resident coin is hot and a buyer when it is starved; unbanded / in-band leaves it to the
// weighted random, where sell weight 1000 always sells and 0 never does.
func TestChooseVisitorTradeDirection(t *testing.T) {
	resident := func(coins int) *World {
		return &World{Actors: map[ActorID]*Actor{"r": {ID: "r", Kind: KindNPCShared, Coins: coins}}}
	}
	r := rand.New(rand.NewSource(1))

	// Band [500,900]: hot -> sell, starved -> buy.
	hot := resident(1000)
	hot.Settings = WorldSettings{VisitorCoinBandLow: 500, VisitorCoinBandHigh: 900}
	if got := chooseVisitorTradeDirection(hot, r); got != TradeDirectionSell {
		t.Errorf("resident coin above high-water: direction = %q, want sell", got)
	}
	starved := resident(100)
	starved.Settings = WorldSettings{VisitorCoinBandLow: 500, VisitorCoinBandHigh: 900}
	if got := chooseVisitorTradeDirection(starved, r); got != TradeDirectionBuy {
		t.Errorf("resident coin below low-water: direction = %q, want buy", got)
	}

	// Unbanded: weighted random. Sell weight 1000 -> always sell; 0 -> always buy.
	allSell := resident(600)
	allSell.Settings = WorldSettings{VisitorSellWeightPermille: 1000}
	if got := chooseVisitorTradeDirection(allSell, r); got != TradeDirectionSell {
		t.Errorf("sell weight 1000: direction = %q, want sell", got)
	}
	allBuy := resident(600)
	allBuy.Settings = WorldSettings{VisitorSellWeightPermille: 0}
	if got := chooseVisitorTradeDirection(allBuy, r); got != TradeDirectionBuy {
		t.Errorf("sell weight 0: direction = %q, want buy", got)
	}
}

// TestSeedFactorPack — a factor carries every factorWareKind (unitsPerKind..+1 of each),
// an iron shipment (ironUnits..+2 — LLM-442), a salt shipment (saltUnits..+2 — LLM-444),
// and a purse inside the configured [min,max]; a min==max range gives a fixed purse.
func TestSeedFactorPack(t *testing.T) {
	valid := map[ItemKind]bool{factorIronKind: true, factorSaltKind: true}
	for _, k := range factorWareKinds {
		valid[k] = true
	}
	for seed := int64(0); seed < 50; seed++ {
		pack, purse := seedFactorPack(rand.New(rand.NewSource(seed)), 2, 10, 12, 120, 200)
		if len(pack) != len(factorWareKinds)+2 {
			t.Fatalf("seed %d: pack has %d kinds, want %d (one per factorWareKind plus iron and salt)", seed, len(pack), len(factorWareKinds)+2)
		}
		for kind, qty := range pack {
			if !valid[kind] {
				t.Errorf("seed %d: pack carries %q, not a factorWareKind", seed, kind)
			}
			if kind == factorIronKind {
				if qty < 10 || qty > 12 {
					t.Errorf("seed %d: iron qty %d out of [10,12]", seed, qty)
				}
				continue
			}
			if kind == factorSaltKind {
				if qty < 12 || qty > 14 {
					t.Errorf("seed %d: salt qty %d out of [12,14]", seed, qty)
				}
				continue
			}
			if qty < 2 || qty > 3 {
				t.Errorf("seed %d: %q qty %d out of [2,3]", seed, kind, qty)
			}
		}
		if purse < 120 || purse > 200 {
			t.Errorf("seed %d: purse %d out of [120,200]", seed, purse)
		}
	}
	if _, purse := seedFactorPack(rand.New(rand.NewSource(1)), 1, 1, 1, 150, 150); purse != 150 {
		t.Errorf("purse = %d, want 150 when min==max", purse)
	}
}

// TestCloneVisitorState_Trade guards that the clone/snapshot copy path DEEP-copies the Trade
// errand (LLM-455). cloneVisitorState backs ActorSnapshot publication (world.go), the mem-repo
// boundary, and the ActorDeparted event; a copy that dropped or aliased Trade would let a live
// merchant lose its gate — or have a snapshot mutate the world's errand — even though the
// plan-jsonb persistence round-trips.
func TestCloneVisitorState_Trade(t *testing.T) {
	src := &VisitorState{Archetype: FactorArchetype, Origin: FactorOrigin,
		Trade: &TradeErrand{Direction: TradeDirectionSell, Good: factorIronKind, Counterparty: "store_a"}}
	cp := cloneVisitorState(src)
	if cp == nil || cp.Trade == nil {
		t.Fatalf("cloneVisitorState dropped Trade: %+v", cp)
	}
	if cp.Trade == src.Trade {
		t.Error("cloneVisitorState aliased the Trade pointer instead of deep-copying")
	}
	if cp.Trade.Direction != TradeDirectionSell || cp.Trade.Counterparty != "store_a" {
		t.Errorf("cloneVisitorState garbled Trade: %+v", cp.Trade)
	}
	if cloneVisitorState(&VisitorState{}).Trade != nil {
		t.Error("cloneVisitorState invented a Trade on a passer-through")
	}
}

// TestPickDistributorArrival — the factor targets the distributor-tagged structure (smallest ID
// on a tie); an ordinary traveler targets the tavern; a factor in a village with no distributor
// falls back to the tavern anchor.
func TestPickDistributorArrival(t *testing.T) {
	w := &World{
		VillageObjects: map[VillageObjectID]*VillageObject{
			"store_b": {ID: "store_b", Pos: WorldPos{X: 200, Y: 200}, Tags: []string{TagDistributor}},
			"store_a": {ID: "store_a", Pos: WorldPos{X: 100, Y: 100}, Tags: []string{TagDistributor}},
			"tavern":  {ID: "tavern", Pos: WorldPos{X: 300, Y: 300}, Tags: []string{VisitorTagTavern}},
		},
		Structures: map[StructureID]*Structure{
			"store_a": {ID: "store_a"},
			"store_b": {ID: "store_b"},
			"tavern":  {ID: "tavern"},
		},
	}
	if id, _, ok := pickDistributorDestination(w); !ok || id != "store_a" {
		t.Fatalf("pickDistributorDestination = (%q, %v), want (store_a, true) — smallest-ID distributor", id, ok)
	}
	// bindSellErrand resolves the distributor as the sell counterparty (LLM-455).
	if errand, ok := bindSellErrand(w, rand.New(rand.NewSource(1))); !ok || errand.Counterparty != "store_a" {
		t.Errorf("bindSellErrand = (%+v, %v), want counterparty store_a", errand, ok)
	}
	// A merchant walks straight to his errand counterparty; a passer-through (nil) heads for the tavern.
	sellErrand := &TradeErrand{Direction: TradeDirectionSell, Counterparty: "store_a"}
	if fid, _, fok := pickArrivalDestination(w, sellErrand); !fok || fid != "store_a" {
		t.Errorf("merchant arrival = (%q, %v), want (store_a, true)", fid, fok)
	}
	if oid, _, ook := pickArrivalDestination(w, nil); !ook || oid != "tavern" {
		t.Errorf("passer-through arrival = (%q, %v), want (tavern, true)", oid, ook)
	}
	// A counterparty NOT backed by a structure falls back to the tavern anchor.
	delete(w.Structures, "store_a")
	delete(w.Structures, "store_b")
	if _, _, ok := pickDistributorDestination(w); ok {
		t.Error("pickDistributorDestination should reject a distributor object with no backing structure")
	}
	if _, ok := bindSellErrand(w, rand.New(rand.NewSource(1))); ok {
		t.Error("bindSellErrand should fail when no distributor is backed by a structure")
	}
	if fid, _, fok := pickArrivalDestination(w, sellErrand); !fok || fid != "tavern" {
		t.Errorf("merchant arrival with unbacked counterparty = (%q, %v), want tavern fallback", fid, fok)
	}
}
