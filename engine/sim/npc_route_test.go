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
		// Third target, used only by threeLampCandidates so a test can distinguish
		// "the immediate next stop" from "a stop further along" (LLM-530). Routes are
		// built from the candidates passed in, so seeding it changes no existing
		// two-stop expectation.
		"lamp-C": {ID: "lamp-C", AssetID: "lamp", CurrentState: "lit", Pos: sim.WorldPos{X: 1280, Y: 320}},
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

// threeLampCandidates is the three-stop fixture: enough stops for a test to place
// the actor on a stop that is NOT the immediate next one (LLM-530).
func threeLampCandidates() []sim.RouteCandidate {
	return []sim.RouteCandidate{
		{ObjectID: "lamp-A", NewState: "unlit", WorldX: 640, WorldY: 320},
		{ObjectID: "lamp-B", NewState: "unlit", WorldX: 960, WorldY: 320},
		{ObjectID: "lamp-C", NewState: "unlit", WorldX: 1280, WorldY: 320},
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

	// Capture the actor's start tile (the route start, which is NOT the first
	// stop's WalkTo). We re-assert it before every advance so each arrival is
	// provably stale — making the condition explicit rather than relying on the
	// test world leaving the actor in place across the re-walk dispatches.
	var startX, startY int
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["lamp"]
		startX, startY = a.Pos.X, a.Pos.Y
		return nil, nil
	}}); err != nil {
		t.Fatalf("read start pos: %v", err)
	}

	// Drive advances until the route stops retrying; it must abandon, not loop
	// forever, not park. The generous cap guards against an infinite-retry
	// regression (the test fails with the loop's last "stale_retry" rather than
	// hanging).
	var lastReason string
	for i := 0; i < 25; i++ {
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			a := world.Actors["lamp"]
			a.Pos.X, a.Pos.Y = startX, startY
			return nil, nil
		}}); err != nil {
			t.Fatalf("reset pos %d: %v", i, err)
		}
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

// TestAdvanceNPCRoute_ConstableStaleArrivalYieldsToVolition: a STATEFUL carrier
// (the constable) that finishes a move somewhere other than the stop walked
// himself off on his OWN volition — his LLM reactor issued the move_to. Unlike a
// decorative carrier (TestAdvanceNPCRoute_StaleArrivalReWalks, where the
// lamplighter is re-walked back), the route must NOT drag him back: it ends the
// tour ("yielded_to_volition"), clears the route, and dispatches NO re-walk. This
// is the LLM-520 fix — the constable no longer fights his own feet (the
// Gideon-at-Ellis-Farm oscillation). The route is labeled AttrConstable because
// advanceActiveRoute keys the yield on the route's Label (routeYieldsToVolition),
// independent of the carrier actor's own attributes. The off-stop arrival here is
// cause-agnostic: this same path fires whether his own move_to walked him off or
// an admin force-moved him — both end the tour by design (the yield is a policy,
// not volition-detection).
func TestAdvanceNPCRoute_ConstableStaleArrivalYieldsToVolition(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrConstable, homeDest, sampleLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("start: %v", err)
	}
	firstStopID := firstStopObjectID(t, w, "lamp")
	beforeState := w.Published().VillageObjects[firstStopID].CurrentState

	// Model the real arrival that reaches advanceActiveRoute's stale branch: the
	// actor's own move_to already completed at a tile that is NOT the stop, and
	// finishArrival cleared MoveIntent before emitting ActorArrived. The seed tile
	// (PadX+10, PadY+10) is a proven non-stop tile (see the re-walk test above).
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["lamp"]
		a.Pos = sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 10}
		a.MoveIntent = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("set pre-arrival state: %v", err)
	}

	res, err := w.Send(sim.AdvanceNPCRoute("lamp"))
	if err != nil {
		t.Fatalf("AdvanceNPCRoute: %v", err)
	}
	r := res.(sim.AdvanceNPCRouteResult)
	if r.Reason != "suspended" {
		t.Errorf("Reason = %q, want suspended", r.Reason)
	}
	// The round is PAUSED, not discarded (LLM-531): the cursor survives so the cue
	// can name where he broke off and he can pick it up again.
	route := activeRouteOf(t, w, "lamp")
	if route == nil {
		t.Fatal("ActiveRoutes[lamp] cleared — the part-walked round was thrown away")
	}
	if route.Phase != sim.RoutePhaseSuspended {
		t.Errorf("Phase = %q, want %q", route.Phase, sim.RoutePhaseSuspended)
	}
	if route.StopIdx != 0 {
		t.Errorf("StopIdx = %d, want 0 (he broke off before reaching stop 0)", route.StopIdx)
	}
	// A suspended round must not read as "busy" to the rest of the engine.
	if route.InFlight() {
		t.Error("a suspended route reports InFlight() — shift duty and the backstop would stay suppressed")
	}
	// The crux: NO re-walk was dispatched — he is not dragged back to the stop.
	if mi := moveIntentOf(t, w, "lamp"); mi != nil {
		t.Errorf("MoveIntent stamped after yield — route re-walked instead of yielding: %+v", mi)
	}
	// Yield returns before the per-stop flip, so the object is untouched.
	if afterState := w.Published().VillageObjects[firstStopID].CurrentState; afterState != beforeState {
		t.Errorf("yielded route flipped object: %q → %q", beforeState, afterState)
	}
}

// TestAdvanceNPCRoute_SupersededRouteWalkCannotArrive documents the invariant
// that makes the LLM-520 yield's clear-by-actor-ID safe against a stale arrival
// from a replaced route (the code_review concern): a route install ALWAYS
// dispatches a walk that overwrites the actor's single MoveIntent, and the
// superseded walk dies silently — no ActorArrived (commands_move.go: "the old
// attempt dies silently"). The locomotion ticker only ever emits an arrival for
// the actor's CURRENT MoveIntent (locomotion_ticker.go finishArrival), so a
// stale arrival generated by route N can never be delivered against its
// replacement N+1: N's walk was discarded and never reaches finishArrival.
// Replacing a constable route and checking the walk's AttemptID + the route Gen
// both changed proves the old walk is dead.
func TestAdvanceNPCRoute_SupersededRouteWalkCannotArrive(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrConstable, homeDest, sampleLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("first start: %v", err)
	}
	first := moveIntentOf(t, w, "lamp")
	if first == nil {
		t.Fatal("first route did not dispatch a walk")
	}
	firstAttempt := first.AttemptID
	firstRoute := activeRouteOf(t, w, "lamp")
	if firstRoute == nil {
		t.Fatal("first route not installed")
	}
	firstGen := firstRoute.Gen

	// Supersede with a fresh constable route (an operator force-tour, say).
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrConstable, homeDest, sampleLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("supersede start: %v", err)
	}
	second := moveIntentOf(t, w, "lamp")
	if second == nil {
		t.Fatal("replacement route did not dispatch a walk")
	}
	// The single MoveIntent was overwritten: the first route's walk is dead and
	// can never emit an ActorArrived, so it can't reach the yield/advance branch
	// against the replacement.
	if second.AttemptID == firstAttempt {
		t.Errorf("MoveIntent AttemptID unchanged after supersede (%d) — old walk still live", second.AttemptID)
	}
	// The replacement carries a fresh generation, so even the async dwell-timer
	// path (Gen-guarded) rejects a stale callback from the first install.
	if g := activeRouteOf(t, w, "lamp").Gen; g == firstGen {
		t.Errorf("replacement route Gen unchanged (%d) — supersede did not re-install", g)
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
			stops := sim.BuildRouteStopsForTest(world, grid, sim.PadX+15, sim.PadY+15, cands)
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

// TestBuildRouteStops_PrefersLoiterPin: a candidate whose object has a
// resolvable, walkable loiter pin gets WalkTo == that pin, not merely a
// tile abutting the object's footprint (ZBBS-HOME-458 — routing one tile
// short of the pin jammed the town crier against a noticeboard in a
// one-lane chokepoint a single passer-by could wedge). lamp-A (WorldPos
// 640,320, asset "lamp": no door offset, footprint 0) resolves through
// computeLoiterTile's footprint-fallback branch to anchor+(0,2), which on
// the all-grass test terrain is walkable and reachable — so the pin wins
// over the adjacent-tile fallback.
func TestBuildRouteStops_PrefersLoiterPin(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	anchor := sim.WorldToTile(640, 320)
	wantPin := sim.Position{X: anchor.X, Y: anchor.Y + 2}

	res, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			grid, err := sim.BuildWalkGridForTest(world)
			if err != nil {
				return nil, err
			}
			cands := []sim.RouteCandidate{
				{ObjectID: "lamp-A", NewState: "unlit", WorldX: 640, WorldY: 320},
			}
			return sim.BuildRouteStopsForTest(world, grid, sim.PadX+10, sim.PadY+10, cands), nil
		},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	stops := res.([]sim.RouteStop)
	if len(stops) != 1 {
		t.Fatalf("stops len = %d, want 1", len(stops))
	}
	if stops[0].WalkTo != wantPin {
		t.Errorf("WalkTo = %+v, want loiter pin %+v", stops[0].WalkTo, wantPin)
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

// TestAdvanceNPCRoute_ConstableWalkingOnwardContinuesRound is the LLM-530 engine
// half. The rounds cue now NAMES the next business, because move_to is how this NPC
// says "I am finished with this place" — so he will walk there himself. Arriving at
// a stop still AHEAD on the circuit is staying on the round, not leaving it: the
// route adopts that stop and carries on, rather than treating the move as volition
// and ending the tour (which would make naming the next stop worth exactly one
// extra visit). Contrast TestAdvanceNPCRoute_ConstableStaleArrivalYieldsToVolition,
// where he lands somewhere NOT on the circuit and the tour does end.
func TestAdvanceNPCRoute_ConstableWalkingOnwardContinuesRound(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrConstable, homeDest, sampleLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Assert the exact fixture shape the expectations below depend on: with exactly
	// 2 stops, the one he walks to IS the last, so the round finishes it and returns.
	if n := stopCountOf(t, w, "lamp"); n != 2 {
		t.Fatalf("fixture must have exactly 2 stops for these expectations, got %d", n)
	}

	// He is en route to stop 0 but walks himself to stop 1 instead — the business the
	// cue named. Clear MoveIntent as finishArrival does before emitting ActorArrived.
	var next sim.Position
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route := world.ActiveRoutes["lamp"]
		next = route.Stops[1].WalkTo
		a := world.Actors["lamp"]
		a.Pos = next
		a.MoveIntent = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("place actor at stop 1: %v", err)
	}

	res, err := w.Send(sim.AdvanceNPCRoute("lamp"))
	if err != nil {
		t.Fatalf("AdvanceNPCRoute: %v", err)
	}
	r := res.(sim.AdvanceNPCRouteResult)
	// The crux: he is NOT treated as having left the round.
	if r.Reason == "yielded_to_volition" {
		t.Fatalf("walking on to a stop still on the circuit ended the tour (Reason=%q)", r.Reason)
	}
	// This fixture has exactly 2 stops, so the one he walked to IS the last: stop 1
	// counts as visited and the route transitions to its return leg. With a longer
	// circuit the same path returns "stop_advanced" and walks him to the next stop.
	if r.Reason != "returning_home" {
		t.Errorf("Reason = %q, want returning_home (stop 1 is the last of 2, so the round finishes it and heads back)", r.Reason)
	}
	route := activeRouteOf(t, w, "lamp")
	if route == nil {
		t.Fatal("route cleared after he walked on to a stop still on the circuit")
	}
	if route.Phase != sim.RoutePhaseReturning {
		t.Errorf("Phase = %q, want %q", route.Phase, sim.RoutePhaseReturning)
	}
	// He must not be dragged backwards to stop 0, which he had never reached.
	if route.StopIdx == 0 {
		t.Error("StopIdx still 0 — the stop he walked to was not adopted")
	}
}

// TestAdvanceNPCRoute_ConstableArrivalBeyondNextStopStillYields pins the LLM-530
// scan's deliberate narrowness: adoption covers the IMMEDIATE next stop only,
// because the cue names exactly one place. An arrival at a stop further along is
// NOT adopted — it would jump the cursor over an intervening stop, skipping its
// visit and dwell (and, for any future yielding carrier whose stops flip state,
// its flip). Such an arrival falls through to the ordinary volition yield.
func TestAdvanceNPCRoute_ConstableArrivalBeyondNextStopStillYields(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrConstable, homeDest, threeLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("start: %v", err)
	}
	if n := stopCountOf(t, w, "lamp"); n != 3 {
		t.Fatalf("fixture must have exactly 3 stops to test a non-adjacent stop, got %d", n)
	}

	// Stand him on stop 2 — a real stop on his circuit, but TWO ahead of the cursor
	// at stop 0, so adopting it would jump over stop 1 and skip its visit entirely.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route := world.ActiveRoutes["lamp"]
		a := world.Actors["lamp"]
		a.Pos = route.Stops[2].WalkTo
		a.MoveIntent = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("place actor on stop 2: %v", err)
	}

	res, err := w.Send(sim.AdvanceNPCRoute("lamp"))
	if err != nil {
		t.Fatalf("AdvanceNPCRoute: %v", err)
	}
	if r := res.(sim.AdvanceNPCRouteResult); r.Reason != "suspended" {
		t.Errorf("Reason = %q, want suspended (an arrival that is not the next stop is stepping away, which pauses the round)", r.Reason)
	}
	route := activeRouteOf(t, w, "lamp")
	if route == nil {
		t.Fatal("route discarded rather than suspended")
	}
	if route.Phase != sim.RoutePhaseSuspended {
		t.Errorf("Phase = %q, want %q", route.Phase, sim.RoutePhaseSuspended)
	}
	// The crux for this case: the cursor did NOT jump to stop 2.
	if route.StopIdx != 0 {
		t.Errorf("StopIdx = %d, want 0 — the cursor must not jump over stop 1", route.StopIdx)
	}
}

// TestAdvanceNPCRoute_SuspendedRoundResumesWhenHeWalksBack is the other half of
// LLM-531: a need interrupts a round, it does not cancel it. He steps away (the
// well, a conversation), the round waits with its cursor intact, and when he walks
// back to the stop he broke off at, it picks up from there — no engine coercion,
// no re-walk. Live, the round used to be discarded at the moment he stepped away,
// so after a drink nothing told him six doors were still unwalked.
func TestAdvanceNPCRoute_SuspendedRoundResumesWhenHeWalksBack(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrConstable, homeDest, threeLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("start: %v", err)
	}

	// He steps away: arrives somewhere that is no stop of his at all.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["lamp"]
		a.Pos = sim.Position{X: sim.PadX + 40, Y: sim.PadY + 40}
		a.MoveIntent = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("step away: %v", err)
	}
	res, err := w.Send(sim.AdvanceNPCRoute("lamp"))
	if err != nil {
		t.Fatalf("advance (step away): %v", err)
	}
	if r := res.(sim.AdvanceNPCRouteResult); r.Reason != "suspended" {
		t.Fatalf("Reason = %q, want suspended", r.Reason)
	}

	// While suspended, a further arrival elsewhere leaves the round waiting rather
	// than nagging or clearing it — he is off seeing to himself.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["lamp"]
		a.Pos = sim.Position{X: sim.PadX + 41, Y: sim.PadY + 41}
		a.MoveIntent = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("wander: %v", err)
	}
	res, err = w.Send(sim.AdvanceNPCRoute("lamp"))
	if err != nil {
		t.Fatalf("advance (wander): %v", err)
	}
	if r := res.(sim.AdvanceNPCRouteResult); r.Reason != "still_suspended" {
		t.Errorf("Reason = %q, want still_suspended", r.Reason)
	}
	if route := activeRouteOf(t, w, "lamp"); route == nil || route.Phase != sim.RoutePhaseSuspended {
		t.Fatalf("round not still waiting: %+v", route)
	}

	// He walks back to the stop he broke off at — the one the cue names.
	var resumeStopID sim.VillageObjectID
	var resumeIdx int
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route := world.ActiveRoutes["lamp"]
		resumeIdx = route.StopIdx
		resumeStopID = route.Stops[resumeIdx].ObjectID
		a := world.Actors["lamp"]
		a.Pos = route.Stops[resumeIdx].WalkTo
		a.MoveIntent = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("walk back: %v", err)
	}
	res, err = w.Send(sim.AdvanceNPCRoute("lamp"))
	if err != nil {
		t.Fatalf("advance (resume): %v", err)
	}
	if r := res.(sim.AdvanceNPCRouteResult); r.Reason != "stop_advanced" {
		t.Errorf("Reason = %q, want stop_advanced (the round picks up from where he broke off)", r.Reason)
	}
	route := activeRouteOf(t, w, "lamp")
	if route == nil {
		t.Fatal("route gone after resume")
	}
	if route.Phase != sim.RoutePhaseActive {
		t.Errorf("Phase = %q, want %q after resuming", route.Phase, sim.RoutePhaseActive)
	}
	if !route.InFlight() {
		t.Error("resumed route still reports InFlight() == false")
	}
	// And the engine is carrying him again — a walk to the next stop is dispatched.
	if mi := moveIntentOf(t, w, "lamp"); mi == nil {
		t.Error("no walk dispatched after resume — the round is not carrying him")
	}
	// Resuming hands to the ordinary clean-visit path, so the stop he came back to is
	// genuinely VISITED, not merely counted: its object flips and the cursor advances
	// past it. Asserting the side effects (code_review) rather than just the reason
	// string — a resume that skipped them would look identical from the outside.
	if got := w.Published().VillageObjects[resumeStopID].CurrentState; got != "unlit" {
		t.Errorf("stop object %q state = %q, want unlit — resuming did not perform the visit", resumeStopID, got)
	}
	if route.StopIdx <= resumeIdx {
		t.Errorf("StopIdx = %d, want > %d — the resumed stop was not counted as visited", route.StopIdx, resumeIdx)
	}
}

// TestAdvanceNPCRoute_SuspendBurnsDwellGeneration covers the race code_review found
// in LLM-531: stopping the dwell timer only cancels a callback that has not yet
// fired, and one already queued through SendContext still runs later. The Phase
// check alone does not save the round, because RESUMING restores exactly the state
// that callback expects — same StopIdx, Phase active again — so without a new
// generation it would sail through every guard and advance the round a stop early,
// minutes after he picked it up. Suspension therefore burns a fresh Gen, which
// invalidates any in-flight callback permanently.
func TestAdvanceNPCRoute_SuspendBurnsDwellGeneration(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrConstable, homeDest, threeLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("start: %v", err)
	}
	genAtArm := activeRouteOf(t, w, "lamp").Gen

	// He steps away — the round suspends.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["lamp"]
		a.Pos = sim.Position{X: sim.PadX + 40, Y: sim.PadY + 40}
		a.MoveIntent = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("step away: %v", err)
	}
	if _, err := w.Send(sim.AdvanceNPCRoute("lamp")); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	genSuspended := activeRouteOf(t, w, "lamp").Gen
	if genSuspended == genAtArm {
		t.Fatalf("Gen unchanged across suspension (%d) — a dwell callback queued before "+
			"the pause would still match after resuming and advance the round spuriously", genSuspended)
	}

	// He comes back and the round resumes: the generation must NOT revert to the one
	// any pre-suspension callback captured.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route := world.ActiveRoutes["lamp"]
		a := world.Actors["lamp"]
		a.Pos = route.Stops[route.StopIdx].WalkTo
		a.MoveIntent = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("walk back: %v", err)
	}
	if _, err := w.Send(sim.AdvanceNPCRoute("lamp")); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if route := activeRouteOf(t, w, "lamp"); route != nil && route.Gen == genAtArm {
		t.Error("resumed route reverted to the pre-suspension Gen — a stale dwell callback would match again")
	}
}
