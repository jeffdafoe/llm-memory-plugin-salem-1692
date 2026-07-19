package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// produce_tick_hearth_boost_test.go — LLM-474 simulation-level coverage of the
// hearth_lit BoostState in the shape the migration ships (porridge: base yield
// with boost [{hearth_lit,+3}]).
//
// The generic booster mechanics live in produce_tick_booster_test.go; what these
// pin is what is DIFFERENT about a state booster:
//
//   - it consumes nothing (the firewood was spent by an earlier stoke);
//   - it reads the WORK structure's fire, not the actor's inventory;
//   - a structure with no hearth object behaves exactly as it did before;
//   - and above all it NEVER gates — a full base batch lands over a stone-cold
//     hearth. Food is the survival good; the deadlock reasoning is on LLM-474
//     and the sibling rule is LLM-444's.

// hearthBoostBonus is the porridge bonus these tests seed, matching the shipped
// migration so a tuning change to one is a visible mismatch with the other.
const hearthBoostBonus = 3

// porridgeBaseQty is the recipe's unboosted batch — the quantity that must land
// regardless of the fire.
const porridgeBaseQty = 10

// buildHearthCookWorld seeds a Hannah-shaped innkeeper at her Inn with the
// hearth-boosted porridge recipe. hearthTagged decides whether the Inn carries a
// fireplace at all; litFor sets how long the fire still burns from now (negative
// or zero = out).
func buildHearthCookWorld(t *testing.T, porridgeCap int, hearthTagged bool, litFor time.Duration) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"porridge": {Name: "porridge", DisplayLabel: "porridge", DisplayLabelSingular: "bowl of porridge", DisplayLabelPlural: "porridge", Category: sim.ItemCategoryFood, SortOrder: 200},
		"firewood": {Name: "firewood", DisplayLabel: "firewood", DisplayLabelSingular: "stick of firewood", DisplayLabelPlural: "firewood", Category: sim.ItemCategoryMaterial, SortOrder: 400},
	})
	handles.Recipes.Seed(map[sim.ItemKind]*sim.ItemRecipe{
		"porridge": {
			OutputItem:   "porridge",
			OutputQty:    porridgeBaseQty,
			RateQty:      10,
			RatePerHours: 1,
			BoostState:   []sim.BoostState{{State: sim.BoostStateHearthLit, BonusQty: hearthBoostBonus}},
		},
	})
	tags := []string{sim.TagBusiness}
	if hearthTagged {
		tags = append(tags, sim.TagHearth)
	}
	inn := &sim.VillageObject{ID: "inn", DisplayName: "Inn", Pos: sim.WorldPos{X: 320, Y: 320}, Tags: tags}
	if hearthTagged {
		inn.HearthLitUntil = time.Now().UTC().Add(litFor)
	}
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{"inn": inn})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"inn": {ID: "inn", DisplayName: "Inn"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:                "hannah",
			LLMAgent:          "hannah-inn",
			Kind:              sim.KindNPCStateful,
			InsideStructureID: "inn",
			WorkStructureID:   "inn",
			Inventory:         map[sim.ItemKind]int{},
			RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: "porridge", Source: sim.RestockSourceProduce, Max: porridgeCap},
			}},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// landPorridgeCycle starts hannah's porridge cycle and drives it to landing.
func landPorridgeCycle(t *testing.T, w *sim.World) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := w.Send(sim.StartProductionCycle("hannah", "porridge", "", false)); err != nil {
		t.Fatalf("StartProductionCycle: %v", err)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hannah"].ProductionActivity.LastProgressAt = now.Add(-24 * time.Hour)
		return nil, nil
	}}); err != nil {
		t.Fatalf("backdate cycle: %v", err)
	}
	if _, err := w.Send(sim.ApplyProduceTick(now)); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

// hannahPorridge reads hannah's landed porridge off the world goroutine.
func hannahPorridge(t *testing.T, w *sim.World) int {
	t.Helper()
	got, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["hannah"].Inventory["porridge"], nil
	}})
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	qty, _ := got.(int)
	return qty
}

// TestBoostStateNeverGatesProduction is the load-bearing invariant of LLM-474,
// and the reason the ticket chose a bonus over a gate. Across every fire state
// that is NOT lit — a hearth burned out, and a kitchen with no hearth at all —
// a full base batch must still land. If this test ever fails, the village has
// been put one cold morning away from a food shutdown; fix the code, never the
// expectation.
func TestBoostStateNeverGatesProduction(t *testing.T) {
	cases := []struct {
		name         string
		hearthTagged bool
		litFor       time.Duration
	}{
		{"fire burned out", true, -time.Hour},
		{"no hearth in the kitchen at all", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, cancel := buildHearthCookWorld(t, 100, tc.hearthTagged, tc.litFor)
			defer cancel()
			landPorridgeCycle(t, w)
			if got := hannahPorridge(t, w); got != porridgeBaseQty {
				t.Fatalf("porridge landed = %d, want the full base batch %d — an unmet boost state must never reduce or block production", got, porridgeBaseQty)
			}
		})
	}
}

// TestBoostStateHearthLitAddsBonus pins the payout: a live fire at landing mints
// the bonus on top of the base batch, and consumes nothing to do it.
func TestBoostStateHearthLitAddsBonus(t *testing.T) {
	w, cancel := buildHearthCookWorld(t, 100, true, 4*time.Hour)
	defer cancel()
	landPorridgeCycle(t, w)
	want := porridgeBaseQty + hearthBoostBonus
	if got := hannahPorridge(t, w); got != want {
		t.Fatalf("porridge landed = %d, want %d (base %d + hearth bonus %d)", got, want, porridgeBaseQty, hearthBoostBonus)
	}
	// Nothing was spent for it — the distinguishing property against BoostInputs.
	got, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["hannah"].Inventory[sim.FirewoodItemKind], nil
	}})
	if err != nil {
		t.Fatalf("read firewood: %v", err)
	}
	if wood, _ := got.(int); wood != 0 {
		t.Fatalf("firewood = %d, want 0 — a state booster consumes nothing; its cost was paid at the stoke", wood)
	}
}

// TestBoostStateClampedToCapHeadroom pins the cap clamp: the bonus may fill
// remaining headroom but never overshoot the entry's carry cap, matching the
// BoostInputs clamp so the two boosters can't disagree about what a cap means.
func TestBoostStateClampedToCapHeadroom(t *testing.T) {
	// Cap 11 against a 10-unit base batch leaves room for 1 of the 3 bonus.
	w, cancel := buildHearthCookWorld(t, porridgeBaseQty+1, true, 4*time.Hour)
	defer cancel()
	landPorridgeCycle(t, w)
	if got := hannahPorridge(t, w); got != porridgeBaseQty+1 {
		t.Fatalf("porridge landed = %d, want %d — the bonus must clamp to cap headroom", got, porridgeBaseQty+1)
	}
}
