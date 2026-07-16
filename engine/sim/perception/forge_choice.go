package perception

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// forge_choice.go — the "## Your trade" cue (LLM-116, redesigned in LLM-319).
// Production is opt-in per batch: at the workplace, with nothing in the works,
// this cue presents each good the actor can make AS A SCENE — stock and
// sell-through compiled into felt business language, the way needs render as
// peckish/hungry/starving rather than numbers — and the actor decides whether
// to start a batch. Never an instruction: the scene is the argument, the model
// draws the conclusion, and "no" is a legitimate outcome (the broke-keeper
// agency LLM-319 exists to grant). Shown to every producer, single- and
// multi-output alike; a one-good keeper's choice is the go/no-go on another
// batch.
//
// The demand read reuses sellerRecentSales over restockSalesWindow — the same
// weekly signal "## Restocking" shows a reseller, kept consistent so the two
// cues can't disagree on "what's moving".
//
// This cue's presence gates the produce tool (gateTools offerCraft), so while
// a batch is in flight — buildForgeChoice returns nil then — the tool
// disappears with the cue, and the standing "You are making a batch of X" line
// (InFlightProduction) is what renders instead. The quietest idle tier still
// renders its soft line for the same reason: an empty cue would strip the tool
// from a keeper who might judge a batch worthwhile anyway.

// ForgeChoiceView is the content-gated "## Your trade" section. A nil view (or
// empty Items) means render omits the section.
type ForgeChoiceView struct {
	Items []ForgeChoiceItem
}

// StockTier is the engine-computed judgment of how much of a good the producer
// holds against its carry cap — the fullness axis of the trade scene.
type StockTier int

const (
	StockEmpty StockTier = iota // none on hand
	StockLow                    // at or below a third of cap
	StockAmple                  // below cap with room for a whole batch
	StockFull                   // a whole batch wouldn't fit — a start would be rejected
)

// MovementTier is the engine-computed judgment of the good's recent
// sell-through (units over restockSalesWindow, measured against the batch
// size) — the demand axis of the trade scene.
type MovementTier int

const (
	MovementNone   MovementTier = iota // nothing sold this window
	MovementSlow                       // less than a batch sold
	MovementSteady                     // a batch or more sold
	MovementBrisk                      // two batches or more sold
)

// ForgeChoiceItem is one good the producer makes, reduced to the judgments the
// scene renders from. Noun is the catalog plural display phrase ("nails",
// "porridge") — used verbatim in the prose, and it resolves as a produce
// argument (resolveItemKind matches plural phrases, LLM-113), so the scene
// keeps the tool binding exact without quoting numbers.
type ForgeChoiceItem struct {
	Noun        string
	itemKind    sim.ItemKind // unexported sort tiebreak
	BatchQty    int
	WorkPhrase  string // humanized cycle work ("an hour and a quarter")
	Stock       StockTier
	Movement    MovementTier
	HasInputs   bool // the recipe requires inputs at all
	InputsReady bool // one batch's inputs are on hand (LLM-257); always true for a listed good — LLM-324 drops input-short ones
	SoldUnits   int  // raw units for narration order (demand first)
}

// buildForgeChoice builds the trade-scene view for a producer AT its
// workplace with nothing in the works, else nil. Pure over the snapshot.
// Gated on: a RestockPolicy with at least one recipe-backed produce entry, the
// recipe catalog present, the actor physically inside its work structure
// (production only advances there — produce_tick's own gate), and NO in-flight
// production cycle (the tool must not be re-offered mid-batch; its cue
// carries the tool, LLM-319).
//
// The menu lists ONLY goods the actor could start a batch of right now — the
// same craftableNow gate shouldChooseProduction (the wake warrant) and
// StartProductionCycle (the produce tool) apply. A good at cap or short an input
// is dropped, and when none survive the view is nil → offerCraft false → the
// produce tool is not advertised, so the cue can never invite a batch the tool
// would then reject (LLM-324).
func buildForgeChoice(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *ForgeChoiceView {
	if snap == nil || actorSnap == nil || actorSnap.RestockPolicy == nil || snap.Recipes == nil {
		return nil
	}
	if actorSnap.WorkStructureID == "" || actorSnap.InsideStructureID != actorSnap.WorkStructureID {
		return nil // only at the post
	}
	if actorSnap.ProductionItem != "" {
		return nil // a batch is in the works — the standing in-progress line renders instead
	}
	// LLM-446: only a FULL production block (degraded at pct 0) suppresses the
	// choice — inviting a batch the tick would pause (and StartProductionCycle
	// rejects) would be a false affordance. At a positive pct a degraded keeper
	// still chooses batches — slowed production is the way out of the
	// sole-nail-producer self-repair deadlock, so the invitation must survive.
	if ownerBusinessProduceBlocked(snap, actorID) {
		return nil
	}
	var items []ForgeChoiceItem
	for _, e := range actorSnap.RestockPolicy.ProduceEntries() {
		recipe := snap.Recipes[e.Item]
		if recipe == nil || recipe.RateQty <= 0 || recipe.RatePerHours <= 0 {
			continue
		}
		batchQty := recipe.OutputQty
		if batchQty < 1 {
			batchQty = 1
		}
		// Craftability filter (LLM-324): mirror craftableNow's two snapshot-visible
		// axes — a whole batch must fit under the carry cap AND the inputs must be on
		// hand (the same HasProduceInputs the tool consumes on). makeableRecipe, its
		// third axis, is the recipe-validity continue above. The cap headroom is
		// computed DIRECTLY (batchFitsCap: uncapped, else onHand+batch <= cap), NOT
		// read off stockTier — stockTier short-circuits to StockEmpty at zero on hand,
		// which would hide a batch larger than the whole cap (batchQty > cap) and
		// wrongly list a good the tool rejects (code_review). A capped or input-short
		// good is dropped so it never enters the menu that advertises the produce tool.
		fitsCap := e.Cap() <= 0 || actorSnap.Inventory[e.Item]+batchQty <= e.Cap()
		inputsReady := sim.HasProduceInputs(recipe, actorSnap.Inventory)
		if !fitsCap || !inputsReady {
			continue
		}
		soldUnits, _ := sellerRecentSales(snap, actorID, e.Item, restockSalesWindow)
		items = append(items, ForgeChoiceItem{
			Noun:        itemPlural(snap, e.Item),
			itemKind:    e.Item,
			BatchQty:    batchQty,
			WorkPhrase:  sim.HumanizeWorkDuration(sim.CycleDurationSeconds(recipe)),
			Stock:       stockTier(actorSnap.Inventory[e.Item], e.Cap(), batchQty),
			Movement:    movementTier(soldUnits, batchQty),
			HasInputs:   len(recipe.Inputs) > 0,
			InputsReady: inputsReady,
			SoldUnits:   soldUnits,
		})
	}
	if len(items) == 0 {
		return nil // not a producer, or nothing craftable right now (LLM-324)
	}
	// Highest recent demand narrated first, then noun, then kind.
	sort.Slice(items, func(i, j int) bool {
		if items[i].SoldUnits != items[j].SoldUnits {
			return items[i].SoldUnits > items[j].SoldUnits
		}
		if items[i].Noun != items[j].Noun {
			return items[i].Noun < items[j].Noun
		}
		return items[i].itemKind < items[j].itemKind
	})
	return &ForgeChoiceView{Items: items}
}

// stockTier reduces on-hand vs cap to the scene's fullness judgment, used for
// NARRATION only. Full means a whole batch wouldn't fit from a POSITIVE on-hand;
// the cue does NOT gate on it — buildForgeChoice computes batchFitsCap directly,
// because stockTier short-circuits to Empty at zero on hand and would miss a batch
// larger than the whole cap. A listed good is therefore never Full (the upstream
// filter dropped it), so the rendered tiers are Empty/Low/Ample. An uncapped good
// (cap 0) never reads Full — there is no shelf to fill.
func stockTier(onHand, cap, batchQty int) StockTier {
	if onHand <= 0 {
		return StockEmpty
	}
	if cap <= 0 {
		return StockAmple
	}
	if onHand+batchQty > cap {
		return StockFull
	}
	if onHand*3 <= cap {
		return StockLow
	}
	return StockAmple
}

// movementTier reduces window sell-through to the scene's demand judgment,
// measured in batches — the natural unit of the decision being made.
func movementTier(soldUnits, batchQty int) MovementTier {
	if soldUnits <= 0 {
		return MovementNone
	}
	if batchQty < 1 {
		batchQty = 1
	}
	if soldUnits >= 2*batchQty {
		return MovementBrisk
	}
	if soldUnits >= batchQty {
		return MovementSteady
	}
	return MovementSlow
}

// renderForgeChoice writes the "## Your trade" scene. Content-gated: a
// nil/empty view writes nothing. One short paragraph per good — the situation
// (stock + sell-through, in felt language) then the affordance (batch size,
// work, means) — demand-first, closing with a neutral choice line that names
// the tool. No imperatives and no stat dumps: the scene escalates by tier
// ("you have no porridge left, and folk keep asking") and the model draws the
// conclusion.
func renderForgeChoice(b *strings.Builder, v *ForgeChoiceView) {
	if v == nil || len(v.Items) == 0 {
		return
	}
	b.WriteString("## Your trade\n")
	for _, it := range v.Items {
		b.WriteString(tradeGoodScene(it))
		b.WriteString("\n")
	}
	b.WriteString("Start a batch with produce, or see to other things.\n\n")
}

// tradeGoodScene phrases one good's situation + affordance. Noun-agreement-safe
// by construction: every template hangs its verb on "stock"/"stores"/"folk"/
// "a few"/"none", never on the noun itself, so mass nouns (porridge) and
// count nouns (nails) read equally well.
func tradeGoodScene(it ForgeChoiceItem) string {
	noun := sanitizeInline(it.Noun)
	var s strings.Builder

	// The situation: stock fullness, then sell-through. Only craftable goods reach
	// the scene (buildForgeChoice drops Full stores, LLM-324), so there is no
	// "no room for another batch" tier here — a good with no headroom isn't offered.
	switch it.Stock {
	case StockEmpty:
		s.WriteString("You have no " + noun + " on hand")
	case StockLow:
		s.WriteString("Your stock of " + noun + " is running low")
	case StockAmple:
		s.WriteString("You have a fair stock of " + noun)
	}
	switch it.Movement {
	case MovementBrisk:
		s.WriteString(", and folk keep asking for more.")
	case MovementSteady:
		s.WriteString("; sales were steady this past week.")
	case MovementSlow:
		s.WriteString("; only a few sold this past week.")
	case MovementNone:
		s.WriteString(", and none sold this past week.")
	}

	// The affordance: what another batch takes. The inputs are on hand by
	// construction (LLM-324 drops input-short goods), so an inputs recipe notes the
	// makings are ready and an origin recipe (no inputs) reads plain.
	work := it.WorkPhrase
	if it.HasInputs {
		s.WriteString(" A batch — " + countPhrase(it.BatchQty) + " more — takes about " + work + ", and you have the makings.")
	} else {
		s.WriteString(" A batch — " + countPhrase(it.BatchQty) + " more — takes about " + work + ".")
	}
	return s.String()
}

// countPhrase renders a small batch count as prose ("10"). Kept as its own
// seam so the scene can grow word-counts ("ten") later without touching the
// templates.
func countPhrase(n int) string {
	return strconv.Itoa(n)
}

// recentProducedUnits totals the units of `item` the actor actually made within
// the trailing `window` (off the restart-lossy RecentProduce ring), using the
// snap.PublishedAt − window cutoff so "made" and "sold" share one reference
// instant. No longer narrated by the trade scene (stock fullness already
// carries accumulation); kept for the umbilical and tests.
func recentProducedUnits(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot, item sim.ItemKind, window time.Duration) int {
	if snap == nil || actorSnap == nil {
		return 0
	}
	cutoff := snap.PublishedAt.Add(-window)
	total := 0
	for _, e := range actorSnap.RecentProduce {
		if e.Item != item || e.At.Before(cutoff) {
			continue
		}
		total += e.Qty
	}
	return total
}
