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
	"database/sql"
	"encoding/json"
	"fmt"
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
// Thin wrapper around startNPCWalk that validates auth and decodes the body.
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

	result, err := app.startNPCWalk(r.Context(), npcID, req.X, req.Y, speed)
	if err != nil {
		if err.Error() == "npc not found" {
			jsonError(w, "NPC not found", http.StatusNotFound)
			return
		}
		if err.Error() == "no path" {
			jsonError(w, "No path to target", http.StatusBadRequest)
			return
		}
		log.Printf("npc walk: %v", err)
		jsonError(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	jsonResponse(w, http.StatusOK, result)
}

// startNPCWalkResult is the summary returned to both HTTP callers and
// internal behavior code.
type startNPCWalkResult struct {
	PathLength  int     `json:"path_length"`
	DurationSec float64 `json:"duration_sec"`
	FinalFacing string  `json:"final_facing"`
	AlreadyThere bool   `json:"already_there,omitempty"`
}

// startNPCWalk computes a path from the NPC's current position (interpolated
// if a walk is in progress) to (targetX, targetY), cancels any existing walk,
// schedules arrival via time.AfterFunc, and broadcasts npc_walking. Behavior
// code (lamplighter etc.) calls this for each leg of a routine.
//
// If the target tile is impassable (e.g., walking to a lamp which itself is
// an obstacle) we fall back to findPathToAdjacent and path to a walkable
// neighbor of the target — so "walk to this lamp" means "walk up to it."
func (app *App) startNPCWalk(ctx context.Context, npcID string, targetX, targetY, speed float64) (*startNPCWalkResult, error) {
	if speed <= 0 {
		speed = defaultNPCSpeed
	}

	// Walk-trace (debug). Logs every walk-start with actor id and target
	// so we can see who the engine is moving and from where in the call
	// stack. Cheap (one log line per walk), removable later. Added while
	// chasing "PC walks autonomously" reports.
	log.Printf("walk-start: actor=%s target=(%.0f,%.0f) speed=%.0f", npcID, targetX, targetY, speed)

	// NOTE: inside=false used to flip HERE, before pathfinding. That left
	// an NPC outside their structure (and any occupancy-tagged state flipped
	// to "unoccupied") when pathfinding later returned "no path" — 400 to
	// the caller but permanent state damage until manually fixed. The flip
	// is now deferred until we know a real walk is going to start, see
	// below.

	app.NPCMovement.mu.Lock()
	existing := app.NPCMovement.active[npcID]
	app.NPCMovement.mu.Unlock()

	var startX, startY float64
	if existing != nil {
		startX, startY = existing.currentPosition()
		if existing.timer != nil {
			existing.timer.Stop()
		}
		// The interrupted walk's applyArrival will never fire, so persist
		// the interpolated position here. Without this, a client that
		// refreshes during a chain of supersessions sees the NPC at some
		// stale pre-first-walk position — the "villagers at random spots"
		// symptom.
		if _, err := app.DB.Exec(ctx,
			`UPDATE actor SET current_x = $2, current_y = $3 WHERE id = $1`,
			npcID, startX, startY,
		); err != nil {
			log.Printf("persist interrupted pos for %s: %v", npcID, err)
		}
	} else {
		if err := app.DB.QueryRow(ctx,
			`SELECT current_x, current_y FROM actor WHERE id = $1`, npcID,
		).Scan(&startX, &startY); err != nil {
			return nil, fmt.Errorf("npc not found")
		}
	}

	grid, err := app.loadWalkGrid(ctx)
	if err != nil {
		return nil, fmt.Errorf("walkgrid: %w", err)
	}
	startTile := gridPoint{}
	startTile.X, startTile.Y = worldToTile(startX, startY)
	goalTile := gridPoint{}
	goalTile.X, goalTile.Y = worldToTile(targetX, targetY)

	// Try exact path first; fall back to adjacent if goal is impassable.
	tilePath := findPath(grid, startTile, goalTile)
	if tilePath == nil && !grid.canWalk(goalTile.X, goalTile.Y) {
		tilePath, _ = findPathToAdjacent(grid, startTile, goalTile)
	}
	if tilePath == nil {
		return nil, fmt.Errorf("no path")
	}

	worldPath := make([]pathPoint, 0, len(tilePath))
	for i, t := range tilePath {
		if i == 0 {
			continue
		}
		worldPath = append(worldPath, tileToWorld(t.X, t.Y))
	}
	if len(worldPath) == 0 {
		// Start and goal tile are the same — walk is a no-op. Fire arrival
		// immediately so behavior hooks advance to the next step. Don't
		// flip inside here: if the NPC was inside their structure, walking
		// to their own tile shouldn't pop them out.
		go app.applyArrival(npcID)
		return &startNPCWalkResult{AlreadyThere: true, FinalFacing: ""}, nil
	}

	// Path verified. NOW it's safe to flip inside=false so the client can
	// un-hide the sprite for the walk animation. setNPCInside is a no-op
	// when already outside (guards on IS DISTINCT FROM).
	app.setNPCInside(ctx, npcID, false, "")

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

	return &startNPCWalkResult{
		PathLength:  len(worldPath),
		DurationSec: duration,
		FinalFacing: facing,
	}, nil
}

// applyArrival runs when an active walk's timer fires. Persists the NPC's
// final position + facing, broadcasts npc_arrived, clears in-memory state,
// and invokes any active behavior's advance hook so scheduled routines
// (lamplighter, washerwoman, ...) step to the next action.
//
// Idempotent on missing walk state — a timer that fires after manual
// cancellation is a no-op.
func (app *App) applyArrival(npcID string) {
	app.NPCMovement.mu.Lock()
	walk := app.NPCMovement.active[npcID]
	if walk == nil {
		app.NPCMovement.mu.Unlock()
		// Still give behavior a chance to advance — a no-op walk (same tile
		// source and destination) fires arrival without ever registering a
		// walk in NPCMovement.active.
		app.advanceBehavior(npcID)
		return
	}
	delete(app.NPCMovement.active, npcID)
	app.NPCMovement.mu.Unlock()

	end := walk.path[len(walk.path)-1]
	ctx := context.Background()
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor SET current_x = $2, current_y = $3, facing = $4 WHERE id = $1`,
		npcID, end.X, end.Y, walk.finalFacing,
	); err != nil {
		log.Printf("npc arrival: UPDATE failed for %s: %v", npcID, err)
		return
	}

	// Apply any object-refresh side-effect for the arrival point. PC and
	// NPC share this path; the spatial lookup in
	// applyObjectRefreshAtArrival picks the nearest refresh-tagged object
	// within tolerance and decrements the configured attribute(s) on the
	// actor. Errors are logged but don't block the arrival flow — the
	// position update has already committed and the client needs the
	// npc_arrived event regardless.
	if _, err := app.applyObjectRefreshAtArrival(ctx, npcID, end.X, end.Y); err != nil {
		log.Printf("object_refresh: %s at (%.1f,%.1f): %v", npcID, end.X, end.Y, err)
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

	app.advanceBehavior(npcID)

	// Event-tick co-located agents on agent-driven arrivals (M6.5
	// pre-staging via ZBBS-075). When an agentized NPC enters a
	// structure, fire immediate ticks on any OTHER agentized NPCs
	// already inside so they can react to the entrance. Background
	// (scheduler-driven) NPC arrivals don't trigger ticks — only
	// agent-on-agent. Cost guard inside triggerImmediateTick prevents
	// tick storms.
	//
	// Also: any NPC arrival inside a structure (agent or scheduler-
	// driven) gets a village_event row so the Village tab renders
	// "Ezekiel arrived at the Blacksmith." regardless of whether the
	// arrival cascaded into other ticks.
	var arriverIsAgent bool
	var displayName string
	var insideID sql.NullString
	if err := app.DB.QueryRow(ctx,
		`SELECT display_name, inside_structure_id, llm_memory_agent IS NOT NULL FROM actor WHERE id = $1`,
		npcID,
	).Scan(&displayName, &insideID, &arriverIsAgent); err == nil && insideID.Valid {
		structName := app.lookupStructureName(ctx, insideID.String)
		if structName == "" {
			structName = "a building"
		}
		text := fmt.Sprintf("%s arrived at %s.", displayName, structName)
		x, y := end.X, end.Y
		app.recordVillageEvent(ctx, villageEventArrival, text, npcID, insideID.String, &x, &y)

		// Parallel room_event so the talk panel's room log shows the
		// arrival narration alongside speech and acts. Symmetric with
		// the departure room_event the agent move_to commit emits in
		// executeAgentCommit. Without this, a PC inside the tavern
		// watches villagers walk in with no on-screen acknowledgement.
		app.Hub.Broadcast(WorldEvent{
			Type: "room_event",
			Data: map[string]interface{}{
				"actor_id":     npcID,
				"actor_name":   displayName,
				"kind":         "arrival",
				"text":         text,
				"structure_id": insideID.String,
				"at":           time.Now().UTC().Format(time.RFC3339),
			},
		})

		if arriverIsAgent {
			// Cascade origin (MEM-121): a fresh scene UUID for the
			// arrival. Walks finish seconds-to-minutes of game time
			// after the move_to that started them, so by the time
			// we get here it's a new scene, not a continuation of
			// whatever scene triggered the original move_to.
			app.triggerCoLocatedTicks(ctx, insideID.String, npcID, "arrival", false, newUUIDv7(), npcID)
			// Cascade origin — fire the chronicler alongside the
			// reactor ticks. Once per arrival, not per in-cascade
			// NPC reaction.
			app.cascadeOriginFireChronicler("arrival", insideID.String)
		}
	}
}
