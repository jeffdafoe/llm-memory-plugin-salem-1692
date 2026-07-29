package handlers_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// town_rate_pay_test.go — LLM-557. End-to-end coverage across the seam that actually
// has to work: a real sim.Pay from a keeper to the constable must draw the town-rate
// debt down on the keeper's business.
//
// The unit tests in sim/town_rate_internal_test.go call settleTownRate directly, which
// proves the arithmetic but NOT that the pay path reaches it. That wiring is the whole
// mechanism — the engine deliberately moves no coin of its own, so if the settle were
// never invoked the levy would accrue forever and the constable would collect nothing
// while every cue insisted he had been paid. These tests drive the production command.

// buildTownRatePayWorld seeds a keeper who owns a business carrying `owed` in arrears,
// a constable, and an ordinary villager — all in one huddle so sim.Pay resolves.
func buildTownRatePayWorld(t *testing.T, owed int) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"josiah": {
			ID: "josiah", DisplayName: "Josiah Thorne", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, CurrentHuddleID: "h1", Coins: 20,
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
		"gideon": {
			ID: "gideon", DisplayName: "Constable Gideon Marsh", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, CurrentHuddleID: "h1", Coins: 0,
			Attributes:    map[string][]byte{sim.AttrConstable: nil},
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
		"hannah": {
			ID: "hannah", DisplayName: "Hannah Boggs", Kind: sim.KindNPCShared,
			State: sim.StateIdle, CurrentHuddleID: "h1", Coins: 5,
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"general_store": {
			ID: "general_store", DisplayName: "General Store",
			OwnerActorID: "josiah", Tags: []string{sim.TagBusiness}, RateOwed: owed,
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() { cancel(); <-done }
}

// peekRateOwed reads a business's arrears off the world goroutine.
func peekRateOwed(t *testing.T, w *sim.World, id sim.VillageObjectID) int {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		obj, ok := world.VillageObjects[id]
		if !ok || obj == nil {
			return -1, nil
		}
		return obj.RateOwed, nil
	}})
	if err != nil {
		t.Fatalf("peekRateOwed(%s): %v", id, err)
	}
	return v.(int)
}

func peekCoins(t *testing.T, w *sim.World, id sim.ActorID) int {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a, ok := world.Actors[id]
		if !ok || a == nil {
			return -1, nil
		}
		return a.Coins, nil
	}})
	if err != nil {
		t.Fatalf("peekCoins(%s): %v", id, err)
	}
	return v.(int)
}

// The whole point of the mechanism: the keeper pays, the coin moves through the
// ordinary channel, and the debt clears.
func TestPay_KeeperSettlesTownRate(t *testing.T) {
	w, stop := buildTownRatePayWorld(t, 3)
	defer stop()

	if _, err := w.Send(sim.Pay("josiah", "Constable Gideon Marsh", 3, "the town rate", time.Now().UTC())); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if got := peekRateOwed(t, w, "general_store"); got != 0 {
		t.Errorf("RateOwed = %d after paying the full rate, want 0", got)
	}
	// The coin really moved — this is a levy, not a bookkeeping entry.
	if got := peekCoins(t, w, "gideon"); got != 3 {
		t.Errorf("constable coins = %d, want 3", got)
	}
	if got := peekCoins(t, w, "josiah"); got != 17 {
		t.Errorf("keeper coins = %d, want 17", got)
	}
}

// A part payment leaves the remainder standing, so the cue keeps naming it.
func TestPay_PartialSettlesTownRatePartly(t *testing.T) {
	w, stop := buildTownRatePayWorld(t, 3)
	defer stop()

	if _, err := w.Send(sim.Pay("josiah", "Constable Gideon Marsh", 1, "the town rate", time.Now().UTC())); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if got := peekRateOwed(t, w, "general_store"); got != 2 {
		t.Errorf("RateOwed = %d after paying 1 of 3, want 2", got)
	}
}

// The settle keys on WHO is paid, not on what the payment is called — the constable
// sells nothing, so any coin he takes from a keeper is the rate. This pins that a
// payment whose for-text says nothing about the rate still settles it, which is what
// lets the mechanism survive whatever prose the model writes.
func TestPay_SettlesTownRateRegardlessOfForText(t *testing.T) {
	w, stop := buildTownRatePayWorld(t, 2)
	defer stop()

	if _, err := w.Send(sim.Pay("josiah", "Constable Gideon Marsh", 2, "a kindness", time.Now().UTC())); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if got := peekRateOwed(t, w, "general_store"); got != 0 {
		t.Errorf("RateOwed = %d, want 0 — the settle keys on the payee, not the for-text", got)
	}
}

// Paying an ordinary villager is an ordinary transaction and must leave the rate alone.
// Without this, every purchase a keeper made would quietly pay off his town rate.
func TestPay_NonConstableDoesNotSettleTownRate(t *testing.T) {
	w, stop := buildTownRatePayWorld(t, 3)
	defer stop()

	if _, err := w.Send(sim.Pay("josiah", "Hannah Boggs", 3, "porridge", time.Now().UTC())); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if got := peekRateOwed(t, w, "general_store"); got != 3 {
		t.Errorf("RateOwed = %d after paying an ordinary villager, want 3 (untouched)", got)
	}
}

// Overpaying clears the debt and no more — the surplus is a gift, never a credit that
// drives the balance negative.
func TestPay_OverpaymentFloorsTownRateAtZero(t *testing.T) {
	w, stop := buildTownRatePayWorld(t, 2)
	defer stop()

	if _, err := w.Send(sim.Pay("josiah", "Constable Gideon Marsh", 10, "the rate and a drink", time.Now().UTC())); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if got := peekRateOwed(t, w, "general_store"); got != 0 {
		t.Errorf("RateOwed = %d after overpaying, want 0", got)
	}
}
