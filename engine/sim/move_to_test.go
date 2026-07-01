package sim_test

import (
	"context"
	"errors"
	"fmt"
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
		"manor": {ID: "manor", AssetID: "house", Pos: sim.WorldPos{X: 320, Y: 320}, EntryPolicy: sim.EntryPolicyOwner, OwnerActorID: "lord"},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"manor": {ID: "manor", DisplayName: "Manor"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"lord":     {ID: "lord", DisplayName: "Lord", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
		"resident": {ID: "resident", DisplayName: "Resident", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}, HomeStructureID: "manor"},
		"stranger": {ID: "stranger", DisplayName: "Stranger", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
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

// TestMoveToStructureByName_RejectsUnresolvableName is the end-to-end wiring
// check for the by-name command on a running world: a name no village structure
// matches (and no remembered source) is rejected (with a retry-anchored message)
// and stamps no MoveIntent. (Resolution correctness — village-wide match,
// nearest-wins — is covered white-box in move_to_byname_test.go.)
func TestMoveToStructureByName_RejectsUnresolvableName(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()

	_, err := w.Send(sim.MoveToStructureByName("walker", "The Nonexistent Place", nil, sim.RememberedPlaces{}, time.Now().UTC()))
	if err == nil {
		t.Fatal("want error for an unresolvable place name, got nil")
	}
	if !strings.Contains(err.Error(), "no place called") {
		t.Errorf("error lacks 'no place called': %v", err)
	}
	if mi := moveIntentOf(t, w, "walker"); mi != nil {
		t.Error("rejected by-name move stamped a MoveIntent; want none")
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

// --- no-op self-move to the current structure (LLM-196) ----------------

// seedActorAtLoiterPin parks id on structureID's loiter pin — the tile a
// StructureVisit resolves to — so a subsequent move_to(structureID) that
// derives a VISIT is a no-op walk to where the actor already stands.
func seedActorAtLoiterPin(t *testing.T, w *sim.World, id sim.ActorID, structureID sim.StructureID) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		pin, ok := sim.EffectiveLoiterTile(world, structureID)
		if !ok {
			return nil, fmt.Errorf("structure %q has no loiter pin", structureID)
		}
		world.Actors[id].Pos = pin
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed %q at %q loiter pin: %v", id, structureID, err)
	}
}

// TestMoveToStructure_RejectsAlreadyAtVisitSlot covers the VISIT sibling of the
// already-inside reject: an actor standing on a structure's loiter pin issues
// move_to for that same structure. The well is EntryPolicyClosed, so the move
// derives a StructureVisit — and the walker already stands on the slot, so it
// is a no-op. It must reject WITHOUT stamping a MoveIntent (LLM-196).
func TestMoveToStructure_RejectsAlreadyAtVisitSlot(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	seedActorAtLoiterPin(t, w, "walker", "well")

	_, err := w.Send(sim.MoveToStructure("walker", "well", now))
	if err == nil {
		t.Fatal("want error for move_to a structure the actor already loiters at, got nil")
	}
	if !strings.Contains(err.Error(), "already at") {
		t.Errorf("error lacks 'already at': %v", err)
	}
	if mi := moveIntentOf(t, w, "walker"); mi != nil {
		t.Error("no-op visit stamped a MoveIntent; want none")
	}
}

// TestMoveTo_NoOpGuardsAreTerminalNoOp (LLM-209): every already-there /
// already-walking no-op reject must carry sim.TerminalNoOpError, so the tick
// harness ENDS the tick on it instead of the weak model re-firing the identical
// move every round to the iteration budget (the observed move_to×6 budget_forced
// storm). A GENUINE error (an unknown structure_id the model should correct)
// must NOT — it stays a retryable ModelFacingError.
func TestMoveTo_NoOpGuardsAreTerminalNoOp(t *testing.T) {
	now := time.Now().UTC()

	t.Run("already inside (enter no-op)", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Actors["walker"].InsideStructureID = "inn"
			return nil, nil
		}}); err != nil {
			t.Fatalf("seed InsideStructureID: %v", err)
		}
		_, err := w.Send(sim.MoveToStructure("walker", "inn", now))
		var noop sim.TerminalNoOpError
		if !errors.As(err, &noop) {
			t.Fatalf("already-inside reject must be TerminalNoOpError, got %T: %v", err, err)
		}
	})

	t.Run("already at visit slot", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		seedActorAtLoiterPin(t, w, "walker", "well")
		_, err := w.Send(sim.MoveToStructure("walker", "well", now))
		var noop sim.TerminalNoOpError
		if !errors.As(err, &noop) {
			t.Fatalf("already-at-slot reject must be TerminalNoOpError, got %T: %v", err, err)
		}
	})

	t.Run("already walking to same dest", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		if _, err := w.Send(sim.MoveToStructure("walker", "inn", now)); err != nil {
			t.Fatalf("first MoveToStructure(inn): %v", err)
		}
		_, err := w.Send(sim.MoveToStructure("walker", "inn", now))
		var noop sim.TerminalNoOpError
		if !errors.As(err, &noop) {
			t.Fatalf("already-walking reject must be TerminalNoOpError, got %T: %v", err, err)
		}
	})

	t.Run("unknown structure stays a retryable error", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		_, err := w.Send(sim.MoveToStructure("walker", "nowhere", now))
		if err == nil {
			t.Fatal("want error for an unknown structure_id, got nil")
		}
		var noop sim.TerminalNoOpError
		if errors.As(err, &noop) {
			t.Fatalf("an unknown structure_id must NOT be TerminalNoOpError (the model should retry): %v", err)
		}
	})
}

// TestMoveToStructure_NoOpVisitKeepsHuddle is the LLM-196 regression proper: a
// co-present actor loitering at a structure's slot, in a huddle there, issues
// move_to for that same structure. The no-op visit must be rejected BEFORE
// MoveActor runs, so the huddle is left intact — the bug was the self-move
// firing HuddleLeft (and a spurious businessowner farewell) then re-forming the
// huddle on instant arrival, mid-negotiation.
func TestMoveToStructure_NoOpVisitKeepsHuddle(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	seedActorAtLoiterPin(t, w, "walker", "well")
	if _, err := w.Send(sim.JoinHuddle("walker", "well", "", now)); err != nil {
		t.Fatalf("JoinHuddle(well): %v", err)
	}
	if huddleIDOf(t, w, "walker") == "" {
		t.Fatal("walker not in a huddle after JoinHuddle")
	}
	// Seed a stale "found it shut" memory for the well: a rejected no-op visit
	// must NOT clear it — the ZBBS-HOME-405 forget fires only on a genuine new
	// walk, and the reordered dest-derivation still sits above forget.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["walker"].Observed = sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "well", Condition: sim.ObservedClosed}: now,
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed stale supplier memory: %v", err)
	}

	if _, err := w.Send(sim.MoveToStructure("walker", "well", now)); err == nil {
		t.Fatal("want no-op reject for move_to the structure the actor loiters at, got nil")
	}
	if got := huddleIDOf(t, w, "walker"); got == "" {
		t.Error("no-op move_to dropped the huddle; want it preserved (LLM-196)")
	}
	if mi := moveIntentOf(t, w, "walker"); mi != nil {
		t.Error("no-op visit stamped a MoveIntent; want none")
	}
	if _, ok := supplierMemoryOf(t, w, "walker").At(sim.ObservedStateKey{StructureID: "well", Condition: sim.ObservedClosed}); !ok {
		t.Error("no-op visit cleared stale supplier memory; want it preserved (ZBBS-HOME-405)")
	}
}

// TestMoveToStructure_NoOpVisitOwnerOnlyRejects exercises the exact decision
// shape of the live bug: a non-member standing on an owner-only structure's
// loiter pin. moveToDestinationFor derives a VISIT (the stranger cannot enter),
// and standing on the pin makes the walk a no-op — rejected "already at" with
// no MoveIntent, rather than a huddle-tearing zero-distance move (LLM-196). The
// closed-well test covers the guard; this covers the shop/owner-only shape.
func TestMoveToStructure_NoOpVisitOwnerOnlyRejects(t *testing.T) {
	w, cancel := buildMoveToOwnerTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	seedActorAtLoiterPin(t, w, "stranger", "manor")

	_, err := w.Send(sim.MoveToStructure("stranger", "manor", now))
	if err == nil {
		t.Fatal("want 'already at' reject for a non-member on the owner-only loiter pin, got nil")
	}
	if !strings.Contains(err.Error(), "already at") {
		t.Errorf("error lacks 'already at': %v", err)
	}
	if mi := moveIntentOf(t, w, "stranger"); mi != nil {
		t.Error("no-op visit stamped a MoveIntent; want none")
	}
}

// --- stale supplier-memory clear on commit (ZBBS-HOME-405) -------------

// TestMoveToStructure_ClearsStaleSupplierMemoryForDestination asserts that
// committing a walk to a structure drops the actor's experiential observed-state
// memories — "found it shut" (ObservedClosed) and "found it dry"
// (ObservedOutOfStock) — for THAT destination only, leaving memories about other
// businesses intact. Without this, a mid-walk re-decision off the stale "shut"
// annotation steers the actor away from the destination it is en route to (the
// Josiah↔Ellis Farm thrash).
func TestMoveToStructure_ClearsStaleSupplierMemoryForDestination(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Seed walker with shut + out-of-stock memories for the destination (inn)
	// AND for an unrelated structure (gazebo), inside a command so the test
	// goroutine never touches live world state.
	const other = sim.StructureID("gazebo")
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["walker"].Observed = sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "inn", Condition: sim.ObservedClosed}:                       now,
			{StructureID: other, Condition: sim.ObservedClosed}:                       now,
			{StructureID: "inn", ItemKind: "meat", Condition: sim.ObservedOutOfStock}: now,
			{StructureID: "inn", ItemKind: "milk", Condition: sim.ObservedOutOfStock}: now,
			{StructureID: other, ItemKind: "meat", Condition: sim.ObservedOutOfStock}: now,
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := w.Send(sim.MoveToStructure("walker", "inn", now)); err != nil {
		t.Fatalf("MoveToStructure(inn): %v", err)
	}

	obs := supplierMemoryOf(t, w, "walker")

	if _, ok := obs.At(sim.ObservedStateKey{StructureID: "inn", Condition: sim.ObservedClosed}); ok {
		t.Error("closed[inn] should be cleared after move_to(inn)")
	}
	if _, ok := obs.At(sim.ObservedStateKey{StructureID: other, Condition: sim.ObservedClosed}); !ok {
		t.Errorf("closed[%s] should be untouched — destination-only clear", other)
	}
	if _, ok := obs.At(sim.ObservedStateKey{StructureID: "inn", ItemKind: "meat", Condition: sim.ObservedOutOfStock}); ok {
		t.Error("out-of-stock{inn,meat} should be cleared after move_to(inn)")
	}
	if _, ok := obs.At(sim.ObservedStateKey{StructureID: "inn", ItemKind: "milk", Condition: sim.ObservedOutOfStock}); ok {
		t.Error("out-of-stock{inn,milk} should be cleared after move_to(inn)")
	}
	if _, ok := obs.At(sim.ObservedStateKey{StructureID: other, ItemKind: "meat", Condition: sim.ObservedOutOfStock}); !ok {
		t.Errorf("out-of-stock{%s,meat} should be untouched — destination-only clear", other)
	}
}

// supplierMemoryOf reads back a deep copy of an actor's Observed store inside a
// command so the test goroutine never touches live world state.
func supplierMemoryOf(t *testing.T, w *sim.World, id sim.ActorID) sim.ObservedStates {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors[id].Observed.Clone(), nil
	}})
	if err != nil {
		t.Fatalf("supplierMemoryOf: %v", err)
	}
	return res.(sim.ObservedStates)
}

// TestMoveToStructure_KeepsMemoryWhenAlreadyOnTheWay asserts the clear fires
// only on a genuinely new walk: re-issuing move_to for a destination already in
// flight hits the "already on your way" guard, which returns before the clear —
// so stale memory seeded after the walk started survives (ZBBS-HOME-405).
func TestMoveToStructure_KeepsMemoryWhenAlreadyOnTheWay(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// Start the walk first (this clears any memory for inn), THEN seed memory so
	// the re-issue below is what we're testing.
	if _, err := w.Send(sim.MoveToStructure("walker", "inn", now)); err != nil {
		t.Fatalf("MoveToStructure(inn): %v", err)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["walker"].Observed = sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "inn", Condition: sim.ObservedClosed}: now,
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Re-issuing the in-flight destination is rejected — and must NOT clear.
	if _, err := w.Send(sim.MoveToStructure("walker", "inn", now)); err == nil {
		t.Fatal("re-issuing in-flight move_to(inn) should be rejected")
	}
	obs := supplierMemoryOf(t, w, "walker")
	if _, ok := obs.At(sim.ObservedStateKey{StructureID: "inn", Condition: sim.ObservedClosed}); !ok {
		t.Error("closed[inn] should survive a rejected (already-on-the-way) move_to")
	}
}

// TestMoveToStructure_KeepsMemoryWhenAlreadyInside asserts the already-inside
// guard also returns before the clear, so memory for that structure survives a
// no-op walk to where the actor already stands (ZBBS-HOME-405).
func TestMoveToStructure_KeepsMemoryWhenAlreadyInside(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["walker"]
		a.InsideStructureID = "inn"
		a.Observed = sim.NewObservedStates(map[sim.ObservedStateKey]time.Time{
			{StructureID: "inn", Condition: sim.ObservedClosed}: now,
		})
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := w.Send(sim.MoveToStructure("walker", "inn", now)); err == nil {
		t.Fatal("move_to to the structure the actor is already inside should be rejected")
	}
	obs := supplierMemoryOf(t, w, "walker")
	if _, ok := obs.At(sim.ObservedStateKey{StructureID: "inn", Condition: sim.ObservedClosed}); !ok {
		t.Error("closed[inn] should survive a rejected (already-inside) move_to")
	}
}

// --- reserved home/work keywords (LLM-212) ----------------------------------

// setWalkerAnchors seeds walker's home/work anchor structures on the world
// goroutine, so the keyword resolver can map "home"/"work" to them.
func setWalkerAnchors(t *testing.T, w *sim.World, home, work sim.StructureID) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["walker"].HomeStructureID = home
		world.Actors["walker"].WorkStructureID = work
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed anchors: %v", err)
	}
}

// TestMoveTo_AnchorKeywords: move_to("home")/("work") (+ synonyms, article- and
// case-tolerant) resolve to the actor's own anchor structure, via BOTH the name
// path and the structure_id path. A homeless actor gets a retryable steer; an
// actor already at its anchor gets the LLM-209 terminal no-op.
func TestMoveTo_AnchorKeywords(t *testing.T) {
	now := time.Now().UTC()

	t.Run("home keyword walks to HomeStructureID (name path)", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		setWalkerAnchors(t, w, "inn", "well")
		if _, err := w.Send(sim.MoveToStructureByName("walker", "home", nil, sim.RememberedPlaces{}, now)); err != nil {
			t.Fatalf("move_to(home): %v", err)
		}
		if _, sid := destKindOf(t, w, "walker"); sid != "inn" {
			t.Errorf("'home' resolved to %q, want inn (HomeStructureID)", sid)
		}
	})

	t.Run("work synonym walks to WorkStructureID", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		setWalkerAnchors(t, w, "well", "inn")
		if _, err := w.Send(sim.MoveToStructureByName("walker", "my shop", nil, sim.RememberedPlaces{}, now)); err != nil {
			t.Fatalf("move_to(my shop): %v", err)
		}
		if _, sid := destKindOf(t, w, "walker"); sid != "inn" {
			t.Errorf("'my shop' resolved to %q, want inn (WorkStructureID)", sid)
		}
	})

	t.Run("home keyword via structure_id path", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		setWalkerAnchors(t, w, "inn", "well")
		if _, err := w.Send(sim.MoveToStructure("walker", "home", now)); err != nil {
			t.Fatalf("MoveToStructure(home): %v", err)
		}
		if _, sid := destKindOf(t, w, "walker"); sid != "inn" {
			t.Errorf("structure_id 'home' resolved to %q, want inn", sid)
		}
	})

	t.Run("case- and article-tolerant", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		setWalkerAnchors(t, w, "inn", "well")
		if _, err := w.Send(sim.MoveToStructureByName("walker", "the Home", nil, sim.RememberedPlaces{}, now)); err != nil {
			t.Fatalf("move_to('the Home'): %v", err)
		}
		if _, sid := destKindOf(t, w, "walker"); sid != "inn" {
			t.Errorf("'the Home' resolved to %q, want inn", sid)
		}
	})

	t.Run("already home is a terminal no-op (composes with LLM-209)", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		setWalkerAnchors(t, w, "inn", "well")
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Actors["walker"].InsideStructureID = "inn"
			return nil, nil
		}}); err != nil {
			t.Fatalf("seed inside: %v", err)
		}
		_, err := w.Send(sim.MoveToStructureByName("walker", "home", nil, sim.RememberedPlaces{}, now))
		var noop sim.TerminalNoOpError
		if !errors.As(err, &noop) {
			t.Fatalf("move_to(home) while already home must be TerminalNoOpError, got %T: %v", err, err)
		}
	})

	t.Run("homeless actor gets a retryable steer, not a no-op", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		setWalkerAnchors(t, w, "", "well")
		_, err := w.Send(sim.MoveToStructureByName("walker", "home", nil, sim.RememberedPlaces{}, now))
		if err == nil {
			t.Fatal("want error for a homeless actor's move_to(home), got nil")
		}
		if !strings.Contains(err.Error(), "no home") {
			t.Errorf("error should explain there's no home to go to: %v", err)
		}
		var noop sim.TerminalNoOpError
		if errors.As(err, &noop) {
			t.Fatalf("homeless move_to(home) must stay retryable, not TerminalNoOpError: %v", err)
		}
	})

	t.Run("stale anchor gives a retryable steer, no recursion/panic", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		// HomeStructureID points at a structure that isn't in the world (stale/
		// corrupt). It must steer, not dispatch a bad move or recurse.
		setWalkerAnchors(t, w, "ghost-home", "well")
		_, err := w.Send(sim.MoveToStructureByName("walker", "home", nil, sim.RememberedPlaces{}, now))
		if err == nil {
			t.Fatal("want a steer for a stale HomeStructureID, got nil")
		}
		var noop sim.TerminalNoOpError
		if errors.As(err, &noop) {
			t.Fatalf("stale-anchor move_to(home) must stay retryable, not TerminalNoOpError: %v", err)
		}
	})

	t.Run("work synonym via structure_id path", func(t *testing.T) {
		w, cancel, _ := buildMoveTestWorld(t)
		defer cancel()
		setWalkerAnchors(t, w, "well", "inn")
		if _, err := w.Send(sim.MoveToStructure("walker", "my post", now)); err != nil {
			t.Fatalf("MoveToStructure(my post): %v", err)
		}
		if _, sid := destKindOf(t, w, "walker"); sid != "inn" {
			t.Errorf("structure_id 'my post' resolved to %q, want inn (WorkStructureID)", sid)
		}
	})
}
