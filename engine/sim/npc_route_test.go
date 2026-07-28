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

// TestBeatRoute_StartsWithoutDispatchingAWalk is the difference between a beat and
// every other route, and the reason the whole class of bug this replaced cannot
// recur (LLM-548): a walk that is never dispatched is a walk that can never be
// refused. The old round died the first time a dispatch was rejected because he was
// standing in a conversation that had gone quiet, and it was rejected at the second
// stop of eight.
//
// The first leg is deliberately included in "dispatches nothing". It is the one
// place the engine could plausibly still start him off, and the worst place for it:
// a round begins at his post, so a walk with LeaveHuddleFirst set would drag him out
// of whatever conversation he was having in order to send him on his rounds.
func TestBeatRoute_StartsWithoutDispatchingAWalk(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	res, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrConstable, homeDest, sampleLampCandidates(), time.Now().UTC()))
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if started := res.(sim.StartNPCRouteResult); started.Stops == 0 {
		t.Fatal("beat installed no stops — the circuit is what the cue reads from")
	}
	route := activeRouteOf(t, w, "lamp")
	if route == nil {
		t.Fatal("ActiveRoutes[lamp] empty — the beat must be installed even though nothing is dispatched")
	}
	if route.Phase != sim.RoutePhaseBeat {
		t.Errorf("Phase = %q, want %q", route.Phase, sim.RoutePhaseBeat)
	}
	if mi := moveIntentOf(t, w, "lamp"); mi != nil {
		t.Errorf("beat start dispatched a walk: %+v — he takes himself where the cue names", mi)
	}
	// A beat is never "busy". This is what keeps shift duty waking him, the idle
	// backstop live, and — the load-bearing one — lets an ordinary arrival encounter
	// form when he turns up at a shop, which is how the conversations at his stops
	// happen at all.
	if route.InFlight() {
		t.Error("a beat route reports InFlight() — duty, the backstop and arrival encounters would stay suppressed")
	}
}

// TestBeatRoute_ArrivalElsewhereIsANoOp: he has gone to see to something of his own
// — a thirst, an errand, a suspicion. That is his to do. The round simply waits,
// with nothing dragged back and nothing thrown away, and the cue goes on naming what
// is left whenever he next reads it.
//
// The predecessor of this behaviour ended the tour outright on an off-stop arrival,
// which is how a constable who stopped for a drink at the well came back to find six
// doors unwalked and no reason left to walk them.
func TestBeatRoute_ArrivalElsewhereIsANoOp(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrConstable, homeDest, sampleLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("start: %v", err)
	}
	firstStopID := firstStopObjectID(t, w, "lamp")
	beforeState := w.Published().VillageObjects[firstStopID].CurrentState
	before := activeRouteOf(t, w, "lamp")
	beforeIdx, beforeGen := before.StopIdx, before.Gen

	// His own move_to completed at a tile that is no stop on the circuit;
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
	if r := res.(sim.AdvanceNPCRouteResult); r.Reason != "beat_elsewhere" {
		t.Errorf("Reason = %q, want beat_elsewhere", r.Reason)
	}
	route := activeRouteOf(t, w, "lamp")
	if route == nil {
		t.Fatal("ActiveRoutes[lamp] cleared — the part-walked round was thrown away")
	}
	if route.StopIdx != beforeIdx || route.Gen != beforeGen {
		t.Errorf("cursor/gen moved on an off-circuit arrival: %d/%d → %d/%d",
			beforeIdx, beforeGen, route.StopIdx, route.Gen)
	}
	// The crux: no re-walk. He is not dragged back to the stop.
	if mi := moveIntentOf(t, w, "lamp"); mi != nil {
		t.Errorf("MoveIntent stamped on an off-circuit arrival — the beat re-walked him: %+v", mi)
	}
	if afterState := w.Published().VillageObjects[firstStopID].CurrentState; afterState != beforeState {
		t.Errorf("beat flipped an object: %q → %q — a beat visits, it does not mutate", beforeState, afterState)
	}
}

// TestAdvanceNPCRoute_SupersededRouteWalkCannotArrive documents the invariant
// that makes clear-by-actor-ID safe against a stale arrival from a replaced route
// (the code_review concern): a DISPATCHED route install always dispatches a walk that overwrites the actor's single MoveIntent, and the
// superseded walk dies silently — no ActorArrived (commands_move.go: "the old
// attempt dies silently"). The locomotion ticker only ever emits an arrival for
// the actor's CURRENT MoveIntent (locomotion_ticker.go finishArrival), so a
// stale arrival generated by route N can never be delivered against its
// replacement N+1: N's walk was discarded and never reaches finishArrival.
// Replacing a dispatched route and checking the walk's AttemptID + the route Gen
// both changed proves the old walk is dead. A BEAT route dispatches no walk at all
// (LLM-548), so it has no stale walk to worry about and this invariant is about the
// decorative carriers that still have one.
func TestAdvanceNPCRoute_SupersededRouteWalkCannotArrive(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrTownCrier, homeDest, sampleLampCandidates(), time.Now().UTC())); err != nil {
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

	// Supersede with a fresh crier route (an operator force-tour, say).
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrTownCrier, homeDest, sampleLampCandidates(), time.Now().UTC())); err != nil {
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
	// The replacement carries a fresh generation, so an operator comparing two reads
	// can tell a re-install from the same route continuing.
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

// standAtStop puts the actor exactly on stop idx's WalkTo tile and clears his
// MoveIntent — the state finishArrival leaves behind when a move he issued himself
// completes there. A beat is driven entirely by arrivals, so this is how every beat
// test says "he took himself to this place".
func standAtStop(t *testing.T, w *sim.World, id sim.ActorID, idx int) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route := world.ActiveRoutes[id]
		if route == nil || idx >= len(route.Stops) {
			t.Fatalf("standAtStop: no route or stop %d out of range", idx)
		}
		a := world.Actors[id]
		a.Pos.X, a.Pos.Y = route.Stops[idx].WalkTo.X, route.Stops[idx].WalkTo.Y
		a.MoveIntent = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("standAtStop: %v", err)
	}
}

// visitorSlotBeside returns a tile one king's move off the loiter pin — where
// pickVisitorSlot actually parks an actor who walked himself to a place. The pin
// itself is taken only when all eight slots are blocked, so this, not the pin, is
// where a beat carrier stands at essentially every stop he calls at.
func visitorSlotBeside(pin sim.TilePos) sim.TilePos {
	return sim.TilePos{X: pin.X + 1, Y: pin.Y}
}

// startBeat installs a beat over the given candidates and returns nothing — the
// route is read back through activeRouteOf. Nothing is dispatched, so the actor
// stays wherever the fixture left him.
func startBeat(t *testing.T, w *sim.World, id sim.ActorID, cands []sim.RouteCandidate) {
	t.Helper()
	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute(id, sim.AttrConstable, homeDest, cands, time.Now().UTC())); err != nil {
		t.Fatalf("start beat: %v", err)
	}
}

// TestBeatRoute_CreditsWhicheverStopHeCallsAt is the model in one test (LLM-548):
// a round is COVERAGE, not a sequence. He calls at stop 2 while the cursor sits on
// 0, and the round records it — because he was demonstrably there, and a round that
// cannot see that goes on offering him a shop he is standing in.
//
// The predecessor credited only the stop the engine had walked him to, so a
// constable who called at the General Store, the Blacksmith and the Inn inside
// twenty minutes was credited with none of them, re-derived the same plan on his
// next wake, and set off for somewhere he had just been.
func TestBeatRoute_CreditsWhicheverStopHeCallsAt(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	startBeat(t, w, "lamp", threeLampCandidates())
	if idx := activeRouteOf(t, w, "lamp").StopIdx; idx != 0 {
		t.Fatalf("fixture: cursor starts at %d, want 0", idx)
	}

	standAtStop(t, w, "lamp", 2)
	res, err := w.Send(sim.AdvanceNPCRoute("lamp"))
	if err != nil {
		t.Fatalf("AdvanceNPCRoute: %v", err)
	}
	if r := res.(sim.AdvanceNPCRouteResult); r.Reason != "beat_stop_called" {
		t.Errorf("Reason = %q, want beat_stop_called", r.Reason)
	}
	route := activeRouteOf(t, w, "lamp")
	if route == nil {
		t.Fatal("beat cleared — two stops are still owed")
	}
	if !sim.RouteStopVisited(route, 2) {
		t.Error("stop 2 not credited — he stood on it and the round did not notice")
	}
	if sim.RouteStopVisited(route, 0) || sim.RouteStopVisited(route, 1) {
		t.Error("an uncalled stop was credited")
	}
	if got := sim.RouteUnvisitedCount(route); got != 2 {
		t.Errorf("unvisited = %d, want 2", got)
	}
	// The cursor must point at something still OWED, or the cue names a place he
	// has already been and sends him back to it.
	if sim.RouteStopVisited(route, route.StopIdx) {
		t.Errorf("cursor is on stop %d, which is already visited", route.StopIdx)
	}
	// Still nothing dispatched: crediting a stop is bookkeeping, not a marching order.
	if mi := moveIntentOf(t, w, "lamp"); mi != nil {
		t.Errorf("beat dispatched a walk on crediting a stop: %+v", mi)
	}
}

// TestBeatRoute_CreditsAStopReachedOnFootFromAVisitorSlot: his own move_to resolves
// to a StructureVisit and pickVisitorSlot parks him in one of the eight slots AROUND
// the loiter pin, taking the pin itself only when all eight are blocked. So the
// strict pin-equality test essentially never matches the path it exists to serve.
//
// This is the LLM-543 predicate, and it is doubly load-bearing under a beat: EVERY
// arrival is now one he made himself, so a strict test here would credit nothing at
// all and the round could never finish.
func TestBeatRoute_CreditsAStopReachedOnFootFromAVisitorSlot(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	startBeat(t, w, "lamp", sampleLampCandidates())

	// One tile off the pin — a visitor slot, where his own walk actually lands him.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route := world.ActiveRoutes["lamp"]
		stop := route.Stops[0]
		a := world.Actors["lamp"]
		a.Pos = sim.TilePos{X: stop.WalkTo.X + 1, Y: stop.WalkTo.Y}
		a.MoveIntent = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("park beside the pin: %v", err)
	}

	if _, err := w.Send(sim.AdvanceNPCRoute("lamp")); err != nil {
		t.Fatalf("AdvanceNPCRoute: %v", err)
	}
	route := activeRouteOf(t, w, "lamp")
	if route == nil {
		t.Fatal("beat cleared — a stop is still owed")
	}
	if !sim.RouteStopVisited(route, 0) {
		t.Error("standing in a visitor slot beside the pin did not credit the stop — " +
			"a beat carrier arrives this way every single time, so nothing would ever be credited")
	}
}

// TestBeatRoute_CompletesWhenEveryStopIsCalledAtOutOfOrder: the round ends when the
// circuit is COVERED, in whatever order he walked it, and ending means the route is
// gone. Clearing it is what un-suppresses the shift-duty steer, and that is what
// sends him back to his post — no home walk is dispatched, because the same duty
// machinery that governs every other on-shift NPC does it.
//
// The wrap in nextUnvisitedFrom is what makes this terminate: he takes the last stop
// second, leaving an earlier one unwalked and BEHIND the cursor, and a forward-only
// search would strand it — the count would sit at one for the rest of the day.
func TestBeatRoute_CompletesWhenEveryStopIsCalledAtOutOfOrder(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	startBeat(t, w, "lamp", threeLampCandidates())

	// Deliberately out of order, ending on one BEHIND the cursor.
	for _, idx := range []int{2, 0, 1} {
		if activeRouteOf(t, w, "lamp") == nil {
			t.Fatalf("beat cleared before stop %d — the circuit was not covered", idx)
		}
		standAtStop(t, w, "lamp", idx)
		if _, err := w.Send(sim.AdvanceNPCRoute("lamp")); err != nil {
			t.Fatalf("advance at stop %d: %v", idx, err)
		}
	}
	if route := activeRouteOf(t, w, "lamp"); route != nil {
		t.Errorf("beat still installed after every stop was called at: unvisited=%d — "+
			"the duty steer stays suppressed and he never goes back to his post",
			sim.RouteUnvisitedCount(route))
	}
	// Completion dispatches nothing either. He walks home under the duty steer.
	if mi := moveIntentOf(t, w, "lamp"); mi != nil {
		t.Errorf("beat completion dispatched a home walk: %+v", mi)
	}
}

// TestBeatRoute_ArrivingTwiceAtTheSameStopIsHarmless: a duplicate ActorArrived for a
// place already on the books must not double-count or disturb the cursor. The old
// dispatched round needed an explicit guard flag for this (a second arrival would
// arm a second dwell timer); a beat needs none, because crediting is idempotent —
// which is the point of recording coverage rather than counting steps.
func TestBeatRoute_ArrivingTwiceAtTheSameStopIsHarmless(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	startBeat(t, w, "lamp", threeLampCandidates())
	standAtStop(t, w, "lamp", 1)
	if _, err := w.Send(sim.AdvanceNPCRoute("lamp")); err != nil {
		t.Fatalf("first arrival: %v", err)
	}
	afterFirst := activeRouteOf(t, w, "lamp")
	idxAfterFirst, unvisitedAfterFirst := afterFirst.StopIdx, sim.RouteUnvisitedCount(afterFirst)

	if _, err := w.Send(sim.AdvanceNPCRoute("lamp")); err != nil {
		t.Fatalf("duplicate arrival: %v", err)
	}
	after := activeRouteOf(t, w, "lamp")
	if after == nil {
		t.Fatal("beat cleared by a duplicate arrival")
	}
	if after.StopIdx != idxAfterFirst || sim.RouteUnvisitedCount(after) != unvisitedAfterFirst {
		t.Errorf("duplicate arrival moved the round: cursor %d→%d, unvisited %d→%d",
			idxAfterFirst, after.StopIdx, unvisitedAfterFirst, sim.RouteUnvisitedCount(after))
	}
}

// TestSnapshotProjection_ConstableInAVisitorSlotStillHasHisRoundsCue covers the
// LLM-543 seam the perception goldens structurally cannot: they take the snapshot
// projection as GIVEN (each fixture sets RouteStopObjectID by hand), so a bug in
// the projection that feeds them renders a perfect cue in every golden and an empty
// one in the village.
//
// The projection gated the stop name, the remaining count and the next stop's name
// on exact pin equality. A constable who reached the stop on his own feet stands in
// a visitor slot beside the pin, so all three dropped together and his cue collapsed
// to a bare "You are walking your rounds." — no place, no count, nowhere to go. That
// is the scene he re-read on every wake while the round sat frozen.
func TestSnapshotProjection_ConstableInAVisitorSlotStillHasHisRoundsCue(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrConstable, homeDest, threeLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Park him where his own locomotion leaves him: beside the current stop's pin,
	// not on it.
	var wantStop, wantNext sim.VillageObjectID
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route := world.ActiveRoutes["lamp"]
		wantStop = route.Stops[route.StopIdx].ObjectID
		wantNext = route.Stops[route.StopIdx+1].ObjectID
		a := world.Actors["lamp"]
		a.Pos = visitorSlotBeside(route.Stops[route.StopIdx].WalkTo)
		a.MoveIntent = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("place beside pin: %v", err)
	}

	snap := w.Published().Actors["lamp"]
	if snap == nil {
		t.Fatal("no published snapshot for the constable")
	}
	if snap.RouteStopObjectID != wantStop {
		t.Errorf("RouteStopObjectID = %q, want %q — standing in a stop's visitor slot IS "+
			"standing at that stop, and without it the cue cannot name where he is",
			snap.RouteStopObjectID, wantStop)
	}
	if snap.RouteStopsAhead != 2 {
		t.Errorf("RouteStopsAhead = %d, want 2 — the cue drops the whole round-continues "+
			"line at 0, so a quiet stop reads as a dead end", snap.RouteStopsAhead)
	}
	if snap.RouteNextStopObjectID != wantNext {
		t.Errorf("RouteNextStopObjectID = %q, want %q — the next stop's name is the only "+
			"move_to token the round offers him", snap.RouteNextStopObjectID, wantNext)
	}
}

// TestSnapshotProjection_CountAndNextAnchorOnWhereHeStands covers the seam a beat
// opens that a dispatched round did not (LLM-548).
//
// A beat credits a stop the INSTANT he arrives, so from then until he leaves he is
// standing on a stop that is already on the books and the cursor has moved on to
// somewhere else. Anchoring the count and the next name on the cursor at that
// moment would count the ground under his feet among the places still ahead of him
// — "seven places lie ahead" read by a man standing in the seventh, which is the
// exact sentence that used to send him back somewhere he had just been — and would
// offer that same place as the one to go to next.
//
// Both are therefore measured from where he IS whenever he is at a stop. The
// dispatched round never had to face this: it credited a stop on DEPARTURE, so the
// cursor and his feet agreed for the whole of every visit.
func TestSnapshotProjection_CountAndNextAnchorOnWhereHeStands(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	startBeat(t, w, "lamp", threeLampCandidates())

	// He calls at stop 1 himself. The beat credits it and moves the cursor away.
	standAtStop(t, w, "lamp", 1)
	if _, err := w.Send(sim.AdvanceNPCRoute("lamp")); err != nil {
		t.Fatalf("advance: %v", err)
	}

	var here, cursorStop sim.VillageObjectID
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route := world.ActiveRoutes["lamp"]
		if route == nil {
			t.Fatal("beat cleared with two stops still owed")
		}
		if !sim.RouteStopVisited(route, 1) {
			t.Fatal("fixture: stop 1 was not credited")
		}
		if route.StopIdx == 1 {
			t.Fatal("fixture: cursor did not move off the credited stop")
		}
		here = route.Stops[1].ObjectID
		cursorStop = route.Stops[route.StopIdx].ObjectID
		return nil, nil
	}}); err != nil {
		t.Fatalf("read route: %v", err)
	}

	snap := w.Published().Actors["lamp"]
	if snap == nil {
		t.Fatal("no published snapshot for the constable")
	}
	if snap.RouteStopObjectID != here {
		t.Errorf("RouteStopObjectID = %q, want %q — he is still standing there, and the cue "+
			"cannot say 'you stand before' anywhere once the visit is on the books",
			snap.RouteStopObjectID, here)
	}
	// Two stops are unwalked, and neither is the one he is standing on.
	if snap.RouteStopsAhead != 2 {
		t.Errorf("RouteStopsAhead = %d, want 2", snap.RouteStopsAhead)
	}
	if snap.RouteNextStopObjectID == here {
		t.Errorf("RouteNextStopObjectID names %q, the place he is standing in — "+
			"that is the cue sending him where he already is", here)
	}
	if snap.RouteNextStopObjectID != cursorStop {
		t.Errorf("RouteNextStopObjectID = %q, want %q — the cue and the beat's own cursor "+
			"must never name different places", snap.RouteNextStopObjectID, cursorStop)
	}
}

// TestBeatRoute_OverlappingStopRegionsResolveToTheCursor pins the resolution order
// in reachedStopIndex (code_review, LLM-543). Two stops whose tolerant regions
// overlap — businesses close enough to share a doorstep — must not let a
// neighbouring pin answer for the one he actually walked to. The cursor wins
// outright, because it is the place the cue named as next and therefore the one he
// most likely set out for; plain first-match would credit a stop he never went to
// while leaving the one he did go to still owed.
func TestBeatRoute_OverlappingStopRegionsResolveToTheCursor(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrConstable, homeDest, threeLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Force stop 2's pin onto stop 0's so both match where he stands, then put him
	// in a visitor slot beside the shared pin. The cursor is on stop 0.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route := world.ActiveRoutes["lamp"]
		route.Stops[2].WalkTo = route.Stops[0].WalkTo
		a := world.Actors["lamp"]
		a.Pos = visitorSlotBeside(route.Stops[0].WalkTo)
		a.MoveIntent = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("overlap the pins: %v", err)
	}
	if _, err := w.Send(sim.AdvanceNPCRoute("lamp")); err != nil {
		t.Fatalf("advance: %v", err)
	}
	route := activeRouteOf(t, w, "lamp")
	if route == nil {
		t.Fatal("route gone")
	}
	if !sim.RouteStopVisited(route, 0) {
		t.Error("the cursor's stop was not credited — an overlapping neighbour answered for it")
	}
	if sim.RouteStopVisited(route, 2) {
		t.Error("credited stop 2 for an arrival at stop 0's pin — the cursor must win")
	}
}

// TestBeatRoute_ReachedStopResolutionOrder covers the rest of the ordering rule
// in reachedStopIndex (code_review, LLM-543). The cursor preference has its own
// test above; these are the two cases that keep the original silent failure from
// coming back — a VISITED neighbour answering for a stop he still owes, so the owed
// one is never recorded and the count never falls.
func TestBeatRoute_ReachedStopResolutionOrder(t *testing.T) {
	// beatAtStopZero starts a three-stop beat (cursor on stop 0), then runs
	// mutate to arrange the geometry the case needs. The cursor sits on stop 0.
	beatAtStopZero := func(t *testing.T, mutate func(route *sim.NPCRoute, a *sim.Actor)) (*sim.World, func()) {
		t.Helper()
		w, cancel := buildRouteTestWorld(t)
		homeDest := sim.NewStructureEnterDestination("home")
		if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrConstable, homeDest, threeLampCandidates(), time.Now().UTC())); err != nil {
			cancel()
			t.Fatalf("start: %v", err)
		}
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			mutate(world.ActiveRoutes["lamp"], world.Actors["lamp"])
			world.Actors["lamp"].MoveIntent = nil
			return nil, nil
		}}); err != nil {
			cancel()
			t.Fatalf("arrange: %v", err)
		}
		return w, cancel
	}

	t.Run("an unvisited later stop is credited when the cursor stop does not match", func(t *testing.T) {
		w, cancel := beatAtStopZero(t, func(route *sim.NPCRoute, a *sim.Actor) {
			a.Pos = visitorSlotBeside(route.Stops[2].WalkTo)
		})
		defer cancel()
		if _, err := w.Send(sim.AdvanceNPCRoute("lamp")); err != nil {
			t.Fatalf("advance: %v", err)
		}
		route := activeRouteOf(t, w, "lamp")
		if route == nil || !route.Visited[2] {
			t.Fatal("the stop he called at was not credited")
		}
	})

	t.Run("an unvisited stop wins over a visited one sharing its ground", func(t *testing.T) {
		w, cancel := beatAtStopZero(t, func(route *sim.NPCRoute, a *sim.Actor) {
			// Stop 1 is already walked and sits on the same ground as stop 2, which is
			// not. Plain first-match would hand the arrival to stop 1 and stop 2 would
			// never be recorded — the count would never reach zero and the round would
			// never end.
			route.Visited[1] = true
			route.Stops[1].WalkTo = route.Stops[2].WalkTo
			a.Pos = visitorSlotBeside(route.Stops[2].WalkTo)
		})
		defer cancel()
		if _, err := w.Send(sim.AdvanceNPCRoute("lamp")); err != nil {
			t.Fatalf("advance: %v", err)
		}
		route := activeRouteOf(t, w, "lamp")
		if route == nil {
			t.Fatal("route gone")
		}
		if !route.Visited[2] {
			t.Error("stop 2 not credited — the visited neighbour answered for it, which is " +
				"exactly the shadowing failure this ordering exists to prevent")
		}
	})

	t.Run("an arrival where every match is already walked records nothing", func(t *testing.T) {
		w, cancel := beatAtStopZero(t, func(route *sim.NPCRoute, a *sim.Actor) {
			route.Visited[2] = true
			a.Pos = visitorSlotBeside(route.Stops[2].WalkTo)
		})
		defer cancel()
		res, err := w.Send(sim.AdvanceNPCRoute("lamp"))
		if err != nil {
			t.Fatalf("advance: %v", err)
		}
		if reason := res.(sim.AdvanceNPCRouteResult).Reason; reason != "beat_elsewhere" {
			t.Errorf("Reason = %q, want beat_elsewhere — every match is already on the books", reason)
		}
		route := activeRouteOf(t, w, "lamp")
		if route == nil || route.StopIdx != 0 {
			t.Fatalf("cursor moved on a re-visit: %+v", route)
		}
		if route.Visited[0] || route.Visited[1] {
			t.Error("a second call at a place he had already walked credited some OTHER stop")
		}
	})
}

// TestRoundCompletes_WhenEveryStopIsWalkedOutOfOrder pins the property the whole
// visited set exists to give: a round ENDS. Jeff's "never finishes" was the cursor
// unable to move, but a half-fixed version — crediting stops without letting the
// cursor reach the ones he skipped — finishes no better, it just relocates the stall
// to whichever place he happened to walk past.
// re-walk/abandon machinery rather than being waved through as "close enough".
func TestRouteStopReached_DecorativeCarrierStaysStrict(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrLamplighter, homeDest, sampleLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Bumped one tile off the stop — inside the constable's tolerance, outside hers.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route := world.ActiveRoutes["lamp"]
		a := world.Actors["lamp"]
		a.Pos = visitorSlotBeside(route.Stops[route.StopIdx].WalkTo)
		a.MoveIntent = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("bump: %v", err)
	}
	res, err := w.Send(sim.AdvanceNPCRoute("lamp"))
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if reason := res.(sim.AdvanceNPCRouteResult).Reason; reason != "stale_retry" {
		t.Errorf("Reason = %q, want stale_retry — a decorative carrier one tile off its "+
			"stop was bumped there, and the route must walk her back", reason)
	}
}
