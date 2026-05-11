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
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
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
	// targetStructureID is the structure the walk is destined for, when
	// the caller knew one. Walks to a structure land on its loiter
	// point (or door); the actor may or may not enter on arrival
	// depending on entry_policy + association — but the structure is
	// the truth of "where you walked to" regardless of entry. Empty
	// for coordinate-only walks (admin /api/move, ad-hoc relocations).
	// Set via markWalkTargetStructure right after startNPCWalk by the
	// callers who know the target (startReturnWalk, the handlePCMove
	// structure click). Read in applyArrival so arrival-side hooks
	// (e.g. closed-business narration) get the structure even when
	// inside_structure_id never flipped.
	targetStructureID string
}

// NPCMovement is the mutex-guarded map of active walks. One per App.
type NPCMovement struct {
	mu     sync.Mutex
	active map[string]*npcWalk
}

func newNPCMovement() *NPCMovement {
	return &NPCMovement{active: map[string]*npcWalk{}}
}

// actorHasWalkInFlight reports whether an arrival timer is pending for
// this actor — i.e. a walk has been dispatched but applyArrival has
// not yet fired. Used by the speak commit gate in executeAgentCommit
// to refuse a speak that was chained after a move_to in the same
// tool batch (ZBBS-HOME-237).
func (app *App) actorHasWalkInFlight(actorID string) bool {
	app.NPCMovement.mu.Lock()
	defer app.NPCMovement.mu.Unlock()
	w, ok := app.NPCMovement.active[actorID]
	return ok && w != nil
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

	// Mirror handlePCMove's raw-coord cleanup: clear any prior route
	// before raw-coord walk so a stale route from a previous routed walk
	// doesn't fire on this walk's arrival. See the comment there for the
	// observed symptom shape.
	app.clearBehavior(npcID)
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

	worldPath := make([]pathPoint, 0, len(tilePath)+1)
	for i, t := range tilePath {
		if i == 0 {
			continue
		}
		worldPath = append(worldPath, tileToWorld(t.X, t.Y))
	}
	// Off-grid goal extension (ZBBS-HOME-224). Visitor despawn walks
	// target a tile a few past the map edge so the actor visibly
	// exits the village instead of stopping ON the visible boundary.
	// Pathfinding stays in-grid (findPathToAdjacent above pathed to
	// the closest in-grid tile); appending the off-grid target as a
	// synthetic final pathPoint animates the actor past the edge
	// before applyArrival fires off-grid. Caller-opt-in via passing
	// off-grid (targetX, targetY); in-grid targets are unaffected.
	if goalTile.X < 0 || goalTile.X >= mapW || goalTile.Y < 0 || goalTile.Y >= mapH {
		worldPath = append(worldPath, pathPoint{X: targetX, Y: targetY})
	}
	if len(worldPath) == 0 {
		// Start and goal tile are the same — walk is a no-op. Run the
		// per-arrival side-effects directly (object refresh, npc_arrived
		// broadcast, behavior + errand advancement) so client handlers like
		// the noticeboard walk-then-read panel re-fire on a re-click and
		// "click well when already adjacent" still quenches thirst. We skip
		// the position UPDATE (nothing changed) and the entrance-only
		// downstream work (village/room arrival events, co-located cascades,
		// self-tick) — those are about a fresh entrance, not a re-click on
		// a place we're already at. Don't flip inside here: if the NPC was
		// inside their structure, walking to their own tile shouldn't pop
		// them out.
		//
		// Read the actor's current facing from DB so the npc_arrived
		// broadcast carries a real direction (south_idle / east_idle /
		// etc) — empty facing on the wire makes the client compose
		// "_idle" as the animation name, which doesn't exist, so the
		// previous walk animation keeps cycling on a stationary actor
		// (ZBBS-HOME-225).
		var nopFacing string
		_ = app.DB.QueryRow(ctx, `SELECT facing FROM actor WHERE id = $1`, npcID).Scan(&nopFacing)
		if nopFacing == "" {
			nopFacing = "south"
		}
		go app.applyArrivalSideEffects(context.Background(), npcID, startX, startY, nopFacing, "")
		return &startNPCWalkResult{AlreadyThere: true, FinalFacing: nopFacing}, nil
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
// final position + facing, then runs the shared per-arrival side-effects
// and the entrance-only downstream work (village/room arrival events,
// co-located cascades, self-tick).
//
// Idempotent on missing walk state — same-tile no-op walks bypass this
// function and call applyArrivalSideEffects directly from startNPCWalk;
// a stray invocation here with no registered walk is a no-op.
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
		`UPDATE actor SET current_x = $2, current_y = $3, facing = $4 WHERE id = $1`,
		npcID, end.X, end.Y, walk.finalFacing,
	); err != nil {
		log.Printf("npc arrival: UPDATE failed for %s: %v", npcID, err)
		return
	}

	// Object refresh + npc_arrived broadcast + behavior/errand
	// advancement. Shared with same-tile no-op walks called from
	// startNPCWalk so both paths produce the same client/event ordering.
	app.applyArrivalSideEffects(ctx, npcID, end.X, end.Y, walk.finalFacing, walk.targetStructureID)

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
	var arriverIsPC bool
	var displayName string
	var insideID sql.NullString
	if err := app.DB.QueryRow(ctx,
		`SELECT display_name, inside_structure_id,
		        llm_memory_agent IS NOT NULL,
		        login_username IS NOT NULL
		   FROM actor WHERE id = $1`,
		npcID,
	).Scan(&displayName, &insideID, &arriverIsAgent, &arriverIsPC); err == nil && insideID.Valid {
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
		data := map[string]interface{}{
			"actor_id":     npcID,
			"actor_name":   displayName,
			"kind":         "arrival",
			"text":         text,
			"structure_id": insideID.String,
			"at":           time.Now().UTC().Format(time.RFC3339),
		}
		app.addRoomScopeToData(ctx, data, npcID)
		app.Hub.Broadcast(WorldEvent{Type: "room_event", Data: data})

		if arriverIsAgent || arriverIsPC {
			// Cascade origin (MEM-121): a fresh scene UUID for the
			// arrival. Walks finish seconds-to-minutes of game time
			// after the move_to that started them, so by the time
			// we get here it's a new scene, not a continuation of
			// whatever scene triggered the original move_to.
			//
			// PC arrivals force=true (PC actions are rare and
			// significant — never cost-gate a player presence).
			// Agent arrivals respect the cost guard (force=false).
			// Decorative NPC arrivals don't fire ticks at all —
			// their schedule-driven movement is background, not a
			// signal worth reacting to.
			force := arriverIsPC
			app.triggerCoLocatedTicks(ctx, insideID.String, npcID, "arrival", force, app.newScene(ctx, insideID.String), npcID)
		}
	}

	// Loiter-arrival: NPC walked TO a structure but didn't enter
	// (entry_policy=owner with non-owner arriver, or entry_policy=none
	// for outdoor loiter targets like wells / market stalls). Without
	// this branch the keeper inside an entry_policy=owner stall never
	// sees a non-owner NPC arrive at the counter, and deliver_order /
	// pay co-location gates (sameStructure || sameHuddle) reject any
	// vendor-to-vendor transaction at the buyer's stall — the seller
	// has neither the buyer's inside_structure_id nor any
	// current_huddle_id even though they're physically at the door.
	// Observed 2026-05-08 with Prudence Ward delivering coca_tea to
	// Josiah at the General Store: she landed at the loiter slot with
	// current_huddle_id=NULL, deliver_order rejected, no recovery.
	//
	// Mirrors the inside-arrival block above scoped to walk.target
	// StructureID. Decorative NPCs (no llm_memory_agent) and PCs are
	// excluded — decoratives don't interact with vendors, and PCs use
	// fireKnockPerception via pc_handlers.go.
	if !insideID.Valid && walk.targetStructureID != "" && arriverIsAgent && !arriverIsPC {
		structureID := walk.targetStructureID
		var loiterHuddleID string
		err := app.DB.QueryRow(ctx,
			`SELECT id::text FROM scene_huddle
			  WHERE structure_id = $1 AND concluded_at IS NULL
			  ORDER BY created_at DESC LIMIT 1`,
			structureID,
		).Scan(&loiterHuddleID)
		if err == nil {
			// Join the active huddle if not already in one. The
			// IS NULL gate prevents yanking an NPC out of an
			// existing conversation at an adjacent loiter pin.
			var currentHuddle sql.NullString
			if err := app.DB.QueryRow(ctx,
				`SELECT current_huddle_id::text FROM actor WHERE id = $1`,
				npcID,
			).Scan(&currentHuddle); err == nil && !currentHuddle.Valid {
				if _, err := app.joinOrCreateHuddle(ctx, npcID, structureID); err != nil {
					log.Printf("loiter-arrival: join %s into %s: %v", npcID, structureID, err)
				}
			}

			// Room_event narration so PCs and NPCs inside see the
			// arrival in the talk panel — symmetric with the
			// inside-arrival room_event above.
			structName := app.lookupStructureName(ctx, structureID)
			if structName == "" {
				structName = "a building"
			}
			text := fmt.Sprintf("%s arrived at %s.", displayName, structName)
			data := map[string]interface{}{
				"actor_id":     npcID,
				"actor_name":   displayName,
				"kind":         "arrival",
				"text":         text,
				"structure_id": structureID,
				"at":           time.Now().UTC().Format(time.RFC3339),
			}
			app.addRoomScopeToData(ctx, data, npcID)
			app.Hub.Broadcast(WorldEvent{Type: "room_event", Data: data})

			// Trigger co-located ticks on the structure so the
			// keeper inside reacts. Force=false: routine NPC
			// arrivals respect the cost guard, same as inside
			// arrivals above.
			app.triggerCoLocatedTicks(ctx, structureID, npcID, "loiter-arrival", false, app.newScene(ctx, structureID), npcID)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("loiter-arrival: huddle lookup %s: %v", structureID, err)
		}
	}

	// Self-tick on agent-driven arrivals — even when the arriver is alone
	// (well, outhouse, market stall outdoors). Without this, an NPC who
	// walks somewhere via chore/move_to and ends up alone has no way to
	// decide "I'm here, now what" until something else cascades. Their
	// agent_override_until pin keeps the scheduler off them, so they
	// stand frozen at the destination for 30 minutes. Force=true to
	// bypass the 5-min cost guard — the move_to/chore that brought them
	// here was within that window.
	//
	// EXCEPTION (ZBBS-107): respect agent_override_until even on the
	// arriver-self-tick path. The summon errand walks the summoner to
	// a summon_point and pins their override for the duration of the
	// errand; without this skip, the arriver would tick on summon_point
	// arrival and immediately walk back home, bypassing the wait for
	// the messenger. force=true bypasses the cost guard but should not
	// bypass an explicit "engine owns this NPC right now" pin.
	//
	// Decorative NPCs (no llm_memory_agent) don't tick; their walks are
	// scheduler-driven and they don't need to reflect on arrival.
	// PCs don't tick either — no LLM tool surface.
	if arriverIsAgent && !arriverIsPC {
		var overrideUntil sql.NullTime
		_ = app.DB.QueryRow(ctx,
			`SELECT agent_override_until FROM actor WHERE id = $1`,
			npcID,
		).Scan(&overrideUntil)
		if overrideUntil.Valid && overrideUntil.Time.After(time.Now()) {
			log.Printf("event-tick %s: skipped on arrival — agent_override_until pinned until %s",
				displayName, overrideUntil.Time.Format(time.RFC3339))
		} else {
			go app.triggerImmediateTick(context.Background(), npcID, "arrived", true, app.newScene(ctx, insideID.String), npcID)
		}
	}
}

// markWalkTargetStructure records the structure id that the in-flight
// walk for npcID is destined for. Called by walk-starting paths that
// know they're targeting a specific structure (startReturnWalk,
// handlePCMove's structure click). Safe no-op when no walk is active
// for the actor — startNPCWalk may have rejected the walk request, in
// which case the setter has nothing to mark.
//
// Stored on the npcWalk so applyArrival can read it on completion and
// pass it to arrival-side hooks. The structure stays the truth across
// entry / no-entry outcomes — owner-policy walks land at the loiter
// point without flipping inside_structure_id, and we still want
// arrival hooks to know which structure was walked to.
func (app *App) markWalkTargetStructure(npcID, structureID string) {
	if npcID == "" || structureID == "" {
		return
	}
	app.NPCMovement.mu.Lock()
	defer app.NPCMovement.mu.Unlock()
	if walk, ok := app.NPCMovement.active[npcID]; ok && walk != nil {
		walk.targetStructureID = structureID
	}
}

// applyArrivalSideEffects runs the per-arrival side-effects shared between
// real walk completions (via applyArrival) and same-tile no-op walks called
// directly from startNPCWalk: object refresh at the arrival point, the
// npc_arrived broadcast, and behavior + errand advancement.
//
// Real-walk callers UPDATE the actor's row first; no-op walks skip the
// UPDATE since position is unchanged. Entrance-only work (village/room
// arrival events, co-located cascades, self-tick) stays in applyArrival —
// a re-click on a place you're already at is not a fresh entrance.
//
// targetStructureID is the structure the walk was destined for (set by
// callers via markWalkTargetStructure). Empty for coordinate-only walks.
// Used by arrival-side hooks that need to know "you walked to X" even
// when inside_structure_id never flipped (owner-policy structures
// where the actor isn't allowed in still walked TO that structure).
func (app *App) applyArrivalSideEffects(ctx context.Context, npcID string, x, y float64, facing string, targetStructureID string) {
	// Buy / fulfill walker arrivals (ZBBS-HOME-244 / -247) —
	// short-circuit if this arrival is part of an in-progress trip.
	// We return early after a walker handles arrival: the broader
	// side-effects below (object_refresh consumption at the seller's
	// well, closed-business narration at the customer's stall,
	// auto-sleep on arriving at a non-home structure, etc.) are
	// intended for "an NPC arrived somewhere of their own decision"
	// — not for a delivery / restock detour where the walker is
	// driving and the destination isn't meaningful to the actor's
	// state. The walker re-dispatches a return walk that fires this
	// function again on completion, at which point the side-effects
	// run for that arrival. An NPC won't be in both walkers at once.
	if app.handleBuyWalkerArrival(ctx, npcID, targetStructureID) {
		log.Printf("arrival: handled by buy_walker for %s at structure=%s",
			npcID, targetStructureID)
		return
	}
	if app.handleFulfillWalkerArrival(ctx, npcID, targetStructureID) {
		log.Printf("arrival: handled by fulfill_walker for %s at structure=%s",
			npcID, targetStructureID)
		return
	}

	if _, err := app.applyObjectRefreshAtArrival(ctx, npcID, x, y); err != nil {
		log.Printf("object_refresh: %s at (%.1f,%.1f): %v", npcID, x, y, err)
	}
	app.Hub.Broadcast(WorldEvent{
		Type: "npc_arrived",
		Data: map[string]any{
			"id":     npcID,
			"x":      x,
			"y":      y,
			"facing": facing,
		},
	})
	app.advanceBehavior(npcID)
	app.advanceErrandFromArrival(ctx, npcID)

	// ZBBS-155: PC-only gatherable pickup on walk arrival. Lookup the
	// actor's display name + login_username gate in one query so this
	// stays a no-op for NPCs (no extra round-trip when the path doesn't
	// fire). The pickup itself runs only when login_username is set.
	var gatherActorName string
	var gatherIsPC bool
	if err := app.DB.QueryRow(ctx,
		`SELECT display_name, login_username IS NOT NULL FROM actor WHERE id = $1`,
		npcID,
	).Scan(&gatherActorName, &gatherIsPC); err == nil && gatherIsPC {
		app.pickupNearbyGatherables(ctx, npcID, gatherActorName, x, y)
	}

	// Clear agent_override_until on arrival. The override was set by
	// chore / move_to / relocateVisitors as a fixed 30-minute window
	// "long enough to cover any walk." Actual walks finish in 1-3
	// minutes, leaving the NPC stuck on a stale override for ~25+
	// minutes after arrival — observed in play with Prudence drinking
	// at the well at 01:25, finishing 50 seconds later, and standing
	// silent on the loiter slot until the override expired at 01:55.
	// Tying the override to the walk-it-covers frees the scheduler
	// to tick the NPC again as soon as the walk is actually done.
	//
	// Preserved when:
	//   - On break (break_until > NOW): take_break legitimately holds
	//     the override past arrival home for the full break duration.
	//   - Active summon errand involving this actor: the summoner
	//     waits at the summon point until the messenger returns;
	//     the messenger waits at the target during chat-at-target.
	//     The errand state machine in summon_errand.go clears its
	//     own override on terminal transitions, so this skip just
	//     defers to those.
	if _, err := app.DB.Exec(ctx,
		`UPDATE actor
		    SET agent_override_until = NULL
		  WHERE id = $1::uuid
		    AND (break_until IS NULL OR break_until <= NOW())
		    AND NOT EXISTS (
		      SELECT 1 FROM summon_errand
		       WHERE state NOT IN ('done', 'failed')
		         AND (summoner_id = $1::uuid OR messenger_id = $1::uuid)
		    )`,
		npcID,
	); err != nil {
		log.Printf("arrival: clear override %s: %v", npcID, err)
	}

	// ZBBS-175: NPC auto-sleep on arrival home. Cheap no-op for non-NPCs
	// or NPCs not at their home_structure_id; eligible NPCs get bedded
	// here so arrival home triggers sleep without a periodic sweep.
	app.maybeNPCAutoSleep(ctx, npcID)

	// ZBBS-179: brown-panel narration for PCs arriving at a closed
	// business. No-op for NPCs (gated inside the helper). Pass the
	// walk's targetStructureID rather than reading inside_structure_id
	// — owner-policy walks land at the loiter point without entering,
	// and inside_structure_id stays NULL there. The structure they
	// walked TO is the truth, regardless of entry.
	if targetStructureID != "" {
		app.maybeNarrateClosedBusinessArrival(ctx, npcID, targetStructureID)
	}

	// ZBBS-183: keeper arriving at their own workplace pulls in any PC
	// waiting at the loiter slot. Inverse of the PC-side loiter-huddle:
	// when the PC came first and the keeper wasn't there yet, the
	// PC stayed outside in no huddle while the structure huddle was
	// born around the lone keeper on arrival — perception saw nobody
	// to greet. The helper gates on work_structure_id match so visitor
	// entries (lodger to tavern, drop-in to a different shop) don't
	// accidentally adopt PCs.
	if targetStructureID != "" {
		app.maybeAdoptWaitingPCsAtArrival(ctx, npcID, targetStructureID)
	}

	// ZBBS-HOME-237: visitor-arrival huddle adoption. NPCs walking to a
	// structure they don't enter (owner-policy non-owner, none-policy,
	// agent-visitor anyone-policy with EnterOnArrival=false) get joined
	// into the destination's active scene_huddle when their arrival
	// position is on the loiter ring. Without this, an NPC who walks
	// across town to converse with a shopkeeper has no huddle scope on
	// arrival: a follow-up speak() goes to the wrong scope and the
	// keeper never perceives them. PC-side equivalent lives in
	// handlePCMove (pc_handlers.go).
	//
	// Skipped for actors who entered the structure on arrival —
	// setNPCInside already handled their huddle membership via
	// joinOrCreateHuddle.
	//
	// On a successful join with other members already present, fan out
	// a co-located tick for the existing members AND a self-tick for
	// the new arrival, so the just-arrived visitor can decide what to
	// say with fresh perception that includes the existing members
	// rather than waiting for idle-sweep.
	if targetStructureID != "" {
		var insideID sql.NullString
		var hasAgent bool
		_ = app.DB.QueryRow(ctx,
			`SELECT inside_structure_id::text, llm_memory_agent IS NOT NULL FROM actor WHERE id = $1`,
			npcID,
		).Scan(&insideID, &hasAgent)
		// NPC-only: PC arrivals get their loiter-huddle handled in
		// handlePCMove (pc_handlers.go:1165) using joinOrCreateHuddleForPC
		// for PC-specific acquaintance shape. Skipping PCs here also
		// avoids a double-join (handlePCMove already wrote
		// current_huddle_id by the time the walk arrives).
		if hasAgent && !insideID.Valid {
			if huddleID, others := app.adoptVisitorLoiterHuddle(ctx, npcID, targetStructureID, x, y); huddleID != "" && others > 0 {
				log.Printf("visitor-loiter-huddle: %s joined %s at %s (others=%d) — fanning out", npcID, huddleID, targetStructureID, others)
				// One scene shared between the arrival self-tick and the
				// existing members' reaction ticks so they group together
				// in chat history. Without an arrivalScene minted here,
				// both ticks would write rows with scene_id=NULL and the
				// shared-VA read path (MEM-132) would orphan them on the
				// next tick.
				arrivalScene := app.newScene(ctx, targetStructureID)
				app.triggerCoLocatedTicks(ctx, targetStructureID, npcID, "visitor-arrival", false, arrivalScene, npcID)
				app.triggerImmediateTick(ctx, npcID, "arrival-into-populated-huddle", false, arrivalScene, "")
			}
		}
	}
}
