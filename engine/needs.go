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
// tiredness, SMALLINT 0–24, higher = more in need). They grow with
// simulated time and drop when an NPC consumes (eating drops hunger,
// drinking drops thirst). Tiredness drops via three paths: take_break
// (recovery sweep at break_until > NOW), sleep (recovery sweep at
// sleeping_until > NOW — NPC auto-sleep on arrival home in
// maybeNPCAutoSleep, PC auto-bed via autoBedIdleLodgers), and dwell
// recovery at trees / wells / meals (dispatchObjectRefreshDwell).
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
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
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

	// Hysteresis margin between the onset-cross threshold and the
	// resolved-cross threshold (ZBBS-119 Phase 2.A). Onset fires at
	// `value >= threshold`; resolved fires at `value < threshold -
	// margin`. The gap prevents flapping if a future config reduces
	// consumption magnitude below the margin (today's default zeros the
	// need, so this is defensive). Hardcoded rather than configured
	// because the small constant covers all foreseeable cases — promote
	// to a setting if a real tuning need emerges. Two is the smallest
	// margin that absorbs a partial-consumption config without making
	// resolution feel sluggish; four would push thirst's resolve floor
	// to the awareness floor (8) given the default thirst threshold of
	// 12.
	needsHysteresisMargin = 2
)

// needTickEligibilityPred is the shared SQL predicate that picks the
// actors whose bodies age over time — agent NPCs and PCs. Both the
// pre-tick lock CTE and the UPDATE's lock CTE in dispatchNeedsTick
// reference this constant, so a future widening (a fourth actor kind
// with a body) edits one place. Decoratives (no login, no agent)
// stay excluded because they have no need-pressure mechanic.
//
// Sleeping actors are also excluded (ZBBS-HOME-204): once a sleep
// state is set (via NPC auto-sleep or PC /pc/sleep), the body's clock
// pauses — no hunger / thirst / tiredness accrual until they wake.
// Vendors on break_until still tick (break ≠ sleep — a vendor on
// break is awake, just off-shift, and should still get hungry).
//
// Concatenated into the SQL string at build time. Safe here because
// the value is a compile-time constant — no SQL injection surface.
// DO NOT extend this pattern to fragments derived from request
// payloads, settings, or any other runtime input; route those
// through bound parameters ($1, $2, ...) instead.
const needTickEligibilityPred = `(login_username IS NOT NULL OR llm_memory_agent IS NOT NULL)
                                  AND (sleeping_until IS NULL OR sleeping_until <= NOW())`

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

	// Pre-tick read for onset-crossing detection (ZBBS-119 Phase 2.A,
	// converted to actor_need rows in ZBBS-121 commit 3). The locking
	// CTE acquires actor row locks first in a separate sub-statement
	// so the lock-acquisition order is the SELECT's ORDER BY id,
	// independent of how Postgres orders the outer JOIN result. The
	// outer SELECT then reads need values via LEFT JOIN — locks
	// already held, so this is a pure read. Matches consumption.go's
	// `FOR UPDATE` lock target (the actor row), serializing the tick
	// against concurrent applyConsumption.
	//
	// Lock scope (ZBBS-123): every actor whose body the UPDATE below
	// touches must be locked first per the actor_need write contract
	// (writeNeedRows doc) — so the CTE locks both agent NPCs and PCs.
	// Onset detection still only fires for agent NPCs (chronicler has
	// no way to dispatch attention to a PC); the outer SELECT filters
	// to llm_memory_agent IS NOT NULL so PCs are locked but not
	// scanned for crossings. Decoratives stay excluded entirely — no
	// body model, no need to lock or accrue.
	//
	// LEFT JOIN keeps actors with missing actor_need rows in the
	// result set; the per-need GetOK loop below logs missing rows and
	// skips onset detection for that need.
	type onsetPre struct {
		actorID string
		needs   NeedSet
	}
	var pres []onsetPre
	// Eligibility predicate is shared verbatim between the pre-tick
	// lock CTE and the UPDATE's lock CTE — see needTickEligibilityPred
	// in needs_repo-style helpers below. Both queries must select the
	// same actor set so the lock acquired here serializes the writes
	// done there. The shared constant means a future widening (adding
	// a fourth actor kind) edits one string instead of two.
	preRows, preErr := tx.Query(ctx,
		`WITH locked_actors AS (
		     SELECT id, llm_memory_agent
		       FROM actor
		      WHERE `+needTickEligibilityPred+`
		      ORDER BY id
		      FOR UPDATE
		 )
		 SELECT la.id, n.key, n.value
		   FROM locked_actors la
		   LEFT JOIN actor_need n ON n.actor_id = la.id
		  WHERE la.llm_memory_agent IS NOT NULL
		  ORDER BY la.id, n.key`,
	)
	if preErr != nil {
		// Continue without onset detection — the UPDATE itself is the
		// primary work; a missed onset surfaces in the chronicler's
		// next perception via the standing distress block.
		log.Printf("needs_tick: read pre-values for onset detection (continuing without): %v", preErr)
	} else {
		// Defer Close so any early return from this branch doesn't leave
		// rows open on the tx connection — leaving rows open can wedge
		// later queries on the same connection.
		func() {
			defer preRows.Close()
			byID := map[string]NeedSet{}
			var order []string
			for preRows.Next() {
				var id string
				var key sql.NullString
				var value sql.NullInt64
				if err := preRows.Scan(&id, &key, &value); err != nil {
					log.Printf("needs_tick: scan pre-value row (skipping onset detection this tick): %v", err)
					pres = nil
					return
				}
				s, ok := byID[id]
				if !ok {
					s = NeedSet{}
					byID[id] = s
					order = append(order, id)
				}
				// LEFT JOIN can produce NULL key/value when the actor has no
				// actor_need rows. Skip the assignment; per-need GetOK in the
				// crossing loop below logs and skips for missing rows.
				if key.Valid && value.Valid {
					s[key.String] = int(value.Int64)
				}
			}
			if err := preRows.Err(); err != nil {
				log.Printf("needs_tick: iterate pre-values (skipping onset detection this tick): %v", err)
				pres = nil
				return
			}
			for _, id := range order {
				pres = append(pres, onsetPre{actorID: id, needs: byID[id]})
			}
		}()
	}

	// Apply the hourly increment directly to actor_need rows
	// (ZBBS-121 commit 5: rows are now the sole write target). Same
	// LEAST clamp as the pre-conversion column UPDATE.
	//
	// Eligibility filter: every actor with a body that ages — agent
	// NPCs and PCs. Decoratives stay excluded because they have no
	// need-pressure mechanic (no chronicler attention, no LLM-driven
	// reaction, no player driving consumption). PCs were originally
	// excluded here (ZBBS-121 commit 5) on the rationale that the
	// player drives their own consumption; ZBBS-123 brings them in
	// so their HUD readout is meaningful — walking burns tiredness
	// (already wired in agent_tick.go / pc_handlers.go) and time
	// burns hunger / thirst the same way it does for NPCs.
	//
	// Onset detection (further down) keeps the agent-only filter, so
	// chronicler dispatch still only fires for NPCs — PC distress
	// surfaces in the HUD, not via the overseer.
	//
	// Drive the UPDATE from a locked_actors CTE that uses the same
	// eligibility predicate as the pre-tick read above. Both CTEs lock
	// the same rows in the same tx — the first acquires, the second
	// is a no-op re-acquire — but using the CTE here means the
	// UPDATE's "which actors does this touch" is structurally tied to
	// the lock set, not to a free-floating WHERE that could drift if
	// the predicate is edited in only one place.
	// ZBBS-141: tiredness recovery during break is no longer applied
	// here. The previous in-needs_tick branch flat-credited a full
	// hour's worth of recovery to anyone whose break_until was in the
	// future at tick time, which dropped sub-hour breaks (no boundary
	// crossed → zero recovery) and over-credited boundary-crossing
	// breaks (full hour regardless of actual elapsed minutes). Recovery
	// now streams continuously through the break via the per-minute
	// runTirednessRecoverySweep goroutine, tracked by the
	// last_tiredness_recovery_at cursor on actor. needs_tick still
	// accrues hunger / thirst / tiredness from time alone — vendors get
	// hungry on break too. Sleeping actors (ZBBS-HOME-204) are excluded
	// at the predicate level so a sleeping body's clock pauses; break
	// stays awake-but-off-shift and keeps ticking.
	tag, err := tx.Exec(ctx, `
		WITH locked_actors AS (
		    SELECT id
		      FROM actor
		     WHERE `+needTickEligibilityPred+`
		     ORDER BY id
		     FOR UPDATE
		)
		UPDATE actor_need an
		   SET value = LEAST($1::int, an.value + $2::int)
		  FROM locked_actors la
		 WHERE la.id = an.actor_id
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

	// Resolve onset crossings + load dispatch agent info while still in
	// the tx so all reads share one snapshot. Enqueue happens after
	// commit so the chronicler doesn't see events for actors whose
	// updates rolled back. Crossing detection iterates the Need
	// registry — adding a fourth need (mood, loneliness) only requires
	// extending the registry, not editing this loop.
	thresholds := app.loadNeedThresholds(ctx)
	var onsets []chroniclerDispatchAgent
	for _, p := range pres {
		var crossed []string
		var severities []NeedTier
		for _, nd := range Needs {
			old, ok := p.needs.GetOK(nd.Key)
			if !ok {
				log.Printf("needs_tick: missing actor_need row actor=%s key=%s (skipping onset detection for this need)", p.actorID, nd.Key)
				continue
			}
			postVal := clampNeed(old + totalIncrement)
			threshold := thresholds.Get(nd.Key)
			oldTier := nd.Tier(old, threshold)
			newTier := nd.Tier(postVal, threshold)
			// Phase 2.B (ZBBS-121 commit 7): generalize from
			// "crossed red threshold" to "tier increased into red or
			// peak". Catches both fresh red-tier onsets (mild→red,
			// matches Phase 2.A) and peak-tier onsets the original
			// detector missed (red→peak: oldH was already >= threshold
			// so the < threshold guard skipped it; or mild→peak via a
			// large catch-up increment, where newH lands at needMax in
			// one step).
			if newTier > oldTier && newTier >= NeedRed {
				crossed = append(crossed, nd.Key)
				severities = append(severities, newTier)
			}
		}
		if len(crossed) == 0 {
			continue
		}
		agent, ok, err := app.loadDispatchAgentForActor(ctx, tx, p.actorID)
		if err != nil {
			log.Printf("needs_tick: load dispatch agent for onset %s: %v", p.actorID, err)
			continue
		}
		if !ok {
			// Actor row vanished or became non-agent between SELECT and
			// here — skip silently, no chronicler attention warranted.
			continue
		}
		agent.OnsetNeeds = crossed
		agent.OnsetSeverities = severities
		onsets = append(onsets, agent)
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("needs_tick: commit failed: %v", err)
		return
	}

	// Enqueue at the hour boundary so the dispatch queue's
	// (event_type, minute) coalescing folds same-tick onsets into one
	// batch. Notify the buffered dispatcher so onsets surface in the
	// next perception even when no arrival follows; without this,
	// onsets would sit in the queue indefinitely (the legacy
	// dispatchChroniclerShiftBoundaries fallback is suppressed when
	// buffered dispatch is on).
	for _, a := range onsets {
		app.ChroniclerDispatchQueue.enqueue(dispatchNeedsOnset, hourBoundary, a)
	}
	if len(onsets) > 0 {
		app.ChroniclerBufferedDispatcher.notify()
	}

	log.Printf("needs_tick: %d hour(s) elapsed, applying %d capped hour(s) (last %s -> now %s), +%d to %d villagers (onsets: %d)",
		hoursElapsed, cappedHours, lastAt.Format(time.RFC3339), hourBoundaryStr, totalIncrement, tag.RowsAffected(), len(onsets))
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

	// defaultTirednessCriticalThresholdPct (ZBBS-172) is the percent-of-
	// needMax tier at which the recovery-options perception block lifts
	// the on-shift gate that hides home/inn/tavern from tired NPCs.
	// Stored as percent so the absolute tracks needMax if it ever
	// changes. With needMax=24 this resolves to ceil(24*0.90) = 22 —
	// two ticks of grace past the red threshold (20) before max
	// collapse (24). At critical, abandoning shift to sleep becomes
	// a real option the LLM can weigh.
	defaultTirednessCriticalThresholdPct = 90
)

// loadTirednessCriticalThreshold reads tiredness_critical_threshold_pct,
// clamps it into [50, 100], and returns the absolute threshold —
// ceil(needMax * pct / 100). Out-of-range values fall back to the
// default.
//
// The result is then floored at red+1 so that critical is always
// strictly greater than the red threshold. Without that floor, an
// operator who set the percent low (e.g. 50 → 12 with current red=20)
// would lift the on-shift recovery-options gates the moment any actor
// crossed red, collapsing the two-tier model into one. The floor
// preserves the design intent (red triggers the recovery block;
// critical lifts the shift gate) without surfacing a separate
// "critical_min_offset" config knob.
func (app *App) loadTirednessCriticalThreshold(ctx context.Context) int {
	pct := app.loadIntSetting(ctx, "tiredness_critical_threshold_pct", defaultTirednessCriticalThresholdPct)
	if pct < 50 || pct > 100 {
		log.Printf("needs: out-of-range tiredness_critical_threshold_pct=%d (using default %d)", pct, defaultTirednessCriticalThresholdPct)
		pct = defaultTirednessCriticalThresholdPct
	}
	// Ceiling division: (needMax*pct + 99) / 100.
	crit := (needMax*pct + 99) / 100
	red := app.loadNeedThreshold(ctx, "tiredness_red_threshold", defaultTirednessRedThreshold)
	if crit <= red {
		crit = red + 1
	}
	if crit > needMax {
		crit = needMax
	}
	return crit
}

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

// needResolveThreshold returns the value below which a need is considered
// resolved (down-crossing target), given the configured red threshold.
// Implements the hysteresis gap by subtracting needsHysteresisMargin —
// then floors at 1 so the resolve cross stays reachable even if the red
// threshold is configured at or below the margin (settings rows can drift
// past the loadNeedThreshold clamp via direct DB edits, and the clamp
// itself could widen in a future change). Without the floor, a tiny
// threshold like 2 paired with margin=2 would target `newH < 0` — a
// condition the SQL clamp at 0 makes unreachable, silently disabling
// needs_resolved for that need.
func needResolveThreshold(redThreshold int) int {
	floor := redThreshold - needsHysteresisMargin
	if floor < 1 {
		return 1
	}
	return floor
}

// loadNonNegativeIntSetting clamps to >= 0. Useful where a negative
// value would invert a count-comparison and break the predicate
// (e.g. ceilings, budgets).
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

// visibleNeedsLines returns one perception line per co-located NPC
// whose hunger/thirst/tiredness is at red tier or above. Each line
// names the actor and joins their visible needs in a single sentence.
//
// "Co-located" = same `inside_structure_id` as the perceiver. Visitors
// loitering at the structure's door (visitor mode, inside_structure_id
// NULL) are intentionally not surfaced — they're outside the room.
//
// PCs (login_username set, llm_memory_agent NULL) are filtered out;
// their needs aren't a sim concept today. Decoratives are filtered for
// the same reason.
//
// Threshold is hardcoded at tier ≥ 2 to match the perceiver's own
// "Address now" cutoff. Mild-tier needs stay private. When
// hunger/thirst/tiredness move into attribute_definition rows, both the
// threshold and the visibility scope will become per-attribute config.
//
// Format examples:
//   "Ezekiel Crane looks hungry."
//   "Ezekiel Crane looks hungry and weary."
//   "Ezekiel Crane looks starving, parched, and exhausted."
func (app *App) visibleNeedsLines(ctx context.Context, perceiverID, structureID string) []string {
	// Reads from actor_need (ZBBS-121 commit 3). One row per (actor,
	// need) — three rows per agent NPC in the structure today.
	// Grouped in code by actor_id so the registry-loop band
	// classification can run per-actor. Loads thresholds via the
	// registry rather than accepting them as args — keeps the function
	// future-proof when a fourth need is added (callers don't need to
	// know to pass a fourth threshold). LEFT JOIN so an actor missing
	// any actor_need rows still appears (with empty NeedSet); the
	// per-need GetOK check inside the loop logs and skips missing
	// rows instead of silently treating them as value=0.
	thresholds := app.loadNeedThresholds(ctx)
	// ZBBS-149: filter to actors in the SAME room as the perceiver.
	// Without this, a sleeping lodger in a private bedroom appears in the
	// keeper's "visible needs" perception and the keeper greets/serves
	// them through the wall.
	//
	// Fail closed: require both the perceiver AND the other actor to
	// have a non-NULL inside_room_id. The migration backfills, and
	// every legitimate transition path stamps room_id alongside
	// inside_structure_id, so a NULL here is a code bug — better to
	// surface it as "perceives nobody" than to form a NULL=NULL ghost
	// bucket where two broken actors mutually see each other.
	rows, err := app.DB.Query(ctx,
		`SELECT a.id::text, a.display_name, n.key, n.value
		 FROM actor a
		 LEFT JOIN actor_need n ON n.actor_id = a.id
		 WHERE a.inside_structure_id = $1::uuid
		   AND a.id != $2::uuid
		   AND a.llm_memory_agent IS NOT NULL
		   AND a.inside_room_id IS NOT NULL
		   AND a.inside_room_id =
		       (SELECT inside_room_id FROM actor
		         WHERE id = $2::uuid AND inside_room_id IS NOT NULL)
		 ORDER BY a.display_name, n.key`,
		structureID, perceiverID)
	if err != nil {
		log.Printf("visibleNeedsLines: query structure %s: %v", structureID, err)
		return nil
	}
	defer rows.Close()

	type actorAcc struct {
		name  string
		needs NeedSet
	}
	byID := map[string]*actorAcc{}
	var order []string // first-appearance order, matches ORDER BY display_name from the query
	for rows.Next() {
		var id, name string
		var key sql.NullString
		var value sql.NullInt64
		if err := rows.Scan(&id, &name, &key, &value); err != nil {
			log.Printf("visibleNeedsLines: scan: %v", err)
			return nil
		}
		a, ok := byID[id]
		if !ok {
			a = &actorAcc{name: name, needs: NeedSet{}}
			byID[id] = a
			order = append(order, id)
		}
		// LEFT JOIN can produce a row with NULL key/value when the
		// actor has no actor_need rows at all. Skip the assignment for
		// that case; the per-need GetOK loop below will log it as
		// missing.
		if key.Valid && value.Valid {
			a.needs[key.String] = int(value.Int64)
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("visibleNeedsLines: iterate structure %s: %v", structureID, err)
		return nil
	}

	var lines []string
	for _, id := range order {
		a := byID[id]
		var visible []string
		for _, n := range Needs {
			value, ok := a.needs.GetOK(n.Key)
			if !ok {
				log.Printf("visibleNeedsLines: missing actor_need row actor=%s key=%s (treating as silent)", id, n.Key)
				continue
			}
			tier := n.Tier(value, thresholds.Get(n.Key))
			if tier >= NeedRed {
				visible = append(visible, n.Label(tier))
			}
		}
		if len(visible) == 0 {
			continue
		}
		var joined string
		switch len(visible) {
		case 1:
			joined = visible[0]
		case 2:
			joined = visible[0] + " and " + visible[1]
		default:
			joined = strings.Join(visible[:len(visible)-1], ", ") + ", and " + visible[len(visible)-1]
		}
		lines = append(lines, fmt.Sprintf("%s looks %s.", a.name, joined))
	}
	return lines
}

// snapshotNeeds reads the actor's current hunger/thirst/tiredness as a
// pre-action snapshot. Returns zeros on error — the caller's readback
// path treats "no change" as silent, so a failed read produces an empty
// readback rather than a misleading one.
//
// Reads from actor_need rows via the repo (ZBBS-121 commit 2). The
// dual-write hooks established in commit 1 keep rows in sync with the
// legacy actor columns until those columns drop in a later commit.
// Wrapper preserves the (h, t, ti) return shape so existing call sites
// in agent_tick.go don't need to change.
func (app *App) snapshotNeeds(ctx context.Context, actorID string) (int, int, int) {
	set, err := app.needsSnapshot(ctx, actorID)
	if err != nil {
		return 0, 0, 0
	}
	return set.Get("hunger"), set.Get("thirst"), set.Get("tiredness")
}

// buildPostConsumeReadback summarizes what changed in the actor's needs
// after a consume / pay-with-consumption, so the agent can decide
// whether to chain another tool call (drink water after eating bread,
// rest after refilling thirst) without waiting for the next perception
// build. Added 2026-05-02 — saw John Ellis eat bread and immediately
// `done` despite still being parched and exhausted.
//
// before* are pre-action snapshots (caller's responsibility to capture
// before executeAgentCommit). This function reads current state from the
// DB and produces the readback string. Returns "" if nothing changed —
// caller should omit the readback in that case.
//
// Output forms (followed by a trailing space for inline embedding):
//   "All needs settled now. "                       — every need below the awareness floor
//   "You no longer feel hungry. Still feel parched, exhausted. "  — at least one resolution + persistence
//   "You no longer feel hungry. "                   — pure resolution (everything else silent or mild-only)
//   "Still feel parched, exhausted. "               — only persistence (small reduction, no threshold cross)
//   "Needs eased slightly. "                        — change happened but only mild-tier residual
func (app *App) buildPostConsumeReadback(ctx context.Context, actorID string, beforeH, beforeT, beforeTi int) string {
	// Read from actor_need rows via the repo (ZBBS-121 commit 2). The
	// applyConsumption call that triggered this readback wrote both
	// columns and rows in its tx, so post-commit reads of either
	// surface return the same values.
	set, err := app.needsSnapshot(ctx, actorID)
	if err != nil {
		return ""
	}
	h := set.Get("hunger")
	t := set.Get("thirst")
	ti := set.Get("tiredness")
	if h == beforeH && t == beforeT && ti == beforeTi {
		return ""
	}

	hungerT := app.loadNeedThreshold(ctx, "hunger_red_threshold", defaultHungerRedThreshold)
	thirstT := app.loadNeedThreshold(ctx, "thirst_red_threshold", defaultThirstRedThreshold)
	tiredT := app.loadNeedThreshold(ctx, "tiredness_red_threshold", defaultTirednessRedThreshold)

	// Resolutions: was at red+ tier before, now below. Use the
	// before-value to label so "no longer feel hungry" reads correctly
	// (before=20 → red label "hungry", after=10 → mild "peckish" or
	// silent). What's resolved is the red-tier need, not the current
	// state.
	var resolved []string
	if beforeH >= hungerT && h < hungerT {
		resolved = append(resolved, needLabel("hunger", beforeH, hungerT))
	}
	if beforeT >= thirstT && t < thirstT {
		resolved = append(resolved, needLabel("thirst", beforeT, thirstT))
	}
	if beforeTi >= tiredT && ti < tiredT {
		resolved = append(resolved, needLabel("tiredness", beforeTi, tiredT))
	}

	// Still pressing: red+ tier needs after the action. Mild-tier needs
	// are intentionally omitted — the perception already noted them and
	// repeating them post-consume would clutter the reply.
	var stillPressing []string
	if needLabelTier(h, hungerT) >= 2 {
		stillPressing = append(stillPressing, needLabel("hunger", h, hungerT))
	}
	if needLabelTier(t, thirstT) >= 2 {
		stillPressing = append(stillPressing, needLabel("thirst", t, thirstT))
	}
	if needLabelTier(ti, tiredT) >= 2 {
		stillPressing = append(stillPressing, needLabel("tiredness", ti, tiredT))
	}

	allSilent := needLabel("hunger", h, hungerT) == "" &&
		needLabel("thirst", t, thirstT) == "" &&
		needLabel("tiredness", ti, tiredT) == ""
	if allSilent {
		return "All needs settled now. "
	}

	var parts []string
	if len(resolved) > 0 {
		parts = append(parts, fmt.Sprintf("You no longer feel %s.", strings.Join(resolved, ", ")))
	}
	if len(stillPressing) > 0 {
		parts = append(parts, fmt.Sprintf("Still feel %s.", strings.Join(stillPressing, ", ")))
	}
	if len(parts) == 0 {
		// Numeric change happened but no threshold crossed and no red+
		// residual. Still worth acknowledging — keeps the agent aware
		// the action had effect.
		return "Needs eased slightly. "
	}
	return strings.Join(parts, " ") + " "
}
