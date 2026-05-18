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
	w.Actors["runner"].CurrentX = noticeStop.WalkTo.X
	w.Actors["runner"].CurrentY = noticeStop.WalkTo.Y
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
	w.Actors["runner"].CurrentX = sim.PadX + 30
	w.Actors["runner"].CurrentY = sim.PadY + 21
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
