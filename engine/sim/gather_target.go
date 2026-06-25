package sim

// gather_target.go — LLM-93. The shared gather-source resolution used by BOTH the
// gather command (findGatherableObjectNear → Gather / StartHarvest) and the at-bush
// perception cue (findGatherableCue), so cue and command never disagree on which
// bush an actor harvests.
//
// The bug it fixes: a grower's raspberry + blueberry bushes are packed at adjacent
// tiles (one loiter radius), and the old resolution picked the single NEAREST object
// (resolveLoiteringObject, tie-break lowest id) — supply-blind and item-blind, and
// it did NOT skip past a depleted/wrong bush to a usable one nearby. So she resolved
// a zeroed bush ("the source is depleted right now") or a blueberry bush (over-
// harvesting blueberries) instead of the ripe raspberry the forage cue pointed her
// to. This resolution honors the bush she WALKED to first, then falls back to a
// supply/item-aware pick among the co-located candidates.
//
// resolveLoiteringObject (structure_anchors.go) is deliberately left untouched —
// its other consumers (arrival-eat, dwell credit, closed-business, attribution)
// keep the single-nearest "one object owns the tile" semantics.

// GatherCandidate is one gatherable village object within an actor's loiter reach,
// scored for selection. Pin/distance (Cheb) is computed by the caller, since the
// command and the perception cue derive the loiter pin slightly differently.
type GatherCandidate struct {
	ID       VillageObjectID
	Cheb     int  // Chebyshev distance from the actor to the object's loiter pin
	Mine     bool // the actor may harvest it — owned by the actor or unowned commons
	HasStock bool // the gatherable row has stock to give (finite >0, or infinite)
	Low      bool // its gather item is one the actor is restocking (below threshold)
}

// BetterGatherCandidate reports whether a should be chosen over b for a gather.
// Tiers, in order:
//  1. the explicit walked-to target WITH stock — honor where she went (LLM-93),
//     but only if it can actually give (else fall through so a depleted target
//     doesn't trump an adjacent ripe bush);
//  2. ownable (mine/commons) over owned-by-other — preserves the owner-gate: the
//     command's post-check still raises ErrNotYourSource when only another's bush
//     is in range;
//  3. has stock over depleted — skip the zeroed bush;
//  4. a restock (low) item over a not-needed one — go for what she's short on,
//     not the nearer blueberry she's already over-capped on;
//  5. nearer; then 6. lower id (deterministic).
//
// targetID is the actor's GatherTargetObjectID ("" = none).
func BetterGatherCandidate(a, b GatherCandidate, targetID VillageObjectID) bool {
	aTarget := targetID != "" && a.ID == targetID && a.HasStock
	bTarget := targetID != "" && b.ID == targetID && b.HasStock
	if aTarget != bTarget {
		return aTarget
	}
	if a.Mine != b.Mine {
		return a.Mine
	}
	if a.HasStock != b.HasStock {
		return a.HasStock
	}
	if a.Low != b.Low {
		return a.Low
	}
	if a.Cheb != b.Cheb {
		return a.Cheb < b.Cheb
	}
	return a.ID < b.ID
}

// FirstGatherableRow returns obj's first gatherable refresh row, whether that row
// has stock to give (a finite row with AvailableQuantity > 0, or any infinite
// row), and ok=false when obj carries no gatherable row at all. The single source
// of truth for "is this a gather source, and does it have anything left" shared by
// the command and the cue.
func FirstGatherableRow(obj *VillageObject) (row *ObjectRefresh, hasStock bool, ok bool) {
	if obj == nil {
		return nil, false, false
	}
	for _, r := range obj.Refreshes {
		if r.IsGatherable() {
			return r, r.HasStock(), true
		}
	}
	return nil, false, false
}

// ResolveGatherSource is THE shared gather-source resolution — the gather command
// (findGatherableObjectNear) and the perception cue (findGatherableCue) both call
// it, so they can never disagree on which bush / item (the whole point of LLM-93).
// objects/assets are the world's (live) or the snapshot's; passing both keeps the
// loiter-pin math (computeLoiterTile) identical on both sides.
//
// It keeps the single-nearest GATE: resolveLoiteringObject considers every
// displayable placed object (not just gatherable ones), so a closer non-gatherable
// object, or a nearest bush owned by another, still BLOCKS — those are handed back
// (owned-by-other → object returned so the caller raises ErrNotYourSource) or
// suppressed (non-gatherable → nil → ErrNoGatherSource), never skipped past to a
// farther source. That preserves the deliberate resolve-then-check decisions
// (TestGather_NearestOwned_RejectsDespiteFartherCommons, the closer-non-gatherable
// cue case).
//
// What it WIDENS: once the nearest is established to be a bush the actor MAY harvest
// (their own or unowned commons), the choice among the co-located harvestable
// (owned-ok) bushes is ranked — the one walked to first (targetID), then stocked
// over depleted, then a restock item over a not-needed one (BetterGatherCandidate).
// So in a dense interleaved plot a depleted or wrong-item nearest yields to a ripe
// sibling. lowItems is the actor's below-threshold forage set (LowForageItems).
func ResolveGatherSource(objects map[VillageObjectID]*VillageObject, assets map[AssetID]*Asset, actorTile TilePos, actorID ActorID, targetID VillageObjectID, lowItems map[ItemKind]bool) (VillageObjectID, *VillageObject, *ObjectRefresh) {
	nearestID, ok := ResolveLoiteringObject(objects, assets, actorTile, LoiterAttributionTiles)
	if !ok {
		return "", nil, nil
	}
	nearest := objects[nearestID]
	if nearest == nil {
		return "", nil, nil
	}
	nearestRow, nearestStock, gatherable := FirstGatherableRow(nearest)
	if !gatherable {
		// A non-gatherable object owns the tile — don't skip past it.
		return "", nil, nil
	}
	if nearest.OwnedByOther(actorID) {
		// Another's bush owns the tile — hand it back so the caller raises
		// ErrNotYourSource; do NOT fall through to a farther commons source.
		return nearestID, nearest, nearestRow
	}
	bestID, bestObj, bestRow := nearestID, nearest, nearestRow
	best := GatherCandidate{ID: nearestID, Cheb: loiterChebIn(nearest, assets, actorTile), Mine: true, HasStock: nearestStock, Low: lowItems[nearestRow.GatherItem]}
	for id, obj := range objects {
		if id == nearestID || obj == nil || obj.DisplayName == "" || obj.OwnedByOther(actorID) {
			continue
		}
		asset, ok := assets[obj.AssetID]
		if !ok || asset == nil {
			continue
		}
		cheb := computeLoiterTile(obj, asset).Chebyshev(actorTile)
		if cheb > LoiterAttributionTiles {
			continue
		}
		row, hasStock, ok := FirstGatherableRow(obj)
		if !ok {
			continue
		}
		cand := GatherCandidate{ID: id, Cheb: cheb, Mine: true, HasStock: hasStock, Low: lowItems[row.GatherItem]}
		if BetterGatherCandidate(cand, best, targetID) {
			bestID, bestObj, bestRow, best = id, obj, row, cand
		}
	}
	return bestID, bestObj, bestRow
}

// loiterChebIn is the Chebyshev distance from actorTile to obj's loiter pin, or a
// large sentinel when obj has no known asset (so it never wins a tie). Seeds the
// nearest candidate's distance for the own-plot ranking in ResolveGatherSource.
func loiterChebIn(obj *VillageObject, assets map[AssetID]*Asset, actorTile TilePos) int {
	asset, ok := assets[obj.AssetID]
	if !ok || asset == nil {
		return 1 << 30
	}
	return computeLoiterTile(obj, asset).Chebyshev(actorTile)
}

// LowForageItems returns the set of items the actor restocks by foraging and is
// currently below the reorder threshold on — the item bias the fallback resolution
// uses to prefer the bush she's short on. Empty when the policy has no forage
// entries, the feature is off (pct <= 0), or nothing is low.
func LowForageItems(policy *RestockPolicy, inventory map[ItemKind]int, pct int) map[ItemKind]bool {
	if policy == nil || pct <= 0 {
		return nil
	}
	var low map[ItemKind]bool
	for _, e := range policy.ForageEntries() {
		if RestockReorderThresholdMet(inventory[e.Item], e.Cap(), pct) {
			if low == nil {
				low = make(map[ItemKind]bool)
			}
			low[e.Item] = true
		}
	}
	return low
}

// handleGatherTargetOnArrival stamps the village object an agent NPC deliberately
// walked to as its gather target, so a later gather / StartHarvest prefers it over
// the nearest bush (LLM-93). Set from ActorArrived.DestObjectID; an arrival at a
// structure or a bare position carries an empty DestObjectID, which clears a stale
// bush target. Non-agent arrivals are ignored (PCs drive their own gather verb).
func handleGatherTargetOnArrival(w *World, evt Event) {
	arr, ok := evt.(*ActorArrived)
	if !ok {
		return
	}
	a := w.Actors[arr.ActorID]
	if a == nil || !isAgentNPC(a) {
		return
	}
	a.GatherTargetObjectID = arr.DestObjectID
}

// RegisterGatherTargetSubscriber wires the gather-target capture subscriber. Call
// before World.Run or from inside a Command (world-goroutine-safe). Mirrors
// RegisterClosedBusinessSubscriber — another ActorArrived subscriber (LLM-93).
func RegisterGatherTargetSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterGatherTargetSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleGatherTargetOnArrival))
}
