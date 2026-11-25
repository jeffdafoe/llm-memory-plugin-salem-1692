package perception

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// commission_offer_golden_test.go — LLM-1026, the live 2026-11-24 Ezekiel Crane
// case. He held no shovels, believed he held three, and accepted Elizabeth
// Ellis's 18 coins for three with "Here's your three shovels". His offer line
// said only "— you hold no shovels", and the order book afterwards said
// "#16312: 3 shovel for Elizabeth Ellis — you've yet to make it", which he could
// not tell apart from three shovels he had handed her earlier that day. Two
// goldens pin the fixed wording at each end of the deal; the invariant holds the
// offer-line rule across every scenario.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "smith_offered_three_shovels_holds_none",
			summary: "LLM-1026: the smith at his forge holding no shovels, with Elizabeth Ellis's standing coin-only " +
				"offer of 18 coins for 3 shovels to keep. Shovel is his to make, so accepting would not bounce; it " +
				"would take her coin now and leave the shovels owed. The offer line must say so ('— you hold no " +
				"shovels: accept, and you take payment now and owe Elizabeth Ellis 3 shovels you have yet to make'). " +
				"Live, the bare '— you hold no shovels' was overridden by a smith who believed he held them. " +
				"Cross-scenario guard: TestGoldensCommissionableShortfallSaysWhatAcceptOwes.",
			build: func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
				return smithAndElizabethShovels(true, false)
			},
		},
		perceptionScenario{
			name: "smith_owes_three_shovels_commission",
			summary: "LLM-1026: two minutes after the accept. The smith holds no shovels and owes Elizabeth Ellis a " +
				"commission of 3 shovels she paid 18 coins for. The '## Orders to deliver' line names who paid, " +
				"how much and when ('the 3 shovels Elizabeth Ellis paid 18 coins for 2m ago — you've yet to make " +
				"them'), so it cannot be folded into earlier sales of shovels to the same buyer. The old line, '3 " +
				"shovel for Elizabeth Ellis — you've yet to make it', led the live smith to call the order book an " +
				"engine tracking fault.",
			build: func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
				return smithAndElizabethShovels(false, true)
			},
		},
	)
}

// smithAndElizabethShovels is the forge at 15:00 with Ezekiel (no shovels, water
// on hand, the live shovel recipe) and Elizabeth in his conversation. withOffer
// adds her pending offer 16312 (3 shovels, 18 coins, coin-only, to keep);
// withOrder adds the commission order 16312 that accepting it mints.
func smithAndElizabethShovels(withOffer, withOrder bool) (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		smithID = sim.ActorID("ezekiel")
		buyerID = sim.ActorID("elizabeth")
		forge   = sim.StructureID("crane_forge")
		huddle  = sim.HuddleID("forge_huddle")
	)
	now := 900 // 15:00, on shift
	published := time.Date(2026, 11, 24, 20, 3, 0, 0, time.UTC)
	smith := shovelSmith(0)
	delete(smith.Inventory, "shovel")
	smith.Inventory["water"] = 6
	smith.CurrentHuddleID = huddle
	smith.Acquaintances = map[string]sim.Acquaintance{"Elizabeth Ellis": {}}
	elizabeth := &sim.ActorSnapshot{
		Kind:              sim.KindNPCShared,
		DisplayName:       "Elizabeth Ellis",
		Role:              "farmer",
		State:             sim.StateIdle,
		InsideStructureID: forge,
		CurrentHuddleID:   huddle,
		Coins:             100,
		Needs:             map[sim.NeedKey]int{},
	}
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalDateUTC:     time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC),
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{smithID: smith, buyerID: elizabeth},
		Structures:       map[sim.StructureID]*sim.Structure{forge: plainStructure(forge, "Crane Forge")},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			sim.VillageObjectID(forge): {ID: sim.VillageObjectID(forge), Pos: sim.WorldPos{X: 640, Y: 640}},
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			huddle: {ID: huddle, Members: map[sim.ActorID]struct{}{smithID: {}, buyerID: {}}},
		},
		Quotes: map[sim.QuoteID]*sim.SceneQuote{},
		Scenes: map[sim.SceneID]*sim.Scene{},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"shovel": {Name: "shovel", DisplayLabel: "Shovel", DisplayLabelSingular: "shovel", DisplayLabelPlural: "shovels", Category: sim.ItemCategory("tool")},
			"iron":   {Name: "iron", DisplayLabel: "bar iron", DisplayLabelSingular: "bar of iron", DisplayLabelPlural: "bars of iron", Category: sim.ItemCategoryMaterial},
			"water":  {Name: "water", DisplayLabel: "water", DisplayLabelSingular: "flask of water", DisplayLabelPlural: "flasks of water", Category: sim.ItemCategoryDrink},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"shovel": {
				OutputItem: "shovel", OutputQty: 1, RateQty: 1, RatePerHours: 4,
				Inputs:         []sim.RecipeInput{{Item: "water", Qty: 1}},
				SpeedInputs:    []sim.SpeedInput{{Item: "iron", Qty: 1, RatePct: 200}},
				WholesalePrice: 6, RetailPrice: 12,
			},
		},
		RestockReorderPct: 25,
	}
	if withOffer {
		snap.PayLedger = map[sim.LedgerID]*sim.PayLedgerEntry{
			16312: {
				ID: 16312, BuyerID: buyerID, SellerID: smithID,
				ItemKind: "shovel", Qty: 3, Amount: 18,
				ConsumerIDs: []sim.ActorID{buyerID},
				State:       sim.PayLedgerStatePending,
				CreatedAt:   published.Add(-20 * time.Second),
				ExpiresAt:   published.Add(3 * time.Minute),
			},
		}
	}
	if withOrder {
		snap.Orders = map[sim.OrderID]*sim.Order{
			16312: {
				ID:          16312,
				State:       sim.OrderStateReady,
				SellerID:    smithID,
				BuyerID:     buyerID,
				Item:        "shovel",
				Qty:         3,
				Amount:      18,
				ConsumerIDs: []sim.ActorID{buyerID},
				CreatedAt:   published.Add(-2 * time.Minute),
				ExpiresAt:   published.Add(14 * time.Hour),
			},
		}
	}
	return snap, smithID, nil
}

// TestGoldensCommissionableShortfallSaysWhatAcceptOwes: in every scenario, an
// offer line that carries a stock shortfall says what accepting commits the
// seller to exactly when accepting would mint a commission (the good is his to
// make, the offer is take-home and coin-only), and never otherwise, where accept
// bounces and the line must stay a plain warning.
func TestGoldensCommissionableShortfallSaysWhatAcceptOwes(t *testing.T) {
	const clause = "accept, and you take payment now"
	owedSeen, plainSeen := false, false
	for _, sc := range perceptionScenarios {
		snap, actorID, warrants := sc.build()
		p := Build(snap, actorID, warrants)
		offers := PendingPayOffers(p)
		if len(offers) == 0 {
			continue
		}
		out := combinedPrompt(Render(p, DefaultRenderConfig()))
		section := promptSection(out, "## Offers awaiting your decision")
		for _, o := range offers {
			if _, short := p.PayOfferShortfalls[o.LedgerID]; !short {
				continue
			}
			line := ""
			for _, l := range strings.Split(section, "\n") {
				if strings.Contains(l, fmt.Sprintf("(offer id %d)", o.LedgerID)) {
					line = l
				}
			}
			if line == "" {
				t.Errorf("scenario %q: short offer %d has no line in the offers section", sc.name, o.LedgerID)
				continue
			}
			// The accept path's own rule. Gifts never reach this lane
			// (buildPayOffersForMe sends them to the gift lane), and a bundle is
			// never a pending offer. TestCommissionPreviewMatchesAccept checks
			// the same prediction against a real accept, stock gate included.
			owed := sim.CommissionableOffer(snap.Recipes, snap.ItemKinds, snap.Actors[actorID].RestockPolicy, o.Item, false, o.ConsumeNow, len(o.PayItems) > 0)
			if has := strings.Contains(line, clause); has != owed {
				t.Errorf("scenario %q: offer %d says what accept owes = %v, want %v\n%s", sc.name, o.LedgerID, has, owed, line)
			}
			if owed {
				owedSeen = true
			} else {
				plainSeen = true
			}
		}
	}
	if !owedSeen || !plainSeen {
		t.Fatalf("invariant not exercised (commissionable=%v plain=%v)", owedSeen, plainSeen)
	}
}

// The partly-stocked arm: a seller holding some but not all of the ask owes the
// whole lot, handed over together.
func TestRenderPayOffers_CommissionPartlyStocked(t *testing.T) {
	var b strings.Builder
	offer := sim.PayOfferWarrantReason{LedgerID: 9, Buyer: "elizabeth", Item: "shovel", Qty: 3, Amount: 18}
	renderPayOffers(&b, []sim.PayOfferWarrantReason{offer},
		func(sim.ActorID) string { return "Elizabeth Ellis" },
		map[sim.LedgerID]StockShortfall{9: {Held: 1, Noun: "shovels", Owed: true, OwedQty: 3, OwedNoun: "shovels"}},
		nil, nil)
	want := "— you hold only 1 shovel: accept, and you take payment now and owe Elizabeth Ellis 3 shovels — all of them, handed over together once you have them"
	if !strings.Contains(b.String(), want) {
		t.Errorf("offer line missing %q:\n%s", want, b.String())
	}
}

// Stock owed on earlier orders: the line says why the held stock does not cover
// this offer, and a fully reserved seller has none free to hand over.
func TestRenderPayOffers_ReservedStock(t *testing.T) {
	offer := sim.PayOfferWarrantReason{LedgerID: 9, Buyer: "elizabeth", Item: "shovel", Qty: 3, Amount: 18}
	cases := []struct {
		sf   StockShortfall
		want string
	}{
		{StockShortfall{Held: 3, Reserved: 3, Noun: "shovels", Owed: true, OwedQty: 3, OwedNoun: "shovels"},
			"— you hold 3 shovel, but every one is owed on earlier orders: accept, and you take payment now and owe Elizabeth Ellis 3 shovels you have yet to make"},
		{StockShortfall{Held: 3, Reserved: 2, Noun: "shovels", Owed: true, OwedQty: 3, OwedNoun: "shovels"},
			"— you hold 3 shovel, 2 of them owed on earlier orders: accept, and you take payment now and owe Elizabeth Ellis 3 shovels — all of them, handed over together once you have them"},
	}
	for _, tc := range cases {
		var b strings.Builder
		renderPayOffers(&b, []sim.PayOfferWarrantReason{offer},
			func(sim.ActorID) string { return "Elizabeth Ellis" },
			map[sim.LedgerID]StockShortfall{9: tc.sf}, nil, nil)
		if !strings.Contains(b.String(), tc.want) {
			t.Errorf("offer line missing %q:\n%s", tc.want, b.String())
		}
	}
}

// A commission for several recipients counts every good owed, the same total
// the accept result named.
func TestBuildPendingOrderViews_TotalQtyAcrossRecipients(t *testing.T) {
	snap, smithID, _ := smithAndElizabethShovels(false, true)
	o := snap.Orders[16312]
	o.Qty = 1
	o.ConsumerIDs = []sim.ActorID{"elizabeth", "a", "b"}
	fromMe, _ := buildPendingOrderViews(snap, smithID)
	if len(fromMe) != 1 || fromMe[0].TotalQty != 3 || fromMe[0].Noun != "shovels" {
		t.Fatalf("fromMe = %+v, want TotalQty 3 with noun \"shovels\"", fromMe)
	}
	if got := awaitingMakeOrderPhrase(fromMe[0], "Elizabeth Ellis", snap.PublishedAt); !strings.HasPrefix(got, "the 3 shovels Elizabeth Ellis paid 18 coins for") {
		t.Errorf("phrase = %q, want the 3-shovel total", got)
	}
}

// The awaiting-make order line: one good, a deposit, and no clock.
func TestAwaitingMakeOrderPhrase(t *testing.T) {
	now := time.Date(2026, 11, 24, 21, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		o    OrderView
		want string
	}{
		{"single good", OrderView{Item: "shovel", Noun: "shovel", Qty: 1, Amount: 6, CreatedAt: now.Add(-30 * time.Minute)},
			"the shovel Elizabeth Ellis paid 6 coins for 30m ago"},
		{"deposit", OrderView{Item: "shovel", Noun: "shovels", Qty: 3, Amount: 18, BalanceDue: 12, DepositPaid: 6, CreatedAt: now.Add(-2 * time.Hour)},
			"the 3 shovels Elizabeth Ellis put a deposit on 2h ago"},
		{"no price, no clock", OrderView{Item: "shovel", Qty: 2},
			"the 2 shovel you promised Elizabeth Ellis"},
	}
	for _, tc := range cases {
		if got := awaitingMakeOrderPhrase(tc.o, "Elizabeth Ellis", now); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
	// One coin is singular.
	if got := awaitingMakeOrderPhrase(OrderView{Noun: "nail", Qty: 1, Amount: 1}, "Alice", time.Time{}); got != "the nail Alice paid 1 coin for" {
		t.Errorf("one coin: got %q", got)
	}
}

// What the seller takes at accept follows the offer's terms: the whole price,
// a deposit, or nothing. Goods for others are owed on the order, not to the payer.
func TestRenderPayOffers_CommissionTermsAndRecipients(t *testing.T) {
	sf := StockShortfall{Noun: "shovels", Owed: true, OwedQty: 3, OwedNoun: "shovels"}
	cases := []struct {
		name  string
		offer sim.PayOfferWarrantReason
		want  string
	}{
		{"zero price", sim.PayOfferWarrantReason{LedgerID: 9, Buyer: "elizabeth", Item: "shovel", Qty: 3},
			": accept, and owe Elizabeth Ellis 3 shovels you have yet to make"},
		{"deposit", sim.PayOfferWarrantReason{LedgerID: 9, Buyer: "elizabeth", Item: "shovel", Qty: 3, Amount: 18, Deposit: 6},
			": accept, and you take the deposit now and owe Elizabeth Ellis 3 shovels you have yet to make"},
		{"for others", sim.PayOfferWarrantReason{LedgerID: 9, Buyer: "elizabeth", Item: "shovel", Qty: 1, Amount: 18, ConsumerIDs: []sim.ActorID{"a", "b", "c"}},
			": accept, and you take payment now and owe 3 shovels on the order, which you have yet to make"},
		{"buyer is the one recipient", sim.PayOfferWarrantReason{LedgerID: 9, Buyer: "elizabeth", Item: "shovel", Qty: 3, Amount: 18, ConsumerIDs: []sim.ActorID{"elizabeth"}},
			": accept, and you take payment now and owe Elizabeth Ellis 3 shovels you have yet to make"},
	}
	for _, tc := range cases {
		var b strings.Builder
		renderPayOffers(&b, []sim.PayOfferWarrantReason{tc.offer},
			func(sim.ActorID) string { return "Elizabeth Ellis" },
			map[sim.LedgerID]StockShortfall{9: sf}, nil, nil)
		if !strings.Contains(b.String(), tc.want) {
			t.Errorf("%s: offer line missing %q:\n%s", tc.name, tc.want, b.String())
		}
	}
}

// After an accept with some of the goods free on hand, the order line says how
// many are on hand and how many are still to make, matching the accept result's
// "cannot yet hand over in full". Stock another order owes does not count.
func TestRenderOrders_AwaitingMakePartlyOnHand(t *testing.T) {
	render := func(held int, otherOwes int) string {
		snap, smithID, _ := smithAndElizabethShovels(false, true)
		snap.Actors[smithID].Inventory["shovel"] = held
		if otherOwes > 0 {
			snap.Orders[500] = &sim.Order{
				ID: 500, State: sim.OrderStateReady, SellerID: smithID, BuyerID: "x",
				Item: "shovel", Qty: otherOwes, ConsumerIDs: []sim.ActorID{"x"},
				CreatedAt: snap.PublishedAt, ExpiresAt: snap.PublishedAt.Add(time.Hour),
			}
		}
		fromMe, _ := buildPendingOrderViews(snap, smithID)
		var b strings.Builder
		var mine []OrderView
		for _, v := range fromMe {
			if v.ID == 16312 {
				mine = append(mine, v)
			}
		}
		renderOrdersReadyToHandOver(&b, mine, snap.PublishedAt)
		return b.String()
	}
	if got := render(1, 0); !strings.Contains(got, "the 3 shovels Elizabeth Ellis paid 18 coins for 2m ago — you've 1 on hand and 2 still to make") {
		t.Errorf("1 of 3 free on hand:\n%s", got)
	}
	if got := render(1, 1); !strings.Contains(got, "— you've yet to make them") || strings.Contains(got, "on hand") {
		t.Errorf("the one held shovel is owed on another order, so none is free:\n%s", got)
	}
}
