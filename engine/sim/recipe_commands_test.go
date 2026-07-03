package sim_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// recipe_commands_test.go — sim-level coverage of the recipe-edit helpers
// (LLM-97): ResolveRecipe (catalog-reference validation + canonicalization) and
// SetRecipe (in-memory catalog install). The durable item_recipe write is
// covered in repo/pg; the end-to-end route in httpapi.

func buildRecipeTestWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"cheese": {Name: "cheese", DisplayLabel: "Cheese"},
		"milk":   {Name: "milk", DisplayLabel: "Milk"},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	return w, func() { cancel(); <-done }
}

func TestResolveRecipe_CanonicalizesAndValidates(t *testing.T) {
	w, stop := buildRecipeTestWorld(t)
	defer stop()

	// Label-cased item names resolve to canonical catalog keys.
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.ResolveRecipe(world, sim.ItemRecipe{
			OutputItem: "Cheese", OutputQty: 1, RateQty: 1, RatePerHours: 1,
			Inputs: []sim.RecipeInput{{Item: "Milk", Qty: 2}},
		})
	}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := res.(sim.ItemRecipe)
	if got.OutputItem != "cheese" || len(got.Inputs) != 1 || got.Inputs[0].Item != "milk" {
		t.Fatalf("canonicalized = %+v, want cheese/milk keys", got)
	}

	// Unknown output and unknown input both wrap ErrUnknownItemKind.
	_, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.ResolveRecipe(world, sim.ItemRecipe{OutputItem: "dragonfruit", OutputQty: 1, RateQty: 1, RatePerHours: 1})
	}})
	if !errors.Is(err, sim.ErrUnknownItemKind) {
		t.Fatalf("unknown output err = %v, want ErrUnknownItemKind", err)
	}
	_, err = w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.ResolveRecipe(world, sim.ItemRecipe{
			OutputItem: "cheese", OutputQty: 1, RateQty: 1, RatePerHours: 1,
			Inputs: []sim.RecipeInput{{Item: "unobtanium", Qty: 1}},
		})
	}})
	if !errors.Is(err, sim.ErrUnknownItemKind) {
		t.Fatalf("unknown input err = %v, want ErrUnknownItemKind", err)
	}
}

// ResolveRecipe boost-input coverage (LLM-248): canonicalization, the
// required/optional overlap guard, and — unlike required inputs, whose numeric
// validation stays with the HTTP handler — positive qty/bonus_qty enforcement,
// so a non-HTTP SetRecipe caller can't install a booster the DB would reject
// and the tick would silently skip (code_review).
func TestResolveRecipe_BoostInputs(t *testing.T) {
	w, stop := buildRecipeTestWorld(t)
	defer stop()

	// Happy path: label-cased booster canonicalizes.
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.ResolveRecipe(world, sim.ItemRecipe{
			OutputItem: "cheese", OutputQty: 1, RateQty: 1, RatePerHours: 1,
			BoostInputs: []sim.BoostInput{{Item: "Milk", Qty: 1, BonusQty: 2}},
		})
	}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := res.(sim.ItemRecipe)
	if len(got.BoostInputs) != 1 || got.BoostInputs[0].Item != "milk" {
		t.Fatalf("canonicalized boosts = %+v, want milk key", got.BoostInputs)
	}

	cases := []struct {
		name  string
		boost sim.BoostInput
		input []sim.RecipeInput
	}{
		{"unknown booster", sim.BoostInput{Item: "unobtanium", Qty: 1, BonusQty: 1}, nil},
		{"zero qty", sim.BoostInput{Item: "milk", Qty: 0, BonusQty: 1}, nil},
		{"zero bonus_qty", sim.BoostInput{Item: "milk", Qty: 1, BonusQty: 0}, nil},
		{"overlaps required input", sim.BoostInput{Item: "milk", Qty: 1, BonusQty: 1},
			[]sim.RecipeInput{{Item: "milk", Qty: 3}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
				return sim.ResolveRecipe(world, sim.ItemRecipe{
					OutputItem: "cheese", OutputQty: 1, RateQty: 1, RatePerHours: 1,
					Inputs:      tc.input,
					BoostInputs: []sim.BoostInput{tc.boost},
				})
			}})
			if err == nil {
				t.Errorf("%s: ResolveRecipe accepted an invalid booster", tc.name)
			}
		})
	}
}

func TestSetRecipe_InstallsIntoCatalog(t *testing.T) {
	w, stop := buildRecipeTestWorld(t)
	defer stop()

	if _, err := w.Send(sim.SetRecipe(sim.ItemRecipe{OutputItem: "cheese", OutputQty: 2, RateQty: 1, RatePerHours: 3})); err != nil {
		t.Fatalf("set: %v", err)
	}
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		r := world.Recipes["cheese"]
		if r == nil {
			return -1, nil
		}
		return r.OutputQty, nil
	}})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if q, _ := res.(int); q != 2 {
		t.Fatalf("cheese OutputQty = %d, want 2 (recipe not installed)", q)
	}
}
