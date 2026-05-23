package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/cascade"
)

// moved_encounter_subscriber_test.go — mid-route encounter subscriber
// tests. Lives alongside arrival_encounter_subscriber_test.go so they
// share buildEncounterWorld / readEncounterHuddleState /
// encounterEventRec helpers (defined in the arrival test file, same
// package sim_test).
//
// All tests synthesize ActorMoved via sim.EmitForTest rather than driving
// the locomotion ticker — isolates the subscriber from pathfinding /
// terrain (which don't affect the encounter decision) and keeps the
// world bootstrap small. The locomotion ticker's "ActorMoved followed
// by ActorArrived on final-tile arrival" sequence is tested explicitly
// in TestMovedEncounter_SameTickArrivalRace.

// emitMovedFor synthesizes an ActorMoved event matching the actor's
// current position and structure attribution. Runs on the world
// goroutine via w.Send + EmitForTest so subscriber dispatch happens
// inline. FromPosition / FromStructureID / MovementAttemptID are not
// material to the encounter subscriber (it reads ToPosition,
// ToStructureID, ActorID only) and are left zero.
func emitMovedFor(t *testing.T, w *sim.World, actorID sim.ActorID, now time.Time) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		actor, ok := world.Actors[actorID]
		if !ok {
			t.Fatalf("emitMovedFor: actor %q not found", actorID)
		}
		sim.EmitForTest(world, &sim.ActorMoved{
			ActorID:       actorID,
			ToPosition:    sim.Position{X: actor.Pos.X, Y: actor.Pos.Y},
			ToStructureID: actor.InsideStructureID,
			At:            now,
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("emitMovedFor: %v", err)
	}
}

// TestMovedEncounter_SoloMover covers the no-nearby-actors case: mover
// alone in the world, no encounter forms.
func TestMovedEncounter_SoloMover(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100},
	}, true)
	defer cancel()

	emitMovedFor(t, w, "ann", time.Now().UTC())

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 0 {
		t.Errorf("solo mover minted %d huddles, want 0", st.activeHuddleCount)
	}
	if st.memberToHuddleIDs["ann"] != "" {
		t.Errorf("ann should not be in a huddle after a solo move, got %q", st.memberToHuddleIDs["ann"])
	}
}

// TestMovedEncounter_NearbyOutdoorActor covers the basic happy path:
// mover advances a tile within radius of a stationary outdoor actor,
// both join one huddle. Mover-first ordering: mover joins before the
// nearby actor in the HuddleJoined sequence.
func TestMovedEncounter_NearbyOutdoorActor(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100}, // mover
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

	emitMovedFor(t, w, "ann", time.Now().UTC())

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

	// Mover-first ordering.
	annJoined := rec.huddleJoinedFor("ann")
	benJoined := rec.huddleJoinedFor("ben")
	if annJoined == nil || benJoined == nil {
		t.Fatalf("missing HuddleJoined events: ann=%v ben=%v", annJoined, benJoined)
	}
	if annJoined.EventID() >= benJoined.EventID() {
		t.Errorf("ann should join before ben (mover-first), got ann EventID %d, ben %d",
			annJoined.EventID(), benJoined.EventID())
	}
	if !annJoined.HuddleNew {
		t.Error("ann should have HuddleNew=true (first joiner)")
	}
	if benJoined.HuddleNew {
		t.Error("ben should have HuddleNew=false (joining an existing huddle)")
	}
}

// TestMovedEncounter_FarActorExcluded covers radius filtering: an
// outdoor actor outside the radius is not pulled into a huddle.
func TestMovedEncounter_FarActorExcluded(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100},
		{id: "far", x: 200, y: 200}, // outside default radius 3
	}, true)
	defer cancel()

	emitMovedFor(t, w, "ann", time.Now().UTC())

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 0 {
		t.Errorf("expected no huddle (far actor excluded), got %d", st.activeHuddleCount)
	}
}

// TestMovedEncounter_IndoorNeighborExcluded covers the outdoor-only
// rule: a nearby actor inside a structure is excluded by both the
// outdoorActors index (the new optimization) and SceneBound.Contains
// (the underlying semantic guard).
func TestMovedEncounter_IndoorNeighborExcluded(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100},
		{id: "indoor", x: 101, y: 100, insideStructureID: "hut"},
	}, true)
	defer cancel()

	emitMovedFor(t, w, "ann", time.Now().UTC())

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 0 {
		t.Errorf("expected no huddle (indoor neighbor excluded), got %d", st.activeHuddleCount)
	}
}

// TestMovedEncounter_IndoorMoverSkipped covers the mover-indoor case:
// a move that lands the actor on a structure tile does not trigger
// outdoor encounter detection. (Arrival into that structure is the
// arrival subscriber's domain.)
func TestMovedEncounter_IndoorMoverSkipped(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100, insideStructureID: "hut"},
		{id: "ben", x: 101, y: 100}, // outdoors, would be nearby if ann were outdoors
	}, true)
	defer cancel()

	emitMovedFor(t, w, "ann", time.Now().UTC())

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 0 {
		t.Errorf("indoor move should not mint an outdoor huddle, got %d active", st.activeHuddleCount)
	}
	if st.memberToHuddleIDs["ann"] != "" || st.memberToHuddleIDs["ben"] != "" {
		t.Errorf("no one should be in a huddle, ann=%q ben=%q",
			st.memberToHuddleIDs["ann"], st.memberToHuddleIDs["ben"])
	}
}

// TestMovedEncounter_BusyActorExcluded covers the not-in-huddle gate
// on the neighbor side: a nearby outdoor actor already in a huddle is
// excluded from a new encounter forming.
func TestMovedEncounter_BusyActorExcluded(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100},
		{id: "busy", x: 101, y: 100, currentHuddleID: "existing-hud"},
	}, true)
	defer cancel()

	emitMovedFor(t, w, "ann", time.Now().UTC())

	st := readEncounterHuddleState(t, w)
	// existing-hud is pre-seeded and stays active; the move should NOT
	// mint a second huddle.
	if st.activeHuddleCount != 1 {
		t.Errorf("expected only the pre-existing huddle (1 active), got %d", st.activeHuddleCount)
	}
	if st.memberToHuddleIDs["ann"] != "" {
		t.Errorf("ann should not have joined the pre-existing huddle, got %q",
			st.memberToHuddleIDs["ann"])
	}
}

// TestMovedEncounter_MoverInHuddleSkipped covers the mover-side
// not-in-huddle gate: a moving actor already in a huddle (drift /
// bilateral-pause edge case) does not trigger new encounter detection.
func TestMovedEncounter_MoverInHuddleSkipped(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100, currentHuddleID: "existing-hud"},
		{id: "ben", x: 101, y: 100},
	}, true)
	defer cancel()

	emitMovedFor(t, w, "ann", time.Now().UTC())

	st := readEncounterHuddleState(t, w)
	// existing-hud stays active (1); no second huddle for ann+ben.
	if st.activeHuddleCount != 1 {
		t.Errorf("expected only the pre-existing huddle, got %d active", st.activeHuddleCount)
	}
	if st.memberToHuddleIDs["ben"] != "" {
		t.Errorf("ben should not have been pulled in, got huddle=%q", st.memberToHuddleIDs["ben"])
	}
}

// TestMovedEncounter_MultipleNearbyActors covers multi-actor
// encounters: mover near several eligible outdoor actors pulls all of
// them into one huddle, with participants ordered [mover, sorted
// nearby by ActorID].
func TestMovedEncounter_MultipleNearbyActors(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "carol", x: 100, y: 100}, // mover — alphabetically NOT first
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

	emitMovedFor(t, w, "carol", time.Now().UTC())

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 1 {
		t.Fatalf("expected 1 huddle, got %d", st.activeHuddleCount)
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

	// Mover-first, then nearby sorted by ActorID: [carol, ann, ben].
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

// TestMovedEncounter_StalePositionSkipped covers the position half of
// the event-freshness invariant: if the event's ToPosition disagrees
// with the mover's current tile, the subscriber skips rather than
// minting a huddle anchored at the wrong coordinates.
func TestMovedEncounter_StalePositionSkipped(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100},
		{id: "ben", x: 101, y: 100},
	}, true)
	defer cancel()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.EmitForTest(world, &sim.ActorMoved{
			ActorID:    "ann",
			ToPosition: sim.Position{X: 999, Y: 999}, // doesn't match ann's tile
			At:         time.Now().UTC(),
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("emit stale ActorMoved: %v", err)
	}

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 0 {
		t.Errorf("stale-position move should not mint a huddle, got %d active", st.activeHuddleCount)
	}
}

// TestMovedEncounter_StaleStructureMismatchSkipped covers the structure
// half of the event-freshness invariant: an ActorMoved whose ToPosition
// matches the mover's tile but whose ToStructureID disagrees with the
// mover's current InsideStructureID is rejected. Without the invariant
// check, a "moved to (X,Y) indoors" event against an actor currently
// outdoors at the same coordinates would still flow into the rest of
// the pre-filter where InsideStructureID == "" passes, opening a window
// for inconsistent state.
func TestMovedEncounter_StaleStructureMismatchSkipped(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100}, // outdoors
		{id: "ben", x: 101, y: 100},
	}, true)
	defer cancel()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.EmitForTest(world, &sim.ActorMoved{
			ActorID:       "ann",
			ToPosition:    sim.Position{X: 100, Y: 100},
			ToStructureID: "phantom-structure",
			At:            time.Now().UTC(),
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("emit stale-structure ActorMoved: %v", err)
	}

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 0 {
		t.Errorf("stale-structure move should not mint a huddle, got %d active", st.activeHuddleCount)
	}
}

// TestMovedEncounter_SameTickArrivalRace covers the critical
// interaction between handleMovedEncounter and handleArrivalEncounter
// on a final-tile arrival, where locomotion_ticker.go emits ActorMoved
// first and then (still same tick) ActorArrived via finishArrival.
//
// Expected: ActorMoved fires first, handleMovedEncounter mints the
// huddle; ActorArrived fires second, handleArrivalEncounter's
// pre-filter ("arriver.CurrentHuddleID != "" → return") short-circuits
// — the huddle is minted exactly once.
func TestMovedEncounter_SameTickArrivalRace(t *testing.T) {
	w, cancel := buildEncounterWorld(t, []encounterActor{
		{id: "ann", x: 100, y: 100}, // arriving mover
		{id: "ben", x: 101, y: 100},
	}, true)
	defer cancel()

	rec := &encounterEventRec{}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(rec.handle))
		return nil, nil
	}}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Same-tick: both events emitted from inside one Command (mirrors
	// the ticker's actor.Pos.X advance → emit(ActorMoved) → emit(
	// ActorArrived) sequence).
	now := time.Now().UTC()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		actor := world.Actors["ann"]
		pos := sim.Position{X: actor.Pos.X, Y: actor.Pos.Y}
		sim.EmitForTest(world, &sim.ActorMoved{
			ActorID:    "ann",
			ToPosition: pos,
			At:         now,
		})
		sim.EmitForTest(world, &sim.ActorArrived{
			ActorID:       "ann",
			FinalPosition: pos,
			At:            now,
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("same-tick emit: %v", err)
	}

	st := readEncounterHuddleState(t, w)
	if st.activeHuddleCount != 1 {
		t.Fatalf("same-tick ActorMoved+ActorArrived should mint exactly 1 huddle, got %d",
			st.activeHuddleCount)
	}
	huddleID := st.memberToHuddleIDs["ann"]
	if huddleID == "" {
		t.Fatal("ann not in any huddle")
	}
	if st.memberToHuddleIDs["ben"] != huddleID {
		t.Errorf("ben should share ann's huddle, got %q vs %q",
			st.memberToHuddleIDs["ben"], huddleID)
	}
	if st.huddleMemberCounts[huddleID] != 2 {
		t.Errorf("huddle should have 2 members, got %d", st.huddleMemberCounts[huddleID])
	}

	// Exactly two HuddleJoined events in this huddle (ann + ben). A
	// regression where the arrival subscriber didn't pre-filter would
	// either fail StartOutdoorHuddle (already-in-huddle) or produce a
	// second huddle — both would show up here as either an extra
	// HuddleJoined for ann or a non-zero activeHuddleCount delta above.
	joinedCount := 0
	for _, e := range rec.events {
		if j, ok := e.(*sim.HuddleJoined); ok && j.HuddleID == huddleID {
			joinedCount++
		}
	}
	if joinedCount != 2 {
		t.Errorf("HuddleJoined count for huddle %q = %d, want 2", huddleID, joinedCount)
	}
}

// TestMovedEncounter_DoubleRegistrationProducesOneHuddle covers the
// idempotency-by-pre-filter property: registering the subscriber pair
// twice means handleMovedEncounter dispatches twice per ActorMoved, but
// the second invocation sees the mover already in the huddle the first
// invocation minted and short-circuits. Net: one huddle.
func TestMovedEncounter_DoubleRegistrationProducesOneHuddle(t *testing.T) {
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

	emitMovedFor(t, w, "ann", time.Now().UTC())

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
