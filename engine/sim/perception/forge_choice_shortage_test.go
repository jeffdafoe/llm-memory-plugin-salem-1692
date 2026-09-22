package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// forge_choice_shortage_test.go — LLM-658: a standing village shortage of a
// good the producer makes reads as demand in "## Your trade". The sales read
// (movementTier) cannot see it — a request the seller cannot fill never becomes
// a sale — so live the only meat producer ranked meat last for six weeks while
// the Tavern went without stew, and the only whetstone maker ranked stones
// behind nails for ten days while the wright's service income went to nothing.
// The LLM-656 shortage record already lists every (keeper, good) the village
// has gone without with no supplier holding any; a good of the producer's that
// stands in it leads the scene and names the shop and the days.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "dairy_choosing_with_tavern_meat_shortage",
			summary: "LLM-658: Elizabeth Ellis idle at her farm with milk selling briskly, cheese steadily and no " +
				"meat on hand, while the shortage record says the Tavern has been without meat for six days. " +
				"'## Your trade' leads with meat — 'The Tavern has been without meat for 6 days' in the demand " +
				"slot — ahead of the goods that are selling; the live shape that left the Tavern without stew.",
			build: dairyChoosingWithTavernMeatShortage,
		},
		perceptionScenario{
			name: "smith_choosing_with_wright_whetstone_shortage",
			summary: "LLM-658: Ezekiel idle at his forge with nails selling and no whetstone on hand, while the " +
				"shortage record says Lewis's Workshop has been without whetstones for three days (a buy-line " +
				"shortage, LLM-657). '## Your trade' leads with whetstones ahead of the nails that are selling.",
			build: smithChoosingWithWrightWhetstoneShortage,
		},
	)
}

const (
	shortageDairyID   = sim.ActorID("elizabeth")
	shortageTavernID  = sim.ActorID("john")
	shortageEllisFarm = sim.StructureID("ellis-farm")
	shortageTavern    = sim.StructureID("tavern")
)

// dairyShortageSnapshot is Elizabeth Ellis idle at Ellis Farm on shift with
// nothing in the works: milk (brisk, 12 sold against a batch of 4), cheese
// (steady, 2 sold against a batch of 2) and meat (nothing sold — nobody could
// buy any). inventory overrides her stock; shortages is the standing record.
func dairyShortageSnapshot(inventory map[sim.ItemKind]int, shortages []sim.InputShortage) *sim.Snapshot {
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	published := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	elizabeth := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Elizabeth Ellis",
		Role:              "farmer",
		State:             sim.StateIdle,
		WorkStructureID:   shortageEllisFarm,
		InsideStructureID: shortageEllisFarm,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             9,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         inventory,
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "milk", Source: sim.RestockSourceProduce, Max: 40},
			{Item: "cheese", Source: sim.RestockSourceProduce, Max: 20},
			{Item: "meat", Source: sim.RestockSourceProduce, Max: 12},
		}},
	}
	john := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateSleeping,
		WorkStructureID:   shortageTavern,
		InsideStructureID: shortageTavern,
		HomeStructureID:   shortageTavern,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "stew", Source: sim.RestockSourceProduce, Max: 30},
			{Item: "meat", Source: sim.RestockSourceBuy, Max: 12},
		}},
	}
	milkSales := sim.NewRingBuffer[sim.PriceObservation](8)
	cheeseSales := sim.NewRingBuffer[sim.PriceObservation](8)
	for day := 1; day <= 3; day++ {
		at := published.Add(-time.Duration(day) * 24 * time.Hour)
		milkSales.Push(sim.PriceObservation{BuyerID: shortageTavernID, Amount: 4, Qty: 4, Consumers: 1, At: at})
	}
	cheeseSales.Push(sim.PriceObservation{BuyerID: shortageTavernID, Amount: 6, Qty: 2, Consumers: 1, At: published.Add(-2 * 24 * time.Hour)})
	return &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{shortageDairyID: elizabeth, shortageTavernID: john},
		Structures: map[sim.StructureID]*sim.Structure{
			shortageEllisFarm: plainStructure(shortageEllisFarm, "Ellis Farm"),
			shortageTavern:    plainStructure(shortageTavern, "The Tavern"),
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"milk":   {OutputItem: "milk", OutputQty: 4, RateQty: 4, RatePerHours: 2, WholesalePrice: 1, RetailPrice: 1},
			"cheese": {OutputItem: "cheese", OutputQty: 2, RateQty: 2, RatePerHours: 3, WholesalePrice: 2, RetailPrice: 3, Inputs: []sim.RecipeInput{{Item: "milk", Qty: 2}}},
			"meat":   {OutputItem: "meat", OutputQty: 1, RateQty: 1, RatePerHours: 4, WholesalePrice: 2, RetailPrice: 4},
			"stew":   {OutputItem: "stew", OutputQty: 6, RateQty: 30, RatePerHours: 6, WholesalePrice: 3, RetailPrice: 5, Inputs: []sim.RecipeInput{{Item: "meat", Qty: 2}}},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"milk":   {Name: "milk", DisplayLabel: "Milk", DisplayLabelSingular: "pail of milk", DisplayLabelPlural: "pails of milk", Capabilities: []string{"portable"}, Category: sim.ItemCategoryDrink},
			"cheese": {Name: "cheese", DisplayLabel: "Cheese", DisplayLabelSingular: "wheel of cheese", DisplayLabelPlural: "wheels of cheese", Capabilities: []string{"portable"}, Category: sim.ItemCategoryFood},
			"meat":   {Name: "meat", DisplayLabel: "Meat", DisplayLabelSingular: "cut of meat", DisplayLabelPlural: "cuts of meat", Capabilities: []string{"portable"}, Category: sim.ItemCategoryFood},
			"stew":   {Name: "stew", DisplayLabel: "Stew", DisplayLabelSingular: "bowl of stew", DisplayLabelPlural: "bowls of stew", Category: sim.ItemCategoryFood},
		},
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: shortageDairyID, Item: "milk"}:   milkSales,
			{SellerID: shortageDairyID, Item: "cheese"}: cheeseSales,
		},
		Environment: sim.WorldEnvironment{InputShortages: shortages},
	}
}

func dairyStock() map[sim.ItemKind]int {
	return map[sim.ItemKind]int{"milk": 14, "cheese": 4}
}

func tavernMeatShortage(days int) []sim.InputShortage {
	return []sim.InputShortage{{KeeperID: shortageTavernID, Item: "meat", Days: days}}
}

func dairyChoosingWithTavernMeatShortage() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap := dairyShortageSnapshot(dairyStock(), tavernMeatShortage(6))
	warrants := []sim.WarrantMeta{
		{TriggerActorID: shortageDairyID, Reason: sim.ProductionChoiceWarrantReason{}, SourceEventID: 1},
	}
	return snap, shortageDairyID, warrants
}

// smithChoosingWithWrightWhetstoneShortage is the whetstone case: a buy-line
// shortage (LLM-657) of a good the smith makes, listed against the wright who
// owns his workshop with no businessowner attribute.
func smithChoosingWithWrightWhetstoneShortage() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		ezekielID = sim.ActorID("ezekiel")
		lewisID   = sim.ActorID("lewis")
		forge     = sim.StructureID("blacksmith")
		workshop  = sim.StructureID("workshop")
	)
	start, end := 360, 1080
	now := 600
	published := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	ezekiel := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Ezekiel Crane",
		Role:              "blacksmith",
		State:             sim.StateIdle,
		WorkStructureID:   forge,
		InsideStructureID: forge,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             12,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"nail": 6},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "nail", Source: sim.RestockSourceProduce, Max: 20},
			{Item: sim.WhetstoneKind, Source: sim.RestockSourceProduce, Max: 6},
		}},
	}
	lewis := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Lewis Walker",
		Role:            "wright",
		State:           sim.StateIdle,
		WorkStructureID: workshop,
		Needs:           map[sim.NeedKey]int{},
		Inventory:       map[sim.ItemKind]int{},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: sim.WhetstoneKind, Source: sim.RestockSourceBuy, Max: 4},
		}},
	}
	nailSales := sim.NewRingBuffer[sim.PriceObservation](8)
	nailSales.Push(sim.PriceObservation{BuyerID: lewisID, Amount: 10, Qty: 5, Consumers: 1, At: published.Add(-24 * time.Hour)})
	snap := &sim.Snapshot{
		PublishedAt:      published,
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{ezekielID: ezekiel, lewisID: lewis},
		Structures: map[sim.StructureID]*sim.Structure{
			forge:    plainStructure(forge, "Blacksmith"),
			workshop: plainStructure(workshop, "Lewis's Workshop"),
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"nail":            {OutputItem: "nail", OutputQty: 5, RateQty: 5, RatePerHours: 1, WholesalePrice: 1, RetailPrice: 2},
			sim.WhetstoneKind: {OutputItem: sim.WhetstoneKind, OutputQty: 1, RateQty: 1, RatePerHours: 2, WholesalePrice: 2, RetailPrice: 4},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"nail":            {Name: "nail", DisplayLabel: "Nails", DisplayLabelSingular: "nail", DisplayLabelPlural: "nails", Capabilities: []string{"portable"}},
			sim.WhetstoneKind: {Name: sim.WhetstoneKind, DisplayLabel: "Whetstone", DisplayLabelSingular: "whetstone", DisplayLabelPlural: "whetstones", Capabilities: []string{"portable"}},
		},
		PriceBook: map[sim.PriceBookKey]*sim.RingBuffer[sim.PriceObservation]{
			{SellerID: ezekielID, Item: "nail"}: nailSales,
		},
		Environment: sim.WorldEnvironment{InputShortages: []sim.InputShortage{
			{KeeperID: lewisID, Item: sim.WhetstoneKind, Days: 3},
		}},
	}
	warrants := []sim.WarrantMeta{
		{TriggerActorID: ezekielID, Reason: sim.ProductionChoiceWarrantReason{}, SourceEventID: 1},
	}
	return snap, ezekielID, warrants
}

// tradeOrder lists the goods a view narrates, in order.
func tradeOrder(view *ForgeChoiceView) []sim.ItemKind {
	if view == nil {
		return nil
	}
	kinds := make([]sim.ItemKind, len(view.Items))
	for i, it := range view.Items {
		kinds[i] = it.itemKind
	}
	return kinds
}

func sameOrder(got, want []sim.ItemKind) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestForgeChoiceShortageLeadsAndNamesTheShop pins the mechanism: with the
// Tavern six days short of meat, meat leads the scene ahead of the milk that is
// selling briskly, the item carries the shop and the days, and the rendered
// demand slot says so in place of the sell-through clause.
func TestForgeChoiceShortageLeadsAndNamesTheShop(t *testing.T) {
	snap := dairyShortageSnapshot(dairyStock(), tavernMeatShortage(6))
	view := buildForgeChoice(snap, shortageDairyID, snap.Actors[shortageDairyID])
	if got, want := tradeOrder(view), []sim.ItemKind{"meat", "milk", "cheese"}; !sameOrder(got, want) {
		t.Fatalf("trade order = %v, want %v (a standing shortage leads, then sales)", got, want)
	}
	sh := view.Items[0].Shortage
	if sh == nil || sh.Where != "The Tavern" || sh.Good != "meat" || sh.Days != 6 {
		t.Fatalf("meat.Shortage = %+v, want {Where: The Tavern, Good: meat, Days: 6}", sh)
	}
	for _, it := range view.Items[1:] {
		if it.Shortage != nil {
			t.Errorf("%s carries a shortage %+v; only the short good should", it.itemKind, it.Shortage)
		}
	}
	scene := tradeGoodScene(view.Items[0])
	const want = "You have no cuts of meat on hand, and The Tavern has been without meat for 6 days — none is to be had in the village."
	if !strings.Contains(scene, want) {
		t.Errorf("meat scene = %q, want it to contain %q", scene, want)
	}
	if strings.Contains(scene, "sold this past week") || strings.Contains(scene, "sales were steady") {
		t.Errorf("meat scene = %q still voices the sell-through the shortage replaces", scene)
	}
}

// TestForgeChoiceShortageDayPhrase: a first-day entry reads "a day", not "1 days".
func TestForgeChoiceShortageDayPhrase(t *testing.T) {
	snap := dairyShortageSnapshot(dairyStock(), tavernMeatShortage(1))
	view := buildForgeChoice(snap, shortageDairyID, snap.Actors[shortageDairyID])
	if scene := tradeGoodScene(view.Items[0]); !strings.Contains(scene, "without meat for a day —") {
		t.Errorf("meat scene = %q, want 'without meat for a day'", scene)
	}
}

// TestForgeChoiceNoShortageKeepsSalesOrder is the control: with an empty record
// the scene is the pre-LLM-658 one — sales order, no shortage on any item.
func TestForgeChoiceNoShortageKeepsSalesOrder(t *testing.T) {
	snap := dairyShortageSnapshot(dairyStock(), nil)
	view := buildForgeChoice(snap, shortageDairyID, snap.Actors[shortageDairyID])
	if got, want := tradeOrder(view), []sim.ItemKind{"milk", "cheese", "meat"}; !sameOrder(got, want) {
		t.Fatalf("trade order = %v, want %v (sales order when nothing is short)", got, want)
	}
	for _, it := range view.Items {
		if it.Shortage != nil {
			t.Errorf("%s carries a shortage %+v with an empty record", it.itemKind, it.Shortage)
		}
	}
}

// TestForgeChoiceShortageOfOtherGoodsIsIgnored: a shortage of a good this
// producer does not make, or one whose keeper is gone from the snapshot, leaves
// the scene as the control renders it.
func TestForgeChoiceShortageOfOtherGoodsIsIgnored(t *testing.T) {
	cases := map[string][]sim.InputShortage{
		"a good she does not make": {{KeeperID: shortageTavernID, Item: "ale", Days: 5}},
		"a keeper no longer here":  {{KeeperID: "departed", Item: "meat", Days: 5}},
		"a zero-day entry":         {{KeeperID: shortageTavernID, Item: "meat", Days: 0}},
	}
	for name, record := range cases {
		snap := dairyShortageSnapshot(dairyStock(), record)
		view := buildForgeChoice(snap, shortageDairyID, snap.Actors[shortageDairyID])
		if got, want := tradeOrder(view), []sim.ItemKind{"milk", "cheese", "meat"}; !sameOrder(got, want) {
			t.Errorf("%s: trade order = %v, want %v", name, got, want)
		}
		for _, it := range view.Items {
			if it.Shortage != nil {
				t.Errorf("%s: %s carries a shortage %+v", name, it.itemKind, it.Shortage)
			}
		}
	}
}

// TestForgeChoiceShortageNeverRevivesAnInputShortGood pins the LLM-324 order of
// operations: a shortage of cheese while Elizabeth holds no milk to make it
// with leaves cheese dropped from the menu — listing it would advertise a
// produce call the tool rejects. The shortage cannot lift the craftability gate.
func TestForgeChoiceShortageNeverRevivesAnInputShortGood(t *testing.T) {
	record := []sim.InputShortage{{KeeperID: shortageTavernID, Item: "cheese", Days: 4}}
	snap := dairyShortageSnapshot(map[sim.ItemKind]int{"cheese": 1}, record)
	view := buildForgeChoice(snap, shortageDairyID, snap.Actors[shortageDairyID])
	if got, want := tradeOrder(view), []sim.ItemKind{"milk", "meat"}; !sameOrder(got, want) {
		t.Fatalf("trade order = %v, want %v (cheese input-short stays dropped, LLM-324)", got, want)
	}
}

// TestForgeChoiceLongestShortageLeads: two short goods list longest-standing
// first, and a good short at two shops names the shop that has waited longest.
func TestForgeChoiceLongestShortageLeads(t *testing.T) {
	const innID = sim.ActorID("hannah")
	record := []sim.InputShortage{
		{KeeperID: innID, Item: "meat", Days: 9},
		{KeeperID: shortageTavernID, Item: "cheese", Days: 2},
		{KeeperID: shortageTavernID, Item: "meat", Days: 6},
	}
	snap := dairyShortageSnapshot(map[sim.ItemKind]int{"milk": 14}, record) // no cheese on hand, or its entry is stale
	snap.Structures["inn"] = plainStructure("inn", "The Inn")
	snap.Actors[innID] = &sim.ActorSnapshot{Kind: sim.KindNPCStateful, DisplayName: "Hannah Ward", WorkStructureID: "inn",
		Inventory: map[sim.ItemKind]int{}, Needs: map[sim.NeedKey]int{}}
	view := buildForgeChoice(snap, shortageDairyID, snap.Actors[shortageDairyID])
	if got, want := tradeOrder(view), []sim.ItemKind{"meat", "cheese", "milk"}; !sameOrder(got, want) {
		t.Fatalf("trade order = %v, want %v (longest-standing shortage leads)", got, want)
	}
	if sh := view.Items[0].Shortage; sh == nil || sh.Where != "The Inn" || sh.Days != 9 {
		t.Errorf("meat.Shortage = %+v, want the Inn's nine days over the Tavern's six", sh)
	}
}

// TestForgeChoiceOwnStockEndsTheShortage: a record entry that has outlived the
// shelves — the producer landed a batch since the last revalidation — is
// ignored, so "none is to be had in the village" never renders beside her own
// stock line. By the sweep's supplier test, a producer holding any of the good
// IS the end of the shortage.
func TestForgeChoiceOwnStockEndsTheShortage(t *testing.T) {
	stock := dairyStock()
	stock["meat"] = 3
	snap := dairyShortageSnapshot(stock, tavernMeatShortage(6))
	view := buildForgeChoice(snap, shortageDairyID, snap.Actors[shortageDairyID])
	if got, want := tradeOrder(view), []sim.ItemKind{"milk", "cheese", "meat"}; !sameOrder(got, want) {
		t.Fatalf("trade order = %v, want %v (a stale entry does not lead)", got, want)
	}
	for _, it := range view.Items {
		if it.Shortage != nil {
			t.Errorf("%s carries a shortage %+v while the producer holds the good", it.itemKind, it.Shortage)
		}
	}
	if scene := renderScenario(perceptionScenario{build: func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
		return snap, shortageDairyID, nil
	}}); strings.Contains(scene, "has been without") {
		t.Errorf("stale shortage rendered beside the producer's own stock:\n%s", scene)
	}
}

// TestGoldensShortageGoodLeadsTheTradeScene is the LLM-658 cross-scenario
// invariant: wherever a "## Your trade" scene carries goods in a standing
// shortage, they are narrated as a leading prefix — every shortage good before
// every other, longest-standing first — each naming the shop that has gone
// without; and the shortage line never appears without a shortage item behind
// it. Runs over the whole matrix so a future sort tweak cannot bury the one
// good the village is waiting on beneath whatever is selling.
func TestGoldensShortageGoodLeadsTheTradeScene(t *testing.T) {
	const marker = "has been without"
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, _ := sc.build()
			as := snap.Actors[actorID]
			if as == nil {
				return
			}
			view := buildForgeChoice(snap, actorID, as)
			rendered := strings.Contains(renderScenario(sc), marker)
			if view == nil || len(view.Items) == 0 {
				if rendered {
					t.Errorf("scenario %q: renders %q with no trade scene", sc.name, marker)
				}
				return
			}
			short := false
			prefixEnded := false
			lastDays := 0
			for _, it := range view.Items {
				if it.Shortage == nil {
					prefixEnded = true
					continue
				}
				short = true
				if prefixEnded {
					t.Errorf("scenario %q: shortage good %s narrated after a good that is not short; standing shortages lead (LLM-658)", sc.name, it.itemKind)
				}
				if lastDays > 0 && it.Shortage.Days > lastDays {
					t.Errorf("scenario %q: shortage good %s (%d days) narrated after a shorter one (%d days); longest-standing leads", sc.name, it.itemKind, it.Shortage.Days, lastDays)
				}
				lastDays = it.Shortage.Days
				if it.Shortage.Where == "" || it.Shortage.Days < 1 {
					t.Errorf("scenario %q: shortage on %s carries no shop or days: %+v", sc.name, it.itemKind, it.Shortage)
				}
			}
			if short != rendered {
				t.Errorf("scenario %q: shortage item present=%v, %q rendered=%v — the line and the item must travel together", sc.name, short, marker, rendered)
			}
		})
	}
}
