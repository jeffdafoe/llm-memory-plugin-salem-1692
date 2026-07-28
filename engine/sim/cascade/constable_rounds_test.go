package cascade

import (
	"context"
	"fmt"
	"math/rand"
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
// constable NPC (gideon) carrying AttrConstable. Businesses are structure-backed
// with doors, so the rounds route ENTERS them (LLM-514). Any extra ids passed are
// seeded as additional constables at the same post + all-day shift, for the
// multiple-carriers coverage.
func buildConstableCascadeWorld(t *testing.T, extraConstables ...sim.ActorID) *sim.World {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(allGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"house": {
			ID: "house", Category: "structure", DefaultState: "default",
			DoorOffsetX: intp(0), DoorOffsetY: intp(2),
			States: []sim.AssetState{{ID: 1, State: "default"}},
		},
		// "barn" is a DOORLESS structure asset (no door offsets) — a structure-backed
		// placement the constable can never step INTO, so it must resolve to a loiter
		// stop (test E).
		"barn": {
			ID: "barn", Category: "structure", DefaultState: "default",
			States: []sim.AssetState{{ID: 1, State: "default"}},
		},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		// Anchor tiles (WorldPos.Tile = Pad + world/32): meeting_house {70,132},
		// store {80,122}, tavern {90,122}. All door-backed, so rounds enter them.
		"meeting_house": {ID: "meeting_house", AssetID: "house", Pos: sim.WorldPos{X: 320, Y: 640}},
		"store":         {ID: "store", AssetID: "house", Pos: sim.WorldPos{X: 640, Y: 320}, Tags: []string{sim.TagBusiness}},
		"tavern":        {ID: "tavern", AssetID: "house", Pos: sim.WorldPos{X: 960, Y: 320}, Tags: []string{sim.TagBusiness}},
		// A doorless structure at {70,142} — deliberately NOT tagged TagBusiness so it
		// is out of the rounds circuit, but available for the enter-gate door test.
		"farm": {ID: "farm", AssetID: "barn", Pos: sim.WorldPos{X: 320, Y: 960}},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"meeting_house": {ID: "meeting_house", DisplayName: "Meeting House"},
		"store":         {ID: "store", DisplayName: "General Store"},
		"tavern":        {ID: "tavern", DisplayName: "Tavern"},
		"farm":          {ID: "farm", DisplayName: "Ellis Farm"},
	})
	actorSeed := map[sim.ActorID]*sim.Actor{
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
	}
	for _, id := range extraConstables {
		actorSeed[id] = &sim.Actor{
			ID:                id,
			DisplayName:       string(id),
			Kind:              sim.KindNPCStateful,
			Pos:               sim.TilePos{X: sim.PadX + 12, Y: sim.PadY + 10},
			WorkStructureID:   "meeting_house",
			InsideStructureID: "meeting_house",
			ScheduleStartMin:  intp(0),
			ScheduleEndMin:    intp(1440),
			Attributes:        map[string][]byte{sim.AttrConstable: {}},
		}
	}
	handles.Actors.Seed(actorSeed)
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

// constableHasRoute reports whether the actor has a route installed at all.
func constableHasRoute(t *testing.T, w *sim.World, id sim.ActorID) bool {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.ActiveRoutes[id] != nil, nil
	}})
	if err != nil {
		t.Fatalf("read route %q: %v", id, err)
	}
	return res.(bool)
}

// TestMultipleConstablesIndependentRounds: fix #1 — every constable carrier walks
// its own rounds, and the PER-ACTOR stamp means one carrier's tour can't suppress
// another's due beat.
func TestMultipleConstablesIndependentRounds(t *testing.T) {
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)

	t.Run("both_due_both_dispatch", func(t *testing.T) {
		w := buildConstableCascadeWorld(t, "silas")
		RegisterNPCRoutes(context.Background(), w, llm.NewFakeClient())
		cancel := runRouteCascadeWorld(t, w)
		defer cancel()
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			runConstableRounds(world, now)
			return nil, nil
		}}); err != nil {
			t.Fatalf("run rounds: %v", err)
		}
		for _, id := range []sim.ActorID{"gideon", "silas"} {
			if !constableHasRoute(t, w, id) {
				t.Errorf("%s should have an active rounds route (both carriers due at boot)", id)
			}
		}
	})

	t.Run("one_stamped_does_not_suppress_other", func(t *testing.T) {
		w := buildConstableCascadeWorld(t, "silas")
		RegisterNPCRoutes(context.Background(), w, llm.NewFakeClient())
		cancel := runRouteCascadeWorld(t, w)
		defer cancel()
		// Gideon just did his rounds (per-actor stamp at now → not due); Silas is
		// unstamped (boot catch-up → due). Gideon's stamp must not touch Silas.
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			sim.StampConstableRounds(world, "gideon", now)
			runConstableRounds(world, now)
			return nil, nil
		}}); err != nil {
			t.Fatalf("run rounds: %v", err)
		}
		if constableHasRoute(t, w, "gideon") {
			t.Error("gideon just stamped — should NOT be due, no route")
		}
		if !constableHasRoute(t, w, "silas") {
			t.Error("silas is unstamped — gideon's per-actor stamp must not suppress him")
		}
	})
}

// TestForceRouteConstableOffPostOffShift: fix #4 — a forced constable tour
// deliberately bypasses the at-post/on-shift eligibility ConstableRoundsDue gates,
// dispatching from wherever he is.
func TestForceRouteConstableOffPostOffShift(t *testing.T) {
	w := buildConstableCascadeWorld(t)
	RegisterNPCRoutes(context.Background(), w, llm.NewFakeClient())
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	// Put Gideon OFF his post and OFF shift — the autonomous driver would refuse.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["gideon"]
		a.InsideStructureID = "tavern" // not at his post
		s, e := 0, 1                   // 00:00–00:01 shift → off at any normal hour
		a.ScheduleStartMin, a.ScheduleEndMin = &s, &e
		return nil, nil
	}}); err != nil {
		t.Fatalf("set off-post/off-shift: %v", err)
	}
	res, err := w.Send(ForceRouteCommand(sim.AttrConstable, false))
	if err != nil {
		t.Fatalf("force: %v", err)
	}
	if res.(sim.StartNPCRouteResult).Stops == 0 {
		t.Fatal("forced rounds off-post/off-shift built 0 stops — the operator force must bypass eligibility")
	}
}

// TestConstableRoundsTick_WakesACarrierStoodStillWithARoundOwed is the end-to-end
// half of LLM-549, driving the real once-a-minute tick rather than the predicate.
//
// The live shape: the beat is installed, he is on-shift standing INSIDE his post,
// and nothing else in the engine will wake him — shift duty has no at-work arm and
// the idle backstop only covers actors outdoors. Before this wake he stood in the
// Meeting House for twelve minutes with eight doors unwalked and warrant_count 0.
func TestConstableRoundsTick_WakesACarrierStoodStillWithARoundOwed(t *testing.T) {
	w := buildConstableCascadeWorld(t)
	RegisterNPCRoutes(context.Background(), w, llm.NewFakeClient())
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	// Install the beat, then clear the warrant the install path may have left, so
	// what the tick does is unambiguous.
	if _, err := w.Send(ForceRouteCommand(sim.AttrConstable, false)); err != nil {
		t.Fatalf("force beat: %v", err)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["gideon"]
		a.MoveIntent = nil
		a.Warrants = nil
		a.WarrantedSince = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("clear warrants: %v", err)
	}

	if _, err := w.Send(RouteScheduleTick(time.Now().UTC(), nil)); err != nil {
		t.Fatalf("schedule tick: %v", err)
	}

	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return len(world.Actors["gideon"].Warrants), nil
	}})
	if err != nil {
		t.Fatalf("read warrants: %v", err)
	}
	if res.(int) == 0 {
		t.Fatal("the schedule tick stamped no wake for a carrier stood still with a round owed — " +
			"nothing else starts a beat, so he stands at his post until something unrelated ticks him")
	}
}

// TestConstableRoundsTick_LeavesAWalkingCarrierAlone: the wake must be a nudge for a
// man who has stopped, never a nag at one already on his way. His arrival will tick
// him regardless (finishArrival stamps on every arrival), so a stamp here would buy
// nothing and cost an LLM turn mid-leg.
func TestConstableRoundsTick_LeavesAWalkingCarrierAlone(t *testing.T) {
	w := buildConstableCascadeWorld(t)
	RegisterNPCRoutes(context.Background(), w, llm.NewFakeClient())
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	if _, err := w.Send(ForceRouteCommand(sim.AttrConstable, false)); err != nil {
		t.Fatalf("force beat: %v", err)
	}
	// He is walking somewhere of his own accord, and carries no warrant.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["gideon"]
		a.Warrants = nil
		a.WarrantedSince = nil
		a.MoveIntent = &sim.MoveIntent{}
		return nil, nil
	}}); err != nil {
		t.Fatalf("set walking: %v", err)
	}

	if _, err := w.Send(RouteScheduleTick(time.Now().UTC(), nil)); err != nil {
		t.Fatalf("schedule tick: %v", err)
	}

	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return len(world.Actors["gideon"].Warrants), nil
	}})
	if err != nil {
		t.Fatalf("read warrants: %v", err)
	}
	if res.(int) != 0 {
		t.Errorf("stamped %d warrant(s) on a carrier mid-walk — the wake is for a man who has "+
			"stopped, not one already on his way", res.(int))
	}
}

// TestRouteStopEnterOptIn: fix #2 regression — entering is OPT-IN. A tile-based
// route's candidate (Enter=false) over a door-backed business stays a loiter stop;
// the constable's builder (Enter=true) enters.
func TestRouteStopEnterOptIn(t *testing.T) {
	w := buildConstableCascadeWorld(t)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	stopFor := func(enter bool) sim.RouteStop {
		res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			delete(world.ActiveRoutes, "gideon")
			cand := []sim.RouteCandidate{{ObjectID: "store", Enter: enter, WorldX: 640, WorldY: 320}}
			dest := sim.NewPositionDestination(world.Actors["gideon"].Pos)
			if _, err := sim.StartNPCRoute("gideon", "test", dest, cand, time.Now()).Fn(world); err != nil {
				return nil, err
			}
			r := world.ActiveRoutes["gideon"]
			if r == nil || len(r.Stops) == 0 {
				return sim.RouteStop{}, nil
			}
			return r.Stops[0], nil
		}})
		if err != nil {
			t.Fatalf("start route (enter=%v): %v", enter, err)
		}
		return res.(sim.RouteStop)
	}

	if s := stopFor(false); s.EnterStructureID != "" {
		t.Errorf("Enter=false over a door-backed structure became an enter stop (%q) — entering must be opt-in", s.EnterStructureID)
	}
	if s := stopFor(true); s.EnterStructureID != "store" {
		t.Errorf("Enter=true over a door-backed structure: EnterStructureID=%q, want store", s.EnterStructureID)
	}
}

// TestRouteStopEnterOptIn_DoorlessLoiters: fix E — opting into entering a DOORLESS
// structure still resolves to a loiter stop, because the entry gate (moveToCanEnter's
// door check) rejects it. Equivalent to the old assetHasDoor gate on this path.
func TestRouteStopEnterOptIn_DoorlessLoiters(t *testing.T) {
	w := buildConstableCascadeWorld(t)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.ActiveRoutes, "gideon")
		cand := []sim.RouteCandidate{{ObjectID: "farm", Enter: true, WorldX: 320, WorldY: 960}}
		dest := sim.NewPositionDestination(world.Actors["gideon"].Pos)
		if _, err := sim.StartNPCRoute("gideon", "test", dest, cand, time.Now()).Fn(world); err != nil {
			return nil, err
		}
		r := world.ActiveRoutes["gideon"]
		if r == nil || len(r.Stops) == 0 {
			return sim.RouteStop{}, nil
		}
		return r.Stops[0], nil
	}})
	if err != nil {
		t.Fatalf("start route: %v", err)
	}
	if s := res.(sim.RouteStop); s.EnterStructureID != "" {
		t.Errorf("Enter=true over a DOORLESS structure became an enter stop (%q) — the door gate must force a loiter stop", s.EnterStructureID)
	}
}

// TestTileRouteBuildersNeverEnter: fix F — the tile-based route builders
// (lamplighter / washerwoman / town_crier) never set RouteCandidate.Enter, so their
// stops are byte-for-byte unchanged even for a candidate that IS a door-backed
// structure (belt to resolveRouteStop's c.Enter gate). Uses the real builders on the
// standard route world.
func TestTileRouteBuildersNeverEnter(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		rng := rand.New(rand.NewSource(1))
		var all []sim.RouteCandidate
		all = append(all, buildLaundryCandidates(world, true, rng)...)
		all = append(all, buildNoticeboardCandidates(world, rng)...)
		all = append(all, buildLamplighterCandidates(world, sim.TagDayActive)...)
		return all, nil
	}})
	if err != nil {
		t.Fatalf("build candidates: %v", err)
	}
	cands := res.([]sim.RouteCandidate)
	if len(cands) == 0 {
		t.Fatal("no tile-route candidates built — test would be vacuous")
	}
	for _, c := range cands {
		if c.Enter {
			t.Errorf("a tile-route builder set Enter=true on candidate %q — the schedule routes must never opt into entering", c.ObjectID)
		}
	}
}

// TestConstableBeatArrivalUsesSkipFlip pins the defect the LLM-531 audit found, in
// its LLM-548 form: a beat arrival must go through the skip-flip advance and never
// fall through to the generic tail (sim.AdvanceNPCRoute with flip=true). His stops
// are businesses whose village_object state his round has no business flipping, so
// the fall-through would stamp them with a state the round never intended.
//
// The old shape had three constable arrival branches with different phase
// preconditions, and the one that lacked a branch fell through. There is one branch
// now, keyed on the phase alone, which is why this can be pinned with a single walk.
//
// Drives the real sequence through the cascade handler: call at a stop, step away,
// call at another stop. Every business keeps the state it started with throughout.
func TestConstableBeatArrivalUsesSkipFlip(t *testing.T) {
	w := buildConstableCascadeWorld(t)
	RegisterNPCRoutes(context.Background(), w, llm.NewFakeClient())
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		if _, err := ForceRouteCommand(sim.AttrConstable, false).Fn(world); err != nil {
			return nil, err
		}
		route := world.ActiveRoutes["gideon"]
		if route.Phase != sim.RoutePhaseBeat {
			return nil, fmt.Errorf("force-dispatched round is %q, want beat", route.Phase)
		}
		before := map[sim.VillageObjectID]string{}
		for _, s := range route.Stops {
			before[s.ObjectID] = world.VillageObjects[s.ObjectID].CurrentState
		}
		arriveAt := func(stop sim.RouteStop) {
			a := world.Actors["gideon"]
			a.InsideStructureID = stop.EnterStructureID
			if stop.EnterStructureID == "" {
				a.Pos = stop.WalkTo
			}
			a.MoveIntent = nil
			handleActorArrivedAdvanceRoute(context.Background(), world,
				&sim.ActorArrived{ActorID: "gideon", FinalStructureID: stop.EnterStructureID, At: time.Now()}, llm.NewFakeClient())
		}

		first := route.Stops[0]
		arriveAt(first)
		if got := world.ActiveRoutes["gideon"]; got == nil || !sim.RouteStopVisited(got, 0) {
			return nil, fmt.Errorf("stop 0 not credited on arrival: %+v", got)
		}

		// He steps away to see to something of his own — no stop at all.
		a := world.Actors["gideon"]
		a.InsideStructureID = ""
		a.Pos = sim.Position{X: sim.PadX + 60, Y: sim.PadY + 60}
		a.MoveIntent = nil
		handleActorArrivedAdvanceRoute(context.Background(), world,
			&sim.ActorArrived{ActorID: "gideon", At: time.Now()}, llm.NewFakeClient())
		if got := world.ActiveRoutes["gideon"]; got == nil {
			return nil, fmt.Errorf("beat dropped when he stepped off it")
		}

		// And calls at a different stop of his own accord.
		if len(route.Stops) > 1 {
			arriveAt(route.Stops[1])
			if got := world.ActiveRoutes["gideon"]; got != nil && !sim.RouteStopVisited(got, 1) {
				return nil, fmt.Errorf("stop 1 not credited when he called at it himself")
			}
		}

		// The crux: no business object was flipped anywhere along the way.
		for id, was := range before {
			if now := world.VillageObjects[id].CurrentState; now != was {
				return nil, fmt.Errorf("business %q state changed %q -> %q — the generic flip path ran", id, was, now)
			}
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("beat-arrival skip-flip: %v", err)
	}
}
