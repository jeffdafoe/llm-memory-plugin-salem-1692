package handlers_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// consume_reactor_test.go — coverage of the ItemConsumed self-narration
// subscriber (handleConsumedNarrationWarrants). Drives it by sending a real
// Consume so the test exercises emit → subscriber → warrant. No village object
// is seeded, so no dwell credit is stamped — keeping the ConsumedWarrantReason
// the sole warrant under test.

func buildConsumeReactorWorld(t *testing.T, hunger int) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:               "hannah",
			DisplayName:      "Hannah",
			Kind:             sim.KindNPCStateful,
			State:            sim.StateIdle,
			StateEnteredAt:   time.Now().UTC(),
			Pos:              sim.TilePos{X: 105, Y: 100},
			Needs:            map[sim.NeedKey]int{"hunger": hunger},
			Inventory:        map[sim.ItemKind]int{"stew": 2},
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	handlers.RegisterConsumeHandlers(w)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() { cancel(); <-done }
}

func consumeWarrantReason(t *testing.T, w *sim.World, id sim.ActorID) (sim.ConsumedWarrantReason, bool) {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a, ok := world.Actors[id]
		if !ok {
			return []sim.WarrantMeta(nil), nil
		}
		return append([]sim.WarrantMeta(nil), a.Warrants...), nil
	}})
	if err != nil {
		t.Fatalf("peek warrants(%s): %v", id, err)
	}
	for _, m := range v.([]sim.WarrantMeta) {
		if r, ok := m.Reason.(sim.ConsumedWarrantReason); ok {
			return r, true
		}
	}
	return sim.ConsumedWarrantReason{}, false
}

// TestConsumeSubscriberStampsWarrant — consuming stew while hungry moves
// hunger, so the subscriber stamps a ConsumedWarrantReason with the felt line.
func TestConsumeSubscriberStampsWarrant(t *testing.T) {
	w, stop := buildConsumeReactorWorld(t, 20)
	defer stop()

	if _, err := w.Send(sim.Consume("hannah", "stew", 1, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	got, ok := consumeWarrantReason(t, w, "hannah")
	if !ok {
		t.Fatal("no ConsumedWarrantReason stamped after a need-moving consume")
	}
	if got.ItemKind != "stew" {
		t.Errorf("ItemKind = %q, want stew", got.ItemKind)
	}
	if got.NarrationText != "You eat the stew; the gnawing ebbs." {
		t.Errorf("NarrationText = %q, want the felt consume line", got.NarrationText)
	}
}

// TestConsumeSubscriberDoesNotDedup — two consumes before the warrant list is
// drained stamp two distinct ConsumedWarrantReasons. Locks the dedup-bypass
// posture: ConsumedWarrantReason.DedupDiscriminator()==0 makes
// WarrantMeta.eventSourced() false, so tryStampWarrant skips its source-aware
// dedup entirely — without that, both would collapse under (WarrantKindConsumed, 0).
func TestConsumeSubscriberDoesNotDedup(t *testing.T) {
	w, stop := buildConsumeReactorWorld(t, sim.NeedMax) // hunger high so both consumes move it
	defer stop()

	for i := 0; i < 2; i++ {
		if _, err := w.Send(sim.Consume("hannah", "stew", 1, time.Now().UTC())); err != nil {
			t.Fatalf("Consume %d: %v", i, err)
		}
	}
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return append([]sim.WarrantMeta(nil), world.Actors["hannah"].Warrants...), nil
	}})
	if err != nil {
		t.Fatalf("peek warrants: %v", err)
	}
	count := 0
	for _, m := range v.([]sim.WarrantMeta) {
		if _, ok := m.Reason.(sim.ConsumedWarrantReason); ok {
			count++
		}
	}
	if count != 2 {
		t.Errorf("ConsumedWarrantReason count = %d, want 2 (no dedup collapse)", count)
	}
}

// TestConsumeSubscriberSilentOnNoOp — consuming while already sated (hunger 0)
// moves no need (Applied empty), so the audit ItemConsumed still fires but NO
// consume narration warrant is stamped.
func TestConsumeSubscriberSilentOnNoOp(t *testing.T) {
	w, stop := buildConsumeReactorWorld(t, 0)
	defer stop()

	if _, err := w.Send(sim.Consume("hannah", "stew", 1, time.Now().UTC())); err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if _, ok := consumeWarrantReason(t, w, "hannah"); ok {
		t.Error("ConsumedWarrantReason stamped on a no-op consume (no need moved); want none")
	}
}
