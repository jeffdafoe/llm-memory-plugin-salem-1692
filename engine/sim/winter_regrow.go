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
//
// A factor of 0 means NONE in winter (LLM-967e, Jeff 2026-11-13: apples pause
// in winter). Such a row is held empty for the whole of winter rather than slowed
// down: winterDormant picks it out and holdDormantRow, on the regen tick, takes
// whatever stock is left on December 1 and keeps the regrow anchor at now, so in
// April it starts a full fresh period. Because stock is the one predicate behind
// both the art (berry_state.go) and gathering, the tree shows bare and nobody can
// pick it, for the same reason a picked-clean tree refuses mid-week. It is not the
// absorbing state the cap below rules out: the hold ends with winter.

// DefaultWinterRegrowFactors is the seed for the winter_regrow_factors setting:
// item=percent pairs, 100 meaning unchanged, 0 none in winter. Jeff 2026-10-23,
// from the live picking rates of the week to 10-23 (LLM-834 comment 15052,
// LLM-848); apples 0 from 2026-11-13 (LLM-967e).
const DefaultWinterRegrowFactors = "apples=0,blueberries=200,carrots=150,raspberries=200,sage=200,wheat=200"

// winterRegrowMaxPct bounds a factor at ten times the period. Higher is not a
// harder winter but a source that never comes back within a season, which is the
// absorbing state the epic rules out.
const winterRegrowMaxPct = 1000

// ParseWinterRegrowFactors parses "item=percent,item=percent". Each percent is 0
// (none in winter) or a whole number from 100 (unchanged) to winterRegrowMaxPct —
// winter never speeds a regrow. Whitespace around items and numbers is ignored; an item named twice is
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
		if err != nil || (pct != 0 && (pct < 100 || pct > winterRegrowMaxPct)) {
			return nil, fmt.Errorf("winter_regrow_factors: %q needs 0 (none in winter) or a whole percent from 100 to %d", part, winterRegrowMaxPct)
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

// winterFactor is r's winter factor when the village is in winter at now and
// r's gather item is listed; ok is false otherwise (no factor applies).
func winterFactor(w *World, r *ObjectRefresh, now time.Time) (pct int, ok bool) {
	if w == nil || r == nil || !r.IsGatherable() {
		return 0, false
	}
	if month, _ := VillageMonthDay(w, now); SeasonOf(month) != SeasonWinter {
		return 0, false
	}
	pct, ok = w.Settings.WinterRegrowFactors[ItemKind(strings.ToLower(strings.TrimSpace(string(r.GatherItem))))]
	return pct, ok
}

// winterDormant reports whether r takes no stock at all at now: winter, and its
// item's factor is 0.
func winterDormant(w *World, r *ObjectRefresh, now time.Time) bool {
	pct, ok := winterFactor(w, r, now)
	return ok && pct == 0
}

// holdDormantRow is the regen step for a dormant row (winterDormant): a finite
// row loses any stock left on it and its anchor is kept at now, so the regrow
// that follows winter is a full period from the day winter ends rather than one
// backdated to the last picking. Reports whether the stock changed.
func holdDormantRow(r *ObjectRefresh, now time.Time) bool {
	if r == nil || !r.IsFinite() {
		return false
	}
	t := now
	r.LastRefreshAt = &t
	if *r.AvailableQuantity == 0 {
		return false
	}
	zero := 0
	r.AvailableQuantity = &zero
	return true
}

// regrowPct is the percentage of its stored period a refresh row takes to regrow
// at now: the row's winter factor in winter, 100 otherwise. A dormant row's 0
// never reaches a period (regenObjectRefresh holds it instead), so it reads 100
// here too.
func regrowPct(w *World, r *ObjectRefresh, now time.Time) int {
	if pct, ok := winterFactor(w, r, now); ok && pct > 0 {
		return pct
	}
	return 100
}

// scaledRegrowPeriod is hours×pct/100 as a Duration. ok is false for a
// non-positive input or a product that would overflow time.Duration, which can
// wrap onto a plausible POSITIVE value (the same guard cropStageState carries).
//
// It multiplies by a hundredth of an hour rather than dividing by 100 at the
// end, so no intermediate is larger than the result: at pct 100 the longest
// period accepted is the same ~292 years an unscaled period always allowed.
func scaledRegrowPeriod(hours, pct int) (time.Duration, bool) {
	if hours <= 0 || pct <= 0 {
		return 0, false
	}
	const hundredthHour = time.Hour / 100
	if int64(hours) > math.MaxInt64/int64(hundredthHour)/int64(pct) {
		return 0, false
	}
	return time.Duration(hours) * hundredthHour * time.Duration(pct), true
}
