package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// offer_trade_handlers_test.go — ZBBS-HOME-407. Coverage of
// DecodeOfferTradeArgs's static validation + its lowering onto a
// PayWithItemArgs, plus the two harness guards (same-tick dedup +
// post-offer steer) that offer_trade inherits by reusing that shape.
//
// World-state validation (huddle, ledger, gates, the two-way swap) is the
// EXISTING pay_with_item flow — covered in pay_with_item_commands_test.go /
// pay_with_item_barter_test.go — and offer_trade lowers onto the identical
// command, so there's nothing new to test there.

// TestDecodeOfferTrade_Valid — a mixed coins+goods proposal lowers onto the
// buyer-centric PayWithItemArgs with the right field mapping.
func TestDecodeOfferTrade_Valid(t *testing.T) {
	raw := json.RawMessage(`{
        "with":"Josiah Thorne",
        "give":[{"item":"milk","qty":5},{"item":"cheese","qty":2}],
        "coins":3,
        "want_item":"bread","want_qty":5,
        "for":"a fair swap"
    }`)
	decoded, err := DecodeOfferTradeArgs(raw)
	if err != nil {
		t.Fatalf("DecodeOfferTradeArgs: %v", err)
	}
	got, ok := decoded.(PayWithItemArgs)
	if !ok {
		t.Fatalf("decoded type = %T, want PayWithItemArgs (lowering must produce the buyer-centric shape so the harness guards apply)", decoded)
	}
	if got.Seller != "Josiah Thorne" {
		t.Errorf("Seller = %q, want %q (with → seller)", got.Seller, "Josiah Thorne")
	}
	if got.Item != "bread" || got.Qty != 5 {
		t.Errorf("Item/Qty = %q/%d, want bread/5 (want_item/want_qty → item/qty)", got.Item, got.Qty)
	}
	if got.Amount != 3 {
		t.Errorf("Amount = %d, want 3 (coins → amount)", got.Amount)
	}
	if got.ConsumeNow {
		t.Error("ConsumeNow = true, want false (offer_trade is a handover, never eat-here)")
	}
	if got.QuoteID != 0 || got.InResponseTo != 0 || got.ReadyInDays != 0 || len(got.Consumers) != 0 {
		t.Errorf("unused buyer fields not zero: %+v", got)
	}
	if len(got.PayItems) != 2 ||
		got.PayItems[0].Item != "milk" || got.PayItems[0].Qty != 5 ||
		got.PayItems[1].Item != "cheese" || got.PayItems[1].Qty != 2 {
		t.Errorf("PayItems = %+v, want [{milk 5} {cheese 2}] (give → pay_items)", got.PayItems)
	}
	if got.For != "a fair swap" {
		t.Errorf("For = %q, want %q", got.For, "a fair swap")
	}
}

// TestDecodeOfferTrade_GoodsOnly — a pure goods-for-goods swap (no coins)
// decodes with Amount 0 and passes the must-give-something rule on the
// strength of the give lines.
func TestDecodeOfferTrade_GoodsOnly(t *testing.T) {
	decoded, err := DecodeOfferTradeArgs(json.RawMessage(`{
        "with":"Josiah Thorne","give":[{"item":"milk","qty":5}],
        "want_item":"bread","want_qty":5
    }`))
	if err != nil {
		t.Fatalf("goods-only decode: %v", err)
	}
	got := decoded.(PayWithItemArgs)
	if got.Amount != 0 {
		t.Errorf("Amount = %d, want 0 (coins omitted)", got.Amount)
	}
	if len(got.PayItems) != 1 || got.PayItems[0].Item != "milk" || got.PayItems[0].Qty != 5 {
		t.Errorf("PayItems = %+v, want [{milk 5}]", got.PayItems)
	}
}

// TestDecodeOfferTrade_CoinsOnly — coins with no give lines is still a valid
// trade (coins for their goods); it lowers exactly like a coin pay_with_item.
func TestDecodeOfferTrade_CoinsOnly(t *testing.T) {
	decoded, err := DecodeOfferTradeArgs(json.RawMessage(`{
        "with":"Josiah Thorne","coins":12,"want_item":"bread","want_qty":3
    }`))
	if err != nil {
		t.Fatalf("coins-only decode: %v", err)
	}
	got := decoded.(PayWithItemArgs)
	if got.Amount != 12 || len(got.PayItems) != 0 {
		t.Errorf("got Amount=%d PayItems=%+v, want 12 / none", got.Amount, got.PayItems)
	}
}

func TestDecodeOfferTrade_RejectsShapeErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"null", `null`, "must be a JSON object"},
		{"array", `[]`, "must be a JSON object"},
		{"string", `"oops"`, "must be a JSON object"},
		{"unknown_field", `{"with":"A","want_item":"bread","want_qty":1,"coins":1,"frobnicate":true}`, "malformed arguments"},
		{"trailing_data", `{"with":"A","want_item":"bread","want_qty":1,"coins":1}{"junk":true}`, "trailing data"},
		{"missing_with", `{"want_item":"bread","want_qty":1,"coins":1}`, "with is required"},
		{"missing_want_item", `{"with":"A","want_qty":1,"coins":1}`, "want_item is required"},
		{"zero_want_qty", `{"with":"A","want_item":"bread","want_qty":0,"coins":1}`, "want_qty must be at least 1"},
		{"negative_want_qty", `{"with":"A","want_item":"bread","want_qty":-1,"coins":1}`, "want_qty must be at least 1"},
		{"nothing_offered", `{"with":"A","want_item":"bread","want_qty":1}`, "trade must give goods or coins"},
		{"nothing_offered_zero_coins", `{"with":"A","want_item":"bread","want_qty":1,"coins":0}`, "trade must give goods or coins"},
		{"negative_coins", `{"with":"A","want_item":"bread","want_qty":1,"coins":-5}`, "coins cannot be negative"},
		{"over_max_coins", `{"with":"A","want_item":"bread","want_qty":1,"coins":2147483648}`, "coins exceeds maximum"},
		{"fractional_coins", `{"with":"A","want_item":"bread","want_qty":1,"coins":3.5}`, "malformed arguments"},
		{"with_over_cap", `{"with":"` + strings.Repeat("a", 101) + `","want_item":"bread","want_qty":1,"coins":1}`, "with exceeds"},
		{"want_item_over_cap", `{"with":"A","want_item":"` + strings.Repeat("a", 65) + `","want_qty":1,"coins":1}`, "want_item exceeds"},
		{"for_over_cap", `{"with":"A","want_item":"bread","want_qty":1,"coins":1,"for":"` + strings.Repeat("a", 201) + `"}`, "'for' text exceeds"},
		{"give_zero_qty", `{"with":"A","want_item":"bread","want_qty":1,"give":[{"item":"milk","qty":0}]}`, "give[0].qty must be at least 1"},
		{"give_too_many", `{"with":"A","want_item":"bread","want_qty":1,"give":[{"item":"a","qty":1},{"item":"b","qty":1},{"item":"c","qty":1},{"item":"d","qty":1},{"item":"e","qty":1},{"item":"f","qty":1},{"item":"g","qty":1},{"item":"h","qty":1},{"item":"i","qty":1}]}`, "give exceeds"},
		{"give_unknown_nested_field", `{"with":"A","want_item":"bread","want_qty":1,"give":[{"item":"milk","qty":2,"extra":1}]}`, "malformed arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeOfferTradeArgs(json.RawMessage(tc.raw))
			if err == nil {
				t.Fatalf("want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

// TestOfferTrade_LoweredArgsBuildCommand — the decoded (lowered) args run
// through HandlePayWithItem and produce a real Command, proving the
// front-door reuses the existing handler end-to-end.
func TestOfferTrade_LoweredArgsBuildCommand(t *testing.T) {
	decoded, err := DecodeOfferTradeArgs(json.RawMessage(`{
        "with":"Josiah Thorne","give":[{"item":"milk","qty":5}],
        "want_item":"bread","want_qty":5
    }`))
	if err != nil {
		t.Fatalf("DecodeOfferTradeArgs: %v", err)
	}
	cmd, err := HandlePayWithItem(HandlerInput{
		ActorID:   "elizabeth",
		AttemptID: "tk-test",
		Args:      decoded.(PayWithItemArgs),
	})
	if err != nil {
		t.Fatalf("HandlePayWithItem on lowered offer_trade args: %v", err)
	}
	if cmd.Fn == nil {
		t.Fatal("returned Command has nil Fn")
	}
}

// TestPayOfferKey_OfferTrade — the same-tick dedup guard recognizes an
// offer_trade call (it was extended to accept the name in ZBBS-HOME-407). A
// silent miss here would let the re-offer storm return for the new tool.
func TestPayOfferKey_OfferTrade(t *testing.T) {
	decoded, err := DecodeOfferTradeArgs(json.RawMessage(`{
        "with":"Josiah Thorne","give":[{"item":"milk","qty":5}],
        "want_item":"bread","want_qty":5
    }`))
	if err != nil {
		t.Fatalf("DecodeOfferTradeArgs: %v", err)
	}
	key, ok := payOfferKey(&ValidatedCall{Name: "offer_trade", DecodedArgs: decoded})
	if !ok {
		t.Fatal("payOfferKey ok=false for an offer_trade call — dedup would be silently disabled for the new tool")
	}
	if want := "josiah thorne\x00bread\x00keep"; key != want {
		t.Errorf("key = %q, want %q", key, want)
	}
}

// TestCommitResultContent_OfferTradeSteer — a placed offer_trade gets the
// "now before them, call done()" steer (storm-prevention), phrased as a
// trade rather than a buy.
func TestCommitResultContent_OfferTradeSteer(t *testing.T) {
	decoded, err := DecodeOfferTradeArgs(json.RawMessage(`{
        "with":"Josiah Thorne","give":[{"item":"milk","qty":5}],
        "want_item":"bread","want_qty":5
    }`))
	if err != nil {
		t.Fatalf("DecodeOfferTradeArgs: %v", err)
	}
	got := commitResultContent(&ValidatedCall{Name: "offer_trade", DecodedArgs: decoded.(PayWithItemArgs)})
	for _, want := range []string{"trade for 5 bread with Josiah Thorne", "call done()", "accept, decline, or counter"} {
		if !strings.Contains(got, want) {
			t.Errorf("steer missing %q\ngot: %s", want, got)
		}
	}
}

// TestHarness_OfferTradeDedup_RejectsRepeatAcrossRounds — the end-to-end
// storm guard through RunTick: a proposer that re-offers the same trade on a
// later round (drifting coins) is blocked before dispatch, exactly as
// pay_with_item is. Mirrors TestHarness_PayDedup_RejectsRepeatOfferAcrossRounds.
func TestHarness_OfferTradeDedup_RejectsRepeatAcrossRounds(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "offer_trade", offerTradeJSON("Josiah Thorne", "bread", 5, "milk", 5, 0))}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "offer_trade", offerTradeJSON("Josiah Thorne", "bread", 5, "milk", 5, 2))}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)

	r := NewRegistry()
	var offerLog []string
	tradeFn := func(_ context.Context, in HandlerInput) (string, error) {
		args, ok := in.Args.(PayWithItemArgs)
		if !ok {
			return "", errors.New("offer_trade test handler: unexpected args type")
		}
		offerLog = append(offerLog, args.Seller+"/"+args.Item)
		return "[offer: ok]", nil
	}
	if err := r.RegisterObservation("offer_trade", offerTradeSchema, DecodeOfferTradeArgs, tradeFn, WithDescription(offerTradeDescription)); err != nil {
		t.Fatalf("register offer_trade: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done (model ended with done() after the repeat trade was blocked)", result.TerminalStatus)
	}
	if got := offerLog; len(got) != 1 {
		t.Errorf("trades placed: got %d %q, want exactly 1 (coin-drift repeat blocked)", len(got), got)
	}
	if !contains(result.ToolsFailedRejected, "offer_trade") {
		t.Errorf("ToolsFailedRejected should include the blocked repeat, got %v", result.ToolsFailedRejected)
	}
}

// offerTradeJSON builds an offer_trade tool-call payload. give is a single
// item+qty line (the common one-for-one swap); coins is added when > 0.
func offerTradeJSON(with, wantItem string, wantQty int, giveItem string, giveQty, coins int) string {
	b, _ := json.Marshal(offerTradeArgs{
		With:     with,
		Give:     []payItemArg{{Item: giveItem, Qty: giveQty}},
		Coins:    coins,
		WantItem: wantItem,
		WantQty:  wantQty,
	})
	return string(b)
}
