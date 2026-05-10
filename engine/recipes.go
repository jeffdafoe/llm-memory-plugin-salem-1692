package main

// Recipes + restock policy data model (ZBBS-HOME-241).
//
// item_recipe is the global table that defines how each item is
// produced. Two flavors share one schema:
//
//   * Terminator — inputs is empty. The item appears at rate from
//     nothing. Used for chain endpoints (the farmer's grain, the
//     well's water) where we don't model an upstream.
//
//   * Transformation — inputs is non-empty. Each recipe execution
//     consumes one of each input qty and produces output_qty units
//     of the output. Stew is the v1 example: 1 batch every 2h yields
//     10 bowls, consuming 1 each of meat/water/milk/carrots
//     (effective 0.1 of each per bowl, expressed without fractional
//     schema).
//
// Restock policies live on actor_attribute.params (a per-assignment
// JSONB). Each entry declares an item, a source mode (produce or
// buy), and either a max (for produce — inventory cap) or a target
// (for buy — the threshold below which the buy dispatcher fires).
//
// This file owns the shape definitions and DB lookups. The actual
// produce tick lives in produce_tick.go; the buy dispatcher in
// buy_dispatcher.go.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ItemRecipe is the in-memory shape of an item_recipe row.
type ItemRecipe struct {
	OutputItem     string
	OutputQty      int
	RateQty        int
	RatePerHours   int
	Inputs         []RecipeInput
	WholesalePrice int // charged by a producer to a merchant buying upstream
	RetailPrice    int // charged by a merchant to a customer downstream
}

// RecipeInput is one (item, qty) pair from item_recipe.inputs.
type RecipeInput struct {
	Item string `json:"item"`
	Qty  int    `json:"qty"`
}

// RestockSource enumerates the supply modes a restock entry can use.
const (
	RestockSourceProduce = "produce"
	RestockSourceBuy     = "buy"
)

// RestockEntry is one item the role manages — either by producing
// it themselves (`produce`) or by buying it from another actor
// (`buy`). Max is the unified personal carry cap (ZBBS-HOME-249);
// Target is accepted as a legacy alias for buy entries written
// before the unification.
type RestockEntry struct {
	Item   string `json:"item"`
	Source string `json:"source"`
	Max    int    `json:"max,omitempty"`
	Target int    `json:"target,omitempty"`
}

// Cap returns the unified personal-carry cap for this entry: the
// maximum quantity the actor should hold of this item. Prefers Max;
// falls back to Target for legacy buy entries. Returns 0 when
// neither is set — callers treat that as "no cap configured".
func (e RestockEntry) Cap() int {
	if e.Max > 0 {
		return e.Max
	}
	return e.Target
}

// RestockPolicy wraps the restock array stored under
// actor_attribute.params.restock. A given actor may have multiple
// attributes (tavernkeeper + worker, etc.); the dispatcher unions
// the entries from all of them. First-listed wins on ordering ties.
type RestockPolicy struct {
	Restock []RestockEntry `json:"restock"`
}

// loadItemRecipe fetches a single recipe by output item. Returns
// (nil, nil) when no recipe exists — caller decides whether that's
// an error or a skip.
func (app *App) loadItemRecipe(ctx context.Context, item string) (*ItemRecipe, error) {
	r := &ItemRecipe{}
	var inputsJSON []byte
	var wholesale, retail sql.NullInt32
	err := app.DB.QueryRow(ctx,
		`SELECT output_item, output_qty, rate_qty, rate_per_hours, inputs,
		        wholesale_price, retail_price
		   FROM item_recipe WHERE output_item = $1`,
		item,
	).Scan(&r.OutputItem, &r.OutputQty, &r.RateQty, &r.RatePerHours, &inputsJSON,
		&wholesale, &retail)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load recipe %s: %w", item, err)
	}
	if wholesale.Valid {
		r.WholesalePrice = int(wholesale.Int32)
	}
	if retail.Valid {
		r.RetailPrice = int(retail.Int32)
	}
	if err := json.Unmarshal(inputsJSON, &r.Inputs); err != nil {
		return nil, fmt.Errorf("parse recipe %s inputs: %w", item, err)
	}
	if err := validateRecipeInputs(r.Inputs); err != nil {
		return nil, fmt.Errorf("recipe %s: %w", item, err)
	}
	return r, nil
}

// loadAllRecipes returns every recipe keyed by output item. Used by
// produce_tick.go to avoid round-tripping per actor × per item.
func (app *App) loadAllRecipes(ctx context.Context) (map[string]*ItemRecipe, error) {
	rows, err := app.DB.Query(ctx,
		`SELECT output_item, output_qty, rate_qty, rate_per_hours, inputs,
		        wholesale_price, retail_price
		   FROM item_recipe`,
	)
	if err != nil {
		return nil, fmt.Errorf("load recipes: %w", err)
	}
	defer rows.Close()
	out := make(map[string]*ItemRecipe)
	for rows.Next() {
		r := &ItemRecipe{}
		var inputsJSON []byte
		var wholesale, retail sql.NullInt32
		if err := rows.Scan(&r.OutputItem, &r.OutputQty, &r.RateQty, &r.RatePerHours,
			&inputsJSON, &wholesale, &retail); err != nil {
			return nil, fmt.Errorf("scan recipe: %w", err)
		}
		if wholesale.Valid {
			r.WholesalePrice = int(wholesale.Int32)
		}
		if retail.Valid {
			r.RetailPrice = int(retail.Int32)
		}
		if err := json.Unmarshal(inputsJSON, &r.Inputs); err != nil {
			return nil, fmt.Errorf("parse recipe %s inputs: %w", r.OutputItem, err)
		}
		if err := validateRecipeInputs(r.Inputs); err != nil {
			return nil, fmt.Errorf("recipe %s: %w", r.OutputItem, err)
		}
		out[r.OutputItem] = r
	}
	return out, rows.Err()
}

// validateRecipeInputs enforces whole-positive-qty inputs. Belt and
// suspenders given the integer schema everywhere downstream — but
// catches the case where a hand-edited JSONB row sneaks in a 0 or a
// fractional qty before the engine touches it.
func validateRecipeInputs(inputs []RecipeInput) error {
	for i, in := range inputs {
		if in.Item == "" {
			return fmt.Errorf("input[%d] item is empty", i)
		}
		if in.Qty <= 0 {
			return fmt.Errorf("input[%d] qty must be positive (got %d)", i, in.Qty)
		}
	}
	return nil
}

// loadActorRestockPolicy unions the restock arrays from every
// attribute_definition.params row attached to the actor. Returns an
// empty policy (not nil) when the actor has no restock entries —
// callers can iterate the result safely without nil-checking.
//
// Each actor_attribute.params is a JSONB blob keyed by convention.
// We pull the `restock` array if present and concatenate. If the
// same item appears in multiple role rows, the first-encountered
// wins (later entries are dropped) — this matches the first-listed
// priority rule documented in the design note.
func (app *App) loadActorRestockPolicy(ctx context.Context, actorID string) (*RestockPolicy, error) {
	rows, err := app.DB.Query(ctx,
		`SELECT params FROM actor_attribute WHERE actor_id = $1::uuid ORDER BY slug`,
		actorID,
	)
	if err != nil {
		return nil, fmt.Errorf("load actor_attribute params for %s: %w", actorID, err)
	}
	defer rows.Close()

	policy := &RestockPolicy{}
	seen := make(map[string]bool)
	for rows.Next() {
		var paramsJSON []byte
		if err := rows.Scan(&paramsJSON); err != nil {
			return nil, fmt.Errorf("scan params for %s: %w", actorID, err)
		}
		if len(paramsJSON) == 0 {
			continue
		}
		var perAttribute RestockPolicy
		if err := json.Unmarshal(paramsJSON, &perAttribute); err != nil {
			// Skip unparseable params rather than failing the whole
			// load — other roles on this actor may still be valid.
			continue
		}
		for _, entry := range perAttribute.Restock {
			if seen[entry.Item] {
				continue
			}
			if entry.Source != RestockSourceProduce && entry.Source != RestockSourceBuy {
				// Unknown source mode — skip silently rather than
				// fail-stop. Future versions may add modes.
				continue
			}
			seen[entry.Item] = true
			policy.Restock = append(policy.Restock, entry)
		}
	}
	return policy, rows.Err()
}

// listActorsWithRestockEntries returns every actor that has at least
// one matching restock entry across all their attribute_definition
// rows. Used as the candidate set for produce_tick and buy_dispatcher
// scans — both want "every actor who declared they manage stock for
// any item via the given source mode."
//
// The JSONB query uses jsonb_path_exists for a server-side filter:
// 'restock' must be an array and at least one entry's source must
// match. Avoids pulling every actor into Go just to filter most out.
func (app *App) listActorsWithRestockEntries(ctx context.Context, source string) ([]string, error) {
	rows, err := app.DB.Query(ctx,
		`SELECT DISTINCT actor_id::text
		   FROM actor_attribute
		  WHERE jsonb_path_exists(params,
		    '$.restock[*] ? (@.source == $src)',
		    jsonb_build_object('src', $1::text))`,
		source,
	)
	if err != nil {
		return nil, fmt.Errorf("list actors with restock %s: %w", source, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan actor id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
