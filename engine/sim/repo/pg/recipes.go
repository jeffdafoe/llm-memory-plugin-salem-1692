package pg

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// RecipesRepo loads the item_recipe catalog. Reference state — read-only,
// no checkpoint path (admin recipe edits write directly to item_recipe
// through the editor port; the world rebuilds the map via LoadAll on
// SIGHUP).
type RecipesRepo struct {
	pool Pool
}

// NewRecipesRepo constructs a RecipesRepo against the given pool. Normal
// wiring path is pg.NewRepository.
func NewRecipesRepo(pool Pool) *RecipesRepo {
	return &RecipesRepo{pool: pool}
}

// loadAllRecipesSQL pulls every recipe in one query. inputs is a JSONB
// array of {item, qty} objects; wholesale_price / retail_price are
// nullable smallints.
const loadAllRecipesSQL = `
SELECT output_item, output_qty, rate_qty, rate_per_hours, inputs,
       boost_inputs, boost_state, speed_inputs, wholesale_price, retail_price
  FROM item_recipe`

// LoadAll returns every recipe keyed by output item. Port of v1's
// loadAllRecipes (engine/recipes.go). NULL wholesale/retail price → 0.
//
// Runs against the pool directly (no Tx) — read-only restart path, same
// posture as the other repos' LoadAll.
func (r *RecipesRepo) LoadAll(ctx context.Context) (map[sim.ItemKind]*sim.ItemRecipe, error) {
	rows, err := r.pool.Query(ctx, loadAllRecipesSQL)
	if err != nil {
		return nil, fmt.Errorf("pg recipes LoadAll: query: %w", err)
	}
	defer rows.Close()

	out := make(map[sim.ItemKind]*sim.ItemRecipe)
	for rows.Next() {
		var (
			outputItem        string
			inputsJSON        []byte
			boostInputsJSON   []byte
			boostStateJSON    []byte
			speedInputsJSON   []byte
			wholesale, retail sql.NullInt32
		)
		rec := &sim.ItemRecipe{}
		if err := rows.Scan(&outputItem, &rec.OutputQty, &rec.RateQty,
			&rec.RatePerHours, &inputsJSON, &boostInputsJSON, &boostStateJSON, &speedInputsJSON, &wholesale, &retail); err != nil {
			return nil, fmt.Errorf("pg recipes LoadAll: scan: %w", err)
		}
		rec.OutputItem = sim.ItemKind(outputItem)
		if wholesale.Valid {
			rec.WholesalePrice = int(wholesale.Int32)
		}
		if retail.Valid {
			rec.RetailPrice = int(retail.Int32)
		}
		if err := json.Unmarshal(inputsJSON, &rec.Inputs); err != nil {
			return nil, fmt.Errorf("pg recipes LoadAll: parse inputs for %q: %w", outputItem, err)
		}
		if err := json.Unmarshal(boostInputsJSON, &rec.BoostInputs); err != nil {
			return nil, fmt.Errorf("pg recipes LoadAll: parse boost_inputs for %q: %w", outputItem, err)
		}
		if err := json.Unmarshal(boostStateJSON, &rec.BoostState); err != nil {
			return nil, fmt.Errorf("pg recipes LoadAll: parse boost_state for %q: %w", outputItem, err)
		}
		if err := json.Unmarshal(speedInputsJSON, &rec.SpeedInputs); err != nil {
			return nil, fmt.Errorf("pg recipes LoadAll: parse speed_inputs for %q: %w", outputItem, err)
		}
		if err := validateRecipeInputs(rec.OutputItem, rec.Inputs); err != nil {
			return nil, fmt.Errorf("pg recipes LoadAll: %w", err)
		}
		if err := validateRecipeBoostInputs(rec.OutputItem, rec.Inputs, rec.BoostInputs); err != nil {
			return nil, fmt.Errorf("pg recipes LoadAll: %w", err)
		}
		if err := validateRecipeBoostState(rec.OutputItem, rec.BoostState); err != nil {
			return nil, fmt.Errorf("pg recipes LoadAll: %w", err)
		}
		if err := validateRecipeSpeedInputs(rec.OutputItem, rec.Inputs, rec.BoostInputs, rec.SpeedInputs); err != nil {
			return nil, fmt.Errorf("pg recipes LoadAll: %w", err)
		}
		// Loud duplicate detection (consistent with the other loaded-map
		// repos). output_item is the item_recipe PK so this is unreachable
		// in valid data — guards against schema drift rather than letting a
		// later row silently win.
		if _, exists := out[rec.OutputItem]; exists {
			return nil, fmt.Errorf("pg recipes LoadAll: duplicate output_item %q", rec.OutputItem)
		}
		out[rec.OutputItem] = rec
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg recipes LoadAll: iter: %w", err)
	}
	return out, nil
}

// upsertRecipeSQL writes one item_recipe row (the operator recipe-edit path,
// LLM-97). PK is output_item; ON CONFLICT updates every field. inputs is bound
// as text + cast ::jsonb (same posture as the actor_attribute params write).
const upsertRecipeSQL = `
INSERT INTO item_recipe (
    output_item, output_qty, rate_qty, rate_per_hours, inputs, boost_inputs, boost_state, speed_inputs, wholesale_price, retail_price
) VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7::jsonb, $8::jsonb, $9, $10)
ON CONFLICT (output_item) DO UPDATE SET
    output_qty      = EXCLUDED.output_qty,
    rate_qty        = EXCLUDED.rate_qty,
    rate_per_hours  = EXCLUDED.rate_per_hours,
    inputs          = EXCLUDED.inputs,
    boost_inputs    = EXCLUDED.boost_inputs,
    boost_state     = EXCLUDED.boost_state,
    speed_inputs    = EXCLUDED.speed_inputs,
    wholesale_price = EXCLUDED.wholesale_price,
    retail_price    = EXCLUDED.retail_price`

// UpsertRecipe inserts or updates one recipe in item_recipe — the durable half
// of the umbilical recipe-edit route (LLM-97). The catalog has no checkpoint
// path (reference data), so this is a direct, standalone write; the in-memory
// World.Recipes update is the caller's separate step. output_item must already
// exist in item_kind (FK enforced by the DB); inputs are validated Go-side
// (validateRecipeInputs — there's no DB CHECK inside the JSONB array). A nil
// inputs slice persists as '[]'.
func (r *RecipesRepo) UpsertRecipe(ctx context.Context, rec sim.ItemRecipe) error {
	if rec.OutputItem == "" {
		return fmt.Errorf("pg recipes UpsertRecipe: empty output_item")
	}
	if err := validateRecipeInputs(rec.OutputItem, rec.Inputs); err != nil {
		return fmt.Errorf("pg recipes UpsertRecipe: %w", err)
	}
	if err := validateRecipeBoostInputs(rec.OutputItem, rec.Inputs, rec.BoostInputs); err != nil {
		return fmt.Errorf("pg recipes UpsertRecipe: %w", err)
	}
	if err := validateRecipeBoostState(rec.OutputItem, rec.BoostState); err != nil {
		return fmt.Errorf("pg recipes UpsertRecipe: %w", err)
	}
	if err := validateRecipeSpeedInputs(rec.OutputItem, rec.Inputs, rec.BoostInputs, rec.SpeedInputs); err != nil {
		return fmt.Errorf("pg recipes UpsertRecipe: %w", err)
	}
	inputs := rec.Inputs
	if inputs == nil {
		inputs = []sim.RecipeInput{}
	}
	inputsJSON, err := json.Marshal(inputs)
	if err != nil {
		return fmt.Errorf("pg recipes UpsertRecipe: marshal inputs: %w", err)
	}
	boostInputs := rec.BoostInputs
	if boostInputs == nil {
		boostInputs = []sim.BoostInput{}
	}
	boostInputsJSON, err := json.Marshal(boostInputs)
	if err != nil {
		return fmt.Errorf("pg recipes UpsertRecipe: marshal boost_inputs: %w", err)
	}
	boostState := rec.BoostState
	if boostState == nil {
		boostState = []sim.BoostState{}
	}
	boostStateJSON, err := json.Marshal(boostState)
	if err != nil {
		return fmt.Errorf("pg recipes UpsertRecipe: marshal boost_state: %w", err)
	}
	speedInputs := rec.SpeedInputs
	if speedInputs == nil {
		speedInputs = []sim.SpeedInput{}
	}
	speedInputsJSON, err := json.Marshal(speedInputs)
	if err != nil {
		return fmt.Errorf("pg recipes UpsertRecipe: marshal speed_inputs: %w", err)
	}
	if _, err := r.pool.Exec(ctx, upsertRecipeSQL,
		string(rec.OutputItem),
		rec.OutputQty,
		rec.RateQty,
		rec.RatePerHours,
		string(inputsJSON),
		string(boostInputsJSON),
		string(boostStateJSON),
		string(speedInputsJSON),
		rec.WholesalePrice,
		rec.RetailPrice,
	); err != nil {
		return fmt.Errorf("pg recipes UpsertRecipe: exec: %w", err)
	}
	return nil
}

// validateRecipeInputs enforces whole-positive-qty, non-empty-item
// inputs — belt-and-suspenders against a hand-edited JSONB row sneaking
// in a 0/fractional qty or empty item before the engine touches it.
// Port of v1's validateRecipeInputs (engine/recipes.go).
func validateRecipeInputs(output sim.ItemKind, inputs []sim.RecipeInput) error {
	for i, in := range inputs {
		if in.Item == "" {
			return fmt.Errorf("recipe %q input[%d] item is empty", output, i)
		}
		if in.Qty <= 0 {
			return fmt.Errorf("recipe %q input[%d] qty must be positive (got %d)", output, i, in.Qty)
		}
	}
	return nil
}

// validateRecipeBoostInputs is the boost_inputs mirror of validateRecipeInputs
// (LLM-248): non-empty item, positive qty AND bonus_qty, plus the overlap guard
// — a booster may not also be a required input (the produce tick would
// double-consume it with ambiguous semantics). Same belt-and-suspenders posture
// against hand-edited JSONB.
func validateRecipeBoostInputs(output sim.ItemKind, inputs []sim.RecipeInput, boosts []sim.BoostInput) error {
	required := make(map[sim.ItemKind]bool, len(inputs))
	for _, in := range inputs {
		required[in.Item] = true
	}
	for i, bi := range boosts {
		if bi.Item == "" {
			return fmt.Errorf("recipe %q boost_input[%d] item is empty", output, i)
		}
		if bi.Qty <= 0 {
			return fmt.Errorf("recipe %q boost_input[%d] qty must be positive (got %d)", output, i, bi.Qty)
		}
		if bi.BonusQty <= 0 {
			return fmt.Errorf("recipe %q boost_input[%d] bonus_qty must be positive (got %d)", output, i, bi.BonusQty)
		}
		if required[bi.Item] {
			return fmt.Errorf("recipe %q boost_input[%d] %q is already a required input", output, i, bi.Item)
		}
	}
	return nil
}

// validateRecipeSpeedInputs is the speed_inputs mirror of validateRecipeBoostInputs
// (LLM-511): non-empty item, positive qty, a rate_pct in the speedup band
// (101..sim.MaxSpeedInputRatePct), no duplicate item, and the overlap guard
// against BOTH required inputs and boost inputs — one optional role per item (a
// required overlap double-consumes at start; a boost overlap charges the same
// item at start and at landing). Same belt-and-suspenders posture against
// hand-edited JSONB: the DB enforces only the array shape.
func validateRecipeSpeedInputs(output sim.ItemKind, inputs []sim.RecipeInput, boosts []sim.BoostInput, speeds []sim.SpeedInput) error {
	required := make(map[sim.ItemKind]bool, len(inputs))
	for _, in := range inputs {
		required[in.Item] = true
	}
	boosted := make(map[sim.ItemKind]bool, len(boosts))
	for _, bi := range boosts {
		boosted[bi.Item] = true
	}
	seen := make(map[sim.ItemKind]bool, len(speeds))
	for i, si := range speeds {
		if si.Item == "" {
			return fmt.Errorf("recipe %q speed_input[%d] item is empty", output, i)
		}
		if si.Qty <= 0 {
			return fmt.Errorf("recipe %q speed_input[%d] qty must be positive (got %d)", output, i, si.Qty)
		}
		if si.RatePct <= 100 || si.RatePct > sim.MaxSpeedInputRatePct {
			return fmt.Errorf("recipe %q speed_input[%d] rate_pct must be between 101 and %d (got %d)", output, i, sim.MaxSpeedInputRatePct, si.RatePct)
		}
		if required[si.Item] {
			return fmt.Errorf("recipe %q speed_input[%d] %q is already a required input", output, i, si.Item)
		}
		if boosted[si.Item] {
			return fmt.Errorf("recipe %q speed_input[%d] %q is already a boost input", output, i, si.Item)
		}
		if seen[si.Item] {
			return fmt.Errorf("recipe %q speed_input[%d] %q listed more than once", output, i, si.Item)
		}
		seen[si.Item] = true
	}
	return nil
}

// validateRecipeBoostState is the boost_state mirror of validateRecipeBoostInputs
// (LLM-474): a recognised state name, a positive bonus_qty, and no duplicate
// state (two `hearth_lit` rows would pay one fire twice). Same
// belt-and-suspenders posture against hand-edited JSONB — the DB enforces only
// the array shape, so an unknown state reaching the catalog would otherwise sit
// there validating and never firing.
func validateRecipeBoostState(output sim.ItemKind, states []sim.BoostState) error {
	seen := make(map[sim.RecipeBoostState]bool, len(states))
	for i, bs := range states {
		if !sim.ValidRecipeBoostState(bs.State) {
			return fmt.Errorf("recipe %q boost_state[%d] unknown state %q", output, i, bs.State)
		}
		if bs.BonusQty <= 0 {
			return fmt.Errorf("recipe %q boost_state[%d] bonus_qty must be positive (got %d)", output, i, bs.BonusQty)
		}
		if seen[bs.State] {
			return fmt.Errorf("recipe %q boost_state[%d] %q listed more than once", output, i, bs.State)
		}
		seen[bs.State] = true
	}
	return nil
}
