package handlers_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// pay_reactor_test.go — coverage of handlePaidWarrants (registered via
// handlers.RegisterPayHandlers). Drives the subscriber by sending real
// sim.Pay commands so the test exercises the production wire: Pay emits
// Paid, subscriber stamps PaidWarrantReason on the seller.
//
// Source-dedup behavior of the warrant infrastructure itself is tested in
// sim/reactor_pr3a_test.go — this file only verifies that the pay
// subscriber stamps with the right SHAPE (kind, payload, SourceEventID,
// Force).

type payReactorActor struct {
	id          sim.ActorID
	displayName string
	kind        sim.ActorKind
	huddleID    sim.HuddleID
	coins       int
}

func buildPayReactorWorld(t *testing.T, specs ...payReactorActor) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	seed := make(map[sim.ActorID]*sim.Actor, len(specs))
	for _, s := range specs {
		seed[s.id] = &sim.Actor{
			ID:              s.id,
			DisplayName:     s.displayName,
			Kind:            s.kind,
			State:           sim.StateIdle,
			CurrentHuddleID: s.huddleID,
			Coins:           s.coins,
			RecentActions:   sim.NewRingBuffer[sim.Action](4),
		}
	}
	handles.Actors.Seed(seed)

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	handlers.RegisterPayHandlers(w)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() { cancel(); <-done }
}

// peekActorWarrants reads an actor's Warrants slice off the world
// goroutine for assertion. Mirrors peekWarrants in speech_reactor_test.go
// but uses a distinct name to avoid duplicate-symbol clashes in the same
// _test package.
func peekActorWarrants(t *testing.T, w *sim.World, id sim.ActorID) []sim.WarrantMeta {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a, ok := world.Actors[id]
		if !ok {
			return []sim.WarrantMeta(nil), nil
		}
		return append([]sim.WarrantMeta(nil), a.Warrants...), nil
	}})
	if err != nil {
		t.Fatalf("peekActorWarrants(%s): %v", id, err)
	}
	return v.([]sim.WarrantMeta)
}

// --- TestPayReactor_SellerGetsWarrant: happy path — buyer pays seller,
// seller's Warrants list has one PaidWarrantReason entry.
func TestPayReactor_SellerGetsWarrant(t *testing.T) {
	w, stop := buildPayReactorWorld(t,
		payReactorActor{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payReactorActor{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	if _, err := w.Send(sim.Pay("hannah", "Ezekiel Crane", 3, "ale", time.Now().UTC())); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	ws := peekActorWarrants(t, w, "ezekiel")
	if len(ws) != 1 {
		t.Fatalf("ezekiel.Warrants = %d, want 1", len(ws))
	}
	got := ws[0]
	if got.Kind() != sim.WarrantKindPaid {
		t.Errorf("Kind = %q, want %q", got.Kind(), sim.WarrantKindPaid)
	}
	if got.Force {
		t.Error("Force = true, want false (PR B locked: no Force on pay warrants)")
	}
	if got.TriggerActorID != "hannah" {
		t.Errorf("TriggerActorID = %q, want hannah", got.TriggerActorID)
	}
	if got.SourceActorID != "hannah" {
		t.Errorf("SourceActorID = %q, want hannah", got.SourceActorID)
	}
	if got.SourceEventID == 0 {
		t.Error("SourceEventID = 0 (must be nonzero for source-aware dedup)")
	}
	reason, ok := got.Reason.(sim.PaidWarrantReason)
	if !ok {
		t.Fatalf("Reason type = %T, want PaidWarrantReason", got.Reason)
	}
	if reason.Buyer != "hannah" {
		t.Errorf("reason.Buyer = %q, want hannah", reason.Buyer)
	}
	if reason.Amount != 3 {
		t.Errorf("reason.Amount = %d, want 3", reason.Amount)
	}
	if reason.ForText != "ale" {
		t.Errorf("reason.ForText = %q, want ale", reason.ForText)
	}
	if reason.PaidID != got.SourceEventID {
		t.Errorf("PaidID (%d) != SourceEventID (%d) — must alias", reason.PaidID, got.SourceEventID)
	}
}

// --- TestPayReactor_BuyerHasNoWarrant: confirm only the seller is
// warranted. The buyer just committed the pay; they don't need to react
// to themselves.
func TestPayReactor_BuyerHasNoWarrant(t *testing.T) {
	w, stop := buildPayReactorWorld(t,
		payReactorActor{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payReactorActor{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	if _, err := w.Send(sim.Pay("hannah", "Ezekiel Crane", 3, "", time.Now().UTC())); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if ws := peekActorWarrants(t, w, "hannah"); len(ws) != 0 {
		t.Errorf("hannah (buyer) Warrants = %d, want 0", len(ws))
	}
}

// --- TestPayReactor_ExcerptTruncated: ForText longer than
// MaxSalientFactTextLen truncates in the warrant payload (the per-tick
// prompt cost bound).
func TestPayReactor_ExcerptTruncated(t *testing.T) {
	// Build a ForText that's at the handler's 200-char cap (max allowed by
	// the schema) — the warrant Excerpt then truncates to MaxSalientFactTextLen
	// runes. The 200-char text is BELOW the 220-rune cap so it won't actually
	// truncate; we need a ForText > 220 runes to test truncation. Since the
	// handler caps at 200, we send through sim.Pay directly with a longer
	// string — the truncation happens regardless of the handler's cap (the
	// substrate path is the floor).
	longForText := ""
	for i := 0; i < 300; i++ {
		longForText += "x"
	}
	w, stop := buildPayReactorWorld(t,
		payReactorActor{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payReactorActor{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	if _, err := w.Send(sim.Pay("hannah", "Ezekiel Crane", 3, longForText, time.Now().UTC())); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	ws := peekActorWarrants(t, w, "ezekiel")
	if len(ws) != 1 {
		t.Fatalf("ezekiel.Warrants = %d, want 1", len(ws))
	}
	reason := ws[0].Reason.(sim.PaidWarrantReason)
	if got := len([]rune(reason.ForText)); got != sim.MaxSalientFactTextLen {
		t.Errorf("ForText excerpt = %d runes, want %d (truncated)", got, sim.MaxSalientFactTextLen)
	}
}

// --- TestPayReactor_NoSelfStamp: the seller's pre-existing warrant set
// is empty if they never received a pay (sanity for the "only seller is
// warranted" claim in BuyerHasNoWarrant).
func TestPayReactor_NoWarrantBeforePay(t *testing.T) {
	w, stop := buildPayReactorWorld(t,
		payReactorActor{id: "hannah", displayName: "Hannah", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		payReactorActor{id: "ezekiel", displayName: "Ezekiel Crane", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	if ws := peekActorWarrants(t, w, "ezekiel"); len(ws) != 0 {
		t.Errorf("ezekiel Warrants before pay = %d, want 0", len(ws))
	}
}

// --- TestPayReactor_RegistrationRequiresWorld: nil world panics.
func TestPayReactor_RegistrationRequiresWorld(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("RegisterPayHandlers(nil): want panic, got none")
		}
	}()
	handlers.RegisterPayHandlers(nil)
}
