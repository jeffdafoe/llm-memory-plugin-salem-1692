package pg

import (
	"context"
	"fmt"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ItemKindsRepo loads the item_kind + item_satisfies catalog flattened
// into ItemKindDef aggregates. Reference state — read-only, no checkpoint
// path (admin edits write directly to the underlying tables; the world
// rebuilds the map wholesale via LoadAll on SIGHUP).
//
// Pricing is not catalog-static: item_kind has no price column. Prices
// are negotiated/quoted dynamically (NPC speak-price → scene_quote,
// enforced at pay) anchored on ItemRecipe wholesale/retail, with the
// pay_ledger-derived PriceBook as observed history.
type ItemKindsRepo struct {
	pool Pool
}

// NewItemKindsRepo constructs an ItemKindsRepo against the given pool.
// Normal wiring path is pg.NewRepository.
func NewItemKindsRepo(pool Pool) *ItemKindsRepo {
	return &ItemKindsRepo{pool: pool}
}

// loadAllItemKindsSQL reads the catalog definitions. capabilities and
// hours_per_unit are intentionally unselected — neither is modeled on
// sim.ItemKindDef.
const loadAllItemKindsSQL = `
SELECT name, display_label, category, sort_order, consume_dwell_narration
  FROM item_kind
 ORDER BY sort_order, name`

// loadAllItemSatisfiesSQL pulls every per-need effect in one query (no
// N+1). ORDER BY amount DESC, attribute matches v1 so the most
// magnitudinal effect is first — narration callers that take a single
// "primary" attribute rely on head-of-slice ordering.
const loadAllItemSatisfiesSQL = `
SELECT item_kind, attribute, amount,
       COALESCE(dwell_amount, 0),
       COALESCE(dwell_period_minutes, 0),
       COALESCE(dwell_total_ticks, 0)
  FROM item_satisfies
 ORDER BY item_kind, amount DESC, attribute`

// LoadAll builds the item-kind catalog: one pass over item_kind for the
// definitions, then one pass over item_satisfies attaching per-need
// effects to the matching def (indexed by name, no N+1). Port of v1's
// handleListItems catalog assembly (engine/inventory_api.go) +
// loadItemSatisfactions (engine/item_satisfies.go).
//
// Runs against the pool directly (no Tx) — read-only restart path, same
// posture as the other repos' LoadAll.
func (r *ItemKindsRepo) LoadAll(ctx context.Context) (map[sim.ItemKind]*sim.ItemKindDef, error) {
	defs, err := r.loadDefs(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.attachSatisfactions(ctx, defs); err != nil {
		return nil, err
	}
	return defs, nil
}

// loadDefs reads item_kind into ItemKindDef values keyed by name.
func (r *ItemKindsRepo) loadDefs(ctx context.Context) (map[sim.ItemKind]*sim.ItemKindDef, error) {
	rows, err := r.pool.Query(ctx, loadAllItemKindsSQL)
	if err != nil {
		return nil, fmt.Errorf("pg item_kinds LoadAll: item_kind query: %w", err)
	}
	defer rows.Close()

	defs := make(map[sim.ItemKind]*sim.ItemKindDef)
	for rows.Next() {
		var (
			name, displayLabel, category string
			sortOrder                    int
			narration                    *string
		)
		if err := rows.Scan(&name, &displayLabel, &category, &sortOrder, &narration); err != nil {
			return nil, fmt.Errorf("pg item_kinds LoadAll: item_kind scan: %w", err)
		}
		def := &sim.ItemKindDef{
			Name:         sim.ItemKind(name),
			DisplayLabel: displayLabel,
			Category:     sim.ItemCategory(category),
			SortOrder:    sortOrder,
		}
		if narration != nil {
			def.ConsumeDwellNarration = *narration
		}
		// Loud duplicate detection (consistent with the other loaded-map
		// repos). name is the item_kind PK so this is unreachable in valid
		// data — guards against schema drift rather than letting a later row
		// silently win.
		if _, exists := defs[def.Name]; exists {
			return nil, fmt.Errorf("pg item_kinds LoadAll: duplicate item_kind %q", def.Name)
		}
		defs[def.Name] = def
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg item_kinds LoadAll: item_kind iter: %w", err)
	}
	return defs, nil
}

// attachSatisfactions reads item_satisfies and appends each effect to its
// parent def's Satisfies slice.
func (r *ItemKindsRepo) attachSatisfactions(ctx context.Context, defs map[sim.ItemKind]*sim.ItemKindDef) error {
	rows, err := r.pool.Query(ctx, loadAllItemSatisfiesSQL)
	if err != nil {
		return fmt.Errorf("pg item_kinds LoadAll: item_satisfies query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			itemKind, attribute                   string
			amount, dwellAmt, dwellPer, dwellTick int
		)
		if err := rows.Scan(&itemKind, &attribute, &amount, &dwellAmt, &dwellPer, &dwellTick); err != nil {
			return fmt.Errorf("pg item_kinds LoadAll: item_satisfies scan: %w", err)
		}
		def, ok := defs[sim.ItemKind(itemKind)]
		if !ok {
			// Unreachable in valid data: item_satisfies.item_kind has an FK
			// to item_kind(name) ON DELETE CASCADE, so a satisfaction can't
			// outlive its parent. Guarded loudly anyway — reaching it means
			// the FK was dropped or bypassed (hard schema drift).
			return fmt.Errorf("pg item_kinds LoadAll: item_satisfies row references unknown item_kind %q", itemKind)
		}
		def.Satisfies = append(def.Satisfies, sim.ItemSatisfaction{
			Attribute:          sim.NeedKey(attribute),
			Immediate:          amount,
			DwellAmount:        dwellAmt,
			DwellPeriodMinutes: dwellPer,
			DwellTotalTicks:    dwellTick,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("pg item_kinds LoadAll: item_satisfies iter: %w", err)
	}
	return nil
}
