package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// visitor_persistence_test.go — LLM-369 restart-resume coverage for in-flight
// transient visitors. Mirrors labor_persistence_test.go: seed the durable
// visitor mirror, LoadWorld (which runs FinalizeLoad -> rehydrateVisitorsOnLoad),
// and assert the ExpiresAt reconcile + Actor reconstruction + index placement.

func newVisitorFixture(id sim.ActorID, expiresAt time.Time, inside sim.StructureID) *sim.LoadedVisitor {
	return &sim.LoadedVisitor{
		ID:                id,
		DisplayName:       "Elias Drum the peddler",
		Pos:               sim.TilePos{X: sim.PadX + 4, Y: sim.PadY + 6},
		InsideStructureID: inside,
		VisitorState: &sim.VisitorState{
			Archetype:   "peddler",
			Origin:      "Boston",
			Disposition: "weary",
			ExpiresAt:   expiresAt,
			Phase:       sim.VisitorPhasePresent,
			Payload:     "Ezekiel Crane turned out a plow for the Hale farm",
		},
	}
}

func containsVisitorID(ids []sim.ActorID, want sim.ActorID) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestFinalizeLoad_ResumesInWindowVisitor — a visitor still within its stay
// window is rebuilt into World.Actors the way spawn mints one (shared VA, idle,
// seeded needs) with its persona + position restored, and indexed as outdoors.
func TestFinalizeLoad_ResumesInWindowVisitor(t *testing.T) {
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	handles.Visitors.Seed(map[sim.ActorID]*sim.LoadedVisitor{
		"vstr-0000abcd": newVisitorFixture("vstr-0000abcd", now.Add(2*time.Hour), ""),
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	a, ok := w.Actors["vstr-0000abcd"]
	if !ok {
		t.Fatal("in-window visitor not rehydrated into World.Actors")
	}
	if a.Kind != sim.KindNPCShared || a.LLMAgent != sim.VisitorAgentName || a.State != sim.StateIdle {
		t.Errorf("rehydrated identity = kind=%q agent=%q state=%q; want shared / visitor-VA / idle", a.Kind, a.LLMAgent, a.State)
	}
	if a.VisitorState == nil {
		t.Fatal("rehydrated actor has nil VisitorState")
	}
	if a.VisitorState.Archetype != "peddler" || a.VisitorState.Origin != "Boston" || a.VisitorState.Phase != sim.VisitorPhasePresent {
		t.Errorf("rehydrated VisitorState = %+v; want peddler / Boston / present", a.VisitorState)
	}
	if a.VisitorState.Payload != "Ezekiel Crane turned out a plow for the Hale farm" {
		t.Errorf("rehydrated Payload = %q; want the carried rumor restored", a.VisitorState.Payload)
	}
	if a.Pos.X != sim.PadX+4 || a.Pos.Y != sim.PadY+6 {
		t.Errorf("rehydrated Pos = %+v; want the persisted tile", a.Pos)
	}
	if !containsVisitorID(sim.OutdoorActorIDs(w), "vstr-0000abcd") {
		t.Error("rehydrated outdoor visitor missing from the outdoorActors index")
	}
}

// TestFinalizeLoad_ResumesInsideStructureVisitor — a visitor checkpointed inside
// a structure reloads inside it, in the actorsByStructure index (not outdoors).
func TestFinalizeLoad_ResumesInsideStructureVisitor(t *testing.T) {
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	const tavern sim.StructureID = "str-tavern-0001"
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		tavern: {ID: tavern, DisplayName: "The Ordinary"},
	})
	handles.Visitors.Seed(map[sim.ActorID]*sim.LoadedVisitor{
		"vstr-0000beef": newVisitorFixture("vstr-0000beef", now.Add(90*time.Minute), tavern),
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	a, ok := w.Actors["vstr-0000beef"]
	if !ok {
		t.Fatal("inside-structure visitor not rehydrated")
	}
	if a.InsideStructureID != tavern {
		t.Errorf("InsideStructureID = %q, want %q", a.InsideStructureID, tavern)
	}
	if !containsVisitorID(sim.ActorsInStructure(w, tavern), "vstr-0000beef") {
		t.Error("inside-structure visitor missing from the actorsByStructure index")
	}
	if containsVisitorID(sim.OutdoorActorIDs(w), "vstr-0000beef") {
		t.Error("inside-structure visitor wrongly in the outdoorActors index")
	}
}

// TestFinalizeLoad_DropsElapsedVisitor — a visitor whose stay elapsed while the
// engine was down is not resurrected and leaves no dangling index membership.
// Its row is swept from the table on the next checkpoint (absent from cp.Actors).
func TestFinalizeLoad_DropsElapsedVisitor(t *testing.T) {
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	handles.Visitors.Seed(map[sim.ActorID]*sim.LoadedVisitor{
		"vstr-0000dead": newVisitorFixture("vstr-0000dead", now.Add(-1*time.Minute), ""),
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	if _, ok := w.Actors["vstr-0000dead"]; ok {
		t.Error("elapsed visitor was resurrected; want dropped")
	}
	if containsVisitorID(sim.OutdoorActorIDs(w), "vstr-0000dead") {
		t.Error("elapsed visitor left dangling in the outdoorActors index")
	}
}

// TestFinalizeLoad_DropsVisitorWithMissingStructure — a visitor whose
// inside_structure_id points at a structure absent from the loaded set (only
// possible from an out-of-band edit) is dropped rather than indexed under a
// nonexistent structure.
func TestFinalizeLoad_DropsVisitorWithMissingStructure(t *testing.T) {
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	const missing sim.StructureID = "str-does-not-exist"
	handles.Visitors.Seed(map[sim.ActorID]*sim.LoadedVisitor{
		"vstr-0000c0de": newVisitorFixture("vstr-0000c0de", now.Add(time.Hour), missing),
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if _, ok := w.Actors["vstr-0000c0de"]; ok {
		t.Error("visitor with a missing inside_structure_id was rehydrated; want dropped")
	}
	if len(sim.ActorsInStructure(w, missing)) != 0 {
		t.Error("dropped visitor left a dangling entry under a nonexistent structure")
	}
}

// TestFinalizeLoad_DropsVisitorWithInvalidPhase — a persisted phase outside the
// Go-owned allowlist (an out-of-band edit) is dropped, not silently loaded.
func TestFinalizeLoad_DropsVisitorWithInvalidPhase(t *testing.T) {
	repo, handles := mem.NewRepository()
	now := time.Now().UTC()
	lv := newVisitorFixture("vstr-0000f00d", now.Add(time.Hour), "")
	lv.VisitorState.Phase = "loitering" // not a known phase
	handles.Visitors.Seed(map[sim.ActorID]*sim.LoadedVisitor{"vstr-0000f00d": lv})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	if _, ok := w.Actors["vstr-0000f00d"]; ok {
		t.Error("visitor with an unknown phase was rehydrated; want dropped")
	}
}

func TestVisitorPhase_Valid(t *testing.T) {
	for _, p := range []sim.VisitorPhase{sim.VisitorPhasePresent, sim.VisitorPhaseDeparting} {
		if !p.Valid() {
			t.Errorf("VisitorPhase(%q).Valid() = false, want true", p)
		}
	}
	for _, p := range []sim.VisitorPhase{"", "departed", "loitering", "PRESENT"} {
		if p.Valid() {
			t.Errorf("VisitorPhase(%q).Valid() = true, want false", p)
		}
	}
}
