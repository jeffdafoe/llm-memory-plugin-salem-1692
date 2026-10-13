package cascade

import (
	"context"
	"log"
	"math/rand"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// room_drift.go — LLM-768. The goroutine driver for the idle drift inside rooms
// with a floor plan: every sim.RoomDriftSweepInterval it runs sim.DriftInRooms,
// which owns who drifts, when and where. Mechanical, no LLM turn.

// RegisterRoomDrift spawns the drift sweep goroutine. It returns when ctx is
// cancelled. Call once at world startup.
func RegisterRoomDrift(ctx context.Context, w *sim.World) {
	if w == nil {
		panic("cascade: RegisterRoomDrift requires a non-nil world")
	}
	w.RegisterTicker("room_drift", sim.RoomDriftSweepInterval)
	go runRoomDrift(ctx, w)
}

func runRoomDrift(ctx context.Context, w *sim.World) {
	ticker := time.NewTicker(sim.RoomDriftSweepInterval)
	defer ticker.Stop()
	// One command at a time draws from it: SendContext waits for each.
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.BeatTicker("room_drift")
			if _, err := w.SendContext(ctx, sim.DriftInRooms(time.Now().UTC(), rng)); err != nil && ctx.Err() == nil {
				log.Printf("cascade/room_drift: %v", err)
			}
		}
	}
}
