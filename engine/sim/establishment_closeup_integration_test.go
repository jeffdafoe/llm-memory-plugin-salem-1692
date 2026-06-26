package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// establishment_closeup_integration_test.go — LLM-129. The end-to-end eviction
// walk, driven against the walkable buildMoveTestWorld (real terrain + an "inn"
// placement with a door): a non-tenant left inside a closed establishment is
// turned out via a StructureVisit, i.e. it gets a MoveIntent toward a visitor
// slot OUTSIDE the footprint and the locomotion ticker paths it out the door.
// The selection / announce / gate logic is unit-tested in package sim
// (establishment_closeup_test.go); this proves the "appear leaving the building,
// walk to the loiter" motion the unit tests can't (no walk grid).
func TestEvictNonTenantsAtClose_WalksNonTenantOut(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	now := time.Now().UTC()

	// The walker holds no home/work/owner/lodger tie to the inn — a non-tenant.
	// Drop it inside the inn (index + InsideStructureID) so the close-up scan
	// finds it; there is no keeper of the inn present, so the house reads closed.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.SetActorInsideStructure(world, world.Actors["walker"], "inn")
		return nil, nil
	}}); err != nil {
		t.Fatalf("place walker inside the inn: %v", err)
	}

	if _, err := w.Send(sim.EvictNonTenantsAtClose("inn", now)); err != nil {
		t.Fatalf("EvictNonTenantsAtClose: %v", err)
	}

	mi := moveIntentOf(t, w, "walker")
	if mi == nil {
		t.Fatal("walker has no MoveIntent — it was not turned out")
	}
	if mi.Destination.Kind != sim.MoveDestinationStructureVisit {
		t.Errorf("destination kind = %q, want structure_visit (walk out to the loiter slot)", mi.Destination.Kind)
	}
	if mi.Destination.StructureID == nil || *mi.Destination.StructureID != "inn" {
		t.Errorf("destination structure = %v, want inn", mi.Destination.StructureID)
	}
}
