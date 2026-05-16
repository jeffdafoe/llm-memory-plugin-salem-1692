package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// order_sweep_test.go — Phase 3 PR S6 sweep coverage. Exercises
// EvaluateOrderSweep deterministically without driving the AfterFunc
// timer chain.

// TestEvaluateOrderSweep_NoOpOnEmpty — sweep is a fast-path on
// world.Orders == 0.
func TestEvaluateOrderSweep_NoOpOnEmpty(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	if _, err := sim.EvaluateOrderSweep(time.Now()).Fn(w); err != nil {
		t.Errorf("EvaluateOrderSweep on empty: %v", err)
	}
}

// TestEvaluateOrderSweep_FlipsReadyPastExpiresAt — the load-bearing
// happy path. Direct world construction + sweep invocation; verify
// state flips and the order remains in the map (terminal, not
// deleted).
func TestEvaluateOrderSweep_FlipsReadyPastExpiresAt(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	at := time.Now().UTC()
	o := &sim.Order{
		ID:          1,
		State:       sim.OrderStateReady,
		BuyerID:     "alice",
		SellerID:    "bob",
		Item:        "stew",
		Qty:         1,
		ConsumerIDs: []sim.ActorID{"alice"},
		CreatedAt:   at.Add(-2 * time.Hour),
		ExpiresAt:   at.Add(-time.Hour),
	}
	w.Orders[o.ID] = o
	if _, err := sim.EvaluateOrderSweep(at).Fn(w); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	if w.Orders[1].State != sim.OrderStateExpired {
		t.Errorf("State = %q, want %q", w.Orders[1].State, sim.OrderStateExpired)
	}
	// Order still present (terminal, not deleted).
	if _, ok := w.Orders[1]; !ok {
		t.Error("Order removed from map after expire; should remain as terminal")
	}
}

// TestEvaluateOrderSweep_SkipsActiveAndTerminal — only Ready entries
// past ExpiresAt are touched. Delivered, Expired, and Ready-in-future
// all survive untouched.
func TestEvaluateOrderSweep_SkipsActiveAndTerminal(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	at := time.Now().UTC()
	deliveredAt := at.Add(-time.Hour)
	w.Orders[1] = &sim.Order{
		ID: 1, State: sim.OrderStateReady, ExpiresAt: at.Add(time.Hour),
	}
	w.Orders[2] = &sim.Order{
		ID: 2, State: sim.OrderStateDelivered, ExpiresAt: at.Add(-time.Hour),
		DeliveredAt: &deliveredAt,
	}
	w.Orders[3] = &sim.Order{
		ID: 3, State: sim.OrderStateExpired, ExpiresAt: at.Add(-2 * time.Hour),
	}
	if _, err := sim.EvaluateOrderSweep(at).Fn(w); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	if w.Orders[1].State != sim.OrderStateReady {
		t.Errorf("Future-ExpiresAt Ready flipped: state = %q", w.Orders[1].State)
	}
	if w.Orders[2].State != sim.OrderStateDelivered {
		t.Errorf("Already-Delivered flipped: state = %q", w.Orders[2].State)
	}
	if w.Orders[3].State != sim.OrderStateExpired {
		t.Errorf("Already-Expired flipped twice: state = %q", w.Orders[3].State)
	}
}

// TestEvaluateOrderSweep_ZeroExpiresAtSkipped — defensive: an Order
// without an ExpiresAt set should not be flipped regardless of `now`.
func TestEvaluateOrderSweep_ZeroExpiresAtSkipped(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	at := time.Now().UTC()
	w.Orders[1] = &sim.Order{
		ID: 1, State: sim.OrderStateReady, ExpiresAt: time.Time{}, // zero
	}
	if _, err := sim.EvaluateOrderSweep(at.Add(100 * time.Hour)).Fn(w); err != nil {
		t.Fatalf("EvaluateOrderSweep: %v", err)
	}
	if w.Orders[1].State != sim.OrderStateReady {
		t.Errorf("Zero-ExpiresAt flipped: state = %q", w.Orders[1].State)
	}
}

// TestRestartExpirePendingOrders_FlipsStaleReady — LoadWorld-time
// expiry pass. Stale Ready flips to Expired in-band, future-Ready
// survives.
func TestRestartExpirePendingOrders_FlipsStaleReady(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	at := time.Now().UTC()
	w.Orders[1] = &sim.Order{
		ID: 1, State: sim.OrderStateReady, ExpiresAt: at.Add(-time.Hour),
	}
	w.Orders[2] = &sim.Order{
		ID: 2, State: sim.OrderStateReady, ExpiresAt: at.Add(time.Hour),
	}
	sim.RestartExpirePendingOrders(w, at)
	if w.Orders[1].State != sim.OrderStateExpired {
		t.Errorf("Stale Ready not flipped: state = %q", w.Orders[1].State)
	}
	if w.Orders[2].State != sim.OrderStateReady {
		t.Errorf("Future Ready flipped: state = %q", w.Orders[2].State)
	}
}

// TestEffectiveOrderTTL_DefaultFallback — zero/negative settings fall
// back to the package default.
func TestEffectiveOrderTTL_DefaultFallback(t *testing.T) {
	if got := sim.EffectiveOrderTTL(sim.WorldSettings{}); got != sim.OrderTTLDefault {
		t.Errorf("zero TTL fallback = %v, want %v", got, sim.OrderTTLDefault)
	}
	if got := sim.EffectiveOrderTTL(sim.WorldSettings{OrderTTL: 5 * time.Minute}); got != 5*time.Minute {
		t.Errorf("override TTL = %v, want 5min", got)
	}
}

// TestEffectiveOrderSweepCadence_DefaultFallback — same pattern.
func TestEffectiveOrderSweepCadence_DefaultFallback(t *testing.T) {
	if got := sim.EffectiveOrderSweepCadence(sim.WorldSettings{}); got != sim.OrderSweepCadenceDefault {
		t.Errorf("zero cadence fallback = %v, want %v", got, sim.OrderSweepCadenceDefault)
	}
	if got := sim.EffectiveOrderSweepCadence(sim.WorldSettings{OrderSweepCadence: 10 * time.Second}); got != 10*time.Second {
		t.Errorf("override cadence = %v, want 10s", got)
	}
}

// TestNextOrderSeq_StartsAtOne — first mint after NewWorld returns
// OrderID(1); OrderID(0) reserved as the unset sentinel.
func TestNextOrderSeq_StartsAtOne(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)
	if got := sim.NextOrderSeq(w); got != 1 {
		t.Errorf("first NextOrderSeq = %d, want 1", got)
	}
	if got := sim.NextOrderSeq(w); got != 2 {
		t.Errorf("second NextOrderSeq = %d, want 2", got)
	}
}

// TestCloneOrder_DeepCopiesConsumerIDsAndDeliveredAt — mutating the
// original's slice/pointer must not leak to the clone.
func TestCloneOrder_DeepCopiesConsumerIDsAndDeliveredAt(t *testing.T) {
	at := time.Now().UTC()
	orig := &sim.Order{
		ID:          1,
		State:       sim.OrderStateDelivered,
		BuyerID:     "alice",
		SellerID:    "bob",
		Item:        "stew",
		Qty:         1,
		ConsumerIDs: []sim.ActorID{"alice", "carol"},
		CreatedAt:   at,
		DeliveredAt: &at,
	}
	clone := sim.CloneOrder(orig)
	if clone == nil {
		t.Fatal("CloneOrder returned nil for non-nil")
	}
	orig.ConsumerIDs[0] = "mallory"
	if clone.ConsumerIDs[0] != "alice" {
		t.Error("ConsumerIDs aliased")
	}
	origTime := at
	mutated := at.Add(time.Hour)
	*orig.DeliveredAt = mutated
	if !clone.DeliveredAt.Equal(origTime) {
		t.Errorf("DeliveredAt pointer aliased: clone holds %v, want %v (mutation leaked)",
			clone.DeliveredAt, origTime)
	}
}

// TestCloneOrder_NilInput — defensive: nil in, nil out.
func TestCloneOrder_NilInput(t *testing.T) {
	if got := sim.CloneOrder(nil); got != nil {
		t.Errorf("CloneOrder(nil) = %+v, want nil", got)
	}
}
