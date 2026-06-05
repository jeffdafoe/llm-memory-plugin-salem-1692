package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildRouteTestWorld seeds a minimal world for npc_route tests:
//
//   - All-grass terrain (every tile walkable).
//   - "house" asset with a door at (0, 2) (matches MoveActor tests).
//   - Two lamp objects (lamp-A/B) on the lamplighter-target path —
//     used for route-candidate fixtures.
//   - One actor "lamp" seeded at the pad origin with HomeStructureID
//     set to "home" (a tagged house structure).
func buildRouteTestWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())

	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"house": {
			ID:           "house",
			Category:     "structure",
			DefaultState: "default",
			DoorOffsetX:  intp(0),
			DoorOffsetY:  intp(2),
			States: []sim.AssetState{
				{ID: 1, State: "default"},
			},
		},
		"lamp": {
			ID:           "lamp",
			Category:     "prop",
			DefaultState: "unlit",
			States: []sim.AssetState{
				{ID: 10, State: "unlit", Tags: []string{"day-active", "lamplighter-target"}},
				{ID: 11, State: "lit", Tags: []string{"night-active", "lamplighter-target"}},
			},
		},
	})

	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"home":   {ID: "home", AssetID: "house", Pos: sim.WorldPos{X: 320, Y: 320}},
		"lamp-A": {ID: "lamp-A", AssetID: "lamp", CurrentState: "lit", Pos: sim.WorldPos{X: 640, Y: 320}},
		"lamp-B": {ID: "lamp-B", AssetID: "lamp", CurrentState: "lit", Pos: sim.WorldPos{X: 960, Y: 320}},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"home": {ID: "home", DisplayName: "Home"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"lamp": {
			ID:              "lamp",
			DisplayName:     "Lamplighter",
			Kind:            sim.KindNPCShared,
			Pos:             sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 10},
			HomeStructureID: "home",
			Attributes: map[string][]byte{
				sim.AttrLamplighter: {},
			},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() { cancel(); <-done }
}

func sampleLampCandidates() []sim.RouteCandidate {
	return []sim.RouteCandidate{
		{ObjectID: "lamp-A", NewState: "unlit", WorldX: 640, WorldY: 320},
		{ObjectID: "lamp-B", NewState: "unlit", WorldX: 960, WorldY: 320},
	}
}

// TestStartNPCRoute_HappyPath: candidates supplied, actor present,
// route installed in ActiveRoutes, first MoveActor dispatched (MoveIntent
// stamped on actor).
func TestStartNPCRoute_HappyPath(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	homeDest := sim.NewStructureEnterDestination("home")
	res, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrLamplighter, homeDest, sampleLampCandidates(), now))
	if err != nil {
		t.Fatalf("StartNPCRoute: %v", err)
	}
	r := res.(sim.StartNPCRouteResult)
	if r.Stops < 1 {
		t.Errorf("Stops = %d, want >= 1", r.Stops)
	}
	if r.Replaced {
		t.Errorf("Replaced = true, want false (no prior route)")
	}
	if r.NPCID != "lamp" || r.Label != sim.AttrLamplighter {
		t.Errorf("Result NPCID=%q Label=%q, want lamp / %q", r.NPCID, r.Label, sim.AttrLamplighter)
	}

	// MoveIntent should be stamped on the actor (the first walk is in flight).
	mi := moveIntentOf(t, w, "lamp")
	if mi == nil {
		t.Fatal("MoveIntent nil — first walk not dispatched")
	}
	if mi.Destination.Kind != sim.MoveDestinationPosition {
		t.Errorf("first walk destination kind = %v, want Position (the adjacent walkable tile)", mi.Destination.Kind)
	}

	// Route should be installed.
	route := activeRouteOf(t, w, "lamp")
	if route == nil {
		t.Fatal("ActiveRoutes[lamp] nil after StartNPCRoute")
	}
	if len(route.Stops) != r.Stops {
		t.Errorf("route.Stops len = %d, result.Stops = %d", len(route.Stops), r.Stops)
	}
	if route.Phase != sim.RoutePhaseActive {
		t.Errorf("route.Phase = %q, want %q", route.Phase, sim.RoutePhaseActive)
	}
	if route.StopIdx != 0 {
		t.Errorf("route.StopIdx = %d, want 0 at start", route.StopIdx)
	}
}

// TestStartNPCRoute_NoCandidates: empty list → no route, no MoveIntent.
func TestStartNPCRoute_NoCandidates(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	res, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrLamplighter, homeDest, nil, time.Now().UTC()))
	if err != nil {
		t.Fatalf("StartNPCRoute: %v", err)
	}
	r := res.(sim.StartNPCRouteResult)
	if r.Stops != 0 {
		t.Errorf("Stops = %d, want 0", r.Stops)
	}
	if route := activeRouteOf(t, w, "lamp"); route != nil {
		t.Errorf("ActiveRoutes[lamp] = %+v, want nil", route)
	}
	if mi := moveIntentOf(t, w, "lamp"); mi != nil {
		t.Errorf("MoveIntent stamped on empty-candidate start: %+v", mi)
	}
}

// TestStartNPCRoute_SupersedesPrior: two consecutive starts → Replaced=true
// on the second, prior route gone.
func TestStartNPCRoute_SupersedesPrior(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	cands := sampleLampCandidates()

	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrLamplighter, homeDest, cands, time.Now().UTC())); err != nil {
		t.Fatalf("first start: %v", err)
	}
	res, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrLamplighter, homeDest, cands, time.Now().UTC()))
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	r := res.(sim.StartNPCRouteResult)
	if !r.Replaced {
		t.Errorf("second start Replaced = false, want true")
	}
}

// TestAdvanceNPCRoute_NoRoute: actor with no entry → "no_route" reason,
// no mutation.
func TestAdvanceNPCRoute_NoRoute(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	res, err := w.Send(sim.AdvanceNPCRoute("lamp"))
	if err != nil {
		t.Fatalf("AdvanceNPCRoute: %v", err)
	}
	r := res.(sim.AdvanceNPCRouteResult)
	if r.Reason != "no_route" {
		t.Errorf("Reason = %q, want no_route", r.Reason)
	}
}

// TestAdvanceNPCRoute_StopAdvancesFlipsAndWalks: simulate an arrival
// after the first stop. Verifies the village_object state flipped,
// StopIdx advanced, and a new MoveActor was dispatched for the next stop.
func TestAdvanceNPCRoute_StopAdvancesFlipsAndWalks(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrLamplighter, homeDest, sampleLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Manually fast-forward: pretend the actor arrived at stop 0.
	// AdvanceNPCRoute is the arrival hook; it flips the stop's object
	// and dispatches the next walk. Teleport to stop 0's WalkTo first
	// so the active-phase stale-arrival guard accepts the advance.
	firstStopID := firstStopObjectID(t, w, "lamp")
	teleportToCurrentStop(t, w, "lamp")
	res, err := w.Send(sim.AdvanceNPCRoute("lamp"))
	if err != nil {
		t.Fatalf("AdvanceNPCRoute: %v", err)
	}
	r := res.(sim.AdvanceNPCRouteResult)
	if r.Reason != "stop_advanced" {
		t.Errorf("Reason = %q, want stop_advanced", r.Reason)
	}

	// Object should have flipped.
	snap := w.Published()
	if got := snap.VillageObjects[firstStopID].CurrentState; got != "unlit" {
		t.Errorf("stop object %q state = %q, want unlit", firstStopID, got)
	}

	// Next walk dispatched — MoveIntent should be present.
	if mi := moveIntentOf(t, w, "lamp"); mi == nil {
		t.Error("MoveIntent nil after stop_advanced — next walk not dispatched")
	}
}

// TestAdvanceNPCRoute_StaleArrivalReWalks: an Advance triggered when the actor
// is NOT at the current stop's WalkTo (an out-of-band MoveActor or admin
// teleport landed them elsewhere) does NOT flip the object or advance StopIdx;
// it re-walks to the stop and reports "stale_retry" so a single bump no longer
// strands the stop.
func TestAdvanceNPCRoute_StaleArrivalReWalks(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrLamplighter, homeDest, sampleLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Don't teleport — actor remains at the route start tile, which is NOT the
	// first stop's WalkTo. The guard rejects the flip and re-walks.
	firstStopID := firstStopObjectID(t, w, "lamp")
	beforeState := w.Published().VillageObjects[firstStopID].CurrentState

	res, err := w.Send(sim.AdvanceNPCRoute("lamp"))
	if err != nil {
		t.Fatalf("AdvanceNPCRoute: %v", err)
	}
	r := res.(sim.AdvanceNPCRouteResult)
	if r.Reason != "stale_retry" {
		t.Errorf("Reason = %q, want stale_retry", r.Reason)
	}
	if afterState := w.Published().VillageObjects[firstStopID].CurrentState; afterState != beforeState {
		t.Errorf("stale-arrival flipped object: %q → %q", beforeState, afterState)
	}
	// A re-walk to the stop was dispatched, and StopIdx did not advance.
	if mi := moveIntentOf(t, w, "lamp"); mi == nil {
		t.Error("MoveIntent nil after stale_retry — re-walk not dispatched")
	}
	if route := activeRouteOf(t, w, "lamp"); route == nil || route.StopIdx != 0 {
		t.Errorf("StopIdx advanced on stale_retry: %+v", route)
	}
}

// TestAdvanceNPCRoute_StaleArrivalAbandonsAfterRetries: repeated stale arrivals
// at the same stop exhaust the per-stop retry budget, after which the route is
// abandoned (cleared) rather than parked forever — the object is never flipped.
// A parked route would be fatal once an in-flight route suppresses the
// shift-duty producer (the actor would stay home-exempt indefinitely).
func TestAdvanceNPCRoute_StaleArrivalAbandonsAfterRetries(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrLamplighter, homeDest, sampleLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("start: %v", err)
	}
	firstStopID := firstStopObjectID(t, w, "lamp")
	beforeState := w.Published().VillageObjects[firstStopID].CurrentState

	// Never teleport to the stop → every advance is stale. Drive advances until
	// the route stops retrying; it must abandon, not loop forever, not park. The
	// generous cap guards against an infinite-retry regression (the test fails
	// with the loop's last "stale_retry" rather than hanging).
	var lastReason string
	for i := 0; i < 25; i++ {
		res, err := w.Send(sim.AdvanceNPCRoute("lamp"))
		if err != nil {
			t.Fatalf("advance %d: %v", i, err)
		}
		lastReason = res.(sim.AdvanceNPCRouteResult).Reason
		if lastReason != "stale_retry" {
			break
		}
	}
	if lastReason != "stale_abandoned" {
		t.Errorf("terminal Reason = %q, want stale_abandoned", lastReason)
	}
	if route := activeRouteOf(t, w, "lamp"); route != nil {
		t.Errorf("ActiveRoutes[lamp] not cleared after abandon: %+v", route)
	}
	if afterState := w.Published().VillageObjects[firstStopID].CurrentState; afterState != beforeState {
		t.Errorf("abandoned route flipped object: %q → %q", beforeState, afterState)
	}
}

// TestAdvanceNPCRoute_LastStopGoesReturning: arriving at the last stop
// transitions Phase to Returning and dispatches the home walk.
func TestAdvanceNPCRoute_LastStopGoesReturning(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrLamplighter, homeDest, sampleLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Drive arrivals for every stop. Each advance flips one object and
	// dispatches the next walk; the last advance returns the actor home.
	// Teleport to the current stop's WalkTo before each advance so the
	// active-phase stale-arrival guard accepts the advance.
	nStops := stopCountOf(t, w, "lamp")
	for i := 0; i < nStops-1; i++ {
		teleportToCurrentStop(t, w, "lamp")
		res, err := w.Send(sim.AdvanceNPCRoute("lamp"))
		if err != nil {
			t.Fatalf("advance %d: %v", i, err)
		}
		r := res.(sim.AdvanceNPCRouteResult)
		if r.Reason != "stop_advanced" {
			t.Errorf("advance %d Reason = %q, want stop_advanced", i, r.Reason)
		}
	}
	// Final advance — last stop done, transitions to returning.
	teleportToCurrentStop(t, w, "lamp")
	res, err := w.Send(sim.AdvanceNPCRoute("lamp"))
	if err != nil {
		t.Fatalf("final advance: %v", err)
	}
	r := res.(sim.AdvanceNPCRouteResult)
	if r.Reason != "returning_home" {
		t.Errorf("final advance Reason = %q, want returning_home", r.Reason)
	}
	if phase := routePhaseOf(t, w, "lamp"); phase != sim.RoutePhaseReturning {
		t.Errorf("Phase = %q, want %q", phase, sim.RoutePhaseReturning)
	}
}

// TestAdvanceNPCRoute_ArrivedHomeClearsRoute: after the home leg fires
// and the actor "arrives" home, AdvanceNPCRoute clears the route.
func TestAdvanceNPCRoute_ArrivedHomeClearsRoute(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrLamplighter, homeDest, sampleLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("start: %v", err)
	}

	nStops := stopCountOf(t, w, "lamp")
	// Advance to returning. Teleport to each active stop first.
	for i := 0; i < nStops; i++ {
		teleportToCurrentStop(t, w, "lamp")
		if _, err := w.Send(sim.AdvanceNPCRoute("lamp")); err != nil {
			t.Fatalf("advance %d: %v", i, err)
		}
	}
	// One more advance simulating arrival back home.
	res, err := w.Send(sim.AdvanceNPCRoute("lamp"))
	if err != nil {
		t.Fatalf("advance home: %v", err)
	}
	r := res.(sim.AdvanceNPCRouteResult)
	if r.Reason != "arrived_home" {
		t.Errorf("Reason = %q, want arrived_home", r.Reason)
	}
	if route := activeRouteOf(t, w, "lamp"); route != nil {
		t.Errorf("ActiveRoutes[lamp] not cleared after arrived_home: %+v", route)
	}
}

// TestBuildRouteStops_GreedyNearestNeighbor: two candidates, the closer
// one is visited first regardless of input order.
func TestBuildRouteStops_GreedyNearestNeighbor(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	// Build the walk grid via a Command and call buildRouteStops on it.
	type result struct {
		stops []sim.RouteStop
	}
	res, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			grid, err := sim.BuildWalkGridForTest(world)
			if err != nil {
				return nil, err
			}
			// Cursor at pad origin. Candidate A is far, B is near.
			// Cursor at (PadX+15, PadY+15) — equidistant in pixels to
			// (320,320) is tile (PadX+10, PadY+10) [5 away], and to
			// (960,320) is tile (PadX+30, PadY+10) [15 away]. The
			// nearer tile must be visited first.
			cands := []sim.RouteCandidate{
				{ObjectID: "far", NewState: "x", WorldX: 960, WorldY: 320},
				{ObjectID: "near", NewState: "x", WorldX: 320, WorldY: 320},
			}
			stops := sim.BuildRouteStopsForTest(grid, sim.PadX+15, sim.PadY+15, cands)
			return result{stops: stops}, nil
		},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	got := res.(result).stops
	if len(got) != 2 {
		t.Fatalf("stops len = %d, want 2", len(got))
	}
	if got[0].ObjectID != "near" {
		t.Errorf("first stop = %q, want near (greedy)", got[0].ObjectID)
	}
	if got[1].ObjectID != "far" {
		t.Errorf("second stop = %q, want far", got[1].ObjectID)
	}
}

// --- test helpers ---

// activeRouteOf reads ActiveRoutes[id] inside a Command, returns a
// shallow-copied snapshot for read-only assertions.
func activeRouteOf(t *testing.T, w *sim.World, id sim.ActorID) *sim.NPCRoute {
	t.Helper()
	res, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			route, ok := world.ActiveRoutes[id]
			if !ok || route == nil {
				return (*sim.NPCRoute)(nil), nil
			}
			cp := *route
			return &cp, nil
		},
	})
	if err != nil {
		t.Fatalf("activeRouteOf: %v", err)
	}
	return res.(*sim.NPCRoute)
}

// firstStopObjectID returns the ObjectID of the route's first stop.
func firstStopObjectID(t *testing.T, w *sim.World, id sim.ActorID) sim.VillageObjectID {
	t.Helper()
	route := activeRouteOf(t, w, id)
	if route == nil || len(route.Stops) == 0 {
		t.Fatalf("no route or empty stops for %q", id)
	}
	return route.Stops[0].ObjectID
}

// stopCountOf returns the number of stops on the actor's active route.
func stopCountOf(t *testing.T, w *sim.World, id sim.ActorID) int {
	t.Helper()
	route := activeRouteOf(t, w, id)
	if route == nil {
		t.Fatalf("no route for %q", id)
	}
	return len(route.Stops)
}

// routePhaseOf returns the actor's active route phase, or "" if none.
func routePhaseOf(t *testing.T, w *sim.World, id sim.ActorID) sim.RoutePhase {
	t.Helper()
	route := activeRouteOf(t, w, id)
	if route == nil {
		return ""
	}
	return route.Phase
}

// teleportToCurrentStop sets the actor's CurrentX/CurrentY to the
// active route's current-stop WalkTo, so AdvanceNPCRoute's
// active-phase stale-arrival guard accepts the advance. No-op for
// routes in returning phase (the returning branch doesn't gate on
// tile).
func teleportToCurrentStop(t *testing.T, w *sim.World, id sim.ActorID) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route, ok := world.ActiveRoutes[id]
		if !ok || route.Phase != sim.RoutePhaseActive || route.StopIdx >= len(route.Stops) {
			return nil, nil
		}
		stop := route.Stops[route.StopIdx]
		actor := world.Actors[id]
		actor.Pos.X = stop.WalkTo.X
		actor.Pos.Y = stop.WalkTo.Y
		return nil, nil
	}}); err != nil {
		t.Fatalf("teleportToCurrentStop: %v", err)
	}
}
