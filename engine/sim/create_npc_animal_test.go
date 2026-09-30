package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// create_npc_animal_test.go — LLM-689: an animal is an animal, not a villager.

func TestSpriteIsAnimalAndSpecies(t *testing.T) {
	cases := []struct {
		sprite  *sim.Sprite
		animal  bool
		species string
	}{
		{&sim.Sprite{Name: "Cow (grey)", Behaviors: []string{sim.BehaviorGrazer, sim.BehaviorAmbient}}, true, "Cow"},
		{&sim.Sprite{Name: "Sheep", Behaviors: []string{sim.BehaviorGrazer, sim.BehaviorAmbient}}, true, "Sheep"},
		{&sim.Sprite{Name: "Duck (mallard)", Behaviors: []string{sim.BehaviorWaterfowl}}, true, "Duck"},
		// Ambient alone is scenery, not an animal (the lamplighter's kind of sprite).
		{&sim.Sprite{Name: "Woman A (v00)", Behaviors: []string{sim.BehaviorAmbient}}, false, "Woman A"},
		{&sim.Sprite{Name: "Old Man B v02"}, false, "Old Man B v02"},
		{nil, false, ""},
	}
	for _, c := range cases {
		if got := c.sprite.IsAnimal(); got != c.animal {
			t.Errorf("%+v IsAnimal = %v, want %v", c.sprite, got, c.animal)
		}
		if got := c.sprite.Species(); got != c.species {
			t.Errorf("%+v Species = %q, want %q", c.sprite, got, c.species)
		}
	}
}

// TestCreateNPCNamesAnimalsForTheirSpecies: an unnamed animal placement is
// named for its species and deduped like "Villager"; a person sprite still
// defaults to "Villager"; an explicit name always wins.
func TestCreateNPCNamesAnimalsForTheirSpecies(t *testing.T) {
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(makeAllGrassTerrain())
	handles.Sprites.Seed(map[sim.SpriteID]*sim.Sprite{
		"sheep":  {ID: "sheep", Name: "Sheep", Behaviors: []string{sim.BehaviorGrazer, sim.BehaviorAmbient}},
		"cow":    {ID: "cow", Name: "Cow (grey)", Behaviors: []string{sim.BehaviorGrazer, sim.BehaviorAmbient}},
		"person": {ID: "person", Name: "Woman A v00"},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	place := func(name, sprite string) string {
		t.Helper()
		res, err := w.Send(sim.CreateNPC(name, sprite, sim.WorldPos{X: 100, Y: 100}, time.Now()))
		if err != nil {
			t.Fatalf("CreateNPC(%q, %s): %v", name, sprite, err)
		}
		id := res.(sim.CreateNPCResult).ActorID
		return runOn(t, w, func(world *sim.World) (any, error) {
			return world.Actors[id].DisplayName, nil
		}).(string)
	}

	for i, want := range []string{"Sheep", "Sheep 2", "Sheep 3"} {
		if got := place("", "sheep"); got != want {
			t.Errorf("unnamed sheep #%d = %q, want %q", i+1, got, want)
		}
	}
	if got := place("", "cow"); got != "Cow" {
		t.Errorf("unnamed cow = %q, want %q (colourway dropped)", got, "Cow")
	}
	if got := place("", "person"); got != "Villager" {
		t.Errorf("unnamed person = %q, want Villager", got)
	}
	if got := place("Dolly", "sheep"); got != "Dolly" {
		t.Errorf("named sheep = %q, want Dolly", got)
	}
	// An unknown sprite is still refused, whatever the name.
	if _, err := w.Send(sim.CreateNPC("", "no-such-sprite", sim.WorldPos{X: 100, Y: 100}, time.Now())); err == nil {
		t.Error("unknown sprite accepted")
	}
}
