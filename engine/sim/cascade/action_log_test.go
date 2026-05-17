package cascade

import (
	"context"
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
	now := time.Now().UTC()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {
			ID:               "hannah",
			DisplayName:      "Hannah",
			Kind:             sim.KindNPCShared,
			State:            sim.StateIdle,
			StateEnteredAt:   now,
			CurrentHuddleID:  "h1",
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
		"bob": {
			ID:               "bob",
			DisplayName:      "Bob",
			Kind:             sim.KindNPCShared,
			State:            sim.StateIdle,
			StateEnteredAt:   now,
			CurrentHuddleID:  "h1",
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {ID: "tavern", DisplayName: "the tavern"},
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
