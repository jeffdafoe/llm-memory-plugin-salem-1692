package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// umbilical_routes_test.go — LLM-539 coverage for GET /umbilical/npc-routes.

// pendingTimer returns a live *time.Timer that will not fire during the test — the
// stand-in for a real armed dwell timer, since the handler reports only whether one
// is present (a *time.Timer cannot be marshalled). Stopped on cleanup so no timer
// outlives the test.
func pendingTimer(t *testing.T) *time.Timer {
	t.Helper()
	timer := time.NewTimer(time.Hour)
	t.Cleanup(func() { timer.Stop() })
	return timer
}

// seedNPCRoutes installs two active routes on the seeded world:
//
//	gideon (constable) — mid-rounds, ARRIVED at stop 1 of 2 and PARKED in a dwell
//	                     with a live timer. This is the LLM-537 shape: the read has
//	                     to make "parked on a running timer" legible.
//	grace  (town_crier) — walking, cursor on stop 0, no dwell, authoring in flight.
//
// A nil entry is seeded too: clearActiveRoute deletes rather than nils, but the map
// is exported and world-goroutine-owned, so the handler must not panic on one.
func seedNPCRoutes(t *testing.T, w *sim.World) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["gideon"] = &sim.Actor{
			ID: "gideon", DisplayName: "Gideon Marsh", Kind: sim.KindNPCStateful,
			// Inside the stop-1 structure → RouteStopArrived true for an enter stop.
			InsideStructureID: "blacksmith",
		}
		world.Actors["grace"] = &sim.Actor{
			ID: "grace", DisplayName: "Grace Edwards", Kind: sim.KindNPCStateful,
			Pos: sim.TilePos{X: 40, Y: 41}, // NOT the stop tile → still walking
		}
		// StartNPCRoute lazy-allocates this map, so a booted world with no route yet
		// carries a nil one — mirror that here rather than assuming it exists.
		if world.ActiveRoutes == nil {
			world.ActiveRoutes = map[sim.ActorID]*sim.NPCRoute{}
		}
		homePost := sim.NewStructureEnterDestination("meeting_house")
		world.ActiveRoutes["gideon"] = &sim.NPCRoute{
			NPCID: "gideon", Label: sim.AttrConstable,
			Phase: sim.RoutePhaseBeat, Gen: 7, StopIdx: 1,
			Stops: []sim.RouteStop{
				{ObjectID: "store", WalkTo: sim.Position{X: 10, Y: 11}, EnterStructureID: "store"},
				{ObjectID: "blacksmith", WalkTo: sim.Position{X: 20, Y: 21}, EnterStructureID: "blacksmith"},
			},
			Visited:         []bool{true, false},
			StaleRetries:    2,
			HomeDestination: homePost,
		}
		world.ActiveRoutes["grace"] = &sim.NPCRoute{
			NPCID: "grace", Label: sim.AttrTownCrier,
			Phase: sim.RoutePhaseActive, Gen: 3, StopIdx: 0,
			Stops: []sim.RouteStop{
				{ObjectID: "board_north", WalkTo: sim.Position{X: 30, Y: 31}, NewState: "posted"},
			},
			Authoring:       true,
			HomeDestination: sim.NewPositionDestination(sim.Position{X: 5, Y: 6}),
		}
		world.ActiveRoutes["ghost"] = nil
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed routes: %v", err)
	}
}

func TestUmbilical_NPCRoutes(t *testing.T) {
	w := seededWorld(t)
	seedNPCRoutes(t, w)
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	h := srv.Handler()

	rec := req(t, h, "/api/village/umbilical/npc-routes", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("npc-routes = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalNPCRoutesDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ContractVersion != ContractVersion {
		t.Errorf("contract_version = %d, want %d", out.ContractVersion, ContractVersion)
	}
	// The nil entry is skipped, not counted and not a panic.
	if out.Total != 2 || len(out.Routes) != 2 {
		t.Fatalf("total/len = %d/%d, want 2/2 (the nil route entry is skipped)", out.Total, len(out.Routes))
	}
	// Sorted by actor id: gideon before grace.
	if out.Routes[0].NPCID != "gideon" || out.Routes[1].NPCID != "grace" {
		t.Fatalf("order = %s,%s, want gideon,grace (sorted by id)", out.Routes[0].NPCID, out.Routes[1].NPCID)
	}

	// The LLM-537 shape: arrived at the current stop, dwelling, timer ARMED.
	// All three together are what separate "working as designed" from "stuck".
	g := out.Routes[0]
	if g.NPCName != "Gideon Marsh" || g.Label != sim.AttrConstable || g.Phase != string(sim.RoutePhaseBeat) {
		t.Errorf("gideon name/label/phase = %q/%q/%q, want Gideon Marsh/constable/beat", g.NPCName, g.Label, g.Phase)
	}
	if g.Gen != 7 || g.StopIdx != 1 || g.StopCount != 2 || g.StaleRetries != 2 {
		t.Errorf("gideon gen/stop_idx/stop_count/stale_retries = %d/%d/%d/%d, want 7/1/2/2",
			g.Gen, g.StopIdx, g.StopCount, g.StaleRetries)
	}
	// An enter stop: both arrival tests agree, since InsideStructureID is exact
	// either way. They can only diverge on a loiter stop (see the sub-test below).
	if !g.ArrivedAtCurrentStop || !g.ReachedCurrentStopOnFoot {
		t.Errorf("gideon arrived/reached = %v/%v, want true/true — he is inside the stop-1 structure",
			g.ArrivedAtCurrentStop, g.ReachedCurrentStopOnFoot)
	}
	if g.Authoring {
		t.Error("gideon authoring = true — that is the crier's flag, not the constable's")
	}
	// Stop list: both stops present, `current` marks exactly the cursor's stop.
	if len(g.Stops) != 2 {
		t.Fatalf("gideon stops = %d, want 2", len(g.Stops))
	}
	if g.Stops[0].Current || !g.Stops[1].Current {
		t.Errorf("gideon current flags = %v/%v, want false/true (cursor is stop 1)", g.Stops[0].Current, g.Stops[1].Current)
	}
	// The beat's real progress read: one of two stops called at. StopIdx alone
	// cannot say this — a beat carrier walks his circuit in his own order.
	if g.UnvisitedCount != 1 {
		t.Errorf("gideon unvisited_count = %d, want 1 (the store is called at, the blacksmith is not)", g.UnvisitedCount)
	}
	if !g.Stops[0].Visited || g.Stops[1].Visited {
		t.Errorf("gideon stop visited flags = %v/%v, want true/false", g.Stops[0].Visited, g.Stops[1].Visited)
	}
	if g.Stops[1].ObjectID != "blacksmith" || g.Stops[1].EnterStructureID != "blacksmith" {
		t.Errorf("gideon stop 1 = %+v, want the blacksmith as an ENTER stop", g.Stops[1])
	}
	if g.Stops[1].WalkToX != 20 || g.Stops[1].WalkToY != 21 {
		t.Errorf("gideon stop 1 tile = (%d,%d), want (20,21)", g.Stops[1].WalkToX, g.Stops[1].WalkToY)
	}
	// The constable returns to his POST, a structure — so the structure sibling is
	// set and the position siblings stay absent.
	if g.HomeDestinationKind != string(sim.MoveDestinationStructureEnter) || g.HomeDestinationStructureID != "meeting_house" {
		t.Errorf("gideon home dest = %q/%q, want structure_enter/meeting_house", g.HomeDestinationKind, g.HomeDestinationStructureID)
	}
	if g.HomeDestinationX != nil || g.HomeDestinationY != nil {
		t.Error("gideon home dest carries a position — a structure_enter destination has none")
	}

	// The crier: still walking (not arrived) but authoring.
	c := out.Routes[1]
	if c.ArrivedAtCurrentStop || c.ReachedCurrentStopOnFoot {
		t.Errorf("grace arrived/reached = %v/%v, want false/false — she is still walking",
			c.ArrivedAtCurrentStop, c.ReachedCurrentStopOnFoot)
	}
	// A decorative route keeps no visited record, so every stop reports unvisited
	// and the count is simply the stop count. Not a bug — she walks her itinerary in
	// cursor order and StopIdx IS her progress.
	if c.Stops[0].Visited {
		t.Error("grace stop visited = true — a decorative route records no visits")
	}
	if !c.Authoring {
		t.Error("grace authoring = false — an author call is in flight for her stop")
	}
	if c.Stops[0].NewState != "posted" {
		t.Errorf("grace stop new_state = %q, want posted", c.Stops[0].NewState)
	}
	// Her home destination is a POSITION, so the tile siblings are set and the
	// structure one is empty. A *int pair is used so an unset destination reads
	// absent rather than as tile (0,0), which is a real tile in the padded grid.
	if c.HomeDestinationKind != string(sim.MoveDestinationPosition) || c.HomeDestinationStructureID != "" {
		t.Errorf("grace home dest = %q/%q, want position/empty", c.HomeDestinationKind, c.HomeDestinationStructureID)
	}
	if c.HomeDestinationX == nil || c.HomeDestinationY == nil || *c.HomeDestinationX != 5 || *c.HomeDestinationY != 6 {
		t.Errorf("grace home dest position = %v/%v, want 5/6", c.HomeDestinationX, c.HomeDestinationY)
	}

	// Gating mirrors the rest of the read surface: 404 when the umbilical is off,
	// 403 for a non-operator.
	if rec := req(t, NewServer(seededWorld(t), permAuth{operatorPerms}).Handler(), "/api/village/umbilical/npc-routes", "tok"); rec.Code != http.StatusNotFound {
		t.Errorf("npc-routes umbilical-off = %d, want 404", rec.Code)
	}
	if rec := req(t, umbilicalServer(t, nil, telemetry.New(4)), "/api/village/umbilical/npc-routes", "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("npc-routes non-operator = %d, want 403", rec.Code)
	}
}

// TestUmbilical_NPCRoutesActorFilter: the optional filter narrows to one carrier,
// and an unknown/routeless actor yields an EMPTY list rather than a 404 — "he has
// no route right now" and "no such actor" are both legitimately empty answers to
// "what is he routing?", so a 404 would force the caller to distinguish two cases
// it does not care about.
func TestUmbilical_NPCRoutesActorFilter(t *testing.T) {
	w := seededWorld(t)
	seedNPCRoutes(t, w)
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	h := srv.Handler()

	decode := func(t *testing.T, path string) UmbilicalNPCRoutesDTO {
		t.Helper()
		rec := req(t, h, path, "tok")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200; body=%s", path, rec.Code, rec.Body.String())
		}
		var out UmbilicalNPCRoutesDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	one := decode(t, "/api/village/umbilical/npc-routes?actor=gideon")
	if one.Total != 1 || len(one.Routes) != 1 || one.Routes[0].NPCID != "gideon" {
		t.Fatalf("filtered = %d routes %+v, want just gideon", one.Total, one.Routes)
	}

	for _, actor := range []string{"hannah", "nobody"} {
		out := decode(t, "/api/village/umbilical/npc-routes?actor="+actor)
		if out.Total != 0 || len(out.Routes) != 0 {
			t.Errorf("actor=%s returned %d routes, want an empty list", actor, out.Total)
		}
	}
}

// TestUmbilical_NPCRoutesBeatProgressIsCoverageNotCursor pins the read a beat
// investigation actually needs (LLM-548).
//
// A beat carrier walks his circuit in whatever order he chooses, so StopIdx is not
// progress — it only says which place the cue will name next. Here he has called at
// stops 0, 2 and 3 while the cursor sits on 1, and an operator reading the cursor
// alone would put him one stop into a four-stop round when he is three-quarters
// done. unvisited_count and the per-stop visited flags are what answer the question.
func TestUmbilical_NPCRoutesBeatProgressIsCoverageNotCursor(t *testing.T) {
	w := seededWorld(t)
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["gideon"] = &sim.Actor{ID: "gideon", DisplayName: "Gideon Marsh"}
		if world.ActiveRoutes == nil {
			world.ActiveRoutes = map[sim.ActorID]*sim.NPCRoute{}
		}
		world.ActiveRoutes["gideon"] = &sim.NPCRoute{
			NPCID: "gideon", Label: sim.AttrConstable, Phase: sim.RoutePhaseBeat,
			StopIdx: 1,
			Stops: []sim.RouteStop{
				{ObjectID: "store", WalkTo: sim.Position{X: 1, Y: 2}},
				{ObjectID: "farm", WalkTo: sim.Position{X: 3, Y: 4}},
				{ObjectID: "inn", WalkTo: sim.Position{X: 5, Y: 6}},
				{ObjectID: "smithy", WalkTo: sim.Position{X: 7, Y: 8}},
			},
			Visited: []bool{true, false, true, true},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed beat route: %v", err)
	}
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(4))

	rec := req(t, srv.Handler(), "/api/village/umbilical/npc-routes?actor=gideon", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("npc-routes = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalNPCRoutesDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Routes) != 1 {
		t.Fatalf("routes = %d, want 1", len(out.Routes))
	}
	r := out.Routes[0]
	if r.Phase != string(sim.RoutePhaseBeat) {
		t.Errorf("phase = %q, want beat", r.Phase)
	}
	if r.UnvisitedCount != 1 {
		t.Errorf("unvisited_count = %d, want 1 — only the farm is still owed", r.UnvisitedCount)
	}
	// The cursor reads 1 while three of four stops are walked. That gap IS the point.
	if r.StopIdx != 1 || r.StopCount != 4 {
		t.Errorf("stop_idx/stop_count = %d/%d, want 1/4", r.StopIdx, r.StopCount)
	}
	for i, want := range []bool{true, false, true, true} {
		if r.Stops[i].Visited != want {
			t.Errorf("stop %d visited = %v, want %v", i, r.Stops[i].Visited, want)
		}
	}
}

// TestUmbilical_NPCRoutesStrictVsTolerantArrival pins why both arrival fields exist.
// On a LOITER stop the route's own strict test is exact tile equality, but a carrier
// who walked himself there lands in a visitor slot AROUND the loiter pin — so he is
// plainly at the stop and reads arrived=false. The tolerant twin reads true, and the
// two disagreeing is the diagnostic (LLM-530, LLM-531 were both that gap).
func TestUmbilical_NPCRoutesStrictVsTolerantArrival(t *testing.T) {
	w := seededWorld(t)
	pin := sim.Position{X: 30, Y: 30}
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["grace"] = &sim.Actor{
			ID: "grace", DisplayName: "Grace Edwards",
			Pos: sim.TilePos{X: pin.X + 1, Y: pin.Y}, // one tile off the pin: a visitor slot
		}
		if world.ActiveRoutes == nil {
			world.ActiveRoutes = map[sim.ActorID]*sim.NPCRoute{}
		}
		world.ActiveRoutes["grace"] = &sim.NPCRoute{
			NPCID: "grace", Label: sim.AttrTownCrier, Phase: sim.RoutePhaseActive,
			Stops: []sim.RouteStop{{ObjectID: "board_north", WalkTo: pin}}, // loiter stop
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed loiter stop: %v", err)
	}
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(4))

	rec := req(t, srv.Handler(), "/api/village/umbilical/npc-routes?actor=grace", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("npc-routes = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalNPCRoutesDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Routes) != 1 {
		t.Fatalf("routes = %d, want 1", len(out.Routes))
	}
	r := out.Routes[0]
	if r.ArrivedAtCurrentStop {
		t.Error("arrived_at_current_stop = true one tile off the pin — the strict test is exact tile equality")
	}
	if !r.ReachedCurrentStopOnFoot {
		t.Error("reached_current_stop_on_foot = false in a visitor slot beside the pin — " +
			"the tolerant test is what 'standing at this place' means everywhere else in the engine")
	}
}

// TestUmbilical_NPCRoutesNoRoutes covers the ordinary boot state, which is also the
// nil-map edge: World.ActiveRoutes is lazy-allocated by StartNPCRoute, so a village
// where nobody has walked a route yet has no map at all. The read must answer with
// an empty list and a real 200, not a panic and not a null array.
func TestUmbilical_NPCRoutesNoRoutes(t *testing.T) {
	w := seededWorld(t)
	// Assert the premise rather than assuming it: this test is only meaningful if
	// the map really is nil, and a future seeding change could quietly allocate it.
	nilMap, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.ActiveRoutes == nil, nil
	}})
	if err != nil {
		t.Fatalf("read ActiveRoutes: %v", err)
	}
	if !nilMap.(bool) {
		t.Fatal("fixture invalid: ActiveRoutes is already allocated, so this does not cover the nil-map edge")
	}
	srv := NewServer(w, permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(4))

	rec := req(t, srv.Handler(), "/api/village/umbilical/npc-routes", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("npc-routes = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalNPCRoutesDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != 0 || len(out.Routes) != 0 {
		t.Errorf("total/len = %d/%d, want 0/0", out.Total, len(out.Routes))
	}
	// `routes` must serialize as [] rather than null — a null array is a decoding
	// hazard for any client that ranges over it.
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"routes":[]`)) {
		t.Errorf("empty routes serialized as null, want []: %s", rec.Body.String())
	}
}

// TestUmbilical_NPCRoutesInManifest pins the route into the READ (non-control)
// whitelist — the sibling reads all carry this assertion, and it is what proves the
// route is reachable without control armed.
func TestUmbilical_NPCRoutesInManifest(t *testing.T) {
	srv := NewServer(seededWorld(t), permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(4))

	rec := req(t, srv.Handler(), "/api/village/umbilical", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest = %d, want 200", rec.Code)
	}
	var dto UmbilicalManifestDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.ControlEnabled {
		t.Fatal("fixture invalid: control is armed, so this cannot prove the route is a READ")
	}
	if !manifestRouteKeys(dto)["GET /api/village/umbilical/npc-routes"] {
		t.Errorf("/umbilical/npc-routes missing from the read manifest: %+v", dto.Routes)
	}
}
