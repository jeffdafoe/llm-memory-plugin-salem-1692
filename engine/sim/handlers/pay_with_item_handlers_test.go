package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
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

// TestDecodePayWithItem_LenientNullStringIDs is the LLM-42 regression guard.
// llama-3.3 emits the STRING "null" for an absent optional id; the bare uint64
// fields hard-failed the whole decode and the model reject-retry-looped for
// ~7.5 min of dead air (Ezekiel/Josiah horseshoe-for-cheese barter,
// 2026-06-19). LenientID coerces "null" → 0 (unset), landing the offer in the
// plain-barter path. Args mirror the live repro payload verbatim.
func TestDecodePayWithItem_LenientNullStringIDs(t *testing.T) {
	args, err := DecodePayWithItemArgs(json.RawMessage(`{
        "for":"","qty":1,"item":"Cheese","amount":0,"seller":"Josiah Thorne",
        "quote_id":"null","consumers":[],"pay_items":[{"qty":1,"item":"Horseshoe"}],
        "consume_now":true,"ready_in_days":0,"in_response_to":"null"
    }`))
	if err != nil {
		t.Fatalf("LLM-42 repro args should decode, got: %v", err)
	}
	got := args.(PayWithItemArgs)
	if got.QuoteID != 0 || got.InResponseTo != 0 {
		t.Errorf("QuoteID=%d InResponseTo=%d, want 0/0 (coerced unset)", got.QuoteID, got.InResponseTo)
	}
	if len(got.PayItems) != 1 || got.PayItems[0].Item != "Horseshoe" || got.PayItems[0].Qty != 1 {
		t.Errorf("PayItems = %+v, want [{Horseshoe 1}]", got.PayItems)
	}
}

// TestDecodePayWithItem_LenientIDForms exercises the LenientID decode surface
// on an OPTIONAL id (quote_id): the weak-model string forms ("null", "", a
// bare numeric string) coerce to 0 / the value, a real JSON number is
// unaffected, and genuinely malformed input is still rejected.
func TestDecodePayWithItem_LenientIDForms(t *testing.T) {
	base := `{"seller":"Aldous","item":"stew","qty":1,"amount":4,"consume_now":false,%s}`
	cases := []struct {
		name     string
		fragment string // the optional key/value injected into the payload
		wantID   LenientID
		wantErr  string // empty = success
	}{
		{"number", `"quote_id":5`, 5, ""},
		{"numeric_string", `"quote_id":"5"`, 5, ""},
		{"string_null", `"quote_id":"null"`, 0, ""},
		{"json_null", `"quote_id":null`, 0, ""},
		{"empty_string", `"quote_id":""`, 0, ""},
		{"omitted", `"ready_in_days":0`, 0, ""},
		{"non_numeric_string", `"quote_id":"abc"`, 0, "not a non-negative integer"},
		{"float", `"quote_id":1.5`, 0, "malformed arguments"},
		{"negative", `"quote_id":-3`, 0, "malformed arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(strings.Replace(base, "%s", tc.fragment, 1))
			args, err := DecodePayWithItemArgs(raw)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("want success, got %v", err)
			}
			if got := args.(PayWithItemArgs).QuoteID; got != tc.wantID {
				t.Errorf("QuoteID = %d, want %d", got, tc.wantID)
			}
		})
	}
}

// TestDecodeAcceptPay_LenientLedgerID — a REQUIRED ledger id sent as the
// weak-model string "null" / "" now coerces to 0 and trips the existing `< 1`
// check, surfacing the model-safe "must be at least 1" reason instead of the
// opaque "argument decode failed" the bare uint64 produced (the same loop
// class as LLM-42, on the seller's accept side). A stringified real id is
// honored.
func TestDecodeAcceptPay_LenientLedgerID(t *testing.T) {
	for _, raw := range []string{`{"ledger_id":"null"}`, `{"ledger_id":""}`} {
		_, err := DecodeAcceptPayArgs(json.RawMessage(raw))
		if err == nil {
			t.Fatalf("%s: want model-safe 'at least 1' error, got nil", raw)
		}
		if !strings.Contains(err.Error(), "at least 1") {
			t.Errorf("%s: err = %v, want substring 'at least 1'", raw, err)
		}
	}
	args, err := DecodeAcceptPayArgs(json.RawMessage(`{"ledger_id":"42"}`))
	if err != nil {
		t.Fatalf("numeric-string ledger_id should decode: %v", err)
	}
	if got := args.(AcceptPayArgs).LedgerID; got != 42 {
		t.Errorf("LedgerID = %d, want 42", got)
	}
}

// TestDecodeLedgerFamily_LenientLedgerID guards the LenientID coercion on the
// non-accept ledger decoders (decline / counter / withdraw share the same
// field type but have their own decode bodies). A string "null" ledger_id
// coerces to 0 and trips each decoder's `< 1` model-safe reject — protecting
// against future drift if one decoder's field type is changed back in isolation.
func TestDecodeLedgerFamily_LenientLedgerID(t *testing.T) {
	decoders := map[string]func(json.RawMessage) (any, error){
		"decline_pay":  DecodeDeclinePayArgs,
		"counter_pay":  DecodeCounterPayArgs,
		"withdraw_pay": DecodeWithdrawPayArgs,
	}
	// counter_pay additionally requires coins or goods; include amount so the
	// only failure under test is the coerced-zero ledger_id.
	raws := map[string]string{
		"decline_pay":  `{"ledger_id":"null"}`,
		"counter_pay":  `{"ledger_id":"null","amount":5}`,
		"withdraw_pay": `{"ledger_id":"null"}`,
	}
	for name, decode := range decoders {
		t.Run(name, func(t *testing.T) {
			_, err := decode(json.RawMessage(raws[name]))
			if err == nil {
				t.Fatal("want model-safe 'at least 1' error, got nil")
			}
			if !strings.Contains(err.Error(), "at least 1") {
				t.Errorf("err = %v, want substring 'at least 1'", err)
			}
		})
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

// coinXlatWorld builds the minimal world the LLM-290 coin-payment translation
// needs to run end-to-end: buyer and seller sharing a huddle, buyer holding
// coins. Seeded through the mem repo + LoadWorld (the buildPayTestWorld
// pattern) so the huddle-membership index Pay's recipient resolver reads is
// built. The translated command IS sim.Pay, so running it against a world is
// the proof the translation reached the pay flow (a staked pay_with_item offer
// would touch the ledger instead of moving coins).
func coinXlatWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {ID: "alice", DisplayName: "Alice", Kind: sim.KindNPCShared, State: sim.StateIdle,
			CurrentHuddleID: "h1", Coins: 10, RecentActions: sim.NewRingBuffer[sim.Action](4)},
		"bob": {ID: "bob", DisplayName: "Bob", Kind: sim.KindNPCShared, State: sim.StateIdle,
			CurrentHuddleID: "h1", Coins: 2, RecentActions: sim.NewRingBuffer[sim.Action](4)},
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

// TestHandlePayWithItem_CoinTokenTranslatesToPay (LLM-290): naming coins as
// the good to buy translates to the pay flow — coins move immediately, no
// pending offer is staked. The coin count comes from `amount` when set (the
// schema's coins-offered field is authoritative), else `qty` (the "pay 5
// coins" as qty=5 shape). Token matching is case- and article-tolerant.
func TestHandlePayWithItem_CoinTokenTranslatesToPay(t *testing.T) {
	cases := []struct {
		name      string
		args      PayWithItemArgs
		wantCoins int // expected transfer
	}{
		{"amount_carries_the_count", PayWithItemArgs{Seller: "Bob", Item: "coins", Qty: 1, Amount: 5}, 5},
		{"qty_fallback_when_no_amount", PayWithItemArgs{Seller: "Bob", Item: "coins", Qty: 4}, 4},
		{"amount_wins_over_qty", PayWithItemArgs{Seller: "Bob", Item: "coins", Qty: 3, Amount: 7}, 7},
		{"singular_and_article_tolerant", PayWithItemArgs{Seller: "Bob", Item: "  The Coin ", Qty: 2}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := HandlePayWithItem(HandlerInput{ActorID: "alice", AttemptID: "tk-test", Args: tc.args})
			if err != nil {
				t.Fatalf("HandlePayWithItem: %v", err)
			}
			w, stop := coinXlatWorld(t)
			defer stop()
			if _, err := w.Send(cmd); err != nil {
				t.Fatalf("translated command failed: %v", err)
			}
			type balances struct {
				buyer, recipient, ledger int
			}
			res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
				return balances{
					buyer:     world.Actors["alice"].Coins,
					recipient: world.Actors["bob"].Coins,
					ledger:    len(world.PayLedger),
				}, nil
			}})
			if err != nil {
				t.Fatalf("read balances: %v", err)
			}
			b := res.(balances)
			if b.buyer != 10-tc.wantCoins {
				t.Errorf("buyer coins = %d, want %d", b.buyer, 10-tc.wantCoins)
			}
			if b.recipient != 2+tc.wantCoins {
				t.Errorf("recipient coins = %d, want %d", b.recipient, 2+tc.wantCoins)
			}
			if b.ledger != 0 {
				t.Errorf("pay ledger has %d entries, want 0 — the translation must settle, not stake an offer", b.ledger)
			}
		})
	}
}

// TestHandlePayWithItem_CoinTokenWithGoodsSteers (LLM-290): a coin-token item
// alongside pay_items GOODS is a sale shape (goods offered for coins) —
// steered to the sell verbs, never guessed into a payment.
func TestHandlePayWithItem_CoinTokenWithGoodsSteers(t *testing.T) {
	_, err := HandlePayWithItem(HandlerInput{ActorID: "alice", AttemptID: "tk-test", Args: PayWithItemArgs{
		Seller: "Bob", Item: "coins", Qty: 1, Amount: 5,
		PayItems: payItemList{{Item: "bread", Qty: 2}},
	}})
	if err == nil || !strings.Contains(err.Error(), "a sale, not a buy") {
		t.Fatalf("want sale-shape steer, got %v", err)
	}
}

// TestFoldCoinPayItems (LLM-290): coin-token pay_items rows fold into the
// coin amount; goods rows pass through in order.
func TestFoldCoinPayItems(t *testing.T) {
	amount, goods, err := foldCoinPayItems(4, payItemList{
		{Item: "bread", Qty: 2},
		{Item: "coins", Qty: 3},
		{Item: "a coin", Qty: 1},
		{Item: "nail", Qty: 5},
	})
	if err != nil {
		t.Fatalf("foldCoinPayItems: %v", err)
	}
	if amount != 8 {
		t.Errorf("amount = %d, want 8 (4 + 3 + 1)", amount)
	}
	if len(goods) != 2 || goods[0].Item != "bread" || goods[1].Item != "nail" {
		t.Errorf("goods = %+v, want [bread nail]", goods)
	}

	if _, _, err := foldCoinPayItems(0, payItemList{{Item: "coins", Qty: 0}}); err == nil {
		t.Error("want error on zero coin quantity, got nil")
	}
	if _, _, err := foldCoinPayItems(sim.MaxPayWithItemAmount, payItemList{{Item: "coins", Qty: 1}}); err == nil {
		t.Error("want overflow error, got nil")
	}
}

// TestDecodeOfferTrade_CoinWantItemSteers (LLM-290): wanting coins for goods
// is a coin sale — steered at decode, before the PayWithItemArgs lowering
// could reach the pay_with_item coin-payment translation (which would invert
// the direction: the proposer means to RECEIVE coins, not hand them over).
func TestDecodeOfferTrade_CoinWantItemSteers(t *testing.T) {
	raw := json.RawMessage(`{"with":"Bob","give":[{"item":"bread","qty":2}],"want_item":"coins","want_qty":5}`)
	_, err := DecodeOfferTradeArgs(raw)
	if err == nil || !strings.Contains(err.Error(), "coin sale, not a trade") {
		t.Fatalf("want coin-sale steer, got %v", err)
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
		e, ok := r.Lookup(name)
		if !ok {
			t.Errorf("tool %q not registered", name)
			continue
		}
		// LLM-184: every pay-family commit is tick-terminal — a placed or
		// answered offer ends the tick, so a weak model can't storm the verb to
		// the round budget (pay_with_item / decline_pay / withdraw_pay x6,
		// observed live). A forced second call is the storm the guards alone
		// couldn't stop.
		if e.TerminalPolicy != TerminalOnSuccess {
			t.Errorf("tool %q TerminalPolicy = %v, want TerminalOnSuccess (LLM-184)", name, e.TerminalPolicy)
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
