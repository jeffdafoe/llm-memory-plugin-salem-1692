package handlers_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// pay_with_item_reactor_test.go — Phase 3 PR S4 step 7 coverage of the
// three subscribers wired by RegisterPayWithItemHandlers, driven via
// real sim.PayWithItem / sim.AcceptPay / sim.DeclinePay /
// sim.CounterPay / sim.WithdrawPay commands so the events emit
// through the production cascade path.
//
// LoadWorld restart re-stamp + DedupDiscriminator interlock is tested
// at the sim-package level in sim/restart_restamp_test.go where the
// export_test.go hook surfaces the restartReStampPayOfferWarrants
// helper.
//
// Index note: Actor.CurrentHuddleID is set during the Actors.Seed
// call, so LoadWorld's rebuildIndices pass populates
// actorsByHuddle["h1"] before this file's tests run. The Huddle + Scene
// structs are seeded post-LoadWorld via raw map writes (consistent with
// pay_commands_test.go's pattern); rebuildIndices doesn't depend on
// those structs existing, so no second rebuild is needed.

type reactorActor struct {
	id          sim.ActorID
	displayName string
	kind        sim.ActorKind
	huddleID    sim.HuddleID
	coins       int
	inventory   map[sim.ItemKind]int
}

func buildReactorWorld(t *testing.T, actors []reactorActor) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())
	now := time.Now().UTC()
	seed := make(map[sim.ActorID]*sim.Actor, len(actors))
	members := make(map[sim.ActorID]struct{}, len(actors))
	for _, a := range actors {
		seed[a.id] = &sim.Actor{
			ID:              a.id,
			DisplayName:     a.displayName,
			Kind:            a.kind,
			State:           sim.StateIdle,
			Coins:           a.coins,
			Inventory:       a.inventory,
			CurrentHuddleID: a.huddleID,
			RecentActions:   sim.NewRingBuffer[sim.Action](4),
		}
		if a.huddleID == "h1" {
			members[a.id] = struct{}{}
		}
	}
	handles.Actors.Seed(seed)
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
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Huddles["h1"] = &sim.Huddle{ID: "h1", Members: members, StartedAt: now}
		world.Scenes["sc1"] = &sim.Scene{
			ID: "sc1", OriginAt: now, Bound: sim.NewUnboundedBound(),
			Huddles: map[sim.HuddleID]struct{}{"h1": {}},
		}
		handlers.RegisterPayWithItemHandlers(world)
		return nil, nil
	}}); err != nil {
		cancel()
		<-done
		t.Fatalf("setup: %v", err)
	}
	return w, func() { cancel(); <-done }
}

func readWarrants(t *testing.T, w *sim.World, id sim.ActorID) []sim.WarrantMeta {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		actor, ok := world.Actors[id]
		if !ok || actor == nil {
			return nil, nil
		}
		out := make([]sim.WarrantMeta, len(actor.Warrants))
		copy(out, actor.Warrants)
		return out, nil
	}})
	if err != nil {
		t.Fatalf("readWarrants: %v", err)
	}
	if res == nil {
		return nil
	}
	return res.([]sim.WarrantMeta)
}

// firstByKind returns the first warrant of the given kind, or empty
// meta + false. Used to assert on a specific subscriber's stamp when
// the actor might be carrying other warrants from earlier cascade
// steps.
func firstByKind(warrants []sim.WarrantMeta, kind sim.WarrantKind) (sim.WarrantMeta, bool) {
	for _, m := range warrants {
		if m.Kind() == kind {
			return m, true
		}
	}
	return sim.WarrantMeta{}, false
}

// ====================================================================
// PayOfferReceived subscriber
// ====================================================================

func TestSubscriber_PayOfferReceived_StampsSeller(t *testing.T) {
	w, stop := buildReactorWorld(t, []reactorActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err != nil {
		t.Fatalf("PayWithItem: %v", err)
	}
	result := res.(sim.PayWithItemResult)

	warrants := readWarrants(t, w, "bob")
	if len(warrants) != 1 {
		t.Fatalf("bob warrants = %d, want 1", len(warrants))
	}
	reason, ok := warrants[0].Reason.(sim.PayOfferWarrantReason)
	if !ok {
		t.Fatalf("Reason type = %T, want PayOfferWarrantReason", warrants[0].Reason)
	}
	if reason.LedgerID != result.LedgerID || reason.Buyer != "alice" || reason.Item != "stew" {
		t.Errorf("payload = %+v", reason)
	}
	if reason.DedupDiscriminator() != uint64(result.LedgerID) {
		t.Errorf("DedupDiscriminator = %d, want %d", reason.DedupDiscriminator(), result.LedgerID)
	}
	if warrants[0].SourceEventID == 0 {
		t.Error("SourceEventID = 0 on normal-flow stamp; should be the PayOfferReceived event ID")
	}
	// Buyer side (alice) gets no warrant from PayOfferReceived — only
	// the seller's subscriber fires.
	if got := readWarrants(t, w, "alice"); len(got) != 0 {
		t.Errorf("buyer alice got warrant from PayOfferReceived: %+v", got)
	}
}

func TestSubscriber_PayOfferReceived_SkipsPCSeller(t *testing.T) {
	w, stop := buildReactorWorld(t, []reactorActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "pc-bob", displayName: "PC Bob", kind: sim.KindPC, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	if _, err := w.Send(sim.PayWithItem("alice", "PC Bob", "stew", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC())); err != nil {
		t.Fatalf("PayWithItem: %v", err)
	}
	if got := readWarrants(t, w, "pc-bob"); len(got) != 0 {
		t.Errorf("PC seller got warrant: %+v", got)
	}
}

// ====================================================================
// PayWithItemResolved subscriber
// ====================================================================

func TestSubscriber_PayWithItemResolved_StampsBuyerOnAccept(t *testing.T) {
	w, stop := buildReactorWorld(t, []reactorActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	if err != nil {
		t.Fatalf("PayWithItem: %v", err)
	}
	ledgerID := res.(sim.PayWithItemResult).LedgerID
	if _, err := w.Send(sim.AcceptPay("bob", ledgerID, time.Now().UTC())); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	warrants := readWarrants(t, w, "alice")
	meta, ok := firstByKind(warrants, sim.WarrantKindPayResolved)
	if !ok {
		t.Fatalf("no PayResolved warrant on alice; warrants = %+v", warrants)
	}
	reason := meta.Reason.(sim.PayResolvedWarrantReason)
	if reason.TerminalState != sim.PayTerminalStateAccepted || reason.LedgerID != ledgerID {
		t.Errorf("payload = %+v", reason)
	}
}

func TestSubscriber_PayWithItemResolved_StampsBuyerOnDecline(t *testing.T) {
	w, stop := buildReactorWorld(t, []reactorActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	res, _ := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	ledgerID := res.(sim.PayWithItemResult).LedgerID
	if _, err := w.Send(sim.DeclinePay("bob", ledgerID, "too low", time.Now().UTC())); err != nil {
		t.Fatalf("DeclinePay: %v", err)
	}
	warrants := readWarrants(t, w, "alice")
	meta, ok := firstByKind(warrants, sim.WarrantKindPayResolved)
	if !ok {
		t.Fatalf("no PayResolved warrant; warrants = %+v", warrants)
	}
	reason := meta.Reason.(sim.PayResolvedWarrantReason)
	if reason.TerminalState != sim.PayTerminalStateDeclined {
		t.Errorf("TerminalState = %q, want declined", reason.TerminalState)
	}
	if reason.Message != "too low" {
		t.Errorf("Message = %q, want %q", reason.Message, "too low")
	}
}

func TestSubscriber_PayWithItemResolved_SkipsWithdrawnByBuyer(t *testing.T) {
	w, stop := buildReactorWorld(t, []reactorActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	res, _ := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	ledgerID := res.(sim.PayWithItemResult).LedgerID
	if _, err := w.Send(sim.WithdrawPay("alice", ledgerID, "", time.Now().UTC())); err != nil {
		t.Fatalf("WithdrawPay: %v", err)
	}
	warrants := readWarrants(t, w, "alice")
	if _, ok := firstByKind(warrants, sim.WarrantKindPayResolved); ok {
		t.Errorf("buyer got PayResolved warrant on own withdraw")
	}
}

// ====================================================================
// PayCountered subscriber
// ====================================================================

func TestSubscriber_PayCountered_StampsBuyerWithCounterTerms(t *testing.T) {
	w, stop := buildReactorWorld(t, []reactorActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	res, _ := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 0, 0, "", time.Now().UTC()))
	ledgerID := res.(sim.PayWithItemResult).LedgerID
	if _, err := w.Send(sim.CounterPay("bob", ledgerID, 6, nil, "how about six", time.Now().UTC())); err != nil {
		t.Fatalf("CounterPay: %v", err)
	}
	warrants := readWarrants(t, w, "alice")
	meta, ok := firstByKind(warrants, sim.WarrantKindPayResolved)
	if !ok {
		t.Fatalf("no PayResolved warrant; warrants = %+v", warrants)
	}
	reason := meta.Reason.(sim.PayResolvedWarrantReason)
	if reason.TerminalState != sim.PayTerminalStateCountered {
		t.Errorf("TerminalState = %q, want countered", reason.TerminalState)
	}
	if reason.CounterAmount != 6 {
		t.Errorf("CounterAmount = %d, want 6", reason.CounterAmount)
	}
	if reason.Amount != 4 {
		t.Errorf("original Amount = %d, want 4", reason.Amount)
	}
	if reason.Message != "how about six" {
		t.Errorf("Message = %q", reason.Message)
	}
}

// ====================================================================
// ServeHandover subscriber (ZBBS-WORK-423) — seller-side wake on an
// instant quote-take so the keeper voices the handover.
// ====================================================================

// seedActiveQuote installs an active scene quote so a pay_with_item with a
// matching quote_id hits runPayWithItemFastPath (the instant take). Mirrors
// the sim-package seedQuote helper, inlined because that one is unexported in
// a different package.
func seedActiveQuote(t *testing.T, w *sim.World, q sim.SceneQuote) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		cp := q
		world.Quotes[q.ID] = &cp
		if scene := world.Scenes[q.SceneID]; scene != nil {
			scene.QuoteIDs = append(scene.QuoteIDs, q.ID)
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seedActiveQuote: %v", err)
	}
}

func TestSubscriber_ServeHandover_StampsSellerOnQuoteTake(t *testing.T) {
	w, stop := buildReactorWorld(t, []reactorActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	now := time.Now().UTC()
	seedActiveQuote(t, w, sim.SceneQuote{
		ID: 8, SceneID: "sc1", SellerID: "bob", ItemKind: "stew",
		Qty: 1, Amount: 4, ConsumeNow: true, State: sim.SceneQuoteStateActive,
		CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	})
	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, true, nil, nil, 8, 0, "", now))
	if err != nil {
		t.Fatalf("PayWithItem fast-path: %v", err)
	}
	result := res.(sim.PayWithItemResult)
	if !result.FastPath {
		t.Fatalf("expected a fast-path take, got %+v", result)
	}
	meta, ok := firstByKind(readWarrants(t, w, "bob"), sim.WarrantKindServeHandover)
	if !ok {
		t.Fatalf("no ServeHandover warrant on seller bob; warrants = %+v", readWarrants(t, w, "bob"))
	}
	reason := meta.Reason.(sim.ServeHandoverWarrantReason)
	if reason.Buyer != "alice" || reason.ItemKind != "stew" || reason.Qty != 1 || reason.Amount != 4 {
		t.Errorf("payload = %+v", reason)
	}
	if !reason.ConsumeNow {
		t.Error("ConsumeNow = false, want true (eat-here take)")
	}
	if reason.LedgerID != result.LedgerID {
		t.Errorf("LedgerID = %d, want %d", reason.LedgerID, result.LedgerID)
	}
	if reason.DedupDiscriminator() != uint64(meta.SourceEventID) {
		t.Errorf("DedupDiscriminator = %d, want SourceEventID %d", reason.DedupDiscriminator(), meta.SourceEventID)
	}
}

// The headline case + the reason this is a separate subscriber: a PC takes the
// keeper's quote. The buyer subscriber returns early for PC buyers, but the
// seller must STILL be warranted to acknowledge the customer.
func TestSubscriber_ServeHandover_StampsSellerWhenPCBuyerTakes(t *testing.T) {
	w, stop := buildReactorWorld(t, []reactorActor{
		{id: "pc-jeff", displayName: "Jefferey", kind: sim.KindPC, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	now := time.Now().UTC()
	seedActiveQuote(t, w, sim.SceneQuote{
		ID: 9, SceneID: "sc1", SellerID: "bob", ItemKind: "stew",
		Qty: 1, Amount: 4, ConsumeNow: true, State: sim.SceneQuoteStateActive,
		CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	})
	if _, err := w.Send(sim.PayWithItem("pc-jeff", "Bob", "stew", 1, 4, true, nil, nil, 9, 0, "", now)); err != nil {
		t.Fatalf("PayWithItem fast-path: %v", err)
	}
	if _, ok := firstByKind(readWarrants(t, w, "bob"), sim.WarrantKindServeHandover); !ok {
		t.Fatal("seller bob got no ServeHandover warrant on a PC quote-take")
	}
	if got := readWarrants(t, w, "pc-jeff"); len(got) != 0 {
		t.Errorf("PC buyer got warrants (buyer subscriber should skip PCs): %+v", got)
	}
}

// A slow-path accept must NOT serve-handover-warrant the seller: the seller
// ran accept_pay on its own tick and already had the floor to speak.
func TestSubscriber_ServeHandover_NotStampedOnSlowPathAccept(t *testing.T) {
	w, stop := buildReactorWorld(t, []reactorActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	now := time.Now().UTC()
	res, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, false, nil, nil, 0, 0, "", now))
	if err != nil {
		t.Fatalf("PayWithItem slow-path: %v", err)
	}
	ledgerID := res.(sim.PayWithItemResult).LedgerID
	if _, err := w.Send(sim.AcceptPay("bob", ledgerID, now)); err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	if meta, ok := firstByKind(readWarrants(t, w, "bob"), sim.WarrantKindServeHandover); ok {
		t.Errorf("seller got a ServeHandover warrant on a slow-path accept: %+v", meta)
	}
}

// Locks the dedup claim: with the handlers registered twice, every subscriber
// fires twice per event, so the serve-handover stamp would double up unless
// tryStampWarrant's (Kind, DedupDiscriminator) key collapses it. Exactly one
// warrant must survive.
func TestSubscriber_ServeHandover_DedupsOnDoubleRegistration(t *testing.T) {
	w, stop := buildReactorWorld(t, []reactorActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	now := time.Now().UTC()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handlers.RegisterPayWithItemHandlers(world) // second registration
		return nil, nil
	}}); err != nil {
		t.Fatalf("second RegisterPayWithItemHandlers: %v", err)
	}
	seedActiveQuote(t, w, sim.SceneQuote{
		ID: 10, SceneID: "sc1", SellerID: "bob", ItemKind: "stew",
		Qty: 1, Amount: 4, ConsumeNow: true, State: sim.SceneQuoteStateActive,
		CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	})
	if _, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 4, true, nil, nil, 10, 0, "", now)); err != nil {
		t.Fatalf("PayWithItem fast-path: %v", err)
	}
	count := 0
	for _, m := range readWarrants(t, w, "bob") {
		if m.Kind() == sim.WarrantKindServeHandover {
			count++
		}
	}
	if count != 1 {
		t.Errorf("ServeHandover warrant count = %d, want exactly 1 (dedup on double registration)", count)
	}
}

// ====================================================================
// RegisterPayWithItemHandlers
// ====================================================================

func TestRegisterPayWithItemHandlers_NilWorldPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("RegisterPayWithItemHandlers(nil) didn't panic")
		}
	}()
	handlers.RegisterPayWithItemHandlers(nil)
}
