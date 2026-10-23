package sim

import (
	"fmt"
	"time"
)

// season.go — LLM-840 (seasons epic LLM-834). The village's time of year.
//
// The village runs on the real clock and believes the year is 1692: its
// calendar date is the real local date in WorldSettings.Location, carried back
// to VillageYear. The messenger news (messenger_news.go) and the court's date
// reading (court/tools.go recordWindow) use the same date. The season follows
// from it by whole months — winter is December to March, the months the village's
// trees stand bare (LLM-848) — so it needs no state of its own and no ticker.

// VillageYear is the year the village believes it is.
const VillageYear = 1692

// Season is the village's time of year.
type Season string

const (
	SeasonWinter Season = "winter"
	SeasonSpring Season = "spring"
	SeasonSummer Season = "summer"
	SeasonAutumn Season = "autumn"
)

// SeasonOf returns the season a calendar month falls in. "" for a month out of
// range (the zero month of a hand-built context included).
func SeasonOf(m time.Month) Season {
	switch m {
	case time.December, time.January, time.February, time.March:
		return SeasonWinter
	case time.April, time.May:
		return SeasonSpring
	case time.June, time.July, time.August:
		return SeasonSummer
	case time.September, time.October, time.November:
		return SeasonAutumn
	}
	return ""
}

// VillageMonthDay is the village's local calendar month and day at the instant
// at. Without a Location (a hand-built world) it reads at in its own zone.
func VillageMonthDay(w *World, at time.Time) (time.Month, int) {
	if loc := w.Settings.Location; loc != nil {
		at = at.In(loc)
	}
	return at.Month(), at.Day()
}

// VillageDateEndsAt is the instant the village date holding at now ends: the
// next local midnight in loc (UTC when loc is nil). time.Date normalizes day+1
// across month and year ends and takes the zone's offset on that day, so a DST
// night comes out 23 or 25 hours long.
func VillageDateEndsAt(now time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, loc)
}

// VillageDateScene is the one sentence every engine-authored prose prompt uses
// to tell the model the date and season, so two surfaces cannot disagree about
// the time of year: "It is October 22, 1692, in the autumn." "" for the zero
// month, so a hand-built context renders no date line.
func VillageDateScene(m time.Month, day int) string {
	season := SeasonOf(m)
	if season == "" || day < 1 {
		return ""
	}
	return fmt.Sprintf("It is %s %d, %d, in the %s.", m, day, VillageYear, season)
}
