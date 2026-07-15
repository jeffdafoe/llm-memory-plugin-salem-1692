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

// loadAllItemKindsSQL reads the catalog definitions. capabilities (TEXT[])
// is now modeled on sim.ItemKindDef (ZBBS-HOME-296 — service/lodging gating
// for the lodging fulfillment path); hours_per_unit remains intentionally
// unselected (not modeled in v2).
const loadAllItemKindsSQL = `
SELECT name, display_label, display_label_singular, display_label_plural,
       category, sort_order, capabilities, consume_dwell_narration,
       durability_uses, description
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
			name, displayLabel, category             string
			displayLabelSingular, displayLabelPlural *string // nullable columns
			sortOrder                                int
			capabilities                             []string
			narration                                *string
			durabilityUses                           int
			description                              *string // nullable column
		)
		if err := rows.Scan(&name, &displayLabel, &displayLabelSingular, &displayLabelPlural, &category, &sortOrder, &capabilities, &narration, &durabilityUses, &description); err != nil {
			return nil, fmt.Errorf("pg item_kinds LoadAll: item_kind scan: %w", err)
		}
		def := &sim.ItemKindDef{
			Name:           sim.ItemKind(name),
			DisplayLabel:   displayLabel,
			Category:       sim.ItemCategory(category),
			SortOrder:      sortOrder,
			Capabilities:   capabilities,
			DurabilityUses: durabilityUses,
		}
		if displayLabelSingular != nil {
			def.DisplayLabelSingular = *displayLabelSingular
		}
		if displayLabelPlural != nil {
			def.DisplayLabelPlural = *displayLabelPlural
		}
		if narration != nil {
			def.ConsumeDwellNarration = *narration
		}
		if description != nil {
			def.Description = *description
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

// upsertItemSatisfiesSQL writes one item_satisfies row (the operator
// satiation-edit path, LLM-119). PK is (item_kind, attribute); ON CONFLICT
// updates ONLY the immediate amount, leaving any hand-authored dwell triple
// (dwell_amount / dwell_period_minutes / dwell_total_ticks) intact. A new
// attribute inserts with the dwell columns at their NULL defaults — immediate-
// only, the MVP write surface.
const upsertItemSatisfiesSQL = `
INSERT INTO item_satisfies (item_kind, attribute, amount)
VALUES ($1, $2, $3)
ON CONFLICT (item_kind, attribute) DO UPDATE SET
    amount = EXCLUDED.amount`

// UpsertItemSatisfies inserts or updates one immediate need-ease magnitude in
// item_satisfies — the durable half of the umbilical /item/set-satisfies route
// (LLM-119). The catalog has no checkpoint path (reference data), so this is a
// direct, standalone write; the in-memory ItemKindDef.Satisfies update is the
// caller's separate step. item_kind must already exist (FK enforced by the DB);
// amount must be positive (item_satisfies.amount is CHECK > 0 — re-asserted here
// so a bad value fails before it reaches pg). Only the amount column is written,
// so an edit preserves any existing dwell triple on the row.
func (r *ItemKindsRepo) UpsertItemSatisfies(ctx context.Context, kind sim.ItemKind, attribute sim.NeedKey, amount int) error {
	if kind == "" {
		return fmt.Errorf("pg item_kinds UpsertItemSatisfies: empty item_kind")
	}
	if attribute == "" {
		return fmt.Errorf("pg item_kinds UpsertItemSatisfies: empty attribute")
	}
	if amount <= 0 {
		return fmt.Errorf("pg item_kinds UpsertItemSatisfies: amount must be positive (got %d)", amount)
	}
	if _, err := r.pool.Exec(ctx, upsertItemSatisfiesSQL, string(kind), string(attribute), amount); err != nil {
		return fmt.Errorf("pg item_kinds UpsertItemSatisfies: exec: %w", err)
	}
	return nil
}

// upsertItemKindSQL writes one item_kind row — the durable half of the operator
// item-definition edit (umbilical /item/set, LLM-200). ON CONFLICT (name) it
// UPDATEs every authorable definitional column in place, which is what makes the
// all-live new-good flow possible (item/set → recipe/set → restock/set; no
// migration, no restart). This is the full-upsert sibling of the insert-or-ignore
// upsertDiscoveredItemKindSQL below — that one only protects an engine-minted row
// from clobber, this one is the operator deliberately authoring the definition.
//
// hours_per_unit is deliberately NOT written: v2 doesn't model it (the loader
// skips it, ItemKindDef has no field) — production rate lives on item_recipe
// (rate_qty / rate_per_hours), set via recipe/set. On insert it takes its column
// default (NULL); on update it's left untouched. item_satisfies is a separate
// table (set via item/set-satisfies) and is likewise untouched here, so editing
// an item's definition never disturbs its satiation rows.
const upsertItemKindSQL = `
INSERT INTO item_kind (
    name, display_label, display_label_singular, display_label_plural,
    category, sort_order, capabilities, consume_dwell_narration,
    durability_uses, description)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (name) DO UPDATE SET
    display_label           = EXCLUDED.display_label,
    display_label_singular  = EXCLUDED.display_label_singular,
    display_label_plural    = EXCLUDED.display_label_plural,
    category                = EXCLUDED.category,
    sort_order              = EXCLUDED.sort_order,
    capabilities            = EXCLUDED.capabilities,
    consume_dwell_narration = EXCLUDED.consume_dwell_narration,
    durability_uses         = EXCLUDED.durability_uses,
    description             = EXCLUDED.description`

// UpsertItemKind inserts or updates one item_kind definition — the durable half
// of the umbilical /item/set route (LLM-200). The catalog has no checkpoint path
// (reference data), so this is a direct write; the in-memory World.ItemKinds
// update (sim.SetItemKind) is the caller's separate step and runs only after this
// lands. name/display_label/category are re-asserted non-empty here (the handler
// validates first, 400) so a bad value fails before it reaches pg. The nullable
// label/narration columns store SQL NULL for a Go empty string (matching the
// loader's NULL→empty round-trip and the Singular()/Plural() fallback); the
// NOT NULL capabilities[] column coalesces nil → '{}'.
func (r *ItemKindsRepo) UpsertItemKind(ctx context.Context, def sim.ItemKindDef) error {
	if def.Name == "" {
		return fmt.Errorf("pg item_kinds UpsertItemKind: empty name")
	}
	if def.DisplayLabel == "" {
		return fmt.Errorf("pg item_kinds UpsertItemKind: empty display_label")
	}
	if def.Category == "" {
		return fmt.Errorf("pg item_kinds UpsertItemKind: empty category")
	}
	// Nullable label/narration columns: a Go empty string → SQL NULL by leaving
	// the arg as a nil interface (the village_objects nullable-write idiom).
	var singularArg, pluralArg, narrationArg any
	if def.DisplayLabelSingular != "" {
		singularArg = def.DisplayLabelSingular
	}
	if def.DisplayLabelPlural != "" {
		pluralArg = def.DisplayLabelPlural
	}
	if def.ConsumeDwellNarration != "" {
		narrationArg = def.ConsumeDwellNarration
	}
	// description is a nullable free-text column: Go empty string → SQL NULL,
	// round-tripping with the loader's NULL→"" mapping (same idiom as the labels).
	var descriptionArg any
	if def.Description != "" {
		descriptionArg = def.Description
	}
	// capabilities is NOT NULL DEFAULT '{}': nil → empty slice so pg stores '{}'.
	caps := def.Capabilities
	if caps == nil {
		caps = []string{}
	}
	if _, err := r.pool.Exec(ctx, upsertItemKindSQL,
		string(def.Name),
		def.DisplayLabel,
		singularArg,
		pluralArg,
		string(def.Category),
		def.SortOrder,
		caps,
		narrationArg,
		def.DurabilityUses,
		descriptionArg,
	); err != nil {
		return fmt.Errorf("pg item_kinds UpsertItemKind: exec: %w", err)
	}
	return nil
}

// upsertDiscoveredItemKindSQL inserts an engine-minted kind (ZBBS-WORK-412) and
// does nothing if the name already exists — so the checkpoint never clobbers an
// authored row or an operator's later edit to a discovered kind (e.g. once they
// source it and re-categorize it food). Only the three columns a discovery
// carries are written; sort_order/capabilities/consume_dwell_narration take
// their column defaults and no item_satisfies rows are added.
const upsertDiscoveredItemKindSQL = `
INSERT INTO item_kind (name, display_label, category)
VALUES ($1, $2, $3)
ON CONFLICT (name) DO NOTHING`

// saveDiscoveredKinds upserts the discovered (engine-minted) item kinds carried
// on the checkpoint snapshot. Called from SaveWorld inside the checkpoint Tx so
// a crash can't split it from the rest of the checkpoint. Package-private and
// SQL-co-located here (rather than an ItemKindsRepo interface method) because
// it's a pg-only write with no mem/test-fake counterpart — the catalog is
// reference data everywhere else.
func saveDiscoveredKinds(ctx context.Context, tx sim.Tx, kinds []sim.DiscoveredKind) error {
	for _, k := range kinds {
		if _, err := tx.Exec(ctx, upsertDiscoveredItemKindSQL, string(k.Name), k.DisplayLabel, string(k.Category)); err != nil {
			return fmt.Errorf("pg item_kinds saveDiscoveredKinds: upsert %q: %w", k.Name, err)
		}
	}
	return nil
}
