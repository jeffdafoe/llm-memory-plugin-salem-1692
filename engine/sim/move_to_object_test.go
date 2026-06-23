package sim_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// move_to_object_test.go — ZBBS-HOME-359. End-to-end coverage of move_to
// reaching a BARE refresh source (a well) on a running world: the MoveToObject
// command, the structure_id fallthrough (a non-structure refresh id routes to an
// object visit), the by-name fallthrough, and the rejects. Resolver correctness
// is covered white-box in move_to_object_resolve_test.go.

// buildMoveToObjectTestWorld seeds a world with two BARE placements (no
// Structure rows): "village_well" (a thirst refresh source) and "lamp" (décor,
// no refresh), plus a "walker" at the pad with a clear grass path to both.
func buildMoveToObjectTestWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"well": {ID: "well", Category: "prop"},
		"lamp": {ID: "lamp", Category: "prop"},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		// 3 tiles east of the pad origin — within the default name-resolution
		// scene radius (3) and outside LoiterAttributionTiles (1, so not
		// "already at"), with a clear grass path from the walker.
		"village_well": {ID: "village_well", AssetID: "well", Pos: sim.WorldPos{X: 96, Y: 0},
			DisplayName: "Village Well", EntryPolicy: sim.EntryPolicyClosed,
			Refreshes: []*sim.ObjectRefresh{{Attribute: "thirst", Amount: -8}}},
		"lamp": {ID: "lamp", AssetID: "lamp", Pos: sim.WorldPos{X: 640, Y: 320}, DisplayName: "Lamp Post"},
	})
	// No Structures seeded — both placements are bare.
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"walker": {ID: "walker", DisplayName: "Walker", Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// objDestOf returns an actor's in-flight object-visit destination id (empty when
// not moving or not an object visit).
func objDestOf(t *testing.T, w *sim.World, id sim.ActorID) (sim.MoveDestinationKind, sim.VillageObjectID) {
	t.Helper()
	mi := moveIntentOf(t, w, id)
	if mi == nil {
		return "", ""
	}
	oid := sim.VillageObjectID("")
	if mi.Destination.ObjectID != nil {
		oid = *mi.Destination.ObjectID
	}
	return mi.Destination.Kind, oid
}

func TestMoveToObject_WalksToBareWell(t *testing.T) {
	w, cancel := buildMoveToObjectTestWorld(t)
	defer cancel()

	if _, err := w.Send(sim.MoveToObject("walker", "village_well", time.Now().UTC())); err != nil {
		t.Fatalf("MoveToObject(village_well): %v", err)
	}
	kind, oid := objDestOf(t, w, "walker")
	if kind != sim.MoveDestinationObjectVisit || oid != "village_well" {
		t.Errorf("dest = %q/%q, want object_visit/village_well", kind, oid)
	}
}

// The satiation free-source cue carries the well's id in the structure_id field;
// move_to(structure_id=<well>) must fall through to an object visit.
func TestMoveToStructure_IDFallthroughToObject(t *testing.T) {
	w, cancel := buildMoveToObjectTestWorld(t)
	defer cancel()

	if _, err := w.Send(sim.MoveToStructure("walker", "village_well", time.Now().UTC())); err != nil {
		t.Fatalf("MoveToStructure(village_well) fallthrough: %v", err)
	}
	kind, oid := objDestOf(t, w, "walker")
	if kind != sim.MoveDestinationObjectVisit || oid != "village_well" {
		t.Errorf("dest = %q/%q, want object_visit/village_well (id fallthrough)", kind, oid)
	}
}

func TestMoveToStructureByName_NameFallthroughToObject(t *testing.T) {
	w, cancel := buildMoveToObjectTestWorld(t)
	defer cancel()

	if _, err := w.Send(sim.MoveToStructureByName("walker", "Village Well", nil, nil, sim.RememberedPlaces{}, time.Now().UTC())); err != nil {
		t.Fatalf("MoveToStructureByName(Village Well) fallthrough: %v", err)
	}
	kind, oid := objDestOf(t, w, "walker")
	if kind != sim.MoveDestinationObjectVisit || oid != "village_well" {
		t.Errorf("dest = %q/%q, want object_visit/village_well (name fallthrough)", kind, oid)
	}
}

// A bare décor prop (no refresh) is NOT a refresh source, so its id does not
// fall through — it rejects as an unknown structure rather than routing a walk.
func TestMoveToStructure_BareNonRefreshObjectRejected(t *testing.T) {
	w, cancel := buildMoveToObjectTestWorld(t)
	defer cancel()

	_, err := w.Send(sim.MoveToStructure("walker", "lamp", time.Now().UTC()))
	if err == nil {
		t.Fatal("want reject for a bare non-refresh object id, got nil")
	}
	if !strings.Contains(err.Error(), "no structure") {
		t.Errorf("error lacks 'no structure': %v", err)
	}
	if mi := moveIntentOf(t, w, "walker"); mi != nil {
		t.Error("rejected move stamped a MoveIntent; want none")
	}
}

func TestMoveToObject_RejectsUnknownObject(t *testing.T) {
	w, cancel := buildMoveToObjectTestWorld(t)
	defer cancel()

	_, err := w.Send(sim.MoveToObject("walker", "nowhere", time.Now().UTC()))
	if err == nil {
		t.Fatal("want reject for an unknown object id, got nil")
	}
	if !strings.Contains(err.Error(), "no place") {
		t.Errorf("error lacks 'no place': %v", err)
	}
	if mi := moveIntentOf(t, w, "walker"); mi != nil {
		t.Error("rejected move stamped a MoveIntent; want none")
	}
}

func TestMoveToObject_RejectsAlreadyWalkingSameObject(t *testing.T) {
	w, cancel := buildMoveToObjectTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	if _, err := w.Send(sim.MoveToObject("walker", "village_well", now)); err != nil {
		t.Fatalf("first MoveToObject(village_well): %v", err)
	}
	// Re-issuing the same in-flight object destination is a no-op reject (the
	// test world runs no locomotion ticker, so the walker is still mid-intent).
	_, err := w.Send(sim.MoveToObject("walker", "village_well", now))
	if err == nil {
		t.Fatal("want reject for re-issuing the same in-flight object destination, got nil")
	}
	if !strings.Contains(err.Error(), "already on your way") {
		t.Errorf("error lacks 'already on your way': %v", err)
	}
}
