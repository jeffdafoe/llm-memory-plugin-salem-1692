package sim_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/cascade"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// arrival_encounter_subscriber_test.go — arrival-encounter subscriber
// tests. Lives in package sim_test (in engine/sim/, not in cascade/)
// so it can reach sim.EmitForTest, which export_test.go only exposes
// inside the sim package's own test scope.
//
// All tests synthesize ActorArrived via sim.EmitForTest rather than
// driving the locomotion ticker — that isolates the subscriber from
// pathfinding / terrain (which don't affect the encounter decision) and
// keeps the world bootstrap small.

// encounterActor describes one seeded actor for buildEncounterWorld.
type encounterActor struct {
	id                sim.ActorID
	x, y              int
	insideStructureID sim.StructureID // empty = outdoors
	currentHuddleID   sim.HuddleID    // empty = not in a huddle
}

// buildEncounterWorld seeds a running world for arrival-encounter
// tests with the given actors at fixed positions. No terrain (the
// subscriber doesn't drive pathfinding) and no structures beyond what
// `insideStructureID` references (those are seeded as empty stubs).
//
// The world is wired with the encounter subscriber unless register=false.
func buildEncounterWorld(t *testing.T, actors []encounterActor, register bool) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, h := mem.NewRepository()

	structures := map[sim.StructureID]*sim.Structure{}
	huddles := map[sim.HuddleID]*sim.Huddle{}
	actorMap := make(map[sim.ActorID]*sim.Actor, len(actors))
	for _, a := range actors {
		actorMap[a.id] = &sim.Actor{
			ID:                a.id,
			DisplayName:       string(a.id),
			Pos:               sim.TilePos{X: a.x, Y: a.y},
			InsideStructureID: a.insideStructureID,
			CurrentHuddleID:   a.currentHuddleID,
		}
		if a.insideStructureID != "" {
			if _, exists := structures[a.insideStructureID]; !exists {
				structures[a.insideStructureID] = &sim.Structure{
					ID:          a.insideStructureID,
					DisplayName: string(a.insideStructureID),
				}
			}
		}
		if a.currentHuddleID != "" {
			hud, exists := huddles[a.currentHuddleID]
			if !exists {
				hud = &sim.Huddle{
					ID:        a.currentHuddleID,
					Members:   map[sim.ActorID]struct{}{},
					StartedAt: time.Now().UTC().Add(-time.Minute),
				}
				huddles[a.currentHuddleID] = hud
			}
			hud.Members[a.id] = struct{}{}
		}
	}
	if len(structures) > 0 {
		h.Structures.Seed(structures)
	}
	if len(huddles) > 0 {
		h.Huddles.Seed(huddles)
	}
	h.Actors.Seed(actorMap)

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)

	if register {
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			cascade.RegisterEncounter(world)
			return nil, nil
		}}); err != nil {
			cancel()
			t.Fatalf("RegisterEncounter: %v", err)
		}
	}
	return w, cancel
}

// emitArrivalFor synthesizes an ActorArrived event matching the actor's
// current position. Runs on the world goroutine via w.Send + EmitForTest
// so subscriber dispatch happens inline.
func emitArrivalFor(t *testing.T, w *sim.World, actorID sim.ActorID, now time.Time) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		actor, ok := world.Actors[actorID]
		if !ok {
			t.Fatalf("emitArrivalFor: actor %q not found", actorID)
		}
		sim.EmitForTest(world, &sim.ActorArrived{
			ActorID:          actorID,
			FinalPosition:    sim.Position{X: actor.Pos.X, Y: actor.Pos.Y},
			FinalStructureID: actor.InsideStructureID,
			At:               now,
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("emitArrivalFor: %v", err)
	}
}

// encounterHuddleState reads the active-huddle topology post-arrival.
// Runs on the world goroutine for a clean read.
type encounterHuddleState struct {
	activeHuddleCount  int
	memberToHuddleIDs  map[sim.ActorID]sim.HuddleID
	huddleMemberCounts map[sim.HuddleID]int
}

func readEncounterHuddleState(t *testing.T, w *sim.World) encounterHuddleState {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		st := encounterHuddleState{
			memberToHuddleIDs:  map[sim.ActorID]sim.HuddleID{},
			huddleMemberCounts: map[sim.HuddleID]int{},
		}
		for id, hud := range world.Huddles {
			if hud.ConcludedAt != nil {
				continue
			}
			st.activeHuddleCount++
			st.huddleMemberCounts[id] = len(hud.Members)
		}
		for id, a := range world.Actors {
			if a.CurrentHuddleID != "" {
				st.memberToHuddleIDs[id] = a.CurrentHuddleID
			}
		}
		return st, nil
	}})
	if err != nil {
		t.Fatalf("readEncounterHuddleState: %v", err)
	}
	return v.(encounterHuddleState)
}

// encounterEventRec captures every event for after-the-fact lookup.
// (Cannot reuse eventRec from commands_move_test.go without exposing
// it; this is a tiny duplicate for the encounter tests.)
type encounterEventRec struct {
	events []sim.Event
}

func (r *encounterEventRec) handle(_ *sim.World, e sim.Event) {
	r.events = append(r.events, e)
}

// huddleJoinedFor returns the recorded HuddleJoined event for actorID,
// or nil. Safe to call from the test goroutine after a synchronous Send.
func (r *encounterEventRec) huddleJoinedFor(actorID sim.ActorID) *sim.HuddleJoined {
	for _, e := range r.events {
		if j, ok := e.(*sim.HuddleJoined); ok && j.ActorID == actorID {
			return j
		}
	}
	return nil
}

// TestArrivalEncounter_SoloArrival covers the no-nearby-actors case:
// arriver is alone in the world, no encounter forms.
func TestArrivalEncounter_SoloArrival(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100},
	}, true)
	defer cancel()

	emitArrivalFor(t, w, "ann", time.Now().UTC())

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 0 {
		t.Errorf("solo arrival minted %d huddles, want 0", st.activeHuddleCount)
	}
	if st.memberToHuddleIDs["ann"] != "" {
		t.Errorf("ann should not be in a huddle after a solo arrival, got %q", st.memberToHuddleIDs["ann"])
	}
}

// TestArrivalEncounter_NearbyOutdoorActor covers the basic happy path:
// arriver lands within radius of a stationary outdoor actor, both
// join one huddle.
func TestArrivalEncounter_NearbyOutdoorActor(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100},
		{id: "ben", x: 101, y: 100}, // one tile east, within default radius 3
	}, true)
	defer cancel()

	rec := &encounterEventRec{}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(rec.handle))
		return nil, nil
	}}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	emitArrivalFor(t, w, "ann", time.Now().UTC())

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 1 {
		t.Fatalf("expected 1 active huddle, got %d", st.activeHuddleCount)
	}
	annHuddle := st.memberToHuddleIDs["ann"]
	benHuddle := st.memberToHuddleIDs["ben"]
	if annHuddle == "" || benHuddle == "" {
		t.Fatalf("both actors should be in the huddle, got ann=%q ben=%q", annHuddle, benHuddle)
	}
	if annHuddle != benHuddle {
		t.Errorf("ann and ben should share a huddle, got %q vs %q", annHuddle, benHuddle)
	}
	if st.huddleMemberCounts[annHuddle] != 2 {
		t.Errorf("huddle should have 2 members, got %d", st.huddleMemberCounts[annHuddle])
	}

	// Arriver-first ordering: ann's HuddleJoined fires before ben's
	// (in StartOutdoorHuddle's per-participant loop), and ben sees ann
	// as an OtherMember (was already in the huddle by the time ben
	// joined).
	annJoined := rec.huddleJoinedFor("ann")
	benJoined := rec.huddleJoinedFor("ben")
	if annJoined == nil || benJoined == nil {
		t.Fatalf("missing HuddleJoined events: ann=%v ben=%v", annJoined, benJoined)
	}
	if annJoined.EventID() >= benJoined.EventID() {
		t.Errorf("ann should join before ben (arriver-first), got ann EventID %d, ben %d",
			annJoined.EventID(), benJoined.EventID())
	}
	if !annJoined.HuddleNew {
		t.Error("ann should have HuddleNew=true (first joiner)")
	}
	if benJoined.HuddleNew {
		t.Error("ben should have HuddleNew=false (joining an existing huddle)")
	}
	if len(benJoined.OtherMembers) != 1 || benJoined.OtherMembers[0] != "ann" {
		t.Errorf("ben.OtherMembers = %v, want [ann]", benJoined.OtherMembers)
	}
}

// TestArrivalEncounter_FarActorExcluded covers radius filtering: an
// outdoor actor outside the radius is not pulled into the huddle.
// With only the arriver eligible, no huddle forms.
func TestArrivalEncounter_FarActorExcluded(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100},
		{id: "far", x: 200, y: 200}, // outside default radius 3
	}, true)
	defer cancel()

	emitArrivalFor(t, w, "ann", time.Now().UTC())

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 0 {
		t.Errorf("expected no huddle (far actor excluded, no other nearby), got %d", st.activeHuddleCount)
	}
}

// TestArrivalEncounter_IndoorActorExcluded covers the outdoor-only
// rule: a nearby actor inside a structure is excluded by
// SceneBound.Contains. The arriver, alone outdoors, mints no huddle.
func TestArrivalEncounter_IndoorActorExcluded(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100},
		{id: "indoor", x: 101, y: 100, insideStructureID: "hut"},
	}, true)
	defer cancel()

	emitArrivalFor(t, w, "ann", time.Now().UTC())

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 0 {
		t.Errorf("expected no huddle (indoor neighbor excluded), got %d", st.activeHuddleCount)
	}
}

// TestArrivalEncounter_IndoorArrivalSkipped covers the arriver-indoor
// case: arrival into a structure does not trigger outdoor encounter
// detection.
func TestArrivalEncounter_IndoorArrivalSkipped(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100, insideStructureID: "hut"},
		{id: "ben", x: 101, y: 100}, // outdoors, would be nearby if ann were outdoors
	}, true)
	defer cancel()

	emitArrivalFor(t, w, "ann", time.Now().UTC())

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 0 {
		t.Errorf("indoor arrival should not mint an outdoor huddle, got %d active",
			st.activeHuddleCount)
	}
	if st.memberToHuddleIDs["ann"] != "" || st.memberToHuddleIDs["ben"] != "" {
		t.Errorf("no one should be in a huddle, ann=%q ben=%q",
			st.memberToHuddleIDs["ann"], st.memberToHuddleIDs["ben"])
	}
}

// TestArrivalEncounter_BusyActorExcluded covers the not-in-huddle gate:
// a nearby outdoor actor who is already in a huddle is excluded.
// Without other eligible nearby actors, no encounter forms.
func TestArrivalEncounter_BusyActorExcluded(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100},
		{id: "busy", x: 101, y: 100, currentHuddleID: "existing-hud"},
	}, true)
	defer cancel()

	emitArrivalFor(t, w, "ann", time.Now().UTC())

	st := readEncounterHuddleState(t, w)
	// 'existing-hud' was seeded as an active huddle containing busy, so
	// it stays counted as active. The new arrival should NOT create a
	// second huddle.
	if st.activeHuddleCount != 1 {
		t.Errorf("expected only the pre-existing huddle (1 active), got %d", st.activeHuddleCount)
	}
	if st.memberToHuddleIDs["ann"] != "" {
		t.Errorf("ann should not have joined the pre-existing huddle, got %q",
			st.memberToHuddleIDs["ann"])
	}
}

// TestArrivalEncounter_MultipleNearbyActors covers multi-actor
// encounters: arriver near several eligible outdoor actors pulls all
// of them into one huddle, with participants ordered [arriver, sorted
// nearby by ActorID].
func TestArrivalEncounter_MultipleNearbyActors(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "carol", x: 100, y: 100}, // arriver — alphabetically NOT first
		{id: "ann", x: 101, y: 100},
		{id: "ben", x: 100, y: 101},
		{id: "far", x: 200, y: 200}, // outside radius
	}, true)
	defer cancel()

	rec := &encounterEventRec{}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(rec.handle))
		return nil, nil
	}}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	emitArrivalFor(t, w, "carol", time.Now().UTC())

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 1 {
		t.Fatalf("expected 1 huddle for the encounter, got %d", st.activeHuddleCount)
	}
	huddleID := st.memberToHuddleIDs["carol"]
	if huddleID == "" {
		t.Fatal("carol not in any huddle")
	}
	if st.memberToHuddleIDs["ann"] != huddleID || st.memberToHuddleIDs["ben"] != huddleID {
		t.Errorf("nearby actors should be in carol's huddle, got ann=%q ben=%q huddle=%q",
			st.memberToHuddleIDs["ann"], st.memberToHuddleIDs["ben"], huddleID)
	}
	if st.memberToHuddleIDs["far"] != "" {
		t.Errorf("far actor should be excluded, got huddle=%q", st.memberToHuddleIDs["far"])
	}
	if st.huddleMemberCounts[huddleID] != 3 {
		t.Errorf("huddle should have 3 members, got %d", st.huddleMemberCounts[huddleID])
	}

	// Ordering: carol (arriver) joins first, then ann, then ben (the
	// nearby pair stable-sorted by ActorID).
	joinedOrder := []sim.ActorID{}
	for _, e := range rec.events {
		if j, ok := e.(*sim.HuddleJoined); ok && j.HuddleID == huddleID {
			joinedOrder = append(joinedOrder, j.ActorID)
		}
	}
	want := []sim.ActorID{"carol", "ann", "ben"}
	if len(joinedOrder) != len(want) {
		t.Fatalf("HuddleJoined order = %v, want %v", joinedOrder, want)
	}
	for i := range want {
		if joinedOrder[i] != want[i] {
			t.Errorf("HuddleJoined[%d] = %q, want %q", i, joinedOrder[i], want[i])
		}
	}
}

// TestArrivalEncounter_StalePositionSkipped covers the position-mismatch
// guard: if the event's FinalPosition disagrees with the arriver's
// current position (e.g. a stale event), the subscriber skips rather
// than minting a huddle anchored at the wrong coordinates.
func TestArrivalEncounter_StalePositionSkipped(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100},
		{id: "ben", x: 101, y: 100},
	}, true)
	defer cancel()

	// Synthesize an ActorArrived with a FinalPosition that doesn't
	// match ann's actual tile.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.EmitForTest(world, &sim.ActorArrived{
			ActorID:       "ann",
			FinalPosition: sim.Position{X: 999, Y: 999},
			At:            time.Now().UTC(),
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("emit stale ActorArrived: %v", err)
	}

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 0 {
		t.Errorf("stale-position arrival should not mint a huddle, got %d active", st.activeHuddleCount)
	}
}

// TestArrivalEncounter_StaleStructureMismatchSkipped covers the
// structure half of the event-freshness invariant: an ActorArrived
// whose FinalPosition matches the arriver's tile but whose
// FinalStructureID disagrees with the arriver's current
// InsideStructureID is rejected. Without the invariant check, an event
// stamped "arrived indoors" against an actor currently outdoors at the
// same coordinates would mint an outdoor huddle from an indoor-arrival
// event.
func TestArrivalEncounter_StaleStructureMismatchSkipped(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100}, // outdoors
		{id: "ben", x: 101, y: 100},
	}, true)
	defer cancel()

	// Synthesize an ActorArrived whose coordinates match ann's tile but
	// whose FinalStructureID says she arrived inside a structure — the
	// stale-event hazard.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.EmitForTest(world, &sim.ActorArrived{
			ActorID:          "ann",
			FinalPosition:    sim.Position{X: 100, Y: 100},
			FinalStructureID: "phantom-structure",
			At:               time.Now().UTC(),
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("emit stale-structure ActorArrived: %v", err)
	}

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 0 {
		t.Errorf("stale-structure arrival should not mint a huddle, got %d active", st.activeHuddleCount)
	}
}

// TestArrivalEncounter_DoubleRegistrationProducesOneHuddle covers the
// idempotency-by-pre-filter property: registering the subscriber twice
// means it dispatches twice per ActorArrived, but the second
// invocation sees the arriver already in the huddle the first
// invocation minted and short-circuits. Net result: one huddle.
func TestArrivalEncounter_DoubleRegistrationProducesOneHuddle(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100},
		{id: "ben", x: 101, y: 100},
	}, true)
	defer cancel()

	// Second registration on the same world.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		cascade.RegisterEncounter(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("second RegisterEncounter: %v", err)
	}

	emitArrivalFor(t, w, "ann", time.Now().UTC())

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 1 {
		t.Errorf("double registration should still mint exactly 1 huddle, got %d", st.activeHuddleCount)
	}
	huddleID := st.memberToHuddleIDs["ann"]
	if huddleID == "" {
		t.Fatal("ann not in any huddle")
	}
	if st.memberToHuddleIDs["ben"] != huddleID {
		t.Errorf("ben should share ann's huddle, got %q vs %q", st.memberToHuddleIDs["ben"], huddleID)
	}
	if st.huddleMemberCounts[huddleID] != 2 {
		t.Errorf("huddle should have 2 members, got %d", st.huddleMemberCounts[huddleID])
	}
}

// TestArrivalEncounter_WalkingActorPulledIn covers the "do not filter on
// MoveIntent" decision: a nearby outdoor actor who is mid-route (still
// has a MoveIntent) is eligible for the encounter and joins the huddle.
// PR 4's bilateral-pause then freezes their locomotion next tick,
// preserving MovementAttemptID for resume.
func TestArrivalEncounter_WalkingActorPulledIn(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100},
		{id: "walker", x: 101, y: 100},
	}, true)
	defer cancel()

	const walkerAttemptID sim.MovementAttemptID = 42
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		walker := world.Actors["walker"]
		walker.MoveIntent = &sim.MoveIntent{
			Destination: sim.NewPositionDestination(sim.Position{X: 500, Y: 500}),
			AttemptID:   walkerAttemptID,
		}
		walker.MoveAttemptCounter = walkerAttemptID
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed MoveIntent: %v", err)
	}

	emitArrivalFor(t, w, "ann", time.Now().UTC())

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 1 {
		t.Fatalf("expected 1 huddle (walker pulled in), got %d", st.activeHuddleCount)
	}
	if st.memberToHuddleIDs["walker"] != st.memberToHuddleIDs["ann"] {
		t.Errorf("walker should share ann's huddle, got walker=%q ann=%q",
			st.memberToHuddleIDs["walker"], st.memberToHuddleIDs["ann"])
	}

	// MoveIntent + MovementAttemptID preserved for resume.
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		walker := world.Actors["walker"]
		if walker.MoveIntent == nil {
			return [2]sim.MovementAttemptID{0, 0}, nil
		}
		return [2]sim.MovementAttemptID{walker.MoveIntent.AttemptID, walker.MoveAttemptCounter}, nil
	}})
	if err != nil {
		t.Fatalf("read walker MoveIntent: %v", err)
	}
	got := v.([2]sim.MovementAttemptID)
	if got[0] != walkerAttemptID {
		t.Errorf("walker MoveIntent.AttemptID = %d, want %d (preserved across huddle pull-in)",
			got[0], walkerAttemptID)
	}
	if got[1] != walkerAttemptID {
		t.Errorf("walker MoveAttemptCounter = %d, want %d (preserved)", got[1], walkerAttemptID)
	}
}

// sortedActorIDsForDebug returns a stable-sorted copy, used by ad-hoc
// debugging if a test fails locally.
//
//lint:ignore U1000 used by ad-hoc debugging when a test fails locally
func sortedActorIDsForDebug(ids []sim.ActorID) []sim.ActorID {
	out := append([]sim.ActorID(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
