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

// payOfferWarrantAt builds the warrant with ExpiresAt derived from the SAME `now` the
// caller passes to ActorCanReactNowAt, so expiry-boundary cases are exact rather than
// racing two independent wall-clock reads (code_review).
func payOfferWarrantAt(now time.Time, expiresIn time.Duration) sim.WarrantMeta {
	return sim.WarrantMeta{Reason: sim.PayOfferWarrantReason{
		LedgerID:  1911,
		Buyer:     "bob",
		Item:      "meat",
		Qty:       1,
		PayItems:  []sim.ItemKindQty{{Kind: "milk", Qty: 1}},
		ExpiresAt: now.Add(expiresIn),
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
			a.Warrants = []sim.WarrantMeta{payOfferWarrantAt(now, 3 * time.Minute)}
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

// TestActorCanReactNow_LaboringWakesOnExpiredPayOfferWarrant documents a deliberate
// non-gate (code_review): hasPayOfferWarrant does NOT check ExpiresAt, so a warrant whose
// offer has already lapsed still wakes the worker.
//
// This is a cheap superset, not an oversight. The cost is one wasted tick, and it cannot
// repeat — the evaluator consumes the cycle at emit, so the stale warrant is gone
// afterward. Crucially the wasted tick is not a WRONG tick: the prompt's offer section and
// the accept/decline/counter tool gate both read the live PayOffersForMe view off the
// snapshot ledger rather than this warrant, so a lapsed offer simply doesn't render and
// the NPC sees an accurate scene. Re-checking expiry here would duplicate the ledger's
// expiry rule in a second place where it could drift out of agreement.
//
// In practice the window barely exists: the wake fires on the next 250ms scan, roughly
// three minutes before the TTL, and a warrant that DOES sit unconsumed (because some
// earlier gate shelved the actor) is evicted by warrantCycleStale at MaxWarrantAge.
func TestActorCanReactNow_LaboringWakesOnExpiredPayOfferWarrant(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			now := time.Now().UTC()
			a := world.Actors["alice"]
			a.State = sim.StateLaboring
			until := now.Add(2 * time.Hour)
			a.LaboringUntil = &until
			a.Warrants = []sim.WarrantMeta{payOfferWarrantAt(now, -1*time.Minute)} // lapsed
			eligible, stale := sim.ActorCanReactNowAt(world, a, now)
			if !eligible || stale {
				t.Errorf("laboring + EXPIRED pay-offer warrant: eligible=%v stale=%v; want true,false. "+
					"The predicate deliberately ignores ExpiresAt — if this now shelves, someone added "+
					"an expiry gate; make sure it agrees with the ledger's own expiry rule and update "+
					"this test's rationale (LLM-460)", eligible, stale)
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// TestActorCanReactNow_SourceActivityOutranksPayOffer pins the gate ORDER (code_review):
// an actor that is both laboring and mid source-activity (eating, drinking, harvesting)
// hits the source-activity shelve first, which has no pay-offer carve-out, so it stays
// shelved. That is intended — LLM-460 scoped the wake to the laboring gate alone and
// deliberately left the mid-bite protection whole. Without this test a later refactor
// (merging the gates, or hoisting the carve-out) could silently let commerce yank an
// actor out of a half-eaten meal, which is exactly what the source-activity shelve exists
// to prevent.
func TestActorCanReactNow_SourceActivityOutranksPayOffer(t *testing.T) {
	w, cancel := buildReactorTestWorld(t)
	defer cancel()
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			now := time.Now().UTC()
			a := world.Actors["alice"]
			a.State = sim.StateLaboring
			until := now.Add(2 * time.Hour)
			a.LaboringUntil = &until
			a.SourceActivity = &sim.SourceActivity{StartedAt: now, Until: now.Add(10 * time.Second)}
			a.Warrants = []sim.WarrantMeta{payOfferWarrantAt(now, 3 * time.Minute)}
			eligible, stale := sim.ActorCanReactNowAt(world, a, now)
			if eligible || stale {
				t.Errorf("mid source-activity + laboring + pay offer: eligible=%v stale=%v; want "+
					"false,false — the source-activity shelve runs first and has no pay-offer "+
					"carve-out by design, so a buyer cannot pull an actor off a half-eaten meal "+
					"(LLM-460 scoped the wake to the laboring gate alone)", eligible, stale)
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
			sleeper.Warrants = []sim.WarrantMeta{payOfferWarrantAt(now, 3 * time.Minute)}
			if eligible, _ := sim.ActorCanReactNowAt(world, sleeper, now); eligible {
				t.Error("sleeping + pending pay offer: eligible=true, want false — sleep is never " +
					"interrupted, and a pay offer must not become a way to wake a sleeper (LLM-460)")
			}

			rester := world.Actors["bob"]
			rester.State = sim.StateResting
			breakUntil := now.Add(30 * time.Minute)
			rester.BreakUntil = &breakUntil
			rester.Warrants = []sim.WarrantMeta{payOfferWarrantAt(now, 3 * time.Minute)}
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
