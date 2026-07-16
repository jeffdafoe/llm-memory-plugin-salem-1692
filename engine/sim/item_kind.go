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

	// DisplayLabel is the human-facing menu/catalog label (Pay modal, admin
	// UI) and a resolution alias. v1's item_kind.display_label.
	DisplayLabel string

	// DisplayLabelSingular / DisplayLabelPlural are the in-world COUNTING noun
	// phrases (article-less), LLM-113 — the form woven into perception prose and
	// buy/gather cues. Count nouns are regular ("axe"/"axes",
	// "raspberry"/"raspberries"); mass nouns carry a period measure word
	// ("tankard of ale"/"tankards of ale", "loaf of bread"/"loaves of bread").
	// Both are ALSO matched on the input side (resolveItemKind), so the model
	// may name an item by key, label, singular, or plural. Empty falls back to
	// DisplayLabel — a discovery-minted kind (ZBBS-WORK-412) carries neither.
	DisplayLabelSingular string
	DisplayLabelPlural   string

	// Description is optional flavor prose for a catalog good (LLM-410) —
	// item_kind.description, a nullable free-text column. Distinct from the
	// counting labels: it's what the good IS in-world ("a heavy wool coat, long
	// against the wind and the rain"), surfaced when the good is laid out or
	// examined — e.g. the factor spreading his wares (LLM-410 slice 3) — not woven
	// into every buy cue. Empty for goods with no authored description (the whole
	// pre-410 catalog: NULL round-trips to "" like the label columns).
	Description string

	// Category is the soft-typed bucket — a free VARCHAR(32) in both v1 and v2.
	// ItemCategory names the well-known values (food | drink | material | craft,
	// plus the engine-minted "unknown"), but it is a string soft-type, not a
	// closed set: the umbilical item/set route (LLM-200) accepts any category so
	// operators can introduce new good classes (e.g. "tool") without a deploy.
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

	// DurabilityUses marks this kind a durable TOOL when > 0: how many produce
	// executions one unit lasts (LLM-330). A recipe input of a durable kind is
	// required on hand at produce start but NOT consumed; instead the actor's
	// per-kind wear counter (Actor.ToolWear) decrements 1 per execution, and at
	// 0 the unit is spent (inventory -1, next use takes up a fresh unit at full
	// durability). 1 degenerates to the old consumed-per-execution behavior;
	// 0 (the default) keeps plain consumed-input semantics. From
	// item_kind.durability_uses; tunable per kind via the umbilical item/set
	// route.
	DurabilityUses int

	// WearMinutes marks this kind a wearable GARMENT when > 0: how many WORKED
	// MINUTES one unit lasts (LLM-422). While its bearer is in a working posture
	// (actorWearsGarments), the garment-wear sweep decrements the actor's in-use
	// unit (Actor.GarmentWear) by the elapsed worked minutes; at 0 the unit is
	// spent (inventory -1, next use takes up a fresh unit at full budget) and the
	// bearer must rebuy — the recurring clothing demand this ticket exists to
	// create. 0 (the default) keeps a good durable-forever (the whole pre-422
	// catalog; charms, whose mechanic is LLM-423). The worked-MINUTE sibling of
	// DurabilityUses (produce executions): a garment's life is measured in labor
	// time, not batches. From item_kind.wear_minutes; tunable per kind via the
	// umbilical item/set route.
	WearMinutes int

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

// ItemCategory is a free-text soft-type mirroring the item_kind.category
// VARCHAR(32) column — NOT a closed enum. The constants below are the
// well-known values, but any string is valid: the umbilical item/set route
// (LLM-200) writes operator-authored categories (e.g. "tool") straight
// through, by design (LLM-204). Treat the named constants as a convenience
// vocabulary, not an exhaustive set — any switch over this type must include a
// default branch so an off-vocabulary value is handled, not dropped.
type ItemCategory string

const (
	ItemCategoryFood     ItemCategory = "food"
	ItemCategoryDrink    ItemCategory = "drink"
	ItemCategoryMaterial ItemCategory = "material"
	ItemCategoryCraft    ItemCategory = "craft"

	// ItemCategoryUnknown tags a kind the engine MINTED from an agent NPC's
	// reference to a good not in the catalog (ZBBS-WORK-412 hallucinated-item
	// discovery). It carries no recipe/price/satisfies and zero instances —
	// inert until an operator sources it (recipe/price/gather) or deletes the
	// row. The category doubles as the discovery marker: the Village Config
	// items table renders it "unknown" at "0 in world", and the checkpoint
	// persists exactly the unknown-category kinds.
	ItemCategoryUnknown ItemCategory = "unknown"
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

// Singular is the article-less singular counting noun phrase for in-world prose
// and cues (LLM-113), falling back to DisplayLabel when unset — a discovery-
// minted kind (ZBBS-WORK-412) carries no phrase. Nil-safe.
func (d *ItemKindDef) Singular() string {
	if d == nil {
		return ""
	}
	if d.DisplayLabelSingular != "" {
		return d.DisplayLabelSingular
	}
	return d.DisplayLabel
}

// Plural is the plural counting noun phrase (LLM-113), falling back to the
// singular form (then DisplayLabel) when unset. Nil-safe.
func (d *ItemKindDef) Plural() string {
	if d == nil {
		return ""
	}
	if d.DisplayLabelPlural != "" {
		return d.DisplayLabelPlural
	}
	return d.Singular()
}

// CountNoun is the count-aware noun for "N <item>" prose (LLM-113): the singular
// phrase at qty == 1, the plural otherwise. Nil-safe.
func (d *ItemKindDef) CountNoun(qty int) string {
	if qty == 1 {
		return d.Singular()
	}
	return d.Plural()
}

// WithIndefiniteArticle prefixes "a"/"an" to a noun phrase by the leading
// letter's vowel sound — "a skillet", "an ingot of iron", "an axe" (LLM-113).
// Sufficient for the catalog vocabulary (no silent-h or "u-as-you" cases in the
// authored phrases). Empty in, empty out. Shared by the consume failure copy
// (item_commands.go) and the perception buy cue so the rule lives in one place.
func WithIndefiniteArticle(noun string) string {
	if noun == "" {
		return ""
	}
	switch noun[0] {
	case 'a', 'e', 'i', 'o', 'u', 'A', 'E', 'I', 'O', 'U':
		return "an " + noun
	default:
		return "a " + noun
	}
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

// IsDrink reports whether this kind is a drink by its intrinsic category —
// the food/drink identity that fixes how a consume reads, INDEPENDENT of which
// need a unit happens to ease. A belly-filling ale (Category drink, primary
// Satisfies hunger) is still a drink: you drink it, and its hunger relief is a
// bonus, not what makes it food (LLM-318). Nil-safe. Off-vocabulary categories
// (material/craft/unknown) are not drinks — only the explicit drink category
// counts, so the default stays "eat".
func (d *ItemKindDef) IsDrink() bool {
	return d != nil && d.Category == ItemCategoryDrink
}

// ConsumeVerb is the second-person verb for consuming a unit of this kind:
// "drink" for a drink, "eat" for everything else. Keyed on the item's own
// category (IsDrink), NOT on the need eased, so a hunger-restoring ale reads
// "drink ale" (LLM-318). Shared by the consume-result suffix and any other
// consume-facing copy so the verb can't drift between them.
func ConsumeVerb(def *ItemKindDef) string {
	if def.IsDrink() {
		return "drink"
	}
	return "eat"
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
