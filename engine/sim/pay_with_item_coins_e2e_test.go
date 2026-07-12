package sim_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
)

// pay_with_item_coins_e2e_test.go — LLM-377 lifecycle-boundary coverage.
//
// The decode-tolerance unit tests (handlers/lenient_args_test.go) prove the
// weak-model coin shape parses; this proves the whole buy COMPLETES from that
// raw arg — decode → handler → world stakes a real pending offer → the seller
// accepts and coins move. It drives the exact shape that pinned Prudence Ward
// at Ezekiel's blacksmith: the price named as `coins` (not `amount`), with the
// scalars and the boolean sent as strings. Without the fix this raw arg never
// decodes, so the offer is never placed and no seller can accept it.
func TestPayWithItem_CoinsShape_EndToEnd(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "alice", displayName: "Alice", kind: sim.KindNPCShared, huddleID: "h1", coins: 10},
		{id: "bob", displayName: "Bob", kind: sim.KindNPCShared, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 5}},
	})
	defer stop()

	events := capturePayWithItemEvents(t, w)

	// The weak model's shape: `coins` instead of `amount`, qty/coins as strings,
	// consume_now as the string "false".
	raw := json.RawMessage(`{"seller":"Bob","item":"stew","qty":"1","coins":"4","consume_now":"false"}`)
	decoded, err := handlers.DecodePayWithItemArgs(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	cmd, err := handlers.HandlePayWithItem(handlers.HandlerInput{ActorID: "alice", AttemptID: "tk-e2e", Args: decoded})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}

	res, err := w.Send(cmd)
	if err != nil {
		t.Fatalf("send offer: %v", err)
	}
	result, ok := res.(sim.PayWithItemResult)
	if !ok {
		t.Fatalf("result type = %T, want PayWithItemResult", res)
	}
	if result.State != sim.PayLedgerStatePending {
		t.Fatalf("offer State = %q, want pending", result.State)
	}
	if len(events.Offer) != 1 || events.Offer[0].Amount != 4 || events.Offer[0].ItemKind != "stew" ||
		events.Offer[0].BuyerID != "alice" || events.Offer[0].SellerID != "bob" {
		t.Fatalf("PayOfferReceived = %+v, want one stew offer for 4 coins alice->bob", events.Offer)
	}

	// The seller can accept it — coins move, proving the coins-named buy is a
	// real, settleable offer, not just a struct that decoded.
	at := time.Now().UTC()
	if _, err := w.Send(sim.AcceptPay("bob", result.LedgerID, at)); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if got := readPayLedger(t, w)[result.LedgerID].State; got != sim.PayLedgerStateAccepted {
		t.Fatalf("ledger state after accept = %q, want accepted", got)
	}
	snap := w.Published()
	if snap.Actors["alice"].Coins != 6 || snap.Actors["bob"].Coins != 4 {
		t.Errorf("coins after settle: alice=%d bob=%d, want alice=6 bob=4",
			snap.Actors["alice"].Coins, snap.Actors["bob"].Coins)
	}
}
