package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// events_dwell_test.go — Phase 3 dwell perception PR. Covers the event
// stream emitted by Consume (DwellStarted), commitPayTransfer's
// consume_now path (same), and ApplyDwellTick (DwellTickApplied +
// DwellEnded). Subscribers + warrant-stamping live in handlers/;
// substrate-level tests here exercise the engine emits directly.

// buildConsumeDwellWorld seeds an actor with stew in inventory, plus
// a tavern at the actor's position so the dwell pin resolves. Returns
// the world plus a subscriber that captures dwell events.
func buildConsumeDwellWorld(t *testing.T) (*sim.World, *capturedDwellEvents, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"tavern": {
			ID: "tavern", AssetID: "inn-thatched",
			DisplayName: "The Drunken Hare",
			X:           100, Y: 100,
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:       "hannah",
			LLMAgent: "hannah-innkeeper",
			Kind:     sim.KindNPCStateful,
			CurrentX: 105, CurrentY: 100,
			Needs:     map[sim.NeedKey]int{"hunger": 20, "thirst": 5, "tiredness": 5},
			Inventory: map[sim.ItemKind]int{"stew": 2, "water": 1},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	cap := &capturedDwellEvents{}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(cap.handle))
		return nil, nil
	}}); err != nil {
		cancel()
		t.Fatalf("subscribe: %v", err)
	}
	return w, cap, cancel
}

// capturedDwellEvents records every dwell event emitted by the world,
// preserving order. Used by event-emit tests to assert which events
// fired and in what shape.
type capturedDwellEvents struct {
	started []sim.DwellStarted
	ticks   []sim.DwellTickApplied
	ended   []sim.DwellEnded
}

func (c *capturedDwellEvents) handle(_ *sim.World, evt sim.Event) {
	switch e := evt.(type) {
	case *sim.DwellStarted:
		c.started = append(c.started, *e)
	case *sim.DwellTickApplied:
		c.ticks = append(c.ticks, *e)
	case *sim.DwellEnded:
		c.ended = append(c.ended, *e)
	}
}

// TestConsumeEmitsDwellStarted — Consume of a dwell-bearing item at a
// dwell pin emits DwellStarted carrying the stamped credit set and the
// item's catalog narration.
func TestConsumeEmitsDwellStarted(t *testing.T) {
	w, cap, cancel := buildConsumeDwellWorld(t)
	defer cancel()

	now := time.Now().UTC()
	if _, err := w.Send(sim.Consume("hannah", "stew", 1, now)); err != nil {
		t.Fatalf("Consume: %v", err)
	}

	if len(cap.started) != 1 {
		t.Fatalf("DwellStarted count = %d, want 1", len(cap.started))
	}
	e := cap.started[0]
	if e.ActorID != "hannah" || e.Kind != "stew" {
		t.Errorf("DwellStarted ActorID/Kind = %q/%q, want hannah/stew", e.ActorID, e.Kind)
	}
	if e.StructureID != "tavern" {
		t.Errorf("DwellStarted StructureID = %q, want tavern", e.StructureID)
	}
	if len(e.Credits) != 1 || e.Credits[0].Attribute != "hunger" {
		t.Errorf("DwellStarted Credits = %+v, want one hunger credit", e.Credits)
	}
	want := "This stew looks really good. You'll need some time to enjoy it properly."
	if e.NarrationText != want {
		t.Errorf("NarrationText = %q, want %q", e.NarrationText, want)
	}
	if e.EventID() == 0 {
		t.Errorf("DwellStarted EventID not stamped")
	}
}

// TestConsumeSkipsDwellStartedForNonDwellItem — water has no dwell
// triple, so DwellStarted does NOT fire.
func TestConsumeSkipsDwellStartedForNonDwellItem(t *testing.T) {
	w, cap, cancel := buildConsumeDwellWorld(t)
	defer cancel()

	if _, err := w.Send(sim.Consume("hannah", "water", 1, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if len(cap.started) != 0 {
		t.Errorf("DwellStarted fired for non-dwell item: %+v", cap.started)
	}
}

// TestConsumeSkipsDwellStartedWhenWalking — eating far from any
// village object (eat-while-walking) leaves structureID empty, which
// silent-skips both the credit upsert and the DwellStarted emit.
func TestConsumeSkipsDwellStartedWhenWalking(t *testing.T) {
	w, cap, cancel := buildConsumeDwellWorld(t)
	defer cancel()

	// Walk far away from the tavern. Use a Command to move the actor
	// since there's no public sim.MoveActor in this fixture and the
	// dwell pin lookup uses CurrentX/Y.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["hannah"].CurrentX = 9999
		world.Actors["hannah"].CurrentY = 9999
		return nil, nil
	}}); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := w.Send(sim.Consume("hannah", "stew", 1, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if len(cap.started) != 0 {
		t.Errorf("DwellStarted fired for eat-while-walking: %+v", cap.started)
	}
}

// TestApplyDwellTickEmitsTickAppliedAndEnded — drives a 2-tick stew
// dwell and asserts the event stream:
//   - First tick: DwellTickApplied with RemainingTicks=1, no DwellEnded.
//   - Second tick (item exhausted): DwellTickApplied with RemainingTicks=0,
//     followed by DwellEnded{ItemExhausted}.
func TestApplyDwellTickEmitsTickAppliedAndEnded(t *testing.T) {
	w, cap, cancel := buildConsumeDwellWorld(t)
	defer cancel()

	// Force a 2-tick remaining credit so we can drive both events in
	// two distinct ticks.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		remaining := 2
		key := sim.DwellCreditKey{ObjectID: "tavern", Attribute: "hunger", Source: sim.DwellSourceItem}
		world.Actors["hannah"].DwellCredits = map[sim.DwellCreditKey]*sim.DwellCredit{
			key: {
				ObjectID:           "tavern",
				Kind:               "stew",
				Attribute:          "hunger",
				Source:             sim.DwellSourceItem,
				LastCreditedAt:     time.Now().UTC().Add(-3 * time.Minute),
				RemainingTicks:     &remaining,
				DwellDelta:         -1,
				DwellPeriodMinutes: 2,
			},
		}
		// Make sure hunger is high so floor-hit doesn't preempt
		// item-exhausted.
		world.Actors["hannah"].Needs["hunger"] = 20
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// First tick — ripe (3 min elapsed > 2 min period).
	t1 := time.Now().UTC()
	if _, err := w.Send(sim.ApplyDwellTick(t1)); err != nil {
		t.Fatalf("tick1: %v", err)
	}
	if len(cap.ticks) != 1 {
		t.Fatalf("after tick1: DwellTickApplied count = %d, want 1", len(cap.ticks))
	}
	if cap.ticks[0].RemainingTicks == nil || *cap.ticks[0].RemainingTicks != 1 {
		t.Errorf("tick1 RemainingTicks = %v, want 1", cap.ticks[0].RemainingTicks)
	}
	if len(cap.ended) != 0 {
		t.Errorf("after tick1: DwellEnded fired prematurely: %+v", cap.ended)
	}

	// Second tick — advance anchor so the credit is ripe again. The
	// previous tick advanced LastCreditedAt by exactly 2 min from the
	// initial anchor, so the next ripe-window starts then. Push now
	// past that.
	t2 := t1.Add(3 * time.Minute)
	if _, err := w.Send(sim.ApplyDwellTick(t2)); err != nil {
		t.Fatalf("tick2: %v", err)
	}
	if len(cap.ticks) != 2 {
		t.Fatalf("after tick2: DwellTickApplied count = %d, want 2", len(cap.ticks))
	}
	if cap.ticks[1].RemainingTicks == nil || *cap.ticks[1].RemainingTicks != 0 {
		t.Errorf("tick2 RemainingTicks = %v, want 0", cap.ticks[1].RemainingTicks)
	}
	if len(cap.ended) != 1 {
		t.Fatalf("after tick2: DwellEnded count = %d, want 1", len(cap.ended))
	}
	if cap.ended[0].Reason != sim.DwellEndItemExhausted {
		t.Errorf("DwellEnded Reason = %s, want item_exhausted", cap.ended[0].Reason)
	}
	if cap.ended[0].Kind != "stew" {
		t.Errorf("DwellEnded Kind = %q, want stew", cap.ended[0].Kind)
	}
}

// TestApplyDwellTickWalkedAwayEmitsEnded — actor moves off the pin,
// next tick emits DwellEnded{WalkedAway} and no DwellTickApplied
// (walked-away skips the apply path).
func TestApplyDwellTickWalkedAwayEmitsEnded(t *testing.T) {
	w, cap, cancel := buildConsumeDwellWorld(t)
	defer cancel()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		remaining := 5
		world.Actors["hannah"].DwellCredits = map[sim.DwellCreditKey]*sim.DwellCredit{
			{ObjectID: "tavern", Attribute: "hunger", Source: sim.DwellSourceItem}: {
				ObjectID:           "tavern",
				Kind:               "stew",
				Attribute:          "hunger",
				Source:             sim.DwellSourceItem,
				LastCreditedAt:     time.Now().UTC().Add(-3 * time.Minute),
				RemainingTicks:     &remaining,
				DwellDelta:         -1,
				DwellPeriodMinutes: 2,
			},
		}
		world.Actors["hannah"].CurrentX = 9999 // walked off
		world.Actors["hannah"].CurrentY = 9999
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := w.Send(sim.ApplyDwellTick(time.Now().UTC())); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(cap.ticks) != 0 {
		t.Errorf("DwellTickApplied fired despite walk-off: %+v", cap.ticks)
	}
	if len(cap.ended) != 1 {
		t.Fatalf("DwellEnded count = %d, want 1", len(cap.ended))
	}
	if cap.ended[0].Reason != sim.DwellEndWalkedAway {
		t.Errorf("DwellEnded Reason = %s, want walked_away", cap.ended[0].Reason)
	}
}

// TestApplyDwellTickFloorHitEmitsEnded — need hits 0 → DwellEnded
// {FloorHit} fires alongside DwellTickApplied. Credit terminated.
func TestApplyDwellTickFloorHitEmitsEnded(t *testing.T) {
	w, cap, cancel := buildConsumeDwellWorld(t)
	defer cancel()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		remaining := 5 // plenty left — floor-hit pre-empts
		world.Actors["hannah"].DwellCredits = map[sim.DwellCreditKey]*sim.DwellCredit{
			{ObjectID: "tavern", Attribute: "hunger", Source: sim.DwellSourceItem}: {
				ObjectID:           "tavern",
				Kind:               "stew",
				Attribute:          "hunger",
				Source:             sim.DwellSourceItem,
				LastCreditedAt:     time.Now().UTC().Add(-3 * time.Minute),
				RemainingTicks:     &remaining,
				DwellDelta:         -2,
				DwellPeriodMinutes: 2,
			},
		}
		world.Actors["hannah"].Needs["hunger"] = 1 // pre-applied -2 → 0
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := w.Send(sim.ApplyDwellTick(time.Now().UTC())); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(cap.ticks) != 1 {
		t.Fatalf("DwellTickApplied count = %d, want 1", len(cap.ticks))
	}
	if len(cap.ended) != 1 {
		t.Fatalf("DwellEnded count = %d, want 1", len(cap.ended))
	}
	if cap.ended[0].Reason != sim.DwellEndFloorHit {
		t.Errorf("DwellEnded Reason = %s, want floor_hit", cap.ended[0].Reason)
	}
}

// TestDwellEndReasonStringStable — labels are stable across releases
// (consumed by logs and tests).
func TestDwellEndReasonStringStable(t *testing.T) {
	cases := map[sim.DwellEndReason]string{
		sim.DwellEndUnknown:        "unknown",
		sim.DwellEndItemExhausted:  "item_exhausted",
		sim.DwellEndFloorHit:       "floor_hit",
		sim.DwellEndWalkedAway:     "walked_away",
		sim.DwellEndCatalogUnknown: "catalog_unknown",
	}
	for r, want := range cases {
		if got := r.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", r, got, want)
		}
	}
}
