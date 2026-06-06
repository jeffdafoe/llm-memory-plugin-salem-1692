package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_with_item_handlers_test.go — Phase 3 PR S4 step 6. Handler-package
// coverage of Decode<X>Args static validation + Handle<X> pure-builder
// normalization for the five pay-with-item tools.
//
// World-state validation (huddle, ledger lookup, gates, transfer) is
// tested at the sim Command level in pay_with_item_commands_test.go.

// ====================================================================
// pay_with_item
// ====================================================================

func TestDecodePayWithItem_Valid(t *testing.T) {
	raw := json.RawMessage(`{
        "seller":"Aldous","item":"stew","qty":2,"amount":8,
        "consume_now":true,"consumers":["Bea","Carl"],
        "quote_id":5,"in_response_to":3,"for":"a round at the table"
    }`)
	args, err := DecodePayWithItemArgs(raw)
	if err != nil {
		t.Fatalf("DecodePayWithItemArgs: %v", err)
	}
	got := args.(PayWithItemArgs)
	if got.Seller != "Aldous" || got.Item != "stew" || got.Qty != 2 || got.Amount != 8 ||
		!got.ConsumeNow || got.QuoteID != 5 || got.InResponseTo != 3 ||
		got.For != "a round at the table" {
		t.Errorf("decoded args = %+v", got)
	}
	if len(got.Consumers) != 2 || got.Consumers[0] != "Bea" || got.Consumers[1] != "Carl" {
		t.Errorf("Consumers = %v", got.Consumers)
	}
}

func TestDecodePayWithItem_OmittedOptionalsAreZero(t *testing.T) {
	args, err := DecodePayWithItemArgs(json.RawMessage(`{
        "seller":"Aldous","item":"stew","qty":1,"amount":4,"consume_now":false
    }`))
	if err != nil {
		t.Fatalf("DecodePayWithItemArgs: %v", err)
	}
	got := args.(PayWithItemArgs)
	if got.QuoteID != 0 || got.InResponseTo != 0 || got.For != "" || len(got.Consumers) != 0 {
		t.Errorf("optionals not zero: %+v", got)
	}
}

// TestDecodePayWithItem_Barter — goods-only and mixed coin+goods offers
// decode (ZBBS-HOME-393): amount is optional, pay_items carries the goods,
// and a goods-bearing offer passes the must-offer-something rule.
// TestDecodePayWithItem_ReadyInDays — the advance-booking offset decodes
// within bounds (ZBBS-HOME-403); the lodging-only rule is enforced in the
// command, not the decoder.
func TestDecodePayWithItem_ReadyInDays(t *testing.T) {
	args, err := DecodePayWithItemArgs(json.RawMessage(`{
        "seller":"Aldous","item":"nights_stay","qty":2,"amount":56,
        "consume_now":false,"ready_in_days":3
    }`))
	if err != nil {
		t.Fatalf("DecodePayWithItemArgs: %v", err)
	}
	if got := args.(PayWithItemArgs).ReadyInDays; got != 3 {
		t.Errorf("ReadyInDays = %d, want 3", got)
	}
}

func TestDecodePayWithItem_Barter(t *testing.T) {
	// Goods only (no amount).
	args, err := DecodePayWithItemArgs(json.RawMessage(`{
        "seller":"Aldous","item":"stew","qty":1,"consume_now":false,
        "pay_items":[{"item":"nail","qty":5},{"item":"hammer","qty":1}]
    }`))
	if err != nil {
		t.Fatalf("goods-only decode: %v", err)
	}
	got := args.(PayWithItemArgs)
	if got.Amount != 0 {
		t.Errorf("Amount = %d, want 0 (omitted)", got.Amount)
	}
	if len(got.PayItems) != 2 || got.PayItems[0].Item != "nail" || got.PayItems[0].Qty != 5 ||
		got.PayItems[1].Item != "hammer" || got.PayItems[1].Qty != 1 {
		t.Errorf("PayItems = %+v", got.PayItems)
	}

	// Mixed coins + goods.
	if _, err := DecodePayWithItemArgs(json.RawMessage(`{
        "seller":"Aldous","item":"stew","qty":1,"amount":2,"consume_now":false,
        "pay_items":[{"item":"nail","qty":3}]
    }`)); err != nil {
		t.Fatalf("mixed decode: %v", err)
	}
}

// TestDecodePayWithItem_StringifiedPayItems — llama-3.3 intermittently emits
// pay_items as a STRINGIFIED JSON array (seen live in the Josiah/Elizabeth
// episode); the lenient payItemList decode accepts it (ZBBS-HOME-407).
func TestDecodePayWithItem_StringifiedPayItems(t *testing.T) {
	args, err := DecodePayWithItemArgs(json.RawMessage(`{
        "seller":"Aldous","item":"stew","qty":1,"consume_now":false,
        "pay_items":"[{\"item\": \"nail\", \"qty\": 5}]"
    }`))
	if err != nil {
		t.Fatalf("stringified pay_items decode: %v", err)
	}
	got := args.(PayWithItemArgs)
	if len(got.PayItems) != 1 || got.PayItems[0].Item != "nail" || got.PayItems[0].Qty != 5 {
		t.Errorf("PayItems = %+v, want [{nail 5}]", got.PayItems)
	}
}

// TestDecodeCounterPay_StringifiedPayItems — counter_pay's pay_items uses the
// same lenient payItemList type, so a stringified counter array must also
// decode (ZBBS-HOME-407 follow-up; guards the counter decoder path).
func TestDecodeCounterPay_StringifiedPayItems(t *testing.T) {
	decoded, err := DecodeCounterPayArgs(json.RawMessage(`{
        "ledger_id":7,
        "pay_items":"[{\"item\": \"nail\", \"qty\": 3}]"
    }`))
	if err != nil {
		t.Fatalf("stringified counter pay_items decode: %v", err)
	}
	got := decoded.(CounterPayArgs)
	if len(got.PayItems) != 1 || got.PayItems[0].Item != "nail" || got.PayItems[0].Qty != 3 {
		t.Errorf("PayItems = %+v, want [{nail 3}]", got.PayItems)
	}
}

func TestDecodePayWithItem_RejectsShapeErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"null", `null`, "must be a JSON object"},
		{"array", `[]`, "must be a JSON object"},
		{"string", `"oops"`, "must be a JSON object"},
		{"unknown_field", `{"seller":"A","item":"stew","qty":1,"amount":4,"consume_now":false,"frobnicate":true}`, "malformed arguments"},
		{"trailing_data", `{"seller":"A","item":"stew","qty":1,"amount":4,"consume_now":false}{"junk":true}`, "trailing data"},
		{"missing_seller", `{"item":"stew","qty":1,"amount":4,"consume_now":false}`, "seller is required"},
		{"missing_item", `{"seller":"A","qty":1,"amount":4,"consume_now":false}`, "item is required"},
		{"zero_qty", `{"seller":"A","item":"stew","qty":0,"amount":4,"consume_now":false}`, "qty must be at least 1"},
		{"negative_qty", `{"seller":"A","item":"stew","qty":-1,"amount":4,"consume_now":false}`, "qty must be at least 1"},
		{"zero_amount_no_goods", `{"seller":"A","item":"stew","qty":1,"amount":0,"consume_now":false}`, "must include coins or goods"},
		{"negative_amount", `{"seller":"A","item":"stew","qty":1,"amount":-5,"consume_now":false}`, "amount cannot be negative"},
		{"over_max_amount", `{"seller":"A","item":"stew","qty":1,"amount":2147483648,"consume_now":false}`, "amount exceeds maximum"},
		{"fractional_amount", `{"seller":"A","item":"stew","qty":1,"amount":3.5,"consume_now":false}`, "malformed arguments"},
		{"too_many_consumers", `{"seller":"A","item":"stew","qty":1,"amount":4,"consume_now":false,"consumers":["a","b","c","d","e","f","g","h","i"]}`, "consumers exceeds"},
		{"seller_over_cap", `{"seller":"` + strings.Repeat("a", 101) + `","item":"stew","qty":1,"amount":4,"consume_now":false}`, "seller exceeds"},
		{"item_over_cap", `{"seller":"A","item":"` + strings.Repeat("a", 65) + `","qty":1,"amount":4,"consume_now":false}`, "item exceeds"},
		{"for_over_cap", `{"seller":"A","item":"stew","qty":1,"amount":4,"consume_now":false,"for":"` + strings.Repeat("a", 201) + `"}`, "'for' text exceeds"},
		{"pay_items_zero_qty", `{"seller":"A","item":"stew","qty":1,"consume_now":false,"pay_items":[{"item":"nail","qty":0}]}`, "pay_items[0].qty must be at least 1"},
		{"pay_items_too_many", `{"seller":"A","item":"stew","qty":1,"consume_now":false,"pay_items":[{"item":"a","qty":1},{"item":"b","qty":1},{"item":"c","qty":1},{"item":"d","qty":1},{"item":"e","qty":1},{"item":"f","qty":1},{"item":"g","qty":1},{"item":"h","qty":1},{"item":"i","qty":1}]}`, "pay_items exceeds"},
		{"pay_items_unknown_nested_field", `{"seller":"A","item":"stew","qty":1,"consume_now":false,"pay_items":[{"item":"nail","qty":2,"extra":1}]}`, "malformed arguments"},
		{"ready_in_days_negative", `{"seller":"A","item":"stew","qty":1,"amount":4,"consume_now":false,"ready_in_days":-1}`, "ready_in_days cannot be negative"},
		{"ready_in_days_over_cap", `{"seller":"A","item":"stew","qty":1,"amount":4,"consume_now":false,"ready_in_days":31}`, "ready_in_days too far ahead"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodePayWithItemArgs(json.RawMessage(tc.raw))
			if err == nil {
				t.Fatalf("want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestHandlePayWithItem_BuildsCommand(t *testing.T) {
	cmd, err := HandlePayWithItem(HandlerInput{
		ActorID:   "alice",
		AttemptID: "tk-test",
		Args: PayWithItemArgs{
			Seller: "  Bob  ", Item: "stew", Qty: 1, Amount: 4,
			ConsumeNow: false, Consumers: []string{" Carl "},
			For: "  the news  ",
		},
	})
	if err != nil {
		t.Fatalf("HandlePayWithItem: %v", err)
	}
	if cmd.Fn == nil {
		t.Fatal("returned Command has nil Fn")
	}
}

func TestHandlePayWithItem_RejectsBadShapes(t *testing.T) {
	cases := []struct {
		name string
		args PayWithItemArgs
		want string
	}{
		{"empty_seller_after_trim", PayWithItemArgs{Seller: "   ", Item: "stew", Qty: 1, Amount: 4}, "seller is empty after trim"},
		{"empty_item_after_trim", PayWithItemArgs{Seller: "Bob", Item: "   ", Qty: 1, Amount: 4}, "item is empty after trim"},
		{"seller_control_char", PayWithItemArgs{Seller: "Bob\x01", Item: "stew", Qty: 1, Amount: 4}, "seller contains a disallowed control character"},
		{"item_control_char", PayWithItemArgs{Seller: "Bob", Item: "stew\x01", Qty: 1, Amount: 4}, "item contains a disallowed control character"},
		{"empty_consumer_after_trim", PayWithItemArgs{Seller: "Bob", Item: "stew", Qty: 1, Amount: 4, Consumers: []string{"   "}}, "consumers[0] is empty after trim"},
		{"consumer_control_char", PayWithItemArgs{Seller: "Bob", Item: "stew", Qty: 1, Amount: 4, Consumers: []string{"Carl\x01"}}, "consumers[0] contains a disallowed control character"},
		{"duplicate_consumer", PayWithItemArgs{Seller: "Bob", Item: "stew", Qty: 1, Amount: 4, Consumers: []string{"Carl", "carl"}}, "appears more than once"},
		{"for_control_char", PayWithItemArgs{Seller: "Bob", Item: "stew", Qty: 1, Amount: 4, For: "the news\x01"}, "'for' contains a disallowed control character"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := HandlePayWithItem(HandlerInput{ActorID: "alice", AttemptID: "tk-test", Args: tc.args})
			if err == nil {
				t.Fatalf("want error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestHandlePayWithItem_WrongArgsType(t *testing.T) {
	_, err := HandlePayWithItem(HandlerInput{ActorID: "alice", AttemptID: "tk-test", Args: PayArgs{Recipient: "X", Amount: 1}})
	if err == nil || !strings.Contains(err.Error(), "unexpected args type") {
		t.Fatalf("want unexpected-args-type error, got %v", err)
	}
}

// ====================================================================
// accept_pay
// ====================================================================

func TestDecodeAcceptPay(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string // empty = should succeed
	}{
		{"valid", `{"ledger_id":42}`, ""},
		{"null", `null`, "must be a JSON object"},
		{"array", `[]`, "must be a JSON object"},
		{"unknown_field", `{"ledger_id":42,"extra":true}`, "malformed arguments"},
		{"trailing", `{"ledger_id":42}{"x":1}`, "trailing data"},
		{"missing_ledger", `{}`, "at least 1"},
		{"zero_ledger", `{"ledger_id":0}`, "at least 1"},
		{"negative_ledger", `{"ledger_id":-1}`, "malformed arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, err := DecodeAcceptPayArgs(json.RawMessage(tc.raw))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want success, got %v", err)
				}
				if args.(AcceptPayArgs).LedgerID != 42 {
					t.Errorf("LedgerID = %d, want 42", args.(AcceptPayArgs).LedgerID)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestHandleAcceptPay_BuildsCommand(t *testing.T) {
	cmd, err := HandleAcceptPay(HandlerInput{
		ActorID: "bob", AttemptID: "tk-test",
		Args: AcceptPayArgs{LedgerID: 42},
	})
	if err != nil {
		t.Fatalf("HandleAcceptPay: %v", err)
	}
	if cmd.Fn == nil {
		t.Fatal("nil Fn")
	}
}

func TestHandleAcceptPay_WrongArgsType(t *testing.T) {
	_, err := HandleAcceptPay(HandlerInput{ActorID: "bob", AttemptID: "tk-test", Args: PayArgs{}})
	if err == nil || !strings.Contains(err.Error(), "unexpected args type") {
		t.Fatalf("want unexpected-args error, got %v", err)
	}
}

// ====================================================================
// decline_pay
// ====================================================================

func TestDecodeDeclinePay(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"valid_with_reason", `{"ledger_id":42,"reason":"too low"}`, ""},
		{"valid_no_reason", `{"ledger_id":42}`, ""},
		{"null", `null`, "must be a JSON object"},
		{"unknown_field", `{"ledger_id":42,"x":1}`, "malformed arguments"},
		{"zero_ledger", `{"ledger_id":0}`, "at least 1"},
		{"reason_over_cap", `{"ledger_id":42,"reason":"` + strings.Repeat("a", 221) + `"}`, "reason exceeds"},
		{"missing_ledger", `{"reason":"too low"}`, "at least 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeDeclinePayArgs(json.RawMessage(tc.raw))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want success, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestHandleDeclinePay(t *testing.T) {
	cmd, err := HandleDeclinePay(HandlerInput{
		ActorID: "bob", AttemptID: "tk-test",
		Args: DeclinePayArgs{LedgerID: 42, Reason: "  too low  "},
	})
	if err != nil {
		t.Fatalf("HandleDeclinePay: %v", err)
	}
	if cmd.Fn == nil {
		t.Fatal("nil Fn")
	}
	// Control char in reason is rejected.
	_, err = HandleDeclinePay(HandlerInput{
		ActorID: "bob", AttemptID: "tk-test",
		Args: DeclinePayArgs{LedgerID: 42, Reason: "too low\x01"},
	})
	if err == nil || !strings.Contains(err.Error(), "control character") {
		t.Errorf("want control-char error, got %v", err)
	}
}

// ====================================================================
// counter_pay
// ====================================================================

func TestDecodeCounterPay(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"valid", `{"ledger_id":42,"amount":7,"message":"how about seven"}`, ""},
		{"valid_no_message", `{"ledger_id":42,"amount":7}`, ""},
		{"missing_amount_no_goods", `{"ledger_id":42}`, "must propose coins or goods"},
		{"zero_amount_no_goods", `{"ledger_id":42,"amount":0}`, "must propose coins or goods"},
		{"goods_only", `{"ledger_id":42,"pay_items":[{"item":"nail","qty":5}]}`, ""},
		{"negative_amount", `{"ledger_id":42,"amount":-5}`, "amount cannot be negative"},
		{"over_max_amount", `{"ledger_id":42,"amount":2147483648}`, "amount exceeds maximum"},
		{"missing_ledger", `{"amount":7}`, "at least 1"},
		{"zero_ledger", `{"ledger_id":0,"amount":7}`, "at least 1"},
		{"message_over_cap", `{"ledger_id":42,"amount":7,"message":"` + strings.Repeat("a", 221) + `"}`, "message exceeds"},
		{"unknown_field", `{"ledger_id":42,"amount":7,"x":1}`, "malformed arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeCounterPayArgs(json.RawMessage(tc.raw))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want success, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestHandleCounterPay(t *testing.T) {
	cmd, err := HandleCounterPay(HandlerInput{
		ActorID: "bob", AttemptID: "tk-test",
		Args: CounterPayArgs{LedgerID: 42, Amount: 7, Message: "  how about seven  "},
	})
	if err != nil {
		t.Fatalf("HandleCounterPay: %v", err)
	}
	if cmd.Fn == nil {
		t.Fatal("nil Fn")
	}
}

// ====================================================================
// withdraw_pay
// ====================================================================

func TestDecodeWithdrawPay(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"valid_with_message", `{"ledger_id":42,"message":"changed my mind"}`, ""},
		{"valid_no_message", `{"ledger_id":42}`, ""},
		{"missing_ledger", `{}`, "at least 1"},
		{"zero_ledger", `{"ledger_id":0}`, "at least 1"},
		{"message_over_cap", `{"ledger_id":42,"message":"` + strings.Repeat("a", 221) + `"}`, "message exceeds"},
		{"unknown_field", `{"ledger_id":42,"x":1}`, "malformed arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeWithdrawPayArgs(json.RawMessage(tc.raw))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("want success, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestHandleWithdrawPay(t *testing.T) {
	cmd, err := HandleWithdrawPay(HandlerInput{
		ActorID: "alice", AttemptID: "tk-test",
		Args: WithdrawPayArgs{LedgerID: 42, Message: "changed my mind"},
	})
	if err != nil {
		t.Fatalf("HandleWithdrawPay: %v", err)
	}
	if cmd.Fn == nil {
		t.Fatal("nil Fn")
	}
}

// ====================================================================
// Registration
// ====================================================================

func TestRegisterPayWithItemFamily(t *testing.T) {
	r := NewRegistry()
	if err := RegisterPayWithItemFamily(r); err != nil {
		t.Fatalf("RegisterPayWithItemFamily: %v", err)
	}
	want := []string{"pay_with_item", "accept_pay", "decline_pay", "counter_pay", "withdraw_pay"}
	for _, name := range want {
		if _, ok := r.Lookup(name); !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
}

func TestRegisterPayWithItemFamily_RefusesDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := RegisterPayWithItem(r); err != nil {
		t.Fatalf("RegisterPayWithItem: %v", err)
	}
	if err := RegisterPayWithItemFamily(r); err == nil {
		t.Fatal("RegisterPayWithItemFamily after pre-existing pay_with_item: want error, got nil")
	}
}

// TestPayWithItemSchemas_Parse — defensive: every shipped schema must
// be valid JSON (a typo in the literal would silently break only at
// LLM call time).
func TestPayWithItemSchemas_Parse(t *testing.T) {
	schemas := map[string]json.RawMessage{
		"pay_with_item": payWithItemSchema,
		"accept_pay":    acceptPaySchema,
		"decline_pay":   declinePaySchema,
		"counter_pay":   counterPaySchema,
		"withdraw_pay":  withdrawPaySchema,
	}
	for name, s := range schemas {
		var v any
		if err := json.Unmarshal(s, &v); err != nil {
			t.Errorf("%s schema not valid JSON: %v", name, err)
		}
	}
}

// TestPayWithItemSchemas_NumericLiteralsMatchConstants — pin the schema
// literal sync invariant for the 220-rune Message cap. If
// sim.MaxPayMessageRunes changes, the schema literal must change too.
func TestPayWithItemSchemas_NumericLiteralsMatchConstants(t *testing.T) {
	if sim.MaxPayMessageRunes != MaxPayMessageHandlerRunes {
		t.Errorf(
			"sim.MaxPayMessageRunes=%d != handler MaxPayMessageHandlerRunes=%d — constants drifted",
			sim.MaxPayMessageRunes, MaxPayMessageHandlerRunes,
		)
	}
	if sim.MaxPayWithItemConsumers != MaxPayWithItemConsumersHandler {
		t.Errorf(
			"sim.MaxPayWithItemConsumers=%d != handler cap=%d — constants drifted",
			sim.MaxPayWithItemConsumers, MaxPayWithItemConsumersHandler,
		)
	}
}
