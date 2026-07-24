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
	BoostInputs    []BoostInput // optional per-execution yield boosters (LLM-248)
	BoostState     []BoostState // optional per-execution yield boosters keyed on world state (LLM-474)
	SpeedInputs    []SpeedInput // optional speed boosters — item cuts the cycle time, consumed at start (LLM-511)
	WholesalePrice int          // producer → merchant
	RetailPrice    int          // merchant → customer
}

// RecipeInput is one input requirement for a recipe execution. JSON tags
// match the item_recipe.inputs JSONB wire shape ([{"item","qty"}, ...])
// the pg RecipesRepo unmarshals.
type RecipeInput struct {
	Item ItemKind `json:"item"`
	Qty  int      `json:"qty"`
}

// BoostInput is one OPTIONAL booster for a recipe execution (LLM-248) — the
// elective mirror of RecipeInput. At each produce-tick execution, a producer
// holding Qty of the booster consumes it and mints BonusQty extra output
// (cap-clamped); holding none leaves base production untouched (no skip, no
// anchor penalty). Consumption is per-execution, so a booster is a
// production-scaled sink: it drains only while output is actually minting.
// JSON tags match the item_recipe.boost_inputs JSONB wire shape
// ([{"item","qty","bonus_qty"}, ...]).
type BoostInput struct {
	Item     ItemKind `json:"item"`
	Qty      int      `json:"qty"`
	BonusQty int      `json:"bonus_qty"`
}

// RecipeBoostState enumerates the world conditions a BoostState may key on.
// Closed set, checked at every write path: an unrecognised value is rejected
// rather than stored to sit silently never firing.
type RecipeBoostState string

const (
	// BoostStateHearthLit is met when the producer's WORK structure has a
	// burning hearth (the LLM-412 HearthLitUntil clock) at the instant a batch
	// lands. A structure carrying no hearth object never meets it.
	BoostStateHearthLit RecipeBoostState = "hearth_lit"
)

// ValidRecipeBoostState reports whether s is a state the produce tick knows how
// to evaluate. Keep in lockstep with recipeBoostStateMet (produce_tick.go) —
// a state accepted here that the evaluator doesn't answer would be a booster
// that validates and then never pays.
func ValidRecipeBoostState(s RecipeBoostState) bool {
	return s == BoostStateHearthLit
}

// BoostState is one OPTIONAL condition-keyed booster for a recipe execution
// (LLM-474) — the world-state mirror of BoostInput. Where a BoostInput is
// earned by HOLDING an item and consumes it, a BoostState is earned by a fact
// about the world at landing and consumes NOTHING: its cost was already paid
// upstream (firewood burned into the hearth by an earlier stoke). That
// asymmetry is the point — it rewards keeping a condition true instead of
// opening a second sink for the same good.
//
// An unmet state is silent in exactly the way an unheld booster is: no
// execution skip, no anchor penalty, base yield stands. A BoostState must
// never become a gate — see the never-gates invariant in produce_tick_test.go.
// JSON tags match the item_recipe.boost_state JSONB wire shape
// ([{"state","bonus_qty"}, ...]).
type BoostState struct {
	State    RecipeBoostState `json:"state"`
	BonusQty int              `json:"bonus_qty"`
}

// SpeedInput is one OPTIONAL speed booster for a recipe (LLM-511) — the
// rate-side sibling of BoostInput. Where a BoostInput adds output at landing,
// a SpeedInput cuts the TIME to make the batch: a producer holding Qty of Item
// when a cycle STARTS consumes it and the cycle runs at RatePct scale (200 =
// 2x rate = half the wall time). The iron→shovel case: a bar in hand means you
// shape an existing bar instead of forging from scrap, so the work goes quick.
//
// Committed at START, not landing (unlike BoostInput). A rate effect spans the
// whole cycle, so binding the spend to the one instant where it is already
// atomic — the start, where required inputs are also consumed — pairs the cost
// with the benefit cleanly: no mid-cycle "sped for free / charged for nothing"
// split, and no second consumption event at landing. The speedup rides the
// already-checkpointed ProductionActivity.RemainingSeconds (shortened at start),
// so it survives a restart with no extra persisted state.
//
// Elective like BoostInput: holding none leaves the cycle at base rate — never
// a gate (the absorbing-state / liveness rule). RatePct must exceed 100 (a real
// speedup); it composes multiplicatively with produceRateScalePct (the LLM-224
// labor boost, the cold/degraded saps), so iron plus a hired helper runs faster
// still. JSON tags match the item_recipe.speed_inputs JSONB wire shape
// ([{"item","qty","rate_pct"}, ...]).
type SpeedInput struct {
	Item    ItemKind `json:"item"`
	Qty     int      `json:"qty"`
	RatePct int      `json:"rate_pct"`
}

// RestockSource enumerates the supply modes a restock entry can use.
type RestockSource string

const (
	RestockSourceProduce RestockSource = "produce"
	RestockSourceBuy     RestockSource = "buy"
	// RestockSourceForage marks an item the actor restocks by HARVESTING their
	// own owned forage-to-sell bushes (LLM-59) — the produce/harvest-side mirror
	// of `buy`. The replenish destination is the actor's own gatherable objects,
	// not a supplier. Consumed by the "## Your bushes to harvest" perception
	// section (perception/forage.go).
	RestockSourceForage RestockSource = "forage"
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

// ForageEntries filters the policy to just the forage-source entries — the
// items a grower-seller restocks by HARVESTING their own owned forage-to-sell
// bushes rather than buying or auto-producing them. Mirror of BuyEntries;
// consumed by the "## Your bushes to harvest" perception section
// (perception/forage.go). LLM-59.
func (p *RestockPolicy) ForageEntries() []RestockEntry {
	if p == nil {
		return nil
	}
	out := make([]RestockEntry, 0, len(p.Restock))
	for _, e := range p.Restock {
		if e.Source == RestockSourceForage {
			out = append(out, e)
		}
	}
	return out
}

// Manages reports whether this item kind is one of the actor's trade goods —
// something the role produces, buys, or forages per its restock manifest, as
// opposed to personal provisions it merely carries. Any restock source counts
// (a `buy` entry covers a recipe input like a tavernkeeper's stew carrots, not
// just a sellable output). Used to demote a producer's own merchandise out of
// its personal "consume to eat" cue below the desperation tier (LLM-134).
// Nil-safe — an actor with no policy manages nothing.
func (p *RestockPolicy) Manages(kind ItemKind) bool {
	if p == nil {
		return false
	}
	for _, e := range p.Restock {
		if e.Item == kind {
			return true
		}
	}
	return false
}

// ProducesOrForages reports whether this item kind is one the actor supplies at
// first hand — it has a `produce` or `forage` restock entry for it — as opposed
// to one it merely resells via a `buy` entry (or carries as provisions). The
// restock-supplier gate (LLM-252) uses it so a reseller who bought item X in the
// past does NOT qualify as a *supplier* of X: only its producers/foragers (and
// the distributor) do. That keeps the supply chain a one-way DAG (producers →
// distributor → resellers) and makes the Josiah↔John carrot buy-back
// structurally impossible. Nil-safe — a policy-less actor supplies nothing at
// first hand.
func (p *RestockPolicy) ProducesOrForages(kind ItemKind) bool {
	if p == nil {
		return false
	}
	for _, e := range p.Restock {
		if e.Item == kind && (e.Source == RestockSourceProduce || e.Source == RestockSourceForage) {
			return true
		}
	}
	return false
}

// Produces reports whether this item kind is one the actor MAKES — it has a
// `produce` restock entry for it — as distinct from ProducesOrForages, which
// also counts harvested (forage) goods. The commission path (LLM-338) uses it
// to decide whether a stockless take-home offer is a made-to-order forge the
// seller can fulfil by producing, rather than a plain out-of-stock reject.
// Nil-safe — a policy-less actor makes nothing.
func (p *RestockPolicy) Produces(kind ItemKind) bool {
	if p == nil {
		return false
	}
	for _, e := range p.Restock {
		if e.Item == kind && e.Source == RestockSourceProduce {
			return true
		}
	}
	return false
}
