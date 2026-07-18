package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// working_capital_test.go — LLM-294. The conserve-mode determination (merchantConserve)
// and its two render tiers: the ## Restocking buy-cue softening (Tier 1) and the
// ## What your wares fetch sell-first nudge (Tier 2). Covers the coin-poor gate, the
// off-switch, the empty-shelf exception, both halves of the overstock test (velocity
// vs dead-stock floor), the most-overstocked pick, and the invariant that a conserve
// keeper is never handed a buy imperative.

// wcCatalog: labels for the goods in these tests.
func wcCatalog() map[sim.ItemKind]*sim.ItemKindDef {
	return map[sim.ItemKind]*sim.ItemKindDef{
		"porridge": {Name: "porridge", DisplayLabel: "porridge", Category: sim.ItemCategoryFood},
		"water":    {Name: "water", DisplayLabel: "water", Category: sim.ItemCategoryDrink},
		"milk":     {Name: "milk", DisplayLabel: "milk", Category: sim.ItemCategoryDrink},
		"ale":      {Name: "ale", DisplayLabel: "ale", Category: sim.ItemCategoryDrink},
	}
}

// wcPolicy builds a RestockPolicy from (item, source, cap) triples.
func wcPolicy(entries ...sim.RestockEntry) *sim.RestockPolicy {
	return &sim.RestockPolicy{Restock: entries}
}

// wcPriceBook builds a one-seller price book recording that `sellerID` sold `units`
// of `item` (as a single observation) within the weekly window — the seller-side
// sell-through sellerRecentSales reads for the velocity half of the overstock test.
func wcPriceBook(sellerID sim.ActorID, item sim.ItemKind, units int) map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation] {
	pb := sim.NewRingBuffer[sim.PriceObservation](4)
	pb.Push(sim.PriceObservation{BuyerID: "someone", Amount: units, Qty: units, Consumers: 1, At: time.Now().UTC()})
	return map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
		{SellerID: sellerID, Item: item}: pb,
	}
}

func TestMerchantConserve(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name     string
		coins    int
		floor    int
		inv      map[sim.ItemKind]int
		policy   *sim.RestockPolicy
		pb       map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]
		want     bool
		wantWare string
	}{
		{
			name:  "coin-rich is never conserve even when overstocked",
			coins: 50, floor: 10,
			inv:    map[sim.ItemKind]int{"porridge": 40},
			policy: wcPolicy(sim.RestockEntry{Item: "porridge", Source: sim.RestockSourceProduce, Max: 30}),
			want:   false,
		},
		{
			name:  "off-switch: floor 0 disables the gate",
			coins: 1, floor: 0,
			inv:    map[sim.ItemKind]int{"porridge": 40},
			policy: wcPolicy(sim.RestockEntry{Item: "porridge", Source: sim.RestockSourceProduce, Max: 30}),
			want:   false,
		},
		{
			name:  "coin-poor + empty shelves is NOT conserve (empty-shelf exception)",
			coins: 1, floor: 10,
			inv:    map[sim.ItemKind]int{"porridge": 0, "milk": 0},
			policy: wcPolicy(sim.RestockEntry{Item: "porridge", Source: sim.RestockSourceProduce, Max: 30}),
			want:   false,
		},
		{
			name:  "coin-poor + dead stock (zero sales) trips the absolute floor",
			coins: 1, floor: 10,
			inv:      map[sim.ItemKind]int{"porridge": 19}, // >= absFloor 8, weekly 0
			policy:   wcPolicy(sim.RestockEntry{Item: "porridge", Source: sim.RestockSourceProduce, Max: 30}),
			want:     true,
			wantWare: "porridge",
		},
		{
			name:  "coin-poor + fast mover UNDER velocity threshold is not conserve",
			coins: 5, floor: 10,
			inv:    map[sim.ItemKind]int{"porridge": 20}, // weekly 12 → threshold max(8, 24) = 24, 20 < 24
			policy: wcPolicy(sim.RestockEntry{Item: "porridge", Source: sim.RestockSourceProduce, Max: 40}),
			pb:     wcPriceBook("keeper", "porridge", 12),
			want:   false,
		},
		{
			name:  "coin-poor + fast mover OVER velocity threshold is conserve",
			coins: 5, floor: 10,
			inv:      map[sim.ItemKind]int{"porridge": 30}, // weekly 12 → threshold 24, 30 >= 24
			policy:   wcPolicy(sim.RestockEntry{Item: "porridge", Source: sim.RestockSourceProduce, Max: 40}),
			pb:       wcPriceBook("keeper", "porridge", 12),
			want:     true,
			wantWare: "porridge",
		},
		{
			name:  "most-overstocked ware wins (largest excess over its own threshold)",
			coins: 1, floor: 10,
			// water: onHand 30, weekly 0 → threshold 8, excess 22.
			// porridge: onHand 12, weekly 0 → threshold 8, excess 4. → water wins.
			inv: map[sim.ItemKind]int{"water": 30, "porridge": 12},
			policy: wcPolicy(
				sim.RestockEntry{Item: "porridge", Source: sim.RestockSourceProduce, Max: 30},
				sim.RestockEntry{Item: "water", Source: sim.RestockSourceProduce, Max: 40},
			),
			want:     true,
			wantWare: "water",
		},
		{
			name:  "resold input the keeper never sells does not count on its own",
			coins: 1, floor: 10,
			inv:    map[sim.ItemKind]int{"milk": 6}, // buy input, weekly 0, 6 < absFloor 8
			policy: wcPolicy(sim.RestockEntry{Item: "milk", Source: sim.RestockSourceBuy, Max: 12}),
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			subj := &sim.ActorSnapshot{Coins: tc.coins, Inventory: tc.inv, RestockPolicy: tc.policy}
			snap := &sim.Snapshot{
				Actors:            map[sim.ActorID]*sim.ActorSnapshot{"keeper": subj},
				ItemKinds:         wcCatalog(),
				PriceBook:         tc.pb,
				MerchantCoinFloor: tc.floor,
				PublishedAt:       now,
			}
			got := merchantConserve(snap, "keeper", subj)
			if got.Active != tc.want {
				t.Fatalf("Active = %v, want %v (state %+v)", got.Active, tc.want, got)
			}
			if tc.want && got.OverstockedWare != tc.wantWare {
				t.Errorf("OverstockedWare = %q, want %q", got.OverstockedWare, tc.wantWare)
			}
			if tc.want && got.Coins != tc.coins {
				t.Errorf("Coins = %d, want %d", got.Coins, tc.coins)
			}
		})
	}
}

// TestRenderRestocking_ConserveNoBuyImperative: the Tier-1 invariant — a conserve view
// renders the hold-off-buying lead and names low items WITHOUT any buy imperative.
func TestRenderRestocking_ConserveNoBuyImperative(t *testing.T) {
	v := &RestockingView{
		BuyerCoins: 1,
		Conserve:   true,
		Items: []RestockItemView{
			{ItemLabel: "milk", CurrentQty: 0, Cap: 12, kind: "milk", CoPresentSeller: "Elizabeth Ellis", AffordableQty: -1},
		},
	}
	var b strings.Builder
	renderRestocking(&b, v)
	out := b.String()
	if !strings.Contains(out, "Hold off buying more") {
		t.Errorf("conserve lead missing:\n%s", out)
	}
	// LLM-298: the conserve low-item line is self-resolving — it names the lack AND
	// what to do instead (hold, sell first, restock later), never a bare "low on X".
	if !strings.Contains(out, "You are low on milk — no errand for it now; sell first, then restock once your purse recovers.") {
		t.Errorf("self-resolving low-item note missing:\n%s", out)
	}
	// No buy imperative of any form — the whole point of conserve mode.
	for _, banned := range []string{"Buy it now", "pay_with_item", "room for", "buy from", "cover about"} {
		if strings.Contains(out, banned) {
			t.Errorf("conserve render leaked buy imperative %q:\n%s", banned, out)
		}
	}
}

// TestRenderRestocking_ConservePendingOffer: a conserving keeper with a standing
// buy-offer keeps the LLM-64 anti-churn wait-steer but gains the conserve caveat, and
// never gets a re-offer/settle nudge (LLM-294 review follow-up).
func TestRenderRestocking_ConservePendingOffer(t *testing.T) {
	v := &RestockingView{
		BuyerCoins: 1,
		Conserve:   true,
		Items: []RestockItemView{
			{ItemLabel: "milk", CurrentQty: 0, Cap: 12, kind: "milk", CoPresentSeller: "Elizabeth Ellis", PendingOfferToCoPresentSeller: true, AffordableQty: -1},
		},
	}
	var b strings.Builder
	renderRestocking(&b, v)
	out := b.String()
	if !strings.Contains(out, "Wait for their answer") {
		t.Errorf("pending-offer wait-steer missing:\n%s", out)
	}
	if !strings.Contains(out, "put out no new offers while your purse is thin") {
		t.Errorf("conserve caveat missing from pending-offer line:\n%s", out)
	}
	for _, banned := range []string{"Buy it now", "pay_with_item", "do not re-offer or leave"} {
		if strings.Contains(out, banned) {
			t.Errorf("conserve pending-offer render leaked %q:\n%s", banned, out)
		}
	}
}

// TestRenderRestocking_NonConserveUnchanged: guards that the ordinary buy cue is
// untouched when Conserve is false (a co-present seller still gets "Buy it now").
func TestRenderRestocking_NonConserveUnchanged(t *testing.T) {
	v := &RestockingView{
		BuyerCoins: 40,
		Conserve:   false,
		Items: []RestockItemView{
			{ItemLabel: "milk", CurrentQty: 1, Cap: 12, kind: "milk", CoPresentSeller: "Elizabeth Ellis", AffordableQty: -1},
		},
	}
	var b strings.Builder
	renderRestocking(&b, v)
	out := b.String()
	if !strings.Contains(out, "Buy it now") || !strings.Contains(out, "pay_with_item") {
		t.Errorf("non-conserve buy imperative missing (regression):\n%s", out)
	}
	if strings.Contains(out, "Hold off buying") {
		t.Errorf("non-conserve view wrongly rendered the conserve lead:\n%s", out)
	}
}

// TestRenderTradeValue_SellFirstNudge: the Tier-2 nudge points at the sell tool and
// names the overstocked ware, and is absent when SellFirst is false.
func TestRenderTradeValue_SellFirstNudge(t *testing.T) {
	base := []TradeValueItem{{ItemLabel: "porridge", itemKind: "porridge", Low: 1, High: 2}}

	var on strings.Builder
	renderTradeValue(&on, &TradeValueView{Items: base, SellFirst: true, SellFirstWare: "porridge", SellFirstCoins: 1})
	got := on.String()
	if !strings.Contains(got, "use the sell tool") {
		t.Errorf("sell nudge missing the sell-tool pointer:\n%s", got)
	}
	if !strings.Contains(got, "more porridge than folk have been buying") {
		t.Errorf("sell nudge missing the overstocked ware:\n%s", got)
	}

	var off strings.Builder
	renderTradeValue(&off, &TradeValueView{Items: base, SellFirst: false})
	if strings.Contains(off.String(), "use the sell tool") {
		t.Errorf("sell nudge rendered when SellFirst is false:\n%s", off.String())
	}
}

// TestMerchantConserve_WareScopingCases is the perception half of the LLM-462 case
// table. It MIRRORS TestActorConserving_WareScopingCases (engine/sim/merchant_capital_test.go)
// case for case: merchantConserve (here, over a Snapshot) and actorConserving (there,
// over the live World) are hand-written twins that must agree, or the "## Restocking"
// cue and the restock warrant disagree and the keeper is woken to read a hold-off — the
// LLM-298 wake loop. Neither function is reachable from the other's package, so the two
// tables standing side by side is what catches drift. Keep them in sync.
func TestMerchantConserve_WareScopingCases(t *testing.T) {
	porridgeFromWater := map[sim.ItemKind]*sim.ItemRecipe{
		"porridge": {OutputItem: "porridge", OutputQty: 4, RateQty: 4, RatePerHours: 1,
			Inputs: []sim.RecipeInput{{Item: "water", Qty: 5}}},
	}
	cases := []struct {
		name      string
		inventory map[sim.ItemKind]int
		policy    *sim.RestockPolicy
		recipes   map[sim.ItemKind]*sim.ItemRecipe
		want      bool
		wantWare  string // the ware named in the sell nudge; "" when not conserving
		why       string
	}{
		{
			name:      "required_input_only",
			inventory: map[sim.ItemKind]int{"water": 19, "porridge": 0},
			policy: wcPolicy(
				sim.RestockEntry{Item: "porridge", Source: sim.RestockSourceProduce, Max: 30},
				sim.RestockEntry{Item: "water", Source: sim.RestockSourceBuy, Max: 10},
			),
			recipes: porridgeFromWater,
			want:    false,
			why:     "the only pile is raw material and the ware shelf is bare — the empty-shelf exception",
		},
		{
			name:      "plain_ware",
			inventory: map[sim.ItemKind]int{"water": 19},
			policy:    wcPolicy(sim.RestockEntry{Item: "water", Source: sim.RestockSourceBuy, Max: 10}),
			recipes:   map[sim.ItemKind]*sim.ItemRecipe{},
			want:      true,
			wantWare:  "water",
			why:       "water feeds no recipe of hers, so 19 unsold is merchandise sitting still",
		},
		{
			name:      "dual_role_produced_and_consumed",
			inventory: map[sim.ItemKind]int{"water": 40, "porridge": 0},
			policy: wcPolicy(
				sim.RestockEntry{Item: "porridge", Source: sim.RestockSourceProduce, Max: 30},
				sim.RestockEntry{Item: "water", Source: sim.RestockSourceProduce, Max: 30},
			),
			recipes: porridgeFromWater,
			want:    false,
			why:     "required-input-always-wins: a good the actor both sells and cooks with stays raw material (see the invariant in merchant_capital.go)",
		},
		{
			name:      "input_plus_separate_overstocked_ware",
			inventory: map[sim.ItemKind]int{"water": 19, "ale": 20},
			policy: wcPolicy(
				sim.RestockEntry{Item: "porridge", Source: sim.RestockSourceProduce, Max: 30},
				sim.RestockEntry{Item: "water", Source: sim.RestockSourceBuy, Max: 10},
				sim.RestockEntry{Item: "ale", Source: sim.RestockSourceBuy, Max: 24},
			),
			recipes:  porridgeFromWater,
			want:     true,
			wantWare: "ale",
			why:      "excluding the water pile must not excuse the 20 unsold ale beside it — this is the John Ellis shape",
		},
		{
			name:      "nil_recipes",
			inventory: map[sim.ItemKind]int{"water": 19},
			policy: wcPolicy(
				sim.RestockEntry{Item: "porridge", Source: sim.RestockSourceProduce, Max: 30},
				sim.RestockEntry{Item: "water", Source: sim.RestockSourceBuy, Max: 10},
			),
			recipes:  nil,
			want:     true,
			wantWare: "water",
			why:      "no catalog means nothing is known to be an input — everything held is a ware, the pre-LLM-462 behavior",
		},
		{
			name:      "elective_boost_input_is_a_ware",
			inventory: map[sim.ItemKind]int{"salt": 20, "porridge": 0},
			policy: wcPolicy(
				sim.RestockEntry{Item: "porridge", Source: sim.RestockSourceProduce, Max: 30},
				sim.RestockEntry{Item: "salt", Source: sim.RestockSourceBuy, Max: 6},
			),
			recipes: map[sim.ItemKind]*sim.ItemRecipe{
				"porridge": {OutputItem: "porridge", OutputQty: 4, RateQty: 4, RatePerHours: 1,
					Inputs:      []sim.RecipeInput{{Item: "water", Qty: 5}},
					BoostInputs: []sim.BoostInput{{Item: "salt", Qty: 1, BonusQty: 3}}},
			},
			want:     true,
			wantWare: "salt",
			why:      "an elective booster never stalls the line, so a hoard of it is merchandise (ReorderFloors counts required Inputs only)",
		},
	}
	catalog := wcCatalog()
	catalog["salt"] = &sim.ItemKindDef{Name: "salt", DisplayLabel: "salt"}
	catalog["flour"] = &sim.ItemKindDef{Name: "flour", DisplayLabel: "flour"}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			subj := &sim.ActorSnapshot{
				Coins:         2, // below the floor in every case — the coin half is not what's under test
				Inventory:     c.inventory,
				RestockPolicy: c.policy,
			}
			snap := &sim.Snapshot{
				Actors:            map[sim.ActorID]*sim.ActorSnapshot{"keeper": subj},
				ItemKinds:         catalog,
				Recipes:           c.recipes,
				MerchantCoinFloor: 10,
				RestockReorderPct: 25,
				PublishedAt:       time.Now().UTC(),
			}
			got := merchantConserve(snap, "keeper", subj)
			if got.Active != c.want {
				t.Fatalf("Active = %v, want %v — %s", got.Active, c.want, c.why)
			}
			if got.OverstockedWare != c.wantWare {
				t.Errorf("OverstockedWare = %q, want %q — the sell nudge must name a real ware, never raw material", got.OverstockedWare, c.wantWare)
			}
		})
	}
}

// TestMerchantConserve_ProductionInputIsNotOverstock: LLM-462 — the keeper's own raw
// material never tips the overstock verdict. The live Hannah Boggs: 2 coins, a bare
// porridge shelf, 19 water (5 to a batch) and 1 flour. Water alone cleared the
// dead-stock floor, so she read as stock-rich, the section flipped to the hold-off
// steer and the warrant mirror suppressed the flour buy that would have restarted her
// line. Scoped to actual wares she is empty-shelved, which is the LLM-294 exception she
// always qualified for.
//
// The control pins causality: hold the 19-water pile fixed and make water a plain ware
// (no longer a porridge input) — she conserves again, and water is named as the ware to
// sell down. So the verdict turns on input-ness, not on the pile.
func TestMerchantConserve_ProductionInputIsNotOverstock(t *testing.T) {
	porridgeFromFlourAndWater := map[sim.ItemKind]*sim.ItemRecipe{
		"porridge": {OutputItem: "porridge", OutputQty: 4, RateQty: 4, RatePerHours: 1,
			Inputs: []sim.RecipeInput{{Item: "flour", Qty: 2}, {Item: "water", Qty: 5}}},
	}
	subj := &sim.ActorSnapshot{
		Coins:     2,
		Inventory: map[sim.ItemKind]int{"water": 19, "flour": 1, "porridge": 0},
		RestockPolicy: wcPolicy(
			sim.RestockEntry{Item: "porridge", Source: sim.RestockSourceProduce, Max: 30},
			sim.RestockEntry{Item: "water", Source: sim.RestockSourceBuy, Max: 10},
			sim.RestockEntry{Item: "flour", Source: sim.RestockSourceBuy, Max: 6},
		),
	}
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"hannah": subj},
		ItemKinds:         wcCatalog(),
		Recipes:           porridgeFromFlourAndWater,
		MerchantCoinFloor: 10,
		RestockReorderPct: 25,
		PublishedAt:       time.Now().UTC(),
	}
	if got := merchantConserve(snap, "hannah", subj); got.Active {
		t.Errorf("Active = true on a bare-shelved keeper whose only pile is raw material (named %q) — the empty-shelf exception must apply", got.OverstockedWare)
	}

	// Control: same 19 water, but it now feeds no recipe of hers, so it IS merchandise
	// sitting unsold — conserve returns and names it.
	snap.Recipes = map[sim.ItemKind]*sim.ItemRecipe{
		"porridge": {OutputItem: "porridge", OutputQty: 4, RateQty: 4, RatePerHours: 1,
			Inputs: []sim.RecipeInput{{Item: "flour", Qty: 2}}},
	}
	got := merchantConserve(snap, "hannah", subj)
	if !got.Active {
		t.Fatal("Active = false once water is a plain ware, want true (19 on hand clears the dead-stock floor)")
	}
	if got.OverstockedWare != "water" {
		t.Errorf("OverstockedWare = %q, want \"water\" (the only ware over its threshold)", got.OverstockedWare)
	}
}

// TestBuildRestocking_ConserveWiredThrough: end-to-end over buildRestocking — a
// coin-poor + overstocked keeper low on a buy input gets Conserve set on the view, and
// the render carries the conserve steer instead of the co-present buy imperative.
func TestBuildRestocking_ConserveWiredThrough(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Coins: 1,
		// Overstocked on its OWN produced porridge (19 >= absFloor 8, zero sales),
		// and low on the milk it buys as an input (0 of 12).
		Inventory:       map[sim.ItemKind]int{"porridge": 19, "milk": 0},
		RestockPolicy:   wcPolicy(sim.RestockEntry{Item: "milk", Source: sim.RestockSourceBuy, Max: 12}, sim.RestockEntry{Item: "porridge", Source: sim.RestockSourceProduce, Max: 30}),
		CurrentHuddleID: "h1",
	}
	// A co-present milk supplier so the milk item survives the LLM-216 dead-end drop.
	supplier := &sim.ActorSnapshot{
		DisplayName:     "Elizabeth Ellis",
		RestockPolicy:   wcPolicy(sim.RestockEntry{Item: "milk", Source: sim.RestockSourceProduce, Max: 40}),
		WorkStructureID: "farm",
		Inventory:       map[sim.ItemKind]int{"milk": 40},
		CurrentHuddleID: "h1",
	}
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"keeper": subj, "eliza": supplier},
		Structures:        map[sim.StructureID]*sim.Structure{"farm": {ID: "farm", DisplayName: "Ellis Farm"}},
		ItemKinds:         wcCatalog(),
		MerchantCoinFloor: 10,
		RestockReorderPct: 25,
		PublishedAt:       time.Now().UTC(),
	}
	v := buildRestocking(snap, "keeper", subj)
	if v == nil {
		t.Fatal("buildRestocking returned nil, want a view with the low milk item")
	}
	if !v.Conserve {
		t.Fatalf("Conserve = false, want true (coins 1 < floor 10, overstocked on porridge)")
	}
	var b strings.Builder
	renderRestocking(&b, v)
	out := b.String()
	if !strings.Contains(out, "Hold off buying more") {
		t.Errorf("conserve steer missing from wired render:\n%s", out)
	}
	if strings.Contains(out, "Buy it now") {
		t.Errorf("co-present buy imperative leaked in conserve mode:\n%s", out)
	}
}
