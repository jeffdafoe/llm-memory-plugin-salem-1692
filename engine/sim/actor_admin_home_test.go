package sim_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildHomeAnchorWorld seeds a world with two structure placements across the
// shared-identity bridge (VillageObject id == Structure id): "home" backed by a
// doored asset, "barracks" backed by a doorless one. Plus one NPC to anchor.
// LLM-344: a doorless structure is a valid WORK anchor (its loiter pin is the
// work post) but must be rejected as a HOME anchor — no NPC can walk inside to
// sleep, and anchoring one there strands them.
func buildHomeAnchorWorld(t *testing.T) *sim.World {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	dx, dy := 0, 0
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"home-asset":     {ID: "home-asset", Name: "House", Category: "structure", DoorOffsetX: &dx, DoorOffsetY: &dy},
		"barracks-asset": {ID: "barracks-asset", Name: "Barracks", Category: "structure"},
	})
	worldPos := func(gp sim.GridPoint) sim.WorldPos {
		c := sim.TileToWorld(gp)
		return sim.WorldPos{X: c.X, Y: c.Y}
	}
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"home":     {ID: "home", AssetID: "home-asset", Pos: worldPos(sim.GridPoint{X: sim.PadX + 5, Y: sim.PadY + 5})},
		"barracks": {ID: "barracks", AssetID: "barracks-asset", Pos: worldPos(sim.GridPoint{X: sim.PadX + 10, Y: sim.PadY + 10})},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"home":     {ID: "home", DisplayName: "House"},
		"barracks": {ID: "barracks", DisplayName: "Barracks"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"npc": {ID: "npc", DisplayName: "Nathan", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, Pos: sim.TilePos{X: sim.PadX + 7, Y: sim.PadY + 7}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)
	return w
}

// actorAnchors reads an NPC's home/work structure ids back off the live world
// (they aren't published in the snapshot), so a test can assert a rejected
// command left the anchor untouched.
func actorAnchors(t *testing.T, w *sim.World, id sim.ActorID) (home, work sim.StructureID) {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a, ok := world.Actors[id]
		if !ok {
			return nil, errors.New("actor missing")
		}
		return [2]sim.StructureID{a.HomeStructureID, a.WorkStructureID}, nil
	}})
	if err != nil {
		t.Fatalf("read anchors: %v", err)
	}
	pair := res.([2]sim.StructureID)
	return pair[0], pair[1]
}

// A doorless structure is rejected as a HOME anchor (ErrStructureNotHabitable)
// and the anchor is left untouched, while a doored one is accepted.
func TestSetActorHomeStructure_DoorlessRejected(t *testing.T) {
	w := buildHomeAnchorWorld(t)

	if _, err := w.Send(sim.SetActorHomeStructure("npc", "barracks")); !errors.Is(err, sim.ErrStructureNotHabitable) {
		t.Fatalf("home=barracks err = %v, want ErrStructureNotHabitable", err)
	}
	if home, _ := actorAnchors(t, w, "npc"); home != "" {
		t.Errorf("home anchor = %q after a rejected doorless assignment, want unchanged (empty)", home)
	}

	if _, err := w.Send(sim.SetActorHomeStructure("npc", "home")); err != nil {
		t.Fatalf("home=home err = %v, want nil (doored structure is habitable)", err)
	}
	if home, _ := actorAnchors(t, w, "npc"); home != "home" {
		t.Errorf("home anchor = %q, want \"home\"", home)
	}
}

// Work is door-agnostic: a doorless structure IS a valid workplace (its loiter
// pin is the work post), so the same structure the home gate rejects is accepted
// as a work anchor.
func TestSetActorWorkStructure_DoorlessAccepted(t *testing.T) {
	w := buildHomeAnchorWorld(t)

	if _, err := w.Send(sim.SetActorWorkStructure("npc", "barracks")); err != nil {
		t.Fatalf("work=barracks err = %v, want nil (work stays door-agnostic)", err)
	}
	if _, work := actorAnchors(t, w, "npc"); work != "barracks" {
		t.Errorf("work anchor = %q, want \"barracks\"", work)
	}
}

// The membership check precedes the habitability check: an unknown id is
// ErrStructureNotFound (404), not ErrStructureNotHabitable (422). Clearing the
// anchor with an empty id always succeeds.
func TestSetActorHomeStructure_NotFoundBeforeHabitable(t *testing.T) {
	w := buildHomeAnchorWorld(t)

	if _, err := w.Send(sim.SetActorHomeStructure("npc", "ghost")); !errors.Is(err, sim.ErrStructureNotFound) {
		t.Fatalf("home=ghost err = %v, want ErrStructureNotFound", err)
	}
	if _, err := w.Send(sim.SetActorHomeStructure("npc", "")); err != nil {
		t.Fatalf("home=\"\" (clear) err = %v, want nil", err)
	}
}
