package main

// NPC needs ticking — hunger, thirst, tiredness.
//
// Renamed from "attribute" to "needs" in ZBBS-095 to free the
// "attribute" namespace for the chip/role system introduced in
// ZBBS-096. Mechanically nothing changed; the old setting keys
// (attribute_tick_amount / last_attribute_tick_at) were renamed
// in lockstep.
//
// Each villager carries three needs (village_agent.hunger / thirst /
// tiredness, SMALLINT 0–24, higher = more in need). They grow with simulated
// time and drop when an NPC consumes (eating drops hunger, drinking drops
// thirst). Tiredness has no consumption mechanic yet — sleep handling comes
// later.
//
// Cadence: a single hourly batch UPDATE rather than per-NPC stamps. The
// per-minute server tick handler (dispatchNeedsTick, registered in
// runServerTickOnce) reads last_needs_tick_at (RFC3339 timestamp),
// computes how many full hours have elapsed since, and runs the batch
// when at least one hour has rolled.
//
// Catch-up: under multi-restart days, hours that crossed during downtime
// were silently lost in the old hour-of-day storage. The timestamp
// storage lets us increment by `amount * hoursElapsed` in a single
// UPDATE — capped at maxNeedsCatchupHours to keep a long outage
// from shock-spiking everyone to peak need on the first tick after
// recovery.
//
// First-run behavior: when last_needs_tick_at is NULL (fresh
// migration or first deploy after ZBBS-088), the handler stamps the
// current hour boundary without incrementing. Avoids a deploy-time
// pulse where every villager gets +1 hunger the instant the migration
// runs. The next hour boundary fires the first real tick.
//
// Magnitude is governed by the needs_tick_amount setting (default 1).
// All needs share the same per-tick magnitude — distinguishing between
// (e.g.) faster hunger and slower thirst is a future tuning question;
// today they all march at the same rate.

import (
	"context"
	"log"
	"strconv"
	"time"
)

const (
	// Hard ceiling on every need. Matches the SMALLINT scale documented
	// in ZBBS-082 and the LEAST clamp in the UPDATE. Kept as a constant
	// so the prompt-side narration can reference the same number.
	needMax = 24

	// Cap on catch-up increments after a long downtime. Prevents a
	// 30-hour outage from spiking every NPC to peak hunger/thirst/
	// tiredness in a single dispatch — past the cap, those hours
	// simply don't accrue, same as the old design's behavior for any
	// gap. Twelve hours is half a day: enough headroom for a normal
	// deploy-heavy session (multiple restarts spanning a few hours)
	// without making a real outage produce a worst-case stampede.
	maxNeedsCatchupHours = 12
)

// dispatchNeedsTick is registered in runServerTickOnce. Cheap when the
// hour hasn't rolled over (one setting read + integer compare); does a
// single UPDATE across all villagers when it has.
//
// The whole read-decide-update-stamp is wrapped in a transaction with
// SELECT FOR UPDATE on the setting row. runServerTickOnce is single-
// goroutine within one process, so concurrent firing isn't possible
// today, but the row lock makes this safe if the engine is ever scaled
// to multiple instances against the same DB. Cost is one extra round-
// trip per tick; cheap enough to be worth the future-proofing.
func (app *App) dispatchNeedsTick(ctx context.Context) {
	now := time.Now().UTC()
	hourBoundary := now.Truncate(time.Hour)
	hourBoundaryStr := hourBoundary.Format(time.RFC3339)

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		log.Printf("needs_tick: begin tx: %v", err)
		return
	}
	defer tx.Rollback(ctx) // safe after commit (no-op)

	// Lock the setting row up front. If another instance crossed the same
	// boundary first, by the time we acquire the lock we'll see their
	// stamp and skip. COALESCE handles a NULL value (fresh migration or
	// first deploy after ZBBS-088).
	var lastAtStr string
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(value, '') FROM setting WHERE key = 'last_needs_tick_at' FOR UPDATE`,
	).Scan(&lastAtStr)
	if err != nil {
		log.Printf("needs_tick: lock setting row: %v", err)
		return
	}

	if lastAtStr == "" {
		// First run after migration. Stamp the current hour boundary
		// without incrementing so the next hour rolls the first real
		// tick. Avoids a deploy-time pulse where every villager gets
		// +1 hunger the instant the migration runs.
		if _, err := tx.Exec(ctx,
			`UPDATE setting SET value = $1 WHERE key = 'last_needs_tick_at'`,
			hourBoundaryStr,
		); err != nil {
			log.Printf("needs_tick: stamp first-run timestamp: %v", err)
			return
		}
		_ = tx.Commit(ctx)
		return
	}

	lastAt, err := time.Parse(time.RFC3339, lastAtStr)
	if err != nil {
		// Corrupted value. Reset to the current hour boundary and skip
		// the tick. Next hour roll will fire normally; the lost interval
		// is the price of avoiding a flood of retries every minute.
		log.Printf("needs_tick: bad last_needs_tick_at %q (resetting): %v", lastAtStr, err)
		_, _ = tx.Exec(ctx,
			`UPDATE setting SET value = $1 WHERE key = 'last_needs_tick_at'`,
			hourBoundaryStr,
		)
		_ = tx.Commit(ctx)
		return
	}

	// Normalize to UTC + hour boundary before computing elapsed. We
	// always WRITE truncated values, but the setting row is operator-
	// editable and parses any RFC3339 (including non-UTC offsets and
	// sub-hour timestamps). Truncating defensively here means a hand-
	// edited 12:30 doesn't masquerade as zero hours elapsed at 13:00.
	lastAt = lastAt.UTC().Truncate(time.Hour)

	hoursElapsed := int(hourBoundary.Sub(lastAt) / time.Hour)
	if hoursElapsed <= 0 {
		return
	}

	cappedHours := hoursElapsed
	if cappedHours > maxNeedsCatchupHours {
		log.Printf("needs_tick: %d hours since last tick exceeds cap (%d) — applying capped catch-up only",
			hoursElapsed, maxNeedsCatchupHours)
		cappedHours = maxNeedsCatchupHours
	}

	// Read the per-tick magnitude. Non-locking — operator edit racing
	// with this read just means the new magnitude lands next tick.
	amountStr := app.loadSetting(ctx, "needs_tick_amount", "1")
	amount, err := strconv.Atoi(amountStr)
	if err != nil || amount <= 0 {
		log.Printf("needs_tick: bad needs_tick_amount %q (skipping tick): %v", amountStr, err)
		// Stamp the hour so we don't keep retrying every minute. Operator
		// fixes the setting and the next hour boundary picks up the new
		// magnitude.
		_, _ = tx.Exec(ctx,
			`UPDATE setting SET value = $1 WHERE key = 'last_needs_tick_at'`,
			hourBoundaryStr,
		)
		_ = tx.Commit(ctx)
		return
	}

	totalIncrement := amount * cappedHours

	tag, err := tx.Exec(ctx, `
		UPDATE actor SET
			hunger    = LEAST($1::int, hunger    + $2::int),
			thirst    = LEAST($1::int, thirst    + $2::int),
			tiredness = LEAST($1::int, tiredness + $2::int)
	`, needMax, totalIncrement)
	if err != nil {
		log.Printf("needs_tick: UPDATE failed (rolling back, will retry next minute): %v", err)
		return
	}

	if _, err := tx.Exec(ctx,
		`UPDATE setting SET value = $1 WHERE key = 'last_needs_tick_at'`,
		hourBoundaryStr,
	); err != nil {
		log.Printf("needs_tick: stamp timestamp failed (rolling back, will retry next minute): %v", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("needs_tick: commit failed: %v", err)
		return
	}

	log.Printf("needs_tick: %d hour(s) elapsed, applying %d capped hour(s) (last %s -> now %s), +%d to %d villagers",
		hoursElapsed, cappedHours, lastAt.Format(time.RFC3339), hourBoundaryStr, totalIncrement, tag.RowsAffected())
}

// loadNeedMagnitude returns the configured drop magnitude for a given
// consumption kind (food / drink). Used by executePay when a transaction
// resolves to a specific need to reduce. Falls back to needMax (full
// reset) on any read error so a misconfigured setting doesn't strand a
// hungry NPC unfed.
func (app *App) loadNeedMagnitude(ctx context.Context, key string) int {
	v := app.loadSetting(ctx, key, "")
	if v == "" {
		return needMax
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		log.Printf("needs: bad %s magnitude %q (using max): %v", key, v, err)
		return needMax
	}
	return n
}

// Default thresholds used when the corresponding setting row is missing.
// Match the values seeded by ZBBS-083 so absent-config behavior matches
// freshly-migrated behavior.
const (
	defaultHungerRedThreshold    = 18
	defaultThirstRedThreshold    = 12
	defaultTirednessRedThreshold = 20
)

// loadIntSetting reads a setting key as an int, falling back to def when
// missing, NULL, or unparseable. Different defaults per key, hence not
// folded into loadSetting itself. Does NOT validate range — callers that
// need a bounded value should use loadNeedThreshold or
// loadNonNegativeIntSetting instead.
func (app *App) loadIntSetting(ctx context.Context, key string, def int) int {
	v := app.loadSetting(ctx, key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("needs: bad int setting %s=%q (using default %d): %v", key, v, def, err)
		return def
	}
	return n
}

// loadNeedThreshold reads a need-threshold setting and clamps it into the
// valid band [8, needMax]. A threshold below 8 would make every NPC
// red regardless of state (collides with the silent floor in needLabel);
// a threshold above needMax would mean the red label never fires
// (peak still would, but mild→red ordering would invert). Out-of-range
// values fall back to the supplied default.
func (app *App) loadNeedThreshold(ctx context.Context, key string, def int) int {
	n := app.loadIntSetting(ctx, key, def)
	if n < 8 || n > needMax {
		log.Printf("needs: out-of-range need threshold %s=%d (using default %d)", key, n, def)
		return def
	}
	return n
}

// loadNonNegativeIntSetting clamps to >= 0. Used for the dispatch ceiling,
// where a negative value would make the >= comparison true immediately and
// reject every attend_to call.
func (app *App) loadNonNegativeIntSetting(ctx context.Context, key string, def int) int {
	n := app.loadIntSetting(ctx, key, def)
	if n < 0 {
		log.Printf("needs: negative int setting %s=%d (using default %d)", key, n, def)
		return def
	}
	return n
}

// needLabel returns the period-appropriate descriptor for a need value
// against its red threshold. Three intensity bands plus a silent band:
//
//   0 to 7                — "" (NPC isn't aware of the need; not surfaced)
//   8 to threshold-1      — mild ("peckish", "thirsty", "tired")
//   threshold to 23       — red  ("hungry",  "parched", "weary")
//   24                    — peak ("starving", "desperate", "exhausted")
//
// Empty string means "don't surface this need." Both NPC self-perception
// and overseer perception use this — the overseer's distress block filters
// to needs whose label is non-empty AND not the mild tier (i.e. red+ only),
// so mild-tier discomforts stay private to the NPC.
//
// Vocabulary lives in code rather than config because the bands need
// poetically-ordered words and that's a literary choice, not an operator
// dial. The thresholds themselves are configurable.
func needLabel(need string, value, threshold int) string {
	if value < 8 {
		return ""
	}
	var mild, red, peak string
	switch need {
	case "hunger":
		mild, red, peak = "peckish", "hungry", "starving"
	case "thirst":
		mild, red, peak = "thirsty", "parched", "desperate"
	case "tiredness":
		mild, red, peak = "tired", "weary", "exhausted"
	default:
		return ""
	}
	if value >= needMax {
		return peak
	}
	if value >= threshold {
		return red
	}
	return mild
}

// needLabelTier returns 0/1/2/3 for silent / mild / red / peak. Used by
// callers that want to filter (e.g. overseer perception drops mild-tier
// needs) without re-checking the label against vocabulary.
func needLabelTier(value, threshold int) int {
	if value < 8 {
		return 0
	}
	if value >= needMax {
		return 3
	}
	if value >= threshold {
		return 2
	}
	return 1
}

