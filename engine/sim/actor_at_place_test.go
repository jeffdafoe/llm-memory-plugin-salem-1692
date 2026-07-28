package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// actor_at_place_test.go — LLM-550. "Is this actor AT this place?" is one of the
// most-asked questions in the engine and one of the most-broken: the constable
// rounds alone have been derailed by it in LLM-527, LLM-530, LLM-531, LLM-543 and
// LLM-550, each time because a caller compared a position against a tile it
// *associated* with a place rather than against the tile that IS the place.
//
// This file is the standing coverage for the answer. The rules it pins:
//
//  1. A place's tile is ObjectLoiterPin — per-instance loiter offset, else the door
//     offset, else the footprint fallback. Nothing else is the place.
//  2. ActorAtObjectPin asks about the ONE object named. No neighbour can answer for
//     it, however close.
//  3. A route stop's WalkTo is a PATHING GOAL and may sit anywhere walkable near the
//     object. It is never the location test.
//  4. Enter stops answer on InsideStructureID; loiter stops answer on the pin. The
//     STOP decides which, because resolveRouteStop already made that judgement.
//  5. A decorative carrier is unchanged: only the route's own walk can have moved
//     it, so dispatch completion is its complete answer.

func atPlaceIntp(v int) *int { return &v }

// placeWorld builds the maps ActorAtObjectPin reads. Objects are placed by ANCHOR
// tile; the helper converts to the WorldPos the engine stores so the fixtures read
// in the same units the rest of the file reasons about.
func placeWorld(objs map[sim.VillageObjectID]sim.Position, assets map[sim.AssetID]*sim.Asset,
	assetOf map[sim.VillageObjectID]sim.AssetID) (map[sim.VillageObjectID]*sim.VillageObject, map[sim.AssetID]*sim.Asset) {
	out := map[sim.VillageObjectID]*sim.VillageObject{}
	for id, anchor := range objs {
		out[id] = &sim.VillageObject{
			ID:          id,
			AssetID:     assetOf[id],
			DisplayName: string(id),
			// WorldPos → tile is a floor-divide by the tile size, then the pad; going
			// the other way is exact for a tile-aligned anchor.
			Pos: sim.WorldPos{
				X: float64((anchor.X - sim.PadX) * 32),
				Y: float64((anchor.Y - sim.PadY) * 32),
			},
		}
	}
	return out, assets
}

// TestObjectLoiterPin_IsThePlacesOwnTile pins that a place's tile comes from the
// object, through the same fallback chain pickVisitorSlot rings when it parks an
// arrival. If these two ever disagree, an actor is parked somewhere the engine will
// not recognise as the place he was sent to — which is the whole family of bug.
func TestObjectLoiterPin_IsThePlacesOwnTile(t *testing.T) {
	anchor := sim.Position{X: sim.PadX + 10, Y: sim.PadY + 10}

	cases := []struct {
		name  string
		vobj  *sim.VillageObject
		asset *sim.Asset
		want  sim.Position
	}{
		{
			name:  "market stall with no door: footprint fallback",
			vobj:  &sim.VillageObject{AssetID: "a"},
			asset: &sim.Asset{FootprintBottom: 1},
			want:  sim.Position{X: anchor.X, Y: anchor.Y + 1 + 2},
		},
		{
			name:  "door-backed building: one tile south of the door",
			vobj:  &sim.VillageObject{AssetID: "a"},
			asset: &sim.Asset{DoorOffsetX: atPlaceIntp(0), DoorOffsetY: atPlaceIntp(2)},
			want:  sim.Position{X: anchor.X, Y: anchor.Y + 2 + 1},
		},
		{
			name:  "per-instance override beats both",
			vobj:  &sim.VillageObject{AssetID: "a", LoiterOffsetX: atPlaceIntp(-2), LoiterOffsetY: atPlaceIntp(5)},
			asset: &sim.Asset{DoorOffsetX: atPlaceIntp(0), DoorOffsetY: atPlaceIntp(2), FootprintBottom: 4},
			want:  sim.Position{X: anchor.X - 2, Y: anchor.Y + 5},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.vobj.ID = "here"
			tc.vobj.DisplayName = "Here"
			tc.vobj.Pos = sim.WorldPos{X: float64((anchor.X - sim.PadX) * 32), Y: float64((anchor.Y - sim.PadY) * 32)}
			objects := map[sim.VillageObjectID]*sim.VillageObject{"here": tc.vobj}
			assets := map[sim.AssetID]*sim.Asset{"a": tc.asset}

			got, ok := sim.ObjectLoiterPin(objects, assets, "here")
			if !ok {
				t.Fatal("no pin resolved for a placed object with a known asset")
			}
			if got != tc.want {
				t.Errorf("pin = %v, want %v", got, tc.want)
			}
			// The actor standing ON the pin is the base case; if this fails nothing
			// else in the family can be right.
			if !sim.ActorAtObjectPin(objects, assets, got, "here") {
				t.Error("an actor standing exactly on the pin does not read as at the place")
			}
		})
	}
}

// TestObjectLoiterPin_UnresolvableIsNotAPlace: a missing object, a missing asset or
// an empty id must answer "not here" rather than defaulting to the origin tile —
// which would make every actor standing near (0,0) read as at that place.
func TestObjectLoiterPin_UnresolvableIsNotAPlace(t *testing.T) {
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		"orphan": {ID: "orphan", AssetID: "missing", DisplayName: "Orphan"},
		"nilobj": nil,
	}
	assets := map[sim.AssetID]*sim.Asset{}

	for _, id := range []sim.VillageObjectID{"", "absent", "orphan", "nilobj"} {
		if _, ok := sim.ObjectLoiterPin(objects, assets, id); ok {
			t.Errorf("ObjectLoiterPin(%q) resolved a pin it has no business resolving", id)
		}
		if sim.ActorAtObjectPin(objects, assets, sim.Position{}, id) {
			t.Errorf("ActorAtObjectPin(%q) says an actor at the origin is at an unresolvable place", id)
		}
	}
}

// TestActorAtObjectPin_TheRing pins the tolerance as the pin's own footprint: the
// pin tile plus its eight king's-move slots, and nothing further. That is the exact
// inverse of pickVisitorSlot, which is what makes "he walked here himself" and "he
// is here" the same answer.
func TestActorAtObjectPin_TheRing(t *testing.T) {
	anchor := sim.Position{X: sim.PadX + 20, Y: sim.PadY + 20}
	objects, assets := placeWorld(
		map[sim.VillageObjectID]sim.Position{"stall": anchor},
		map[sim.AssetID]*sim.Asset{"a": {FootprintBottom: 0}},
		map[sim.VillageObjectID]sim.AssetID{"stall": "a"},
	)
	pin, ok := sim.ObjectLoiterPin(objects, assets, "stall")
	if !ok {
		t.Fatal("fixture: no pin")
	}

	// Every slot in the ring counts as at the place.
	for dx := -1; dx <= 1; dx++ {
		for dy := -1; dy <= 1; dy++ {
			at := sim.Position{X: pin.X + dx, Y: pin.Y + dy}
			if !sim.ActorAtObjectPin(objects, assets, at, "stall") {
				t.Errorf("slot (%+d,%+d) off the pin does not count as at the place — "+
					"pickVisitorSlot parks arrivals in exactly these tiles", dx, dy)
			}
		}
	}
	// Two out is not. This is the boundary the PW Apothecary fell foul of: its
	// route tile was 2 from its pin, and a man at the stall read as elsewhere.
	for _, off := range []sim.Position{{X: 2}, {X: -2}, {Y: 2}, {Y: -2}, {X: 2, Y: 2}} {
		at := sim.Position{X: pin.X + off.X, Y: pin.Y + off.Y}
		if sim.ActorAtObjectPin(objects, assets, at, "stall") {
			t.Errorf("(%+d,%+d) off the pin counts as at the place — the ring must not widen, "+
				"or a carrier merely walking past would be credited", off.X, off.Y)
		}
	}
}

// TestActorAtObjectPin_NoNeighbourAnswersForIt is the difference between this and
// ResolveLoiteringObject, and the reason a known-place question must not be answered
// by the nearest-scan. Two stalls share a doorstep; standing at one must NOT report
// being at the other, whichever sorts first.
func TestActorAtObjectPin_NoNeighbourAnswersForIt(t *testing.T) {
	anchor := sim.Position{X: sim.PadX + 30, Y: sim.PadY + 30}
	objects, assets := placeWorld(
		map[sim.VillageObjectID]sim.Position{
			"aaa_near": anchor,
			"zzz_far":  {X: anchor.X + 3, Y: anchor.Y},
		},
		map[sim.AssetID]*sim.Asset{"a": {FootprintBottom: 0}},
		map[sim.VillageObjectID]sim.AssetID{"aaa_near": "a", "zzz_far": "a"},
	)
	farPin, ok := sim.ObjectLoiterPin(objects, assets, "zzz_far")
	if !ok {
		t.Fatal("fixture: no pin")
	}

	if !sim.ActorAtObjectPin(objects, assets, farPin, "zzz_far") {
		t.Error("standing on a place's own pin does not count as being at it")
	}
	if sim.ActorAtObjectPin(objects, assets, farPin, "aaa_near") {
		t.Error("standing at one stall reports being at its neighbour — a known-place question " +
			"must never be answered by whichever object happens to be nearest")
	}
}

// TestRouteStopReached_WalkToIsNotThePlace is the live LLM-550 geometry, reproduced.
//
// The constable's PW Apothecary stop had its object anchored at one tile and its
// route WalkTo two tiles away, because buildRouteStops needs a WALKABLE tile and the
// anchor's own was not. He called there three times, held a full conversation each
// visit, and was credited none of them — so his round could never finish and he
// walked the village without stopping for half an hour.
//
// A beat carrier standing at the place must be credited whatever tile the route
// picked to path to.
func TestRouteStopReached_WalkToIsNotThePlace(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	startBeat(t, w, "lamp", sampleLampCandidates())

	var credited bool
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route := world.ActiveRoutes["lamp"]
		stop := route.Stops[0]
		pin, ok := sim.ObjectLoiterPin(world.VillageObjects, world.Assets, stop.ObjectID)
		if !ok {
			t.Fatal("fixture: stop 0 has no resolvable pin")
		}
		// Displace the route's pathing tile well off the pin — the live shape.
		route.Stops[0].WalkTo = sim.Position{X: pin.X + 2, Y: pin.Y + 2}
		if sim.RouteStopArrived(world.Actors["lamp"], route.Stops[0]) {
			t.Fatal("fixture invalid: the actor is already on WalkTo, so the dispatch arm would answer")
		}
		// He walked himself to the place: a visitor slot ringing the PIN.
		a := world.Actors["lamp"]
		a.Pos = sim.Position{X: pin.X + 1, Y: pin.Y}
		a.MoveIntent = nil
		credited = sim.RouteStopReached(world, route, a, route.Stops[0])
		return nil, nil
	}}); err != nil {
		t.Fatalf("arrange: %v", err)
	}

	if !credited {
		t.Error("a beat carrier standing at the place was not recognised because the route's " +
			"pathing tile sits 2 away — this is LLM-550, and it stranded a live round for half an hour")
	}
}

// TestRouteStopReached_BeatIgnoresTheRoutesPathingTile is the code_review finding on
// LLM-550's first draft, kept as a regression.
//
// The draft accepted the dispatch arm for a beat too, reasoning it was a harmless
// superset. It is not. A beat dispatches no walk at any point, so there is no
// dispatched arrival to lose — and WalkTo is just an ordinary walkable tile beside a
// business. Nothing in the predicate can tell "the route put him there" (it never
// does) from a teleport, an admin force-move, a competing movement producer, or the
// carrier wandering across it on his way somewhere else. Crediting on it would mean
// marking a stop called-at while the man is two tiles from the stall, which is the
// same class of wrong answer the whole ticket exists to remove.
func TestRouteStopReached_BeatIgnoresTheRoutesPathingTile(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	startBeat(t, w, "lamp", sampleLampCandidates())

	var onWalkToAwayFromPin, atPin bool
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route := world.ActiveRoutes["lamp"]
		pin, ok := sim.ObjectLoiterPin(world.VillageObjects, world.Assets, route.Stops[0].ObjectID)
		if !ok {
			t.Fatal("fixture: no pin")
		}
		// The live shape: the route's tile sits outside the pin's ring.
		route.Stops[0].WalkTo = sim.Position{X: pin.X + 3, Y: pin.Y + 3}
		a := world.Actors["lamp"]

		a.Pos = route.Stops[0].WalkTo
		onWalkToAwayFromPin = sim.RouteStopReached(world, route, a, route.Stops[0])

		a.Pos = pin
		atPin = sim.RouteStopReached(world, route, a, route.Stops[0])
		return nil, nil
	}}); err != nil {
		t.Fatalf("arrange: %v", err)
	}

	if onWalkToAwayFromPin {
		t.Error("a beat credited a stop for standing on the route's pathing tile, three tiles from " +
			"the place — the beat dispatched no walk, so that tile is evidence of nothing")
	}
	if !atPin {
		t.Error("a beat did not credit a stop for standing at the place itself")
	}
}

// TestRouteStopReached_BeatNotAdoptedByALaterStopsPathingTile: the scan in
// reachedStopIndex walks every unvisited stop, so a carrier who is teleported onto
// some LATER stop's WalkTo must not have that stop adopted either. Same reasoning as
// above, exercised through the path that actually credits.
func TestRouteStopReached_BeatNotAdoptedByALaterStopsPathingTile(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	startBeat(t, w, "lamp", threeLampCandidates())

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route := world.ActiveRoutes["lamp"]
		pin, ok := sim.ObjectLoiterPin(world.VillageObjects, world.Assets, route.Stops[2].ObjectID)
		if !ok {
			t.Fatal("fixture: no pin")
		}
		// Stop 2's pathing tile, well off its pin. He is dropped there by something
		// that is not the route — the route never walks a beat anywhere.
		route.Stops[2].WalkTo = sim.Position{X: pin.X + 4, Y: pin.Y + 4}
		a := world.Actors["lamp"]
		a.Pos = route.Stops[2].WalkTo
		a.MoveIntent = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("arrange: %v", err)
	}

	if _, err := w.Send(sim.AdvanceNPCRoute("lamp")); err != nil {
		t.Fatalf("advance: %v", err)
	}
	route := activeRouteOf(t, w, "lamp")
	if route == nil {
		t.Fatal("beat cleared — nothing should have been credited")
	}
	for i := range route.Stops {
		if sim.RouteStopVisited(route, i) {
			t.Errorf("stop %d credited for a position that is no stop's place — the later-stop scan "+
				"adopted a pathing tile", i)
		}
	}
}

// TestRouteStopReached_DecorativeStillAnswersOnItsDispatchedTile keeps the other half
// honest. A decorative carrier has no location arm at all, so its ONLY signal is
// standing on the tile the route walked it to — including for stops whose object
// cannot resolve a pin (an unnamed prop, a dangling asset), which is most of what
// lamplighter and washerwoman routes visit.
func TestRouteStopReached_DecorativeStillAnswersOnItsDispatchedTile(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrLamplighter, homeDest, sampleLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("start: %v", err)
	}

	var onWalkTo, offWalkTo bool
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route := world.ActiveRoutes["lamp"]
		stop := route.Stops[0]
		// An object that cannot resolve a pin: only the dispatch arm can answer.
		route.Stops[0].ObjectID = "no-such-object"
		a := world.Actors["lamp"]

		a.Pos = stop.WalkTo
		onWalkTo = sim.RouteStopReached(world, route, a, route.Stops[0])

		a.Pos = sim.Position{X: stop.WalkTo.X + 4, Y: stop.WalkTo.Y + 4}
		offWalkTo = sim.RouteStopReached(world, route, a, route.Stops[0])
		return nil, nil
	}}); err != nil {
		t.Fatalf("arrange: %v", err)
	}

	if !onWalkTo {
		t.Error("a decorative carrier standing on its dispatched tile did not read as arrived — " +
			"routes over unnamed props have no other signal")
	}
	if offWalkTo {
		t.Error("a decorative carrier well off its dispatched tile read as arrived")
	}
}

// TestRouteStopReached_DecorativeIgnoresTheLocationArm keeps the decorative carriers
// byte-for-byte. Only the route's own walk can have moved a lamplighter, so an
// off-WalkTo position is an external bump the stale-arrival re-walk exists to undo —
// answering "well, he's near the lamp" would mask it.
func TestRouteStopReached_DecorativeIgnoresTheLocationArm(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	homeDest := sim.NewStructureEnterDestination("home")
	if _, err := w.Send(sim.StartNPCRoute("lamp", sim.AttrLamplighter, homeDest, sampleLampCandidates(), time.Now().UTC())); err != nil {
		t.Fatalf("start: %v", err)
	}

	var decorative, beat bool
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route := world.ActiveRoutes["lamp"]
		stop := route.Stops[0]
		pin, ok := sim.ObjectLoiterPin(world.VillageObjects, world.Assets, stop.ObjectID)
		if !ok {
			t.Fatal("fixture: no pin")
		}
		route.Stops[0].WalkTo = sim.Position{X: pin.X + 3, Y: pin.Y + 3}
		a := world.Actors["lamp"]
		a.Pos = pin // at the place, but NOT on the route's tile

		decorative = sim.RouteStopReached(world, route, a, route.Stops[0])
		route.Label = sim.AttrConstable // same geometry, volition carrier
		beat = sim.RouteStopReached(world, route, a, route.Stops[0])
		return nil, nil
	}}); err != nil {
		t.Fatalf("arrange: %v", err)
	}

	if decorative {
		t.Error("a decorative carrier standing off its dispatched tile read as arrived — that hides " +
			"the external bump its stale-arrival re-walk is there to undo")
	}
	if !beat {
		t.Error("a beat carrier at the same position did NOT read as at the stop — the location arm " +
			"is exactly what a self-directed carrier needs")
	}
}

// TestRouteStopReached_EnterStopsAnswerOnInterior: the STOP decides which posture
// counts, not the asset. resolveRouteStop already made that judgement at build time
// via moveToCanEnter — an open enterable business became an ENTER stop, a closed or
// locked one was downgraded to a loiter stop precisely so the carrier stands at its
// door. Re-deriving door-ness here could contradict the stop he was actually sent to.
func TestRouteStopReached_EnterStopsAnswerOnInterior(t *testing.T) {
	w, cancel := buildRouteTestWorld(t)
	defer cancel()

	startBeat(t, w, "lamp", sampleLampCandidates())

	var insideCounts, atPinCounts bool
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		route := world.ActiveRoutes["lamp"]
		stop := route.Stops[0]
		pin, ok := sim.ObjectLoiterPin(world.VillageObjects, world.Assets, stop.ObjectID)
		if !ok {
			t.Fatal("fixture: no pin")
		}
		// Make it an ENTER stop for a structure the actor is not standing in, and put
		// its pathing tile somewhere else again — so "at the pin is false" cannot pass
		// on fixture geometry that happens to make every tile disagree (code_review).
		route.Stops[0].EnterStructureID = "home"
		route.Stops[0].WalkTo = sim.Position{X: pin.X + 5, Y: pin.Y + 5}
		a := world.Actors["lamp"]

		a.Pos = pin
		a.InsideStructureID = ""
		atPinCounts = sim.RouteStopReached(world, route, a, route.Stops[0])

		a.InsideStructureID = "home"
		insideCounts = sim.RouteStopReached(world, route, a, route.Stops[0])
		return nil, nil
	}}); err != nil {
		t.Fatalf("arrange: %v", err)
	}

	if atPinCounts {
		t.Error("standing at the pin satisfied an ENTER stop — the stop says he must go inside, " +
			"and a loiter-pin answer would credit a shop he never entered")
	}
	if !insideCounts {
		t.Error("being inside the target structure did not satisfy an ENTER stop")
	}
}
