package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestSpritePayloadsCarryRigLayers: a paper-doll sprite (LLM-691) reaches the
// client with its rig and its layer list verbatim, on both the catalog and the
// inline agent payload (the inline one also rides npc_created and
// npc_sprite_changed); a one-sheet sprite carries neither key.
func TestSpritePayloadsCarryRigLayers(t *testing.T) {
	layers := json.RawMessage(`[{"sheet":"/b.png","ramps":{"skin":2}},{"sheet":"/s.png","behind":true}]`)
	farmer := &sim.Sprite{ID: "f", Name: "Farmer", Sheet: "/b.png", FrameWidth: 64, FrameHeight: 64, Rig: "farmer_base", Layers: layers}
	person := &sim.Sprite{ID: "p", Name: "Woman A v00", Sheet: "/w.png"}

	decode := func(t *testing.T, v any) map[string]any {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return m
	}
	var want any
	if err := json.Unmarshal(layers, &want); err != nil {
		t.Fatal(err)
	}

	for name, payload := range map[string]any{
		"catalog": spriteDTO(farmer.ID, farmer),
		"inline":  agentSpriteDTOFromSprite(farmer),
	} {
		m := decode(t, payload)
		if m["rig"] != "farmer_base" {
			t.Errorf("%s: rig = %v, want farmer_base", name, m["rig"])
		}
		gotLayers, _ := json.Marshal(m["layers"])
		wantLayers, _ := json.Marshal(want)
		if string(gotLayers) != string(wantLayers) {
			t.Errorf("%s: layers = %s, want %s", name, gotLayers, wantLayers)
		}
	}
	for name, payload := range map[string]any{
		"catalog": spriteDTO(person.ID, person),
		"inline":  agentSpriteDTOFromSprite(person),
	} {
		m := decode(t, payload)
		for _, key := range []string{"rig", "layers"} {
			if _, present := m[key]; present {
				t.Errorf("%s: one-sheet sprite carries %q", name, key)
			}
		}
	}
}
