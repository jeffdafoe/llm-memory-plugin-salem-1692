package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// reactor_pay_offer_wake_test.go — LLM-460. A buyer's pending pay offer lifts the
// laboring tick-shelve so the worker can answer it before the 3-minute TTL runs out.
//
// PayOfferWarrantReason exists to WAKE the responder, but the laboring gate admitted
// only need / nudge / PC-speech / hired-repair / hired-hearth / return-to-post, so the
// warrant was stamped and then shelved and the offer expired unseen. Live 2026-07-18:
// Moses James posted eight milk-for-meat offers over 30 minutes to Nathaniel Cole — who
// was working a 4-hour job FOR Moses, in the same spot — and seven expired before
// Nathaniel drew a tick. The one that landed inside an unrelated speech-wake he settled
// in two seconds, so the model was never the problem; it simply was never asked.

func payOfferWarrant(expiresIn time.Duration) sim.WarrantMeta {
	return sim.WarrantMeta{Reason: sim.PayOfferWarrantReason{
		LedgerID:  1911,
		Buyer:     "bob",
		Item:      "meat",
		Qty:       1,
		PayItems:  []sim.ItemKindQty{{Kind: "milk", Qty: 1}},
		ExpiresAt: time.Now().UTC().Add(expiresIn),
	}}
}

// TestActorCanReactNow_LaboringInterruptedByPayOffer is the regression: the exact live
// shape — a worker mid-job with a buyer's offer staked against him — must tick.
func TestActorCanReactNow_LaboringInterruptedByPayOffer(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			now := time.Now().UTC()
			a := world.Actors["alice"]
			a.State = sim.StateLaboring
			until := now.Add(2 * time.Hour) // a long job — the live case was 4 hours
			a.LaboringUntil = &until
			a.Warrants = []sim.WarrantMeta{payOfferWarrant(3 * time.Minute)}
			eligible, stale := sim.ActorCanReactNowAt(world, a, now)
			if !eligible || stale {
				t.Errorf("laboring + pending pay offer: eligible=%v stale=%v; want true,false — "+
					"the offer expires in 3 minutes and the worker is the only one who can answer "+
					"it, so shelving him lets it die unseen (LLM-460)", eligible, stale)
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// TestActorCanReactNow_LaboringStillShelvedWithoutPayOffer is the control: the wake must
// come from the pay-offer warrant specifically, not from the fixture being tickable for
// some unrelated reason. Same laboring worker, an idle warrant instead — still shelved.
// Without this, the test above could pass against a gate that had stopped shelving at all.
func TestActorCanReactNow_LaboringStillShelvedWithoutPayOffer(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			now := time.Now().UTC()
			a := world.Actors["alice"]
			a.State = sim.StateLaboring
			until := now.Add(2 * time.Hour)
			a.LaboringUntil = &until
			a.Warrants = []sim.WarrantMeta{{Reason: sim.IdleBackstopWarrantReason{}}}
			eligible, stale := sim.ActorCanReactNowAt(world, a, now)
			if eligible || stale {
				t.Errorf("laboring + idle warrant: eligible=%v stale=%v; want false,false — "+
					"the LLM-460 carve-out must be scoped to the pay-offer warrant, not a "+
					"general un-shelving of laboring workers", eligible, stale)
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// TestActorCanReactNow_SleepAndBreakUnaffectedByPayOffer pins the scoping the other way:
// the carve-out is the LABORING gate's alone. Sleep is sacrosanct (a standing v1
// decision, reaffirmed 2026-05-29) and a break is only cut short by a red need or an
// operator nudge — a buyer's offer must not reach into either, or commerce becomes a way
// to drag NPCs out of bed. Mirrors the scoping of hasHiredRepairWarrant / hasNPCSpeech-
// Warrant, which are likewise deliberately absent from these two gates.
func TestActorCanReactNow_SleepAndBreakUnaffectedByPayOffer(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			now := time.Now().UTC()

			sleeper := world.Actors["alice"]
			sleeper.State = sim.StateSleeping
			sleeper.Warrants = []sim.WarrantMeta{payOfferWarrant(3 * time.Minute)}
			if eligible, _ := sim.ActorCanReactNowAt(world, sleeper, now); eligible {
				t.Error("sleeping + pending pay offer: eligible=true, want false — sleep is never " +
					"interrupted, and a pay offer must not become a way to wake a sleeper (LLM-460)")
			}

			rester := world.Actors["bob"]
			rester.State = sim.StateResting
			breakUntil := now.Add(30 * time.Minute)
			rester.BreakUntil = &breakUntil
			rester.Warrants = []sim.WarrantMeta{payOfferWarrant(3 * time.Minute)}
			if eligible, _ := sim.ActorCanReactNowAt(world, rester, now); eligible {
				t.Error("on break + pending pay offer: eligible=true, want false — a break yields " +
					"only to a red need or an operator nudge, not to commerce (LLM-460)")
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
}
