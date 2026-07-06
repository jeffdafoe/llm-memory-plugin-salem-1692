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
	if !strings.Contains(out, "You are low on milk.") {
		t.Errorf("low-item note missing:\n%s", out)
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
