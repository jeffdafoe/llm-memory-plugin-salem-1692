package sim

// Recipe + restock-policy data model — in-memory port of engine/recipes.go
// types. The recipe catalog is reference state (loaded at startup, hot-
// reload on SIGHUP); RestockPolicy lives per-actor.

// ItemRecipe is one entry in the item_recipe catalog — how an item is
// produced, at what rate, with what (optional) inputs, and the
// wholesale/retail price points used by pay-deliberation.
type ItemRecipe struct {
	OutputItem     ItemKind
	OutputQty      int // batch size; one execution mints this many units
	RateQty        int
	RatePerHours   int // rate_qty units per rate_per_hours hours
	Inputs         []RecipeInput
	WholesalePrice int // producer → merchant
	RetailPrice    int // merchant → customer
}

// RecipeInput is one input requirement for a recipe execution. JSON tags
// match the item_recipe.inputs JSONB wire shape ([{"item","qty"}, ...])
// the pg RecipesRepo unmarshals.
type RecipeInput struct {
	Item ItemKind `json:"item"`
	Qty  int      `json:"qty"`
}

// RestockSource enumerates the supply modes a restock entry can use.
type RestockSource string

const (
	RestockSourceProduce RestockSource = "produce"
	RestockSourceBuy     RestockSource = "buy"
)

// RestockEntry is one item the role manages — either by producing it
// themselves (`produce`) or by buying it from another actor (`buy`).
//
// Cap (the unified personal-carry cap, ZBBS-HOME-249) lives in Max;
// Target is a legacy alias retained for buy entries written before the
// unification. Cap() picks the right one.
type RestockEntry struct {
	Item   ItemKind
	Source RestockSource
	Max    int
	Target int // legacy alias for buy entries
}

// Cap returns the personal-carry cap for this entry: prefer Max, fall
// back to Target, return 0 when neither is set ("no cap configured").
func (e RestockEntry) Cap() int {
	if e.Max > 0 {
		return e.Max
	}
	return e.Target
}

// RestockPolicy is an actor's union of restock entries across all
// applicable attributes (tavernkeeper + worker, etc.). Read from
// actor_attribute.params.restock in legacy; first-listed wins on
// ordering ties.
type RestockPolicy struct {
	Restock []RestockEntry
}

// ProduceEntries filters the policy to just the produce-source entries.
// Used by produce_tick to iterate without re-checking the source field.
func (p *RestockPolicy) ProduceEntries() []RestockEntry {
	if p == nil {
		return nil
	}
	out := make([]RestockEntry, 0, len(p.Restock))
	for _, e := range p.Restock {
		if e.Source == RestockSourceProduce {
			out = append(out, e)
		}
	}
	return out
}

// BuyEntries filters the policy to just the buy-source entries — the items
// a reseller restocks by purchasing from another actor rather than making
// itself. Mirror of ProduceEntries; consumed by the restock producer
// (restock_tick.go) and the "## Restocking" perception section.
func (p *RestockPolicy) BuyEntries() []RestockEntry {
	if p == nil {
		return nil
	}
	out := make([]RestockEntry, 0, len(p.Restock))
	for _, e := range p.Restock {
		if e.Source == RestockSourceBuy {
			out = append(out, e)
		}
	}
	return out
}
