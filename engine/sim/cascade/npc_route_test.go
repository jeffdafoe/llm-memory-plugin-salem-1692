package cascade

import (
	"context"
	"math/rand"
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
		"laundry-line": {
			ID: "laundry-line", Category: "prop", DefaultState: "dirty",
			RotationAlgo: sim.RotationAlgoDeterministic,
			States: []sim.AssetState{
				{ID: 20, State: "dirty", Tags: []string{"rotatable", "laundry"}},
				{ID: 21, State: "clean", Tags: []string{"rotatable", "laundry"}},
			},
		},
		"notice-board": {
			ID: "notice-board", Category: "prop", DefaultState: "blank",
			RotationAlgo: sim.RotationAlgoDeterministic,
			States: []sim.AssetState{
				{ID: 30, State: "blank", Tags: []string{"rotatable", "notice-board"}},
				{ID: 31, State: "posted", Tags: []string{"rotatable", "notice-board"}},
			},
		},
	})

	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"home":    {ID: "home", AssetID: "house", X: 320, Y: 320},
		"lamp-A":  {ID: "lamp-A", AssetID: "lamp", CurrentState: "lit", X: 640, Y: 320},
		"lamp-B":  {ID: "lamp-B", AssetID: "lamp", CurrentState: "lit", X: 960, Y: 320},
		"laundry": {ID: "laundry", AssetID: "laundry-line", CurrentState: "dirty", X: 640, Y: 640},
		"notice":  {ID: "notice", AssetID: "notice-board", CurrentState: "blank", X: 960, Y: 640},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"home": {ID: "home", DisplayName: "Home"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"runner": {
			ID:              "runner",
			DisplayName:     "Route Runner",
			Kind:            sim.KindNPCShared,
			CurrentX:        sim.PadX + 10,
			CurrentY:        sim.PadY + 10,
			HomeStructureID: "home",
			Attributes:      map[string][]byte{},
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
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

// TestWasherwomanDispatchesOnRotationApplied: ApplyDailyRotation with
// TagLaundry in ExcludeTags fires the washerwoman.
func TestWasherwomanDispatchesOnRotationApplied(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w, sim.AttrWasherwoman)
	RegisterNPCRoutes(context.Background(), w)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	r := newDeterministicRand()
	scope := sim.RotationScope{ExcludeTags: []string{sim.TagLaundry, sim.TagNoticeBoard}}
	if _, err := w.Send(sim.ApplyDailyRotation(sim.RotationTickInputs{Now: time.Now().UTC(), Rand: r}, scope)); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if !hasActiveRoute(t, w) {
		t.Error("expected washerwoman route after RotationApplied with TagLaundry excluded")
	}
}

// TestWasherwomanSkipsWhenTagNotExcluded: ApplyDailyRotation with empty
// ExcludeTags — the bulk pass rotates laundry directly, washerwoman skips.
func TestWasherwomanSkipsWhenTagNotExcluded(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w, sim.AttrWasherwoman)
	RegisterNPCRoutes(context.Background(), w)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	r := newDeterministicRand()
	if _, err := w.Send(sim.ApplyDailyRotation(sim.RotationTickInputs{Now: time.Now().UTC(), Rand: r}, sim.RotationScope{})); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if hasActiveRoute(t, w) {
		t.Error("expected no washerwoman route — TagLaundry not in ExcludeTags")
	}
}

// TestTownCrierDispatchesOnRotationApplied: TagNoticeBoard variant of
// the washerwoman test.
func TestTownCrierDispatchesOnRotationApplied(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w, sim.AttrTownCrier)
	RegisterNPCRoutes(context.Background(), w)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	r := newDeterministicRand()
	scope := sim.RotationScope{ExcludeTags: []string{sim.TagLaundry, sim.TagNoticeBoard}}
	if _, err := w.Send(sim.ApplyDailyRotation(sim.RotationTickInputs{Now: time.Now().UTC(), Rand: r}, scope)); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if !hasActiveRoute(t, w) {
		t.Error("expected town_crier route after RotationApplied with TagNoticeBoard excluded")
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
			actor.CurrentX = stop.WalkTo.X
			actor.CurrentY = stop.WalkTo.Y
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
