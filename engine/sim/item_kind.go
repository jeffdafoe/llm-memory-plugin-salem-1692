package sim

// ItemKindDef is the reference-state aggregate describing one item kind in
// the catalog: display label, category, default per-unit price, UI sort
// order, and the per-need amounts a unit satisfies when consumed.
//
// Loaded at startup from the item_kind + item_satisfies tables (port of v1's
// ZBBS-091 + ZBBS-125 schema). Lives on World.ItemKinds keyed by ItemKind
// (== Name). Reference state — never mutated at runtime; admin edits go
// through a hot-reload path on SIGHUP that rebuilds the map wholesale.
//
// Mirrors the ItemRecipe / Asset reference-data pattern: no clone helper,
// no checkpoint path. Treat *ItemKindDef as read-only from the moment it
// lands in the world map.
type ItemKindDef struct {
	// Name == the map key. Denormalized so a value-by-itself surface (admin
	// catalog rendering, perception text) doesn't need separate plumbing of
	// the key. Mirrors ItemRecipe.OutputItem.
	Name ItemKind

	// DisplayLabel is the human-facing label rendered in prompts and admin
	// UI. v1's item_kind.display_label.
	DisplayLabel string

	// Category is the soft-typed bucket. v1 stored it as a free VARCHAR with
	// values food | drink | material | craft; v2 ports it as a typed enum so
	// misspellings fail to compile.
	Category ItemCategory

	// SortOrder is the UI sort hint (low → high). v1's item_kind.sort_order.
	SortOrder int

	// Capabilities is the soft-typed capability set from item_kind.capabilities
	// (TEXT[]). Tokens gate non-default item behavior:
	//   - "service" — no physical good: no inventory backing, so the stock
	//     gates (accept_pay gate 10, deliver_order gate 5 + transfer) are
	//     skipped. nights_stay carries this.
	//   - "lodging" — a service that grants a private bedroom on delivery
	//     (deliver_order routes to AssignBedroomForLodger instead of transfer).
	//   - "portable" — take-home eligible (v1 token; not consumed in v2 yet).
	// v1 read these via hasCapability(); v2 models the column on the def.
	Capabilities []string

	// Satisfies is the per-need effect of consuming one unit of this item.
	// Port of v1's item_satisfies table (PK (item_kind, attribute), one row
	// per attribute), embedded here because the v2 single-goroutine substrate
	// doesn't need the join normalization and the read pattern is always
	// "what does this item satisfy?" — never the reverse direction.
	//
	// Each entry carries the immediate-hit amount (post-clamp subtracted from
	// Actor.Needs at consume time) AND the optional dwell triple
	// (DwellAmount, DwellPeriodMinutes, DwellTotalTicks) for the slow-burn
	// per-tick payoff handled by UpsertItemDwellCredits + ApplyDwellTick
	// (see dwell.go + dwell_tick.go). Nil/empty for non-consumables
	// (materials like wheat / flour / iron).
	//
	// Callers shouldn't have duplicate Attribute entries; the load path
	// (v1 schema PK is (item_kind, attribute)) enforces uniqueness, and the
	// in-memory shape relies on that contract. No runtime dedup.
	Satisfies []ItemSatisfaction

	// ConsumeDwellNarration is the one-shot "you're starting a dwell-bearing
	// meal" hint (port of v1's item_kind.consume_dwell_narration). Stamped
	// onto the DwellStarted event when Consume upserts at least one dwell
	// credit for this item, so the eater's NEXT-tick perception surfaces
	// the LLM-readable cue:
	//
	//   "This stew looks really good. You'll need some time to enjoy it
	//    properly."
	//
	// Empty when the item has no narration configured (or no dwell triple).
	// Subscribers render only when non-empty.
	ConsumeDwellNarration string
}

// ItemCategory is the typed item-category enum. Consumers must always
// include a default branch on switches over this type so adding a new
// category doesn't break them.
type ItemCategory string

const (
	ItemCategoryFood     ItemCategory = "food"
	ItemCategoryDrink    ItemCategory = "drink"
	ItemCategoryMaterial ItemCategory = "material"
	ItemCategoryCraft    ItemCategory = "craft"
)

// Consumable reports whether this item kind satisfies any need when a unit
// is consumed. v1 used `satisfies_attribute IS NOT NULL` for the same
// signal pre-ZBBS-125, and `EXISTS (... FROM item_satisfies ...)` after.
// v2 derives it from the embedded Satisfies slice — any entries → consumable.
// Materials with no entries return false; food/drink with entries return
// true. An all-zero entry (no immediate, no dwell triple) is technically
// consumable here but is a catalog-author bug; Consume silent-skips zero-
// magnitude entries at apply time so the consume succeeds-with-no-effect
// rather than rejecting (matches v1 behavior).
func (d *ItemKindDef) Consumable() bool {
	return len(d.Satisfies) > 0
}

// EatHereOnly reports whether this kind always settles eat-here: a
// consumable that is neither a service nor portable (stew, a poured
// drink) cannot leave the seller's premises — "people can't carry stew",
// the hand-seeded data ruling behind ZBBS-WORK-403/405. Centralized so
// the pay clamp, the quote clamp, and the perception facts all derive
// the same class from the same predicate. Nil-safe: an unseeded kind is
// not eat-here-only (degrades permissive, mirroring itemDispositionClass).
func (d *ItemKindDef) EatHereOnly() bool {
	if d == nil {
		return false
	}
	return d.Consumable() &&
		!d.HasCapability("service") &&
		!d.HasCapability("portable")
}

// HasCapability reports whether this item kind carries the given capability
// token (e.g. "service", "lodging", "portable"). Linear over the small
// Capabilities slice — capability sets are tiny (a handful of tokens).
func (d *ItemKindDef) HasCapability(token string) bool {
	for _, c := range d.Capabilities {
		if c == token {
			return true
		}
	}
	return false
}

// itemHasCapability is the world-level capability check used by the commerce
// paths (pay-with-item stock gates, deliver_order fulfillment): does the
// catalog entry for kind carry token? A kind absent from the catalog (or a
// nil ItemKinds map) returns false — callers run their own catalog-presence
// gate separately. Assumes a live world: every caller invokes it from inside
// a Command.Fn, where w is non-nil by construction.
func itemHasCapability(w *World, kind ItemKind, token string) bool {
	def := w.ItemKinds[kind]
	return def != nil && def.HasCapability(token)
}
