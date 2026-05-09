package main

// Unified server tick.
//
// A single goroutine wakes once per serverTickInterval and fans out to every
// registered handler. Each handler is a plain function that reads the world
// state, decides whether it needs to act, and acts — all operations are
// idempotent so a missed tick, a server restart mid-loop, or a late handler
// call never double-fires.
//
// History: the day/night + daily rotation machinery in world_phase.go ran its
// own ticker. When we added worker-NPC schedules (per-NPC arrive/leave
// dispatch via shift_offset_hours), we hit the second server-side scheduling
// need, so the ticker was lifted here. New scheduled behaviors drop in as
// additional handlers in runServerTick — no new goroutine, no new cadence.

import (
	"context"
	"log"
	"time"
)

const serverTickInterval = 60 * time.Second

// runServerTick is the engine's one scheduled-work goroutine. Started from
// main() and shut down via ctx cancellation during graceful shutdown.
//
// A kick-once at startup catches up any boundaries crossed while the server
// was down, matching the "server came up mid-phase" guarantee the phase
// ticker already provided before the lift.
func (app *App) runServerTick(ctx context.Context) {
	log.Printf("server_tick: started (%s interval)", serverTickInterval)
	ticker := time.NewTicker(serverTickInterval)
	defer ticker.Stop()

	app.runServerTickOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Println("server_tick: stopping")
			return
		case <-ticker.C:
			app.runServerTickOnce(ctx)
		}
	}
}

// runServerTickOnce invokes every registered handler for a single tick. New
// handlers go here. Keep them individually idempotent — one handler panicking
// or logging an error mustn't stop the next from running.
func (app *App) runServerTickOnce(ctx context.Context) {
	app.checkAndTransition(ctx)
	app.checkAndRotate(ctx)
	// Refresh the agent slug → display_name map every server tick so
	// the recall result formatter has fresh data even when other
	// dispatchers short-circuit. Reactive ticks fire at any hour and
	// need the map; cheap query.
	app.refreshNPCDisplayNames(ctx)
	// Chronicler runs first so any atmosphere it writes at the phase
	// boundary lands in world_environment before reactive NPC ticks build
	// perceptions. Cheap when no boundary just crossed — single setting
	// read + comparison. Phase boundaries (dawn / midday / dusk) are the
	// only chronicler firing path post-ZBBS-WORK-202; cascade origins,
	// shift boundaries, and routine-arrival buffering all gone.
	app.dispatchChroniclerPhase(ctx)
	app.dispatchScheduledBehaviors(ctx)
	app.dispatchSocialSchedules(ctx)
	// Attribute tick advances NPC needs (hunger/thirst/tiredness) when the
	// wall-clock hour has rolled. No-op on most ticks (cheap setting read +
	// integer compare); single batch UPDATE on the boundary.
	app.dispatchNeedsTick(ctx)
	// Object-refresh regen (ZBBS-090) — replenishes available_quantity for
	// rows configured with refresh_period_hours, in continuous or periodic
	// mode. Cheap when no row is behind (single SELECT, zero UPDATEs).
	app.dispatchObjectRefreshRegen(ctx)
	// Dwell tick (ZBBS-172) — applies per-tick recovery to actors still
	// present at a credited object/structure. Drives the rest-tree, well-
	// linger, and meal-at-tavern mechanics from a single handler. Cheap
	// when no credits are ripe (single SELECT, zero UPDATEs).
	app.dispatchObjectRefreshDwell(ctx)
	// Sim conversation push — pushes the previous UTC day's
	// agent_action_log digest to llm-memory-api so the api builds a
	// distilled conversations/YYYY-MM-DD-sim-day note for the dream
	// pipeline. No-op on most ticks (cheap setting read + date compare);
	// fires only on the first tick after a UTC day rollover.
	app.dispatchSimConversationPush(ctx)
	// Visitor archetype (ZBBS-WORK-201) — transient VAs that arrive,
	// hang around, deliver content, depart. Three handlers in fixed
	// order:
	//   despawn — start expired visitors walking back to spawn-edge
	//   cleanup — hard-delete visitors past the grace window
	//   spawn   — probabilistically spawn a new visitor
	// All three are no-ops by default (spawn chance = 0; spawn coords
	// and sprite name unconfigured) — the feature is off until an
	// admin sets the gating dials. Steady-state cost when off is a
	// handful of cheap settings reads. Run before dispatchIdleSweep
	// so a fresh visitor's last_agent_tick_at (NULL on insert) doesn't
	// get caught in the same-tick sweep once Phase 2+ wires the
	// llm_memory_agent slot.
	app.dispatchVisitorDespawn(ctx)
	app.dispatchVisitorCleanup(ctx)
	app.dispatchVisitorSpawn(ctx)
	// Idle-sweep (ZBBS-HOME-201) — deterministic floor for agentized
	// NPC ticks. Finds VAs idle past the threshold and schedules a
	// self-tick within a randomized response window so the engine
	// doesn't fire 30 NPCs on the same minute. Runs before
	// dispatchSelfTicks so a same-tick fire can land if the random
	// delay rolls 0 (rare, intentional). No-op when no candidates
	// (single SELECT, zero UPDATEs).
	app.dispatchIdleSweep(ctx)
	// Self-tick scheduler drain (ZBBS-110) — fires NPC harnesses whose
	// scheduled fire time has arrived. Single SQL UPDATE...RETURNING
	// claims the rows atomically so a slow trigger doesn't get re-fired
	// on the next server tick. Bounded LIMIT inside the query. Last in
	// the handler list so it observes any state changes the prior
	// handlers committed this tick (a shift boundary the worker
	// scheduler enqueued, for instance, would already be visible to a
	// self-tick that fires immediately after).
	app.dispatchSelfTicks(ctx)
}
