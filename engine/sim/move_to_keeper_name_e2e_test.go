package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// move_to_keeper_name_e2e_test.go — LLM-868 through MoveToStructureByName: the
// keeper's phrasing walks to the keeper's place, and a place actually named that
// still wins.

func seedInnkeeper(t *testing.T, w *sim.World) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hannah"] = &sim.Actor{ID: "hannah", DisplayName: "Hannah Boggs", WorkStructureID: "inn",
			Pos: sim.TilePos{X: sim.PadX, Y: sim.PadY}}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestMoveToByName_KeeperPhrasingWalksToTheKeepersPlace(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	seedInnkeeper(t, w)
	now := time.Date(2026, 10, 25, 21, 11, 0, 0, time.UTC)
	if _, err := w.Send(sim.MoveToStructureByName("walker", "Hannah Boggs's Inn", nil, sim.RememberedPlaces{}, now)); err != nil {
		t.Fatalf("move_to Hannah Boggs's Inn: %v", err)
	}
	mi := moveIntentOf(t, w, "walker")
	if mi == nil || mi.Destination.StructureID == nil || *mi.Destination.StructureID != "inn" {
		t.Fatalf("MoveIntent = %+v, want a walk to the inn", mi)
	}
}

func TestMoveToByName_RealPlaceNameWinsOverKeeper(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	seedInnkeeper(t, w)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Structures["gazebo"].DisplayName = "Boggs's Gazebo"
		return nil, nil
	}}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	now := time.Date(2026, 10, 25, 21, 11, 0, 0, time.UTC)
	if _, err := w.Send(sim.MoveToStructureByName("walker", "Boggs's Gazebo", nil, sim.RememberedPlaces{}, now)); err != nil {
		t.Fatalf("move_to Boggs's Gazebo: %v", err)
	}
	mi := moveIntentOf(t, w, "walker")
	if mi == nil || mi.Destination.StructureID == nil || *mi.Destination.StructureID != "gazebo" {
		t.Fatalf("MoveIntent = %+v, want a walk to the gazebo (the structure named that), not the keeper's inn", mi)
	}
}
