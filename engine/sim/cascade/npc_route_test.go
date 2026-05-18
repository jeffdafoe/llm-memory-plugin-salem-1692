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
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() { cancel(); <-done }
}

// setActorAttribute sets a single attribute on the runner actor under
// the world goroutine — Attributes is on Actor, so we mutate it
// inside a Command.Fn.
func setActorAttribute(t *testing.T, w *sim.World, slug string) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		actor := world.Actors["runner"]
		if actor.Attributes == nil {
			actor.Attributes = map[string][]byte{}
		}
		actor.Attributes[slug] = []byte{}
		return nil, nil
	}}); err != nil {
		t.Fatalf("setActorAttribute: %v", err)
	}
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
	w, cancel := buildRouteCascadeWorld(t)
	defer cancel()

	RegisterNPCRoutes(context.Background(), w)
	setActorAttribute(t, w, sim.AttrLamplighter)

	// Lamps start at "lit" (the night state). Force them to "unlit"
	// so the upcoming night transition produces actual route stops
	// (target=lit on lamps already at lit yields zero stops).
	if _, err := w.Send(sim.SetVillageObjectState("lamp-A", "unlit", 0)); err != nil {
		t.Fatalf("force lamp-A unlit: %v", err)
	}
	if _, err := w.Send(sim.SetVillageObjectState("lamp-B", "unlit", 0)); err != nil {
		t.Fatalf("force lamp-B unlit: %v", err)
	}

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
func TestLamplighterNoActor(t *testing.T) {
	w, cancel := buildRouteCascadeWorld(t)
	defer cancel()

	RegisterNPCRoutes(context.Background(), w)
	// Deliberately skip setActorAttribute.

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
	w, cancel := buildRouteCascadeWorld(t)
	defer cancel()

	RegisterNPCRoutes(context.Background(), w)
	setActorAttribute(t, w, sim.AttrWasherwoman)

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
	w, cancel := buildRouteCascadeWorld(t)
	defer cancel()

	RegisterNPCRoutes(context.Background(), w)
	setActorAttribute(t, w, sim.AttrWasherwoman)

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
	w, cancel := buildRouteCascadeWorld(t)
	defer cancel()

	RegisterNPCRoutes(context.Background(), w)
	setActorAttribute(t, w, sim.AttrTownCrier)

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
// advances").
func TestArrivalAdvancesRoute(t *testing.T) {
	w, cancel := buildRouteCascadeWorld(t)
	defer cancel()

	RegisterNPCRoutes(context.Background(), w)
	setActorAttribute(t, w, sim.AttrLamplighter)

	// Get a route installed (re-uses the lamplighter dispatch path).
	if _, err := w.Send(sim.ApplyPhaseTransition(sim.PhaseDay)); err != nil {
		t.Fatalf("transition: %v", err)
	}
	// Lamps are at "lit" (night state) and we transitioned to day, so
	// lamplighter route fires with target=unlit. Verify the route
	// installed.
	if !hasActiveRoute(t, w) {
		t.Fatal("expected route after PhaseApplied")
	}

	// Drive enough AdvanceNPCRoute calls (one per stop, plus the
	// transition-to-returning, plus the arrived-home) to clear the
	// route. The cascade subscribes to ActorArrived, so dispatching
	// AdvanceNPCRoute directly under the world goroutine simulates
	// the locomotion-ticker arrival sequence.
	for i := 0; i < 10; i++ {
		if !hasActiveRoute(t, w) {
			return // route cleared — happy path
		}
		if _, err := w.Send(sim.AdvanceNPCRoute("runner")); err != nil {
			t.Fatalf("advance %d: %v", i, err)
		}
	}
	t.Fatal("route did not clear after 10 advances")
}

// newDeterministicRand returns a *rand.Rand seeded predictably so test
// runs are deterministic.
func newDeterministicRand() *rand.Rand {
	return rand.New(rand.NewSource(42))
}
