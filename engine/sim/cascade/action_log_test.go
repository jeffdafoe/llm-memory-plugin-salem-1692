package cascade

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// action_log_test.go — driver + subscriber tests for the action-log
// cascade slice. Each handler is tested directly (constructed event +
// invocation on the world goroutine via a Command), the compaction
// sweep is tested via runOneActionLogSweep, and the registration
// wiring is verified by driving a real sim.Speak through the world.

// buildActionLogCascadeWorld stands up a world with seeded actors +
// structures, runs it, and returns ready-to-test handles. The world
// goroutine is stopped by the returned cleanup.
func buildActionLogCascadeWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:              "hannah",
			DisplayName:     "Hannah",
			Kind:            sim.KindNPCShared,
			State:           sim.StateIdle,
			CurrentHuddleID: "h1",
			RecentActions:   sim.NewRingBuffer[sim.Action](4),
		},
		"bob": {
			ID:              "bob",
			DisplayName:     "Bob",
			Kind:            sim.KindNPCShared,
			State:           sim.StateIdle,
			CurrentHuddleID: "h1",
			RecentActions:   sim.NewRingBuffer[sim.Action](4),
		},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "the tavern"},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"well": {ID: "well", DisplayName: "the well"},
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

// invokeOnWorld runs fn on the world goroutine inside a Command. Used
// to call subscriber handlers under their real concurrency model
// (state mutations must atomically observe pre-state).
func invokeOnWorld(t *testing.T, w *sim.World, fn func(*sim.World)) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		fn(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("invokeOnWorld: %v", err)
	}
}

// readActionLog pulls the world's ActionLog slice off the goroutine.
func readActionLog(t *testing.T, w *sim.World) []sim.ActionLogEntry {
	t.Helper()
	v, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		out := make([]sim.ActionLogEntry, len(world.ActionLog))
		copy(out, world.ActionLog)
		return out, nil
	}})
	if err != nil {
		t.Fatalf("readActionLog: %v", err)
	}
	return v.([]sim.ActionLogEntry)
}

// --- TestHandleSpokeActionLog_AppendsEntry --------------------------
func TestHandleSpokeActionLog_AppendsEntry(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		handleSpokeActionLog(world, &sim.Spoke{
			SpeakerID:    "hannah",
			HuddleID:     "h1",
			RecipientIDs: []sim.ActorID{"bob"},
			Text:         "Good morrow, Bob.",
			At:           at,
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1", len(got))
	}
	e := got[0]
	if e.ActorID != "hannah" {
		t.Errorf("ActorID = %q, want hannah", e.ActorID)
	}
	if e.ActionType != sim.ActionTypeSpoke {
		t.Errorf("ActionType = %q, want %q", e.ActionType, sim.ActionTypeSpoke)
	}
	if e.HuddleID != "h1" {
		t.Errorf("HuddleID = %q, want h1", e.HuddleID)
	}
	if e.Text != "Good morrow, Bob." {
		t.Errorf("Text = %q, want %q", e.Text, "Good morrow, Bob.")
	}
	if !e.OccurredAt.Equal(at) {
		t.Errorf("OccurredAt = %v, want %v", e.OccurredAt, at)
	}
}

// --- TestHandlePaidActionLog_AppendsEntryAndDerivesHuddle -----------
// The Paid event doesn't carry HuddleID; the handler reads from the
// buyer's CurrentHuddleID at write time (mirrors v1's
// SELECT current_huddle_id FROM actor INSERT pattern).
func TestHandlePaidActionLog_AppendsEntryAndDerivesHuddle(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		handlePaidActionLog(world, &sim.Paid{
			BuyerID:  "hannah",
			SellerID: "bob",
			Amount:   5,
			ForText:  "the ale",
			At:       at,
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1", len(got))
	}
	e := got[0]
	if e.ActorID != "hannah" {
		t.Errorf("ActorID = %q, want hannah (buyer)", e.ActorID)
	}
	if e.ActionType != sim.ActionTypePaid {
		t.Errorf("ActionType = %q, want %q", e.ActionType, sim.ActionTypePaid)
	}
	if e.HuddleID != "h1" {
		t.Errorf("HuddleID = %q, want h1 (derived from buyer's CurrentHuddleID)", e.HuddleID)
	}
	if e.Text != "the ale" {
		t.Errorf("Text = %q, want %q", e.Text, "the ale")
	}
	// ZBBS-WORK-377: recipient (seller display name) + amount on the lean ring.
	if e.CounterpartyName != "Bob" {
		t.Errorf("CounterpartyName = %q, want Bob (seller)", e.CounterpartyName)
	}
	if e.Amount != 5 {
		t.Errorf("Amount = %d, want 5", e.Amount)
	}
}

// --- TestHandlePaidActionLog_UnknownBuyerHuddleEmpty ----------------
// Buyer not in world (defensive — emit fired but the lookup races):
// HuddleID stays "" rather than panicking.
func TestHandlePaidActionLog_UnknownBuyerHuddleEmpty(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	invokeOnWorld(t, w, func(world *sim.World) {
		handlePaidActionLog(world, &sim.Paid{
			BuyerID:  "ghost",
			SellerID: "bob",
			Amount:   1,
			ForText:  "?",
			At:       time.Now().UTC(),
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1", len(got))
	}
	if got[0].HuddleID != "" {
		t.Errorf("HuddleID = %q, want empty for unknown buyer", got[0].HuddleID)
	}
}

// --- TestHandlePayResolvedActionLog ---------------------------------
// ZBBS-HOME-434: an ACCEPTED ledger settle appends a buyer-side Paid row
// (the bridge that puts pay_with_item commerce into the action log — the
// Paid event only fires from the bare-coin path, which HOME-430 removed
// from NPC toolsets). Non-accepted terminals append nothing.
func TestHandlePayResolvedActionLog_AcceptedAppendsPaidRow(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		handlePayResolvedActionLog(world, &sim.PayWithItemResolved{
			LedgerID:       77,
			BuyerID:        "hannah",
			SellerID:       "bob",
			ItemKind:       "ale",
			QtyPerConsumer: 1,
			Amount:         4,
			TerminalState:  sim.PayTerminalStateAccepted,
			HuddleID:       "h1",
			At:             at,
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1", len(got))
	}
	e := got[0]
	if e.ActorID != "hannah" {
		t.Errorf("ActorID = %q, want hannah (buyer)", e.ActorID)
	}
	if e.ActionType != sim.ActionTypePaid {
		t.Errorf("ActionType = %q, want %q", e.ActionType, sim.ActionTypePaid)
	}
	if e.Text != "ale" {
		t.Errorf("Text = %q, want %q", e.Text, "ale")
	}
	if e.HuddleID != "h1" {
		t.Errorf("HuddleID = %q, want h1 (from the event)", e.HuddleID)
	}
	if e.CounterpartyName != "Bob" {
		t.Errorf("CounterpartyName = %q, want Bob (seller)", e.CounterpartyName)
	}
	if e.Amount != 4 {
		t.Errorf("Amount = %d, want 4", e.Amount)
	}
}

func TestHandlePayResolvedActionLog_GroupOrderTotalsQty(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	invokeOnWorld(t, w, func(world *sim.World) {
		handlePayResolvedActionLog(world, &sim.PayWithItemResolved{
			BuyerID:        "hannah",
			SellerID:       "bob",
			ItemKind:       "stew",
			QtyPerConsumer: 2,
			ConsumerIDs:    []sim.ActorID{"hannah", "bob", "cara"},
			Amount:         12,
			TerminalState:  sim.PayTerminalStateAccepted,
			At:             time.Now().UTC(),
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1", len(got))
	}
	if got[0].Text != "6x stew" {
		t.Errorf("Text = %q, want %q (qty-per-consumer × consumers)", got[0].Text, "6x stew")
	}
}

func TestHandlePayResolvedActionLog_NonAcceptedAppendsNothing(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	for _, terminal := range []sim.PayTerminalState{
		sim.PayTerminalStateDeclined,
		sim.PayTerminalStateWithdrawnByBuyer,
		sim.PayTerminalStateExpired,
	} {
		invokeOnWorld(t, w, func(world *sim.World) {
			handlePayResolvedActionLog(world, &sim.PayWithItemResolved{
				BuyerID:        "hannah",
				SellerID:       "bob",
				ItemKind:       "ale",
				QtyPerConsumer: 1,
				Amount:         4,
				TerminalState:  terminal,
				At:             time.Now().UTC(),
			})
		})
	}

	if got := readActionLog(t, w); len(got) != 0 {
		t.Errorf("len(ActionLog) = %d, want 0 (no money moved on non-accepted terminals)", len(got))
	}
}

// --- TestHandleConsumedActionLog_FormatsText -----------------------
// Qty 1 → bare kind; Qty > 1 → "Nx kind".
func TestHandleConsumedActionLog_FormatsText(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	cases := []struct {
		qty  int
		want string
	}{
		{1, "ale"},
		{2, "2x ale"},
		{7, "7x ale"},
	}
	for _, c := range cases {
		invokeOnWorld(t, w, func(world *sim.World) {
			handleConsumedActionLog(world, &sim.ItemConsumed{
				ActorID: "hannah",
				Kind:    "ale",
				Qty:     c.qty,
				At:      time.Now().UTC(),
			})
		})
	}
	got := readActionLog(t, w)
	if len(got) != len(cases) {
		t.Fatalf("len(ActionLog) = %d, want %d", len(got), len(cases))
	}
	for i, c := range cases {
		if got[i].Text != c.want {
			t.Errorf("case %d: Text = %q, want %q", i, got[i].Text, c.want)
		}
		if got[i].ActionType != sim.ActionTypeConsumed {
			t.Errorf("case %d: ActionType = %q, want %q", i, got[i].ActionType, sim.ActionTypeConsumed)
		}
		if got[i].HuddleID != "h1" {
			t.Errorf("case %d: HuddleID = %q, want h1", i, got[i].HuddleID)
		}
	}
}

// --- TestHandleOrderDeliveredActionLog_AppendsSellerSide -----------
func TestHandleOrderDeliveredActionLog_AppendsSellerSide(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		handleOrderDeliveredActionLog(world, &sim.OrderDelivered{
			OrderID:  1,
			BuyerID:  "hannah",
			SellerID: "bob",
			Item:     "bread",
			Qty:      3,
			At:       at,
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1", len(got))
	}
	if got[0].ActorID != "bob" {
		t.Errorf("ActorID = %q, want bob (seller)", got[0].ActorID)
	}
	if got[0].ActionType != sim.ActionTypeDelivered {
		t.Errorf("ActionType = %q, want %q", got[0].ActionType, sim.ActionTypeDelivered)
	}
	if got[0].Text != "3x bread" {
		t.Errorf("Text = %q, want %q", got[0].Text, "3x bread")
	}
	if got[0].HuddleID != "h1" {
		t.Errorf("HuddleID = %q, want h1 (derived from seller's huddle)", got[0].HuddleID)
	}
	// ZBBS-WORK-377: recipient (buyer display name) on the lean ring; no amount.
	if got[0].CounterpartyName != "Hannah" {
		t.Errorf("CounterpartyName = %q, want Hannah (buyer)", got[0].CounterpartyName)
	}
	if got[0].Amount != 0 {
		t.Errorf("Amount = %d, want 0 (delivered carries no amount)", got[0].Amount)
	}
}

// --- TestHandleActorArrivedActionLog_StructureLabelAsText ----------
// Arrival inside a known structure: Text = structure DisplayName,
// HuddleID empty (arrival precedes huddle join).
func TestHandleActorArrivedActionLog_StructureLabelAsText(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		handleActorArrivedActionLog(world, &sim.ActorArrived{
			ActorID:          "hannah",
			FinalStructureID: "tavern",
			At:               at,
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1", len(got))
	}
	if got[0].ActionType != sim.ActionTypeWalked {
		t.Errorf("ActionType = %q, want %q", got[0].ActionType, sim.ActionTypeWalked)
	}
	if got[0].Text != "the tavern" {
		t.Errorf("Text = %q, want %q (structure DisplayName)", got[0].Text, "the tavern")
	}
	if got[0].HuddleID != "" {
		t.Errorf("HuddleID = %q, want empty (arrival precedes huddle join)", got[0].HuddleID)
	}
}

// --- TestHandleActorArrivedActionLog_OutdoorEmptyText --------------
// Outdoor arrival (FinalStructureID empty) → Text empty.
func TestHandleActorArrivedActionLog_OutdoorEmptyText(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	invokeOnWorld(t, w, func(world *sim.World) {
		handleActorArrivedActionLog(world, &sim.ActorArrived{
			ActorID: "hannah",
			At:      time.Now().UTC(),
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1", len(got))
	}
	if got[0].Text != "" {
		t.Errorf("Text = %q, want empty for outdoor arrival", got[0].Text)
	}
}

// --- TestHandleActorArrivedActionLog_UnknownStructureEmptyText -----
// FinalStructureID set but the structure isn't in World — defensive
// path: Text empty rather than a stale label.
func TestHandleActorArrivedActionLog_UnknownStructureEmptyText(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	invokeOnWorld(t, w, func(world *sim.World) {
		handleActorArrivedActionLog(world, &sim.ActorArrived{
			ActorID:          "hannah",
			FinalStructureID: "ghost-structure",
			At:               time.Now().UTC(),
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1", len(got))
	}
	if got[0].Text != "" {
		t.Errorf("Text = %q, want empty for unknown structure", got[0].Text)
	}
}

// --- TestHandleActorArrivedActionLog_VisitNamesShop ---------------
// StructureVisit/knock arrival: the actor stops at a loiter slot OUTSIDE the
// shop (FinalStructureID empty) but DestStructureID names the shop. Text must
// name the destination, not go blank — this is the core ZBBS-WORK-359 fix.
func TestHandleActorArrivedActionLog_VisitNamesShop(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	invokeOnWorld(t, w, func(world *sim.World) {
		handleActorArrivedActionLog(world, &sim.ActorArrived{
			ActorID:         "hannah",
			DestStructureID: "tavern",
			At:              time.Now().UTC(),
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1", len(got))
	}
	if got[0].Text != "the tavern" {
		t.Errorf("Text = %q, want %q (destination shop, FinalStructureID empty)", got[0].Text, "the tavern")
	}
}

// --- TestHandleActorArrivedActionLog_ObjectVisitNamesObject -------
// ObjectVisit arrival (well/tree/pile): DestObjectID names a village object,
// not a structure. Text resolves the object's DisplayName.
func TestHandleActorArrivedActionLog_ObjectVisitNamesObject(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	invokeOnWorld(t, w, func(world *sim.World) {
		handleActorArrivedActionLog(world, &sim.ActorArrived{
			ActorID:      "hannah",
			DestObjectID: "well",
			At:           time.Now().UTC(),
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1", len(got))
	}
	if got[0].Text != "the well" {
		t.Errorf("Text = %q, want %q (destination object)", got[0].Text, "the well")
	}
}

// --- TestHandlers_IgnoreUnrelatedEvents -----------------------------
// Each handler should no-op on events of the wrong type — the
// subscriber gets fanned out to every event, the type assertion is
// the filter.
func TestHandlers_IgnoreUnrelatedEvents(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	// ActorMoved is not one of the 5 watched events.
	invokeOnWorld(t, w, func(world *sim.World) {
		evt := &sim.ActorMoved{ActorID: "hannah", At: time.Now().UTC()}
		handleSpokeActionLog(world, evt)
		handlePaidActionLog(world, evt)
		handleConsumedActionLog(world, evt)
		handleOrderDeliveredActionLog(world, evt)
		handleActorArrivedActionLog(world, evt)
	})

	if got := readActionLog(t, w); len(got) != 0 {
		t.Errorf("len(ActionLog) = %d, want 0 (no handler should fire on unrelated events)", len(got))
	}
}

// --- TestRunOneActionLogSweep_DropsStaleEntries ---------------------
// Seed entries past + within retention, run the sweep, verify the
// stale ones are dropped.
func TestRunOneActionLogSweep_DropsStaleEntries(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	now := time.Now().UTC()
	retention := 1 * time.Hour
	// Two stale entries (older than retention from now), two fresh.
	seed := []sim.ActionLogEntry{
		{ActorID: "a", OccurredAt: now.Add(-3 * time.Hour), ActionType: sim.ActionTypeSpoke, Text: "stale1"},
		{ActorID: "b", OccurredAt: now.Add(-2 * time.Hour), ActionType: sim.ActionTypeSpoke, Text: "stale2"},
		{ActorID: "c", OccurredAt: now.Add(-30 * time.Minute), ActionType: sim.ActionTypeSpoke, Text: "fresh1"},
		{ActorID: "d", OccurredAt: now.Add(-5 * time.Minute), ActionType: sim.ActionTypeSpoke, Text: "fresh2"},
	}
	for _, e := range seed {
		if _, err := w.Send(sim.AppendActionLogEntry(e)); err != nil {
			t.Fatalf("Append %s: %v", e.ActorID, err)
		}
	}

	runOneActionLogSweep(context.Background(), w, retention)

	got := readActionLog(t, w)
	if len(got) != 2 {
		t.Fatalf("len(ActionLog) = %d, want 2 (only fresh entries kept)", len(got))
	}
	if got[0].Text != "fresh1" || got[1].Text != "fresh2" {
		t.Errorf("texts = [%q, %q], want [fresh1, fresh2]", got[0].Text, got[1].Text)
	}
}

// --- TestRunOneActionLogSweep_ContextCancelledNoOp ------------------
// Cancelled context before the SendContext call: the sweep returns
// without touching the log.
func TestRunOneActionLogSweep_ContextCancelledNoOp(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	now := time.Now().UTC()
	if _, err := w.Send(sim.AppendActionLogEntry(sim.ActionLogEntry{
		ActorID:    "a",
		OccurredAt: now.Add(-3 * time.Hour),
		ActionType: sim.ActionTypeSpoke,
	})); err != nil {
		t.Fatalf("Append: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runOneActionLogSweep(ctx, w, 1*time.Hour)

	if got := readActionLog(t, w); len(got) != 1 {
		t.Errorf("len(ActionLog) = %d, want 1 (cancelled sweep should be a no-op)", len(got))
	}
}

// --- TestRegisterActionLog_WiresSubscribers -------------------------
// End-to-end: register the cascade slice, drive a real sim.Speak
// command through the world, verify ActionLog has the speak row. This
// is the wiring test that proves the subscribers are actually
// subscribed to the bus.
func TestRegisterActionLog_WiresSubscribers(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	// Put hannah and bob in the same huddle so Speak finds a recipient.
	invokeOnWorld(t, w, func(world *sim.World) {
		for _, id := range []sim.ActorID{"hannah", "bob"} {
			if a, ok := world.Actors[id]; ok {
				a.CurrentHuddleID = "h1"
			}
		}
	})

	// Register against a long-lived ctx — the sweep goroutine runs
	// until cleanup; we don't drive ticker-cadence here.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	RegisterActionLog(ctx, w)

	at := time.Now().UTC()
	if _, err := w.Send(sim.Speak("hannah", "Good morrow.", at)); err != nil {
		t.Fatalf("Speak: %v", err)
	}

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("len(ActionLog) = %d, want 1 (Speak should land an entry via the wired subscriber)", len(got))
	}
	e := got[0]
	if e.ActorID != "hannah" || e.ActionType != sim.ActionTypeSpoke || e.Text != "Good morrow." {
		t.Errorf("entry = %+v, want speak from hannah", e)
	}
}

// --- TestRegisterActionLog_PanicsOnNilWorld -------------------------
func TestRegisterActionLog_PanicsOnNilWorld(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("RegisterActionLog(nil) did not panic")
		}
	}()
	RegisterActionLog(context.Background(), nil)
}

// --- TestFormatItemQty -----------------------------------------------
func TestFormatItemQty(t *testing.T) {
	cases := []struct {
		kind sim.ItemKind
		qty  int
		want string
	}{
		{"ale", 1, "ale"},
		{"bread", 2, "2x bread"},
		{"ale", 7, "7x ale"},
		{"ale", 0, "ale"},  // defensive: 0 / negative qty renders as bare kind
		{"ale", -1, "ale"}, // ditto
	}
	for _, c := range cases {
		if got := formatItemQty(c.kind, c.qty); got != c.want {
			t.Errorf("formatItemQty(%q, %d) = %q, want %q", c.kind, c.qty, got, c.want)
		}
	}
}

// recordingActionLogSink captures DurableActionLogRows for assertion in
// the subscriber-emission test. Append fires on the world goroutine, so
// the mutex guards the cross-goroutine read from the test.
type recordingActionLogSink struct {
	mu   sync.Mutex
	rows []sim.DurableActionLogRow
}

func (r *recordingActionLogSink) Append(_ context.Context, row sim.DurableActionLogRow) error {
	r.mu.Lock()
	r.rows = append(r.rows, row)
	r.mu.Unlock()
	return nil
}

func (r *recordingActionLogSink) snapshot() []sim.DurableActionLogRow {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sim.DurableActionLogRow, len(r.rows))
	copy(out, r.rows)
	return out
}

// --- TestSubscribers_EmitDurableRows -------------------------------
// ZBBS-WORK-376: each subscriber, after the lean in-memory append,
// mirrors a structured DurableActionLogRow to the installed sink. This
// asserts the per-kind payload shape, the speaker-name denormalization,
// and the PC-vs-NPC source derivation. (The existing handler tests run
// with no sink installed, so the durable mirror is a no-op there.)
func TestSubscribers_EmitDurableRows(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	rec := &recordingActionLogSink{}
	invokeOnWorld(t, w, func(world *sim.World) {
		world.SetActionLogSink(rec)
		// A PC (LoginUsername set) to exercise source="player". The source
		// derivation keys on LoginUsername, not Kind.
		world.Actors["jeff"] = &sim.Actor{
			ID:              "jeff",
			DisplayName:     "Jefferey",
			LoginUsername:   "jeff",
			Kind:            sim.KindNPCShared,
			State:           sim.StateIdle,
			CurrentHuddleID: "h1",
			RecentActions:   sim.NewRingBuffer[sim.Action](4),
		}
	})

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		handleSpokeActionLog(world, &sim.Spoke{SpeakerID: "hannah", HuddleID: "h1", Text: "Good morrow, Bob.", At: at})
		handlePaidActionLog(world, &sim.Paid{BuyerID: "hannah", SellerID: "bob", Amount: 5, ForText: "the ale", At: at})
		handleConsumedActionLog(world, &sim.ItemConsumed{ActorID: "hannah", Kind: "ale", Qty: 2, At: at})
		handleOrderDeliveredActionLog(world, &sim.OrderDelivered{BuyerID: "hannah", SellerID: "bob", Item: "bread", Qty: 3, Amount: 9, At: at})
		handleActorArrivedActionLog(world, &sim.ActorArrived{ActorID: "hannah", FinalStructureID: "tavern", At: at})
		handleTookBreakActionLog(world, &sim.TookBreak{ActorID: "hannah", Reason: "weary", At: at})
		handleSpokeActionLog(world, &sim.Spoke{SpeakerID: "jeff", HuddleID: "h1", Text: "Aye.", At: at})
	})

	rows := rec.snapshot()
	if len(rows) != 7 {
		t.Fatalf("recorded %d durable rows, want 7", len(rows))
	}

	wantStr := func(i int, p map[string]any, key, want string) {
		t.Helper()
		got, _ := p[key].(string)
		if got != want {
			t.Errorf("row %d payload[%q] = %q, want %q", i, key, got, want)
		}
	}
	wantInt := func(i int, p map[string]any, key string, want int) {
		t.Helper()
		got, _ := p[key].(int)
		if got != want {
			t.Errorf("row %d payload[%q] = %v, want %d", i, key, p[key], want)
		}
	}

	// 0: spoke (NPC) — text payload, agent source, denormalized speaker.
	if rows[0].ActorID != "hannah" || rows[0].ActionType != sim.ActionTypeSpoke ||
		rows[0].Source != "agent" || rows[0].SpeakerName != "Hannah" || rows[0].HuddleID != "h1" {
		t.Errorf("row 0 spoke header = %+v", rows[0])
	}
	wantStr(0, rows[0].Payload, "text", "Good morrow, Bob.")

	// 1: paid — recipient is the seller's display name; amount + for.
	if rows[1].ActorID != "hannah" || rows[1].ActionType != sim.ActionTypePaid ||
		rows[1].SpeakerName != "Hannah" || rows[1].HuddleID != "h1" {
		t.Errorf("row 1 paid header = %+v", rows[1])
	}
	wantStr(1, rows[1].Payload, "recipient", "Bob")
	wantStr(1, rows[1].Payload, "for", "the ale")
	wantInt(1, rows[1].Payload, "amount", 5)

	// 2: consumed — item + qty.
	if rows[2].ActorID != "hannah" || rows[2].ActionType != sim.ActionTypeConsumed {
		t.Errorf("row 2 consumed header = %+v", rows[2])
	}
	wantStr(2, rows[2].Payload, "item", "ale")
	wantInt(2, rows[2].Payload, "qty", 2)

	// 3: delivered — seller acts; recipient is the buyer.
	if rows[3].ActorID != "bob" || rows[3].ActionType != sim.ActionTypeDelivered || rows[3].SpeakerName != "Bob" {
		t.Errorf("row 3 delivered header = %+v", rows[3])
	}
	wantStr(3, rows[3].Payload, "recipient", "Hannah")
	wantStr(3, rows[3].Payload, "item", "bread")
	wantInt(3, rows[3].Payload, "qty", 3)
	wantInt(3, rows[3].Payload, "amount", 9)

	// 4: walked — destination name; huddle empty (arrival precedes join).
	if rows[4].ActorID != "hannah" || rows[4].ActionType != sim.ActionTypeWalked || rows[4].HuddleID != "" {
		t.Errorf("row 4 walked header = %+v", rows[4])
	}
	wantStr(4, rows[4].Payload, "destination", "the tavern")

	// 5: took_break — reason.
	if rows[5].ActorID != "hannah" || rows[5].ActionType != sim.ActionTypeTookBreak {
		t.Errorf("row 5 took_break header = %+v", rows[5])
	}
	wantStr(5, rows[5].Payload, "reason", "weary")

	// 6: spoke by a PC → source="player".
	if rows[6].Source != "player" || rows[6].SpeakerName != "Jefferey" {
		t.Errorf("row 6 PC spoke source/speaker = %q/%q, want player/Jefferey", rows[6].Source, rows[6].SpeakerName)
	}
}
