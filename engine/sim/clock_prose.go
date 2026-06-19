package sim

import "fmt"

// clock_prose.go — ClockHourProse (LLM-40).
//
// The numeric-but-in-period companion to the perception header's felt
// time-of-day bands (perception.timeOfDayProse). Used where the model needs an
// actual hour it can act on — a keeper's shift-close time on the at-post cue,
// and the stay_open no-op reject — not just a felt sense of the hour. Lives in
// sim (not perception) so both the perception render layer and the stay_open
// command, which voice the same close time, share one formatter and one voice.

// ClockHourProse renders a village wall-clock minute-of-day (0–1439) as a
// period-voiced clock time for in-world prose — e.g. 1260 → "9 in the evening",
// 720 → "noon", 0 → "midnight", 1290 → "9:30 in the evening". Out-of-range
// input returns "" so callers omit the clause rather than render a misleading
// time.
func ClockHourProse(minuteOfDay int) string {
	if minuteOfDay < 0 || minuteOfDay > 1439 {
		return ""
	}
	hour := minuteOfDay / 60
	minute := minuteOfDay % 60
	if minute == 0 {
		switch hour {
		case 0:
			return "midnight"
		case 12:
			return "noon"
		}
	}
	h12 := hour % 12
	if h12 == 0 {
		h12 = 12
	}
	band := clockBand(hour)
	if minute == 0 {
		return fmt.Sprintf("%d %s", h12, band)
	}
	return fmt.Sprintf("%d:%02d %s", h12, minute, band)
}

// clockBand maps a 24-hour hour to its in-world period phrase. Bands chosen to
// match common speech: pre-noon "morning", through 4pm "afternoon", through 9pm
// "evening", later "night". (Exact noon/midnight are handled by their own words
// in ClockHourProse before this is consulted.)
func clockBand(hour int) string {
	switch {
	case hour < 12:
		return "in the morning"
	case hour < 17:
		return "in the afternoon"
	case hour < 22:
		return "in the evening"
	default:
		return "at night"
	}
}
