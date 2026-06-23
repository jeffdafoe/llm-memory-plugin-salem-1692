package perception

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// forage.go — LLM-59, re-based on earned memory by LLM-79. The "## Your bushes
// to harvest" perception section: the produce/harvest-side mirror of restock.go's
// "## Restocking". Surfaces, to a grower-seller whose harvested stock of an item
// is running low, their own forage-to-sell bushes for that item so they can walk
// over and restock — the on-hand/cap, the ripe count across their bushes, and a
// move_to handle to the ripest one.
//
// OWNER-ONLY and DISTANCE-INDEPENDENT — the opposite gating from the wild-bush
// proximity cue (findGatherableCue, build.go): a passer-by only learns of a
// commons bush when adjacent, but a grower must stand aware of their OWN farm —
// wherever it sits — to decide to make the trip. Without this a grower whose
// farm is off their daily path (Prudence Ward: apothecary on the east side,
// berry plot in the NW corner) never gets cued to it and never harvests, so the
// whole forage-to-sell farm is dead content.
//
// SOURCED FROM EARNED MEMORY (LLM-79), not an omniscient world scan: the bushes
// come from the grower's durable known-places set (LLM-77 seeds an owner's owned
// gatherables into it at load), intersected with still-being-owned + still a
// forage source (the liveness re-check). For a present owner the two are the same
// set, but the cue is now a READ of remembered world-knowledge rather than the
// engine god-injecting the owner's farm — the no-omniscience posture the
// world-memory epic (LLM-76) applies across perception. A remembered gather
// source the actor no longer owns (sold the plot) or that's gone falls out.
//
// Gates on the same RestockReorderThresholdMet(on-hand, cap, RestockReorderPct)
// as the buy side (default 25% of cap), so "low" means the same thing for a
// grower as for a reseller. The grower's own LLM decides whether and how much to
// harvest, then walks there with move_to — the cue steers move_to ONLY (LLM-79);
// the at-bush proximity cue (findGatherableCue) advertises the gather tool once
// the grower arrives, so this distant cue never names a tool that isn't callable
// yet (LLM-66: advertise a tool only with its triggering cue; the LLM-59
// gather-from-afar reject loop). No new commit tool, the same LLM-decided posture
// as buy-side restock.

// ForageView is the content-gated "## Your bushes to harvest" section. A nil
// view (or empty Items) means render omits the section.
type ForageView struct {
	Items []ForageItemView
}

// ForageItemView is one low `forage` item the grower could replenish by
// harvesting: its label, current on-hand vs the cap it tops up toward, how many
// of the grower's own bushes carry it, the total ripe units across them, and the
// move_to handle of the ripest bush (surfaced as a structure_id, the same as
// satiation's free-source navigation). RipeUnits 0 / MoveHandle "" means the
// grower owns bushes for the item but none are ripe this tick.
type ForageItemView struct {
	ItemLabel  string
	CurrentQty int
	Cap        int
	BushCount  int
	RipeUnits  int
	MoveHandle sim.VillageObjectID

	// kind is the final sort tie-break for two kinds sharing a display label.
	// Unexported — never rendered. Same posture as RestockItemView.kind.
	kind sim.ItemKind
}

// buildForage builds the forage view for actorSnap, or nil when the actor holds
// no `forage` entry below the reorder threshold, remembers no still-owned forage
// bush for a low item (LLM-79 — sourced from the known-places set, not a world
// scan), restock is disabled (RestockReorderPct == 0), or it carries no
// RestockPolicy. Pure over the snapshot.
func buildForage(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot) *ForageView {
	if snap == nil || actorSnap == nil || actorSnap.RestockPolicy == nil {
		return nil
	}
	pct := snap.RestockReorderPct
	if pct <= 0 {
		return nil // restock feature disabled
	}
	var items []ForageItemView
	for _, e := range actorSnap.RestockPolicy.ForageEntries() {
		cap := e.Cap()
		current := actorSnap.Inventory[e.Item]
		if !sim.RestockReorderThresholdMet(current, cap, pct) {
			continue
		}
		// Scan the grower's REMEMBERED gather bushes for this item (LLM-79): the
		// known-places set, not an omniscient world scan. A bush qualifies when the
		// grower remembers gathering this item there (the "gather:<item>" affordance
		// LLM-77 seeds for an owner's owned gatherables) AND it is still live, still
		// the grower's own, and still a forage source — the liveness re-check that
		// drops a sold-off or removed plot and a wild bush the grower merely
		// gathered at (not their own to restock from). Deterministic move handle:
		// the ripest bush, ties broken by the lowest object id.
		affordance := "gather:" + string(e.Item)
		bushCount := 0
		ripeUnits := 0
		var moveHandle sim.VillageObjectID
		bestStock := -1
		for ref, kp := range actorSnap.KnownPlaces {
			if !kp.HasAffordance(affordance) {
				continue
			}
			id := sim.VillageObjectID(ref)
			obj := snap.VillageObjects[id]
			if obj == nil || obj.OwnerActorID != actorID {
				continue
			}
			stock, ok := forageStockForItem(obj, e.Item)
			if !ok {
				continue
			}
			bushCount++
			ripeUnits += stock
			if stock > 0 && (moveHandle == "" || stock > bestStock || (stock == bestStock && id < moveHandle)) {
				bestStock = stock
				moveHandle = id
			}
		}
		if bushCount == 0 {
			continue // owns no bushes producing this item — nothing to point at
		}
		items = append(items, ForageItemView{
			ItemLabel:  itemDisplayLabel(snap, e.Item),
			CurrentQty: current,
			Cap:        cap,
			BushCount:  bushCount,
			RipeUnits:  ripeUnits,
			MoveHandle: moveHandle,
			kind:       e.Item,
		})
	}
	if len(items) == 0 {
		return nil
	}
	// Deterministic section order — by item label, then the underlying kind as a
	// tie-break for two kinds sharing a display label (ForageEntries order is
	// stable, but the sort makes the section robust to policy reordering too).
	sort.Slice(items, func(i, j int) bool {
		if items[i].ItemLabel != items[j].ItemLabel {
			return items[i].ItemLabel < items[j].ItemLabel
		}
		return items[i].kind < items[j].kind
	})
	return &ForageView{Items: items}
}

// forageStockForItem returns the total gatherable stock of `item` across obj's
// finite forage-to-sell refresh rows (Amount == 0 — yield-only harvest sources),
// and whether obj carries any such row for the item. A non-forage owned object
// (the grower's house, an eat+pick bush) returns ok=false. Aggregates rather than
// taking the first match so the count never depends on Refreshes slice order if
// an object ever carries more than one matching row.
func forageStockForItem(obj *sim.VillageObject, item sim.ItemKind) (int, bool) {
	total := 0
	found := false
	for _, r := range obj.Refreshes {
		if r == nil || !r.IsFinite() || !r.IsGatherable() {
			continue
		}
		// IsFinite already implies AvailableQuantity != nil; the explicit nil
		// check keeps the deref safe against a partial/corrupt row regardless.
		if r.Amount != 0 || r.GatherItem != item || r.AvailableQuantity == nil {
			continue
		}
		stock := *r.AvailableQuantity
		if stock < 0 {
			stock = 0 // a stock counter is never negative; clamp a corrupt row
		}
		total += stock
		found = true
	}
	return total, found
}

// renderForage writes the "## Your bushes to harvest" section. Mirrors
// renderRestocking's shape: a one-line header, then one line per low item with
// the on-hand/cap, the bush + ripe count, and the move_to handle to the ripest
// bush.
func renderForage(b *strings.Builder, v *ForageView) {
	if v == nil || len(v.Items) == 0 {
		return
	}
	b.WriteString("## Your bushes to harvest\n")
	b.WriteString("Your stock of these is running low. You grow them yourself — walk out to your bushes to restock. You choose how much to pick.\n")
	for _, it := range v.Items {
		headroom := it.Cap - it.CurrentQty
		if headroom < 0 {
			headroom = 0
		}
		fmt.Fprintf(b, "- %s: %d on hand of %d cap (room for %d more). You own %d bush(es) of it",
			sanitizeInline(it.ItemLabel), it.CurrentQty, it.Cap, headroom, it.BushCount)
		if it.MoveHandle != "" {
			// Steer move_to ONLY — no `gather` mention (LLM-79 / LLM-59 fix). The
			// at-bush proximity cue (findGatherableCue) advertises and steers gather
			// once the grower arrives; naming it here, where gather isn't callable
			// yet, drove the weak model to fixate on gather and skip the walk (the
			// LLM-59 reject-retry loop).
			fmt.Fprintf(b, ", %d ripe to pick now. Use move_to with structure_id \"%s\" to walk out to them.\n",
				it.RipeUnits, it.MoveHandle)
		} else {
			b.WriteString(", none ripe yet — they will regrow, so check back later.\n")
		}
	}
}
