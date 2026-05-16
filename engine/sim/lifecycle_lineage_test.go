package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// lifecycle_lineage_test.go — PR 3e retrofit coverage.
//
// PR 3a's zero-lineage invariant: a warrant either carries FULL event
// lineage (SourceEventID != 0, with the other source fields populated
// from that event) or NONE. PR 3 retrofits the synchronous lifecycle
// stamp callsites (StartOutdoorHuddle, JoinHuddle, leaveCurrentHuddle,
// ConcludeHuddle, finishArrival) from "stamp-without-lineage" to "emit
// first, stamp from the resulting event."
//
// These tests pin the retrofit: every warrant stamped by these callsites
// carries SourceEventID != 0 and the right per-event identity, and
// per-event dedup keys are distinguishable.

// readWarrants returns a copy of the actor's Warrants list. Runs as a
// Command so it observes the post-mutation world goroutine state
// directly (not via a snapshot, whose publication timing is independent
// of Send).
func readWarrants(t *testing.T, w *sim.World, id sim.ActorID) []sim.WarrantMeta {
	t.Helper()
	v, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a, ok := world.Actors[id]
			if !ok {
				return nil, nil
			}
			out := make([]sim.WarrantMeta, len(a.Warrants))
			copy(out, a.Warrants)
			return out, nil
		},
	})
	if err != nil {
		t.Fatalf("readWarrants(%q): %v", id, err)
	}
	if v == nil {
		return nil
	}
	return v.([]sim.WarrantMeta)
}

// findWarrant returns the first warrant in list matching kind, or nil.
func findWarrant(list []sim.WarrantMeta, kind sim.WarrantKind) *sim.WarrantMeta {
	for i := range list {
		if list[i].Kind() == kind {
			return &list[i]
		}
	}
	return nil
}

// huddleJoinedFor returns the recorded HuddleJoined event for actorID,
// or nil if none. Held-mu read; safe after a synchronous Send.
func huddleJoinedFor(rec *eventRec, actorID sim.ActorID) *sim.HuddleJoined {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, e := range rec.events {
		j, ok := e.(*sim.HuddleJoined)
		if !ok {
			continue
		}
		if j.ActorID == actorID {
			return j
		}
	}
	return nil
}

// huddleLeftFor returns the recorded HuddleLeft event for actorID, or nil.
func huddleLeftFor(rec *eventRec, actorID sim.ActorID) *sim.HuddleLeft {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, e := range rec.events {
		l, ok := e.(*sim.HuddleLeft)
		if !ok {
			continue
		}
		if l.ActorID == actorID {
			return l
		}
	}
	return nil
}

// firstHuddleConcluded returns the first recorded HuddleConcluded event.
func firstHuddleConcluded(rec *eventRec) *sim.HuddleConcluded {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, e := range rec.events {
		if c, ok := e.(*sim.HuddleConcluded); ok {
			return c
		}
	}
	return nil
}

// firstActorArrivedFor returns the first recorded ActorArrived event for
// the given actor, or nil.
func firstActorArrivedFor(rec *eventRec, actorID sim.ActorID) *sim.ActorArrived {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, e := range rec.events {
		a, ok := e.(*sim.ActorArrived)
		if !ok {
			continue
		}
		if a.ActorID == actorID {
			return a
		}
	}
	return nil
}

// assertFullLineage fails the test if the warrant doesn't carry every
// source-lineage field PR 3a requires. PR S4 decoupled lineage from
// dedup-participation: lifecycle warrants (BasicWarrantReason kind) carry
// full lineage for perception/debug but their Reason returns 0 from
// DedupDiscriminator, so they no longer participate in the in-flight /
// recently-consumed dedup paths. That's correct in practice — lifecycle
// stamps are 1:1 with their source events, so dedup never had anything
// to suppress. Hence no eventSourced() check here.
func assertFullLineage(t *testing.T, label string, m *sim.WarrantMeta, expectedKind sim.WarrantKind) {
	t.Helper()
	if m == nil {
		t.Fatalf("%s: warrant missing", label)
	}
	if m.Kind() != expectedKind {
		t.Errorf("%s: warrant kind = %q, want %q", label, m.Kind(), expectedKind)
	}
	if m.SourceEventID == 0 {
		t.Errorf("%s: SourceEventID == 0 (PR 3a zero-lineage invariant violated)", label)
	}
	if m.RootEventID == 0 {
		t.Errorf("%s: RootEventID == 0", label)
	}
}

// TestLifecycleLineage_FinishArrival covers the locomotion_ticker.go
// retrofit: arriving at a destination stamps an ArrivalWarrantReason
// warrant whose SourceEventID matches the emitted ActorArrived event.
func TestLifecycleLineage_FinishArrival(t *testing.T) {
	w, cancel, rec := buildMoveTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	// "walker" sits at the pad origin; move one tile east. With all-grass
	// terrain the locomotion ticker will advance and arrive in one step.
	dest := sim.NewPositionDestination(sim.Position{X: sim.PadX + 1, Y: sim.PadY})
	if _, err := w.Send(sim.MoveActor("walker", dest, false, now)); err != nil {
		t.Fatalf("MoveActor: %v", err)
	}
	if _, err := w.Send(sim.EvaluateLocomotion(now.Add(time.Second))); err != nil {
		t.Fatalf("EvaluateLocomotion: %v", err)
	}

	arrived := firstActorArrivedFor(rec, "walker")
	if arrived == nil {
		t.Fatal("no ActorArrived event for walker")
	}
	if arrived.EventID() == 0 {
		t.Fatal("ActorArrived was not stamped with an EventID")
	}

	warrants := readWarrants(t, w, "walker")
	arrivalWarrant := findWarrant(warrants, sim.WarrantKindArrived)
	assertFullLineage(t, "walker arrival warrant", arrivalWarrant, sim.WarrantKindArrived)
	if arrivalWarrant.SourceEventID != arrived.EventID() {
		t.Errorf("arrival warrant SourceEventID = %d, want ActorArrived EventID %d",
			arrivalWarrant.SourceEventID, arrived.EventID())
	}
	if arrivalWarrant.RootEventID != arrived.RootEventID() {
		t.Errorf("arrival warrant RootEventID = %d, want ActorArrived RootEventID %d",
			arrivalWarrant.RootEventID, arrived.RootEventID())
	}
	if arrivalWarrant.SourceActorID != "walker" {
		t.Errorf("arrival warrant SourceActorID = %q, want walker", arrivalWarrant.SourceActorID)
	}
	if arrivalWarrant.TriggerActorID != "walker" {
		t.Errorf("arrival warrant TriggerActorID = %q, want walker", arrivalWarrant.TriggerActorID)
	}
}

// TestLifecycleLineage_StartOutdoorHuddle covers the commands_move.go
// retrofit: each participant in StartOutdoorHuddle gets a warrant whose
// SourceEventID is THAT participant's HuddleJoined event (distinct
// SourceEventIDs across participants), with a shared RootEventID.
func TestLifecycleLineage_StartOutdoorHuddle(t *testing.T) {
	w, cancel, rec := buildOutdoorTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	res, err := w.Send(sim.StartOutdoorHuddle([]sim.ActorID{"ann", "ben"}, outdoorAnchor, 3, nil, now))
	if err != nil {
		t.Fatalf("StartOutdoorHuddle: %v", err)
	}
	huddleID := res.(sim.StartOutdoorHuddleResult).HuddleID

	annJoined := huddleJoinedFor(rec, "ann")
	benJoined := huddleJoinedFor(rec, "ben")
	if annJoined == nil || benJoined == nil {
		t.Fatalf("missing HuddleJoined events: ann=%v ben=%v", annJoined, benJoined)
	}
	if annJoined.EventID() == benJoined.EventID() {
		t.Errorf("ann and ben HuddleJoined should have distinct EventIDs, both = %d", annJoined.EventID())
	}
	// NB: when StartOutdoorHuddle runs as a top-level command (no
	// ambient cascade root) — as in this test — each HuddleJoined emit
	// becomes its own causal root (world.go emit fresh-origin path). The
	// shared-root behavior the spec describes ("RootEventID inherits
	// from the original movement/arrival event") applies when
	// StartOutdoorHuddle.Fn runs INSIDE a subscriber dispatch (the
	// encounter subscriber path) — verified by the encounter subscriber
	// tests, not here.

	annWarrants := readWarrants(t, w, "ann")
	annWarrant := findWarrant(annWarrants, sim.WarrantKindHuddleJoined)
	assertFullLineage(t, "ann huddle-joined warrant", annWarrant, sim.WarrantKindHuddleJoined)
	if annWarrant.SourceEventID != annJoined.EventID() {
		t.Errorf("ann warrant SourceEventID = %d, want ann HuddleJoined EventID %d",
			annWarrant.SourceEventID, annJoined.EventID())
	}
	if annWarrant.HuddleID != huddleID {
		t.Errorf("ann warrant HuddleID = %q, want %q", annWarrant.HuddleID, huddleID)
	}
	if annWarrant.SourceActorID != "ann" {
		t.Errorf("ann warrant SourceActorID = %q, want ann", annWarrant.SourceActorID)
	}

	benWarrants := readWarrants(t, w, "ben")
	benWarrant := findWarrant(benWarrants, sim.WarrantKindHuddleJoined)
	assertFullLineage(t, "ben huddle-joined warrant", benWarrant, sim.WarrantKindHuddleJoined)
	if benWarrant.SourceEventID != benJoined.EventID() {
		t.Errorf("ben warrant SourceEventID = %d, want ben HuddleJoined EventID %d",
			benWarrant.SourceEventID, benJoined.EventID())
	}

	// Distinct SourceEventIDs across participants — PR 3a dedup precision
	// would collapse if both participants reused the same source ID.
	if annWarrant.SourceEventID == benWarrant.SourceEventID {
		t.Errorf("ann and ben warrants share SourceEventID %d; dedup precision lost",
			annWarrant.SourceEventID)
	}
}

// TestLifecycleLineage_JoinHuddle covers the huddle_commands.go
// retrofit: the joiner's HuddleJoined warrant and every prior member's
// HuddlePeerJoined warrant share the same SourceEventID (the joiner's
// HuddleJoined event), with the joiner as SourceActorID throughout.
func TestLifecycleLineage_JoinHuddle(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	if _, err := w.Send(sim.JoinHuddle("alice", "tavern", "", now)); err != nil {
		t.Fatalf("alice JoinHuddle: %v", err)
	}

	// Subscribe AFTER alice's join so we only capture events from bob's
	// join (avoids matching alice's HuddleJoined when looking up bob's).
	rec := &eventRec{}
	w.Subscribe(sim.SubscriberFunc(rec.handle))

	later := now.Add(time.Second)
	if _, err := w.Send(sim.JoinHuddle("bob", "tavern", "", later)); err != nil {
		t.Fatalf("bob JoinHuddle: %v", err)
	}

	bobJoined := huddleJoinedFor(rec, "bob")
	if bobJoined == nil {
		t.Fatal("no HuddleJoined event for bob")
	}

	bobWarrants := readWarrants(t, w, "bob")
	bobWarrant := findWarrant(bobWarrants, sim.WarrantKindHuddleJoined)
	assertFullLineage(t, "bob huddle-joined warrant", bobWarrant, sim.WarrantKindHuddleJoined)
	if bobWarrant.SourceEventID != bobJoined.EventID() {
		t.Errorf("bob warrant SourceEventID = %d, want bob HuddleJoined EventID %d",
			bobWarrant.SourceEventID, bobJoined.EventID())
	}

	aliceWarrants := readWarrants(t, w, "alice")
	alicePeerWarrant := findWarrant(aliceWarrants, sim.WarrantKindHuddlePeerJoined)
	assertFullLineage(t, "alice peer-joined warrant", alicePeerWarrant, sim.WarrantKindHuddlePeerJoined)
	if alicePeerWarrant.SourceEventID != bobJoined.EventID() {
		t.Errorf("alice peer-joined warrant SourceEventID = %d, want bob HuddleJoined EventID %d",
			alicePeerWarrant.SourceEventID, bobJoined.EventID())
	}
	if alicePeerWarrant.SourceActorID != "bob" {
		t.Errorf("alice peer-joined warrant SourceActorID = %q, want bob", alicePeerWarrant.SourceActorID)
	}
	if alicePeerWarrant.TriggerActorID != "bob" {
		t.Errorf("alice peer-joined warrant TriggerActorID = %q, want bob", alicePeerWarrant.TriggerActorID)
	}
}

// TestLifecycleLineage_LeaveHuddle covers the leaveCurrentHuddle retrofit:
// the leaver's HuddleLeft warrant and every remaining member's
// HuddlePeerLeft warrant share the same SourceEventID (the emitted
// HuddleLeft event), with the leaver as SourceActorID.
func TestLifecycleLineage_LeaveHuddle(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	if _, err := w.Send(sim.JoinHuddle("alice", "tavern", "", now)); err != nil {
		t.Fatalf("alice JoinHuddle: %v", err)
	}
	if _, err := w.Send(sim.JoinHuddle("bob", "tavern", "", now)); err != nil {
		t.Fatalf("bob JoinHuddle: %v", err)
	}

	rec := &eventRec{}
	w.Subscribe(sim.SubscriberFunc(rec.handle))

	later := now.Add(time.Minute)
	if _, err := w.Send(sim.LeaveHuddle("alice", later)); err != nil {
		t.Fatalf("alice LeaveHuddle: %v", err)
	}

	left := huddleLeftFor(rec, "alice")
	if left == nil {
		t.Fatal("no HuddleLeft event for alice")
	}

	aliceWarrants := readWarrants(t, w, "alice")
	aliceLeftWarrant := findWarrant(aliceWarrants, sim.WarrantKindHuddleLeft)
	assertFullLineage(t, "alice huddle-left warrant", aliceLeftWarrant, sim.WarrantKindHuddleLeft)
	if aliceLeftWarrant.SourceEventID != left.EventID() {
		t.Errorf("alice left warrant SourceEventID = %d, want HuddleLeft EventID %d",
			aliceLeftWarrant.SourceEventID, left.EventID())
	}

	bobWarrants := readWarrants(t, w, "bob")
	bobPeerLeftWarrant := findWarrant(bobWarrants, sim.WarrantKindHuddlePeerLeft)
	assertFullLineage(t, "bob peer-left warrant", bobPeerLeftWarrant, sim.WarrantKindHuddlePeerLeft)
	if bobPeerLeftWarrant.SourceEventID != left.EventID() {
		t.Errorf("bob peer-left warrant SourceEventID = %d, want HuddleLeft EventID %d",
			bobPeerLeftWarrant.SourceEventID, left.EventID())
	}
	if bobPeerLeftWarrant.SourceActorID != "alice" {
		t.Errorf("bob peer-left warrant SourceActorID = %q, want alice", bobPeerLeftWarrant.SourceActorID)
	}
}

// TestLifecycleLineage_ConcludeHuddle covers the ConcludeHuddle retrofit:
// every evicted member gets a HuddleConcluded warrant whose
// SourceEventID matches the (single) emitted HuddleConcluded event.
// No TriggerActorID / SourceActorID — bulk eviction, no single trigger.
func TestLifecycleLineage_ConcludeHuddle(t *testing.T) {
	w, cancel := buildHuddleTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	if _, err := w.Send(sim.JoinHuddle("alice", "tavern", "", now)); err != nil {
		t.Fatalf("alice JoinHuddle: %v", err)
	}
	res, err := w.Send(sim.JoinHuddle("bob", "tavern", "", now))
	if err != nil {
		t.Fatalf("bob JoinHuddle: %v", err)
	}
	huddleID := res.(sim.JoinHuddleResult).HuddleID

	rec := &eventRec{}
	w.Subscribe(sim.SubscriberFunc(rec.handle))

	later := now.Add(time.Minute)
	if _, err := w.Send(sim.ConcludeHuddle(huddleID, later)); err != nil {
		t.Fatalf("ConcludeHuddle: %v", err)
	}

	concluded := firstHuddleConcluded(rec)
	if concluded == nil {
		t.Fatal("no HuddleConcluded event")
	}

	for _, id := range []sim.ActorID{"alice", "bob"} {
		warrants := readWarrants(t, w, id)
		warrant := findWarrant(warrants, sim.WarrantKindHuddleConcluded)
		assertFullLineage(t, string(id)+" huddle-concluded warrant", warrant, sim.WarrantKindHuddleConcluded)
		if warrant.SourceEventID != concluded.EventID() {
			t.Errorf("%s concluded warrant SourceEventID = %d, want HuddleConcluded EventID %d",
				id, warrant.SourceEventID, concluded.EventID())
		}
		if warrant.TriggerActorID != "" {
			t.Errorf("%s concluded warrant TriggerActorID = %q, want empty (bulk)", id, warrant.TriggerActorID)
		}
		if warrant.SourceActorID != "" {
			t.Errorf("%s concluded warrant SourceActorID = %q, want empty (bulk)", id, warrant.SourceActorID)
		}
		if warrant.HuddleID != huddleID {
			t.Errorf("%s concluded warrant HuddleID = %q, want %q", id, warrant.HuddleID, huddleID)
		}
	}
}
