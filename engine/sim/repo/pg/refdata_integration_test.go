package pg

// Real-pg integration tests for the read-side reference-data repos
// (ZBBS-WORK-246): TerrainRepo, RecipesRepo, ItemKindsRepo. Run against an
// embedded Postgres with the full prod-baseline schema applied; skipped
// under `go test -short`.
//
// These are read-only LoadAll/Load ports — no SaveSnapshot, no gen-marker,
// no Tx. The substrate facts worth exercising against real pg: bytea
// round-trip + length validation (terrain), JSONB inputs unmarshal +
// nullable smallint prices (recipes), the item_kind/item_satisfies join +
// dwell-triple nullability + amount-DESC ordering (item kinds). pgxmock
// would only re-assert the SQL strings we wrote, so real pg is the higher-
// fidelity coverage here.

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// terrainBlob builds a full MapW*MapH grid filled with one terrain byte.
func terrainBlob(fill byte) []byte {
	b := make([]byte, sim.MapW*sim.MapH)
	for i := range b {
		b[i] = fill
	}
	return b
}

// --- Terrain --------------------------------------------------------------

// T1 happy path — a correctly-dimensioned blob round-trips, including the
// exact byte content at the ends (proves the bytea isn't truncated/padded).
func TestIntegration_Terrain_LoadHappyPath(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	blob := terrainBlob(sim.TerrainLightGrass)
	blob[0] = sim.TerrainCobblestone
	blob[len(blob)-1] = sim.TerrainDeepWater
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO village_terrain (id, width, height, data) VALUES (1, $1, $2, $3)`,
		sim.MapW, sim.MapH, blob); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := NewTerrainRepo(f.Pool).Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil {
		t.Fatal("terrain nil")
	}
	if len(got.Data) != sim.MapW*sim.MapH {
		t.Fatalf("len=%d want %d", len(got.Data), sim.MapW*sim.MapH)
	}
	if got.Data[0] != sim.TerrainCobblestone || got.Data[len(got.Data)-1] != sim.TerrainDeepWater {
		t.Errorf("sentinel bytes wrong: [0]=%d [last]=%d", got.Data[0], got.Data[len(got.Data)-1])
	}
}

// T2 absent row → (nil, nil) per the mem-contract / procedural-fallback
// posture.
func TestIntegration_Terrain_AbsentReturnsNil(t *testing.T) {
	f := newFixture(t)
	got, err := NewTerrainRepo(f.Pool).Load(t.Context())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil terrain when no row, got %+v", got)
	}
}

// T3 stored dimensions that don't match the fixed grid → loud error.
func TestIntegration_Terrain_DimMismatchErrors(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	small := make([]byte, 100*100)
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO village_terrain (id, width, height, data) VALUES (1, 100, 100, $1)`,
		small); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := NewTerrainRepo(f.Pool).Load(ctx)
	if err == nil || !strings.Contains(err.Error(), "dimensions") {
		t.Fatalf("expected dimensions error, got %v", err)
	}
}

// T4 dimensions match but the blob length is wrong → loud error (guards the
// silent-pathfinding-corruption case).
func TestIntegration_Terrain_LengthMismatchErrors(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	short := make([]byte, 100)
	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO village_terrain (id, width, height, data) VALUES (1, $1, $2, $3)`,
		sim.MapW, sim.MapH, short); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := NewTerrainRepo(f.Pool).Load(ctx)
	if err == nil || !strings.Contains(err.Error(), "blob length") {
		t.Fatalf("expected blob length error, got %v", err)
	}
}

// clearCatalog empties the reference-data catalog tables so a refdata test
// sees only the rows it inserts. The shared test template applies the prod
// baseline + every migration, which now seed catalog rows (ZBBS-HOME-465 adds
// porridge: one item_kind, one item_recipe, one item_satisfies). Without this
// reset the exact-count assertions below drift each time a new catalog seed
// migration lands. CASCADE covers anything else FK'd to item_kind (e.g.
// actor_inventory), which is empty in the fresh template.
func clearCatalog(t *testing.T, f *integrationFixture) {
	t.Helper()
	if _, err := f.Pool.Exec(t.Context(),
		`TRUNCATE item_satisfies, item_recipe, item_kind CASCADE`); err != nil {
		t.Fatalf("clear catalog: %v", err)
	}
}

// --- Recipes --------------------------------------------------------------

// R1 happy path — JSONB inputs unmarshal; NULL wholesale/retail map to 0;
// empty inputs array yields an empty slice.
func TestIntegration_Recipes_LoadAllHappyPath(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	clearCatalog(t, f)

	// output_item FKs item_kind(name) — seed the parents first.
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO item_kind (name, display_label, category)
		VALUES ('bread','Bread','food'), ('flour','Flour','material')`); err != nil {
		t.Fatalf("seed item_kind: %v", err)
	}
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO item_recipe (output_item, output_qty, rate_qty, rate_per_hours, inputs, wholesale_price, retail_price)
		VALUES
			('bread', 2, 1, 3, '[{"item":"flour","qty":2}]'::jsonb, 5, 9),
			('flour', 1, 4, 1, '[]'::jsonb, NULL, NULL)`); err != nil {
		t.Fatalf("seed item_recipe: %v", err)
	}

	got, err := NewRecipesRepo(f.Pool).LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d want 2", len(got))
	}

	bread := got["bread"]
	if bread == nil {
		t.Fatal("bread missing")
	}
	if bread.OutputItem != "bread" || bread.OutputQty != 2 || bread.RateQty != 1 ||
		bread.RatePerHours != 3 || bread.WholesalePrice != 5 || bread.RetailPrice != 9 {
		t.Errorf("bread fields: %+v", bread)
	}
	if len(bread.Inputs) != 1 || bread.Inputs[0].Item != "flour" || bread.Inputs[0].Qty != 2 {
		t.Errorf("bread inputs: %+v", bread.Inputs)
	}

	flour := got["flour"]
	if flour == nil {
		t.Fatal("flour missing")
	}
	if flour.WholesalePrice != 0 || flour.RetailPrice != 0 {
		t.Errorf("flour NULL prices should map to 0, got %d/%d", flour.WholesalePrice, flour.RetailPrice)
	}
	if len(flour.Inputs) != 0 {
		t.Errorf("flour inputs should be empty, got %+v", flour.Inputs)
	}
}

// R2 a hand-edited JSONB input with qty 0 (no DB CHECK guards inside the
// array) is rejected by the Go-side validator.
func TestIntegration_Recipes_InvalidInputQtyErrors(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	clearCatalog(t, f)

	if _, err := f.Pool.Exec(ctx,
		`INSERT INTO item_kind (name, display_label, category) VALUES ('bread','Bread','food')`); err != nil {
		t.Fatalf("seed item_kind: %v", err)
	}
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO item_recipe (output_item, output_qty, rate_qty, rate_per_hours, inputs)
		VALUES ('bread', 1, 1, 1, '[{"item":"flour","qty":0}]'::jsonb)`); err != nil {
		t.Fatalf("seed item_recipe: %v", err)
	}
	_, err := NewRecipesRepo(f.Pool).LoadAll(ctx)
	if err == nil || !strings.Contains(err.Error(), "qty must be positive") {
		t.Fatalf("expected qty error, got %v", err)
	}
}

// R3 UpsertRecipe inserts a new recipe and updates an existing one in place
// (LLM-97 — the operator recipe-edit durable write). Round-trips via LoadAll.
func TestIntegration_Recipes_UpsertRecipe(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	clearCatalog(t, f)

	// output_item FKs item_kind(name) — seed the parents first.
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO item_kind (name, display_label, category)
		VALUES ('cheese','Cheese','food'), ('milk','Milk','material')`); err != nil {
		t.Fatalf("seed item_kind: %v", err)
	}

	repo := NewRecipesRepo(f.Pool)

	// Insert a new recipe.
	if err := repo.UpsertRecipe(ctx, sim.ItemRecipe{
		OutputItem: "cheese", OutputQty: 1, RateQty: 1, RatePerHours: 2,
		Inputs: []sim.RecipeInput{{Item: "milk", Qty: 3}}, WholesalePrice: 4, RetailPrice: 7,
	}); err != nil {
		t.Fatalf("UpsertRecipe insert: %v", err)
	}
	got, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	c := got["cheese"]
	if c == nil || c.RatePerHours != 2 || c.RetailPrice != 7 ||
		len(c.Inputs) != 1 || c.Inputs[0].Item != "milk" || c.Inputs[0].Qty != 3 {
		t.Fatalf("after insert: %+v", c)
	}

	// Update in place (same output_item) — change rate, drop inputs, new prices.
	if err := repo.UpsertRecipe(ctx, sim.ItemRecipe{
		OutputItem: "cheese", OutputQty: 2, RateQty: 5, RatePerHours: 1,
		Inputs: nil, WholesalePrice: 6, RetailPrice: 11,
	}); err != nil {
		t.Fatalf("UpsertRecipe update: %v", err)
	}
	got, err = repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("recipe count = %d, want 1 (update, not a second insert)", len(got))
	}
	c = got["cheese"]
	if c.OutputQty != 2 || c.RateQty != 5 || c.RatePerHours != 1 ||
		c.WholesalePrice != 6 || c.RetailPrice != 11 || len(c.Inputs) != 0 {
		t.Fatalf("after update: %+v", c)
	}
}

// --- ItemKinds ------------------------------------------------------------

// I1 happy path — defs join their item_satisfies effects (amount-DESC
// order), dwell triple round-trips, NULL narration → "", and a material
// with no effects is non-consumable.
func TestIntegration_ItemKinds_LoadAllHappyPath(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	clearCatalog(t, f)

	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO item_kind (name, display_label, category, sort_order, consume_dwell_narration)
		VALUES
			('ale','Ale','drink', 2, NULL),
			('stew','Hearty Stew','food', 1, 'This stew looks really good.'),
			('iron','Iron Ingot','material', 5, NULL)`); err != nil {
		t.Fatalf("seed item_kind: %v", err)
	}
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO item_satisfies (item_kind, attribute, amount, dwell_amount, dwell_period_minutes, dwell_total_ticks)
		VALUES
			('ale','thirst', 6, NULL, NULL, NULL),
			('ale','hunger', 2, NULL, NULL, NULL),
			('stew','hunger', 10, 2, 15, 4)`); err != nil {
		t.Fatalf("seed item_satisfies: %v", err)
	}

	got, err := NewItemKindsRepo(f.Pool).LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len=%d want 3", len(got))
	}

	ale := got["ale"]
	if ale == nil {
		t.Fatal("ale missing")
	}
	if ale.DisplayLabel != "Ale" || ale.Category != sim.ItemCategoryDrink || ale.SortOrder != 2 {
		t.Errorf("ale fields: %+v", ale)
	}
	if ale.ConsumeDwellNarration != "" {
		t.Errorf("ale narration should be empty for NULL, got %q", ale.ConsumeDwellNarration)
	}
	if len(ale.Satisfies) != 2 {
		t.Fatalf("ale satisfies len=%d want 2", len(ale.Satisfies))
	}
	// amount DESC: thirst(6) before hunger(2).
	if ale.Satisfies[0].Attribute != "thirst" || ale.Satisfies[0].Immediate != 6 ||
		ale.Satisfies[1].Attribute != "hunger" || ale.Satisfies[1].Immediate != 2 {
		t.Errorf("ale satisfies order/values: %+v", ale.Satisfies)
	}
	if !ale.Consumable() {
		t.Error("ale should be consumable")
	}

	stew := got["stew"]
	if stew == nil {
		t.Fatal("stew missing")
	}
	if stew.ConsumeDwellNarration == "" {
		t.Error("stew narration should be populated")
	}
	if len(stew.Satisfies) != 1 {
		t.Fatalf("stew satisfies len=%d want 1", len(stew.Satisfies))
	}
	st := stew.Satisfies[0]
	if st.Attribute != "hunger" || st.Immediate != 10 || st.DwellAmount != 2 ||
		st.DwellPeriodMinutes != 15 || st.DwellTotalTicks != 4 {
		t.Errorf("stew dwell triple: %+v", st)
	}

	iron := got["iron"]
	if iron == nil {
		t.Fatal("iron missing")
	}
	if len(iron.Satisfies) != 0 {
		t.Errorf("iron should have no satisfactions, got %+v", iron.Satisfies)
	}
	if iron.Consumable() {
		t.Error("iron (material) should not be consumable")
	}
}

// I2 UpsertItemSatisfies inserts a new satiation row and updates an existing
// one's immediate amount in place (LLM-119 — the operator satiation-edit durable
// write), preserving the dwell triple on edit. Round-trips via LoadAll.
func TestIntegration_ItemKinds_UpsertItemSatisfies(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	clearCatalog(t, f)

	// item_satisfies.item_kind FKs item_kind(name) — seed the parents first.
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO item_kind (name, display_label, category)
		VALUES ('berry','Berry','food'), ('stew','Hearty Stew','food')`); err != nil {
		t.Fatalf("seed item_kind: %v", err)
	}
	// stew starts with a full dwell triple so the edit-preserves-dwell case is real.
	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO item_satisfies (item_kind, attribute, amount, dwell_amount, dwell_period_minutes, dwell_total_ticks)
		VALUES ('stew','hunger', 10, 2, 15, 4)`); err != nil {
		t.Fatalf("seed item_satisfies: %v", err)
	}

	repo := NewItemKindsRepo(f.Pool)

	// Insert a brand-new satiation row (berry has none yet).
	if err := repo.UpsertItemSatisfies(ctx, "berry", "hunger", 2); err != nil {
		t.Fatalf("UpsertItemSatisfies insert: %v", err)
	}
	got, err := repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if b := got["berry"]; b == nil || len(b.Satisfies) != 1 ||
		b.Satisfies[0].Attribute != "hunger" || b.Satisfies[0].Immediate != 2 {
		t.Fatalf("after berry insert: %+v", got["berry"])
	}

	// Update stew's immediate hunger amount — the dwell triple must survive (the
	// upsert touches only the amount column).
	if err := repo.UpsertItemSatisfies(ctx, "stew", "hunger", 12); err != nil {
		t.Fatalf("UpsertItemSatisfies update: %v", err)
	}
	got, err = repo.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	stew := got["stew"]
	if stew == nil || len(stew.Satisfies) != 1 {
		t.Fatalf("stew satisfies = %+v, want exactly one entry (update, not a second row)", stew)
	}
	st := stew.Satisfies[0]
	if st.Attribute != "hunger" || st.Immediate != 12 ||
		st.DwellAmount != 2 || st.DwellPeriodMinutes != 15 || st.DwellTotalTicks != 4 {
		t.Fatalf("after update: %+v, want immediate 12 with dwell triple intact", st)
	}
}
