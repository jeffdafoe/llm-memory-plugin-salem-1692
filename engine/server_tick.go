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
	app.dispatchScheduledBehaviors(ctx)
	app.dispatchSocialSchedules(ctx)
}
