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
	// dispatchers short-circuit (paused, asleep, baseline disabled).
	// Reactive ticks fire at any hour and need the map; cheap query.
	app.refreshNPCDisplayNames(ctx)
	// Chronicler runs next so any atmosphere or events it writes at the
	// phase boundary land in world_environment / world_events before NPCs
	// (or anything else this tick) build perceptions. Cheap when no
	// boundary just crossed — single setting read + comparison.
	app.dispatchChroniclerPhase(ctx)
	// Agent dispatch runs next among the per-NPC handlers. When an agent
	// commits to a move, executeAgentMoveTo sets agent_override_until before
	// returning, so the worker / rotation / social schedulers below see the
	// override at load time and short-circuit (M6.1 short-circuits added in
	// ZBBS-072). Order matters: if scheduled behaviors ran first, they could
	// dispatch a worker walk that the agent then countermanded.
	app.dispatchAgentTicks(ctx)
	app.dispatchScheduledBehaviors(ctx)
	app.dispatchSocialSchedules(ctx)
}
