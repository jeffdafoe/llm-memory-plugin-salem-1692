package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildBerryBushWorld seeds a single two-state "bush" asset (berries/bare,
// tag-driven) and one bush instance carrying a finite hunger refresh that is
// also gatherable, plus a hungry actor on the bush's loiter pin. available is
// the starting berry stock; currentState is the seeded visual; lastRefresh is
// the regen anchor (nil = freshly loaded).
func buildBerryBushWorld(t *testing.T, available int, currentState string, lastRefresh *time.Time) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"bush-2state": {
			ID:           "bush-2state",
			Name:         "Bush",
			DefaultState: "berries",
			States: []sim.AssetState{
				{State: "berries", Tags: []string{sim.TagBerries}},
				{State: "bare", Tags: []string{sim.TagBare}},
			},
		},
	})
	zero := 0
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"bush": {
			ID: "bush", DisplayName: "Bush", AssetID: "bush-2state", CurrentState: currentState,
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 100, Y: 100},
			Refreshes: []*sim.ObjectRefresh{
				{
					Attribute:          "hunger",
					Amount:             -8,
					AvailableQuantity:  ip(available),
					MaxQuantity:        ip(3),
					RefreshMode:        sim.RefreshModePeriodic,
					RefreshPeriodHours: ip(6),
					LastRefreshAt:      lastRefresh,
					GatherItem:         "berries",
				},
			},
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"forager": {
			ID:    "forager",
			Needs: map[sim.NeedKey]int{"hunger": 20},
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

// bushState reads the bush's live CurrentState off the world goroutine.
func bushState(t *testing.T, w *sim.World) string {
	t.Helper()
	s, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.VillageObjects["bush"].CurrentState, nil
	}})
	if err != nil {
		t.Fatalf("read bush state: %v", err)
	}
	return s.(string)
}

// TestBerryStateEatingLastBerryGoesBare: eating the last unit of stock at
// arrival drains the bush, so its visual flips berries -> bare.
func TestBerryStateEatingLastBerryGoesBare(t *testing.T) {
	w, cancel := buildBerryBushWorld(t, 1, "berries", nil)
	defer cancel()

	if got := bushState(t, w); got != "berries" {
		t.Fatalf("initial bush state = %q, want berries", got)
	}
	placeAtObjectPin(t, w, "forager", "bush")
	if _, err := w.Send(sim.ApplyObjectRefreshAtArrival("forager")); err != nil {
		t.Fatalf("arrival: %v", err)
	}
	if got := bushState(t, w); got != "bare" {
		t.Errorf("after eating the last berry, bush state = %q, want bare", got)
	}
}

// TestBerryStateEatingWithStockLeftStaysBerries: eating one of several units
// leaves stock behind, so the bush stays berried (no spurious flip).
func TestBerryStateEatingWithStockLeftStaysBerries(t *testing.T) {
	w, cancel := buildBerryBushWorld(t, 3, "berries", nil)
	defer cancel()

	placeAtObjectPin(t, w, "forager", "bush")
	if _, err := w.Send(sim.ApplyObjectRefreshAtArrival("forager")); err != nil {
		t.Fatalf("arrival: %v", err)
	}
	if got := bushState(t, w); got != "berries" {
		t.Errorf("after eating 1 of 3, bush state = %q, want berries", got)
	}
}

// TestBerryStateRegrowGoesBerries: a depleted (bare) bush flips back to berries
// when the periodic regen restocks it past zero.
func TestBerryStateRegrowGoesBerries(t *testing.T) {
	last := time.Now().UTC().Add(-7 * time.Hour) // period is 6h, so regen fires
	w, cancel := buildBerryBushWorld(t, 0, "bare", &last)
	defer cancel()

	if got := bushState(t, w); got != "bare" {
		t.Fatalf("initial bush state = %q, want bare", got)
	}
	touched, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return sim.RegenObjectRefresh(world, time.Now().UTC()), nil
	}})
	if err != nil {
		t.Fatalf("regen: %v", err)
	}
	if touched.(int) != 1 {
		t.Fatalf("regen touched = %d, want 1 (periodic restock fired)", touched.(int))
	}
	if got := bushState(t, w); got != "berries" {
		t.Errorf("after regrowth, bush state = %q, want berries", got)
	}
}
