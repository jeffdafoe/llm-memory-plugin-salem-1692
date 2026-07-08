package perception

import (
	"fmt"
	"math"
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

// ForageView is the content-gated forage cue. A nil view (or a view with both
// slices empty) means render omits the whole thing. It carries two independent
// sections:
//   - Items — "## Your bushes to harvest": the actor's OWN bushes for a low item,
//     owner-only and distance-independent (LLM-79).
//   - WildSources — "## Free sources you can gather from": the LLM-253 ranged cue
//     for a forager carrying sim.AttrForageRange — the nearest ripe UNOWNED source
//     for a low item the actor owns no bush for, at any distance. A distinct section
//     so it never claims to be "your bushes."
//
// Both live on one view (not two payload fields) so the whole cue defers together
// on a live sale (customerEngaged) and a single p.Forage != nil signal drives the
// duty-steer suppression (build.go) whether the errand is an owned harvest or a
// wild trek.
type ForageView struct {
	Items       []ForageItemView
	WildSources []WildForageItemView
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

// WildForageItemView is one low forage item a ranged forager (sim.AttrForageRange)
// can replenish from an UNOWNED wild source at any distance (LLM-253). Distinct
// from ForageItemView: the source is not the actor's own, so it carries the
// source's own name and a qualitative distance + direction — this cue crosses the
// whole map, unlike the distance-independent owned-bush cue. MoveHandle steers
// move_to ONLY; the at-bush proximity cue (findGatherableCue) advertises gather
// once the forager arrives, so this distant cue never names a tool that isn't
// callable yet (the LLM-59 gather-from-afar reject loop).
type WildForageItemView struct {
	ItemLabel   string
	SourceLabel string
	CurrentQty  int
	Cap         int
	RipeUnits   int
	Distance    string // qualitativeDistance phrase, e.g. "a long walk"
	Direction   string // 8-point compass bearing; empty when coincident
	MoveHandle  sim.VillageObjectID

	// kind is the sort tie-break for two kinds sharing a display label. Unexported
	// — never rendered. Same posture as ForageItemView.kind.
	kind sim.ItemKind
}

// buildForage builds the forage view for actorSnap, or nil when the actor holds
// no `forage` entry below the reorder threshold, remembers no still-owned forage
// bush for a low item (LLM-79 — sourced from the known-places set, not a world
// scan), restock is disabled (RestockReorderPct == 0), a customer is engaged at
// the stall (customerEngaged — see below), or it carries no RestockPolicy. Pure
// over the snapshot.
//
// customerEngaged defers the whole harvest cue while a sale is live at the stall
// — a buyer's pending offer awaiting her decision, a co-present customer in the
// huddle, or a quote she has standing out to a buyer (LLM-90). The harvest cue
// steers her to WALK OFF to her bushes; firing it mid-sale would invite the weak
// model to abandon a customer mid-transaction. Deferring keeps the at-post
// stabilizer in force (it isn't flipped to the step-out line, since that keys on
// p.Forage != nil), so she finishes the deal; the errand is level-triggered and
// the cue returns the moment the stall is clear.
func buildForage(snap *sim.Snapshot, actorID sim.ActorID, actorSnap *sim.ActorSnapshot, customerEngaged bool) *ForageView {
	if snap == nil || actorSnap == nil || actorSnap.RestockPolicy == nil {
		return nil
	}
	if customerEngaged {
		return nil // don't pull a grower off a live sale to go harvest
	}
	pct := snap.RestockReorderPct
	if pct <= 0 {
		return nil // restock feature disabled
	}
	// A ranged forager (LLM-253) also gets wild UNOWNED sources for low items it
	// owns no bush for — computed inside the same low-item loop so it shares the
	// threshold gate and the customerEngaged deferral above.
	tagged := hasForageRange(actorSnap)
	var items []ForageItemView
	var wild []WildForageItemView
	for _, e := range actorSnap.RestockPolicy.ForageEntries() {
		cap := e.Cap()
		current := actorSnap.Inventory[e.Item]
		if !sim.RestockReorderThresholdMet(current, cap, pct, 0) { // forage stock is not a recipe input — cap fraction only
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
			// Object-kind only: the VillageObjectID cast is sound only for an object
			// ref — a structure ref shares its id with its placement object, so an
			// unchecked cast could resolve a different object. gather:<item> is only
			// ever recorded for object places, so this also guards a future
			// cross-kind affordance vocabulary (code_review).
			if kp == nil || kp.Kind != sim.PlaceKindObject || !kp.HasAffordance(affordance) {
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
			// Owns no bush for this low item. A ranged forager (sim.AttrForageRange,
			// LLM-253) can still be pointed at the nearest ripe UNOWNED wild source
			// for it — the gap the owner-only cue here and the proximity-only at-bush
			// cue both leave open. Untagged: nothing to point at, as before.
			if tagged {
				if wv, ok := nearestWildForageSource(snap, actorSnap, e.Item, current, cap); ok {
					wild = append(wild, wv)
				}
			}
			continue
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
	if len(items) == 0 && len(wild) == 0 {
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
	sort.Slice(wild, func(i, j int) bool {
		if wild[i].ItemLabel != wild[j].ItemLabel {
			return wild[i].ItemLabel < wild[j].ItemLabel
		}
		return wild[i].kind < wild[j].kind
	})
	return &ForageView{Items: items, WildSources: wild}
}

// hasForageRange reports whether the actor carries the sim.AttrForageRange
// capability marker (LLM-253), read from the snapshot's sorted AttributeSlugs
// projection — the same presence-only read as subjectIsWorker (LLM-26).
func hasForageRange(actorSnap *sim.ActorSnapshot) bool {
	if actorSnap == nil {
		return false
	}
	for _, slug := range actorSnap.AttributeSlugs {
		if slug == sim.AttrForageRange {
			return true
		}
	}
	return false
}

// nearestWildForageSource finds the nearest ripe UNOWNED forage-to-sell source for
// item across the whole map (the LLM-253 ranged cue) and renders it as a
// WildForageItemView, or ok=false when no unowned source for the item has stock
// this tick. "Unowned" means OwnerActorID == "" (a commons bush); an owned bush is
// the province of the owned-bush cue and is never surfaced here. Selection uses
// integer squared tile distance — no Sqrt and no float equality in the tie-break;
// the rendered phrase takes Sqrt once. Ordering is identical to the Euclidean
// qualitativeDistance calibration buildSeekWorkPlaces uses. Ties break by ripest
// then lowest id for determinism over unordered map iteration. Requires stock > 0
// so the cue never sends a forager on a long trek to an empty bush — the source
// reappears in a later tick once it regrows. snap/actorSnap non-nil is guaranteed
// by the sole caller (buildForage); guarded anyway for a nil-safe helper.
func nearestWildForageSource(snap *sim.Snapshot, actorSnap *sim.ActorSnapshot, item sim.ItemKind, current, capacity int) (WildForageItemView, bool) {
	if snap == nil || actorSnap == nil {
		return WildForageItemView{}, false
	}
	ax, ay := actorSnap.Pos.X, actorSnap.Pos.Y
	var best *sim.VillageObject
	var bestDist2, bestStock int
	for _, obj := range snap.VillageObjects {
		if obj == nil || obj.OwnerActorID != "" || obj.DisplayName == "" {
			continue // owned, or nameless (no name to render in the cue) — skip
		}
		stock, ok := forageStockForItem(obj, item)
		if !ok || stock <= 0 {
			continue
		}
		objTile := obj.Pos.Tile()
		dx := objTile.X - ax
		dy := objTile.Y - ay
		dist2 := dx*dx + dy*dy
		if best == nil || dist2 < bestDist2 ||
			(dist2 == bestDist2 && stock > bestStock) ||
			(dist2 == bestDist2 && stock == bestStock && obj.ID < best.ID) {
			best, bestDist2, bestStock = obj, dist2, stock
		}
	}
	if best == nil {
		return WildForageItemView{}, false
	}
	objTile := best.Pos.Tile()
	return WildForageItemView{
		ItemLabel:   itemDisplayLabel(snap, item),
		SourceLabel: best.DisplayName,
		CurrentQty:  current,
		Cap:         capacity,
		RipeUnits:   bestStock,
		Distance:    qualitativeDistance(math.Sqrt(float64(bestDist2))),
		Direction:   cardinalDirection(float64(ax), float64(ay), float64(objTile.X), float64(objTile.Y)),
		MoveHandle:  best.ID,
		kind:        item,
	}, true
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
		// IsForageToSellFor is the shared row predicate (finite + yield-only +
		// matching gather item) the forage WARRANT's actionability gate also uses,
		// so the cue and the wake agree on what's a harvestable own-bush (LLM-90).
		// It implies IsFinite (AvailableQuantity != nil), so the deref below is safe.
		if !r.IsForageToSellFor(item) {
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
	if v == nil || (len(v.Items) == 0 && len(v.WildSources) == 0) {
		return
	}
	if len(v.Items) > 0 {
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
				fmt.Fprintf(b, ", %d ripe to pick now. Use move_to with destination \"%s\" to walk out to them.\n",
					it.RipeUnits, it.MoveHandle)
			} else {
				b.WriteString(", none ripe yet — they will regrow, so check back later.\n")
			}
		}
	}
	renderWildForage(b, v.WildSources)
}

// renderWildForage writes the "## Free sources you can gather from" section — the
// LLM-253 ranged cue. Source-agnostic copy (LLM-254): the source's own name carries
// the noun ("The Well is…", "The Sage Bush is…"), so the section reads right for a
// stone well as well as a countryside bush — no "grows wild / ripe to pick" flavor.
// A distinct header from "## Your bushes to harvest" so it never claims the actor
// owns these; move_to ONLY, no `gather` mention (same LLM-59/79 posture as the owned
// cue — gather isn't callable until the forager arrives).
func renderWildForage(b *strings.Builder, sources []WildForageItemView) {
	if len(sources) == 0 {
		return
	}
	b.WriteString("## Free sources you can gather from\n")
	b.WriteString("You know of these unowned sources out in the town and countryside. No one owns them, so you may take freely — walk out to gather when your own stock runs low. You choose how much to take.\n")
	for _, it := range sources {
		headroom := it.Cap - it.CurrentQty
		if headroom < 0 {
			headroom = 0
		}
		// Direction is empty only if the source shares the actor's tile — not a real
		// case for a ranged cue, but fall back to the bare distance phrase then.
		where := it.Distance
		if it.Direction != "" {
			where = it.Distance + " to the " + it.Direction
		}
		fmt.Fprintf(b, "- %s: %d on hand of %d cap (room for %d more). The %s is %s, %d ready to gather now. Use move_to with destination \"%s\" to walk out to it.\n",
			sanitizeInline(it.ItemLabel), it.CurrentQty, it.Cap, headroom,
			sanitizeInline(it.SourceLabel), where, it.RipeUnits, it.MoveHandle)
	}
}
