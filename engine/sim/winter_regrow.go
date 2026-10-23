package sim

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// winter_regrow.go — LLM-848 (seasons epic LLM-834). In winter some gatherables
// regrow more slowly: the regrow period of a refresh row is multiplied by a
// per-gather-item percentage while the village date is in winter (SeasonOf).
//
// The factor is applied where the period is READ — regenRefreshRow for the
// refill and cropStageState for a crop's growth-stage dating — and never written
// into the object_refresh rows. Rewriting refresh_period_hours at the season
// boundary would reset every regrow anchor (a changed period does, through the
// umbilical set-refresh), so every bush would restart its clock on December 1.
//
// Rows are chosen by gather item, not by asset: both rows of an eat-and-pick bush
// (the hunger row and the carry row) yield the same item, and a new asset that
// yields the same item is covered with no change here. A row with no gather item
// (a shade tree's rest row) and a milking animal's row are never scaled.

// DefaultWinterRegrowFactors is the seed for the winter_regrow_factors setting:
// item=percent pairs, 100 meaning unchanged. Jeff 2026-10-23, from the live
// picking rates of the week to 10-23 (LLM-834 comment 15052, LLM-848).
const DefaultWinterRegrowFactors = "apples=125,blueberries=200,carrots=150,raspberries=200,sage=200,wheat=200"

// winterRegrowMaxPct bounds a factor at ten times the period. Higher is not a
// harder winter but a source that never comes back within a season, which is the
// absorbing state the epic rules out.
const winterRegrowMaxPct = 1000

// ParseWinterRegrowFactors parses "item=percent,item=percent". Each percent is a
// whole number from 100 (unchanged) to winterRegrowMaxPct — winter never speeds a
// regrow. Whitespace around items and numbers is ignored; an item named twice is
// an error rather than a silent last-wins. The error is operator-facing.
func ParseWinterRegrowFactors(raw string) (map[ItemKind]int, error) {
	out := map[ItemKind]int{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		item := ItemKind(strings.ToLower(strings.TrimSpace(name)))
		if !ok || item == "" {
			return nil, fmt.Errorf("winter_regrow_factors: %q is not item=percent", part)
		}
		pct, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || pct < 100 || pct > winterRegrowMaxPct {
			return nil, fmt.Errorf("winter_regrow_factors: %q needs a whole percent from 100 to %d", part, winterRegrowMaxPct)
		}
		if _, dup := out[item]; dup {
			return nil, fmt.Errorf("winter_regrow_factors: %q is named twice", item)
		}
		out[item] = pct
	}
	return out, nil
}

// FormatWinterRegrowFactors is ParseWinterRegrowFactors' inverse, sorted by item
// so the stored value and the umbilical read-back are stable.
func FormatWinterRegrowFactors(factors map[ItemKind]int) string {
	items := make([]string, 0, len(factors))
	for item := range factors {
		items = append(items, string(item))
	}
	sort.Strings(items)
	parts := make([]string, len(items))
	for i, item := range items {
		parts[i] = item + "=" + strconv.Itoa(factors[ItemKind(item)])
	}
	return strings.Join(parts, ",")
}

// winterRegrowFactorsSetting is the registry spec for winter_regrow_factors. A
// live write REPLACES the map, never edits it in place, because snapshots share
// the WorldSettings value and with it the map.
func winterRegrowFactorsSetting() SettingSpec {
	return SettingSpec{
		Key: "winter_regrow_factors", Kind: SettingKindString, Effect: SettingEffectImmediate, Persist: true,
		get: func(ws *WorldSettings) string { return FormatWinterRegrowFactors(ws.WinterRegrowFactors) },
		set: func(ws *WorldSettings, raw string) error {
			factors, err := ParseWinterRegrowFactors(raw)
			if err != nil {
				return err
			}
			ws.WinterRegrowFactors = factors
			return nil
		},
	}
}

// regrowPct is the percentage of its stored period a refresh row takes to regrow
// at now: the row's winter factor in winter, 100 otherwise.
func regrowPct(w *World, r *ObjectRefresh, now time.Time) int {
	if w == nil || r == nil || !r.IsGatherable() {
		return 100
	}
	if month, _ := VillageMonthDay(w, now); SeasonOf(month) != SeasonWinter {
		return 100
	}
	if pct, ok := w.Settings.WinterRegrowFactors[ItemKind(strings.ToLower(strings.TrimSpace(string(r.GatherItem))))]; ok && pct > 0 {
		return pct
	}
	return 100
}

// scaledRegrowPeriod is hours×pct/100 as a Duration. ok is false for a
// non-positive input or a product that would overflow time.Duration, which can
// wrap onto a plausible POSITIVE value (the same guard cropStageState carries).
func scaledRegrowPeriod(hours, pct int) (time.Duration, bool) {
	if hours <= 0 || pct <= 0 {
		return 0, false
	}
	if int64(hours) > math.MaxInt64/int64(time.Hour)/int64(pct) {
		return 0, false
	}
	return time.Duration(hours) * time.Hour * time.Duration(pct) / 100, true
}
