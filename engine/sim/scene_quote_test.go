package sim_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// scene_quote_test.go — Phase 3 PR S3 coverage of the substrate
// (SceneQuote struct + Clone), the SceneQuoteCreate Command Fn (10
// gates + happy paths + duplicate-key + cap), Snapshot isolation,
// and the restart helpers (restartExpireScannedQuotes,
// rebuildSceneQuoteIndex). Sweep tests live in
// scene_quote_sweep_test.go; handler-side static validation lives
// in handlers/scene_quote_test.go.

// quoteTestActor — minimal Actor seed for scene-quote tests.
type quoteTestActor struct {
	id           sim.ActorID
	displayName  string
	kind         sim.ActorKind
	huddleID     sim.HuddleID
	inventory    map[sim.ItemKind]int
	breakUntil   *time.Time
	lastPCSeenAt *time.Time // PC presence stamp (ZBBS-HOME-408); nil = stale/ghost
}

// buildQuoteTestWorld constructs a world with:
//   - seeded ItemKinds (via mem.SeedItemKinds — same fixture used
//     by consume tests, so "ale", "bread", "stew" are valid).
//   - One huddle containing every actor whose huddleID matches.
//   - One scene observing that huddle (BoundUnbounded for test
//     simplicity — the gates we exercise don't depend on scene
//     bound).
//
// Caller specifies the huddle + scene IDs. Pass an empty huddleID on
// an actor to leave them out of every huddle (used to test
// "seller not in a conversation" gate).
func buildQuoteTestWorld(
	t *testing.T,
	huddleID sim.HuddleID,
	sceneID sim.SceneID,
	actors []quoteTestActor,
) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())

	now := time.Now().UTC()
	actorSeed := make(map[sim.ActorID]*sim.Actor, len(actors))
	huddleMembers := make(map[sim.ActorID]struct{}, len(actors))
	for _, s := range actors {
		kind := s.kind
		// Default Kind=NPCShared keeps existing handler interactions
		// (e.g. consume) working without per-test boilerplate.
		// Tests that need PC explicitly set Kind=KindPC.
		_ = kind
		a := &sim.Actor{
			ID:               s.id,
			DisplayName:      s.displayName,
			Kind:             s.kind,
			State:            sim.StateIdle,
			StateEnteredAt:   now,
			Inventory:        s.inventory,
			CurrentHuddleID:  s.huddleID,
			BreakUntil:       s.breakUntil,
			LastPCSeenAt:     s.lastPCSeenAt,
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
		}
		actorSeed[s.id] = a
		if s.huddleID == huddleID && huddleID != "" {
			huddleMembers[s.id] = struct{}{}
		}
	}
	handles.Actors.Seed(actorSeed)

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

	if huddleID != "" {
		if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Huddles[huddleID] = &sim.Huddle{
				ID:        huddleID,
				Members:   huddleMembers,
				StartedAt: now,
			}
			world.Scenes[sceneID] = &sim.Scene{
				ID:       sceneID,
				OriginAt: now,
				Bound:    sim.NewUnboundedBound(),
				Huddles:  map[sim.HuddleID]struct{}{huddleID: {}},
			}
			// Actor.CurrentHuddleID was set during Seed; rebuild
			// the actorsByHuddle index so target-buyer + consumer
			// resolution can find the seeded members.
			sim.RebuildIndicesForTest(world)
			return nil, nil
		}}); err != nil {
			cancel()
			<-done
			t.Fatalf("seed scene+huddle: %v", err)
		}
	}

	return w, func() { cancel(); <-done }
}

// captureSceneQuoteCreated registers a subscriber recording every
// SceneQuoteCreated event for inspection.
func captureSceneQuoteCreated(t *testing.T, w *sim.World) *[]sim.SceneQuoteCreated {
	t.Helper()
	var out []sim.SceneQuoteCreated
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			if e, ok := evt.(*sim.SceneQuoteCreated); ok {
				out = append(out, *e)
			}
		}))
		return nil, nil
	}}); err != nil {
		t.Fatalf("captureSceneQuoteCreated: %v", err)
	}
	return &out
}

// captureSceneQuoteExpired records every SceneQuoteExpired event.
func captureSceneQuoteExpired(t *testing.T, w *sim.World) *[]sim.SceneQuoteExpired {
	t.Helper()
	var out []sim.SceneQuoteExpired
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			if e, ok := evt.(*sim.SceneQuoteExpired); ok {
				out = append(out, *e)
			}
		}))
		return nil, nil
	}}); err != nil {
		t.Fatalf("captureSceneQuoteExpired: %v", err)
	}
	return &out
}

// liveQuoteView is the per-world quote projection unit tests read
// for fields the published Snapshot doesn't expose (terminal-state
// quotes still in World.Quotes after sweep).
type liveQuoteView struct {
	Quotes   map[sim.QuoteID]sim.SceneQuote
	QuoteSeq uint64
	SceneIdx map[sim.SceneID][]sim.QuoteID
}

func readLiveQuotes(t *testing.T, w *sim.World) liveQuoteView {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		view := liveQuoteView{
			Quotes:   make(map[sim.QuoteID]sim.SceneQuote),
			SceneIdx: make(map[sim.SceneID][]sim.QuoteID),
		}
		for id, q := range world.Quotes {
			if q == nil {
				continue
			}
			view.Quotes[id] = *q
		}
		for sid, scene := range world.Scenes {
			if scene == nil {
				continue
			}
			if len(scene.QuoteIDs) == 0 {
				continue
			}
			ids := make([]sim.QuoteID, len(scene.QuoteIDs))
			copy(ids, scene.QuoteIDs)
			view.SceneIdx[sid] = ids
		}
		view.QuoteSeq = sim.QuoteSeqForTest(world)
		return view, nil
	}})
	if err != nil {
		t.Fatalf("readLiveQuotes: %v", err)
	}
	return res.(liveQuoteView)
}

// ---- Happy paths ----

func TestSceneQuoteCreate_HappyPath_Public(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	captured := captureSceneQuoteCreated(t, w)
	at := time.Now().UTC()
	res, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 2, false, "", nil, at))
	if err != nil {
		t.Fatalf("SceneQuoteCreate: %v", err)
	}
	result, ok := res.(sim.SceneQuoteCreateResult)
	if !ok {
		t.Fatalf("result type = %T, want SceneQuoteCreateResult", res)
	}
	if result.QuoteID != 1 {
		t.Errorf("QuoteID = %d, want 1 (first mint)", result.QuoteID)
	}
	wantExpiry := at.Add(sim.SceneQuoteTTLDefault)
	if !result.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("ExpiresAt = %v, want %v", result.ExpiresAt, wantExpiry)
	}

	if len(*captured) != 1 {
		t.Fatalf("SceneQuoteCreated events = %d, want 1", len(*captured))
	}
	evt := (*captured)[0]
	if evt.QuoteID != result.QuoteID || evt.SellerID != "aldous" || evt.ItemKind != "ale" {
		t.Errorf("event mismatch: %+v", evt)
	}
	if evt.TargetBuyer != "" {
		t.Errorf("public quote TargetBuyer = %q, want empty", evt.TargetBuyer)
	}

	view := readLiveQuotes(t, w)
	if len(view.Quotes) != 1 {
		t.Fatalf("World.Quotes count = %d, want 1", len(view.Quotes))
	}
	q := view.Quotes[result.QuoteID]
	if q.State != sim.SceneQuoteStateActive {
		t.Errorf("quote state = %q, want active", q.State)
	}
	if q.SourceEventID == 0 {
		t.Error("quote SourceEventID not populated post-emit")
	}
	if ids := view.SceneIdx["sc1"]; len(ids) != 1 || ids[0] != result.QuoteID {
		t.Errorf("scene index = %v, want [%d]", ids, result.QuoteID)
	}
}

// TestSceneQuoteCreate_EatHereClamp (ZBBS-WORK-405): a quote for a
// non-portable consumable proposed as take-home stands as eat-here — the
// seller-side mirror of the pay_with_item buyer clamp — and the result
// reports the adjustment so tool feedback can tell the seller model. A
// portable kind keeps the proposed take-home.
func TestSceneQuoteCreate_EatHereClamp(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3, "bread": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()
	at := time.Now().UTC()

	// ale: consumable, no portable capability in the fixture — clamps.
	res, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 2, false, "", nil, at))
	if err != nil {
		t.Fatalf("SceneQuoteCreate ale: %v", err)
	}
	result := res.(sim.SceneQuoteCreateResult)
	if !result.EatHereClamped {
		t.Error("ale result EatHereClamped = false, want true")
	}
	view := readLiveQuotes(t, w)
	if q := view.Quotes[result.QuoteID]; !q.ConsumeNow {
		t.Error("ale quote ConsumeNow = false, want true (eat-here clamp)")
	}

	// bread: portable in the fixture — the proposed take-home survives.
	res, err = w.Send(sim.SceneQuoteCreate("aldous", "bread", 1, 2, false, "", nil, at))
	if err != nil {
		t.Fatalf("SceneQuoteCreate bread: %v", err)
	}
	result = res.(sim.SceneQuoteCreateResult)
	if result.EatHereClamped {
		t.Error("bread result EatHereClamped = true, want false (portable)")
	}
	view = readLiveQuotes(t, w)
	if q := view.Quotes[result.QuoteID]; q.ConsumeNow {
		t.Error("bread quote ConsumeNow = true, want false (take-home survives)")
	}
}

func TestSceneQuoteCreate_HappyPath_TargetedNPC(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	captured := captureSceneQuoteCreated(t, w)
	_, err := w.Send(sim.SceneQuoteCreate("aldous", "stew", 1, 5, true, "Bea", nil, time.Now().UTC()))
	if err != nil {
		t.Fatalf("SceneQuoteCreate: %v", err)
	}
	if len(*captured) != 1 || (*captured)[0].TargetBuyer != "bea" {
		t.Fatalf("targeted quote: %+v", *captured)
	}
}

func TestSceneQuoteCreate_HappyPath_GroupOrder(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 5}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
		{id: "cyrus", displayName: "Cyrus", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	captured := captureSceneQuoteCreated(t, w)
	_, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 2, 12, false, "", []string{"Bea", "Cyrus"}, time.Now().UTC()))
	if err != nil {
		t.Fatalf("SceneQuoteCreate group: %v", err)
	}
	if len(*captured) != 1 {
		t.Fatalf("events = %d, want 1", len(*captured))
	}
	got := (*captured)[0]
	if len(got.ConsumerIDs) != 2 {
		t.Fatalf("ConsumerIDs = %v, want 2", got.ConsumerIDs)
	}
	have := map[sim.ActorID]bool{got.ConsumerIDs[0]: true, got.ConsumerIDs[1]: true}
	if !have["bea"] || !have["cyrus"] {
		t.Errorf("ConsumerIDs = %v, want {bea, cyrus}", got.ConsumerIDs)
	}
}

// ---- Gate rejections ----

func TestSceneQuoteCreate_Reject_NoHuddle(t *testing.T) {
	// Seller has no current huddle.
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "", inventory: map[sim.ItemKind]int{"ale": 3}},
	})
	defer stop()

	_, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 2, false, "", nil, time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "not in a conversation") {
		t.Fatalf("err = %v, want 'not in a conversation'", err)
	}
}

func TestSceneQuoteCreate_Reject_NoScene(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	// Drop the scene to simulate "huddle exists but no scene observes it."
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.Scenes, "sc1")
		return nil, nil
	}}); err != nil {
		t.Fatalf("drop scene: %v", err)
	}

	_, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 2, false, "", nil, time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "anchored to a scene") {
		t.Fatalf("err = %v, want 'anchored to a scene'", err)
	}
}

func TestSceneQuoteCreate_Reject_UnknownItem(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	// ZBBS-WORK-412: an unknown quoted good is now MINTED (a discovery); the
	// quote then fails the stock gate (Aldous holds 0 of the minted kind) rather
	// than "unknown item kind".
	_, err := w.Send(sim.SceneQuoteCreate("aldous", "moonshine", 1, 2, false, "", nil, time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "insufficient stock") {
		t.Fatalf("err = %v, want 'insufficient stock' (moonshine minted at qty 0)", err)
	}
}

func TestSceneQuoteCreate_Reject_OnBreak(t *testing.T) {
	future := time.Now().UTC().Add(15 * time.Minute)
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}, breakUntil: &future},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	_, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 2, false, "", nil, time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "on a break") {
		t.Fatalf("err = %v, want 'on a break'", err)
	}
}

func TestSceneQuoteCreate_Reject_InsufficientStock(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 1}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	_, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 3, 6, false, "", nil, time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "insufficient stock") {
		t.Fatalf("err = %v, want 'insufficient stock'", err)
	}
}

func TestSceneQuoteCreate_GroupOrderStockMultiplier(t *testing.T) {
	// 2 consumers, qty=2 per consumer = needs 4 stock. Has 3 → reject.
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
		{id: "cyrus", displayName: "Cyrus", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	_, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 2, 12, false, "", []string{"Bea", "Cyrus"}, time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "insufficient stock") {
		t.Fatalf("err = %v, want 'insufficient stock' (needed 4, has 3)", err)
	}
}

func TestSceneQuoteCreate_Reject_TargetBuyerMissing(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	_, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 2, false, "Nonexistent", nil, time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "no one named") {
		t.Fatalf("err = %v, want 'no one named'", err)
	}
}

func TestSceneQuoteCreate_Reject_TargetBuyerAmbiguous(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
		{id: "bea1", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
		{id: "bea2", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	_, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 2, false, "Bea", nil, time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("err = %v, want 'more than one'", err)
	}
}

func TestSceneQuoteCreate_Reject_SellerAsConsumer(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	_, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 2, false, "", []string{"Aldous"}, time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "seller can't be a consumer") {
		t.Fatalf("err = %v, want 'seller can't be a consumer'", err)
	}
}

func TestSceneQuoteCreate_Reject_TooManyConsumers(t *testing.T) {
	// 9 consumers > cap of 8.
	actors := []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 100}},
	}
	names := make([]string, 0, 9)
	for i := 0; i < 9; i++ {
		id := sim.ActorID("peer" + string(rune('1'+i)))
		actors = append(actors, quoteTestActor{
			id:          id,
			displayName: "Peer" + string(rune('1'+i)),
			kind:        sim.KindNPCStateful,
			huddleID:    "h1",
		})
		names = append(names, "Peer"+string(rune('1'+i)))
	}
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", actors)
	defer stop()

	_, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 9, false, "", names, time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "too many consumers") {
		t.Fatalf("err = %v, want 'too many consumers'", err)
	}
}

func TestSceneQuoteCreate_Reject_QtyZero(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	_, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 0, 2, false, "", nil, time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "qty must be at least 1") {
		t.Fatalf("err = %v, want qty validation", err)
	}
}

func TestSceneQuoteCreate_Reject_AmountZero(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	_, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 0, false, "", nil, time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "amount must be at least 1") {
		t.Fatalf("err = %v, want amount validation", err)
	}
}

// ---- Duplicate-key supersede + cap displacement ----

func TestSceneQuoteCreate_DuplicateKey_Supersedes(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	expired := captureSceneQuoteExpired(t, w)

	// First quote — 1 ale for 2 coins, eat-in, public.
	res1, _ := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 2, true, "", nil, time.Now().UTC()))
	q1 := res1.(sim.SceneQuoteCreateResult).QuoteID

	// Same non-Amount key, different price — must supersede.
	res2, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 3, true, "", nil, time.Now().UTC()))
	if err != nil {
		t.Fatalf("second SceneQuoteCreate: %v", err)
	}
	q2 := res2.(sim.SceneQuoteCreateResult).QuoteID

	if q2 == q1 {
		t.Fatalf("second quote QuoteID = %d, want different from first (%d)", q2, q1)
	}

	view := readLiveQuotes(t, w)
	// Both quotes still in World.Quotes (terminal-state quotes
	// stay; this is by design for admin replay).
	if view.Quotes[q1].State != sim.SceneQuoteStateSuperseded {
		t.Errorf("first quote state = %q, want superseded", view.Quotes[q1].State)
	}
	if view.Quotes[q2].State != sim.SceneQuoteStateActive {
		t.Errorf("second quote state = %q, want active", view.Quotes[q2].State)
	}
	// Scene index has only the new (active) quote.
	if ids := view.SceneIdx["sc1"]; len(ids) != 1 || ids[0] != q2 {
		t.Errorf("scene index = %v, want [%d]", ids, q2)
	}
	// Supersede event fired with the right reason.
	if len(*expired) != 1 {
		t.Fatalf("SceneQuoteExpired events = %d, want 1", len(*expired))
	}
	if (*expired)[0].Reason != sim.SceneQuoteExpiredReasonSuperseded {
		t.Errorf("Reason = %q, want %q", (*expired)[0].Reason, sim.SceneQuoteExpiredReasonSuperseded)
	}
	if (*expired)[0].QuoteID != q1 {
		t.Errorf("expired QuoteID = %d, want %d", (*expired)[0].QuoteID, q1)
	}
}

func TestSceneQuoteCreate_CapHit_DisplacesOldest(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 100, "bread": 100, "stew": 100}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	expired := captureSceneQuoteExpired(t, w)

	// Fill the cap with 10 distinct quotes (varying qty so non-Amount
	// key differs and no supersede fires).
	base := time.Now().UTC()
	var firstID sim.QuoteID
	for i := 1; i <= sim.SceneQuoteMaxPerSellerScene; i++ {
		// CreatedAt monotonic via base.Add(i) so the oldest is unambiguous.
		res, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", i, i, false, "", nil, base.Add(time.Duration(i)*time.Second)))
		if err != nil {
			t.Fatalf("seed quote %d: %v", i, err)
		}
		id := res.(sim.SceneQuoteCreateResult).QuoteID
		if i == 1 {
			firstID = id
		}
	}

	// 11th quote (different qty so no supersede) → triggers cap displacement.
	_, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", 11, 11, false, "", nil, base.Add(11*time.Second)))
	if err != nil {
		t.Fatalf("over-cap SceneQuoteCreate: %v", err)
	}

	view := readLiveQuotes(t, w)
	if got, want := view.Quotes[firstID].State, sim.SceneQuoteStateCapDisplaced; got != want {
		t.Errorf("first quote state = %q, want %q", got, want)
	}
	// Active quotes in scene index = exactly cap (10 carried over + 1 new − 1 displaced).
	if got := len(view.SceneIdx["sc1"]); got != sim.SceneQuoteMaxPerSellerScene {
		t.Errorf("scene index active count = %d, want %d", got, sim.SceneQuoteMaxPerSellerScene)
	}
	// Cap-displaced event fired with the right reason.
	if len(*expired) != 1 || (*expired)[0].Reason != sim.SceneQuoteExpiredReasonCapDisplaced || (*expired)[0].QuoteID != firstID {
		t.Fatalf("expired events = %+v, want one cap_displaced for QuoteID=%d", *expired, firstID)
	}
}

// ---- ID sequence ----

func TestSceneQuoteCreate_MintsSequentialIDs(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 10}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	at := time.Now().UTC()
	// Three quotes with distinct qty so no supersede.
	for i := 1; i <= 3; i++ {
		res, err := w.Send(sim.SceneQuoteCreate("aldous", "ale", i, i*2, false, "", nil, at))
		if err != nil {
			t.Fatalf("SceneQuoteCreate %d: %v", i, err)
		}
		if got, want := res.(sim.SceneQuoteCreateResult).QuoteID, sim.QuoteID(i); got != want {
			t.Errorf("QuoteID #%d = %d, want %d", i, got, want)
		}
	}
}

// ---- Snapshot isolation ----

func TestSceneQuote_Snapshot_Isolation(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 5}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
		{id: "cyrus", displayName: "Cyrus", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	res, _ := w.Send(sim.SceneQuoteCreate("aldous", "ale", 1, 2, false, "", []string{"Bea", "Cyrus"}, time.Now().UTC()))
	qid := res.(sim.SceneQuoteCreateResult).QuoteID

	snap1 := w.Published()
	q1 := snap1.Quotes[qid]
	if q1 == nil {
		t.Fatalf("snap1.Quotes missing %d", qid)
	}
	// Mutate snap1's ConsumerIDs — must not affect snap2 or world.
	q1.ConsumerIDs[0] = "bogus"

	// Force a republish.
	if _, err := w.Send(sim.Command{Fn: func(_ *sim.World) (any, error) { return nil, nil }}); err != nil {
		t.Fatalf("nop send: %v", err)
	}
	snap2 := w.Published()
	q2 := snap2.Quotes[qid]
	if q2.ConsumerIDs[0] == "bogus" {
		t.Error("snap2 inherited mutation from snap1 — snapshot isolation broken")
	}

	view := readLiveQuotes(t, w)
	if view.Quotes[qid].ConsumerIDs[0] == "bogus" {
		t.Error("world inherited mutation from snap1 — snapshot isolation broken")
	}
}

// ---- Restart helpers ----

func TestRestartExpireScannedQuotes(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	// Inject a quote with ExpiresAt already past, mimicking a load
	// from a future repo that crossed a restart boundary.
	now := time.Now().UTC()
	staleAt := now.Add(-1 * time.Hour)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Quotes[42] = &sim.SceneQuote{
			ID:        42,
			SceneID:   "sc1",
			SellerID:  "aldous",
			ItemKind:  "ale",
			Qty:       1,
			Amount:    2,
			State:     sim.SceneQuoteStateActive,
			CreatedAt: staleAt.Add(-1 * time.Hour),
			ExpiresAt: staleAt,
		}
		sim.RestartExpireScannedQuotesForTest(world, now)
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed + restart helper: %v", err)
	}

	view := readLiveQuotes(t, w)
	q, ok := view.Quotes[42]
	if !ok {
		t.Fatalf("quote 42 missing")
	}
	if q.State != sim.SceneQuoteStateExpired {
		t.Errorf("state = %q, want expired", q.State)
	}
	if !q.ResolvedAt.Equal(now) {
		t.Errorf("ResolvedAt = %v, want %v", q.ResolvedAt, now)
	}
}

func TestRebuildSceneQuoteIndex(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	// Inject quotes + corrupt the scene index.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		now := time.Now().UTC()
		world.Quotes[10] = &sim.SceneQuote{
			ID:        10,
			SceneID:   "sc1",
			SellerID:  "aldous",
			ItemKind:  "ale",
			Qty:       1,
			Amount:    2,
			State:     sim.SceneQuoteStateActive,
			CreatedAt: now,
			ExpiresAt: now.Add(time.Hour),
		}
		world.Quotes[11] = &sim.SceneQuote{
			ID:        11,
			SceneID:   "sc1",
			SellerID:  "aldous",
			ItemKind:  "ale",
			Qty:       2,
			Amount:    4,
			State:     sim.SceneQuoteStateActive,
			CreatedAt: now,
			ExpiresAt: now.Add(time.Hour),
		}
		world.Quotes[12] = &sim.SceneQuote{
			ID:         12,
			SceneID:    "sc1",
			SellerID:   "aldous",
			State:      sim.SceneQuoteStateExpired, // terminal — must NOT be indexed.
			ResolvedAt: now,
		}
		// Pre-rebuild: deliberately corrupt index with a bogus entry
		// + missing entries.
		world.Scenes["sc1"].QuoteIDs = []sim.QuoteID{999}
		sim.RebuildSceneQuoteIndexForTest(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed + rebuild: %v", err)
	}

	view := readLiveQuotes(t, w)
	ids := view.SceneIdx["sc1"]
	if len(ids) != 2 {
		t.Fatalf("scene index = %v, want 2 active entries", ids)
	}
	have := map[sim.QuoteID]bool{ids[0]: true, ids[1]: true}
	if !have[10] || !have[11] {
		t.Errorf("scene index = %v, want {10, 11}", ids)
	}
	if have[12] {
		t.Error("terminal quote 12 leaked into scene index")
	}
	if have[999] {
		t.Error("bogus pre-rebuild entry 999 not cleared")
	}
}

// TestSceneQuoteCreate_ServiceItem_SkipsStockGate — a "service"-capability item
// (nights_stay) carries no inventory, so scene_quote must let a keeper post a
// room quote despite 0 stock — the lodging-booking front door (ZBBS-WORK-382),
// mirroring the deliver_order / pay_with_item service stock-skip. A non-service
// item at 0 stock still rejects, so the bypass stays scoped to services.
func TestSceneQuoteCreate_ServiceItem_SkipsStockGate(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "hannah", displayName: "Hannah", kind: sim.KindNPCStateful, huddleID: "h1"},
		{id: "ezekiel", displayName: "Ezekiel", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	// nights_stay is a service+lodging item nobody stocks (minted on transfer).
	w.ItemKinds["nights_stay"] = &sim.ItemKindDef{
		Name:         "nights_stay",
		DisplayLabel: "a night's stay",
		Capabilities: []string{"service", "lodging"},
	}
	at := time.Now().UTC()

	// Hannah holds 0 nights_stay; the service bypass still lets her quote a room.
	res, err := w.Send(sim.SceneQuoteCreate("hannah", "nights_stay", 2, 8, false, "", nil, at))
	if err != nil {
		t.Fatalf("service-item quote rejected (stock bypass failed): %v", err)
	}
	if _, ok := res.(sim.SceneQuoteCreateResult); !ok {
		t.Fatalf("result type = %T, want SceneQuoteCreateResult", res)
	}

	// Control: a non-service item at 0 stock still hits the stock gate.
	_, err = w.Send(sim.SceneQuoteCreate("hannah", "ale", 1, 2, false, "", nil, at))
	if err == nil || !strings.Contains(err.Error(), "insufficient stock") {
		t.Fatalf("non-service item at 0 stock must reject with insufficient stock, got %v", err)
	}
}
