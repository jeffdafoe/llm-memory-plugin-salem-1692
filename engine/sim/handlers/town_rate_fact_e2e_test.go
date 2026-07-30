package handlers_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/cascade"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// town_rate_fact_e2e_test.go — LLM-572. The seam that actually has to work: a real
// sim.Pay that settles the town rate must leave the OBLIGATION wording in the
// keeper's relationship trail, not the generic purchase wording.
//
// The unit tests in sim/town_rate_fact_internal_test.go prove the sentence; they do
// not prove the pay path selects it. That wiring is the whole fix — the bad fact
// reached consolidation through this exact path, and if the settle result were not
// threaded through, every keeper in the village would go on accumulating "I paid the
// constable and got nothing" in durable memory.
//
// The keeper here is KindNPCShared deliberately. RecordInteraction is gated to
// shared-VA actors (relationship_commands.go), which is precisely why the live
// victim was Moses James — a salem-vendor actor — and not the stateful constable.

func buildTownRateFactWorld(t *testing.T, owed int) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"moses": {
			ID: "moses", DisplayName: "Moses James", Kind: sim.KindNPCShared,
			State: sim.StateIdle, CurrentHuddleID: "h1", Coins: 20,
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
		"gideon": {
			ID: "gideon", DisplayName: "Constable Gideon Marsh", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, CurrentHuddleID: "h1", Coins: 0,
			Attributes:    map[string][]byte{sim.AttrConstable: nil},
			RecentActions: sim.NewRingBuffer[sim.Action](4),
		},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"james_farm": {
			ID: "james_farm", DisplayName: "James Farm",
			OwnerActorID: "moses", Tags: []string{sim.TagBusiness}, RateOwed: owed,
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	// The coin record is credited from the action-log subscribers, deliberately —
	// they are also what writes the durable `paid` rows the record is seeded from at
	// boot, and the live tally and a post-restart seed must agree about what counts
	// as a payment. That co-location means the subscribers have to be wired for the
	// tally to move, so this harness wires them.
	cascade.RegisterActionLog(ctx, w)
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() { cancel(); <-done }
}

// peekSalientTexts reads one side of a pair's fact trail off the world goroutine.
func peekSalientTexts(t *testing.T, w *sim.World, subject, peer sim.ActorID) []string {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[subject]
		if a == nil || a.Relationships[peer] == nil {
			return []string(nil), nil
		}
		out := make([]string, 0, len(a.Relationships[peer].SalientFacts))
		for _, f := range a.Relationships[peer].SalientFacts {
			out = append(out, f.Text)
		}
		return out, nil
	}})
	if err != nil {
		t.Fatalf("peekSalientTexts(%s→%s): %v", subject, peer, err)
	}
	return v.([]string)
}

// The fix, end to end. The model's own for-text ("Day's rate on the James Farm" in
// the live case) is DROPPED — it is the misleading half, and the engine knows what
// actually settled.
func TestPay_SettledTownRateWritesTheObligationFact(t *testing.T) {
	w, stop := buildTownRateFactWorld(t, 1)
	defer stop()

	if _, err := w.Send(sim.Pay("moses", "Constable Gideon Marsh", 1, "Day's rate on the James Farm", time.Now().UTC())); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	facts := peekSalientTexts(t, w, "moses", "gideon")
	if len(facts) != 1 {
		t.Fatalf("facts = %v, want exactly one", facts)
	}
	got := facts[0]
	const want = "I paid Constable Gideon Marsh the day's rate on the James Farm, 1 coin — the town's due, owed and now settled. No goods were bought and none are owed in return."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	// The generic purchase shape — a payment with a stated purpose and nothing
	// delivered against it — must not be what lands in memory.
	if strings.Contains(got, "1 coin for Day's rate") {
		t.Errorf("the generic pay wording survived: %q", got)
	}
}

// A payment to the constable that settles NOTHING keeps the ordinary wording. The
// obligation framing has to be earned by an actual settlement, or it would assert a
// discharged debt on every coin the constable is ever handed.
func TestPay_UnsettledPaymentKeepsTheOrdinaryFact(t *testing.T) {
	w, stop := buildTownRateFactWorld(t, 0)
	defer stop()

	if _, err := w.Send(sim.Pay("moses", "Constable Gideon Marsh", 2, "a kindness", time.Now().UTC())); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	facts := peekSalientTexts(t, w, "moses", "gideon")
	if len(facts) != 1 {
		t.Fatalf("facts = %v, want exactly one", facts)
	}
	const want = "I paid Constable Gideon Marsh 2 coins for a kindness."
	if facts[0] != want {
		t.Errorf("got  %q\nwant %q", facts[0], want)
	}
}

// A settled payment also credits the coin record, which is what the co-present cue
// reads. Both writes hang off the same Paid event, and this pins that a pay reaching
// the relationship trail reached the tally too — a drift between them would leave
// the scene contradicting the memory.
func TestPay_SettledTownRateCreditsTheCoinRecord(t *testing.T) {
	w, stop := buildTownRateFactWorld(t, 1)
	defer stop()

	at := time.Now().UTC()
	if _, err := w.Send(sim.Pay("moses", "Constable Gideon Marsh", 1, "Day's rate on the James Farm", at)); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		// Read through the same clone the publish path uses, so this exercises the
		// snapshot shape perception actually gets rather than the live map.
		return &sim.Snapshot{
			CoinRecord:       sim.CloneCoinRecord(world.CoinRecord),
			CoinRecordWindow: world.CoinRecordWindow(),
		}, nil
	}})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snap := v.(*sim.Snapshot)
	d := snap.CoinDealingsFor("gideon", "moses", at)
	if d.ReceivedCount != 1 || d.ReceivedTotal != 1 {
		t.Errorf("constable's record of Moses = %+v, want one 1-coin payment received", d)
	}
	if d.PaidCount != 0 {
		t.Errorf("constable's record shows %d payments OUT to Moses, want 0 — nothing went back", d.PaidCount)
	}
}
