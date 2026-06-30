package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_item_set_test.go — handler coverage for the item-definition write
// control route (LLM-200), the create/edit leg of the all-live new-good flow.
// The durable item_kind write is covered against real pg in
// repo/pg/refdata_integration_test.go; here a fake ItemKindWriter stands in so
// the tests assert the HTTP plumbing: validation, the write-then-update
// ordering, satiation preservation on edit, status mapping, and manifest
// presence.

// fakeItemKindWriter records the upserted def, or returns a forced error.
type fakeItemKindWriter struct {
	def   sim.ItemKindDef
	calls int
	err   error
}

func (f *fakeItemKindWriter) UpsertItemKind(_ context.Context, def sim.ItemKindDef) error {
	// Record the attempt before returning any configured error, so a write-error
	// test can assert the durable write WAS attempted (and that the in-memory
	// update did not run afterward).
	f.def = def
	f.calls++
	return f.err
}

// itemSetServer builds a control-enabled server with the given writer (nil =
// leave it unwired) and a catalog containing stew (food, one satiation entry) so
// the edit-preserves-satiation case is real.
func itemSetServer(t *testing.T, writer ItemKindWriter) (*Server, http.Handler) {
	t.Helper()
	srv, h := controlServer(t, operatorPerms)
	if writer != nil {
		srv.SetItemKindWriter(writer)
	}
	if _, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{
			"stew": {Name: "stew", DisplayLabel: "Hearty Stew", Category: sim.ItemCategoryFood, SortOrder: 5,
				Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 10}}},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	return srv, h
}

func TestUmbilicalItemSet_CreateNewGood(t *testing.T) {
	fake := &fakeItemKindWriter{}
	srv, h := itemSetServer(t, fake)

	// Leading/trailing whitespace on name is trimmed → the catalog key is "shovel".
	rec := postReq(t, h, "/api/village/umbilical/item/set", "tok",
		`{"name":"  shovel  ","display_label":"Shovel","category":"tool","sort_order":0,"capabilities":["portable"],"display_label_singular":"shovel","display_label_plural":"shovels"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalItemDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Name != "shovel" || out.Category != "tool" || len(out.Satisfies) != 0 {
		t.Fatalf("response = %+v, want shovel/tool/no-satiation", out)
	}
	// The durable write got the trimmed def with its capability carried through.
	if fake.calls != 1 || fake.def.Name != "shovel" || fake.def.Category != "tool" ||
		len(fake.def.Capabilities) != 1 || fake.def.Capabilities[0] != "portable" {
		t.Fatalf("writer not called as expected: %+v", fake.def)
	}
	// The live catalog reflects the new good (keyed on the trimmed name).
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.ItemKinds["shovel"], nil
	}})
	if live, _ := res.(*sim.ItemKindDef); live == nil || live.Plural() != "shovels" {
		t.Errorf("live shovel def = %+v", res)
	}
}

func TestUmbilicalItemSet_EditPreservesSatiation(t *testing.T) {
	fake := &fakeItemKindWriter{}
	srv, h := itemSetServer(t, fake)

	// Re-label stew via item/set — the body carries no satiation, so the live
	// hunger entry must survive (it lives in item_satisfies, edited separately).
	rec := postReq(t, h, "/api/village/umbilical/item/set", "tok",
		`{"name":"stew","display_label":"Beef Stew","category":"food","sort_order":3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalItemDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Label != "Beef Stew" || len(out.Satisfies) != 1 || out.Satisfies[0].Amount != 10 {
		t.Fatalf("edit response = %+v, want relabeled + satiation preserved", out)
	}
	// The live catalog kept the satiation too.
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return len(world.ItemKinds["stew"].Satisfies), nil
	}})
	if n, _ := res.(int); n != 1 {
		t.Errorf("live stew satiation count = %d, want 1 (preserved)", n)
	}
}

func TestUmbilicalItemSet_Validation(t *testing.T) {
	_, h := itemSetServer(t, &fakeItemKindWriter{})
	cases := []struct{ name, body string }{
		{"missing name", `{"display_label":"X","category":"food"}`},
		{"blank name", `{"name":"   ","display_label":"X","category":"food"}`},
		{"missing display_label", `{"name":"x","category":"food"}`},
		{"missing category", `{"name":"x","display_label":"X"}`},
		{"name too long", `{"name":"` + strings.Repeat("a", 33) + `","display_label":"X","category":"food"}`},
		{"label too long", `{"name":"x","display_label":"` + strings.Repeat("a", 65) + `","category":"food"}`},
		{"category too long", `{"name":"x","display_label":"X","category":"` + strings.Repeat("a", 33) + `"}`},
		{"negative sort_order", `{"name":"x","display_label":"X","category":"food","sort_order":-1}`},
		{"blank capability token", `{"name":"x","display_label":"X","category":"food","capabilities":["  "]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postReq(t, h, "/api/village/umbilical/item/set", "tok", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s = %d, want 400; body=%s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestUmbilicalItemSet_Unwired(t *testing.T) {
	_, h := itemSetServer(t, nil) // writer not wired
	rec := postReq(t, h, "/api/village/umbilical/item/set", "tok",
		`{"name":"shovel","display_label":"Shovel","category":"tool"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unwired = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

func TestUmbilicalItemSet_WriterError(t *testing.T) {
	fake := &fakeItemKindWriter{err: errors.New("db down")}
	srv, h := itemSetServer(t, fake)
	rec := postReq(t, h, "/api/village/umbilical/item/set", "tok",
		`{"name":"shovel","display_label":"Shovel","category":"tool"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("writer error = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	// The durable write was attempted...
	if fake.calls != 1 {
		t.Errorf("writer calls = %d, want 1 (durable write attempted)", fake.calls)
	}
	// ...and because it failed, the in-memory catalog was NOT updated (write-first
	// contract: memory only changes once the durable write lands).
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		_, ok := world.ItemKinds["shovel"]
		return ok, nil
	}})
	if present, _ := res.(bool); present {
		t.Error("live catalog updated despite the write failure (want no shovel)")
	}
}

// The route table is the single source of truth for both registration and the
// manifest, so the new /item/set route must appear in the served manifest.
func TestUmbilicalItemSet_AppearsInManifest(t *testing.T) {
	_, h := itemSetServer(t, &fakeItemKindWriter{})
	rec := req(t, h, "/api/village/umbilical", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest status = %d, want 200", rec.Code)
	}
	var dto UmbilicalManifestDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, r := range dto.Routes {
		if r.Path == "/api/village/umbilical/item/set" {
			found = true
		}
	}
	if !found {
		t.Errorf("/umbilical/item/set missing from manifest: %+v", dto.Routes)
	}
}
