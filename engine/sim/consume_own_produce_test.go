package sim_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// consume_own_produce_test.go — LLM-267. A wholesaler owner cannot eat its own
// produce: the item is stock to sell, not its larder. Covers the pure predicate
// (sim.IsOwnProduce) and the Consume command guard that rejects on it. The
// perception half (the eat cue never offering own produce) is covered by
// TestWholesalerNeverCuedToEatOwnProduce in perception/golden_test.go.

func TestIsOwnProduce(t *testing.T) {
	objects := map[sim.VillageObjectID]*sim.VillageObject{
		// A live farm carries both tags; only wholesaler gates the block.
		"james_farm": {ID: "james_farm", OwnerActorID: "moses", Tags: []string{sim.TagFarm, sim.TagWholesaler}},
		// A retailer/keeper workplace — no wholesaler tag.
		"the_inn": {ID: "the_inn", OwnerActorID: "john"},
	}
	policy := &sim.RestockPolicy{Restock: []sim.RestockEntry{
		{Item: "bread", Source: sim.RestockSourceProduce, Max: 40}, // produce row
		{Item: "wheat", Source: sim.RestockSourceBuy, Max: 20},     // buy row, not produce
	}}

	cases := []struct {
		name   string
		work   sim.StructureID
		policy *sim.RestockPolicy
		kind   sim.ItemKind
		want   bool
	}{
		{"wholesaler own produce", "james_farm", policy, "bread", true},
		{"wholesaler buy-row is not produce", "james_farm", policy, "wheat", false},
		{"wholesaler item not in policy", "james_farm", policy, "stew", false},
		{"non-wholesaler workplace", "the_inn", policy, "bread", false},
		{"no workplace", "", policy, "bread", false},
		{"nil policy", "james_farm", nil, "bread", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sim.IsOwnProduce(objects, tc.work, tc.policy, tc.kind); got != tc.want {
				t.Errorf("IsOwnProduce(work=%q, kind=%q) = %v, want %v", tc.work, tc.kind, got, tc.want)
			}
		})
	}
}

// makeWholesaler sets an actor's work anchor + produce policy and tags its workplace
// wholesaler on the world goroutine — the fields buildConsumeTestWorld's spec doesn't
// carry (the tagWholesaleWorld idiom).
func makeWholesaler(t *testing.T, w *sim.World, actorID sim.ActorID, work sim.VillageObjectID, produce sim.ItemKind, wholesaler bool) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		if world.VillageObjects == nil {
			world.VillageObjects = map[sim.VillageObjectID]*sim.VillageObject{}
		}
		a := world.Actors[actorID]
		a.WorkStructureID = sim.StructureID(work)
		a.RestockPolicy = &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: produce, Source: sim.RestockSourceProduce, Max: 40},
		}}
		tags := []string(nil)
		if wholesaler {
			tags = []string{sim.TagFarm, sim.TagWholesaler}
		}
		world.VillageObjects[work] = &sim.VillageObject{ID: work, OwnerActorID: actorID, Tags: tags}
		return nil, nil
	}}); err != nil {
		t.Fatalf("makeWholesaler setup: %v", err)
	}
}

// TestConsume_WholesalerOwnProduceRejected: a wholesaler owner is blocked from
// eating its own produce even at starvation (no red-need escape), but a consumable
// it did NOT produce stays edible.
func TestConsume_WholesalerOwnProduceRejected(t *testing.T) {
	w, stop := buildConsumeTestWorld(t, []consumeActorSpec{
		{
			id: "moses", displayName: "Moses James",
			inventory: map[sim.ItemKind]int{"bread": 40, "stew": 3},
			needs:     map[sim.NeedKey]int{"hunger": 30}, // well past red — the guard ignores need level
		},
	}, nil)
	defer stop()
	makeWholesaler(t, w, "moses", "james_farm", "bread", true)

	at := time.Now().UTC()

	// Own produce (bread) is blocked — even at starvation.
	_, err := w.Send(sim.Consume("moses", "bread", 1, at))
	if !errors.Is(err, sim.ErrOwnProduceStock) {
		t.Fatalf("consume of own produce err = %v, want ErrOwnProduceStock", err)
	}
	if !strings.Contains(err.Error(), "stock to sell") {
		t.Errorf("reject message should steer to a real food source, got %q", err.Error())
	}

	// A consumable NOT in the produce rows (bought/foraged stew) is still eatable.
	if _, err := w.Send(sim.Consume("moses", "stew", 1, at)); err != nil {
		t.Errorf("consume of non-produce food err = %v, want success", err)
	}
}

// TestConsume_NonWholesalerProducerMayEatOwnProduce: the block is scoped to
// wholesaler-tagged workplaces. A retailer/keeper baking its own bread (John Ellis)
// is not wholesale-gated, so its own bread stays edible — natural innkeeping.
func TestConsume_NonWholesalerProducerMayEatOwnProduce(t *testing.T) {
	w, stop := buildConsumeTestWorld(t, []consumeActorSpec{
		{
			id: "john", displayName: "John Ellis",
			inventory: map[sim.ItemKind]int{"bread": 10},
			needs:     map[sim.NeedKey]int{"hunger": 14},
		},
	}, nil)
	defer stop()
	makeWholesaler(t, w, "john", "the_inn", "bread", false) // not a wholesaler workplace

	if _, err := w.Send(sim.Consume("john", "bread", 1, time.Now().UTC())); err != nil {
		t.Errorf("non-wholesaler eating its own bread err = %v, want success", err)
	}
}
