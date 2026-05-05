package main

// Needs registry + repository (ZBBS-121).
//
// Single-source-of-truth abstractions for the graduated need values
// (hunger, thirst, tiredness, future kinds). Replaces the spread of
// inlined SQL across consumption.go, needs.go, room_narration.go,
// chronicler.go, etc. that each redeclared the column triplet and
// the threshold-loading triplet.
//
// Migration story (ZBBS-121 refactor commits):
//   commit 1 (this one): schema + registry + read methods + dual-write
//                        hooks at the two existing column-write sites.
//                        Reads still come from the legacy columns; rows
//                        exist as a parallel write target so that by
//                        the time read sites convert, the rows are
//                        current and trustworthy.
//   commit 2..N        : convert read sites to use the repo (reading
//                        from rows). Convert remaining write sites.
//   commit N+1         : repo's write helpers stop touching the legacy
//                        columns.
//   commit N+2         : drop the legacy columns from actor.
//
// The Need registry is the place where the next graduated quantity
// (mood / loneliness / fatigue-of-a-different-kind) gets added — one
// entry here + one row per actor in actor_need + one threshold setting.
// All loop-driven code (crossing detection, perception render, hourly
// tick) picks it up automatically.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Need describes one graduated quantity an actor carries. Every Need
// in the registry has a row in actor_need per actor (post-backfill).
//
// DBColumn is the legacy actor.<column> name during the dual-write
// transition window. Removed when the columns drop.
type Need struct {
	Key                 string // "hunger" — actor_need.key, registry lookup
	DBColumn            string // "hunger" — legacy actor column (removed at end of refactor)
	Mild                string // "peckish" — vocabulary band 1
	Red                 string // "hungry"  — vocabulary band 2
	Peak                string // "starving" — vocabulary band 3
	DefaultThreshold    int    // 18 — fallback when the setting row is missing
	ThresholdSettingKey string // "hunger_red_threshold" — setting row key
}

// Needs is the canonical registry. Iteration order is stable across
// processes — code that depends on stable ordering (e.g. lock order
// for SELECT FOR UPDATE) can rely on this slice's order.
var Needs = []Need{
	{
		Key:                 "hunger",
		DBColumn:            "hunger",
		Mild:                "peckish",
		Red:                 "hungry",
		Peak:                "starving",
		DefaultThreshold:    defaultHungerRedThreshold,
		ThresholdSettingKey: "hunger_red_threshold",
	},
	{
		Key:                 "thirst",
		DBColumn:            "thirst",
		Mild:                "thirsty",
		Red:                 "parched",
		Peak:                "desperate",
		DefaultThreshold:    defaultThirstRedThreshold,
		ThresholdSettingKey: "thirst_red_threshold",
	},
	{
		Key:                 "tiredness",
		DBColumn:            "tiredness",
		Mild:                "tired",
		Red:                 "weary",
		Peak:                "exhausted",
		DefaultThreshold:    defaultTirednessRedThreshold,
		ThresholdSettingKey: "tiredness_red_threshold",
	},
}

// FindNeed returns the Need with the given key. Used by code paths
// that have a string need key in hand (e.g. crossing detection during
// the conversion window, where the legacy code uses string keys).
func FindNeed(key string) (Need, bool) {
	for _, n := range Needs {
		if n.Key == key {
			return n, true
		}
	}
	return Need{}, false
}

// NeedTier classifies a need's value into intensity bands. Mirrors
// needLabelTier — once the conversion is complete, needLabelTier will
// return NeedTier directly instead of int.
type NeedTier int

const (
	// NeedSilent — value < 8. NPC isn't aware of the need; perception
	// suppresses it.
	NeedSilent NeedTier = 0
	// NeedMild — value in [8, threshold). Awareness without distress;
	// the standing distress block filters these out.
	NeedMild NeedTier = 1
	// NeedRed — value in [threshold, needMax). Distress; the standing
	// distress block surfaces these and the chronicler may attend.
	NeedRed NeedTier = 2
	// NeedPeak — value == needMax. Critical distress.
	NeedPeak NeedTier = 3
)

// Tier classifies a value against this need's threshold. Caller
// supplies the threshold (loaded via app.loadNeedThreshold) so the
// computation stays a pure function on Need + values.
func (n Need) Tier(value, threshold int) NeedTier {
	if value < 8 {
		return NeedSilent
	}
	if value >= needMax {
		return NeedPeak
	}
	if value >= threshold {
		return NeedRed
	}
	return NeedMild
}

// Label returns the vocabulary word for the given tier. Empty for
// NeedSilent — perception code uses that as the "don't surface" signal.
func (n Need) Label(tier NeedTier) string {
	switch tier {
	case NeedMild:
		return n.Mild
	case NeedRed:
		return n.Red
	case NeedPeak:
		return n.Peak
	}
	return ""
}

// NeedSet is the values for one actor across the registry. Map keyed
// by Need.Key. Unknown keys read as 0 (Get is the safe accessor).
type NeedSet map[string]int

// Get returns the value for the given need key, or 0 if absent.
// Defaulting to 0 means an actor with a missing row is treated as
// silent rather than panicking — defensive against partial backfills
// or future needs added to the registry but not yet in actor_need.
func (s NeedSet) Get(key string) int {
	return s[key]
}

// GetOK returns the value and a presence flag — distinguishes a real
// 0 value from a missing row. Read paths that classify into tiers
// should use this and log a warning on absent rows so partial
// backfills or post-backfill new actors without rows surface as
// observable errors instead of silently being treated as silent.
func (s NeedSet) GetOK(key string) (int, bool) {
	v, ok := s[key]
	return v, ok
}

// needThresholds is a key→threshold lookup. Loaded once per perception
// build / chronicler turn via loadNeedThresholds so callsites don't
// repeat three loadNeedThreshold calls.
type needThresholds map[string]int

// Get returns the threshold for the given need key. Falls back to the
// registry default if the key isn't in the map (shouldn't happen if
// the map was built via loadNeedThresholds, but stays safe).
func (t needThresholds) Get(key string) int {
	if v, ok := t[key]; ok {
		return v
	}
	if n, ok := FindNeed(key); ok {
		return n.DefaultThreshold
	}
	return 0
}

// loadNeedThresholds reads the configured red threshold for every Need
// in the registry. One settings read per Need; the result is meant to
// be cached for the duration of one perception build / chronicler turn
// rather than re-loaded inside band-classification loops.
func (app *App) loadNeedThresholds(ctx context.Context) needThresholds {
	out := needThresholds{}
	for _, n := range Needs {
		out[n.Key] = app.loadNeedThreshold(ctx, n.ThresholdSettingKey, n.DefaultThreshold)
	}
	return out
}

// needsSnapshot reads all need values for one actor. Non-locking — for
// the post-action readback path, distance perception, and other
// callers that don't need the lock-out semantics of FOR UPDATE.
//
// Returns an empty NeedSet (not nil) when the actor exists but has no
// rows in actor_need (shouldn't happen post-backfill but stays safe).
func (app *App) needsSnapshot(ctx context.Context, actorID string) (NeedSet, error) {
	rows, err := app.DB.Query(ctx,
		`SELECT key, value FROM actor_need WHERE actor_id = $1`,
		actorID)
	if err != nil {
		return nil, fmt.Errorf("needsSnapshot: query: %w", err)
	}
	defer rows.Close()
	set := NeedSet{}
	for rows.Next() {
		var key string
		var value int
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("needsSnapshot: scan: %w", err)
		}
		set[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("needsSnapshot: iterate: %w", err)
	}
	return set, nil
}

// writeNeedRows mirrors a per-actor need-value map onto actor_need
// rows. Used by the dual-write hooks during the transition — every
// site that UPDATEs the legacy columns also calls this so the rows
// stay current. UPSERT pattern via INSERT ... ON CONFLICT to handle
// both the post-backfill "row exists, update it" case and the
// future "actor created since backfill" case in one statement.
//
// Validates keys against the registry up front so a callsite typo
// surfaces as a clear error rather than as a CHECK constraint
// violation from Postgres.
//
// Caller is responsible for the transaction. One Exec per (actor,
// need) entry; small N, acceptable cost. If this becomes hot enough
// to matter, batch into one INSERT with UNNEST.
//
// Lock contract: callers writing to actor_need MUST hold the actor
// row lock first (e.g. via SELECT ... FOR UPDATE on actor) — the
// read paths in applyConsumption / dispatchNeedsTick lock the actor
// row to serialize concurrent need updates, and bypassing that lock
// here would let an unrelated writer race against an in-flight read.
// All current callers (applyConsumption, the tick's
// syncAllNeedRowsFromColumns) satisfy this; new callers must too.
func (app *App) writeNeedRows(ctx context.Context, tx pgx.Tx, actorID string, values map[string]int) error {
	for key, value := range values {
		if _, ok := FindNeed(key); !ok {
			return fmt.Errorf("writeNeedRows: unknown need key %q (not in registry)", key)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO actor_need (actor_id, key, value)
			VALUES ($1, $2, $3)
			ON CONFLICT (actor_id, key) DO UPDATE SET value = EXCLUDED.value
		`, actorID, key, value); err != nil {
			return fmt.Errorf("writeNeedRows: upsert (%s, %s): %w", actorID, key, err)
		}
	}
	return nil
}

// seedNeedRowsIfMissing inserts an actor_need row for every Need in
// the registry, defaulting the value to 0. ON CONFLICT DO NOTHING
// makes it idempotent — existing rows are left untouched, only
// missing ones are created. Called from actor-creation paths
// (npcs.go's NPC create, pc_handlers.go's PC create/upsert) so a
// fresh actor immediately has the full set of rows that the read
// paths in applyConsumption / dispatchNeedsTick assume.
//
// Caller is responsible for the transaction. Should run in the same
// tx as the actor INSERT so the actor either exists with its full
// need row set or doesn't exist at all — never a half-state.
func (app *App) seedNeedRowsIfMissing(ctx context.Context, tx pgx.Tx, actorID string) error {
	for _, n := range Needs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO actor_need (actor_id, key, value)
			VALUES ($1, $2, 0)
			ON CONFLICT (actor_id, key) DO NOTHING
		`, actorID, n.Key); err != nil {
			return fmt.Errorf("seedNeedRowsIfMissing(actor=%s, key=%s): %w", actorID, n.Key, err)
		}
	}
	return nil
}
