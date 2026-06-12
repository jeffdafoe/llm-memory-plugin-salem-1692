package cascade

import (
	"context"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildRouteCascadeWorld stands up a world with terrain + lamp asset +
// rotatable laundry/noticeboard assets + a lamplighter/washerwoman/
// town_crier route NPC. Caller can selectively reset
// Actor.Attributes before triggering the cascade.
func buildRouteCascadeWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(allGrassTerrain())

	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"house": {
			ID: "house", Category: "structure", DefaultState: "default",
			DoorOffsetX: intp(0), DoorOffsetY: intp(2),
			States: []sim.AssetState{{ID: 1, State: "default"}},
		},
		"lamp": {
			ID: "lamp", Category: "prop", DefaultState: "unlit",
			States: []sim.AssetState{
				{ID: 10, State: "unlit", Tags: []string{"day-active", "lamplighter-target"}},
				{ID: 11, State: "lit", Tags: []string{"night-active", "lamplighter-target"}},
			},
		},
		// Both route-domain assets deliberately mirror production:
		// random_per_object RotationAlgo (the pre-446 builder's
		// deterministic-only gate silently skipped ALL production
		// laundry/notice-board objects — regression coverage), and the
		// laundry shape is DefaultState=empty + hung variants.
		"laundry-line": {
			ID: "laundry-line", Category: "prop", DefaultState: "empty",
			RotationAlgo: sim.RotationAlgoRandomPerObject,
			States: []sim.AssetState{
				{ID: 20, State: "empty", Tags: []string{"rotatable", "laundry"}},
				{ID: 21, State: "hung-1", Tags: []string{"rotatable", "laundry"}},
				{ID: 22, State: "hung-2", Tags: []string{"rotatable", "laundry"}},
			},
		},
		"notice-board": {
			ID: "notice-board", Category: "prop", DefaultState: "blank",
			RotationAlgo: sim.RotationAlgoRandomPerObject,
			States: []sim.AssetState{
				{ID: 30, State: "blank", Tags: []string{"rotatable", "notice-board"}},
				{ID: 31, State: "posted", Tags: []string{"rotatable", "notice-board"}},
			},
		},
	})

	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"home":    {ID: "home", AssetID: "house", Pos: sim.WorldPos{X: 320, Y: 320}},
		"lamp-A":  {ID: "lamp-A", AssetID: "lamp", CurrentState: "lit", Pos: sim.WorldPos{X: 640, Y: 320}},
		"lamp-B":  {ID: "lamp-B", AssetID: "lamp", CurrentState: "lit", Pos: sim.WorldPos{X: 960, Y: 320}},
		"laundry": {ID: "laundry", AssetID: "laundry-line", CurrentState: "empty", Pos: sim.WorldPos{X: 640, Y: 640}},
		"notice":  {ID: "notice", AssetID: "notice-board", CurrentState: "blank", Pos: sim.WorldPos{X: 960, Y: 640}},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"home": {ID: "home", DisplayName: "Home"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"runner": {
			ID:              "runner",
			DisplayName:     "Route Runner",
			Kind:            sim.KindNPCShared,
			Pos:             sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 10},
			HomeStructureID: "home",
			Attributes:      map[string][]byte{},
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	// Pin the world timezone so the schedule-boundary tests' fixed UTC
	// instants resolve deterministically regardless of the host's local
	// zone (same convention as sim's socialTestWorld).
	w.Settings.Location = time.UTC
	return w, func() {}
}

// runRouteCascadeWorld starts the world goroutine. Call after any
// pre-Run registrations or seeding the test wants to do directly
// against w.subscribers / w.Actors — once Run is going, those
// mutations must happen via w.Send(Command{Fn: ...}).
func runRouteCascadeWorld(t *testing.T, w *sim.World) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return func() { cancel(); <-done }
}

// seedActorAttribute mutates the runner actor's Attributes map
// directly. Must be called BEFORE runRouteCascadeWorld starts the
// world goroutine — direct mutation of the world map is unsafe once
// Run is dispatching commands.
func seedActorAttribute(w *sim.World, slug string) {
	actor := w.Actors["runner"]
	if actor.Attributes == nil {
		actor.Attributes = map[string][]byte{}
	}
	actor.Attributes[slug] = []byte{}
}

// hasActiveRoute reads ActiveRoutes for the runner inside a Command.
func hasActiveRoute(t *testing.T, w *sim.World) bool {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		_, ok := world.ActiveRoutes["runner"]
		return ok, nil
	}})
	if err != nil {
		t.Fatalf("hasActiveRoute: %v", err)
	}
	return res.(bool)
}

// allGrassTerrain builds a TerrainLightGrass-filled map of the standard
// MapW * MapH dimensions.
func allGrassTerrain() *sim.Terrain {
	data := make([]byte, sim.MapW*sim.MapH)
	for i := range data {
		data[i] = sim.TerrainLightGrass
	}
	return &sim.Terrain{Data: data}
}

func intp(i int) *int { return &i }

// TestRegisterNPCRoutes_NilWorldPanics is a wiring guard regression.
func TestRegisterNPCRoutes_NilWorldPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("RegisterNPCRoutes(nil) did not panic")
		}
	}()
	RegisterNPCRoutes(context.Background(), nil)
}

// TestLamplighterDispatchesOnPhaseApplied: the runner carries
// AttrLamplighter; ApplyPhaseTransition emits PhaseApplied; the
// subscriber dispatches StartNPCRoute and installs an active route.
func TestLamplighterDispatchesOnPhaseApplied(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w, sim.AttrLamplighter)
	// Lamps start at "lit" (the night state). Pre-flip them to
	// "unlit" so the upcoming night transition produces actual route
	// stops (target=lit on lamps already at lit yields zero stops).
	w.VillageObjects["lamp-A"].CurrentState = "unlit"
	w.VillageObjects["lamp-B"].CurrentState = "unlit"

	RegisterNPCRoutes(context.Background(), w)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	// Night transition: lamp-A/B carved out of the bulk pass; the
	// lamplighter cascade subscriber dispatches a route to flip
	// them back to "lit".
	if _, err := w.Send(sim.ApplyPhaseTransition(sim.PhaseNight)); err != nil {
		t.Fatalf("night transition: %v", err)
	}
	if !hasActiveRoute(t, w) {
		t.Error("expected active route after PhaseApplied")
	}
}

// TestLamplighterNoActor: no actor carries AttrLamplighter — no route.
// Also verifies the conditional carve-out: lamp-A flips in the bulk
// pass (no actor to carve out for).
func TestLamplighterNoActor(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	// Deliberately skip seedActorAttribute.
	w.VillageObjects["lamp-A"].CurrentState = "unlit"
	w.VillageObjects["lamp-B"].CurrentState = "unlit"

	RegisterNPCRoutes(context.Background(), w)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	if _, err := w.Send(sim.ApplyPhaseTransition(sim.PhaseNight)); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if hasActiveRoute(t, w) {
		t.Error("expected no route — no actor carries AttrLamplighter")
	}
}

// TestRouteAbandonedOnMoveStopped: any ActorMoveStopped for an actor holding an
// active route abandons that route — cleared from ActiveRoutes — so it can't sit
// parked forever and keep the actor shift-duty-exempt. The synthesized event is
// NOT tied to the route's own walk, so this also exercises the deliberate
// "a competing move superseded the route walk and then stopped" case (abandon is
// the only signal that reaches us; ignoring it would re-strand the route). A
// no-route actor is a no-op.
func TestRouteAbandonedOnMoveStopped(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w, sim.AttrLamplighter)

	// Install a synthetic active route for the runner (direct mutation is safe
	// before Run starts).
	if w.ActiveRoutes == nil {
		w.ActiveRoutes = map[sim.ActorID]*sim.NPCRoute{}
	}
	w.ActiveRoutes["runner"] = &sim.NPCRoute{
		NPCID:           "runner",
		Label:           sim.AttrLamplighter,
		Stops:           []sim.RouteStop{{ObjectID: "lamp-A", WalkTo: sim.Position{X: sim.PadX + 20, Y: sim.PadY + 10}, NewState: "lit"}},
		Phase:           sim.RoutePhaseActive,
		HomeDestination: sim.NewPositionDestination(sim.Position{X: sim.PadX + 10, Y: sim.PadY + 10}),
	}

	RegisterNPCRoutes(context.Background(), w)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	// The route's walk fails to reach the stop — unreachable.
	stoppedEvt := &sim.ActorMoveStopped{ActorID: "runner", Reason: sim.MoveStoppedUnreachable, At: time.Now()}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handleActorMoveStoppedAdvanceRoute(world, stoppedEvt)
		return nil, nil
	}}); err != nil {
		t.Fatalf("invoke handler: %v", err)
	}
	if hasActiveRoute(t, w) {
		t.Error("route not abandoned after ActorMoveStopped")
	}

	// No-op for an actor with no route (must not panic).
	ghostEvt := &sim.ActorMoveStopped{ActorID: "ghost", Reason: sim.MoveStoppedBlocked, At: time.Now()}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handleActorMoveStoppedAdvanceRoute(world, ghostEvt)
		return nil, nil
	}}); err != nil {
		t.Fatalf("invoke handler (ghost): %v", err)
	}
}

// seedRunnerSchedule sets the runner's schedule window (minute-of-day).
// Must be called BEFORE runRouteCascadeWorld, same as seedActorAttribute.
func seedRunnerSchedule(w *sim.World, startMin, endMin int) {
	actor := w.Actors["runner"]
	actor.ScheduleStartMin = &startMin
	actor.ScheduleEndMin = &endMin
}

// routeStopStates returns the NewState of every stop on the runner's
// active route (nil when no route).
func routeStopStates(t *testing.T, w *sim.World) []string {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route, ok := world.ActiveRoutes["runner"]
		if !ok {
			return []string(nil), nil
		}
		var states []string
		for _, s := range route.Stops {
			states = append(states, s.NewState)
		}
		return states, nil
	}})
	if err != nil {
		t.Fatalf("routeStopStates: %v", err)
	}
	return res.([]string)
}

// clearRunnerRoute deletes the runner's active route (so a follow-up
// tick's "no new route" assertion isn't masked by the old one).
func clearRunnerRoute(t *testing.T, w *sim.World) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.ActiveRoutes, "runner")
		return nil, nil
	}}); err != nil {
		t.Fatalf("clearRunnerRoute: %v", err)
	}
}

// at builds a UTC instant on a fixed test day at h:m. The test world has
// no Settings.Location, so window boundaries resolve in UTC too.
func at(h, m int) time.Time {
	return time.Date(2026, 6, 12, h, m, 0, 0, time.UTC)
}

// TestWasherwomanHangsOutAtWindowStart: a schedule tick inside the
// window (start boundary unprocessed) starts a route whose stop flips
// the empty laundry line to a hung variant. The fixture asset is
// random_per_object, regression-proving the builder no longer gates on
// a deterministic RotationAlgo (the pre-446 bug that built zero stops
// against every production laundry/board asset).
func TestWasherwomanHangsOutAtWindowStart(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w, sim.AttrWasherwoman)
	seedRunnerSchedule(w, 540, 1080) // 9:00–18:00
	RegisterNPCRoutes(context.Background(), w)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	if _, err := w.Send(RouteScheduleTick(at(10, 0), newDeterministicRand())); err != nil {
		t.Fatalf("tick: %v", err)
	}
	states := routeStopStates(t, w)
	if len(states) != 1 {
		t.Fatalf("route stops = %v, want exactly the laundry object", states)
	}
	if states[0] != "hung-1" && states[0] != "hung-2" {
		t.Errorf("hang-out NewState = %q, want a hung variant", states[0])
	}
}

// TestWasherwomanBringsInAtWindowEnd: past the window end, the route's
// stop flips a hung line back to the asset's DefaultState.
func TestWasherwomanBringsInAtWindowEnd(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w, sim.AttrWasherwoman)
	seedRunnerSchedule(w, 540, 1080)
	w.VillageObjects["laundry"].CurrentState = "hung-2"
	RegisterNPCRoutes(context.Background(), w)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	if _, err := w.Send(RouteScheduleTick(at(19, 0), newDeterministicRand())); err != nil {
		t.Fatalf("tick: %v", err)
	}
	states := routeStopStates(t, w)
	if len(states) != 1 || states[0] != "empty" {
		t.Errorf("bring-in stops = %v, want [empty]", states)
	}
}

// TestWasherwomanBoundaryFiresOnce: a processed boundary is stamped and
// doesn't re-fire — the second tick at the same instant starts no new
// route. Also covers the idempotent no-op stamp: laundry already hung at
// the start boundary builds zero candidates, yet the boundary still
// stamps (no per-tick re-evaluation for the rest of the window).
func TestWasherwomanBoundaryFiresOnce(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w, sim.AttrWasherwoman)
	seedRunnerSchedule(w, 540, 1080)
	RegisterNPCRoutes(context.Background(), w)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	rng := newDeterministicRand()
	if _, err := w.Send(RouteScheduleTick(at(10, 0), rng)); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if !hasActiveRoute(t, w) {
		t.Fatal("expected route after first start-boundary tick")
	}
	clearRunnerRoute(t, w)
	if _, err := w.Send(RouteScheduleTick(at(10, 5), rng)); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if hasActiveRoute(t, w) {
		t.Error("start boundary re-fired — expected the stamp to suppress it")
	}

	// Idempotent no-op at a boundary: hung laundry + a fresh world whose
	// start boundary is due → zero candidates, no route, but stamped.
	w2, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w2, sim.AttrWasherwoman)
	seedRunnerSchedule(w2, 540, 1080)
	w2.VillageObjects["laundry"].CurrentState = "hung-1"
	RegisterNPCRoutes(context.Background(), w2)
	cancel2 := runRouteCascadeWorld(t, w2)
	defer cancel2()

	if _, err := w2.Send(RouteScheduleTick(at(10, 0), rng)); err != nil {
		t.Fatalf("w2 tick: %v", err)
	}
	if hasActiveRoute(t, w2) {
		t.Error("expected no route — laundry already hung (zero candidates)")
	}
	res, err := w2.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		_, stamped := world.RouteBoundaryStamps[sim.AttrWasherwoman]
		return stamped, nil
	}})
	if err != nil {
		t.Fatalf("read stamp: %v", err)
	}
	if !res.(bool) {
		t.Error("zero-candidate boundary was not stamped — would re-evaluate every tick")
	}
}

// TestTownCrierToursAtBothBoundaries: the crier route fires at the
// window start AND again at the window end (the end boundary is a later
// instant than the stamped start, so it passes the edge guard), flipping
// the board each time.
func TestTownCrierToursAtBothBoundaries(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w, sim.AttrTownCrier)
	seedRunnerSchedule(w, 540, 1080)
	RegisterNPCRoutes(context.Background(), w)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	rng := newDeterministicRand()
	if _, err := w.Send(RouteScheduleTick(at(10, 0), rng)); err != nil {
		t.Fatalf("start tick: %v", err)
	}
	states := routeStopStates(t, w)
	if len(states) != 1 || states[0] != "posted" {
		t.Fatalf("start-boundary stops = %v, want [posted] (blank board advances)", states)
	}
	clearRunnerRoute(t, w)

	// End boundary: a fresh route even though the start stamp is set.
	if _, err := w.Send(RouteScheduleTick(at(19, 0), rng)); err != nil {
		t.Fatalf("end tick: %v", err)
	}
	if !hasActiveRoute(t, w) {
		t.Error("expected crier route at the window-end boundary")
	}
}

// TestRouteScheduleWrapMidnightWindow: the stamp comparison stays sound
// across a wrap-midnight window (22:00–02:00). The start boundary
// resolves to YESTERDAY's 22:00 for an early-morning tick; the stamp
// must suppress its re-fire yet still let today's 02:00 end boundary
// through (a later instant than the stamped start).
func TestRouteScheduleWrapMidnightWindow(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w, sim.AttrWasherwoman)
	seedRunnerSchedule(w, 1320, 120) // 22:00–02:00
	RegisterNPCRoutes(context.Background(), w)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	rng := newDeterministicRand()
	// 00:30 — inside the window; most recent boundary is yesterday's
	// 22:00 start → hang-out route.
	if _, err := w.Send(RouteScheduleTick(at(0, 30), rng)); err != nil {
		t.Fatalf("tick 00:30: %v", err)
	}
	states := routeStopStates(t, w)
	if len(states) != 1 || states[0] == "empty" {
		t.Fatalf("00:30 stops = %v, want one hung variant (yesterday's start boundary)", states)
	}
	clearRunnerRoute(t, w)

	// 00:35 — same boundary, stamped → no re-fire.
	if _, err := w.Send(RouteScheduleTick(at(0, 35), rng)); err != nil {
		t.Fatalf("tick 00:35: %v", err)
	}
	if hasActiveRoute(t, w) {
		t.Error("start boundary re-fired at 00:35 despite the stamp")
	}

	// The 00:30 hang-out stamped, but its route was cleared before any
	// stop flipped — hang the laundry manually so the end boundary has
	// something to bring in.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.VillageObjects["laundry"].CurrentState = "hung-1"
		return nil, nil
	}}); err != nil {
		t.Fatalf("hang laundry: %v", err)
	}

	// 02:30 — past the window end; today's 02:00 end boundary is later
	// than the stamped start → bring-in route fires.
	if _, err := w.Send(RouteScheduleTick(at(2, 30), rng)); err != nil {
		t.Fatalf("tick 02:30: %v", err)
	}
	states = routeStopStates(t, w)
	if len(states) != 1 || states[0] != "empty" {
		t.Errorf("02:30 stops = %v, want [empty] (end boundary past the start stamp)", states)
	}
}

// TestRouteScheduleNoCarrier: no actor carries the attribute — no route,
// no stamp (the next tick re-checks; a carrier added at runtime starts
// getting boundaries without a restart).
func TestRouteScheduleNoCarrier(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	// Deliberately no seedActorAttribute.
	seedRunnerSchedule(w, 540, 1080)
	RegisterNPCRoutes(context.Background(), w)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	if _, err := w.Send(RouteScheduleTick(at(10, 0), newDeterministicRand())); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if hasActiveRoute(t, w) {
		t.Error("expected no route — no attribute carrier")
	}
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return len(world.RouteBoundaryStamps), nil
	}})
	if err != nil {
		t.Fatalf("read stamps: %v", err)
	}
	if res.(int) != 0 {
		t.Error("stamped a boundary with no carrier present")
	}
}

// TestTownCrierReadsBoardContentBeforeFlip: the npc_route subscriber's
// town_crier branch reads NoticeboardContent for the just-arrived
// stop's object BEFORE dispatching AdvanceNPCRoute (which flips the
// state). Verifies a Spoke event was emitted carrying the board's
// content text. White-box test — invokes
// handleActorArrivedAdvanceRoute directly with a synthesized
// ActorArrived event under the world goroutine.
func TestTownCrierReadsBoardContentBeforeFlip(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w, sim.AttrTownCrier)

	// Install a synthetic town_crier route with one stop on the
	// "notice" board (which is the notice-board-tagged rotatable
	// object in the cascade test fixture). Stop's WalkTo is the
	// notice board's adjacent walkable tile; the actor will be
	// teleported there before the synthesized arrival fires.
	noticeStop := sim.RouteStop{
		ObjectID: "notice",
		WalkTo:   sim.Position{X: sim.PadX + 30, Y: sim.PadY + 21},
		NewState: "posted",
	}
	if w.ActiveRoutes == nil {
		w.ActiveRoutes = map[sim.ActorID]*sim.NPCRoute{}
	}
	w.ActiveRoutes["runner"] = &sim.NPCRoute{
		NPCID:           "runner",
		Label:           sim.AttrTownCrier,
		Stops:           []sim.RouteStop{noticeStop},
		StopIdx:         0,
		Phase:           sim.RoutePhaseActive,
		HomeDestination: sim.NewPositionDestination(sim.Position{X: sim.PadX + 10, Y: sim.PadY + 10}),
	}
	// Position the actor at the stop's WalkTo so the active-phase
	// stale-arrival guard accepts the arrival.
	w.Actors["runner"].Pos.X = noticeStop.WalkTo.X
	w.Actors["runner"].Pos.Y = noticeStop.WalkTo.Y
	// Pre-stamp the board with content the crier should read.
	w.NoticeboardContent = map[sim.VillageObjectID]*sim.NoticeboardContent{
		"notice": {Text: "A travelling cobbler lodges at the Ordinary.", PostedAt: time.Now(), AtState: "blank"},
	}

	// Subscribe a spoke recorder BEFORE Run starts.
	spokeRec := &cascadeSpokeRecorder{}
	w.Subscribe(sim.SubscriberFunc(spokeRec.handle))

	RegisterNPCRoutes(context.Background(), w)

	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	// Synthesize the ActorArrived event and dispatch the subscriber
	// directly. (Going through the locomotion ticker would require
	// driving the actor tile-by-tile, which is out of scope for this
	// subscriber test.)
	arrivedEvt := &sim.ActorArrived{
		ActorID:          "runner",
		FinalPosition:    noticeStop.WalkTo,
		FinalStructureID: "",
		At:               time.Now(),
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handleActorArrivedAdvanceRoute(world, arrivedEvt)
		return nil, nil
	}}); err != nil {
		t.Fatalf("invoke handler: %v", err)
	}

	got := spokeRec.collect()
	if len(got) == 0 {
		t.Fatal("no Spoke event emitted after town_crier arrival")
	}
	last := got[len(got)-1]
	if last.SpeakerID != "runner" {
		t.Errorf("Spoke.SpeakerID = %q, want runner", last.SpeakerID)
	}
	if last.Text != "A travelling cobbler lodges at the Ordinary." {
		t.Errorf("Spoke.Text = %q, want board content", last.Text)
	}
}

// TestTownCrierSilentOnStaleAtState: town_crier arrives at a stop
// whose NoticeboardContent.AtState DOES NOT match the board's
// current CurrentState — content is stale (from a previous rotation
// cycle). The read-path AtState guard rejects this; no Spoke event
// emitted. Mirrors the "out-of-band state mutation" case (admin
// direct flip, future code paths that change state without
// clearing content).
func TestTownCrierSilentOnStaleAtState(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w, sim.AttrTownCrier)

	// Mutate the noticeboard's CurrentState to "posted" so it no
	// longer matches the AtState we stamp on the content (which
	// will say "blank" — a state authored two cycles ago).
	w.VillageObjects["notice"].CurrentState = "posted"

	if w.ActiveRoutes == nil {
		w.ActiveRoutes = map[sim.ActorID]*sim.NPCRoute{}
	}
	w.ActiveRoutes["runner"] = &sim.NPCRoute{
		NPCID: "runner",
		Label: sim.AttrTownCrier,
		Stops: []sim.RouteStop{
			{ObjectID: "notice", WalkTo: sim.Position{X: sim.PadX + 30, Y: sim.PadY + 21}, NewState: "blank"},
		},
		Phase:           sim.RoutePhaseActive,
		HomeDestination: sim.NewPositionDestination(sim.Position{X: sim.PadX + 10, Y: sim.PadY + 10}),
	}
	w.Actors["runner"].Pos.X = sim.PadX + 30
	w.Actors["runner"].Pos.Y = sim.PadY + 21
	// Stale content: AtState=blank but the board's CurrentState is now "posted".
	w.NoticeboardContent = map[sim.VillageObjectID]*sim.NoticeboardContent{
		"notice": {Text: "Yesterday's news.", PostedAt: time.Now(), AtState: "blank"},
	}

	spokeRec := &cascadeSpokeRecorder{}
	w.Subscribe(sim.SubscriberFunc(spokeRec.handle))

	RegisterNPCRoutes(context.Background(), w)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	arrivedEvt := &sim.ActorArrived{
		ActorID:       "runner",
		FinalPosition: sim.Position{X: sim.PadX + 30, Y: sim.PadY + 21},
		At:            time.Now(),
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handleActorArrivedAdvanceRoute(world, arrivedEvt)
		return nil, nil
	}}); err != nil {
		t.Fatalf("invoke handler: %v", err)
	}

	if got := spokeRec.collect(); len(got) != 0 {
		t.Errorf("emitted %d Spoke events on stale AtState, want 0", len(got))
	}
}

// TestTownCrierSilentWhenBoardEmpty: town_crier arrival at a stop
// with NO NoticeboardContent stored does NOT emit a Spoke (cold-start
// / first-cycle silent path).
func TestTownCrierSilentWhenBoardEmpty(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w, sim.AttrTownCrier)

	if w.ActiveRoutes == nil {
		w.ActiveRoutes = map[sim.ActorID]*sim.NPCRoute{}
	}
	w.ActiveRoutes["runner"] = &sim.NPCRoute{
		NPCID: "runner",
		Label: sim.AttrTownCrier,
		Stops: []sim.RouteStop{
			{ObjectID: "notice", WalkTo: sim.Position{X: sim.PadX + 30, Y: sim.PadY + 21}, NewState: "posted"},
		},
		Phase:           sim.RoutePhaseActive,
		HomeDestination: sim.NewPositionDestination(sim.Position{X: sim.PadX + 10, Y: sim.PadY + 10}),
	}
	w.Actors["runner"].Pos.X = sim.PadX + 30
	w.Actors["runner"].Pos.Y = sim.PadY + 21
	// NO NoticeboardContent stamped.

	spokeRec := &cascadeSpokeRecorder{}
	w.Subscribe(sim.SubscriberFunc(spokeRec.handle))

	RegisterNPCRoutes(context.Background(), w)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	arrivedEvt := &sim.ActorArrived{
		ActorID:       "runner",
		FinalPosition: sim.Position{X: sim.PadX + 30, Y: sim.PadY + 21},
		At:            time.Now(),
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handleActorArrivedAdvanceRoute(world, arrivedEvt)
		return nil, nil
	}}); err != nil {
		t.Fatalf("invoke handler: %v", err)
	}

	if got := spokeRec.collect(); len(got) != 0 {
		t.Errorf("emitted %d Spoke events on empty board, want 0", len(got))
	}
}

// TestArrivalAdvancesRoute: with a route installed, emitting
// ActorArrived for the route owner advances StopIdx (verified
// indirectly via "route exists" then "route gone after all
// advances"). Manually positions the actor at each stop's WalkTo
// before dispatching AdvanceNPCRoute, satisfying the active-phase
// stale-arrival guard.
func TestArrivalAdvancesRoute(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w, sim.AttrLamplighter)
	RegisterNPCRoutes(context.Background(), w)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	if _, err := w.Send(sim.ApplyPhaseTransition(sim.PhaseDay)); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if !hasActiveRoute(t, w) {
		t.Fatal("expected route after PhaseApplied")
	}

	// Drive enough Advance calls to clear the route. Before each call,
	// teleport the actor to the expected stop's WalkTo so the
	// active-phase stale-arrival guard accepts the advance.
	for i := 0; i < 10; i++ {
		if !hasActiveRoute(t, w) {
			return
		}
		if err := teleportActorToCurrentStop(w); err != nil {
			t.Fatalf("teleport %d: %v", i, err)
		}
		if _, err := w.Send(sim.AdvanceNPCRoute("runner")); err != nil {
			t.Fatalf("advance %d: %v", i, err)
		}
	}
	t.Fatal("route did not clear after 10 advances")
}

// teleportActorToCurrentStop sets the runner's tile to the active
// route's current stop's WalkTo (or to the home position for routes
// in returning phase). Used by TestArrivalAdvancesRoute to satisfy
// the active-phase stale-arrival guard without driving the real
// locomotion ticker.
func teleportActorToCurrentStop(w *sim.World) error {
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route, ok := world.ActiveRoutes["runner"]
		if !ok {
			return nil, nil
		}
		actor := world.Actors["runner"]
		if route.Phase == sim.RoutePhaseActive && route.StopIdx < len(route.Stops) {
			stop := route.Stops[route.StopIdx]
			actor.Pos.X = stop.WalkTo.X
			actor.Pos.Y = stop.WalkTo.Y
		}
		return nil, nil
	}})
	return err
}

// newDeterministicRand returns a *rand.Rand seeded predictably so test
// runs are deterministic.
func newDeterministicRand() *rand.Rand {
	return rand.New(rand.NewSource(42))
}

// cascadeSpokeRecorder accumulates *sim.Spoke events under a mutex so
// the test goroutine can read after the world goroutine emits.
type cascadeSpokeRecorder struct {
	mu     sync.Mutex
	events []sim.Spoke
}

func (r *cascadeSpokeRecorder) handle(_ *sim.World, evt sim.Event) {
	if e, ok := evt.(*sim.Spoke); ok {
		r.mu.Lock()
		r.events = append(r.events, *e)
		r.mu.Unlock()
	}
}

func (r *cascadeSpokeRecorder) collect() []sim.Spoke {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sim.Spoke(nil), r.events...)
}
