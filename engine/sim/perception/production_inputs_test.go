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
// A purse by default: the "## Keeping up production" runway only surfaces for an
// input the producer has an actionable buy path to (LLM-260), and a buy path now needs
// MEANS TO PAY (LLM-406). A coinless producer holding nothing but the input it is
// short of cannot buy that input — a good is not payment for itself — so it would
// surface no runway at all, and none of these batch-floor/runway cases would be
// exercising what they mean to.
func makesStewBuying(input sim.ItemKind, cap, onHand int) *sim.ActorSnapshot {
	return &sim.ActorSnapshot{
		Coins:     50,
		Inventory: map[sim.ItemKind]int{input: onHand},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "stew", Source: sim.RestockSourceProduce, Max: 60},
			{Item: input, Source: sim.RestockSourceBuy, Max: cap},
		}},
	}
}

func productionSnap(subj *sim.ActorSnapshot, recipes map[sim.ItemKind]*sim.ItemRecipe, vendorOf ...sim.ItemKind) *sim.Snapshot {
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"john": subj},
		ItemKinds:         productionCatalog(),
		Recipes:           recipes,
		RestockReorderPct: 25,
	}
	for _, item := range vendorOf {
		addSnapSupplier(snap, item)
	}
	return snap
}

// addSnapSupplier wires a first-hand supplier of item into the snapshot — a
// stocked keeper at its own workplace with a `produce` entry for the item, so
// it passes the LLM-252 supplier gate. Production-input lines are gated on an
// actionable buy path (LLM-260), so fixtures exercising a different gate add
// one of these to keep the path open.
func addSnapSupplier(snap *sim.Snapshot, item sim.ItemKind) {
	id := sim.ActorID("supplier-" + string(item))
	sid := sim.StructureID("shop-" + string(item))
	snap.Actors[id] = &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Supplier of " + string(item),
		State:           sim.StateIdle,
		WorkStructureID: sid,
		Inventory:       map[sim.ItemKind]int{item: 20},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: item, Source: sim.RestockSourceProduce, Max: 40},
		}},
	}
	if snap.Structures == nil {
		snap.Structures = map[sim.StructureID]*sim.Structure{}
	}
	snap.Structures[sid] = plainStructure(sid, "Shop of "+string(item))
}

// TestBuildRefillCues_DegradedBusinessSuppressed — LLM-304: a degraded business is
// shut for restock/production, so BOTH refill cues ("## Restocking" and "## Keeping
// up production") go silent — neither may steer a buy the shut shop can't turn into
// stock, which would fight the "## Your business" cue's "can't restock until mended".
// Same low-on-a-bought-input producer renders both cues until the business degrades.
func TestBuildRefillCues_DegradedBusinessSuppressed(t *testing.T) {
	subj := makesStewBuying("skillet", 10, 1) // 1/10 = 10% < 25% → low
	snap := productionSnap(subj, stewRecipe("skillet", 1), "skillet")

	// Baseline: both refill cues render for the low-on-input producer.
	if buildRestocking(snap, "john", subj) == nil {
		t.Fatal("baseline: buildRestocking should render for a low bought input")
	}
	if buildProductionInputs(snap, "john", subj) == nil {
		t.Fatal("baseline: buildProductionInputs should render for a low bought input")
	}

	// Degrade john's own business — both refill cues must go silent.
	snap.StallWearDegradeThreshold = 600
	snap.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{
		"johns_shop": {ID: "johns_shop", OwnerActorID: "john", Tags: []string{sim.TagBusiness}, Wear: 650},
	}
	if v := buildRestocking(snap, "john", subj); v != nil {
		t.Errorf("degraded business: buildRestocking must be suppressed, got %+v", v)
	}
	if v := buildProductionInputs(snap, "john", subj); v != nil {
		t.Errorf("degraded business: buildProductionInputs must be suppressed, got %+v", v)
	}
}

// An input consumed 1-per-batch surfaces at the last unit with the per-batch
// runway (1 × 30-stew batch = 30 stews). This catalog carries NO durability, so
// the skillet here exercises the plain consumed-input path — the durable-tool
// wear runway (LLM-330) is pinned separately below.
func TestBuildProductionInputs_SkilletLowSurfacesRunway(t *testing.T) {
	subj := makesStewBuying("skillet", 2, 1) // cap 2 @ 25% = 0.5 → fires at the last unit
	snap := productionSnap(subj, stewRecipe("skillet", 1), "skillet")

	v := buildProductionInputs(snap, "john", subj)
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
	snap := productionSnap(subj, stewRecipe("carrots", 30), "carrots")

	v := buildProductionInputs(snap, "john", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("expected one item, got %+v", v)
	}
	if got := v.Items[0].RunwayUnits; got != 7 {
		t.Errorf("carrots runway = %d, want 7 (7 × 30 / 30)", got)
	}
}

// ---- Durable tools (LLM-330) ---------------------------------------------

// durableToolSnap is productionSnap with the skillet carrying durability 20 —
// the tool path: runway = wear-based uses × outputQty, not on-hand ÷ per-batch.
func durableToolSnap(subj *sim.ActorSnapshot) *sim.Snapshot {
	snap := productionSnap(subj, stewRecipe("skillet", 1), "skillet")
	snap.ItemKinds["skillet"] = &sim.ItemKindDef{Name: "skillet", DisplayLabel: "skillet", DurabilityUses: 20}
	return snap
}

// A durable tool's runway is wear-based: the last skillet with 5 uses left on
// it backs 5 executions × 30 stew, and the line phrases wear, not stock burn.
func TestBuildProductionInputs_DurableToolWearRunway(t *testing.T) {
	subj := makesStewBuying("skillet", 2, 1)
	subj.ToolWear = map[sim.ItemKind]int{"skillet": 5}
	snap := durableToolSnap(subj)

	v := buildProductionInputs(snap, "john", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("expected one production-input item, got %+v", v)
	}
	it := v.Items[0]
	if !it.Tool || it.CurrentQty != 1 || it.RunwayUnits != 150 {
		t.Fatalf("got %+v, want tool, 1 on hand, runway 150 (5 uses × 30)", it)
	}

	var b strings.Builder
	renderProductionInputs(&b, v)
	out := b.String()
	for _, want := range []string{"## Keeping up production", "The skillet you make stew with is wearing down", "1 on hand", "about 150 more before you need another"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q\n--- got ---\n%s", want, out)
		}
	}
	if strings.Contains(out, "You use skillet") {
		t.Errorf("tool line must not use the consumed-input phrasing:\n%s", out)
	}
}

// An unworn tool reads fresh: full durability per unit (1 skillet × 20 uses ×
// 30 stew = 600).
func TestBuildProductionInputs_DurableToolFreshRunway(t *testing.T) {
	subj := makesStewBuying("skillet", 2, 1) // no ToolWear entry
	snap := durableToolSnap(subj)

	v := buildProductionInputs(snap, "john", subj)
	if v == nil || len(v.Items) != 1 {
		t.Fatalf("expected one production-input item, got %+v", v)
	}
	if it := v.Items[0]; !it.Tool || it.RunwayUnits != 600 {
		t.Fatalf("got %+v, want tool runway 600 (20 fresh uses × 30)", it)
	}
}

// LLM-279: a produce input reorders on batch coverage, not a cap fraction. Carrots
// consumed 5 per batch → floor 10 (2 × batch). With a small resale-style cap of 12
// (fraction threshold 3), the cap fraction alone would only fire below 3 — leaving
// the producer stranded at 4-9 units, unable to cover a 5-carrot batch yet above
// the fraction. The floor surfaces the "## Keeping up production" runway across the
// whole 0..9 band and stays silent at two full batches on hand.
func TestBuildProductionInputs_BatchFloorReorders(t *testing.T) {
	cases := []struct {
		onHand int
		low    bool
		note   string
	}{
		{10, false, "two full batches on hand — not low"},
		{9, true, "below two batches — floor fires where the cap fraction (3) would not"},
		{5, true, "one batch left — reorder before the stall (mode 1)"},
		{4, true, "can't cover a 5-carrot batch — deadlock band (mode 2)"},
		{0, true, "empty"},
	}
	for _, c := range cases {
		subj := makesStewBuying("carrots", 12, c.onHand)
		snap := productionSnap(subj, stewRecipe("carrots", 5), "carrots")
		v := buildProductionInputs(snap, "john", subj)
		got := v != nil && len(v.Items) == 1
		if got != c.low {
			t.Errorf("onHand=%d (%s): surfaced=%v, want %v (floor 10 = 2×batch 5)", c.onHand, c.note, got, c.low)
		}
	}
}

// At full stock the input isn't low, so the section is omitted.
func TestBuildProductionInputs_FullStockNil(t *testing.T) {
	subj := makesStewBuying("skillet", 2, 2) // full → 2 <= 1 false
	snap := productionSnap(subj, stewRecipe("skillet", 1), "skillet")
	if v := buildProductionInputs(snap, "john", subj); v != nil {
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
	snap := productionSnap(subj, stewRecipe("skillet", 1), "skillet")
	if v := buildProductionInputs(snap, "john", subj); v != nil {
		t.Errorf("a self-produced input must not surface as a buy concern, got %+v", v)
	}
}

// A bought item that no produced recipe consumes is not a production input, so it
// stays in Restocking's lane and doesn't surface here. The recipe's actual input
// (carrots), by contrast, now surfaces WITHOUT a hand-authored buy entry — the
// derived-demand path (LLM-260) gives the unsourced input its buy cap.
func TestBuildProductionInputs_BoughtButNotConsumedIgnored(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Coins:     50, // means to pay — see makesStewBuying
		Inventory: map[sim.ItemKind]int{"skillet": 0},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "stew", Source: sim.RestockSourceProduce, Max: 60},
			{Item: "skillet", Source: sim.RestockSourceBuy, Max: 2},
		}},
	}
	// stew's recipe consumes carrots, not skillet — so the low skillet is irrelevant
	// here, while the unsourced carrots input derives demand and surfaces.
	snap := productionSnap(subj, stewRecipe("carrots", 30), "carrots", "skillet")
	v := buildProductionInputs(snap, "john", subj)
	if v == nil || len(v.Items) != 1 || v.Items[0].InputKind != "carrots" {
		t.Fatalf("want exactly the derived carrots input to surface, got %+v", v)
	}
	for _, it := range v.Items {
		if it.InputKind == "skillet" {
			t.Errorf("a bought item no recipe consumes must not surface, got %+v", v)
		}
	}
}

// pct 0 disables the feature (operator off-switch), same as Restocking.
func TestBuildProductionInputs_DisabledNil(t *testing.T) {
	subj := makesStewBuying("skillet", 2, 1)
	snap := productionSnap(subj, stewRecipe("skillet", 1), "skillet")
	snap.RestockReorderPct = 0
	if v := buildProductionInputs(snap, "john", subj); v != nil {
		t.Errorf("pct 0 should disable the section, got %+v", v)
	}
}

// The cue carries no supplier, structure_id, or pay_with_item — that's
// Restocking's job. The LLM-64 split: this section motivates, Restocking acts.
func TestRenderProductionInputs_NoBuyMechanics(t *testing.T) {
	subj := makesStewBuying("skillet", 2, 1)
	snap := productionSnap(subj, stewRecipe("skillet", 1), "skillet")
	var b strings.Builder
	renderProductionInputs(&b, buildProductionInputs(snap, "john", subj))
	out := b.String()
	for _, forbidden := range []string{"destination", "pay_with_item", "buy from", "move_to"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("producer cue must not carry buy mechanics, found %q in:\n%s", forbidden, out)
		}
	}
}

// A corrupt negative on-hand reads as "out" (0), not a negative count/runway.
func TestBuildProductionInputs_NegativeInventoryClampedToZero(t *testing.T) {
	subj := makesStewBuying("skillet", 2, -3)
	snap := productionSnap(subj, stewRecipe("skillet", 1), "skillet")
	v := buildProductionInputs(snap, "john", subj)
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
		Coins:     50, // means to pay — see makesStewBuying
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
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"elizabeth": subj},
		ItemKinds:         catalog,
		Recipes:           milkRecipeBoostedBySage(),
		RestockReorderPct: 25,
	}
	// A sage supplier keeps the LLM-260 buy-path gate open; the no-vendor
	// silence case is pinned separately.
	addSnapSupplier(snap, "sage")
	return snap
}

// A low bought booster surfaces with the forgone bonus (no runway — production
// continues without it).
func TestBuildProductionInputs_LowBoosterSurfacesBonus(t *testing.T) {
	subj := makesMilkBuyingSage(3, 0)
	snap := boosterSnap(subj)

	v := buildProductionInputs(snap, "elizabeth", subj)
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
	if v := buildProductionInputs(snap, "elizabeth", subj); v != nil {
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
	if v := buildProductionInputs(snap, "elizabeth", subj); v != nil {
		t.Errorf("a self-foraged booster must not surface as a buy concern, got %+v", v)
	}
}

// The booster line carries no buy mechanics either (LLM-64 split).
func TestRenderProductionInputs_BoosterNoBuyMechanics(t *testing.T) {
	subj := makesMilkBuyingSage(3, 0)
	snap := boosterSnap(subj)
	var b strings.Builder
	renderProductionInputs(&b, buildProductionInputs(snap, "elizabeth", subj))
	out := b.String()
	for _, forbidden := range []string{"destination", "pay_with_item", "buy from", "move_to"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("booster cue must not carry buy mechanics, found %q in:\n%s", forbidden, out)
		}
	}
}

// An input with NO vendor anywhere renders in NEITHER section (LLM-260): the
// runway motivate-line is gated on the same actionable buy path as Restocking,
// so an unobtainable input (the live Hannah Boggs water case) stays silent
// instead of inviting the model to improvise on a dead end.
func TestBuildProductionInputs_NoVendorInputSilent(t *testing.T) {
	subj := makesStewBuying("carrots", 30, 0)
	snap := productionSnap(subj, stewRecipe("carrots", 30)) // no supplier wired
	if v := buildProductionInputs(snap, "john", subj); v != nil {
		t.Errorf("an input with no buy path must not surface a runway line, got %+v", v)
	}
}

// Same silence for a booster with no vendor: the forgone-bonus motivation is a
// buy nudge, and a buy nudge with nowhere to buy is the same dead end.
func TestBuildProductionInputs_NoVendorBoosterSilent(t *testing.T) {
	subj := makesMilkBuyingSage(3, 0)
	catalog := productionCatalog()
	catalog["milk"] = &sim.ItemKindDef{Name: "milk", DisplayLabel: "milk", Category: sim.ItemCategoryDrink}
	catalog["sage"] = &sim.ItemKindDef{Name: "sage", DisplayLabel: "sage"}
	snap := &sim.Snapshot{
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"elizabeth": subj},
		ItemKinds:         catalog,
		Recipes:           milkRecipeBoostedBySage(),
		RestockReorderPct: 25,
	}
	if v := buildProductionInputs(snap, "elizabeth", subj); v != nil {
		t.Errorf("a booster with no buy path must not surface, got %+v", v)
	}
}
