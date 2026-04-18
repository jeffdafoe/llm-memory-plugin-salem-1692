package main

// Scheduled NPC behaviors that piggyback on the movement infrastructure.
//
// Three behaviors ship today:
//
//   lamplighter — at each dusk/dawn boundary, walks to every village_object
//     whose asset has a day-active / night-active tagged state and flips it.
//   washerwoman — at the daily rotation boundary, walks to every laundry-tagged
//     rotatable state and rotates it to the next variant per the asset's
//     rotation_algo.
//   town_crier  — same as washerwoman but for notice-board-tagged states.
//
// All three share the same state machine and route-walking skeleton. The only
// behavior-specific parts are (1) which candidates to visit, and (2) what
// state each should land in on arrival — both baked into the per-stop
// routeStop at route build time.

import (
	"context"
	"log"
	"sync"
)

const (
	behaviorLamplighter = "lamplighter"
	behaviorWasherwoman = "washerwoman"
	behaviorTownCrier   = "town_crier"

	tagLaundry      = "laundry"
	tagNoticeBoard  = "notice-board"
)

// routeStop is one object an NPC visits on its scheduled route.
type routeStop struct {
	ObjectID string
	WalkX    float64 // world-pixel coord of the adjacent walkable tile
	WalkY    float64
	NewState string
}

// npcRoute is the in-memory state machine for a running behavior routine.
// StopIdx is the index of the stop the NPC is CURRENTLY heading toward (or
// has most recently finished, right before we advance). Phase toggles to
// "returning" after the last stop so arrival at home doesn't trigger a
// state flip.
type npcRoute struct {
	NPCID   string
	Stops   []routeStop
	StopIdx int
	HomeX   float64
	HomeY   float64
	Phase   string // "active" or "returning"
}

// NPCBehaviors tracks active behavior state machines keyed by NPC id.
type NPCBehaviors struct {
	mu     sync.Mutex
	active map[string]*npcRoute
}

func newNPCBehaviors() *NPCBehaviors {
	return &NPCBehaviors{active: map[string]*npcRoute{}}
}

// advanceBehavior is the arrival hook. If the NPC has an active route, flip
// the target object for the just-arrived stop (in 'active' phase), then
// start the next walk — or transition to 'returning' + walk home — or clear
// the state machine when home is reached.
func (app *App) advanceBehavior(npcID string) {
	app.NPCBehaviors.mu.Lock()
	route := app.NPCBehaviors.active[npcID]
	app.NPCBehaviors.mu.Unlock()
	if route == nil {
		return
	}

	ctx := context.Background()

	// If we were visiting a stop, flip that object now.
	if route.Phase == "active" && route.StopIdx < len(route.Stops) {
		stop := route.Stops[route.StopIdx]
		if _, err := app.DB.Exec(ctx,
			`UPDATE village_object SET current_state = $2
			 WHERE id = $1 AND current_state IS DISTINCT FROM $2`,
			stop.ObjectID, stop.NewState,
		); err != nil {
			log.Printf("npc_route: flip %s → %s failed: %v", stop.ObjectID, stop.NewState, err)
		} else {
			app.Hub.Broadcast(WorldEvent{
				Type: "object_state_changed",
				Data: map[string]string{"id": stop.ObjectID, "state": stop.NewState},
			})
		}
		route.StopIdx++
	}

	// Decide next action.
	if route.Phase == "active" && route.StopIdx < len(route.Stops) {
		next := route.Stops[route.StopIdx]
		if _, err := app.startNPCWalk(ctx, npcID, next.WalkX, next.WalkY, defaultNPCSpeed); err != nil {
			log.Printf("npc_route: walk to next stop failed: %v", err)
			app.clearBehavior(npcID)
		}
		return
	}
	if route.Phase == "active" {
		// All stops done — walk home.
		route.Phase = "returning"
		if _, err := app.startNPCWalk(ctx, npcID, route.HomeX, route.HomeY, defaultNPCSpeed); err != nil {
			log.Printf("npc_route: walk home failed: %v", err)
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

// behaviorNPC is the per-NPC context a route starter needs: the NPC id,
// its current position (for route origin), and its home position (for the
// return-walk leg). Home coords come from home_structure_id when set,
// falling back to the scalar home_x / home_y otherwise.
type behaviorNPC struct {
	ID     string
	CurX   float64
	CurY   float64
	HomeX  float64
	HomeY  float64
}

// findNPCWithBehavior returns the NPC tagged with the given behavior slug,
// resolving their home coords from home_structure_id (if set) or falling
// back to the scalar home_x / home_y columns. If the NPC is mid-walk, its
// interpolated current position replaces the last-persisted current_x/y.
func (app *App) findNPCWithBehavior(ctx context.Context, slug string) (*behaviorNPC, bool) {
	var n behaviorNPC
	err := app.DB.QueryRow(ctx,
		`SELECT n.id, n.current_x, n.current_y,
		        COALESCE(s.x, n.home_x), COALESCE(s.y, n.home_y)
		 FROM npc n
		 LEFT JOIN village_object s ON s.id = n.home_structure_id
		 WHERE n.behavior = $1
		 LIMIT 1`, slug,
	).Scan(&n.ID, &n.CurX, &n.CurY, &n.HomeX, &n.HomeY)
	if err != nil {
		return nil, false
	}

	// If the NPC is mid-walk, interpolate so the route starts from where
	// they visually are, not the last persisted waypoint.
	app.NPCMovement.mu.Lock()
	if w := app.NPCMovement.active[n.ID]; w != nil {
		n.CurX, n.CurY = w.currentPosition()
	}
	app.NPCMovement.mu.Unlock()

	return &n, true
}

// routeCandidate is one object to visit with a pre-computed target state.
// All behavior-specific queries boil down to producing a []routeCandidate;
// route layout (ordering, walk-to tiles) is shared across behaviors.
type routeCandidate struct {
	ObjectID string
	NewState string
	WorldX   float64
	WorldY   float64
}

// buildRouteStops lays out the NPC's visit order over the candidates using a
// greedy nearest-neighbor walk on the A* grid. Each step picks the candidate
// whose adjacent-walkable tile is shortest from the current position. Runs
// O(n^2) A* calls in the worst case — fine for a handful of lamps/laundry
// lines; would need optimization at 100+ stops.
func buildRouteStops(grid *walkGrid, startX, startY float64, candidates []routeCandidate) []routeStop {
	curTileX, curTileY := worldToTile(startX, startY)
	curTile := gridPoint{curTileX, curTileY}
	remaining := make([]routeCandidate, len(candidates))
	copy(remaining, candidates)

	var stops []routeStop
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
		stops = append(stops, routeStop{
			ObjectID: chosen.ObjectID,
			WalkX:    world.X,
			WalkY:    world.Y,
			NewState: chosen.NewState,
		})
		curTile = bestNeighbor
		remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)
	}
	return stops
}

// startNPCRoute installs the behavior state machine for the given NPC and
// kicks off the walk to the first stop. Cancels any prior route on the same
// NPC so rapid triggers (e.g. Force Day / Force Night spam) supersede cleanly.
func (app *App) startNPCRoute(ctx context.Context, npc *behaviorNPC, stops []routeStop, label string) error {
	if len(stops) == 0 {
		return nil
	}
	route := &npcRoute{
		NPCID:   npc.ID,
		Stops:   stops,
		StopIdx: 0,
		HomeX:   npc.HomeX,
		HomeY:   npc.HomeY,
		Phase:   "active",
	}
	app.NPCBehaviors.mu.Lock()
	app.NPCBehaviors.active[npc.ID] = route
	app.NPCBehaviors.mu.Unlock()

	first := stops[0]
	if _, err := app.startNPCWalk(ctx, npc.ID, first.WalkX, first.WalkY, defaultNPCSpeed); err != nil {
		app.clearBehavior(npc.ID)
		return err
	}
	log.Printf("%s: %s started route with %d stops", label, npc.ID, len(stops))
	return nil
}

// startLamplighterRoute builds a nearest-neighbor route over objects whose
// asset has a state with targetTag ('day-active' at dawn, 'night-active' at
// dusk) and whose current state differs from the tagged target. Returns the
// number of stops queued.
func (app *App) startLamplighterRoute(ctx context.Context, targetTag string) (int, error) {
	npc, ok := app.findNPCWithBehavior(ctx, behaviorLamplighter)
	if !ok {
		return 0, nil
	}

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
	var cands []routeCandidate
	for rows.Next() {
		var c routeCandidate
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
	stops := buildRouteStops(grid, npc.CurX, npc.CurY, cands)
	if err := app.startNPCRoute(ctx, npc, stops, "lamplighter"); err != nil {
		return 0, err
	}
	return len(stops), nil
}

// startRotationRoute is the shared implementation for washerwoman /
// town_crier. It walks the NPC through the subset of rotation flips whose
// state carries the given tag, applying each per-stop on arrival instead
// of in the bulk rotation pass.
func (app *App) startRotationRoute(ctx context.Context, slug, domainTag, label string) (int, error) {
	npc, ok := app.findNPCWithBehavior(ctx, slug)
	if !ok {
		return 0, nil
	}

	flips, err := app.determineRotationFlipsForTag(ctx, domainTag)
	if err != nil {
		return 0, err
	}
	if len(flips) == 0 {
		return 0, nil
	}

	cands, err := app.flipsToCandidates(ctx, flips)
	if err != nil {
		return 0, err
	}
	if len(cands) == 0 {
		return 0, nil
	}

	grid, err := app.loadWalkGrid(ctx)
	if err != nil {
		return 0, err
	}
	stops := buildRouteStops(grid, npc.CurX, npc.CurY, cands)
	if err := app.startNPCRoute(ctx, npc, stops, label); err != nil {
		return 0, err
	}
	return len(stops), nil
}

// startWasherwomanRoute rotates laundry-tagged objects per the asset's
// rotation_algo, delivered one-at-a-time as the NPC arrives at each line.
func (app *App) startWasherwomanRoute(ctx context.Context) (int, error) {
	return app.startRotationRoute(ctx, behaviorWasherwoman, tagLaundry, "washerwoman")
}

// startTownCrierRoute rotates notice-board-tagged objects.
func (app *App) startTownCrierRoute(ctx context.Context) (int, error) {
	return app.startRotationRoute(ctx, behaviorTownCrier, tagNoticeBoard, "town_crier")
}

// flipsToCandidates looks up each pendingFlip's world coordinates so the
// route builder has the data it needs. determineRotationFlips doesn't carry
// (x, y) through because the bulk scheduler doesn't need it.
func (app *App) flipsToCandidates(ctx context.Context, flips []pendingFlip) ([]routeCandidate, error) {
	if len(flips) == 0 {
		return nil, nil
	}
	ids := make([]string, len(flips))
	newStateByID := make(map[string]string, len(flips))
	for i, f := range flips {
		ids[i] = f.ObjectID
		newStateByID[f.ObjectID] = f.NewState
	}
	rows, err := app.DB.Query(ctx,
		`SELECT id, x, y FROM village_object WHERE id = ANY($1)`, ids,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cands []routeCandidate
	for rows.Next() {
		var id string
		var x, y float64
		if err := rows.Scan(&id, &x, &y); err != nil {
			return nil, err
		}
		cands = append(cands, routeCandidate{
			ObjectID: id,
			NewState: newStateByID[id],
			WorldX:   x,
			WorldY:   y,
		})
	}
	return cands, nil
}
