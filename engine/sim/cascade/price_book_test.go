package cascade

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// price_book_test.go — subscriber-layer tests for the PayWithItemResolved
// → PriceBook cascade slice. Substrate-layer tests live in
// engine/sim/price_book_test.go; pg seed-query tests in
// engine/sim/repo/pg/orders_test.go.

func buildPriceBookCascadeWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, _ := mem.NewRepository()
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

func readPriceBook(t *testing.T, w *sim.World, key sim.PriceBookKey) []sim.PriceObservation {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.LookupSellerRecent(key.SellerID, key.Item), nil
	}})
	if err != nil {
		t.Fatalf("readPriceBook: %v", err)
	}
	if v == nil {
		return nil
	}
	return v.([]sim.PriceObservation)
}

// --- TestHandlePayWithItemResolvedPriceBook_AcceptedAppends ---------
func TestHandlePayWithItemResolvedPriceBook_AcceptedAppends(t *testing.T) {
	w, stop := buildPriceBookCascadeWorld(t)
	defer stop()

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		handlePayWithItemResolvedPriceBook(world, &sim.PayWithItemResolved{
			LedgerID:       42,
			BuyerID:        "alice",
			SellerID:       "bob",
			ItemKind:       "ale",
			QtyPerConsumer: 2,
			ConsumeNow:     true,
			ConsumerIDs:    []sim.ActorID{"alice"},
			Amount:         6,
			TerminalState:  sim.PayTerminalStateAccepted,
			At:             at,
		})
	})

	history := readPriceBook(t, w, sim.PriceBookKey{SellerID: "bob", Item: "ale"})
	if len(history) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(history))
	}
	obs := history[0]
	if obs.BuyerID != "alice" || obs.Amount != 6 || obs.Qty != 2 || obs.Consumers != 1 {
		t.Errorf("observation = %+v; want alice/6/qty=2/consumers=1", obs)
	}
	if !obs.At.Equal(at) {
		t.Errorf("observation At = %v, want %v", obs.At, at)
	}
}

// --- TestHandlePayWithItemResolvedPriceBook_NonAcceptedIsNoop -------
func TestHandlePayWithItemResolvedPriceBook_NonAcceptedIsNoop(t *testing.T) {
	w, stop := buildPriceBookCascadeWorld(t)
	defer stop()

	nonAccepted := []sim.PayTerminalState{
		sim.PayTerminalStateDeclined,
		sim.PayTerminalStateWithdrawnByBuyer,
		sim.PayTerminalStateExpired,
		sim.PayTerminalStateFailedInsufficientFunds,
		sim.PayTerminalStateFailedInsufficientStock,
		sim.PayTerminalStateFailedUnavailable,
	}
	for _, ts := range nonAccepted {
		invokeOnWorld(t, w, func(world *sim.World) {
			handlePayWithItemResolvedPriceBook(world, &sim.PayWithItemResolved{
				BuyerID:        "alice",
				SellerID:       "bob",
				ItemKind:       "ale",
				QtyPerConsumer: 1,
				ConsumerIDs:    []sim.ActorID{"alice"},
				Amount:         3,
				TerminalState:  ts,
				At:             time.Now().UTC(),
			})
		})
	}

	history := readPriceBook(t, w, sim.PriceBookKey{SellerID: "bob", Item: "ale"})
	if len(history) != 0 {
		t.Errorf("non-Accepted terminals should not append; got %d observations", len(history))
	}
}

// --- TestHandlePayWithItemResolvedPriceBook_WrongEventTypeIsNoop ----
func TestHandlePayWithItemResolvedPriceBook_WrongEventTypeIsNoop(t *testing.T) {
	w, stop := buildPriceBookCascadeWorld(t)
	defer stop()

	// Send a different event type — the handler should ignore it.
	invokeOnWorld(t, w, func(world *sim.World) {
		handlePayWithItemResolvedPriceBook(world, &sim.Spoke{
			SpeakerID: "alice",
			Text:      "hello",
			At:        time.Now().UTC(),
		})
	})

	invokeOnWorld(t, w, func(world *sim.World) {
		if world.PriceBook != nil && len(world.PriceBook) != 0 {
			t.Errorf("unrelated event should not allocate PriceBook entries; got %d", len(world.PriceBook))
		}
	})
}

// --- TestHandlePayWithItemResolvedPriceBook_EmptyConsumerIDsFloorsTo1 ---
func TestHandlePayWithItemResolvedPriceBook_EmptyConsumerIDsFloorsTo1(t *testing.T) {
	w, stop := buildPriceBookCascadeWorld(t)
	defer stop()

	invokeOnWorld(t, w, func(world *sim.World) {
		handlePayWithItemResolvedPriceBook(world, &sim.PayWithItemResolved{
			BuyerID:        "alice",
			SellerID:       "bob",
			ItemKind:       "ale",
			QtyPerConsumer: 1,
			ConsumerIDs:    nil, // malformed but tolerated
			Amount:         3,
			TerminalState:  sim.PayTerminalStateAccepted,
			At:             time.Now().UTC(),
		})
	})

	history := readPriceBook(t, w, sim.PriceBookKey{SellerID: "bob", Item: "ale"})
	if len(history) != 1 || history[0].Consumers != 1 {
		t.Errorf("nil ConsumerIDs should floor Consumers to 1; got %+v", history)
	}
}

// --- TestHandlePayWithItemResolvedPriceBook_MultiConsumerPreserved --
func TestHandlePayWithItemResolvedPriceBook_MultiConsumerPreserved(t *testing.T) {
	w, stop := buildPriceBookCascadeWorld(t)
	defer stop()

	invokeOnWorld(t, w, func(world *sim.World) {
		handlePayWithItemResolvedPriceBook(world, &sim.PayWithItemResolved{
			BuyerID:        "alice",
			SellerID:       "bob",
			ItemKind:       "stew",
			QtyPerConsumer: 2,
			ConsumerIDs:    []sim.ActorID{"alice", "dave", "eve"},
			Amount:         18,
			TerminalState:  sim.PayTerminalStateAccepted,
			At:             time.Now().UTC(),
		})
	})

	history := readPriceBook(t, w, sim.PriceBookKey{SellerID: "bob", Item: "stew"})
	if len(history) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(history))
	}
	if history[0].Consumers != 3 {
		t.Errorf("Consumers = %d, want 3 (preserved from len(ConsumerIDs))", history[0].Consumers)
	}
	if history[0].Qty != 2 {
		t.Errorf("Qty = %d, want 2 (QtyPerConsumer)", history[0].Qty)
	}
}

// --- TestHandlePayWithItemResolvedPriceBook_BundleEmptyItemIsNoop ---
func TestHandlePayWithItemResolvedPriceBook_BundleEmptyItemIsNoop(t *testing.T) {
	// A bundle quote-take (LLM-101) resolves with ItemKind empty — the
	// goods ride Lines and the lump Amount has no per-line split, so no
	// (seller, item) observation exists to record. Without the guard the
	// handler keyed the ring on "" (LLM-246).
	w, stop := buildPriceBookCascadeWorld(t)
	defer stop()

	invokeOnWorld(t, w, func(world *sim.World) {
		handlePayWithItemResolvedPriceBook(world, &sim.PayWithItemResolved{
			BuyerID:       "alice",
			SellerID:      "bob",
			ItemKind:      "",
			ConsumeNow:    true,
			ConsumerIDs:   []sim.ActorID{"alice"},
			Lines:         []sim.QuoteLine{{ItemKind: "ale", Qty: 1}, {ItemKind: "bread", Qty: 1}},
			Amount:        6,
			TerminalState: sim.PayTerminalStateAccepted,
			At:            time.Now().UTC(),
		})
	})

	invokeOnWorld(t, w, func(world *sim.World) {
		if len(world.PriceBook) != 0 {
			t.Errorf("bundle resolve (empty ItemKind) should record nothing; got %d keys", len(world.PriceBook))
		}
	})
}

// --- TestRegisterPriceBook_PanicsOnNil ------------------------------
func TestRegisterPriceBook_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("RegisterPriceBook(nil) should panic")
		}
	}()
	RegisterPriceBook(nil)
}

// Note: end-to-end "Register wires the subscriber to receive events from
// the world's emit dispatch" is not exercised here — sim.EmitForTest is
// only visible inside the sim package (export_test.go). The cascade-side
// integration is structurally trivial: RegisterPriceBook calls w.Subscribe
// once with handlePayWithItemResolvedPriceBook, and the subscriber itself
// is tested directly above. Full event-flow coverage lands when a sim-
// package integration test exercises the pay-with-item accept path.
