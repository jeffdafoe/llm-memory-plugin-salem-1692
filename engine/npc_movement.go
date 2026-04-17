package main

// NPC waypoint movement.
//
// A walk request (POST /api/village/npcs/:id/walk-to) computes an A* path from
// the NPC's current world position to the target and hands it to the client
// in a single npc_walking WS event carrying the full path + speed + start
// time. The client interpolates along the path locally.
//
// Server keeps the in-flight walk in memory only — no per-tick DB writes. On
// final arrival a single timer fires, writes current_x/y/facing to npc, and
// broadcasts npc_arrived.
//
// A new walk-to during an active walk cancels the pending arrival timer,
// recomputes from the NPC's interpolated current position, and fires a fresh
// event. Generation counters aren't needed — the client uses the latest
// npc_walking event authoritatively.

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"sync"
	"time"
)

const defaultNPCSpeed = 48.0 // world pixels per second

// npcWalk is the in-memory state for an active walk. Zeroed / deleted on
// arrival or cancellation.
type npcWalk struct {
	npcID       string
	startX      float64
	startY      float64
	path        []pathPoint
	speed       float64
	startedAt   time.Time
	finalFacing string
	timer       *time.Timer
}

// NPCMovement is the mutex-guarded map of active walks. One per App.
type NPCMovement struct {
	mu     sync.Mutex
	active map[string]*npcWalk
}

func newNPCMovement() *NPCMovement {
	return &NPCMovement{active: map[string]*npcWalk{}}
}

// currentPosition returns the NPC's interpolated world position based on how
// much of its path it's walked since startedAt. Clamps to path end if time
// has passed total walk duration.
func (w *npcWalk) currentPosition() (float64, float64) {
	elapsed := time.Since(w.startedAt).Seconds()
	remaining := elapsed * w.speed

	prev := pathPoint{X: w.startX, Y: w.startY}
	for _, wp := range w.path {
		legDist := math.Hypot(wp.X-prev.X, wp.Y-prev.Y)
		if remaining <= legDist {
			t := 0.0
			if legDist > 0 {
				t = remaining / legDist
			}
			return prev.X + (wp.X-prev.X)*t, prev.Y + (wp.Y-prev.Y)*t
		}
		remaining -= legDist
		prev = wp
	}
	return prev.X, prev.Y
}

// pathDuration returns total walk time in seconds given the speed.
func pathDuration(startX, startY float64, path []pathPoint, speed float64) float64 {
	total := 0.0
	prev := pathPoint{X: startX, Y: startY}
	for _, wp := range path {
		total += math.Hypot(wp.X-prev.X, wp.Y-prev.Y)
		prev = wp
	}
	return total / speed
}

// deriveFacing picks a cardinal direction from a vector. |dx| vs |dy| breaks
// which axis dominates; sign picks the direction.
func deriveFacing(dx, dy float64) string {
	if math.Abs(dx) > math.Abs(dy) {
		if dx > 0 {
			return "east"
		}
		return "west"
	}
	if dy > 0 {
		return "south"
	}
	return "north"
}

// finalFacingForPath returns the facing the NPC should end in, based on the
// last leg of the path.
func finalFacingForPath(startX, startY float64, path []pathPoint) string {
	if len(path) == 0 {
		return "south"
	}
	var fromX, fromY float64
	if len(path) == 1 {
		fromX, fromY = startX, startY
	} else {
		fromX, fromY = path[len(path)-2].X, path[len(path)-2].Y
	}
	last := path[len(path)-1]
	return deriveFacing(last.X-fromX, last.Y-fromY)
}

// handleWalkTo is POST /api/village/npcs/{id}/walk-to. Body: {x, y, speed?}.
// Computes a path and fires npc_walking. Cancels any existing walk first,
// using the NPC's interpolated current position as the new start.
func (app *App) handleWalkTo(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil {
		jsonError(w, "Auth required", http.StatusUnauthorized)
		return
	}

	npcID := r.PathValue("id")
	if npcID == "" {
		jsonError(w, "Missing NPC id", http.StatusBadRequest)
		return
	}

	var req struct {
		X     float64 `json:"x"`
		Y     float64 `json:"y"`
		Speed float64 `json:"speed"` // optional override
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	speed := req.Speed
	if speed <= 0 {
		speed = defaultNPCSpeed
	}

	// Determine current position: either the in-memory walk's interpolated
	// spot (if we're walking) or the NPC's persisted current_x/y.
	app.NPCMovement.mu.Lock()
	existing := app.NPCMovement.active[npcID]
	app.NPCMovement.mu.Unlock()

	var startX, startY float64
	if existing != nil {
		startX, startY = existing.currentPosition()
		// Cancel the old timer — we'll schedule a new arrival below.
		if existing.timer != nil {
			existing.timer.Stop()
		}
	} else {
		if err := app.DB.QueryRow(r.Context(),
			`SELECT current_x, current_y FROM npc WHERE id = $1`, npcID,
		).Scan(&startX, &startY); err != nil {
			jsonError(w, "NPC not found", http.StatusNotFound)
			return
		}
	}

	// Pathfind from current to target.
	grid, err := app.loadWalkGrid(r.Context())
	if err != nil {
		log.Printf("npc walk: walkgrid load failed: %v", err)
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	startTile := gridPoint{}
	startTile.X, startTile.Y = worldToTile(startX, startY)
	goalTile := gridPoint{}
	goalTile.X, goalTile.Y = worldToTile(req.X, req.Y)

	tilePath := findPath(grid, startTile, goalTile)
	if tilePath == nil {
		jsonError(w, "No path to target", http.StatusBadRequest)
		return
	}

	// Convert to world waypoints, skipping the first tile (it's the NPC's
	// current tile — she's already there). The last waypoint is the target
	// tile's center.
	worldPath := make([]pathPoint, 0, len(tilePath))
	for i, t := range tilePath {
		if i == 0 {
			continue
		}
		worldPath = append(worldPath, tileToWorld(t.X, t.Y))
	}
	if len(worldPath) == 0 {
		// Start and goal tile are the same — no-op walk. Treat as arrival.
		jsonResponse(w, http.StatusOK, map[string]any{"already_there": true})
		return
	}

	facing := finalFacingForPath(startX, startY, worldPath)
	startedAt := time.Now()
	duration := pathDuration(startX, startY, worldPath, speed)

	walk := &npcWalk{
		npcID:       npcID,
		startX:      startX,
		startY:      startY,
		path:        worldPath,
		speed:       speed,
		startedAt:   startedAt,
		finalFacing: facing,
	}
	walk.timer = time.AfterFunc(time.Duration(duration*float64(time.Second)), func() {
		app.applyArrival(npcID)
	})

	app.NPCMovement.mu.Lock()
	app.NPCMovement.active[npcID] = walk
	app.NPCMovement.mu.Unlock()

	app.Hub.Broadcast(WorldEvent{
		Type: "npc_walking",
		Data: map[string]any{
			"id":         npcID,
			"start_x":    startX,
			"start_y":    startY,
			"path":       worldPath,
			"speed":      speed,
			"started_at": startedAt.UTC().Format(time.RFC3339Nano),
		},
	})

	jsonResponse(w, http.StatusOK, map[string]any{
		"path_length":  len(worldPath),
		"duration_sec": duration,
		"final_facing": facing,
	})
}

// applyArrival runs when an active walk's timer fires. Persists the NPC's
// final position + facing, broadcasts npc_arrived, and clears in-memory
// state. Idempotent on missing state (timer could fire after a manual
// cancellation; we just no-op).
func (app *App) applyArrival(npcID string) {
	app.NPCMovement.mu.Lock()
	walk := app.NPCMovement.active[npcID]
	if walk == nil {
		app.NPCMovement.mu.Unlock()
		return
	}
	delete(app.NPCMovement.active, npcID)
	app.NPCMovement.mu.Unlock()

	end := walk.path[len(walk.path)-1]
	ctx := context.Background()
	if _, err := app.DB.Exec(ctx,
		`UPDATE npc SET current_x = $2, current_y = $3, facing = $4 WHERE id = $1`,
		npcID, end.X, end.Y, walk.finalFacing,
	); err != nil {
		log.Printf("npc arrival: UPDATE failed for %s: %v", npcID, err)
		return
	}
	app.Hub.Broadcast(WorldEvent{
		Type: "npc_arrived",
		Data: map[string]any{
			"id":     npcID,
			"x":      end.X,
			"y":      end.Y,
			"facing": walk.finalFacing,
		},
	})
}
