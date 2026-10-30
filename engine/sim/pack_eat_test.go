package sim_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// pack_eat_test.go — LLM-888: Eat / Drink from the Your Pack list.

const packEaterID sim.ActorID = "pc-eater"

func packEatCatalog() map[sim.ItemKind]*sim.ItemKindDef {
	return map[sim.ItemKind]*sim.ItemKindDef{
		"bread": {Name: "bread", Category: sim.ItemCategoryFood, Capabilities: []string{"portable"},
			Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 20}}},
		"ale": {Name: "ale", Category: sim.ItemCategoryDrink, Capabilities: []string{"portable"},
			Satisfies: []sim.ItemSatisfaction{{Attribute: "thirst", Immediate: 15}}},
		"raw_meat": {Name: "raw_meat", Category: sim.ItemCategoryFood},
		// A belly-filling drink, hunger listed first.
		"stout": {Name: "stout", Category: sim.ItemCategoryDrink, Capabilities: []string{"portable"},
			Satisfies: []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 10}, {Attribute: "thirst", Immediate: 10}}},
	}
}

// packEatWorld seeds a player holding two bread, an ale and some raw meat,
// with the needs given. Not run: the tests call UseItem's Fn directly.
func packEatWorld(t *testing.T, needs map[sim.NeedKey]int) *sim.World {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(packEatCatalog())
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		packEaterID: {
			ID: packEaterID, DisplayName: "Jeff", Kind: sim.KindPC, LoginUsername: "jeff",
			Pos: sim.Position{X: 50, Y: 50}, Facing: "south", State: sim.StateIdle,
			Inventory: map[sim.ItemKind]int{"bread": 2, "ale": 1, "raw_meat": 1},
			Needs:     needs,
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	return w
}

func TestItemActionsFoodAndDrink(t *testing.T) {
	catalog := packEatCatalog()
	cases := map[sim.ItemKind][]string{
		"bread":    {sim.ItemActionEat},
		"ale":      {sim.ItemActionDrink},
		"raw_meat": {},
		"stout":    {sim.ItemActionDrink},
	}
	for kind, want := range cases {
		if got := sim.ItemActions(catalog[kind], false); !slices.Equal(got, want) {
			t.Errorf("ItemActions(%s) = %v, want %v", kind, got, want)
		}
	}
}

// One press eats one unit, eases the need, and pushes the new needs to the
// player's top bar.
func TestEatFromPack(t *testing.T) {
	w := packEatWorld(t, map[sim.NeedKey]int{"hunger": 60, "thirst": 40})
	var pushed []*sim.PCNeedsChanged
	var consumed []*sim.ItemConsumed
	w.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
		switch e := evt.(type) {
		case *sim.PCNeedsChanged:
			pushed = append(pushed, e)
		case *sim.ItemConsumed:
			consumed = append(consumed, e)
		}
	}))
	now := time.Now()

	if _, err := sim.UseItem(packEaterID, "bread", sim.ItemActionEat, now).Fn(w); err != nil {
		t.Fatalf("eat bread: %v", err)
	}
	a := w.Actors[packEaterID]
	hunger := a.Needs["hunger"]
	if a.Inventory["bread"] != 1 || hunger >= 60 {
		t.Errorf("after eating: bread %d hunger %d, want 1 and below 60", a.Inventory["bread"], hunger)
	}
	if len(consumed) != 1 || consumed[0].Qty != 1 || consumed[0].Kind != "bread" {
		t.Errorf("ItemConsumed = %+v, want one bread", consumed)
	}
	if len(pushed) != 1 || pushed[0].ActorID != packEaterID || pushed[0].Needs["hunger"] != hunger {
		t.Fatalf("PCNeedsChanged = %+v, want one with hunger %d", pushed, hunger)
	}
	// The pushed needs are a copy: a later change to the actor does not reach it.
	a.Needs["hunger"] = 99
	if pushed[0].Needs["hunger"] != hunger {
		t.Errorf("PCNeedsChanged shares the actor's needs map")
	}

	if _, err := sim.UseItem(packEaterID, "ale", sim.ItemActionDrink, now).Fn(w); err != nil {
		t.Fatalf("drink ale: %v", err)
	}
	if _, held := a.Inventory["ale"]; held || a.Needs["thirst"] >= 40 {
		t.Errorf("after drinking: ale held %v thirst %d, want gone and below 40", held, a.Needs["thirst"])
	}
}

// A drink that eases hunger and thirst is drunk when either is felt, and
// refused as a drink when neither is.
func TestDrinkEasingTwoNeeds(t *testing.T) {
	now := time.Now()
	for _, need := range []sim.NeedKey{"hunger", "thirst"} {
		w := packEatWorld(t, map[sim.NeedKey]int{need: 30})
		w.Actors[packEaterID].Inventory["stout"] = 1
		if _, err := sim.UseItem(packEaterID, "stout", sim.ItemActionDrink, now).Fn(w); err != nil {
			t.Errorf("only %s felt: %v, want drunk", need, err)
		}
		if n := w.Actors[packEaterID].Inventory["stout"]; n != 0 {
			t.Errorf("only %s felt: %d stout left, want 0", need, n)
		}
	}
	w := packEatWorld(t, nil)
	w.Actors[packEaterID].Inventory["stout"] = 1
	_, err := sim.UseItem(packEaterID, "stout", sim.ItemActionDrink, now).Fn(w)
	if !errors.Is(err, sim.ErrItemAction) || err.Error() != "You are not thirsty." {
		t.Errorf("neither felt: err = %v, want \"You are not thirsty.\"", err)
	}
	if n := w.Actors[packEaterID].Inventory["stout"]; n != 1 {
		t.Errorf("neither felt: %d stout left, want 1", n)
	}
}

func TestEatFromPackRefusals(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name   string
		needs  map[sim.NeedKey]int
		walk   bool
		kind   sim.ItemKind
		action string
		want   string
	}{
		{"sated", map[sim.NeedKey]int{"thirst": 30}, false, "bread", sim.ItemActionEat, "You are not hungry."},
		{"not thirsty", map[sim.NeedKey]int{"hunger": 30}, false, "ale", sim.ItemActionDrink, "You are not thirsty."},
		{"walking", map[sim.NeedKey]int{"hunger": 30}, true, "bread", sim.ItemActionEat, "Stop walking to eat."},
		{"walking, a drink", map[sim.NeedKey]int{"thirst": 30}, true, "ale", sim.ItemActionDrink, "Stop walking to drink."},
		{"drink offered only to a drink", map[sim.NeedKey]int{"hunger": 30}, false, "bread", sim.ItemActionDrink, "You cannot do that with it."},
		{"raw meat is not food", map[sim.NeedKey]int{"hunger": 30}, false, "raw_meat", sim.ItemActionEat, "You cannot do that with it."},
	}
	for _, c := range cases {
		w := packEatWorld(t, c.needs)
		a := w.Actors[packEaterID]
		if c.walk {
			a.MoveIntent = &sim.MoveIntent{}
		}
		before := maps.Clone(a.Inventory)
		_, err := sim.UseItem(packEaterID, c.kind, c.action, now).Fn(w)
		if !errors.Is(err, sim.ErrItemAction) || err.Error() != c.want {
			t.Errorf("%s: err = %v, want ErrItemAction %q", c.name, err, c.want)
		}
		if got := a.Inventory; !maps.Equal(got, before) {
			t.Errorf("%s: pack changed %v → %v", c.name, before, got)
		}
	}
}
