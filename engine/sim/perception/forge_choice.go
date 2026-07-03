package perception

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// forge_choice.go — LLM-116. The "## Time to produce" cue. A multi-output crafter
// (the smith makes skillets AND nails) forges one good at a time and picks which
// via the `produce` tool; produce_tick then fills only the actor's ProductionFocus.
// This cue surfaces, AT the workplace, every good the actor can make — its
// per-unit time cost, current stock vs cap, and recent sell-through — so the
// choice leans toward what is actually selling. A single-output producer never
// sees it (one good, nothing to choose). The demand read reuses sellerRecentSales
// over restockSalesWindow — the same weekly signal "## Restocking" shows a
// reseller, kept consistent so the two cues can't disagree on "what's moving".

// ForgeChoiceView is the content-gated "## Time to produce" section. A nil view (or
// empty Items) means render omits the section.
type ForgeChoiceView struct {
	Items []ForgeChoiceItem
	// FocusNoun is the plural counting phrase ("nails") of the crafter's current
	// focus WHEN one is set and still below cap, else "". When set, the cue leads
	// with a continue-and-stop steer instead of the choose menu, so a weak model
	// isn't re-invited to pick what it is already forging (LLM-128). An at-cap or
	// unset focus leaves it "" and keeps the menu — the production-choice warrant
	// legitimately wants a fresh pick there.
	FocusNoun string
}

// ForgeChoiceItem is one good the crafter can forge.
type ForgeChoiceItem struct {
	ItemLabel    string
	itemKind     sim.ItemKind // unexported sort tiebreak
	PerUnitHours int          // hours to make one unit (rate_per_hours / rate_qty, floored at 1)
	RateQty      int          // units per batch (>1 → "N every Hh" wording)
	RatePerHours int
	OnHand       int
	Cap          int // 0 = uncapped
	SoldUnits    int // units sold over the past week — the demand signal
	MadeUnits    int // units actually forged over the past week — recent-production signal
	IsFocus      bool
	InputsReady  bool           // false when the crafter lacks the inputs to make one now (LLM-257)
	Missing      []MissingInput // per-input shortfalls, for the "can't make now" annotation
}

// MissingInput is one recipe input the crafter is short of, resolved to its
// catalog display label, for the forge-choice "can't make now" annotation
// (LLM-257).
type MissingInput struct {
	Label string
	Need  int
	Have  int
}

// buildForgeChoice builds the forge-choice view for a multi-output crafter AT its
// workplace, else nil. Pure over the snapshot. Gated on: a RestockPolicy with MORE
// THAN ONE recipe-backed produce entry (a single-output producer has no choice),
// the recipe catalog present, and the actor physically inside its work structure
// (production only happens there — produce_tick's own gate).
func buildForgeChoice(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *ForgeChoiceView {
	if snap == nil || actorSnap == nil || actorSnap.RestockPolicy == nil || snap.Recipes == nil {
		return nil
	}
	if actorSnap.WorkStructureID == "" || actorSnap.InsideStructureID != actorSnap.WorkStructureID {
		return nil // only at the forge
	}
	produce := actorSnap.RestockPolicy.ProduceEntries()
	if len(produce) <= 1 {
		return nil // single-output producer — no choice to make
	}
	var items []ForgeChoiceItem
	for _, e := range produce {
		recipe := snap.Recipes[e.Item]
		if recipe == nil || recipe.RateQty <= 0 || recipe.RatePerHours <= 0 {
			continue
		}
		perUnit := recipe.RatePerHours / recipe.RateQty
		if perUnit < 1 {
			perUnit = 1
		}
		soldUnits, _ := sellerRecentSales(snap, actorID, e.Item, restockSalesWindow)
		inputsReady := sim.HasProduceInputs(recipe, actorSnap.Inventory)
		var missing []MissingInput
		if !inputsReady {
			missing = missingInputs(snap, recipe, actorSnap.Inventory)
		}
		items = append(items, ForgeChoiceItem{
			ItemLabel:    itemDisplayLabel(snap, e.Item),
			itemKind:     e.Item,
			PerUnitHours: perUnit,
			RateQty:      recipe.RateQty,
			RatePerHours: recipe.RatePerHours,
			OnHand:       actorSnap.Inventory[e.Item],
			Cap:          e.Cap(),
			SoldUnits:    soldUnits,
			MadeUnits:    recentProducedUnits(snap, actorSnap, e.Item, restockSalesWindow),
			IsFocus:      actorSnap.ProductionFocus == e.Item,
			InputsReady:  inputsReady,
			Missing:      missing,
		})
	}
	if len(items) < 2 {
		return nil // need at least two recipe-backed options to be a choice
	}
	// Highest recent demand first (steer toward what sells), then label, then kind.
	sort.Slice(items, func(i, j int) bool {
		if items[i].SoldUnits != items[j].SoldUnits {
			return items[i].SoldUnits > items[j].SoldUnits
		}
		if items[i].ItemLabel != items[j].ItemLabel {
			return items[i].ItemLabel < items[j].ItemLabel
		}
		return items[i].itemKind < items[j].itemKind
	})
	view := &ForgeChoiceView{Items: items}
	// LLM-128: surface an already-set, still-productive focus so the cue can
	// steer "keep going / done()" instead of "choose". `items` holds ONLY
	// recipe-backed makeable goods — the build loop above `continue`s past any
	// without a positive-rate recipe — so an IsFocus entry HERE is already
	// makeable; the below-cap AND inputs-ready checks complete the craftability
	// test, matching shouldChooseProduction's gate (craftableNow: makeable AND
	// below cap AND inputs on hand — LLM-257) exactly. A non-makeable focus never
	// gets an item, and an at-cap OR input-starved focus fails the check, so all
	// three fall through to the choose menu — the same states the production-choice
	// warrant fires on, so the cue and the warrant can't disagree. Keep them in
	// lockstep: if the build filter or shouldChooseProduction's craftability gate
	// changes, the other must too.
	for _, it := range items {
		if it.IsFocus {
			if (it.Cap <= 0 || it.OnHand < it.Cap) && it.InputsReady {
				view.FocusNoun = itemPlural(snap, it.itemKind)
			}
			break
		}
	}
	return view
}

// recentProducedUnits totals the units of `item` the actor actually FORGED within
// the trailing `window` (off the restart-lossy RecentProduce ring) — the
// production analog of sellerRecentSales, using the same snap.PublishedAt − window
// cutoff so "made N" and "sold N" share one reference instant.
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

// renderForgeChoice writes the "## Time to produce" section. Content-gated: a
// nil/empty view writes nothing. One line per makeable good — time cost, stock vs
// cap, and last week's sales — then a steer toward demand. Count-led like the
// other economy cues (no item-noun pluralization).
func renderForgeChoice(b *strings.Builder, v *ForgeChoiceView) {
	if v == nil || len(v.Items) == 0 {
		return
	}
	b.WriteString("## Time to produce\n")
	if v.FocusNoun != "" {
		// LLM-128: a productive focus is already set — lead with the
		// continue-and-stop steer naming what's being made, not the choose
		// prompt that lured the weak model into re-picking every tick. The produce
		// tool stays advertised, so the closing clause notes it may still switch;
		// the lead imperative is to stop and let production run.
		fmt.Fprintf(b, "You are producing %s now — tend your post or call done(). Choose again with produce only if you mean to switch.\n\n", v.FocusNoun)
		return
	}
	b.WriteString("You make one thing at a time. Choose what to produce next — you keep making it until you choose again.\n")
	for _, it := range v.Items {
		rate := fmt.Sprintf("about %dh each", it.PerUnitHours)
		if it.RateQty > 1 {
			rate = fmt.Sprintf("%d every %dh", it.RateQty, it.RatePerHours)
		}
		stock := fmt.Sprintf("%d on hand", it.OnHand)
		if it.Cap > 0 {
			stock = fmt.Sprintf("%d of %d on hand", it.OnHand, it.Cap)
		}
		// Status suffix: an input-starved good is flagged "can't make now" with the
		// shortfall so the model picks informed and isn't lured into re-choosing a
		// good it can't make (LLM-257); otherwise the focused good carries the
		// "making this now" marker. A starved good never carries the focus marker —
		// a starved focus already dropped FocusNoun to "" and lands us in this menu.
		suffix := ""
		if !it.InputsReady {
			suffix = " — can't make now: " + renderMissingInputs(it.Missing)
		} else if it.IsFocus {
			suffix = " — making this now"
		}
		fmt.Fprintf(b, "- %s: %s, %s, made %d and sold %d this past week%s.\n",
			sanitizeInline(it.ItemLabel), rate, stock, it.MadeUnits, it.SoldUnits, suffix)
	}
	b.WriteString("\n")
}

// missingInputs lists the recipe inputs actorSnap's inventory is short of,
// resolving each to its catalog display label for the forge-choice annotation
// (LLM-257). Mirrors sim.HasProduceInputs's per-input test.
func missingInputs(snap *sim.Snapshot, recipe *sim.ItemRecipe, inventory map[sim.ItemKind]int) []MissingInput {
	if recipe == nil {
		return nil
	}
	var out []MissingInput
	for _, in := range recipe.Inputs {
		if in.Qty <= 0 {
			continue
		}
		if have := inventory[in.Item]; have < in.Qty {
			out = append(out, MissingInput{
				Label: itemDisplayLabel(snap, in.Item),
				Need:  in.Qty,
				Have:  have,
			})
		}
	}
	return out
}

// renderMissingInputs phrases a shortfall list for the forge-choice annotation,
// e.g. "short of sage (need 2, have 0), meat (need 10, have 4)" (LLM-257).
func renderMissingInputs(missing []MissingInput) string {
	if len(missing) == 0 {
		return "missing inputs"
	}
	parts := make([]string, 0, len(missing))
	for _, m := range missing {
		parts = append(parts, fmt.Sprintf("%s (need %d, have %d)", sanitizeInline(m.Label), m.Need, m.Have))
	}
	return "short of " + strings.Join(parts, ", ")
}
