package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// inflight_move_test.go — coverage of ActorView.InFlightMove projection in
// buildActorView + the "currently: walking to X" perception-render line
// (ZBBS-HOME-336). The cue that reminds a mid-walk NPC of its own
// destination so a reactor tick doesn't re-pick a goal from scratch. Snapshot
// fixture is hand-built so the test stays independent of LoadWorld / the
// world goroutine, matching dwell_test.go.

// moveSnap builds a minimal *sim.Snapshot with one actor carrying the supplied
// in-flight move read-path projection, plus optional structures / objects for
// label resolution.
func moveSnap(kind sim.MoveDestinationKind, structID sim.StructureID, objID sim.VillageObjectID, pos sim.TilePos, structures map[sim.StructureID]*sim.Structure, objects map[sim.VillageObjectID]*sim.VillageObject) *sim.Snapshot {
	return &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"john": {
				State:               sim.StateIdle,
				Needs:               map[sim.NeedKey]int{"hunger": 24},
				MoveDestKind:        kind,
				MoveDestStructureID: structID,
				MoveDestObjectID:    objID,
				MoveDestPos:         pos,
			},
		},
		Structures:     structures,
		VillageObjects: objects,
		Scenes:         map[sim.SceneID]*sim.Scene{},
		Huddles:        map[sim.HuddleID]*sim.Huddle{},
	}
}

// requireInFlightMove fails the test (rather than panicking on a nil deref)
// when buildActorView regresses to a nil InFlightMove where a view is
// expected.
func requireInFlightMove(t *testing.T, av ActorView) *InFlightMoveView {
	t.Helper()
	if av.InFlightMove == nil {
		t.Fatal("InFlightMove = nil, want a view")
	}
	return av.InFlightMove
}

func TestBuildActorView_NotMoving_NilInFlightMove(t *testing.T) {
	snap := moveSnap("", "", "", sim.TilePos{}, nil, nil)
	av := buildActorView(snap, snap.Actors["john"])
	if av.InFlightMove != nil {
		t.Errorf("InFlightMove = %+v, want nil when not moving", av.InFlightMove)
	}
}

func TestBuildActorView_StructureEnter_ResolvesLabel(t *testing.T) {
	structs := map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "Tavern"},
	}
	snap := moveSnap(sim.MoveDestinationStructureEnter, "tavern", "", sim.TilePos{}, structs, nil)
	av := buildActorView(snap, snap.Actors["john"])
	m := requireInFlightMove(t, av)
	if m.Kind != sim.MoveDestinationStructureEnter {
		t.Errorf("Kind = %q, want structure_enter", m.Kind)
	}
	if m.DestinationLabel != "Tavern" {
		t.Errorf("DestinationLabel = %q, want 'Tavern'", m.DestinationLabel)
	}
	if got := renderInFlightMove(*m); got != "walking to enter Tavern" {
		t.Errorf("render = %q, want 'walking to enter Tavern'", got)
	}
}

func TestBuildActorView_StructureLabelWinsOverSharedObjectID(t *testing.T) {
	// Structures share ids with village_objects in this codebase. When both
	// maps hold the destination id, the structure DisplayName must win — lock
	// it in, since the VillageObjectID(structID) cast hides the dependency.
	structs := map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "Tavern"},
	}
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		"tavern": {ID: "tavern", DisplayName: "wrong object label"},
	}
	snap := moveSnap(sim.MoveDestinationStructureEnter, "tavern", "", sim.TilePos{}, structs, objects)
	av := buildActorView(snap, snap.Actors["john"])
	m := requireInFlightMove(t, av)
	if m.DestinationLabel != "Tavern" {
		t.Errorf("DestinationLabel = %q, want 'Tavern' (structure wins over shared-id object)", m.DestinationLabel)
	}
}

func TestBuildActorView_UnknownKind_NilInFlightMove(t *testing.T) {
	// A non-empty but unrecognized destination kind (corrupt snapshot, or a
	// kind added to the engine but not wired into perception) returns nil
	// rather than a vague view that would mask the gap.
	snap := moveSnap(sim.MoveDestinationKind("teleport_pad"), "", "", sim.TilePos{}, nil, nil)
	av := buildActorView(snap, snap.Actors["john"])
	if av.InFlightMove != nil {
		t.Errorf("InFlightMove = %+v, want nil for unknown kind", av.InFlightMove)
	}
}

func TestBuildActorView_StructureVisit_NoEnterPhrasing(t *testing.T) {
	structs := map[sim.StructureID]*sim.Structure{
		"well": {ID: "well", DisplayName: "the village well"},
	}
	snap := moveSnap(sim.MoveDestinationStructureVisit, "well", "", sim.TilePos{}, structs, nil)
	av := buildActorView(snap, snap.Actors["john"])
	m := requireInFlightMove(t, av)
	if got := renderInFlightMove(*m); got != "walking to the village well" {
		t.Errorf("render = %q, want 'walking to the village well'", got)
	}
}

func TestBuildActorView_ObjectVisit_FallsBackToVillageObjectLabel(t *testing.T) {
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		"oak": {ID: "oak", AssetID: "tree-oak", DisplayName: "the old oak"},
	}
	snap := moveSnap(sim.MoveDestinationObjectVisit, "", "oak", sim.TilePos{}, nil, objects)
	av := buildActorView(snap, snap.Actors["john"])
	m := requireInFlightMove(t, av)
	if m.DestinationLabel != "the old oak" {
		t.Errorf("DestinationLabel = %q, want 'the old oak'", m.DestinationLabel)
	}
}

func TestBuildActorView_PositionMove_RendersTileCoordinate(t *testing.T) {
	snap := moveSnap(sim.MoveDestinationPosition, "", "", sim.TilePos{X: 41, Y: 44}, nil, nil)
	av := buildActorView(snap, snap.Actors["john"])
	m := requireInFlightMove(t, av)
	if m.DestinationLabel != "(41, 44)" {
		t.Errorf("DestinationLabel = %q, want '(41, 44)'", m.DestinationLabel)
	}
	if got := renderInFlightMove(*m); got != "walking to (41, 44)" {
		t.Errorf("render = %q, want 'walking to (41, 44)'", got)
	}
}

func TestRenderInFlightMove_UnresolvedLabel_GenericPhrasing(t *testing.T) {
	// Destination structure not present in the snapshot maps → empty label.
	snap := moveSnap(sim.MoveDestinationStructureEnter, "missing", "", sim.TilePos{}, nil, nil)
	av := buildActorView(snap, snap.Actors["john"])
	m := requireInFlightMove(t, av)
	if got := renderInFlightMove(*m); got != "walking to your destination" {
		t.Errorf("render = %q, want 'walking to your destination'", got)
	}
}

func TestRenderActor_IncludesInFlightMoveLine(t *testing.T) {
	structs := map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "Tavern"},
	}
	snap := moveSnap(sim.MoveDestinationStructureEnter, "tavern", "", sim.TilePos{}, structs, nil)
	av := buildActorView(snap, snap.Actors["john"])

	var b strings.Builder
	renderActor(&b, av)
	if !strings.Contains(b.String(), "currently: walking to enter Tavern") {
		t.Errorf("renderActor output missing in-flight move line:\n%s", b.String())
	}
}
