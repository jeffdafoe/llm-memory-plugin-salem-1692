package main

// Idle-sweep — deterministic engine floor for NPC ticks (ZBBS-HOME-201).
//
// NPC ticks are reactive-only: they fire from cascade origins (PC speak,
// NPC arrival, heard-speech, summon delivery, scheduled self-tick). When
// none of those fires, an NPC sits forever. The chronicler's attend_to
// dispatch existed to backstop this but is unreliable — it tends to pick
// every candidate every fire, billing LLM cost without producing a
// durable schedule. Once we tear out attend_to (ZBBS-HOME-202), we need
// a deterministic floor; this file is that floor.
//
// Mechanism: every server tick, find agentized NPCs whose last_agent_tick_at
// is older than the configured threshold and aren't already scheduled.
// For each candidate, schedule a self-tick at now + random(0, response
// window) so the engine doesn't fire 30 NPCs on the same minute when they
// all cross the threshold together. The actual tick fires through the
// existing dispatchSelfTicks → triggerImmediateTick path.
//
// Cleanup of next_self_tick_at on tick completion is already handled by
// the existing self-tick machinery: dispatchSelfTicks atomically clears
// the slot when claiming rows, and triggerImmediateTick calls
// cancelSelfTick for non-self cascade origins. Nothing new here.

import (
	"context"
	"log"
	"math/rand"
	"time"
)

const (
	defaultIdleSweepThresholdMinutes      = 30
	defaultIdleSweepResponseWindowMinutes = 15
)

// dispatchIdleSweep is the per-server-tick handler. Schedules self-ticks
// for agentized NPCs that have gone idle past the threshold. Either knob
// set to 0 disables the sweep entirely (operator escape hatch).
//
// The SQL pulls candidates and filters out asleep / on-break / already-
// scheduled rows; the in-memory walk filter then drops anyone with an
// active walk (walk state lives in NPCMovement.active, not the DB).
//
// scheduleSelfTick already has wins-the-soonest semantics, so concurrent
// cascade origins (PC speak landing during the sweep) won't lose to the
// sweep — the cascade either cancels the schedule via cancelSelfTick
// before it's set, or the schedule is harmlessly superseded.
func (app *App) dispatchIdleSweep(ctx context.Context) {
	thresholdMin := app.loadNonNegativeIntSetting(ctx, "idle_sweep_threshold_minutes", defaultIdleSweepThresholdMinutes)
	windowMin := app.loadNonNegativeIntSetting(ctx, "idle_sweep_response_window_minutes", defaultIdleSweepResponseWindowMinutes)
	if thresholdMin == 0 || windowMin == 0 {
		return
	}
	threshold := time.Duration(thresholdMin) * time.Minute

	rows, err := app.DB.Query(ctx,
		`SELECT id::text
		 FROM actor
		 WHERE llm_memory_agent IS NOT NULL
		   AND (sleeping_until IS NULL OR sleeping_until <= NOW())
		   AND (break_until IS NULL OR break_until <= NOW())
		   AND next_self_tick_at IS NULL
		   AND (last_agent_tick_at IS NULL OR last_agent_tick_at < $1)`,
		time.Now().Add(-threshold))
	if err != nil {
		log.Printf("idle-sweep: query: %v", err)
		return
	}
	defer rows.Close()

	var candidates []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return
	}

	// Walk-active filter. A walking NPC is implicitly busy via the move
	// loop (the arrival hook will tick them on landing). Skipping here
	// avoids scheduling a tick that would race the arrival cascade.
	app.NPCMovement.mu.Lock()
	walking := make(map[string]bool, len(app.NPCMovement.active))
	for id := range app.NPCMovement.active {
		walking[id] = true
	}
	app.NPCMovement.mu.Unlock()

	windowSeconds := windowMin * 60
	scheduled := 0
	for _, id := range candidates {
		if walking[id] {
			continue
		}
		// rand.Intn(windowSeconds) is [0, windowSeconds). The 0 case is
		// rare (1 in windowSeconds, ~1/900 at default 15m) and only
		// means the schedule fires on the same server tick if
		// dispatchSelfTicks runs after this handler — that's the
		// registered order, but it's fine either way: a 0-delay sweep
		// schedule firing immediately is the intended behavior, just
		// the rare end of the response window.
		delay := time.Duration(rand.Intn(windowSeconds)) * time.Second
		app.scheduleSelfTick(ctx, id, time.Now().Add(delay), "idle-sweep")
		scheduled++
	}

	if scheduled > 0 {
		log.Printf("idle-sweep: scheduled %d VAs (threshold=%dm, window=%dm)",
			scheduled, thresholdMin, windowMin)
	}
}
