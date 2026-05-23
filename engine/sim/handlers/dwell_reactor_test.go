package handlers_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// dwell_reactor_test.go — coverage of the three dwell-lifecycle event
// subscribers (handleDwellStartedWarrants, handleDwellTickAppliedWarrants,
// handleDwellEndedWarrants). Drives them by sending real Consume + then
// ApplyDwellTick commands so the test exercises the full wire:
// emit → subscriber → warrant on actor.
//
// Source-dedup behavior is tested at the substrate level
// (reactor_pr3a_test.go); these tests only verify the dwell subscribers
// stamp with the right SHAPE.

func buildDwellReactorWorld(t *testing.T) (*sim.World, func()) {
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
			ID:               "hannah",
			DisplayName:      "Hannah",
			LLMAgent:         "hannah-innkeeper",
			Kind:             sim.KindNPCStateful,
			State:            sim.StateIdle,
			StateEnteredAt:   time.Now().UTC(),
			Pos:              sim.TilePos{X: 105, Y: 100},
			Needs:            map[sim.NeedKey]int{"hunger": 20},
			Inventory:        map[sim.ItemKind]int{"stew": 2},
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	handlers.RegisterDwellHandlers(w)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() { cancel(); <-done }
}

// peekDwellActorWarrants snapshots an actor's warrant list from the world
// goroutine.
func peekDwellActorWarrants(t *testing.T, w *sim.World, id sim.ActorID) []sim.WarrantMeta {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a, ok := world.Actors[id]
		if !ok {
			return []sim.WarrantMeta(nil), nil
		}
		return append([]sim.WarrantMeta(nil), a.Warrants...), nil
	}})
	if err != nil {
		t.Fatalf("peekDwellActorWarrants(%s): %v", id, err)
	}
	return v.([]sim.WarrantMeta)
}

// TestDwellStartedSubscriberStampsWarrant — Consume of a dwell-bearing
// item triggers DwellStarted, the subscriber stamps a
// DwellStartedWarrantReason on the eater with the catalog narration
// pre-rendered.
func TestDwellStartedSubscriberStampsWarrant(t *testing.T) {
	w, stop := buildDwellReactorWorld(t)
	defer stop()

	if _, err := w.Send(sim.Consume("hannah", "stew", 1, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	warrants := peekDwellActorWarrants(t, w, "hannah")
	var got *sim.DwellStartedWarrantReason
	for _, m := range warrants {
		if r, ok := m.Reason.(sim.DwellStartedWarrantReason); ok {
			got = &r
			break
		}
	}
	if got == nil {
		t.Fatalf("no DwellStartedWarrantReason on hannah; got %d warrant(s)", len(warrants))
	}
	if got.ItemKind != "stew" {
		t.Errorf("ItemKind = %q, want stew", got.ItemKind)
	}
	if got.StructureID != "tavern" {
		t.Errorf("StructureID = %q, want tavern", got.StructureID)
	}
	if got.NarrationText == "" {
		t.Errorf("NarrationText empty; want catalog hint")
	}
	if len(got.Credits) != 1 || got.Credits[0].Attribute != "hunger" {
		t.Errorf("Credits = %+v, want one hunger credit", got.Credits)
	}
}

// TestDwellTickAppliedSubscriberStampsWarrant — ApplyDwellTick fires
// DwellTickApplied, subscriber stamps DwellTickAppliedWarrantReason
// with per-tick narration pre-rendered.
func TestDwellTickAppliedSubscriberStampsWarrant(t *testing.T) {
	w, stop := buildDwellReactorWorld(t)
	defer stop()

	// Seed a ripe credit directly to avoid the consume path's
	// DwellStarted warrant cluttering the assertion target.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		remaining := 3
		world.Actors["hannah"].DwellCredits = map[sim.DwellCreditKey]*sim.DwellCredit{
			{ObjectID: "tavern", Attribute: "hunger", Source: sim.DwellSourceItem}: {
				ObjectID:           "tavern",
				Kind:               "stew",
				Attribute:          "hunger",
				Source:             sim.DwellSourceItem,
				LastCreditedAt:     time.Now().UTC().Add(-5 * time.Minute),
				RemainingTicks:     &remaining,
				DwellDelta:         -1,
				DwellPeriodMinutes: 2,
			},
		}
		// Clear any existing warrants so the assertion target stays
		// uncluttered.
		world.Actors["hannah"].Warrants = nil
		world.Actors["hannah"].WarrantedSince = nil
		world.Actors["hannah"].WarrantDueAt = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := w.Send(sim.ApplyDwellTick(time.Now().UTC())); err != nil {
		t.Fatalf("tick: %v", err)
	}
	warrants := peekDwellActorWarrants(t, w, "hannah")
	var got *sim.DwellTickAppliedWarrantReason
	for _, m := range warrants {
		if r, ok := m.Reason.(sim.DwellTickAppliedWarrantReason); ok {
			got = &r
			break
		}
	}
	if got == nil {
		t.Fatalf("no DwellTickAppliedWarrantReason on hannah; got %d warrant(s)", len(warrants))
	}
	if got.ItemKind != "stew" || got.Attribute != "hunger" {
		t.Errorf("ItemKind/Attribute = %q/%q, want stew/hunger", got.ItemKind, got.Attribute)
	}
	if got.NarrationText != "You take another bite, the gnawing ebbs." {
		t.Errorf("NarrationText = %q", got.NarrationText)
	}
	if got.RemainingTicks == nil || *got.RemainingTicks != 2 {
		t.Errorf("RemainingTicks = %v, want 2", got.RemainingTicks)
	}
	if got.PeriodMinutes != 2 {
		t.Errorf("PeriodMinutes = %d, want 2", got.PeriodMinutes)
	}
}

// TestDwellEndedSubscriberStampsWarrant — ApplyDwellTick on the last
// tick fires DwellEnded{ItemExhausted}, subscriber stamps
// DwellEndedWarrantReason with terminal narration.
func TestDwellEndedSubscriberStampsWarrant(t *testing.T) {
	w, stop := buildDwellReactorWorld(t)
	defer stop()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		remaining := 1 // last tick
		world.Actors["hannah"].DwellCredits = map[sim.DwellCreditKey]*sim.DwellCredit{
			{ObjectID: "tavern", Attribute: "hunger", Source: sim.DwellSourceItem}: {
				ObjectID:           "tavern",
				Kind:               "stew",
				Attribute:          "hunger",
				Source:             sim.DwellSourceItem,
				LastCreditedAt:     time.Now().UTC().Add(-5 * time.Minute),
				RemainingTicks:     &remaining,
				DwellDelta:         -1,
				DwellPeriodMinutes: 2,
			},
		}
		world.Actors["hannah"].Warrants = nil
		world.Actors["hannah"].WarrantedSince = nil
		world.Actors["hannah"].WarrantDueAt = nil
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := w.Send(sim.ApplyDwellTick(time.Now().UTC())); err != nil {
		t.Fatalf("tick: %v", err)
	}
	warrants := peekDwellActorWarrants(t, w, "hannah")
	var got *sim.DwellEndedWarrantReason
	for _, m := range warrants {
		if r, ok := m.Reason.(sim.DwellEndedWarrantReason); ok {
			got = &r
			break
		}
	}
	if got == nil {
		t.Fatalf("no DwellEndedWarrantReason on hannah; got %d warrant(s)", len(warrants))
	}
	if got.Reason != sim.DwellEndItemExhausted {
		t.Errorf("Reason = %s, want item_exhausted", got.Reason)
	}
	if got.ItemKind != "stew" {
		t.Errorf("ItemKind = %q, want stew", got.ItemKind)
	}
	if got.NarrationText != "You finish the last bite, satisfied." {
		t.Errorf("NarrationText = %q", got.NarrationText)
	}
}

// TestDwellSubscribersBypassDedup — each dwell Reason returns
// DedupDiscriminator=0, so the warrant infrastructure routes them
// through the bypass path (same as BasicWarrantReason). This locks the
// posture so a future per-Reason rewrite doesn't accidentally restore
// dedup keying and collapse unrelated dwell stamps.
func TestDwellSubscribersBypassDedup(t *testing.T) {
	cases := []sim.WarrantReason{
		sim.DwellStartedWarrantReason{ItemKind: "stew"},
		sim.DwellTickAppliedWarrantReason{ItemKind: "stew", Attribute: "hunger"},
		sim.DwellEndedWarrantReason{Reason: sim.DwellEndItemExhausted},
	}
	for _, r := range cases {
		if got := r.DedupDiscriminator(); got != 0 {
			t.Errorf("%T.DedupDiscriminator() = %d, want 0", r, got)
		}
	}
}
