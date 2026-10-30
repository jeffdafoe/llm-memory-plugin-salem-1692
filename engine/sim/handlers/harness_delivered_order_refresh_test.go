package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_delivered_order_refresh_test.go — LLM-887: a deliver_order drops the
// handed-over order from the within-tick re-perception. The turn-start snapshot
// still holds it as Ready, so before the fix the seller's next round showed the
// order as still owed — live, with the shovel now out of Ezekiel's pack, as
// "you've yet to make it" — and he named a price for a shovel already paid for.

const deliveredTestOrder sim.OrderID = 7

// seedAliceOrder gives alice one Ready order to hand over (a shovel for bob)
// and the shovel itself, on the world goroutine before the tick runs.
func seedAliceOrder(t *testing.T, w *sim.World) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["alice"].Inventory = map[sim.ItemKind]int{"shovel": 1}
		world.Orders[deliveredTestOrder] = &sim.Order{
			ID:          deliveredTestOrder,
			SellerID:    "alice",
			BuyerID:     "bob",
			Item:        "shovel",
			Qty:         1,
			Amount:      8,
			ConsumerIDs: []sim.ActorID{"bob"},
			State:       sim.OrderStateReady,
			CreatedAt:   time.Now().Add(-time.Hour),
			ExpiresAt:   time.Now().Add(12 * time.Hour),
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed order: %v", err)
	}
}

// registerFakeDeliver registers deliver_order under its real name and decoder,
// with a commit that removes the order from the world as DeliverOrder's prune
// does. moveGoods controls whether the shovel leaves alice's pack (a goods
// handover) or nothing of hers changes (a room handover).
func registerFakeDeliver(t *testing.T, r *Registry, moveGoods bool) {
	t.Helper()
	fn := func(in HandlerInput) (sim.Command, error) {
		id := sim.OrderID(in.Args.(DeliverOrderArgs).OrderID)
		return sim.Command{Fn: func(world *sim.World) (any, error) {
			if moveGoods {
				world.Actors[in.ActorID].Inventory["shovel"]--
			}
			delete(world.Orders, id)
			return sim.DeliverOrderResult{OrderID: id, BuyerName: "bob", Noun: "shovel", Total: 1, PaidAtAccept: 8}, nil
		}}, nil
	}
	if err := r.RegisterCommit(deliverOrderToolName, deliverOrderSchema, DecodeDeliverOrderArgs, fn, false); err != nil {
		t.Fatalf("register deliver_order: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
}

func runDeliverTick(t *testing.T, moveGoods bool) []llm.Request {
	t.Helper()
	r := NewRegistry()
	registerFakeDeliver(t, r, moveGoods)
	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, deliverOrderToolName, `{"order_id": 7}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "done", `{}`)}}},
	)
	f := newIntegrationFixture(t, r, client)
	defer f.stop()

	seedAliceOrder(t, f.world)
	now := time.Now()
	seedDueWarrant(t, f.world, now)
	if _, err := f.world.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}
	if rec := f.waitForTerminalTelemetry(t); rec.Kind == "failed" || rec.Kind == "stale" {
		t.Fatalf("tick did not complete cleanly: kind=%q", rec.Kind)
	}
	reqs := client.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: got %d, want 2 (deliver round, done round)", len(reqs))
	}
	return reqs
}

// The live case: the goods leave the pack, so the self-state refresh fires — and
// must not render the handed-over order as still owed.
func TestHarness_DeliveredOrderLeavesOrderBook(t *testing.T) {
	reqs := runDeliverTick(t, true)
	if !strings.Contains(reqs[0].EphemeralContext, "#7: shovel for bob") {
		t.Fatalf("round 1 must show the order to deliver, got:\n%s", reqs[0].EphemeralContext)
	}
	if strings.Contains(reqs[1].EphemeralContext, "#7:") {
		t.Errorf("round 2 must not show the delivered order, got:\n%s", reqs[1].EphemeralContext)
	}
	if strings.Contains(reqs[1].EphemeralContext, "yet to make it") {
		t.Errorf("round 2 must not read the delivered order as unmade, got:\n%s", reqs[1].EphemeralContext)
	}
}

// A room handover moves no goods or coins, so the self-state gate alone would
// skip the refresh. The delivery forces it.
func TestHarness_DeliveredOrderRefreshesWithoutSelfStateChange(t *testing.T) {
	reqs := runDeliverTick(t, false)
	if !strings.Contains(reqs[0].EphemeralContext, "#7: shovel for bob") {
		t.Fatalf("round 1 must show the order to deliver, got:\n%s", reqs[0].EphemeralContext)
	}
	if strings.Contains(reqs[1].EphemeralContext, "#7:") {
		t.Errorf("round 2 must not show the delivered order, got:\n%s", reqs[1].EphemeralContext)
	}
}

func TestDeliverOrderReceipt(t *testing.T) {
	cases := []struct {
		name string
		r    sim.DeliverOrderResult
		want string
	}{
		{
			"prepaid single good",
			sim.DeliverOrderResult{OrderID: 13966, BuyerName: "Elizabeth Ellis", Noun: "shovel", Total: 1, PaidAtAccept: 8},
			"[ok] You hand Elizabeth Ellis the shovel from order #13966. It was paid for when the order was made — 8 coins — so the deal is settled: nothing more is owed on it; do not ask for payment.",
		},
		{
			"several units",
			sim.DeliverOrderResult{OrderID: 4, BuyerName: "John Ellis", Noun: "bowls of stew", Total: 3, PaidAtAccept: 12},
			"[ok] You hand John Ellis the 3 bowls of stew from order #4. It was paid for when the order was made — 12 coins — so the deal is settled: nothing more is owed on it; do not ask for payment.",
		},
		{
			"deposit and balance",
			sim.DeliverOrderResult{OrderID: 1, BuyerName: "alice", Noun: "nail", Total: 1, PaidAtAccept: 5, BalancePaid: 10},
			"[ok] You hand alice the nail from order #1. 5 coins were paid when the order was made and the other 10 just now, so the deal is settled — nothing more is owed on it; do not ask for payment.",
		},
		{
			"room",
			sim.DeliverOrderResult{OrderID: 9, BuyerName: "Jefferey", Noun: "night's stay", Total: 1, PaidAtAccept: 6, Lodging: true},
			"[ok] Jefferey has the room from order #9 now. It was paid for when the order was made — 6 coins — so the deal is settled: nothing more is owed on it; do not ask for payment.",
		},
		{
			"no noun, nothing paid",
			sim.DeliverOrderResult{OrderID: 2, BuyerName: "bob", Total: 1},
			"[ok] You hand bob the goods from order #2. Nothing is owed on it; do not ask for payment.",
		},
	}
	for _, c := range cases {
		if got := deliverOrderReceipt(c.r); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
	// Wired through commitResultContent for the deliver_order tool only.
	vc := &ValidatedCall{Name: deliverOrderToolName}
	if got := commitResultContent(vc, cases[0].r); got != cases[0].want {
		t.Errorf("commitResultContent: got %q, want the receipt", got)
	}
}

func TestDeliveredOrderID(t *testing.T) {
	if id, ok := deliveredOrderID(&ValidatedCall{Name: deliverOrderToolName, DecodedArgs: DeliverOrderArgs{OrderID: 42}}); !ok || id != 42 {
		t.Errorf("deliver_order: got (%d, %v), want (42, true)", id, ok)
	}
	if _, ok := deliveredOrderID(&ValidatedCall{Name: "accept_pay", DecodedArgs: DeliverOrderArgs{OrderID: 42}}); ok {
		t.Error("other tool name with the same args: want false")
	}
	if _, ok := deliveredOrderID(&ValidatedCall{Name: deliverOrderToolName, DecodedArgs: json.RawMessage(`{}`)}); ok {
		t.Error("wrong decoded-arg shape: want false")
	}
	if _, ok := deliveredOrderID(nil); ok {
		t.Error("nil call: want false")
	}
}
