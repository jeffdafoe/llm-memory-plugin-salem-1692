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

// The settle keys on WHO is paid, not on what the payment is called: a bare coin
// payment from a keeper to the constable settles the rate whatever prose the model
// attaches to it.
//
// This is a deliberate tradeoff, not an oversight, and it is deliberately OVER-broad
// (code_review raised it). A keeper making the constable an unprompted gift — which
// villagers really do through bare pay — clears his arrears as a side effect. That
// runs in the safe direction: the constable ends up holding at least the rate, and
// nothing is minted or destroyed. The alternative, requiring a structured marker on
// the pay call, inverts the failure into the one that kills the feature — the model
// omits the marker, the payment does not settle, the cue nags forever and the
// constable stays broke, which is the exact state this ticket exists to fix.
//
// Note what does NOT reach here: buying goods from the constable goes through the
// pay_with_item / quote flow, which is untouched. Only bare coin payments settle.
// The sibling levy has the same property — any shovel purchase satisfies farm upkeep,
// whatever the owner bought it for.
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

// TestPay_AnyBarePaymentPurposeSettlesTownRate pins the settlement POLICY as an
// explicit, intended invariant rather than leaving it as an implicit assumption
// (code_review asked for this to be made explicit, LLM-557):
//
//	any bare coin payment from a business owner to a constable is applied to that
//	business's town-rate arrears, whatever the payment was for.
//
// It is deliberately over-broad. A gift, a loan, a wage or a repayment all clear the
// keeper's arrears as a side effect, so a payment made for one purpose can discharge
// a different liability. That is accepted: nothing is minted or destroyed, the
// constable ends up holding at least the rate, and the alternative — gating on a
// model-emitted purpose marker — fails in the direction that kills the mechanism
// (marker omitted → nothing settles → the constable stays broke while the cue nags).
//
// If this policy is ever narrowed, these are the cases that change behaviour.
func TestPay_AnyBarePaymentPurposeSettlesTownRate(t *testing.T) {
	purposes := []struct {
		name    string
		forText string
	}{
		{"an outright gift", "a kindness, with my thanks"},
		{"a wage for work done", "your wages for the week's watch"},
		{"a loan repayment", "the coin I borrowed off you"},
		{"no stated purpose at all", ""},
		{"something plainly unrelated", "the widow's relief fund"},
	}
	for _, p := range purposes {
		t.Run(p.name, func(t *testing.T) {
			w, stop := buildTownRatePayWorld(t, 2)
			defer stop()

			if _, err := w.Send(sim.Pay("josiah", "Constable Gideon Marsh", 2, p.forText, time.Now().UTC())); err != nil {
				t.Fatalf("Pay: %v", err)
			}
			if got := peekRateOwed(t, w, "general_store"); got != 0 {
				t.Errorf("RateOwed = %d after a bare payment for %q, want 0 — "+
					"the policy applies ANY bare coin payment to arrears", got, p.forText)
			}
		})
	}
}

// The other half of the invariant: the policy is scoped to payments a business owner
// makes to a CONSTABLE. It must not generalise to the constable paying someone, which
// would be the mirror-image accounting error.
func TestPay_ConstablePayingAKeeperDoesNotTouchArrears(t *testing.T) {
	w, stop := buildTownRatePayWorld(t, 3)
	defer stop()

	// Give the constable something to spend, then have him buy from the keeper.
	if _, err := w.Send(sim.Pay("hannah", "Constable Gideon Marsh", 5, "a gift", time.Now().UTC())); err != nil {
		t.Fatalf("seed Pay: %v", err)
	}
	if _, err := w.Send(sim.Pay("gideon", "Josiah Thorne", 2, "a wedge of cheese", time.Now().UTC())); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if got := peekRateOwed(t, w, "general_store"); got != 3 {
		t.Errorf("RateOwed = %d after the CONSTABLE paid the keeper, want 3 (untouched)", got)
	}
}
