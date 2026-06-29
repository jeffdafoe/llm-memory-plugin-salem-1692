package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// labor_trade_steer_test.go — LLM-167. An NPC that tries to transact labor
// through the goods-trade tools (offer_trade / pay_with_item / sell /
// scene_quote) by naming "work"/"labor" as an item kind gets steered to the
// first-class labor verbs instead of a dead-end "unknown item kind" (buy side)
// or a phantom-mint-then-shortfall (pay_items / quote side). offer_trade lowers
// onto PayWithItem (its want_item → the bought Item; its give → PayItems), so
// the PayWithItem-level coverage here exercises offer_trade's two facets too.

// assertLaborSteer fails unless err is the labor steer — it must name the real
// labor verbs and must NOT be the generic unknown-item / mint-shortfall copy.
func assertLaborSteer(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("want labor steer error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"solicit_work", "accept_work", "decline_work"} {
		if !strings.Contains(msg, want) {
			t.Errorf("steer error %q missing %q", msg, want)
		}
	}
	if strings.Contains(msg, "unknown item kind") {
		t.Errorf("steer error should not be the generic unknown-item copy: %q", msg)
	}
}

// TestPayWithItem_BuySide_LaborTokenSteers — naming "work" as the good you want
// (the offer_trade want_item facet / pay_with_item item) hits the non-minting
// buy-side resolution and steers to the labor verbs.
func TestPayWithItem_BuySide_LaborTokenSteers(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 5},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	// Alice tries to "buy" work from Bob, paying coins.
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "work", 1, 5, false, nil, nil, 0, 0, "", time.Now().UTC()))
	assertLaborSteer(t, err)
}

// TestPayWithItem_PayItems_LaborTokenSteers — offering "labor" as the payment
// (the pay_with_item pay_items facet / offer_trade give) is intercepted BEFORE
// the discovery mint, so no phantom "labor" kind is minted and the model is
// pointed at the labor verbs.
func TestPayWithItem_PayItems_LaborTokenSteers(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 0},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()
	// Alice tries to pay for Bob's stew with her "labor".
	_, err := w.Send(sim.PayWithItem("alice", "Bob", "stew", 1, 0, false, nil,
		[]sim.PayItemInput{{Item: "labor", Qty: 3}}, 0, 0, "", time.Now().UTC()))
	assertLaborSteer(t, err)
}

// TestSceneQuoteCreate_LaborTokenSteers — quoting "work" as a wares line (the
// sell / scene_quote facet) is intercepted before the discovery mint and steered
// to the labor verbs.
func TestSceneQuoteCreate_LaborTokenSteers(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()
	_, err := w.Send(sim.SceneQuoteCreate("aldous", []sim.QuoteLineInput{{ItemName: "work", Qty: 1}}, 2, false, "", nil, time.Now().UTC()))
	assertLaborSteer(t, err)
}
