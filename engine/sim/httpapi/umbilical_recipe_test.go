package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_recipe_test.go — handler coverage for the recipe-edit control route
// (LLM-97). The durable item_recipe write is covered against real pg in
// repo/pg/refdata_integration_test.go; here a fake RecipeWriter stands in so the
// tests assert the HTTP plumbing: validation, item-kind resolution, the
// write-then-update ordering, and status mapping.

// fakeRecipeWriter records the upserted recipe, or returns a forced error.
type fakeRecipeWriter struct {
	last  sim.ItemRecipe
	calls int
	err   error
}

func (f *fakeRecipeWriter) UpsertRecipe(_ context.Context, r sim.ItemRecipe) error {
	if f.err != nil {
		return f.err
	}
	f.last = r
	f.calls++
	return nil
}

// recipeServer builds a control-enabled server with the given writer (nil =
// leave it unwired) and a catalog of cheese (output) + milk (input).
func recipeServer(t *testing.T, writer RecipeWriter) (*Server, http.Handler) {
	t.Helper()
	srv, h := controlServer(t, operatorPerms)
	if writer != nil {
		srv.SetRecipeWriter(writer)
	}
	_, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{
			"cheese": {Name: "cheese", DisplayLabel: "Cheese"},
			"milk":   {Name: "milk", DisplayLabel: "Milk"},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("seed catalog: %v", err)
	}
	return srv, h
}

func TestUmbilicalRecipeSet_AddAndEdit(t *testing.T) {
	fake := &fakeRecipeWriter{}
	srv, h := recipeServer(t, fake)

	rec := postReq(t, h, "/api/village/umbilical/recipe/set", "tok",
		`{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":2,"inputs":[{"item":"milk","qty":3}],"wholesale_price":4,"retail_price":7}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("add = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalRecipeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.OutputItem != "cheese" || out.RatePerHours != 2 || len(out.Inputs) != 1 || out.Inputs[0].Item != "milk" {
		t.Fatalf("response = %+v", out)
	}
	if fake.calls != 1 || fake.last.OutputItem != "cheese" {
		t.Fatalf("writer not called as expected: %+v", fake)
	}
	// The live catalog reflects it.
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		_, ok := world.Recipes["cheese"]
		return ok, nil
	}})
	if has, _ := res.(bool); !has {
		t.Error("world.Recipes has no cheese after set")
	}

	// Edit the same output_item — fields update, no second recipe.
	rec = postReq(t, h, "/api/village/umbilical/recipe/set", "tok",
		`{"output_item":"cheese","output_qty":2,"rate_qty":5,"rate_per_hours":1,"inputs":[],"wholesale_price":6,"retail_price":11}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("edit = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if fake.calls != 2 || fake.last.OutputQty != 2 || len(fake.last.Inputs) != 0 {
		t.Fatalf("edit not applied to writer: %+v", fake.last)
	}
}

func TestUmbilicalRecipeSet_Validation(t *testing.T) {
	_, h := recipeServer(t, &fakeRecipeWriter{})
	cases := []struct{ name, body string }{
		{"missing output", `{"output_qty":1,"rate_qty":1,"rate_per_hours":1}`},
		{"zero output_qty", `{"output_item":"cheese","output_qty":0,"rate_qty":1,"rate_per_hours":1}`},
		{"zero rate_qty", `{"output_item":"cheese","output_qty":1,"rate_qty":0,"rate_per_hours":1}`},
		{"zero rate_per_hours", `{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":0}`},
		{"negative price", `{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1,"wholesale_price":-1}`},
		{"input qty zero", `{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1,"inputs":[{"item":"milk","qty":0}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postReq(t, h, "/api/village/umbilical/recipe/set", "tok", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s = %d, want 400; body=%s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestUmbilicalRecipeSet_UnknownItems(t *testing.T) {
	_, h := recipeServer(t, &fakeRecipeWriter{})
	cases := []struct{ name, body string }{
		{"unknown output", `{"output_item":"dragonfruit","output_qty":1,"rate_qty":1,"rate_per_hours":1}`},
		{"unknown input", `{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1,"inputs":[{"item":"unobtanium","qty":1}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postReq(t, h, "/api/village/umbilical/recipe/set", "tok", tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("%s = %d, want 422; body=%s", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestUmbilicalRecipeSet_Unwired(t *testing.T) {
	_, h := recipeServer(t, nil) // writer not wired
	rec := postReq(t, h, "/api/village/umbilical/recipe/set", "tok",
		`{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unwired = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

func TestUmbilicalRecipeSet_WriterError(t *testing.T) {
	_, h := recipeServer(t, &fakeRecipeWriter{err: errors.New("db down")})
	rec := postReq(t, h, "/api/village/umbilical/recipe/set", "tok",
		`{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1}`)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("writer error = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}
