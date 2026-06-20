package sim

// berry_state.go — derived berries/bare visual state for gatherable /
// refreshable bushes (LLM-12). A "bush" asset has two tagged AssetStates:
// 'berries' (stock available) and 'bare' (picked clean). Which one a placed
// bush renders is DERIVED from the object's finite refresh supply — the same
// tag-driven model the day/night phase flip (world_phase.go) and structure
// occupancy (occupancy.go) use.
//
// Derived flag:
//
//	berries = (any finite refresh row on the object has AvailableQuantity > 0)
//
// Recomputed wherever that supply changes, all on the world goroutine:
//   - eating in place — ApplyObjectRefreshAtArrival decrements a finite row;
//   - picking — Gather decrements the shared stock;
//   - regrowth — regenObjectRefresh refills it.
//
// A real flip emits VillageObjectStateChanged → object_state_changed, so the
// client re-renders berries appearing / vanishing on the bush.

// Asset-state tags marking the berries / bare visual variants.
const (
	TagBerries = "berries"
	TagBare    = "bare"
)

// refreshObjectBerryState recomputes the berries/bare visual for obj from its
// finite refresh supply and applies it if it changed. No-op unless the object's
// asset carries BOTH a 'berries'- and a 'bare'-tagged state — otherwise there's
// no defined pair to toggle between, so the object simply doesn't participate (a
// plain well or a decorative tree is untouched). A real flip emits
// VillageObjectStateChanged via setVillageObjectStateInline.
//
// MUST be called from inside a Command.Fn (reads/writes world maps, emits).
func refreshObjectBerryState(w *World, obj *VillageObject) {
	asset, ok := w.Assets[obj.AssetID]
	if !ok {
		return
	}
	berriesState := asset.StateForTag(TagBerries)
	bareState := asset.StateForTag(TagBare)
	if berriesState == nil || bareState == nil {
		return // not berry-state-tracked
	}

	hasStock := false
	for _, r := range obj.Refreshes {
		if r.IsFinite() && *r.AvailableQuantity > 0 {
			hasStock = true
			break
		}
	}

	target := bareState.State
	if hasStock {
		target = berriesState.State
	}
	if obj.CurrentState == target {
		return
	}
	setVillageObjectStateInline(w, obj, target)
}
