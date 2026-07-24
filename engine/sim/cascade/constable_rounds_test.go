package cascade

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// constable_rounds_test.go — LLM-514 cascade coverage: the business candidate
// builder, the forced rounds route (ForceRouteCommand), and the per-stop dwell /
// no-interrupt-on-conversation advance.

// TestBuildConstableRoundsCandidates: picks every TagBusiness object, sorted by id,
// excluding untagged / otherwise-tagged placements.
func TestBuildConstableRoundsCandidates(t *testing.T) {
	w := &sim.World{VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
		"tavern": {ID: "tavern", Tags: []string{sim.TagBusiness}, Pos: sim.WorldPos{X: 100, Y: 100}},
		"store":  {ID: "store", Tags: []string{sim.TagBusiness}, Pos: sim.WorldPos{X: 200, Y: 200}},
		"house":  {ID: "house", Pos: sim.WorldPos{X: 300, Y: 300}},                // not a business
		"well":   {ID: "well", Tags: []string{"well"}, Pos: sim.WorldPos{X: 400}}, // tagged, but not a business
	}}
	cands := buildConstableRoundsCandidates(w)
	if len(cands) != 2 {
		t.Fatalf("got %d candidates, want 2 (the two businesses)", len(cands))
	}
	if cands[0].ObjectID != "store" || cands[1].ObjectID != "tavern" {
		t.Errorf("candidates = [%s, %s], want [store, tavern] (sorted by id)", cands[0].ObjectID, cands[1].ObjectID)
	}
}

// buildConstableCascadeWorld stands up a walkable world with a Meeting House post
// and two door-backed businesses (store, tavern) tagged TagBusiness, plus a
// constable NPC carrying AttrConstable. Businesses are structure-backed with doors,
// so the rounds route ENTERS them (LLM-514).
func buildConstableCascadeWorld(t *testing.T) *sim.World {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(allGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"house": {
			ID: "house", Category: "structure", DefaultState: "default",
			DoorOffsetX: intp(0), DoorOffsetY: intp(2),
			States: []sim.AssetState{{ID: 1, State: "default"}},
		},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		// Anchor tiles (WorldPos.Tile = Pad + world/32): meeting_house {70,132},
		// store {80,122}, tavern {90,122}. All door-backed, so rounds enter them.
		"meeting_house": {ID: "meeting_house", AssetID: "house", Pos: sim.WorldPos{X: 320, Y: 640}},
		"store":         {ID: "store", AssetID: "house", Pos: sim.WorldPos{X: 640, Y: 320}, Tags: []string{sim.TagBusiness}},
		"tavern":        {ID: "tavern", AssetID: "house", Pos: sim.WorldPos{X: 960, Y: 320}, Tags: []string{sim.TagBusiness}},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"meeting_house": {ID: "meeting_house", DisplayName: "Meeting House"},
		"store":         {ID: "store", DisplayName: "General Store"},
		"tavern":        {ID: "tavern", DisplayName: "Tavern"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"gideon": {
			ID:                "gideon",
			DisplayName:       "Gideon Marsh",
			Kind:              sim.KindNPCStateful,
			Pos:               sim.TilePos{X: sim.PadX + 10, Y: sim.PadY + 10},
			WorkStructureID:   "meeting_house",
			InsideStructureID: "meeting_house",
			ScheduleStartMin:  intp(0),
			ScheduleEndMin:    intp(1440),
			Attributes:        map[string][]byte{sim.AttrConstable: {}},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	w.Settings.Location = time.UTC
	return w
}

// TestForceRouteConstable: forcing the constable route builds a route over the
// businesses, entering them, and returns to his POST (the Meeting House) rather
// than home. Unknown / uncarried attrs error.
func TestForceRouteConstable(t *testing.T) {
	w := buildConstableCascadeWorld(t)
	RegisterNPCRoutes(context.Background(), w, llm.NewFakeClient())
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	res, err := w.Send(ForceRouteCommand(sim.AttrConstable, false))
	if err != nil {
		t.Fatalf("force constable route: %v", err)
	}
	started, ok := res.(sim.StartNPCRouteResult)
	if !ok {
		t.Fatalf("result type %T, want StartNPCRouteResult", res)
	}
	if started.Stops == 0 {
		t.Fatal("forced constable route built 0 stops (businesses unreachable?)")
	}

	// Inspect the installed route: it returns to the Meeting House and every
	// business stop is an ENTER stop (door-backed).
	inspect, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.ActiveRoutes["gideon"], nil
	}})
	if err != nil {
		t.Fatalf("inspect route: %v", err)
	}
	route, ok := inspect.(*sim.NPCRoute)
	if !ok || route == nil {
		t.Fatal("no active route installed after force")
	}
	if route.HomeDestination.Kind != sim.MoveDestinationStructureEnter ||
		route.HomeDestination.StructureID == nil || *route.HomeDestination.StructureID != "meeting_house" {
		t.Errorf("return destination = %+v, want StructureEnter(meeting_house) — back to post, not home", route.HomeDestination)
	}
	for i, stop := range route.Stops {
		if stop.EnterStructureID == "" {
			t.Errorf("stop %d (%s) is a loiter stop, want an enter stop (door-backed business)", i, stop.ObjectID)
		}
	}
}

// TestForceRouteConstableErrors: unknown attr and uncarried attr both error.
func TestForceRouteConstableErrors(t *testing.T) {
	w := buildConstableCascadeWorld(t)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	if _, err := w.Send(ForceRouteCommand("bogus", false)); err == nil {
		t.Error("expected error for unknown route attr")
	}
	// Strip the attribute → no carrier.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.Actors["gideon"].Attributes, sim.AttrConstable)
		return nil, nil
	}}); err != nil {
		t.Fatalf("strip attribute: %v", err)
	}
	if _, err := w.Send(ForceRouteCommand(sim.AttrConstable, false)); err == nil {
		t.Error("expected error when no actor carries the constable attribute")
	}
}

// TestConstableDwellAndNoInterrupt: on arrival at a business the constable DWELLS
// (does not advance immediately); the deferred advance moves him on once the dwell
// elapses AND he is not mid-conversation; a huddle defers the advance.
func TestConstableDwellAndNoInterrupt(t *testing.T) {
	w := buildConstableCascadeWorld(t)
	RegisterNPCRoutes(context.Background(), w, llm.NewFakeClient())
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	// Start the rounds route, then place the constable inside the FIRST stop's
	// business so arrival detection (InsideStructureID) fires for an enter stop.
	if _, err := w.Send(ForceRouteCommand(sim.AttrConstable, false)); err != nil {
		t.Fatalf("force route: %v", err)
	}
	firstStop, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route := world.ActiveRoutes["gideon"]
		if route == nil || len(route.Stops) == 0 {
			return sim.StructureID(""), nil
		}
		sid := route.Stops[0].EnterStructureID
		// Put him inside the first business, as if he had walked in.
		a := world.Actors["gideon"]
		a.InsideStructureID = sid
		return sid, nil
	}})
	if err != nil {
		t.Fatalf("place at first stop: %v", err)
	}
	stopSID := firstStop.(sim.StructureID)
	if stopSID == "" {
		t.Fatal("no first stop to dwell at")
	}

	// Arrival at the stop DWELLS — Dwelling set, StopIdx unchanged (no immediate advance).
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		evt := &sim.ActorArrived{ActorID: "gideon", FinalStructureID: stopSID, At: time.Now()}
		handleActorArrivedAdvanceRoute(context.Background(), world, evt, llm.NewFakeClient())
		return nil, nil
	}}); err != nil {
		t.Fatalf("arrival: %v", err)
	}
	assertRoute(t, w, func(r *sim.NPCRoute) {
		if !r.Dwelling {
			t.Error("arrival should begin a dwell (Dwelling=true)")
		}
		if r.StopIdx != 0 {
			t.Errorf("StopIdx = %d, want 0 (must not advance on arrival — it dwells)", r.StopIdx)
		}
	})

	// While mid-conversation, the dwell advance DEFERS: StopIdx unchanged, still dwelling.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["gideon"]
		a.CurrentHuddleID = "h1"
		world.Huddles["h1"] = &sim.Huddle{ID: "h1", Members: map[sim.ActorID]struct{}{"gideon": {}, "keeper": {}}}
		if _, err := constableAdvanceAfterDwell("gideon", 0).Fn(world); err != nil {
			return nil, err
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("dwell advance (huddling): %v", err)
	}
	assertRoute(t, w, func(r *sim.NPCRoute) {
		if r.StopIdx != 0 {
			t.Errorf("StopIdx = %d, want 0 — must not yank him out of a conversation", r.StopIdx)
		}
		if !r.Dwelling {
			t.Error("still dwelling while the huddle stands")
		}
	})

	// Conversation ends → the dwell advance moves him on to the next stop.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["gideon"]
		a.CurrentHuddleID = ""
		delete(world.Huddles, "h1")
		if _, err := constableAdvanceAfterDwell("gideon", 0).Fn(world); err != nil {
			return nil, err
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("dwell advance (idle): %v", err)
	}
	assertRoute(t, w, func(r *sim.NPCRoute) {
		if r.StopIdx == 0 {
			t.Error("StopIdx still 0 — should have advanced once the conversation ended")
		}
		if r.Dwelling {
			t.Error("Dwelling should clear on advance")
		}
	})
}

// assertRoute reads the constable's active route on the world goroutine and runs
// check against it. Fatals when no route is installed.
func assertRoute(t *testing.T, w *sim.World, check func(*sim.NPCRoute)) {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.ActiveRoutes["gideon"], nil
	}})
	if err != nil {
		t.Fatalf("read route: %v", err)
	}
	route, ok := res.(*sim.NPCRoute)
	if !ok || route == nil {
		t.Fatal("no active route")
	}
	check(route)
}
