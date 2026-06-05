package handlers_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// pay_with_item_arrival_huddle_test.go — ZBBS-HOME-400.
//
// A buyer who walks up to a seller's stall and offers on arrival — with no
// prior speak, and so no huddle — used to hit the transaction gate
// ("you're not in a conversation — start one with the person … first"), then
// in practice wander off before it could speak-then-pay (the live Josiah
// restock-thrash). HandlePayWithItem now runs EnsureColocatedHuddle first
// (withHuddleBootstrap), forming the co-located structure huddle on the offer
// itself, so the offer lands in one action.
//
// The no-op cases (alone / outdoors / out of stall scope) still reject,
// preserving the invariant that you can't transact with an absent counterparty.

// buildArrivalWorld seeds a "farm" structure (+ its shared-identity vobj/asset
// so a structure scene can resolve), the item catalog, and a buyer + seller
// co-located INSIDE the farm with NO huddle.
func buildArrivalWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.ItemKinds.Seed(mem.SeedItemKinds())
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"farm": {ID: "farm", DisplayName: "Ellis Farm"},
	})
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"bldg-asset": {ID: "bldg-asset", Category: "structure"},
	})
	// Shared-Identity Bridge (ZBBS-WORK-342): the structure needs a backing
	// VillageObject so the structure-bound scene's origin tile resolves.
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"farm": {ID: "farm", AssetID: "bldg-asset", Pos: sim.WorldPos{X: 160, Y: 160}},
	})
	now := time.Now().UTC()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"buyer": {
			ID: "buyer", DisplayName: "Josiah", Kind: sim.KindNPCStateful,
			State: sim.StateIdle, StateEnteredAt: now,
			Coins:             50,
			InsideStructureID: "farm", // co-located with the seller, NO huddle
			RecentActions:     sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans:  sim.NewRingBuffer[sim.StateTransition](4),
		},
		"seller": {
			ID: "seller", DisplayName: "Elizabeth", Kind: sim.KindNPCShared,
			State: sim.StateIdle, StateEnteredAt: now,
			Inventory:         map[sim.ItemKind]int{"stew": 5},
			InsideStructureID: "farm",
			RecentActions:     sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans:  sim.NewRingBuffer[sim.StateTransition](4),
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handlers.RegisterPayWithItemHandlers(world)
		return nil, nil
	}}); err != nil {
		cancel()
		<-done
		t.Fatalf("setup: %v", err)
	}
	return w, func() { cancel(); <-done }
}

// TestArrival_PayWithItemFormsHuddleOnOffer is the fix: buyer + seller
// co-located inside the farm, neither huddled. The buyer offers on arrival; the
// offer lands in the ledger as Pending (no "not in a conversation" reject), and
// the two are now co-huddled — the huddle was formed by the offer itself.
func TestArrival_PayWithItemFormsHuddleOnOffer(t *testing.T) {
	w, stop := buildArrivalWorld(t)
	defer stop()

	cmd, err := handlers.HandlePayWithItem(handlers.HandlerInput{
		ActorID: "buyer", AttemptID: "tk-1",
		Args: handlers.PayWithItemArgs{
			Seller: "Elizabeth", Item: "stew", Qty: 1, Amount: 4, ConsumeNow: false,
		},
	})
	if err != nil {
		t.Fatalf("HandlePayWithItem: %v", err)
	}
	res, err := w.Send(cmd)
	if err != nil {
		t.Fatalf("offer-on-arrival rejected (huddle was not bootstrapped): %v", err)
	}
	if got := res.(sim.PayWithItemResult).State; got != sim.PayLedgerStatePending {
		t.Errorf("offer State = %q, want pending", got)
	}
	snap := w.Published()
	bh := snap.Actors["buyer"].CurrentHuddleID
	sh := snap.Actors["seller"].CurrentHuddleID
	if bh == "" || bh != sh {
		t.Errorf("buyer/seller not co-huddled after offer: buyer=%q seller=%q", bh, sh)
	}
}

// TestArrival_PayWithItemStillRejectsWhenAbsent is the invariant: with the
// buyer OUTDOORS and far from any stall loiter pin, EnsureColocatedHuddle is a
// no-op, so the transaction gate still rejects — you can't offer to an absent
// counterparty just by calling the tool.
func TestArrival_PayWithItemStillRejectsWhenAbsent(t *testing.T) {
	w, stop := buildArrivalWorld(t)
	defer stop()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["buyer"]
		a.InsideStructureID = "" // outdoors
		a.Pos = sim.WorldPos{X: 9000, Y: 9000}.Tile()
		return nil, nil
	}}); err != nil {
		t.Fatalf("move buyer outdoors: %v", err)
	}

	cmd, err := handlers.HandlePayWithItem(handlers.HandlerInput{
		ActorID: "buyer", AttemptID: "tk-1",
		Args: handlers.PayWithItemArgs{
			Seller: "Elizabeth", Item: "stew", Qty: 1, Amount: 4, ConsumeNow: false,
		},
	})
	if err != nil {
		t.Fatalf("HandlePayWithItem: %v", err)
	}
	if _, err := w.Send(cmd); err == nil || !strings.Contains(err.Error(), "not in a conversation") {
		t.Fatalf("want 'not in a conversation' reject for absent buyer, got %v", err)
	}
	// Invariant asserted directly (not just via the gate message): a rejected
	// absent offer forms no huddle on either party.
	snap := w.Published()
	if snap.Actors["buyer"].CurrentHuddleID != "" || snap.Actors["seller"].CurrentHuddleID != "" {
		t.Fatalf("unexpected huddle after absent offer: buyer=%q seller=%q",
			snap.Actors["buyer"].CurrentHuddleID, snap.Actors["seller"].CurrentHuddleID)
	}
}

// TestArrival_PayFormsHuddleOnPay pins that HandlePay (coin transfer) also
// routes through withHuddleBootstrap: a buyer paying a co-located recipient with
// no prior huddle succeeds and the two end co-huddled.
func TestArrival_PayFormsHuddleOnPay(t *testing.T) {
	w, stop := buildArrivalWorld(t)
	defer stop()

	cmd, err := handlers.HandlePay(handlers.HandlerInput{
		ActorID: "buyer", AttemptID: "tk-1",
		Args: handlers.PayArgs{Recipient: "Elizabeth", Amount: 4},
	})
	if err != nil {
		t.Fatalf("HandlePay: %v", err)
	}
	if _, err := w.Send(cmd); err != nil {
		t.Fatalf("pay-on-arrival rejected (huddle was not bootstrapped): %v", err)
	}
	snap := w.Published()
	bh := snap.Actors["buyer"].CurrentHuddleID
	sh := snap.Actors["seller"].CurrentHuddleID
	if bh == "" || bh != sh {
		t.Errorf("buyer/seller not co-huddled after pay: buyer=%q seller=%q", bh, sh)
	}
}

// TestArrival_SceneQuoteFormsHuddleOnQuote pins that HandleSceneQuote
// (seller-side quote) also routes through withHuddleBootstrap: a seller posting
// a quote with a co-located customer and no prior huddle succeeds and the two
// end co-huddled.
func TestArrival_SceneQuoteFormsHuddleOnQuote(t *testing.T) {
	w, stop := buildArrivalWorld(t)
	defer stop()

	cmd, err := handlers.HandleSceneQuote(handlers.HandlerInput{
		ActorID: "seller", AttemptID: "tk-1",
		Args: handlers.SceneQuoteArgs{ItemKind: "stew", Qty: 1, Amount: 4, ConsumeNow: false},
	})
	if err != nil {
		t.Fatalf("HandleSceneQuote: %v", err)
	}
	if _, err := w.Send(cmd); err != nil {
		t.Fatalf("quote-on-arrival rejected (huddle was not bootstrapped): %v", err)
	}
	snap := w.Published()
	sh := snap.Actors["seller"].CurrentHuddleID
	bh := snap.Actors["buyer"].CurrentHuddleID
	if sh == "" || sh != bh {
		t.Errorf("seller/buyer not co-huddled after quote: seller=%q buyer=%q", sh, bh)
	}
}
