package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_with_item_quote_take_test.go — LLM-905: on a quote_id take, seller, item
// and qty are optional at decode and in the handler; sim.PayWithItem fills them
// from the quote. Without a quote_id they stay required.

func TestDecodePayWithItem_QuoteTakeTermsOptional(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		// The bundle cue's exact call, as Caleb Wendell sent it live (quote_id as a string).
		{"quote_id_and_amount", `{"quote_id":"61","amount":7}`},
		{"seller_without_item", `{"seller":"Elizabeth Ellis","quote_id":61,"amount":7}`},
		// The single-good cue's call: item and qty, no seller.
		{"item_and_qty_without_seller", `{"quote_id":7,"item":"stew","qty":1,"amount":4}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodePayWithItemArgs(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("DecodePayWithItemArgs(%s): %v", tc.raw, err)
			}
			if got.(PayWithItemArgs).QuoteID == 0 {
				t.Errorf("QuoteID lost in decode")
			}
		})
	}
}

func TestDecodePayWithItem_TermsStillRequiredWithoutQuote(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"no_seller", `{"item":"stew","qty":1,"amount":4}`, "seller is required"},
		{"no_item", `{"seller":"Bob","qty":1,"amount":4}`, "item is required"},
		{"no_qty", `{"seller":"Bob","item":"stew","amount":4}`, "qty must be at least 1"},
		// A negative qty is never an omission, quote or not.
		{"negative_qty_on_quote", `{"quote_id":7,"qty":-1,"amount":4}`, "qty must be at least 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodePayWithItemArgs(json.RawMessage(tc.raw))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestHandlePayWithItem_QuoteTakeWithoutSellerOrItem(t *testing.T) {
	cmd, err := HandlePayWithItem(HandlerInput{ActorID: "alice", AttemptID: "tk-test", Args: PayWithItemArgs{
		Amount: 7, QuoteID: 61, Say: "Twelve coins for the flour and cheese.",
	}})
	if err != nil {
		t.Fatalf("HandlePayWithItem (quote take, no seller/item): %v", err)
	}
	if cmd.Fn == nil {
		t.Fatal("returned Command has nil Fn")
	}

	// Without a quote the handler still refuses an empty seller / item.
	if _, err := HandlePayWithItem(HandlerInput{ActorID: "alice", AttemptID: "tk-test", Args: PayWithItemArgs{
		Item: "stew", Qty: 1, Amount: 4,
	}}); err == nil || !strings.Contains(err.Error(), "seller is empty") {
		t.Errorf("no-quote empty seller: err = %v, want 'seller is empty'", err)
	}
	if _, err := HandlePayWithItem(HandlerInput{ActorID: "alice", AttemptID: "tk-test", Args: PayWithItemArgs{
		Seller: "Bob", Qty: 1, Amount: 4,
	}}); err == nil || !strings.Contains(err.Error(), "item is empty") {
		t.Errorf("no-quote empty item: err = %v, want 'item is empty'", err)
	}
}

// The coin-token steer on a quote take no longer asks for an item name.
func TestHandlePayWithItem_CoinTokenOnQuoteSteersToAmount(t *testing.T) {
	_, err := HandlePayWithItem(HandlerInput{ActorID: "alice", AttemptID: "tk-test", Args: PayWithItemArgs{
		Seller: "Elizabeth Ellis", Item: "coins", Qty: 12, QuoteID: 62,
	}})
	if err == nil || !strings.Contains(err.Error(), "quote_id 62 and your coins in amount") ||
		!strings.Contains(err.Error(), "leave item out") {
		t.Fatalf("err = %v, want the steer to quote_id + amount", err)
	}
}

// The settle line names the seller and goods from the result when the take
// left them out of its args (the HOME-436 feedback must not degrade to [ok]).
func TestSettledPayContent_QuoteTakeTermsFromResult(t *testing.T) {
	single := settledPayContent(PayWithItemArgs{Amount: 4, QuoteID: 7}, sim.PayWithItemResult{
		State: sim.PayLedgerStateAccepted, FastPath: true, TookHome: true,
		SellerName: "Bob", ItemKind: "stew", Qty: 1,
	}, "")
	if !strings.Contains(single, "you pay Bob 4 coins for 1 stew") {
		t.Errorf("single-good settle = %q, want seller and goods from the result", single)
	}

	bundle := settledPayContent(PayWithItemArgs{Amount: 12, QuoteID: 62}, sim.PayWithItemResult{
		State: sim.PayLedgerStateAccepted, FastPath: true, TookHome: true,
		SellerName: "Elizabeth Ellis", ItemKind: "flour", Qty: 1,
		Lines: []sim.QuoteLine{{ItemKind: "flour", Qty: 1}, {ItemKind: "cheese", Qty: 1}},
	}, "")
	if !strings.Contains(bundle, "you pay Elizabeth Ellis 12 coins for 1 flour and 1 cheese") {
		t.Errorf("bundle settle = %q, want seller and the whole bundle", bundle)
	}

	// Named args still win over the result.
	named := settledPayContent(PayWithItemArgs{Seller: "Bob", Item: "Stew", Qty: 1, Amount: 4, QuoteID: 7}, sim.PayWithItemResult{
		State: sim.PayLedgerStateAccepted, FastPath: true, TookHome: true,
		SellerName: "Bob", ItemKind: "stew", Qty: 1,
	}, "")
	if !strings.Contains(named, "you pay Bob 4 coins for 1 stew") {
		t.Errorf("named settle = %q", named)
	}
}
