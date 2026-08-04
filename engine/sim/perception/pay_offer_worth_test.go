package perception

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_offer_worth_test.go — LLM-598. A barter offer must carry what it is worth, or
// the seller judges it by counting units.
//
// The live failure (Joseph Scott, 2026-08-04 14:26 UTC; pay_ledger 3710,
// virtual_agent_calls 155501): Josiah offered 7 wheat for 7 flour. The offer line
// carried the quantities and nothing else; the wares cue further down the same
// prompt carried the worths — flour about 3 coins each to him, wheat about 1. He
// recited both correctly and accepted, calling it "a fair trade," because seven and
// seven look even. He gave 21 coins of flour for 7 coins of wheat, having 47 sheaves
// of wheat already. The same morning he called 3-wheat-for-1-flour a fair trade too;
// the word tracked equal-ish counts, not value.
//
// millerOfferedParitySwapForFlour reproduces that turn. Its foils are inline in
// TestOfferWorthOf: the same swap with a real margin (7 flour for 10 wheat) grades
// fair and renders nothing.

// millerOfferedParitySwapForFlour is the live 14:26 shape: the miller at his post
// with the distributor, a pending 7-wheat-for-7-flour offer, and a price book that
// establishes both worths — his own flour sales to the shop at 3/unit, his own wheat
// purchases at 1/unit. Co-present so the wares cue builds, exactly as it did live:
// the numbers and the verdict must appear in one prompt.
func millerOfferedParitySwapForFlour() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		josephID = sim.ActorID("joseph")
		josiahID = sim.ActorID("josiah")
		mill     = sim.StructureID("mill")
		store    = sim.StructureID("general_store")
		huddle   = sim.HuddleID("h1")
	)
	now := 866 // 14:26, the live window
	published := time.Date(2026, 8, 4, 14, 26, 0, 0, time.UTC)
	joseph := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Joseph Scott",
		Role:              "miller",
		State:             sim.StateIdle,
		InsideStructureID: mill,
		CurrentHuddleID:   huddle,
		WorkStructureID:   mill,
		Coins:             10,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"flour": 16, "wheat": 47},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "flour", Source: sim.RestockSourceProduce, Max: 20},
			{Item: "wheat", Source: sim.RestockSourceBuy, Max: 50},
		}},
		Acquaintances: map[string]sim.Acquaintance{"Josiah Thorne": {}},
	}
	josiah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Josiah Thorne",
		Role:              "shopkeeper",
		State:             sim.StateIdle,
		InsideStructureID: mill,
		CurrentHuddleID:   huddle,
		WorkStructureID:   store,
		Coins:             10,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"wheat": 8},
	}
	pending := &sim.PayLedgerEntry{
		ID: 3710, BuyerID: josiahID, SellerID: josephID,
		ItemKind: "flour", Qty: 7, Amount: 0,
		PayItems:  []sim.ItemKindQty{{Kind: "wheat", Qty: 7}},
		State:     sim.PayLedgerStatePending,
		ExpiresAt: published.Add(3 * time.Minute),
	}
	// What the shop has actually been paying him for flour: 3 a sack.
	flourSales := sim.NewRingBuffer[sim.PriceObservation](8)
	flourSales.Push(sim.PriceObservation{BuyerID: josiahID, Amount: 18, Qty: 6, Consumers: 1, At: published.Add(-26 * time.Hour)})
	// What he has actually been paying for wheat: 1 a sheaf, bought from the shop.
	wheatPurchases := sim.NewRingBuffer[sim.PriceObservation](8)
	wheatPurchases.Push(sim.PriceObservation{BuyerID: josephID, Amount: 12, Qty: 12, Consumers: 1, At: published.Add(-18 * time.Hour)})
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{josephID: joseph, josiahID: josiah},
		Quotes:           map[sim.QuoteID]*sim.SceneQuote{},
		PayLedger:        map[sim.LedgerID]*sim.PayLedgerEntry{3710: pending},
		Scenes:           map[sim.SceneID]*sim.Scene{},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{josephID: {}, josiahID: {}}},
		},
		Structures: map[sim.StructureID]*sim.Structure{
			mill:  plainStructure(mill, "Mill"),
			store: plainStructure(store, "General Store"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(mill):  {ID: sim.VillageObjectID(mill), OwnerActorID: josephID, Tags: []string{sim.TagBusiness, sim.TagWholesaler}},
			sim.VillageObjectID(store): {ID: sim.VillageObjectID(store), OwnerActorID: josiahID, Tags: []string{sim.TagDistributor}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"flour": {OutputItem: "flour", OutputQty: 5, RateQty: 4, RatePerHours: 1, WholesalePrice: 3, RetailPrice: 4,
				Inputs: []sim.RecipeInput{{Item: "wheat", Qty: 5}}},
			"wheat": {OutputItem: "wheat", OutputQty: 1, RateQty: 3, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"flour": {Name: "flour", DisplayLabel: "Flour", DisplayLabelSingular: "sack of flour", DisplayLabelPlural: "sacks of flour", Category: sim.ItemCategoryFood},
			"wheat": {Name: "wheat", DisplayLabel: "Wheat", DisplayLabelSingular: "sheaf of wheat", DisplayLabelPlural: "sheaves of wheat", Category: sim.ItemCategoryFood},
		},
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: josephID, Item: "flour"}: flourSales,
			{SellerID: josiahID, Item: "wheat"}: wheatPurchases,
		},
	}
	return snap, josephID, nil
}

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "miller_offered_parity_swap_for_flour",
			summary: "LLM-598 (the live 2026-08-04 14:26 failure): Josiah offers Joseph Scott 7 sheaves of wheat for 7 " +
				"sacks of flour. The wares cue prices flour at the 3 coins the shop has been paying him and wheat at the 1 " +
				"coin he pays for it, so the swap hands over 21 coins of flour for 7 coins of wheat — and he holds 47 " +
				"sheaves already. The golden pins the offer line now carrying '— far less than the flour is worth', the " +
				"verdict he was left to derive himself and did not: live he recited both rates correctly and accepted, " +
				"calling seven-for-seven a fair trade. No numbers in the clause — they are already in the wares cue above " +
				"it, and it was the arithmetic ACROSS the two sections that he skipped.",
			build: millerOfferedParitySwapForFlour,
		},
	)
}

// TestOfferWorthOf pins the tier boundaries on the live shape and its foils. The
// fair cases matter as much as the short one: a clause on every barter offer would
// be boilerplate the model learns to skip.
func TestOfferWorthOf(t *testing.T) {
	snap, actorID, _ := millerOfferedParitySwapForFlour()

	// flour is worth 3 to him (realized sales), wheat 1 (realized purchases), so the
	// asked side of the live offer totals 21 and the payment 7.
	offer := func(qty int, amount int, pay ...sim.ItemKindQty) sim.PayOfferWarrantReason {
		return sim.PayOfferWarrantReason{LedgerID: 3710, Buyer: "josiah", Item: "flour", Qty: qty, Amount: amount, PayItems: pay}
	}
	wheat := func(n int) sim.ItemKindQty { return sim.ItemKindQty{Kind: "wheat", Qty: n} }

	tests := []struct {
		name  string
		offer sim.PayOfferWarrantReason
		want  offerWorth
	}{
		{"live parity swap: 7 wheat (7) for 7 flour (21)", offer(7, 0, wheat(7)), offerWorthShort},
		{"exactly half: 10 wheat (10) for 7 flour (21) rounds into short at 21/2=10", offer(7, 0, wheat(10)), offerWorthShort},
		{"just above half: 11 wheat (11) for 7 flour (21)", offer(7, 0, wheat(11)), offerWorthThin},
		{"a real margin in kind: 25 wheat (25) for 7 flour (21)", offer(7, 0, wheat(25)), offerWorthFair},
		{"exact parity in worth is fair, not thin: 21 wheat for 7 flour", offer(7, 0, wheat(21)), offerWorthFair},
		{"mixed payment counts the coins: 7 wheat (7) + 14 coins for 7 flour (21)", offer(7, 14, wheat(7)), offerWorthFair},
		{"mixed payment still short: 7 wheat (7) + 2 coins for 7 flour (21)", offer(7, 2, wheat(7)), offerWorthShort},
		{"pure coin is not this cue's business", offer(7, 3), offerWorthUnknown},
		{"an unpriced good abandons the judgment rather than undercounting it",
			offer(7, 0, wheat(1), sim.ItemKindQty{Kind: "whalebone_charm", Qty: 4}), offerWorthUnknown},
		{"an unpriced ASKED good is equally unjudgeable",
			sim.PayOfferWarrantReason{LedgerID: 1, Item: "whalebone_charm", Qty: 2, PayItems: []sim.ItemKindQty{wheat(1)}}, offerWorthUnknown},
		{"an empty ask is no offer", offer(0, 0, wheat(7)), offerWorthUnknown},

		// Malformed legs are UNJUDGED, not skipped. Skipping them would price a
		// barter offer off its coin leg alone and read as short (code_review).
		{"a zero-quantity payment leg is not a free pass to judge on coins alone",
			offer(7, 0, sim.ItemKindQty{Kind: "wheat", Qty: 0}), offerWorthUnknown},
		{"a negative payment leg is unjudgeable, not ignorable",
			offer(7, 0, sim.ItemKindQty{Kind: "wheat", Qty: -5}), offerWorthUnknown},
		{"a zero leg alongside a good one still abandons the offer",
			offer(7, 0, wheat(25), sim.ItemKindQty{Kind: "wheat", Qty: 0}), offerWorthUnknown},
		{"a negative coin amount cannot drag the payment below zero",
			offer(7, -100, wheat(25)), offerWorthUnknown},

		// Overflow: a wrapped total is worse than no total — a negative one enters
		// the short band and renders a confident "far less" on a generous offer.
		{"an absurd ask quantity earns silence, not a wrapped verdict",
			offer(math.MaxInt64, 0, wheat(7)), offerWorthUnknown},
		{"an absurd payment quantity earns silence too",
			offer(7, 0, sim.ItemKindQty{Kind: "wheat", Qty: math.MaxInt64}), offerWorthUnknown},
		{"payment legs that only overflow when SUMMED earn silence",
			offer(7, 0, wheat(math.MaxInt32-1), wheat(math.MaxInt32-1)), offerWorthUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := offerWorthOf(snap, actorID, tc.offer); got != tc.want {
				t.Errorf("offerWorthOf = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOfferItemUnitWorth pins the resolution ORDER. It is the part that keeps the
// verdict agreeing with the numbers printed above it in the same prompt: a catalog
// seed that disagrees with what the actor has actually been getting would produce a
// clause the wares cue contradicts.
func TestOfferItemUnitWorth(t *testing.T) {
	snap, actorID, _ := millerOfferedParitySwapForFlour()

	// flour: realized sales (18 coins / 6 units = 3) beat the catalog wholesale 3 —
	// equal here by construction, so re-price the ring to prove which one won.
	richer := sim.NewRingBuffer[sim.PriceObservation](8)
	richer.Push(sim.PriceObservation{BuyerID: "josiah", Amount: 30, Qty: 6, Consumers: 1, At: snap.PublishedAt.Add(-2 * time.Hour)})
	snap.PriceBook[sim.PriceBookKey{SellerID: actorID, Item: "flour"}] = richer
	if got := offerItemUnitWorth(snap, actorID, "flour"); got != 5 {
		t.Errorf("flour worth = %d, want 5 — a realized sale rate must beat the catalog seed of 3", got)
	}

	// wheat: he has no wheat SALES, so his own purchases (12/12 = 1) carry it.
	if got := offerItemUnitWorth(snap, actorID, "wheat"); got != 1 {
		t.Errorf("wheat worth = %d, want 1 from his own purchases", got)
	}

	// With no history at all the catalog seed carries it: wholesale first.
	delete(snap.PriceBook, sim.PriceBookKey{SellerID: "josiah", Item: "wheat"})
	if got := offerItemUnitWorth(snap, actorID, "wheat"); got != 1 {
		t.Errorf("wheat worth = %d, want the catalog wholesale 1", got)
	}

	// A zero-coin history is not a price signal — a barter leg has units and no
	// coins, and pricing the good at nothing from it would invent a shortfall.
	free := sim.NewRingBuffer[sim.PriceObservation](8)
	free.Push(sim.PriceObservation{BuyerID: actorID, Amount: 0, Qty: 9, Consumers: 1, At: snap.PublishedAt.Add(-time.Hour)})
	snap.PriceBook[sim.PriceBookKey{SellerID: "josiah", Item: "wheat"}] = free
	if got := offerItemUnitWorth(snap, actorID, "wheat"); got != 1 {
		t.Errorf("wheat worth = %d, want the catalog 1 — a zero-coin leg must fall through, not price the good at 0", got)
	}

	// An uncatalogued good is unpriced, which is what makes the whole offer unjudged.
	if got := offerItemUnitWorth(snap, actorID, "whalebone_charm"); got != 0 {
		t.Errorf("uncatalogued good worth = %d, want 0", got)
	}
}

// TestRenderPayOffers_WorthClause pins the render half: the clause lands on the
// offer line for a short offer, and a fair offer's line is left exactly as it was.
func TestRenderPayOffers_WorthClause(t *testing.T) {
	nameOf := func(id sim.ActorID) string { return "Josiah Thorne" }
	offers := []sim.PayOfferWarrantReason{{
		LedgerID: 3710, Buyer: "josiah", Item: "flour", Qty: 7,
		PayItems: []sim.ItemKindQty{{Kind: "wheat", Qty: 7}},
	}}

	var short strings.Builder
	renderPayOffers(&short, offers, nameOf, nil, nil, map[sim.LedgerID]offerWorth{3710: offerWorthShort})
	if want := "— far less than the flour is worth"; !strings.Contains(short.String(), want) {
		t.Errorf("short offer line missing the verdict %q:\n%s", want, short.String())
	}

	var thin strings.Builder
	renderPayOffers(&thin, offers, nameOf, nil, nil, map[sim.LedgerID]offerWorth{3710: offerWorthThin})
	if want := "— a little less than the flour is worth"; !strings.Contains(thin.String(), want) {
		t.Errorf("thin offer line missing the verdict %q:\n%s", want, thin.String())
	}

	// A fair offer carries no entry at all (buildPayOfferWorth omits it), and an
	// absent key must render silence rather than an empty-string artifact.
	var fair strings.Builder
	renderPayOffers(&fair, offers, nameOf, nil, nil, nil)
	if strings.Contains(fair.String(), "is worth") {
		t.Errorf("a fair/unjudged offer must carry no verdict clause:\n%s", fair.String())
	}
	if got, want := fair.String(), "1. Josiah Thorne offers 7 wheat for 7 flour to keep (offer id 3710)\n"; !strings.Contains(got, want) {
		t.Errorf("unjudged offer line changed shape:\ngot  %q\nwant to contain %q", got, want)
	}
}

// TestOfferWorthAgreesWithTheRenderedWaresFigures is the whole point of resolving
// worth realized-first, pinned end to end (code_review asked for it): the verdict and
// the per-unit figures the model reads share one prompt, so a verdict resting on a
// catalog seed the wares cue contradicts would be worse than no verdict. Here the
// realized rate (5) and the catalog seed (3) DISAGREE, and the offer is short only
// under the realized rate — 7 sacks at 5 is 35 against 21 sheaves-worth of wheat,
// where the catalog seed would have graded it fair at 21 against 21.
func TestOfferWorthAgreesWithTheRenderedWaresFigures(t *testing.T) {
	snap, actorID, warrants := millerOfferedParitySwapForFlour()
	richer := sim.NewRingBuffer[sim.PriceObservation](8)
	richer.Push(sim.PriceObservation{BuyerID: "josiah", Amount: 30, Qty: 6, Consumers: 1, At: snap.PublishedAt.Add(-2 * time.Hour)})
	snap.PriceBook[sim.PriceBookKey{SellerID: actorID, Item: "flour"}] = richer
	snap.PayLedger[3710].PayItems = []sim.ItemKindQty{{Kind: "wheat", Qty: 21}}

	got := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))

	if want := "the shop has lately paid you about 5 coins each"; !strings.Contains(got, want) {
		t.Fatalf("wares cue is not showing the realized rate (%q), so this test proves nothing:\n%s", want, got)
	}
	if want := "— a little less than the flour is worth"; !strings.Contains(got, want) {
		t.Errorf("the verdict does not follow the figure the same prompt prints (%q missing):\n%s", want, got)
	}
}

// TestBuildPayOfferWorth_OmitsFairAndUnjudged keeps the map to the cases render has
// a phrase for. Carrying a "fair" entry would invite a later reader to give it one,
// which is the praise tier makingsMargin deliberately does without.
func TestBuildPayOfferWorth_OmitsFairAndUnjudged(t *testing.T) {
	snap, actorID, _ := millerOfferedParitySwapForFlour()
	offers := []sim.PayOfferWarrantReason{
		{LedgerID: 1, Item: "flour", Qty: 7, PayItems: []sim.ItemKindQty{{Kind: "wheat", Qty: 7}}},  // short
		{LedgerID: 2, Item: "flour", Qty: 7, PayItems: []sim.ItemKindQty{{Kind: "wheat", Qty: 25}}}, // fair
		{LedgerID: 3, Item: "flour", Qty: 7, Amount: 21},                                            // pure coin
	}
	got := buildPayOfferWorth(snap, actorID, offers)
	if len(got) != 1 {
		t.Fatalf("built %d entries, want 1 (only the short offer): %+v", len(got), got)
	}
	if got[1] != offerWorthShort {
		t.Errorf("offer 1 tier = %v, want offerWorthShort", got[1])
	}
}
