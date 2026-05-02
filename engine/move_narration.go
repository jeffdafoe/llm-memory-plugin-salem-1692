package main

// Departure narration shared between the live broadcast path
// (agent_tick.go's move_to commit) and the talk-panel backload
// (pc_handlers.go's loadRecentSpeechAtStructure). Both paths reconstruct
// the same line — the one shown to the player as "X left for Y." — so
// any normalization happens here once.
//
// Two normalizations applied to self-references:
//
//   - "my home" / "my house" / "back home" / "go home" → "home"
//     "my work" / "my shop" / "go to work" / "the workplace" → "work"
//
//     The dest string is whatever the LLM typed. The model often
//     prefixes "my" — fine in tool input, awkward in third-person
//     narration ("John Ellis left for my home" reads like the model
//     speaking, not the narrator).
//
//   - When the speaker's home and work are the same structure (a
//     tavernkeeper who lives where they work, etc.), going home isn't
//     really a "leaving for somewhere" event — they were already
//     there. Render it as "X retired for the {evening|afternoon|
//     morning|night}" instead. The day-band comes from the world's
//     local clock against the configured dawn/dusk times.

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var moveDeparturePhrasesHome = map[string]bool{
	"home": true, "my home": true, "back home": true,
	"house": true, "my house": true, "go home": true,
}

var moveDeparturePhrasesWork = map[string]bool{
	"work": true, "my work": true, "the shop": true,
	"my shop": true, "go to work": true, "the workplace": true,
	"my workplace": true,
}

// narrateMoveDeparture builds the "X left for Y" line emitted on
// move_to commit and on talk-panel backload. See file-level doc for
// the normalizations applied.
func (app *App) narrateMoveDeparture(ctx context.Context, speakerName string,
	homeStructureID, workStructureID sql.NullString, dest string) string {
	d := strings.ToLower(strings.TrimSpace(dest))
	if moveDeparturePhrasesHome[d] {
		if homeStructureID.Valid && workStructureID.Valid &&
			homeStructureID.String != "" &&
			homeStructureID.String == workStructureID.String {
			return fmt.Sprintf("%s retired for the %s.", speakerName, app.currentDayBand(ctx))
		}
		return fmt.Sprintf("%s left for home.", speakerName)
	}
	if moveDeparturePhrasesWork[d] {
		return fmt.Sprintf("%s left for work.", speakerName)
	}
	return fmt.Sprintf("%s left for %s.", speakerName, dest)
}

// currentDayBand returns "morning"/"afternoon"/"evening"/"night" based
// on the world's local clock vs configured dawn/dusk. Used by the
// "retired for the X" branch so the line reads naturally for the
// time-of-day. Defaults to "evening" if the world config can't be
// loaded — that's the most common context for "retired" anyway.
//
// Buckets:
//   midnight → dawn:    night
//   dawn → noon:        morning
//   noon → dusk:        afternoon
//   dusk → midnight:    evening
func (app *App) currentDayBand(ctx context.Context) string {
	cfg, err := app.loadWorldConfig(ctx)
	if err != nil || cfg == nil || cfg.Location == nil {
		return "evening"
	}
	now := time.Now().In(cfg.Location)
	h := now.Hour()
	dawnH := parseHourPrefix(cfg.DawnTime, 7)
	duskH := parseHourPrefix(cfg.DuskTime, 19)
	switch {
	case h >= duskH:
		return "evening"
	case h >= 12:
		return "afternoon"
	case h >= dawnH:
		return "morning"
	default:
		return "night"
	}
}

// parseHourPrefix extracts the hour from an "HH:MM" string. Returns
// the fallback on any parse failure so a malformed setting doesn't
// crash the day-band lookup.
func parseHourPrefix(s string, fallback int) int {
	parts := strings.Split(s, ":")
	if len(parts) == 0 {
		return fallback
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return fallback
	}
	return h
}
