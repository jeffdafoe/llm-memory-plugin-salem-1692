package sim

import (
	"strings"
	"time"
)

// season_art.go — LLM-849 (seasons epic LLM-834, slice 1c). Which season's art a
// tree, bush or forest prop shows.
//
// An asset carries its season art as extra states tagged `season-<look>`
// (migration LLM-849-season-art-states): autumn-clean / autumn-leaves /
// autumn-bare on the big trees, a single `autumn` on the small forest sheets,
// winter-clean / winter-leaves / winter-snowy-top / winter-snowy in winter. A
// state with no season tag is the summer look. Spring is summer art, so there is
// no spring look.
//
// Each month (and, later, the snow cover) gives a look CHAIN, most wanted first.
// An object shows the first look in the chain its asset has art for. The chain is
// what carries the per-asset rules without a per-asset table: a bush has no
// winter states, so in winter its chain falls through to its autumn art, and a
// 16x16 prop with only clean / snowy winter sheets falls from `winter-leaves` to
// `winter-clean`.
//
// The pick runs in refreshObjectBerryState, on the one-minute regen sweep, so
// boot, the date change, a new placement and (with the snow cover) a tier change
// all show within a minute, with no ticker of its own. A stock-tracked bush
// picks the stock half (berries / bare) and the season half together.

// TagSeasonPrefix marks a season art state: `season-autumn-leaves`, ...
const TagSeasonPrefix = "season-"

// The season looks. lookSummer is never stored as a tag — an untagged state is
// summer art.
const (
	lookSummer         = "summer"
	lookAutumn         = "autumn"
	lookAutumnClean    = "autumn-clean"
	lookAutumnLeaves   = "autumn-leaves"
	lookAutumnBare     = "autumn-bare"
	lookWinter         = "winter"
	lookWinterClean    = "winter-clean"
	lookWinterLeaves   = "winter-leaves"
	lookWinterSnowyTop = "winter-snowy-top"
	lookWinterSnowy    = "winter-snowy"
)

// SnowTier is how much snow lies on the village. The snow cover that sets it is
// LLM-850; until then the art is always picked at SnowTierNone.
type SnowTier int

const (
	SnowTierNone SnowTier = iota
	SnowTierDusting
	SnowTierLight
	SnowTierDeep
)

// seasonLookChain is the looks an object may show for month and tier, most
// wanted first, per Jeff's month table (LLM-834 comment 15054): leafy trees
// turn in September, are in full colour with leaves falling in October and bare
// in November; December has dry leaves at the foot, January to March clean
// branches. Snow puts the snowy looks in front: a dusting shows snow on the
// treetops where the art has it, any more the full snowy art.
//
// Every chain ends in summer, so an asset with no art for the month keeps its
// summer look rather than dropping out.
func seasonLookChain(m time.Month, tier SnowTier) []string {
	var base []string
	switch m {
	case time.September:
		base = []string{lookAutumnClean, lookAutumn, lookSummer}
	case time.October:
		base = []string{lookAutumnLeaves, lookAutumn, lookSummer}
	case time.November:
		base = []string{lookAutumnBare, lookAutumn, lookSummer}
	case time.December:
		base = []string{lookWinterLeaves, lookWinterClean, lookWinter, lookAutumn, lookSummer}
	case time.January, time.February, time.March:
		base = []string{lookWinterClean, lookWinterLeaves, lookWinter, lookAutumn, lookSummer}
	default:
		base = []string{lookSummer}
	}
	switch tier {
	case SnowTierDusting:
		return append([]string{lookWinterSnowyTop, lookWinterSnowy}, base...)
	case SnowTierLight, SnowTierDeep:
		return append([]string{lookWinterSnowy}, base...)
	}
	return base
}

// stateLook is the season look a state renders: its season tag's suffix, or
// summer for a state with none.
func stateLook(s *AssetState) string {
	for _, t := range s.Tags {
		if look, ok := strings.CutPrefix(t, TagSeasonPrefix); ok && look != "" {
			return look
		}
	}
	return lookSummer
}

// hasSeasonArt reports whether any of asset's states is season art. An asset
// without it (a house, a pine, a crop) never takes part in the season pick.
func hasSeasonArt(asset *Asset) bool {
	for i := range asset.States {
		if stateLook(&asset.States[i]) != lookSummer {
			return true
		}
	}
	return false
}

// seasonState is the state asset shows for chain: the first look in the chain
// that has a state carrying stockTag (any state when stockTag is empty). Within a
// look the asset's default state wins, then the lowest id, so the pick is stable.
// "" when no state matches at all (a bush whose stock tag has no state).
func seasonState(asset *Asset, stockTag string, chain []string) string {
	for _, look := range chain {
		var best *AssetState
		for i := range asset.States {
			s := &asset.States[i]
			if stockTag != "" && !s.HasTag(stockTag) {
				continue
			}
			if stateLook(s) != look {
				continue
			}
			if s.State == asset.DefaultState {
				return s.State
			}
			if best == nil || s.ID < best.ID {
				best = s
			}
		}
		if best != nil {
			return best.State
		}
	}
	return ""
}
