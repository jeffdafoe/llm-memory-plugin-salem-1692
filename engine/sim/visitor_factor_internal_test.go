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

// TestInAfternoonSpawnWindow — the LLM-455 spawn window is [max(dawn, earliest=900), dusk−90),
// inclusive lower / exclusive upper. Pins the boundary semantics (code_review) so a future
// change can't quietly let a merchant arrive too close to dusk or before the tavern opens.
func TestInAfternoonSpawnWindow(t *testing.T) {
	const dawn, dusk = 420, 1140 // 07:00, 19:00 → window [900, 1050)
	cases := []struct {
		name   string
		nowMin int
		want   bool
	}{
		{"before earliest", 899, false},
		{"exactly earliest (inclusive)", 900, true},
		{"mid window", 960, true},
		{"just before latest", 1049, true},
		{"exactly latest (exclusive)", 1050, false},
		{"after latest", 1051, false},
	}
	for _, c := range cases {
		if got := inAfternoonSpawnWindow(dawn, dusk, c.nowMin); got != c.want {
			t.Errorf("%s: inAfternoonSpawnWindow(%d,%d,%d) = %v, want %v", c.name, dawn, dusk, c.nowMin, got, c.want)
		}
	}
	// earliest clamps UP to a late dawn.
	if inAfternoonSpawnWindow(1000, 1140, 950) {
		t.Error("window opened before a late dawn (earliest must clamp up to dawn)")
	}
	// An empty window (dusk − margin <= earliest) rejects everything.
	if inAfternoonSpawnWindow(420, 960, 900) { // dusk 16:00 → latest 870 < earliest 900
		t.Error("empty window (dusk−margin <= earliest) must reject all clocked spawns")
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

// TestSellErrandDelivered — LLM-553. A seller's errand is done once he has HANDED OVER all but
// a quarter of the shipment he arrived with. The predicate reads a monotonic delivered count,
// not current holdings, because his deal is two-way: holdings cannot separate a man who never
// sold from one who sold everything and bought some back. The no-baseline rows are the
// backward-compatibility path — a visitor checkpointed before these fields existed has nothing
// to measure against and must not settle here at all, leaving him the dusk wind-down he had
// before rather than a spuriously early one.
func TestSellErrandDelivered(t *testing.T) {
	cases := []struct {
		name        string
		delivered   int
		shipmentQty int
		want        bool
	}{
		{"nothing handed over", 0, 10, false},
		{"half the bale is not done", 5, 10, false},
		{"one short of the target", 7, 10, false},
		{"exactly the target — all but a quarter of ten", 8, 10, true},
		{"the live case — nine of ten bars landed", 9, 10, true},
		{"the whole shipment", 10, 10, true},
		{"sold everything then bought three back still counts", 10, 10, true},
		{"a small shipment settles on its whole", 1, 1, true},
		{"no baseline, nothing delivered", 0, 0, false},
		{"no baseline, plenty delivered", 20, 0, false},
		{"negative baseline is treated as no baseline", 9, -5, false},
		{"negative delivered never settles", -3, 10, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := sellErrandDelivered(tc.delivered, tc.shipmentQty); got != tc.want {
				t.Errorf("sellErrandDelivered(delivered=%d, shipment=%d) = %v, want %v",
					tc.delivered, tc.shipmentQty, got, tc.want)
			}
		})
	}
}

// TestTransferItemCreditsSellerShipment — the accounting half of LLM-553, and the reason the
// settle reads a counter rather than an inventory. A factor sells his whole ten-bar shipment and
// then buys three bars back off the keeper's shelf (the two-way deal working as designed, and
// exactly what the live factor did when he swapped a locket for iron). His holdings end at 3 —
// indistinguishable from a man who sold seven and bought nothing — but Delivered stands at 10,
// so only the honest measure settles him.
func TestTransferItemCreditsSellerShipment(t *testing.T) {
	const iron = ItemKind("iron")
	errand := &TradeErrand{Direction: TradeDirectionSell, Good: iron, Counterparty: "store", ShipmentQty: 10}
	factor := &Actor{
		ID: "vstr-1", Kind: KindNPCShared,
		Inventory:    map[ItemKind]int{iron: 10},
		VisitorState: &VisitorState{Trade: errand},
	}
	keeper := &Actor{ID: "josiah", Kind: KindNPCStateful, Inventory: map[ItemKind]int{}}

	if err := transferItem(nil, factor, keeper, iron, 10); err != nil {
		t.Fatalf("factor sells his bale: %v", err)
	}
	if errand.Delivered != 10 {
		t.Fatalf("Delivered = %d after handing over the whole shipment, want 10", errand.Delivered)
	}
	// He buys three back. The inbound leg must NOT decrement the credit.
	if err := transferItem(nil, keeper, factor, iron, 3); err != nil {
		t.Fatalf("factor buys iron back: %v", err)
	}
	if errand.Delivered != 10 {
		t.Errorf("Delivered = %d after buying stock back, want it unchanged at 10 — a purchase must not un-deliver a sale", errand.Delivered)
	}
	if factor.Inventory[iron] != 3 {
		t.Fatalf("factor holds %d iron, want 3 (the test's whole point is that holdings are ambiguous here)", factor.Inventory[iron])
	}
	if !sellErrandDelivered(errand.Delivered, errand.ShipmentQty) {
		t.Error("a factor who landed his whole shipment is not settled once he buys a few bars back")
	}
}

// TestTransferItemIgnoresNonSellers — the credit is scoped: a buy-errand merchant, a
// passer-through and a resident must never accrue Delivered, and a seller parting with some
// OTHER good than his errand headline must not either.
func TestTransferItemIgnoresNonSellers(t *testing.T) {
	const iron = ItemKind("iron")
	keeper := &Actor{ID: "josiah", Kind: KindNPCStateful, Inventory: map[ItemKind]int{}}

	buyErrand := &TradeErrand{Direction: TradeDirectionBuy, Good: iron, Counterparty: "store"}
	buyer := &Actor{
		ID: "vstr-2", Kind: KindNPCShared,
		Inventory:    map[ItemKind]int{iron: 4},
		VisitorState: &VisitorState{Trade: buyErrand},
	}
	if err := transferItem(nil, buyer, keeper, iron, 2); err != nil {
		t.Fatalf("buyer transfer: %v", err)
	}
	if buyErrand.Delivered != 0 {
		t.Errorf("a BUY errand accrued Delivered = %d, want 0", buyErrand.Delivered)
	}

	sellErrand := &TradeErrand{Direction: TradeDirectionSell, Good: iron, Counterparty: "store", ShipmentQty: 10}
	seller := &Actor{
		ID: "vstr-3", Kind: KindNPCShared,
		Inventory:    map[ItemKind]int{iron: 10, "cloak": 2},
		VisitorState: &VisitorState{Trade: sellErrand},
	}
	if err := transferItem(nil, seller, keeper, "cloak", 2); err != nil {
		t.Fatalf("seller transfer of a non-errand good: %v", err)
	}
	if sellErrand.Delivered != 0 {
		t.Errorf("parting with a secondary-bale good accrued Delivered = %d, want 0 — the errand is the headline import", sellErrand.Delivered)
	}

	resident := &Actor{ID: "anne", Kind: KindNPCShared, Inventory: map[ItemKind]int{iron: 3}}
	if err := transferItem(nil, resident, keeper, iron, 1); err != nil {
		t.Fatalf("resident transfer: %v", err)
	}
	if sellErrandFor(resident) != nil {
		t.Error("a resident with no VisitorState resolved a sell errand")
	}
}

// TestSeedFactorPackStampsShipmentBaseline — the spawn-side half of LLM-553: whatever
// seedFactorPack actually put in the bale for the errand good is the baseline the settle
// measures against, and a spawn stamps it from the PACK rather than the units knob. The knob is
// the wrong number because the seed jitters the count by up to two — a baseline taken from the
// setting would either put the target out of reach or trip it early.
func TestSeedFactorPackStampsShipmentBaseline(t *testing.T) {
	const ironUnits = 10
	r := rand.New(rand.NewSource(7))
	pack, _ := seedFactorPack(r, 2, ironUnits, 12, 100, 200)
	shipment := pack[factorIronKind]
	if shipment < ironUnits || shipment > ironUnits+2 {
		t.Fatalf("seeded iron = %d, want the shipment quantity plus jitter (%d..%d)", shipment, ironUnits, ironUnits+2)
	}
	// The errand good for a sell errand IS the iron headline, so `shipment` is what a spawn
	// stamps onto TradeErrand.ShipmentQty.
	if sellErrandDelivered(0, shipment) {
		t.Error("a factor who has handed over nothing counts as delivered")
	}
	if !sellErrandDelivered(shipment, shipment) {
		t.Error("a factor who handed over his whole bale does not count as delivered")
	}
	// The jitter is exactly why the knob cannot stand in for the pack: had the seed rolled
	// above the knob, a target computed from ironUnits would be reachable before the bale
	// actually moved.
	if shipment > ironUnits && sellErrandDelivered(ironUnits-ironUnits/sellErrandRemainderDivisor, shipment) != (ironUnits-ironUnits/sellErrandRemainderDivisor >= shipment-shipment/sellErrandRemainderDivisor) {
		t.Error("target computed from the knob disagrees with the target computed from the seeded pack")
	}
}
