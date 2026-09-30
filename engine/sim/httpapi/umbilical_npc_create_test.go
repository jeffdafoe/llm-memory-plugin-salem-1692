package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// umbilical_npc_create_test.go — LLM-689. The npc/create control route and the
// sprite payloads' animal flag.

// addSheepSprite puts an animal sprite in the world's catalog.
func addSheepSprite(t *testing.T, srv *Server) {
	t.Helper()
	if _, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Sprites["sheep"] = &sim.Sprite{
			ID: "sheep", Name: "Sheep", Sheet: "tiny-swords/livestock/sheep.png",
			FrameWidth: 32, FrameHeight: 32,
			Behaviors: []string{sim.BehaviorGrazer, sim.BehaviorAmbient}, RenderScale: 1.0,
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed sheep sprite: %v", err)
	}
}

func TestUmbilicalNPCCreate_PlacesAnAnimalNamedForItsSpecies(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	addSheepSprite(t, srv)

	var ids []string
	for range 2 {
		rec := postReq(t, h, "/api/village/umbilical/npc/create", "tok", `{"sprite_id":"sheep","x":160,"y":160}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("create = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var out adminNPCCreateResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.ID == "" {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
		ids = append(ids, out.ID)
	}

	res, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		var out []string
		for _, id := range ids {
			a := world.Actors[sim.ActorID(id)]
			if a == nil {
				return nil, nil
			}
			if a.Kind != sim.KindDecorative || a.SpriteID != "sheep" {
				t.Errorf("%s kind=%v sprite=%s, want decorative sheep", id, a.Kind, a.SpriteID)
			}
			out = append(out, a.DisplayName)
		}
		return out, nil
	}})
	if err != nil {
		t.Fatalf("read actors: %v", err)
	}
	names, _ := res.([]string)
	if len(names) != 2 || names[0] != "Sheep" || names[1] != "Sheep 2" {
		t.Errorf("names = %v, want [Sheep Sheep 2]", names)
	}
}

func TestUmbilicalNPCCreate_Rejects(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)
	addSheepSprite(t, srv)
	cases := []struct {
		body string
		want int
	}{
		{`{"x":160,"y":160}`, http.StatusBadRequest},                                   // no sprite
		{`{"sprite_id":"no-such","x":160,"y":160}`, http.StatusBadRequest},             // unknown sprite
		{`{"sprite_id":"sheep","x":99999999,"y":160}`, http.StatusUnprocessableEntity}, // off the map
	}
	for _, c := range cases {
		if rec := postReq(t, h, "/api/village/umbilical/npc/create", "tok", c.body); rec.Code != c.want {
			t.Errorf("%s = %d, want %d; body=%s", c.body, rec.Code, c.want, rec.Body.String())
		}
	}
}

func TestUmbilicalNPCCreate_Gated(t *testing.T) {
	const path = "/api/village/umbilical/npc/create"
	srv := NewServer(seededWorld(t), permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(4))
	if rec := postReq(t, srv.Handler(), path, "tok", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("control-disabled = %d, want 404", rec.Code)
	}
	_, nonOp := controlServer(t, nil)
	if rec := postReq(t, nonOp, path, "tok", `{}`); rec.Code != http.StatusForbidden {
		t.Errorf("non-operator = %d, want 403", rec.Code)
	}
}

// TestSpritePayloadsFlagAnimals: the editor sorts animals from villagers by the
// sprite's `animal` flag — set on both the catalog and the inline agent sprite,
// omitted for a person.
func TestSpritePayloadsFlagAnimals(t *testing.T) {
	sheep := &sim.Sprite{ID: "sheep", Name: "Sheep", Behaviors: []string{sim.BehaviorGrazer, sim.BehaviorAmbient}}
	person := &sim.Sprite{ID: "p", Name: "Woman A v00", Behaviors: []string{sim.BehaviorAmbient}}
	if !spriteDTO(sheep.ID, sheep).Animal || !agentSpriteDTOFromSprite(sheep).Animal {
		t.Error("sheep not flagged an animal")
	}
	if spriteDTO(person.ID, person).Animal || agentSpriteDTOFromSprite(person).Animal {
		t.Error("person flagged an animal")
	}
	raw, err := json.Marshal(spriteDTO(person.ID, person))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := m["animal"]; present {
		t.Errorf("person payload carries animal: %s", raw)
	}
}
