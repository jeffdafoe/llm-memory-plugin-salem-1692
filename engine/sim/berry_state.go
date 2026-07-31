package sim

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// berry_state.go — derived visual state for gatherable / refreshable sources
// (LLM-12, extended for staged crops in LLM-576). Which visual a placed source
// renders is DERIVED from its finite gatherable supply — the same tag-driven
// model the day/night phase flip (world_phase.go) and structure occupancy
// (occupancy.go) use.
//
// Two tag vocabularies, checked in order:
//
//   - CROP (LLM-576): states tagged 'growth-1'..'growth-N'. The HIGHEST-numbered
//     stage is ripe and renders whenever there is stock; the lower stages render
//     while the source is spent, selected by how far the regrow period has run.
//   - BUSH (LLM-12): states tagged 'berries' + 'bare'. Binary — berries whenever
//     there is stock.
//
// Both derive RIPENESS from the same predicate (any finite gatherable row with
// units left), which is what keeps "looks ready" and "can be gathered"
// identical: an unripe crop has no stock, so ResolveGatherSource, the at-source
// cue and the forage move handle all refuse it for the same reason the art shows
// it green. Only the immature stages are time-derived, and they exist purely to
// render — nothing reads them.
//
// Recomputed wherever supply changes, all on the world goroutine:
//   - eating in place — ApplyObjectRefreshAtArrival decrements a finite row;
//   - picking — Gather decrements the shared stock;
//   - regrowth — regenObjectRefresh refills it.
//
// regenObjectRefresh also calls this for EVERY object on its one-minute tick
// regardless of whether supply moved, which is what advances a crop through its
// immature stages without a second sweep.
//
// A real flip emits VillageObjectStateChanged → object_state_changed, so the
// client re-renders berries appearing / vanishing, or wheat greening up.

// Asset-state tags marking the berries / bare visual variants.
const (
	TagBerries = "berries"
	TagBare    = "bare"

	// TagGrowthPrefix marks a staged crop's visual variants — 'growth-1' through
	// 'growth-N', ordered by the trailing integer. N is read from the asset, not
	// fixed here, so a crop with three stages works as well as one with five.
	TagGrowthPrefix = "growth-"
)

// refreshObjectBerryState recomputes obj's supply-derived visual and applies it
// if it changed. No-op unless the object's asset carries a recognised tag set —
// otherwise there's no defined vocabulary to pick from, so the object simply
// doesn't participate (a plain well or a decorative tree is untouched). A real
// flip emits VillageObjectStateChanged via setVillageObjectStateInline.
//
// now dates the crop stage selection; it is unused on the binary bush path.
//
// MUST be called from inside a Command.Fn (reads/writes world maps, emits).
func refreshObjectBerryState(w *World, obj *VillageObject, now time.Time) {
	if w == nil || obj == nil {
		return
	}
	asset, ok := w.Assets[obj.AssetID]
	if !ok || asset == nil {
		return
	}

	row, hasStock := gatherableSupply(obj)

	target := ""
	switch {
	case len(growthStates(asset)) > 0:
		target = cropStageState(asset, row, hasStock, now)
	default:
		berriesState := asset.StateForTag(TagBerries)
		bareState := asset.StateForTag(TagBare)
		if berriesState == nil || bareState == nil {
			return // not state-tracked
		}
		target = bareState.State
		if hasStock {
			target = berriesState.State
		}
	}
	if target == "" || obj.CurrentState == target {
		return
	}
	setVillageObjectStateInline(w, obj, target)
}

// gatherableSupply reports whether obj has pickable stock, and returns the row
// that governs it. Stock is narrower than "any finite row" on purpose — a source
// with a second, non-gatherable finite supply (e.g. sap) must not falsely read as
// ripe. IsFinite guarantees AvailableQuantity != nil, so the deref is safe.
//
// The returned row is the first finite gatherable one, stocked or not: the crop
// stage needs its regrow clock precisely when it is EMPTY, so this cannot return
// only stocked rows.
func gatherableSupply(obj *VillageObject) (row *ObjectRefresh, hasStock bool) {
	for _, r := range obj.Refreshes {
		if r == nil || !r.IsFinite() || !r.IsGatherable() {
			continue
		}
		if row == nil {
			row = r
		}
		if *r.AvailableQuantity > 0 {
			return r, true
		}
	}
	return row, false
}

// growthState pairs a crop stage's ordinal with the state name that renders it.
type growthState struct {
	stage int
	state string
}

// growthStates returns asset's crop stages ordered by their trailing integer,
// or nil when the asset carries none (every non-crop asset, so this is also the
// "is this a crop?" test). A malformed or non-positive suffix is skipped rather
// than defaulting to a stage number, so a typo drops one variant instead of
// silently colliding with a real stage.
func growthStates(asset *Asset) []growthState {
	var out []growthState
	for i := range asset.States {
		for _, t := range asset.States[i].Tags {
			if !strings.HasPrefix(t, TagGrowthPrefix) {
				continue
			}
			n, err := strconv.Atoi(strings.TrimPrefix(t, TagGrowthPrefix))
			if err != nil || n < 1 {
				break
			}
			out = append(out, growthState{stage: n, state: asset.States[i].State})
			break
		}
	}
	// Ordinal first, state name as a deterministic tie-break for two states
	// sharing a stage number (authoring error — pick one, don't flap).
	sort.Slice(out, func(i, j int) bool {
		if out[i].stage != out[j].stage {
			return out[i].stage < out[j].stage
		}
		return out[i].state < out[j].state
	})
	return out
}

// cropStageState picks which growth stage a crop renders.
//
// Ripe (the highest stage) whenever there is stock — so a plant seeded full by
// asset_refresh_default lands ripe the moment it is placed, and a plant stays
// ripe until someone actually harvests it rather than aging out from under the
// gather cue.
//
// Spent plants walk the lower stages across the regrow period: with N stages the
// N-1 immature ones each hold period/(N-1), so the plant is visibly further along
// each time the period is a stage older. Returns "" when there is nothing sane to
// render (no stages, or a spent plant on a row with no regrow clock — leave the
// visual alone rather than snapping it to a stage the clock can't justify).
func cropStageState(asset *Asset, row *ObjectRefresh, hasStock bool, now time.Time) string {
	stages := growthStates(asset)
	if len(stages) == 0 {
		return ""
	}
	ripe := stages[len(stages)-1].state
	if hasStock {
		return ripe
	}
	immature := stages[:len(stages)-1]
	if len(immature) == 0 {
		return ripe // single-stage crop: nothing to show but the one variant
	}
	// A spent plant with no anchor has not started regrowing that we can date
	// (freshly loaded, or a hand-built row) — show it just cut.
	if row == nil || row.LastRefreshAt == nil || row.RefreshPeriodHours == nil || *row.RefreshPeriodHours <= 0 {
		return immature[0].state
	}
	period := time.Duration(*row.RefreshPeriodHours) * time.Hour
	elapsed := now.Sub(*row.LastRefreshAt)
	if elapsed < 0 {
		elapsed = 0 // clock skew / a future anchor: treat as just cut
	}
	// Integer arithmetic on the ratio, so a long-overdue plant (elapsed past the
	// period, e.g. regen hasn't run yet after an outage) clamps to the last
	// immature stage instead of indexing past the slice.
	idx := int(elapsed * time.Duration(len(immature)) / period)
	if idx >= len(immature) {
		idx = len(immature) - 1
	}
	return immature[idx].state
}
