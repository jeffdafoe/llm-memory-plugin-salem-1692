package sim_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// move_to_test.go — sim-level coverage of the sim.MoveToStructure Command
// (ZBBS-HOME-285): the enter-vs-visit derivation, the structure-exists /
// already-there / already-walking rejects, supersede-on-change, and the
// huddle-leave on move. Handler-package static validation (decode + the
// terminal registration policy) lives in handlers/move_to_test.go.
//
// Reuses buildMoveTestWorld (commands_move_test.go) for the open/closed/
// doorless structures; buildMoveToOwnerTestWorld below adds an owner-only
// structure to exercise the membership leg of the enter derivation.

// destKindOf walks an actor's current MoveIntent and returns its destination
// kind + structure id. A nil intent yields ("", "").
func destKindOf(t *testing.T, w *sim.World, id sim.ActorID) (sim.MoveDestinationKind, sim.StructureID) {
	t.Helper()
	mi := moveIntentOf(t, w, id)
	if mi == nil {
		return "", ""
	}
	sid := sim.StructureID("")
	if mi.Destination.StructureID != nil {
		sid = *mi.Destination.StructureID
	}
	return mi.Destination.Kind, sid
}

// --- enter-vs-visit derivation ----------------------------------------

func TestMoveToStructure_EntersOpenStructureWithDoor(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.MoveToStructure("walker", "inn", now)); err != nil {
		t.Fatalf("MoveToStructure(inn): %v", err)
	}
	kind, sid := destKindOf(t, w, "walker")
	if kind != sim.MoveDestinationStructureEnter {
		t.Errorf("dest kind = %q, want structure_enter (open structure with a door)", kind)
	}
	if sid != "inn" {
		t.Errorf("dest structure = %q, want inn", sid)
	}
}

func TestMoveToStructure_VisitsClosedStructure(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.MoveToStructure("walker", "well", now)); err != nil {
		t.Fatalf("MoveToStructure(well): %v", err)
	}
	kind, _ := destKindOf(t, w, "walker")
	if kind != sim.MoveDestinationStructureVisit {
		t.Errorf("dest kind = %q, want structure_visit (closed structure)", kind)
	}
}

func TestMoveToStructure_VisitsDoorlessStructure(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// gazebo is EntryPolicyOpen but has no door offset — not enterable, so the
	// derivation must fall back to a visit (distinct from well's closed-policy
	// path).
	if _, err := w.Send(sim.MoveToStructure("walker", "gazebo", now)); err != nil {
		t.Fatalf("MoveToStructure(gazebo): %v", err)
	}
	kind, _ := destKindOf(t, w, "walker")
	if kind != sim.MoveDestinationStructureVisit {
		t.Errorf("dest kind = %q, want structure_visit (open but doorless)", kind)
	}
}

// buildMoveToOwnerTestWorld seeds a world with a single owner-only structure
// "manor" (house asset, has a door) owned by "lord", plus three actors at the
// pad with a clear path: "lord" (owner), "resident" (home == manor), and
// "stranger" (no association). Used to exercise the owner-only leg of the
// enter derivation — enter for a member, visit for a non-member.
func buildMoveToOwnerTestWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"house": {ID: "house", Category: "structure", DoorOffsetX: intp(0), DoorOffsetY: intp(2)},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"manor": {ID: "manor", AssetID: "house", X: 320, Y: 320, EntryPolicy: sim.EntryPolicyOwner, OwnerActorID: "lord"},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"manor": {ID: "manor", DisplayName: "Manor"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"lord":     {ID: "lord", DisplayName: "Lord", CurrentX: sim.PadX, CurrentY: sim.PadY},
		"resident": {ID: "resident", DisplayName: "Resident", CurrentX: sim.PadX, CurrentY: sim.PadY, HomeStructureID: "manor"},
		"stranger": {ID: "stranger", DisplayName: "Stranger", CurrentX: sim.PadX, CurrentY: sim.PadY},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

func TestMoveToStructure_OwnerPolicyEntersForMemberVisitsForStranger(t *testing.T) {
	now := time.Now().UTC()

	cases := []struct {
		actor sim.ActorID
		want  sim.MoveDestinationKind
		why   string
	}{
		{"lord", sim.MoveDestinationStructureEnter, "owner (OwnerActorID) enters its owner-only manor"},
		{"resident", sim.MoveDestinationStructureEnter, "resident (home == manor) enters its owner-only manor"},
		{"stranger", sim.MoveDestinationStructureVisit, "non-member stands outside an owner-only manor"},
	}
	for _, tc := range cases {
		t.Run(string(tc.actor), func(t *testing.T) {
			w, cancel := buildMoveToOwnerTestWorld(t)
			defer cancel()
			if _, err := w.Send(sim.MoveToStructure(tc.actor, "manor", now)); err != nil {
				t.Fatalf("MoveToStructure(manor) for %s: %v", tc.actor, err)
			}
			kind, _ := destKindOf(t, w, tc.actor)
			if kind != tc.want {
				t.Errorf("dest kind = %q, want %q — %s", kind, tc.want, tc.why)
			}
		})
	}
}

// --- rejects ----------------------------------------------------------

func TestMoveToStructure_RejectsUnknownStructure(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	_, err := w.Send(sim.MoveToStructure("walker", "nowhere", now))
	if err == nil {
		t.Fatal("want error for unknown structure, got nil")
	}
	if !strings.Contains(err.Error(), "no structure") {
		t.Errorf("error lacks 'no structure': %v", err)
	}
	if mi := moveIntentOf(t, w, "walker"); mi != nil {
		t.Error("rejected move stamped a MoveIntent; want none")
	}
}

func TestMoveToStructure_RejectsStructureWithoutPlacement(t *testing.T) {
	w, cancel := buildMoveToOwnerTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Simulate a desynced world: a structure record whose backing
	// village-object placement is gone. move_to must reject crisply rather
	// than derive a visit that MoveActor can't resolve to a tile.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.VillageObjects, sim.VillageObjectID("manor"))
		return nil, nil
	}}); err != nil {
		t.Fatalf("drop placement: %v", err)
	}
	_, err := w.Send(sim.MoveToStructure("lord", "manor", now))
	if err == nil {
		t.Fatal("want error for a structure with no village-object placement, got nil")
	}
	if !strings.Contains(err.Error(), "no placement") {
		t.Errorf("error lacks 'no placement': %v", err)
	}
	if mi := moveIntentOf(t, w, "lord"); mi != nil {
		t.Error("rejected move stamped a MoveIntent; want none")
	}
}

func TestMoveToStructure_RejectsAlreadyInside(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Park the walker inside the inn, then ask to walk to the inn — a no-op.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["walker"].InsideStructureID = "inn"
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed InsideStructureID: %v", err)
	}
	_, err := w.Send(sim.MoveToStructure("walker", "inn", now))
	if err == nil {
		t.Fatal("want error for move_to a structure the actor is already inside, got nil")
	}
	if !strings.Contains(err.Error(), "already at") {
		t.Errorf("error lacks 'already at': %v", err)
	}
}

func TestMoveToStructure_RejectsAlreadyWalkingSameDest(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.MoveToStructure("walker", "inn", now)); err != nil {
		t.Fatalf("first MoveToStructure(inn): %v", err)
	}
	// Re-issuing the SAME in-flight destination is a no-op reject (no
	// re-stamp). The test world runs no locomotion ticker, so the walker is
	// still mid-intent toward the inn here.
	_, err := w.Send(sim.MoveToStructure("walker", "inn", now))
	if err == nil {
		t.Fatal("want error for re-issuing the same in-flight destination, got nil")
	}
	if !strings.Contains(err.Error(), "already on your way") {
		t.Errorf("error lacks 'already on your way': %v", err)
	}
}

func TestMoveToStructure_SupersedesOnDifferentDest(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.MoveToStructure("walker", "inn", now)); err != nil {
		t.Fatalf("MoveToStructure(inn): %v", err)
	}
	// Changing destination mid-walk is allowed — MoveActor supersedes the
	// in-flight intent silently.
	if _, err := w.Send(sim.MoveToStructure("walker", "well", now)); err != nil {
		t.Fatalf("MoveToStructure(well) after inn: %v", err)
	}
	_, sid := destKindOf(t, w, "walker")
	if sid != "well" {
		t.Errorf("dest structure = %q, want well (the superseding destination)", sid)
	}
}

// --- huddle-leave on move ---------------------------------------------

func TestMoveToStructure_LeavesHuddleOnMove(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.JoinHuddle("walker", "inn", "", now)); err != nil {
		t.Fatalf("JoinHuddle: %v", err)
	}
	if huddleIDOf(t, w, "walker") == "" {
		t.Fatal("walker not in a huddle after JoinHuddle")
	}

	// move_to passes leaveHuddleFirst=true, so a move while huddled succeeds
	// (rather than the LeaveHuddleFirst rejection MoveActor gives a bare move)
	// and leaves the huddle. Walk to the well so the destination differs from
	// the huddle's structure.
	if _, err := w.Send(sim.MoveToStructure("walker", "well", now)); err != nil {
		t.Fatalf("MoveToStructure(well) while huddled: %v", err)
	}
	if got := huddleIDOf(t, w, "walker"); got != "" {
		t.Errorf("walker still in huddle %q after move_to; want left", got)
	}
	if mi := moveIntentOf(t, w, "walker"); mi == nil {
		t.Error("move_to while huddled left no MoveIntent; want a walk in flight")
	}
}
