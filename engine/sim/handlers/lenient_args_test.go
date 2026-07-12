package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

// lenient_args_test.go — LLM-377 coverage: the weak-model scalar-tolerance
// types (LenientInt, LenientBool), the coin-synonym fold, and the live-repro
// regression that Prudence Ward's blacksmith loop distilled to.

// TestDecodePayWithItem_PrudenceLiveShapes replays Prudence Ward's literal
// pay_with_item calls from the virtual_agent_calls log (2026-07-12). Every one
// failed strict decode — she looped on them to budget_forced for hours. Each
// must now decode to the same buy: one nail, five coins, takeaway. Values are
// asserted, not just "decodes ok", so a future strictening can't pass this by
// accident.
func TestDecodePayWithItem_PrudenceLiveShapes(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"payment_nested_string_coins_string_qty_string_bool", `{"qty":"1","say":"I'd like to buy one nail from you, Ezekiel.","item":"nail","seller":"Ezekiel Crane","payment":{"coins":"5"},"pay_items":"[]","consume_now":"false"}`},
		{"payment_nested_int_string_qty", `{"qty":"1","item":"nail","seller":"Ezekiel Crane","payment":{"coins":5},"consume_now":false}`},
		{"top_level_coins_string_qty", `{"qty":"1","item":"nail","seller":"Ezekiel Crane","coins":5,"consume_now":false}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decoded, err := DecodePayWithItemArgs(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("want decode, got %v", err)
			}
			got := decoded.(PayWithItemArgs)
			if got.Item != "nail" || got.Seller != "Ezekiel Crane" || got.Qty != 1 ||
				got.Amount != 5 || got.ConsumeNow {
				t.Errorf("decoded = %+v; want item=nail seller='Ezekiel Crane' qty=1 amount=5 consume_now=false", got)
			}
		})
	}
}

// TestDecodePayWithItem_CoinSynonyms pins the fold precedence: an explicit
// non-zero amount wins; otherwise top-level coins, then payment.coins.
func TestDecodePayWithItem_CoinSynonyms(t *testing.T) {
	base := `{"seller":"Aldous","item":"stew","qty":1,"consume_now":false,%s}`
	cases := []struct {
		name       string
		fragment   string
		wantAmount int
	}{
		{"amount_only", `"amount":8`, 8},
		{"coins_only", `"coins":6`, 6},
		{"payment_nested", `"payment":{"coins":7}`, 7},
		{"amount_wins_over_coins", `"amount":8,"coins":6`, 8},
		{"coins_wins_over_payment", `"coins":6,"payment":{"coins":7}`, 6},
		{"coins_numeric_string", `"coins":"6"`, 6},
		// An explicit amount:0 is treated as unset, so a stray coins synonym folds
		// in — the sharpest precedence edge (pinned per code review).
		{"amount_zero_folds_coins", `"amount":0,"coins":5`, 5},
		// The nested payment object tolerates extra keys the model tacks on.
		{"payment_ignores_extra_keys", `"payment":{"coins":7,"currency":"usd"}`, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(strings.Replace(base, "%s", tc.fragment, 1))
			decoded, err := DecodePayWithItemArgs(raw)
			if err != nil {
				t.Fatalf("want decode, got %v", err)
			}
			if got := decoded.(PayWithItemArgs).Amount; got != tc.wantAmount {
				t.Errorf("Amount = %d, want %d", got, tc.wantAmount)
			}
		})
	}
}

// TestDecodeCounterPay_CoinSynonyms — the seller's counter accepts the same
// coin synonyms and a stringified amount.
func TestDecodeCounterPay_CoinSynonyms(t *testing.T) {
	for _, raw := range []string{
		`{"ledger_id":4,"coins":6}`,
		`{"ledger_id":4,"payment":{"coins":6}}`,
		`{"ledger_id":4,"amount":"6"}`,
	} {
		decoded, err := DecodeCounterPayArgs(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("%s: want decode, got %v", raw, err)
		}
		if got := decoded.(CounterPayArgs).Amount; got != 6 {
			t.Errorf("%s: Amount = %d, want 6", raw, got)
		}
	}
}

// TestDecodePay_CoinSynonyms — the plain pay tool accepts coins / payment.coins
// / a stringified amount too.
func TestDecodePay_CoinSynonyms(t *testing.T) {
	for _, raw := range []string{
		`{"recipient":"Aldous","coins":5}`,
		`{"recipient":"Aldous","payment":{"coins":5}}`,
		`{"recipient":"Aldous","amount":"5"}`,
	} {
		decoded, err := DecodePayArgs(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("%s: want decode, got %v", raw, err)
		}
		if got := decoded.(PayArgs).Amount; got != 5 {
			t.Errorf("%s: Amount = %d, want 5", raw, got)
		}
	}
}

// TestDecodePayWithItem_LenientConsumeNow — the boolean arrives in the shapes
// the weak model actually emits.
func TestDecodePayWithItem_LenientConsumeNow(t *testing.T) {
	base := `{"seller":"A","item":"stew","qty":1,"amount":4,"consume_now":%s}`
	cases := []struct {
		frag string
		want bool
	}{
		{`true`, true}, {`false`, false},
		{`"true"`, true}, {`"false"`, false},
		{`"1"`, true}, {`"0"`, false},
		{`1`, true}, {`0`, false},
	}
	for _, tc := range cases {
		t.Run(tc.frag, func(t *testing.T) {
			decoded, err := DecodePayWithItemArgs(json.RawMessage(strings.Replace(base, "%s", tc.frag, 1)))
			if err != nil {
				t.Fatalf("consume_now=%s: want decode, got %v", tc.frag, err)
			}
			if got := decoded.(PayWithItemArgs).ConsumeNow; got != tc.want {
				t.Errorf("consume_now=%s -> %v, want %v", tc.frag, got, tc.want)
			}
		})
	}
}

// TestLenientTypes_RejectGenuinelyBad — tolerance must not swallow real
// garbage: a non-numeric qty, a fractional qty, and a non-boolean consume_now
// still reject with model-safe reasons the weak model can act on.
func TestLenientTypes_RejectGenuinelyBad(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{"qty_non_numeric_string", `{"seller":"A","item":"stew","qty":"lots","amount":4,"consume_now":false}`, "not a whole number"},
		{"qty_fractional", `{"seller":"A","item":"stew","qty":1.5,"amount":4,"consume_now":false}`, "malformed arguments"},
		{"qty_empty_string", `{"seller":"A","item":"stew","qty":"","amount":4,"consume_now":false}`, "qty must be at least 1"},
		{"consume_now_non_bool", `{"seller":"A","item":"stew","qty":1,"amount":4,"consume_now":"maybe"}`, "not a boolean"},
		{"consume_now_numeric_two", `{"seller":"A","item":"stew","qty":1,"amount":4,"consume_now":2}`, "not a boolean"},
		{"coins_non_numeric", `{"seller":"A","item":"stew","qty":1,"consume_now":false,"coins":"lots"}`, "not a whole number"},
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

// TestLenientInt_Forms unit-tests the type directly, including the boundary
// cases the pay decoders lean on (empty string → unset, real number passthrough,
// fraction rejected).
func TestLenientInt_Forms(t *testing.T) {
	cases := []struct {
		raw     string
		want    LenientInt
		wantErr bool
	}{
		{`5`, 5, false}, {`"5"`, 5, false}, {`0`, 0, false},
		{`-3`, -3, false}, {`"-3"`, -3, false}, {`""`, 0, false},
		{`3.5`, 0, true}, {`"abc"`, 0, true}, {`true`, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			var got LenientInt
			err := json.Unmarshal([]byte(tc.raw), &got)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (value %d)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("want %d, got error %v", tc.want, err)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestLenientBool_Forms unit-tests the type directly.
func TestLenientBool_Forms(t *testing.T) {
	cases := []struct {
		raw     string
		want    LenientBool
		wantErr bool
	}{
		{`true`, true, false}, {`false`, false, false},
		{`"true"`, true, false}, {`"FALSE"`, false, false},
		{`"1"`, true, false}, {`"0"`, false, false},
		{`"yes"`, true, false}, {`"no"`, false, false},
		{`1`, true, false}, {`0`, false, false},
		{`"maybe"`, false, true}, {`[]`, false, true},
		// A number outside {0,1} is not a boolean — must reject, not widen to true.
		{`2`, false, true}, {`-1`, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			var got LenientBool
			err := json.Unmarshal([]byte(tc.raw), &got)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (value %v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("want %v, got error %v", tc.want, err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
