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
	"net/http"
	"sync"
)

const (
	behaviorLamplighter = "lamplighter"
	behaviorWasherwoman = "washerwoman"
	behaviorTownCrier   = "town_crier"

	tagLaundry          = "laundry"
	tagNoticeBoard      = "notice-board"
	tagLamplighterTarget = "lamplighter-target"
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
	NPCID             string
	Stops             []routeStop
	StopIdx           int
	HomeX             float64
	HomeY             float64
	Phase             string // "active" or "returning"
	// TargetStructureID is the id of the structure the NPC is returning
	// INTO (home for behavior cycles, home/work for manual go-home /
	// go-to-work). On arrival it becomes the NPC's inside_structure_id
	// so occupancy-driven state flipping knows where they landed.
	TargetStructureID string
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
	// Phase == "returning" — we just arrived home. Mark the villager
	// inside (clients hide the sprite for non-see-through structures;
	// market-stall-type assets flip to their "occupied" state via the
	// refresh inside setNPCInside) and clear the route state.
	app.setNPCInside(context.Background(), npcID, true, route.TargetStructureID)
	app.clearBehavior(npcID)
}

func (app *App) clearBehavior(npcID string) {
	app.NPCBehaviors.mu.Lock()
	delete(app.NPCBehaviors.active, npcID)
	app.NPCBehaviors.mu.Unlock()
}

// behaviorNPC is the per-NPC context a route starter needs: the NPC id,
// its current position (for route origin), and its home position (for the
// return-walk leg). Home coords come from home_structure_id (with the
// structure's asset door offset applied, when set) or falling back to the
// scalar home_x / home_y otherwise.
type behaviorNPC struct {
	ID                string
	Behavior          string
	CurX              float64
	CurY              float64
	HomeX             float64
	HomeY             float64
	// HomeStructureID is the structure the NPC returns INTO at the end
	// of a route. Used to set inside_structure_id on arrival so
	// occupancy-driven state flipping knows which building to check.
	// Empty when the NPC has no home structure linked.
	HomeStructureID   string
	// HasCustomSchedule reports whether the NPC's per-NPC schedule
	// fields (interval + start + end) are all set. When true, the
	// per-NPC scheduler owns dispatching this NPC — the legacy global
	// rotation path (applyRotation) must NOT fire their route or the
	// laundry would rotate twice a day.
	HasCustomSchedule bool
}

// homeCoordsSQL resolves the NPC's home target position. Preference order:
//   1. home_structure.x/y + asset.door_offset_x/y * tileSize  (door tile)
//   2. home_structure.x/y                                      (adjacent fallback, pre-door)
//   3. n.home_x / n.home_y                                     (no structure linked)
//
// The arithmetic uses the COALESCE short-circuit: adding a NULL door offset
// yields NULL, so the next COALESCE argument (plain structure x) takes over.
const homeCoordsSQL = `
    COALESCE(s.x + a.door_offset_x * 32.0, s.x, n.home_x),
    COALESCE(s.y + a.door_offset_y * 32.0, s.y, n.home_y)`

// findNPCWithBehavior returns the NPC tagged with the given behavior slug,
// resolving home coords through homeCoordsSQL. If the NPC is mid-walk, its
// interpolated current position replaces the last-persisted current_x/y.
//
// HasCustomSchedule is set when the NPC owns its own scheduling (the per-
// NPC scheduler will dispatch them); callers that want to avoid double-
// firing from the legacy rotation path check this before starting a route.
func (app *App) findNPCWithBehavior(ctx context.Context, slug string) (*behaviorNPC, bool) {
	n := behaviorNPC{Behavior: slug}
	var homeStructureID *string
	var interval, startH, endH *int
	err := app.DB.QueryRow(ctx,
		`SELECT n.id, n.current_x, n.current_y, `+homeCoordsSQL+`, n.home_structure_id,
		        n.schedule_interval_hours, n.active_start_hour, n.active_end_hour
		 FROM npc n
		 LEFT JOIN village_object s ON s.id = n.home_structure_id
		 LEFT JOIN asset a ON a.id = s.asset_id
		 WHERE n.behavior = $1
		 LIMIT 1`, slug,
	).Scan(&n.ID, &n.CurX, &n.CurY, &n.HomeX, &n.HomeY, &homeStructureID,
		&interval, &startH, &endH)
	if err != nil {
		return nil, false
	}
	if homeStructureID != nil {
		n.HomeStructureID = *homeStructureID
	}
	n.HasCustomSchedule = interval != nil && startH != nil && endH != nil

	app.interpolateCurrentPos(&n)
	return &n, true
}

// loadBehaviorNPCByID loads a specific NPC (not by behavior slug) for the
// run-cycle trigger, which targets one NPC directly rather than whichever
// villager happens to carry that behavior.
func (app *App) loadBehaviorNPCByID(ctx context.Context, npcID string) (*behaviorNPC, bool) {
	var n behaviorNPC
	var behavior *string
	var homeStructureID *string
	var interval, startH, endH *int
	err := app.DB.QueryRow(ctx,
		`SELECT n.id, COALESCE(n.behavior, ''), n.current_x, n.current_y, `+homeCoordsSQL+`, n.home_structure_id,
		        n.schedule_interval_hours, n.active_start_hour, n.active_end_hour
		 FROM npc n
		 LEFT JOIN village_object s ON s.id = n.home_structure_id
		 LEFT JOIN asset a ON a.id = s.asset_id
		 WHERE n.id = $1`, npcID,
	).Scan(&n.ID, &behavior, &n.CurX, &n.CurY, &n.HomeX, &n.HomeY, &homeStructureID,
		&interval, &startH, &endH)
	if err != nil {
		return nil, false
	}
	if behavior != nil {
		n.Behavior = *behavior
	}
	if homeStructureID != nil {
		n.HomeStructureID = *homeStructureID
	}
	n.HasCustomSchedule = interval != nil && startH != nil && endH != nil

	app.interpolateCurrentPos(&n)
	return &n, true
}

// interpolateCurrentPos overrides CurX/CurY with the interpolated walk
// position when the NPC is mid-walk, so routes start from where they
// visually are rather than the last persisted waypoint.
func (app *App) interpolateCurrentPos(n *behaviorNPC) {
	app.NPCMovement.mu.Lock()
	if w := app.NPCMovement.active[n.ID]; w != nil {
		n.CurX, n.CurY = w.currentPosition()
	}
	app.NPCMovement.mu.Unlock()
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
//
// If the NPC is currently marked inside (their sprite is hidden on clients),
// flip inside=false and broadcast first so they visually "step out the door"
// before the walk animation starts.
func (app *App) startNPCRoute(ctx context.Context, npc *behaviorNPC, stops []routeStop, label string) error {
	if len(stops) == 0 {
		return nil
	}
	app.setNPCInside(ctx, npc.ID, false, "")
	route := &npcRoute{
		NPCID:             npc.ID,
		Stops:             stops,
		StopIdx:           0,
		HomeX:             npc.HomeX,
		HomeY:             npc.HomeY,
		Phase:             "active",
		TargetStructureID: npc.HomeStructureID,
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

// setNPCInside writes the inside flag + inside_structure_id and broadcasts
// npc_inside_changed when the pair actually changes. structureID is only
// consulted when inside=true; on inside=false we always clear it. After
// the NPC state changes, the affected structures (previous and/or new)
// get their occupancy-tagged state recomputed so market stalls and
// similar see-through buildings flip open/closed automatically.
func (app *App) setNPCInside(ctx context.Context, npcID string, inside bool, structureID string) {
	var newInsideID *string
	if inside && structureID != "" {
		s := structureID
		newInsideID = &s
	}
	// Read the previous value so we know which structure to re-evaluate
	// when the NPC is MOVING between buildings (rare today but cheap to
	// handle). Also used for the no-op early exit.
	var prev struct {
		Inside              bool
		InsideStructureID   *string
	}
	err := app.DB.QueryRow(ctx,
		`SELECT inside, inside_structure_id FROM npc WHERE id = $1`, npcID,
	).Scan(&prev.Inside, &prev.InsideStructureID)
	if err != nil {
		log.Printf("setNPCInside read(%s): %v", npcID, err)
		return
	}
	if prev.Inside == inside && stringPtrEq(prev.InsideStructureID, newInsideID) {
		return
	}
	if _, err := app.DB.Exec(ctx,
		`UPDATE npc SET inside = $2, inside_structure_id = $3 WHERE id = $1`,
		npcID, inside, newInsideID,
	); err != nil {
		log.Printf("setNPCInside write(%s): %v", npcID, err)
		return
	}
	app.Hub.Broadcast(WorldEvent{
		Type: "npc_inside_changed",
		Data: map[string]any{
			"id":                  npcID,
			"inside":              inside,
			"inside_structure_id": newInsideID,
		},
	})
	// Re-evaluate occupancy-driven state for any structure whose roster
	// just changed. Previous structure may go from occupied to empty;
	// new structure may go from empty to occupied.
	touched := map[string]bool{}
	if prev.InsideStructureID != nil {
		touched[*prev.InsideStructureID] = true
	}
	if newInsideID != nil {
		touched[*newInsideID] = true
	}
	for sid := range touched {
		app.refreshStructureOccupancyState(ctx, sid)
	}
}

func stringPtrEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// refreshStructureOccupancyState flips a structure's current_state based
// on how many NPCs are currently inside it. Only structures whose asset
// has states tagged "occupied" and "unoccupied" participate — everything
// else is a no-op.
//
// Two per-asset modifiers (ZBBS-070) refine the binary "anyone inside =
// occupied" rule:
//   - asset.occupied_min_count: minimum headcount to flip to occupied.
//     Defaults to 1, set to 2 for the tavern (keeper alone shouldn't
//     make the hearth glow as if it's a gathering).
//   - asset.occupied_night_only: when true, the occupied state is
//     suppressed during day phase regardless of headcount. The tavern
//     goes dark at dawn even if patrons are still inside.
func (app *App) refreshStructureOccupancyState(ctx context.Context, structureID string) {
	var assetID string
	var currentState string
	var minCount int
	var nightOnly bool
	if err := app.DB.QueryRow(ctx,
		`SELECT vo.asset_id, vo.current_state, a.occupied_min_count, a.occupied_night_only
		 FROM village_object vo
		 JOIN asset a ON a.id = vo.asset_id
		 WHERE vo.id = $1`, structureID,
	).Scan(&assetID, &currentState, &minCount, &nightOnly); err != nil {
		return
	}

	// Look up the two tagged states. If either is missing, the asset
	// isn't wired for occupancy flipping — leave it alone.
	var occupiedState, unoccupiedState string
	if err := app.DB.QueryRow(ctx,
		`SELECT state FROM asset_state s
		 JOIN asset_state_tag t ON t.state_id = s.id
		 WHERE s.asset_id = $1 AND t.tag = 'occupied' LIMIT 1`, assetID,
	).Scan(&occupiedState); err != nil {
		return
	}
	if err := app.DB.QueryRow(ctx,
		`SELECT state FROM asset_state s
		 JOIN asset_state_tag t ON t.state_id = s.id
		 WHERE s.asset_id = $1 AND t.tag = 'unoccupied' LIMIT 1`, assetID,
	).Scan(&unoccupiedState); err != nil {
		return
	}

	var occupants int
	if err := app.DB.QueryRow(ctx,
		`SELECT COUNT(*) FROM npc WHERE inside_structure_id = $1`, structureID,
	).Scan(&occupants); err != nil {
		return
	}

	// Decide target. Default to unoccupied; flip to occupied only when
	// the headcount threshold is met AND the night-only gate (if set)
	// passes. Phase comes from world_phase — single row, queried per
	// flip rather than cached so admins force-toggling phase from the
	// config panel takes effect immediately.
	target := unoccupiedState
	if occupants >= minCount {
		if nightOnly {
			var phase string
			if err := app.DB.QueryRow(ctx, `SELECT phase FROM world_phase LIMIT 1`).Scan(&phase); err == nil {
				if phase == "night" {
					target = occupiedState
				}
			}
		} else {
			target = occupiedState
		}
	}
	if target == currentState {
		return
	}
	if _, err := app.DB.Exec(ctx,
		`UPDATE village_object SET current_state = $2 WHERE id = $1`,
		structureID, target,
	); err != nil {
		log.Printf("refreshStructureOccupancyState(%s): %v", structureID, err)
		return
	}
	app.Hub.Broadcast(WorldEvent{
		Type: "object_state_changed",
		Data: map[string]string{"id": structureID, "state": target},
	})
}

// refreshNightOnlyOccupancyStates re-evaluates every village_object
// whose asset has occupied_night_only=true. Called from the day/night
// phase transitions so taverns light up at dusk and go dark at dawn
// without anyone needing to walk in or out. Without this, an
// already-occupied tavern wouldn't change at the boundary because the
// per-NPC inside_structure_id flip is what normally drives a refresh.
func (app *App) refreshNightOnlyOccupancyStates(ctx context.Context) {
	rows, err := app.DB.Query(ctx,
		`SELECT vo.id
		 FROM village_object vo
		 JOIN asset a ON a.id = vo.asset_id
		 WHERE a.occupied_night_only = TRUE`,
	)
	if err != nil {
		log.Printf("refreshNightOnlyOccupancyStates: %v", err)
		return
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		app.refreshStructureOccupancyState(ctx, id)
	}
}

// startLamplighterRoute looks up the lamplighter NPC and runs the route.
// targetTag is 'day-active' at dawn (turning lamps off) or 'night-active'
// at dusk (turning lamps on).
func (app *App) startLamplighterRoute(ctx context.Context, targetTag string) (int, error) {
	npc, ok := app.findNPCWithBehavior(ctx, behaviorLamplighter)
	if !ok {
		return 0, nil
	}
	return app.startLamplighterRouteForNPC(ctx, npc, targetTag)
}

// startLamplighterRouteForNPC builds a nearest-neighbor route over objects
// whose asset has a state with targetTag and whose current state differs
// from the tagged target. Separated from startLamplighterRoute so the
// run-cycle trigger can target a specific NPC rather than "whichever NPC
// has behavior=lamplighter."
func (app *App) startLamplighterRouteForNPC(ctx context.Context, npc *behaviorNPC, targetTag string) (int, error) {
	// Narrow the lamplighter's route to states that also carry the
	// lamplighter-target tag. Other day/night-active objects (campfires)
	// are left to the bulk transition in applyTransition.
	rows, err := app.DB.Query(ctx,
		`WITH target_states AS (
		    SELECT DISTINCT ON (s.asset_id) s.asset_id, s.state AS target_state
		    FROM asset_state s
		    JOIN asset_state_tag t  ON t.state_id  = s.id AND t.tag  = $1
		    JOIN asset_state_tag t2 ON t2.state_id = s.id AND t2.tag = $2
		    ORDER BY s.asset_id, s.id
		)
		SELECT o.id, ts.target_state, o.x, o.y
		FROM village_object o
		JOIN target_states ts ON ts.asset_id = o.asset_id
		WHERE o.current_state IS DISTINCT FROM ts.target_state`,
		targetTag, tagLamplighterTarget,
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
	return app.startRotationRouteForNPC(ctx, npc, domainTag, label)
}

// startRotationRouteForNPC is the per-NPC variant used by the run-cycle
// trigger. Same rotation logic, but targets a specific villager regardless
// of which (if any) carries the behavior slug on the npc table.
func (app *App) startRotationRouteForNPC(ctx context.Context, npc *behaviorNPC, domainTag, label string) (int, error) {
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

// handleRunNPCCycle triggers the behavior route for a specific NPC on
// demand, bypassing the day/night schedule. Admin only. For lamplighter the
// target tag is chosen from the CURRENT world phase (day => turn lamps off,
// night => turn lamps on), matching the dawn/dusk semantics.
func (app *App) handleRunNPCCycle(w http.ResponseWriter, r *http.Request) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin access required", http.StatusForbidden)
		return
	}
	npcID := r.PathValue("id")
	if npcID == "" {
		jsonError(w, "Missing npc id", http.StatusBadRequest)
		return
	}

	npc, ok := app.loadBehaviorNPCByID(r.Context(), npcID)
	if !ok {
		jsonError(w, "NPC not found", http.StatusNotFound)
		return
	}
	if npc.Behavior == "" {
		jsonError(w, "NPC has no behavior assigned", http.StatusBadRequest)
		return
	}

	stops, err := app.dispatchBehaviorForNPC(r.Context(), npc)
	if err != nil {
		log.Printf("run-cycle %s (%s): %v", npcID, npc.Behavior, err)
		jsonError(w, "Failed to run cycle", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"npc_id":   npcID,
		"behavior": npc.Behavior,
		"stops":    stops,
	})
}

// dispatchBehaviorForNPC routes to the appropriate per-NPC start*Route
// variant based on the NPC's behavior slug. Returns the number of stops
// queued (0 is legitimate — nothing to do right now).
func (app *App) dispatchBehaviorForNPC(ctx context.Context, npc *behaviorNPC) (int, error) {
	switch npc.Behavior {
	case behaviorLamplighter:
		// Manual trigger: pick whichever target tag actually has work to
		// do. Fixed phase-based targets fail at equilibrium (all lamps
		// match the current phase, no candidates, silent no-op). So try
		// the "preview next cycle" target first (opposite of current
		// phase); if that's already done, fall back to the current-phase
		// target. Effectively each click alternates: light them up →
		// turn them off → light them up.
		phase, err := app.currentWorldPhase(ctx)
		if err != nil {
			return 0, err
		}
		first, second := "night-active", "day-active"
		if phase == "night" {
			first, second = "day-active", "night-active"
		}
		n, err := app.startLamplighterRouteForNPC(ctx, npc, first)
		if err != nil || n > 0 {
			return n, err
		}
		return app.startLamplighterRouteForNPC(ctx, npc, second)
	case behaviorWasherwoman:
		return app.startRotationRouteForNPC(ctx, npc, tagLaundry, "washerwoman")
	case behaviorTownCrier:
		return app.startRotationRouteForNPC(ctx, npc, tagNoticeBoard, "town_crier")
	}
	return 0, nil
}

// currentWorldPhase reads the singleton world_phase row for the run-cycle
// dispatcher. Kept local so the behavior file doesn't pull the broader
// world config loader.
func (app *App) currentWorldPhase(ctx context.Context) (string, error) {
	var phase string
	err := app.DB.QueryRow(ctx,
		`SELECT phase FROM world_phase WHERE id = 1`,
	).Scan(&phase)
	return phase, err
}

// handleGoHome and handleGoToWork send the NPC directly to their home or
// work structure's door tile, skipping any behavior-specific route. On
// arrival the NPC flips inside=true via the shared Phase="returning" hook
// in advanceBehavior — same mechanism the full behavior cycle uses on its
// return leg.
func (app *App) handleGoHome(w http.ResponseWriter, r *http.Request) {
	app.handleGoToStructure(w, r, "home")
}

func (app *App) handleGoToWork(w http.ResponseWriter, r *http.Request) {
	app.handleGoToStructure(w, r, "work")
}

// handleGoToStructure is the shared body of go-home / go-to-work. kind is
// "home" or "work" and selects which structure column to resolve.
func (app *App) handleGoToStructure(w http.ResponseWriter, r *http.Request, kind string) {
	user := getUserFromContext(r.Context())
	if user == nil || !user.hasRole("ROLE_SALEM_ADMIN") {
		jsonError(w, "Admin access required", http.StatusForbidden)
		return
	}
	npcID := r.PathValue("id")
	if npcID == "" {
		jsonError(w, "Missing npc id", http.StatusBadRequest)
		return
	}

	structureCol := "home_structure_id"
	if kind == "work" {
		structureCol = "work_structure_id"
	}

	var curX, curY, destX, destY float64
	var structureID *string
	err := app.DB.QueryRow(r.Context(),
		`SELECT n.current_x, n.current_y, n.`+structureCol+`,
		        COALESCE(s.x + a.door_offset_x * 32.0, s.x, 0),
		        COALESCE(s.y + a.door_offset_y * 32.0, s.y, 0)
		 FROM npc n
		 LEFT JOIN village_object s ON s.id = n.`+structureCol+`
		 LEFT JOIN asset a ON a.id = s.asset_id
		 WHERE n.id = $1`, npcID,
	).Scan(&curX, &curY, &structureID, &destX, &destY)
	if err != nil {
		jsonError(w, "NPC not found", http.StatusNotFound)
		return
	}
	if structureID == nil {
		jsonError(w, "NPC has no "+kind+" structure assigned", http.StatusBadRequest)
		return
	}

	npc := &behaviorNPC{ID: npcID, CurX: curX, CurY: curY, HomeX: destX, HomeY: destY}
	app.interpolateCurrentPos(npc)

	if err := app.startReturnWalk(r.Context(), npc, destX, destY, *structureID, "go-"+kind); err != nil {
		log.Printf("go-%s %s: %v", kind, npcID, err)
		jsonError(w, "Failed to start walk", http.StatusInternalServerError)
		return
	}

	jsonResponse(w, http.StatusOK, map[string]any{
		"npc_id": npcID,
		"kind":   kind,
	})
}

// startReturnWalk cancels any in-flight behavior, installs a zero-stop
// Phase="returning" route so the arrival hook flips inside=true, and kicks
// off the walk to the destination. The NPC is visible during the walk
// (inside=false) and hidden on arrival.
func (app *App) startReturnWalk(ctx context.Context, npc *behaviorNPC, destX, destY float64, targetStructureID, label string) error {
	app.setNPCInside(ctx, npc.ID, false, "")
	route := &npcRoute{
		NPCID:             npc.ID,
		Stops:             []routeStop{},
		StopIdx:           0,
		HomeX:             destX,
		HomeY:             destY,
		Phase:             "returning",
		TargetStructureID: targetStructureID,
	}
	app.NPCBehaviors.mu.Lock()
	app.NPCBehaviors.active[npc.ID] = route
	app.NPCBehaviors.mu.Unlock()

	if _, err := app.startNPCWalk(ctx, npc.ID, destX, destY, defaultNPCSpeed); err != nil {
		app.clearBehavior(npc.ID)
		return err
	}
	log.Printf("%s: %s walking to %.0f,%.0f", label, npc.ID, destX, destY)
	return nil
}
