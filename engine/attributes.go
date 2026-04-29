package main

// NPC attribute ticking — hunger, thirst, tiredness.
//
// Each villager carries three needs (village_agent.hunger / thirst /
// tiredness, SMALLINT 0–24, higher = more in need). They grow with simulated
// time and drop when an NPC consumes (eating drops hunger, drinking drops
// thirst). Tiredness has no consumption mechanic yet — sleep handling comes
// later.
//
// Cadence: a single hourly batch UPDATE rather than per-NPC stamps. The
// per-minute server tick handler (dispatchAttributeTick, registered in
// runServerTickOnce) reads last_attribute_tick_hour, compares to the current
// wall-clock hour, and runs the batch only when the hour has rolled over.
// Idempotent against missed ticks and server restarts: if the server is
// down across an hour boundary we still only fire once when it comes back
// (the next hour boundary), and we don't backfill the missed hours — needs
// just don't accrue while the engine is down. That's fine; this isn't a
// fairness model.
//
// First-run behavior: when last_attribute_tick_hour is NULL (fresh
// migration), the handler stamps the current hour without incrementing.
// Avoids a deploy-time pulse where every villager gets +1 hunger the
// instant the migration runs. The next hour boundary fires the first
// real tick like any other.
//
// Magnitude is governed by the attribute_tick_amount setting (default 1).
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
	attributeMax = 24
)

// dispatchAttributeTick is registered in runServerTickOnce. Cheap when the
// hour hasn't rolled over (one setting read + integer compare); does a
// single UPDATE across all villagers when it has.
//
// The whole read-decide-update-stamp is wrapped in a transaction with
// SELECT FOR UPDATE on the setting row. runServerTickOnce is single-
// goroutine within one process, so concurrent firing isn't possible
// today, but the row lock makes this safe if the engine is ever scaled
// to multiple instances against the same DB. Cost is one extra round-
// trip per tick; cheap enough to be worth the future-proofing.
func (app *App) dispatchAttributeTick(ctx context.Context) {
	currentHour := time.Now().Hour()

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		log.Printf("attribute_tick: begin tx: %v", err)
		return
	}
	defer tx.Rollback(ctx) // safe after commit (no-op)

	// Lock the setting row up front. If another instance crossed the same
	// boundary first, by the time we acquire the lock we'll see their
	// stamp and skip. The COALESCE handles a NULL value (fresh migration).
	var lastHourStr string
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(value, '') FROM setting WHERE key = 'last_attribute_tick_hour' FOR UPDATE`,
	).Scan(&lastHourStr)
	if err != nil {
		log.Printf("attribute_tick: lock setting row: %v", err)
		return
	}

	if lastHourStr == "" {
		// First run after migration. Stamp the current hour without
		// incrementing so the next hour boundary fires the first real
		// tick. This avoids a deploy-time pulse where every villager gets
		// +1 hunger the instant the migration runs.
		if _, err := tx.Exec(ctx,
			`UPDATE setting SET value = $1 WHERE key = 'last_attribute_tick_hour'`,
			strconv.Itoa(currentHour),
		); err != nil {
			log.Printf("attribute_tick: stamp first-run hour: %v", err)
			return
		}
		_ = tx.Commit(ctx)
		return
	}

	lastHour, err := strconv.Atoi(lastHourStr)
	if err != nil {
		log.Printf("attribute_tick: bad last_attribute_tick_hour %q (resetting to current): %v", lastHourStr, err)
		_, _ = tx.Exec(ctx,
			`UPDATE setting SET value = $1 WHERE key = 'last_attribute_tick_hour'`,
			strconv.Itoa(currentHour),
		)
		_ = tx.Commit(ctx)
		return
	}

	if currentHour == lastHour {
		return
	}

	// Hour has rolled. Read the per-tick magnitude and apply. The magnitude
	// setting is read non-locking — it's operator-tuned and racing with an
	// admin edit just means the new magnitude lands on the next tick.
	amountStr := app.loadSetting(ctx, "attribute_tick_amount", "1")
	amount, err := strconv.Atoi(amountStr)
	if err != nil || amount <= 0 {
		log.Printf("attribute_tick: bad attribute_tick_amount %q (skipping tick): %v", amountStr, err)
		// Still stamp the hour so we don't keep retrying the bad value
		// every minute until midnight. Operator can fix the setting and
		// the next hour boundary will pick up the corrected magnitude.
		_, _ = tx.Exec(ctx,
			`UPDATE setting SET value = $1 WHERE key = 'last_attribute_tick_hour'`,
			strconv.Itoa(currentHour),
		)
		_ = tx.Commit(ctx)
		return
	}

	tag, err := tx.Exec(ctx, `
		UPDATE village_agent SET
			hunger    = LEAST($1::int, hunger    + $2::int),
			thirst    = LEAST($1::int, thirst    + $2::int),
			tiredness = LEAST($1::int, tiredness + $2::int)
	`, attributeMax, amount)
	if err != nil {
		log.Printf("attribute_tick: UPDATE failed (rolling back, will retry next minute): %v", err)
		return
	}

	if _, err := tx.Exec(ctx,
		`UPDATE setting SET value = $1 WHERE key = 'last_attribute_tick_hour'`,
		strconv.Itoa(currentHour),
	); err != nil {
		log.Printf("attribute_tick: stamp last hour failed (rolling back, will retry next minute): %v", err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("attribute_tick: commit failed: %v", err)
		return
	}

	log.Printf("attribute_tick: hour %d -> %d, +%d to %d villagers", lastHour, currentHour, amount, tag.RowsAffected())
}

// loadAttributeMagnitude returns the configured drop magnitude for a given
// consumption kind (food / drink). Used by executePay when a transaction
// resolves to a specific need to reduce. Falls back to attributeMax (full
// reset) on any read error so a misconfigured setting doesn't strand a
// hungry NPC unfed.
func (app *App) loadAttributeMagnitude(ctx context.Context, key string) int {
	v := app.loadSetting(ctx, key, "")
	if v == "" {
		return attributeMax
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		log.Printf("attributes: bad %s magnitude %q (using max): %v", key, v, err)
		return attributeMax
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
// folded into loadSetting itself.
func (app *App) loadIntSetting(ctx context.Context, key string, def int) int {
	v := app.loadSetting(ctx, key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("attributes: bad int setting %s=%q (using default %d): %v", key, v, def, err)
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
	if value >= attributeMax {
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
	if value >= attributeMax {
		return 3
	}
	if value >= threshold {
		return 2
	}
	return 1
}

