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
       wholesale_price, retail_price
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
			wholesale, retail sql.NullInt32
		)
		rec := &sim.ItemRecipe{}
		if err := rows.Scan(&outputItem, &rec.OutputQty, &rec.RateQty,
			&rec.RatePerHours, &inputsJSON, &wholesale, &retail); err != nil {
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
		if err := validateRecipeInputs(rec.OutputItem, rec.Inputs); err != nil {
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
    output_item, output_qty, rate_qty, rate_per_hours, inputs, wholesale_price, retail_price
) VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)
ON CONFLICT (output_item) DO UPDATE SET
    output_qty      = EXCLUDED.output_qty,
    rate_qty        = EXCLUDED.rate_qty,
    rate_per_hours  = EXCLUDED.rate_per_hours,
    inputs          = EXCLUDED.inputs,
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
	inputs := rec.Inputs
	if inputs == nil {
		inputs = []sim.RecipeInput{}
	}
	inputsJSON, err := json.Marshal(inputs)
	if err != nil {
		return fmt.Errorf("pg recipes UpsertRecipe: marshal inputs: %w", err)
	}
	if _, err := r.pool.Exec(ctx, upsertRecipeSQL,
		string(rec.OutputItem),
		rec.OutputQty,
		rec.RateQty,
		rec.RatePerHours,
		string(inputsJSON),
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
