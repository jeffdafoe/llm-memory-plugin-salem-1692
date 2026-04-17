package main

// Scheduled NPC behaviors that piggyback on the movement infrastructure.
//
// Milestone 4 introduces the 'lamplighter' behavior: at dusk, a designated
// NPC (npc.behavior = 'lamplighter') walks from their current position to
// each village_object whose asset has a night-active state tag, flipping the
// object's state on arrival, then returns to their home. At dawn the same
// routine runs in reverse over day-active states.
//
// The state machine is per-NPC, held in App.NPCBehaviors. Each arrival at a
// waypoint invokes advanceBehavior which flips the target object, then
// starts the next walk (or the return-home walk, or clears the state).

import (
	"context"
	"log"
	"sync"
)

const (
	behaviorLamplighter = "lamplighter"
)

// lamplighterStop is one object the lamplighter visits on his route.
type lamplighterStop struct {
	ObjectID string
	WalkX    float64 // world-pixel coord of the adjacent walkable tile
	WalkY    float64
	NewState string
}

// lamplighterRoute is the in-memory state machine for a running lamplighter
// routine. StopIdx is the index of the stop the NPC is CURRENTLY heading
// toward (or has most recently finished, right before we advance). Phase
// toggles to "returning" after the last stop so arrival at home doesn't
// trigger a state flip.
type lamplighterRoute struct {
	NPCID   string
	Stops   []lamplighterStop
	StopIdx int
	HomeX   float64
	HomeY   float64
	Phase   string // "lighting" or "returning"
}

// NPCBehaviors tracks active behavior state machines keyed by NPC id.
type NPCBehaviors struct {
	mu     sync.Mutex
	active map[string]*lamplighterRoute
}

func newNPCBehaviors() *NPCBehaviors {
	return &NPCBehaviors{active: map[string]*lamplighterRoute{}}
}

// advanceBehavior is the arrival hook. If the NPC has an active behavior,
// flip the target object for the just-arrived stop (in 'lighting' phase),
// then start the next walk — or transition to 'returning' + walk home — or
// clear the state machine when home is reached.
func (app *App) advanceBehavior(npcID string) {
	app.NPCBehaviors.mu.Lock()
	route := app.NPCBehaviors.active[npcID]
	app.NPCBehaviors.mu.Unlock()
	if route == nil {
		return
	}

	ctx := context.Background()

	// If we were lighting a stop, flip that object now.
	if route.Phase == "lighting" && route.StopIdx < len(route.Stops) {
		stop := route.Stops[route.StopIdx]
		if _, err := app.DB.Exec(ctx,
			`UPDATE village_object SET current_state = $2
			 WHERE id = $1 AND current_state IS DISTINCT FROM $2`,
			stop.ObjectID, stop.NewState,
		); err != nil {
			log.Printf("lamplighter: flip %s → %s failed: %v", stop.ObjectID, stop.NewState, err)
		} else {
			app.Hub.Broadcast(WorldEvent{
				Type: "object_state_changed",
				Data: map[string]string{"id": stop.ObjectID, "state": stop.NewState},
			})
		}
		route.StopIdx++
	}

	// Decide next action.
	if route.Phase == "lighting" && route.StopIdx < len(route.Stops) {
		next := route.Stops[route.StopIdx]
		if _, err := app.startNPCWalk(ctx, npcID, next.WalkX, next.WalkY, defaultNPCSpeed); err != nil {
			log.Printf("lamplighter: walk to next stop failed: %v", err)
			app.clearBehavior(npcID)
		}
		return
	}
	if route.Phase == "lighting" {
		// All stops done — walk home.
		route.Phase = "returning"
		if _, err := app.startNPCWalk(ctx, npcID, route.HomeX, route.HomeY, defaultNPCSpeed); err != nil {
			log.Printf("lamplighter: walk home failed: %v", err)
			app.clearBehavior(npcID)
		}
		return
	}
	// Phase == "returning" — we just arrived home.
	app.clearBehavior(npcID)
}

func (app *App) clearBehavior(npcID string) {
	app.NPCBehaviors.mu.Lock()
	delete(app.NPCBehaviors.active, npcID)
	app.NPCBehaviors.mu.Unlock()
}

// findLamplighter returns the (id, current_x, current_y, home_x, home_y) of
// the NPC marked as the village's lamplighter, if any.
func (app *App) findLamplighter(ctx context.Context) (string, float64, float64, float64, float64, bool) {
	var id string
	var cx, cy, hx, hy float64
	err := app.DB.QueryRow(ctx,
		`SELECT id, current_x, current_y, home_x, home_y FROM npc
		 WHERE behavior = $1
		 LIMIT 1`, behaviorLamplighter,
	).Scan(&id, &cx, &cy, &hx, &hy)
	if err != nil {
		return "", 0, 0, 0, 0, false
	}
	return id, cx, cy, hx, hy, true
}

// startLamplighterRoute builds the nearest-neighbor route for the given
// target tag ('day-active' at dawn, 'night-active' at dusk) and kicks off
// the NPC's first walk. Returns the number of stops queued.
//
// Cancels any existing lamplighter state so a new dusk/dawn supersedes a
// routine still in progress (e.g. rapid Force Day / Force Night toggles).
func (app *App) startLamplighterRoute(ctx context.Context, targetTag string) (int, error) {
	npcID, curX, curY, homeX, homeY, ok := app.findLamplighter(ctx)
	if !ok {
		return 0, nil // no lamplighter, nothing to do
	}

	// Pick up actual current position if NPC is mid-walk. Interpolate.
	app.NPCMovement.mu.Lock()
	if w := app.NPCMovement.active[npcID]; w != nil {
		curX, curY = w.currentPosition()
	}
	app.NPCMovement.mu.Unlock()

	// Candidate objects: those whose asset has a state with targetTag, and
	// whose current state differs from that tagged target. DISTINCT ON picks
	// one target state per asset (lowest-id wins, same rule as applyTransition).
	rows, err := app.DB.Query(ctx,
		`WITH target_states AS (
		    SELECT DISTINCT ON (s.asset_id) s.asset_id, s.state AS target_state
		    FROM asset_state s
		    JOIN asset_state_tag t ON t.state_id = s.id
		    WHERE t.tag = $1
		    ORDER BY s.asset_id, s.id
		)
		SELECT o.id, ts.target_state, o.x, o.y
		FROM village_object o
		JOIN target_states ts ON ts.asset_id = o.asset_id
		WHERE o.current_state IS DISTINCT FROM ts.target_state`, targetTag,
	)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		ObjectID string
		NewState string
		WorldX   float64
		WorldY   float64
	}
	var cands []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.ObjectID, &c.NewState, &c.WorldX, &c.WorldY); err != nil {
			rows.Close()
			return 0, err
		}
		cands = append(cands, c)
	}
	rows.Close()

	if len(cands) == 0 {
		return 0, nil
	}

	grid, err := app.loadWalkGrid(ctx)
	if err != nil {
		return 0, err
	}

	// Greedy nearest-neighbor: at each step, run A* from current walker tile
	// to each remaining candidate's adjacent-walkable tile; keep shortest.
	// N^2 in candidates, ~5-10 lamps → 25-100 A* runs total. Fine.
	curTileX, curTileY := worldToTile(curX, curY)
	curTile := gridPoint{curTileX, curTileY}
	var stops []lamplighterStop
	remaining := make([]candidate, len(cands))
	copy(remaining, cands)

	for len(remaining) > 0 {
		bestIdx := -1
		bestNeighbor := gridPoint{}
		bestLen := -1
		for i, c := range remaining {
			goalTile := gridPoint{}
			goalTile.X, goalTile.Y = worldToTile(c.WorldX, c.WorldY)
			path, neighbor := findPathToAdjacent(grid, curTile, goalTile)
			if path == nil {
				continue
			}
			if bestLen < 0 || len(path) < bestLen {
				bestIdx = i
				bestNeighbor = neighbor
				bestLen = len(path)
			}
		}
		if bestIdx < 0 {
			break // nothing else reachable
		}
		chosen := remaining[bestIdx]
		world := tileToWorld(bestNeighbor.X, bestNeighbor.Y)
		stops = append(stops, lamplighterStop{
			ObjectID: chosen.ObjectID,
			WalkX:    world.X,
			WalkY:    world.Y,
			NewState: chosen.NewState,
		})
		curTile = bestNeighbor
		remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)
	}

	if len(stops) == 0 {
		return 0, nil
	}

	route := &lamplighterRoute{
		NPCID:   npcID,
		Stops:   stops,
		StopIdx: 0,
		HomeX:   homeX,
		HomeY:   homeY,
		Phase:   "lighting",
	}
	app.NPCBehaviors.mu.Lock()
	app.NPCBehaviors.active[npcID] = route
	app.NPCBehaviors.mu.Unlock()

	first := stops[0]
	if _, err := app.startNPCWalk(ctx, npcID, first.WalkX, first.WalkY, defaultNPCSpeed); err != nil {
		app.clearBehavior(npcID)
		return 0, err
	}
	log.Printf("lamplighter: %s started %s route with %d stops", npcID, targetTag, len(stops))
	return len(stops), nil
}

