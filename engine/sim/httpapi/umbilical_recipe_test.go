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

// TestUmbilicalRecipeSet_BoostInputsRoundTrip covers the LLM-248 optional-booster
// wire shape: boost_inputs accepted on set, canonicalized, passed to the writer,
// echoed in the response, and visible on the /recipes read side.
func TestUmbilicalRecipeSet_BoostInputsRoundTrip(t *testing.T) {
	fake := &fakeRecipeWriter{}
	srv, h := recipeServer(t, fake)

	rec := postReq(t, h, "/api/village/umbilical/recipe/set", "tok",
		`{"output_item":"cheese","output_qty":4,"rate_qty":4,"rate_per_hours":1,"inputs":[],"boost_inputs":[{"item":"Milk","qty":1,"bonus_qty":2}],"wholesale_price":2,"retail_price":4}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalRecipeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// "Milk" canonicalized to the catalog key.
	if len(out.BoostInputs) != 1 || out.BoostInputs[0].Item != "milk" || out.BoostInputs[0].Qty != 1 || out.BoostInputs[0].BonusQty != 2 {
		t.Fatalf("response boost_inputs = %+v, want [{milk 1 2}]", out.BoostInputs)
	}
	if len(fake.last.BoostInputs) != 1 || fake.last.BoostInputs[0].Item != "milk" {
		t.Fatalf("writer boost_inputs = %+v", fake.last.BoostInputs)
	}
	// The live catalog carries it too.
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		r := world.Recipes["cheese"]
		return r != nil && len(r.BoostInputs) == 1 && r.BoostInputs[0].BonusQty == 2, nil
	}})
	if ok, _ := res.(bool); !ok {
		t.Error("world.Recipes cheese missing the booster after set")
	}
}

// TestUmbilicalRecipeSet_SpeedInputsRoundTrip covers the LLM-511 speed-booster
// wire shape: speed_inputs accepted on set, canonicalized, passed to the writer,
// echoed in the response, and visible in the live catalog.
func TestUmbilicalRecipeSet_SpeedInputsRoundTrip(t *testing.T) {
	fake := &fakeRecipeWriter{}
	srv, h := recipeServer(t, fake)

	rec := postReq(t, h, "/api/village/umbilical/recipe/set", "tok",
		`{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":4,"inputs":[],"speed_inputs":[{"item":"Milk","qty":1,"rate_pct":200}],"wholesale_price":6,"retail_price":12}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out umbilicalRecipeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// "Milk" canonicalized to the catalog key.
	if len(out.SpeedInputs) != 1 || out.SpeedInputs[0].Item != "milk" || out.SpeedInputs[0].Qty != 1 || out.SpeedInputs[0].RatePct != 200 {
		t.Fatalf("response speed_inputs = %+v, want [{milk 1 200}]", out.SpeedInputs)
	}
	if len(fake.last.SpeedInputs) != 1 || fake.last.SpeedInputs[0].Item != "milk" || fake.last.SpeedInputs[0].RatePct != 200 {
		t.Fatalf("writer speed_inputs = %+v", fake.last.SpeedInputs)
	}
	res, _ := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		r := world.Recipes["cheese"]
		return r != nil && len(r.SpeedInputs) == 1 && r.SpeedInputs[0].RatePct == 200, nil
	}})
	if ok, _ := res.(bool); !ok {
		t.Error("world.Recipes cheese missing the speed input after set")
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
		{"boost qty zero", `{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1,"boost_inputs":[{"item":"milk","qty":0,"bonus_qty":1}]}`},
		{"boost bonus_qty zero", `{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1,"boost_inputs":[{"item":"milk","qty":1,"bonus_qty":0}]}`},
		{"boost missing item", `{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1,"boost_inputs":[{"qty":1,"bonus_qty":1}]}`},
		{"speed qty zero", `{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1,"speed_inputs":[{"item":"milk","qty":0,"rate_pct":200}]}`},
		{"speed rate_pct at 100", `{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1,"speed_inputs":[{"item":"milk","qty":1,"rate_pct":100}]}`},
		{"speed rate_pct below 100", `{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1,"speed_inputs":[{"item":"milk","qty":1,"rate_pct":50}]}`},
		{"speed rate_pct above the max", `{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1,"speed_inputs":[{"item":"milk","qty":1,"rate_pct":1001}]}`},
		{"speed missing item", `{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1,"speed_inputs":[{"qty":1,"rate_pct":200}]}`},
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
		{"unknown boost input", `{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1,"boost_inputs":[{"item":"unobtanium","qty":1,"bonus_qty":1}]}`},
		{"boost duplicates required input", `{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1,"inputs":[{"item":"milk","qty":3}],"boost_inputs":[{"item":"milk","qty":1,"bonus_qty":1}]}`},
		{"unknown speed input", `{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1,"speed_inputs":[{"item":"unobtanium","qty":1,"rate_pct":200}]}`},
		{"speed duplicates required input", `{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1,"inputs":[{"item":"milk","qty":3}],"speed_inputs":[{"item":"milk","qty":1,"rate_pct":200}]}`},
		{"speed duplicates boost input", `{"output_item":"cheese","output_qty":1,"rate_qty":1,"rate_per_hours":1,"boost_inputs":[{"item":"milk","qty":1,"bonus_qty":2}],"speed_inputs":[{"item":"milk","qty":1,"rate_pct":200}]}`},
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

// ---- Recipe read (LLM-110) ----------------------------------------------

// seedRecipe installs one recipe into the live catalog for the read tests.
func seedRecipe(t *testing.T, srv *Server, r sim.ItemRecipe) {
	t.Helper()
	if _, err := srv.world.Send(sim.SetRecipe(r)); err != nil {
		t.Fatalf("seed recipe %s: %v", r.OutputItem, err)
	}
}

func TestUmbilicalRecipes_ListAndFilter(t *testing.T) {
	srv, h := recipeServer(t, nil)
	seedRecipe(t, srv, sim.ItemRecipe{OutputItem: "cheese", OutputQty: 1, RateQty: 1, RatePerHours: 2, Inputs: []sim.RecipeInput{{Item: "milk", Qty: 3}}, WholesalePrice: 4, RetailPrice: 7})
	seedRecipe(t, srv, sim.ItemRecipe{OutputItem: "axe", OutputQty: 1, RateQty: 1, RatePerHours: 6, WholesalePrice: 5, RetailPrice: 9})

	// Full catalog, sorted by output item (axe before cheese).
	rec := req(t, h, "/api/village/umbilical/recipes", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("recipes = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalRecipesDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != 2 || len(out.Recipes) != 2 {
		t.Fatalf("total=%d recipes=%d, want 2/2", out.Total, len(out.Recipes))
	}
	if out.Recipes[0].OutputItem != "axe" || out.Recipes[1].OutputItem != "cheese" {
		t.Fatalf("not sorted by output item: %s, %s", out.Recipes[0].OutputItem, out.Recipes[1].OutputItem)
	}
	if out.Recipes[1].RetailPrice != 7 || len(out.Recipes[1].Inputs) != 1 || out.Recipes[1].Inputs[0].Item != "milk" {
		t.Fatalf("cheese recipe wrong: %+v", out.Recipes[1])
	}

	// ?item= filters to one (case-insensitive against the canonical key).
	rec = req(t, h, "/api/village/umbilical/recipes?item=CHEESE", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("recipes?item = %d, want 200", rec.Code)
	}
	out = UmbilicalRecipesDTO{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode filtered: %v", err)
	}
	if out.Total != 1 || out.Recipes[0].OutputItem != "cheese" {
		t.Fatalf("filter = %+v, want only cheese", out.Recipes)
	}

	// Unknown item → empty list, still 200.
	rec = req(t, h, "/api/village/umbilical/recipes?item=dragonfruit", "tok")
	out = UmbilicalRecipesDTO{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || out.Total != 0 {
		t.Fatalf("unknown filter = %d total=%d, want 200/0", rec.Code, out.Total)
	}
}

func TestUmbilicalRecipes_EmptyCatalog(t *testing.T) {
	_, h := recipeServer(t, nil) // ItemKinds seeded, no recipes
	rec := req(t, h, "/api/village/umbilical/recipes", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("recipes = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out UmbilicalRecipesDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != 0 || len(out.Recipes) != 0 {
		t.Fatalf("empty catalog total=%d, want 0", out.Total)
	}
}
