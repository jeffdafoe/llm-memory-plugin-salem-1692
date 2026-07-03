package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// production_inputs_test.go — LLM-82. The producer-side "## Keeping up
// production" cue: a bought input that is also a recipe input the producer
// consumes, surfaced with its runway when low. Gating mirrors Restocking.

// productionCatalog: labels for the goods and inputs in these fixtures.
func productionCatalog() map[sim.ItemKind]*sim.ItemKindDef {
	return map[sim.ItemKind]*sim.ItemKindDef{
		"stew":    {Name: "stew", DisplayLabel: "stew", Category: sim.ItemCategoryFood},
		"skillet": {Name: "skillet", DisplayLabel: "skillet"},
		"carrots": {Name: "carrots", DisplayLabel: "carrots", Category: sim.ItemCategoryFood},
	}
}

// stewRecipe returns a stew recipe whose one bought input is `input` consumed
// `perBatch` per 30-stew batch (skillet 1, carrots 30).
func stewRecipe(input sim.ItemKind, perBatch int) map[sim.ItemKind]*sim.ItemRecipe {
	return map[sim.ItemKind]*sim.ItemRecipe{
		"stew": {
			OutputItem: "stew", OutputQty: 30, RateQty: 30, RatePerHours: 6,
			Inputs: []sim.RecipeInput{{Item: input, Qty: perBatch}},
		},
	}
}

// makesStewBuying builds an actor that PRODUCES stew and BUYS `input` (cap), with
// `onHand` of the input in inventory.
func makesStewBuying(input sim.ItemKind, cap, onHand int) *sim.ActorSnapshot {
	return &sim.ActorSnapshot{
		Inventory: map[sim.ItemKind]int{input: onHand},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "stew", Source: sim.RestockSourceProduce, Max: 60},
			{Item: input, Source: sim.RestockSourceBuy, Max: cap},
		}},
	}
}

func productionSnap(subj *sim.ActorSnapshot, recipes map[sim.ItemKind]*sim.ItemRecipe) *sim.Snapshot {
	return &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"john": subj},
		ItemKinds:         productionCatalog(),
		Recipes:           recipes,
		RestockReorderPct: 25,
	}
}

// A tool consumed 1-per-batch (skillet) surfaces at the last unit with the exact
// wear runway (1 skillet × 30-stew batch = 30 stews).
func TestBuildProductionInputs_SkilletLowSurfacesRunway(t *testing.T) {
	subj := makesStewBuying("skillet", 2, 1) // cap 2 @ 25% = 0.5 → fires at the last unit
	snap := productionSnap(subj, stewRecipe("skillet", 1))

	v := buildProductionInputs(snap, subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("expected one production-input item, got %+v", v)
	}
	it := v.Items[0]
	if it.InputKind != "skillet" || it.OutputKind != "stew" || it.CurrentQty != 1 || it.RunwayUnits != 30 {
		t.Fatalf("got %+v, want skillet→stew, 1 on hand, runway 30", it)
	}

	var b strings.Builder
	renderProductionInputs(&b, v)
	out := b.String()
	for _, want := range []string{"## Keeping up production", "You use skillet to make stew", "1 on hand", "about 30 more", "running low"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q\n--- got ---\n%s", want, out)
		}
	}
}

// A bulk input consumed in step with the output (carrots, 30 per 30-stew batch)
// uses the effective per-unit rate: 7 carrots → about 7 stews.
func TestBuildProductionInputs_BulkInputRunway(t *testing.T) {
	subj := makesStewBuying("carrots", 30, 7) // cap 30 @ 25% = 7.5 → 7 is below
	snap := productionSnap(subj, stewRecipe("carrots", 30))

	v := buildProductionInputs(snap, subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("expected one item, got %+v", v)
	}
	if got := v.Items[0].RunwayUnits; got != 7 {
		t.Errorf("carrots runway = %d, want 7 (7 × 30 / 30)", got)
	}
}

// At full stock the input isn't low, so the section is omitted.
func TestBuildProductionInputs_FullStockNil(t *testing.T) {
	subj := makesStewBuying("skillet", 2, 2) // full → 2 <= 1 false
	snap := productionSnap(subj, stewRecipe("skillet", 1))
	if v := buildProductionInputs(snap, subj); v != nil {
		t.Errorf("full-stock input should not surface, got %+v", v)
	}
}

// An input the actor PRODUCES itself (no buy entry) never surfaces — it's not a
// buy-restock concern, so the producer cue stays silent on it.
func TestBuildProductionInputs_SelfProducedInputIgnored(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Inventory: map[sim.ItemKind]int{"skillet": 0},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "stew", Source: sim.RestockSourceProduce, Max: 60},
			{Item: "skillet", Source: sim.RestockSourceProduce, Max: 5}, // makes its own — not bought
		}},
	}
	snap := productionSnap(subj, stewRecipe("skillet", 1))
	if v := buildProductionInputs(snap, subj); v != nil {
		t.Errorf("a self-produced input must not surface as a buy concern, got %+v", v)
	}
}

// A bought item that no produced recipe consumes is not a production input, so it
// stays in Restocking's lane and doesn't surface here.
func TestBuildProductionInputs_BoughtButNotConsumedIgnored(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Inventory: map[sim.ItemKind]int{"skillet": 0},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "stew", Source: sim.RestockSourceProduce, Max: 60},
			{Item: "skillet", Source: sim.RestockSourceBuy, Max: 2},
		}},
	}
	// stew's recipe consumes carrots, not skillet — so the low skillet is irrelevant here.
	snap := productionSnap(subj, stewRecipe("carrots", 30))
	if v := buildProductionInputs(snap, subj); v != nil {
		t.Errorf("a bought item no recipe consumes must not surface, got %+v", v)
	}
}

// pct 0 disables the feature (operator off-switch), same as Restocking.
func TestBuildProductionInputs_DisabledNil(t *testing.T) {
	subj := makesStewBuying("skillet", 2, 1)
	snap := productionSnap(subj, stewRecipe("skillet", 1))
	snap.RestockReorderPct = 0
	if v := buildProductionInputs(snap, subj); v != nil {
		t.Errorf("pct 0 should disable the section, got %+v", v)
	}
}

// The cue carries no supplier, structure_id, or pay_with_item — that's
// Restocking's job. The LLM-64 split: this section motivates, Restocking acts.
func TestRenderProductionInputs_NoBuyMechanics(t *testing.T) {
	subj := makesStewBuying("skillet", 2, 1)
	snap := productionSnap(subj, stewRecipe("skillet", 1))
	var b strings.Builder
	renderProductionInputs(&b, buildProductionInputs(snap, subj))
	out := b.String()
	for _, forbidden := range []string{"structure_id", "pay_with_item", "buy from", "move_to"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("producer cue must not carry buy mechanics, found %q in:\n%s", forbidden, out)
		}
	}
}

// A corrupt negative on-hand reads as "out" (0), not a negative count/runway.
func TestBuildProductionInputs_NegativeInventoryClampedToZero(t *testing.T) {
	subj := makesStewBuying("skillet", 2, -3)
	snap := productionSnap(subj, stewRecipe("skillet", 1))
	v := buildProductionInputs(snap, subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("a negative (out-of-stock) input should still surface as low, got %+v", v)
	}
	if it := v.Items[0]; it.CurrentQty != 0 || it.RunwayUnits != 0 {
		t.Errorf("negative on-hand should clamp to 0 count / 0 runway, got %+v", it)
	}
}

// ---- Optional boosters (LLM-248) ----------------------------------------

// milkRecipeBoostedBySage: the LLM-83 dairy edge fixture — milk produced in
// 4-unit batches, optionally boosted by 1 sage for +2.
func milkRecipeBoostedBySage() map[sim.ItemKind]*sim.ItemRecipe {
	return map[sim.ItemKind]*sim.ItemRecipe{
		"milk": {
			OutputItem: "milk", OutputQty: 4, RateQty: 4, RatePerHours: 1,
			BoostInputs: []sim.BoostInput{{Item: "sage", Qty: 1, BonusQty: 2}},
		},
	}
}

// makesMilkBuyingSage builds an actor producing milk and buying sage (cap), with
// `onHand` sage.
func makesMilkBuyingSage(cap, onHand int) *sim.ActorSnapshot {
	return &sim.ActorSnapshot{
		Inventory: map[sim.ItemKind]int{"sage": onHand},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "milk", Source: sim.RestockSourceProduce, Max: 30},
			{Item: "sage", Source: sim.RestockSourceBuy, Max: cap},
		}},
	}
}

func boosterSnap(subj *sim.ActorSnapshot) *sim.Snapshot {
	catalog := productionCatalog()
	catalog["milk"] = &sim.ItemKindDef{Name: "milk", DisplayLabel: "milk", Category: sim.ItemCategoryDrink}
	catalog["sage"] = &sim.ItemKindDef{Name: "sage", DisplayLabel: "sage"}
	return &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"elizabeth": subj},
		ItemKinds:         catalog,
		Recipes:           milkRecipeBoostedBySage(),
		RestockReorderPct: 25,
	}
}

// A low bought booster surfaces with the forgone bonus (no runway — production
// continues without it).
func TestBuildProductionInputs_LowBoosterSurfacesBonus(t *testing.T) {
	subj := makesMilkBuyingSage(3, 0)
	snap := boosterSnap(subj)

	v := buildProductionInputs(snap, subj)
	if v == nil || len(v.Boosts) != 1 || len(v.Items) != 0 {
		t.Fatalf("expected exactly one booster view, got %+v", v)
	}
	bo := v.Boosts[0]
	if bo.BoostKind != "sage" || bo.OutputKind != "milk" || bo.CurrentQty != 0 || bo.BonusQty != 2 {
		t.Fatalf("got %+v, want sage→milk, 0 on hand, bonus 2", bo)
	}

	var b strings.Builder
	renderProductionInputs(&b, v)
	out := b.String()
	for _, want := range []string{"## Keeping up production", "A measure of sage in each batch of milk adds 2 extra", "0 on hand", "running low"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q\n--- got ---\n%s", want, out)
		}
	}
}

// A stocked booster isn't low, so no booster line (and with no low required
// inputs either, the whole section is omitted).
func TestBuildProductionInputs_StockedBoosterNil(t *testing.T) {
	subj := makesMilkBuyingSage(3, 3)
	snap := boosterSnap(subj)
	if v := buildProductionInputs(snap, subj); v != nil {
		t.Errorf("a full-stock booster should not surface, got %+v", v)
	}
}

// A booster without a buy entry (self-foraged, e.g. Prudence's own sage) never
// surfaces — same self-supplied exclusion as required inputs.
func TestBuildProductionInputs_SelfForagedBoosterIgnored(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Inventory: map[sim.ItemKind]int{"sage": 0},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "milk", Source: sim.RestockSourceProduce, Max: 30},
			{Item: "sage", Source: sim.RestockSourceForage, Max: 5}, // forages her own — not bought
		}},
	}
	snap := boosterSnap(subj)
	if v := buildProductionInputs(snap, subj); v != nil {
		t.Errorf("a self-foraged booster must not surface as a buy concern, got %+v", v)
	}
}

// The booster line carries no buy mechanics either (LLM-64 split).
func TestRenderProductionInputs_BoosterNoBuyMechanics(t *testing.T) {
	subj := makesMilkBuyingSage(3, 0)
	snap := boosterSnap(subj)
	var b strings.Builder
	renderProductionInputs(&b, buildProductionInputs(snap, subj))
	out := b.String()
	for _, forbidden := range []string{"structure_id", "pay_with_item", "buy from", "move_to"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("booster cue must not carry buy mechanics, found %q in:\n%s", forbidden, out)
		}
	}
}
