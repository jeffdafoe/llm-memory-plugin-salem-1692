package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_test.go — handler-package coverage of DecodePayArgs + HandlePay
// static validation. World-state validation (no-huddle, walk-in-flight,
// recipient resolve, balance, KindNPCShared matrix) is tested at the
// sim.Pay Command level in sim/pay_commands_test.go.

// --- DecodePayArgs ----------------------------------------------------

func TestDecodePayArgs_Valid(t *testing.T) {
	args, err := DecodePayArgs(json.RawMessage(`{"recipient":"Ezekiel","amount":3,"for":"ale"}`))
	if err != nil {
		t.Fatalf("DecodePayArgs: %v", err)
	}
	got, ok := args.(PayArgs)
	if !ok {
		t.Fatalf("Decoded type = %T, want PayArgs", args)
	}
	if got.Recipient != "Ezekiel" {
		t.Errorf("Recipient = %q, want %q", got.Recipient, "Ezekiel")
	}
	if got.Amount != 3 {
		t.Errorf("Amount = %d, want 3", got.Amount)
	}
	if got.For != "ale" {
		t.Errorf("For = %q, want %q", got.For, "ale")
	}
}

func TestDecodePayArgs_OmittedForIsZero(t *testing.T) {
	args, err := DecodePayArgs(json.RawMessage(`{"recipient":"Ezekiel","amount":3}`))
	if err != nil {
		t.Fatalf("DecodePayArgs: %v", err)
	}
	if args.(PayArgs).For != "" {
		t.Errorf("For = %q, want empty", args.(PayArgs).For)
	}
}

func TestDecodePayArgs_MissingRecipient(t *testing.T) {
	_, err := DecodePayArgs(json.RawMessage(`{"amount":3}`))
	if err == nil {
		t.Fatal("DecodePayArgs without recipient: want error, got nil")
	}
	if !strings.Contains(err.Error(), "recipient") {
		t.Errorf("error message lacks 'recipient': %v", err)
	}
}

func TestDecodePayArgs_EmptyRecipientField(t *testing.T) {
	_, err := DecodePayArgs(json.RawMessage(`{"recipient":"","amount":3}`))
	if err == nil {
		t.Fatal("DecodePayArgs with empty recipient: want error, got nil")
	}
}

func TestDecodePayArgs_ZeroAmount(t *testing.T) {
	_, err := DecodePayArgs(json.RawMessage(`{"recipient":"X","amount":0}`))
	if err == nil {
		t.Fatal("DecodePayArgs with amount=0: want error, got nil")
	}
	if !strings.Contains(err.Error(), "at least 1") {
		t.Errorf("error lacks 'at least 1': %v", err)
	}
}

func TestDecodePayArgs_NegativeAmount(t *testing.T) {
	_, err := DecodePayArgs(json.RawMessage(`{"recipient":"X","amount":-5}`))
	if err == nil {
		t.Fatal("DecodePayArgs with negative amount: want error, got nil")
	}
}

func TestDecodePayArgs_AmountOverMax(t *testing.T) {
	// 2147483648 = MaxInt32 + 1 — must reject.
	_, err := DecodePayArgs(json.RawMessage(`{"recipient":"X","amount":2147483648}`))
	if err == nil {
		t.Fatal("DecodePayArgs with amount > MaxInt32: want error, got nil")
	}
}

func TestDecodePayArgs_AmountAtMax(t *testing.T) {
	// 2147483647 = MaxInt32 — boundary, must succeed.
	if _, err := DecodePayArgs(json.RawMessage(`{"recipient":"X","amount":2147483647}`)); err != nil {
		t.Errorf("DecodePayArgs at MaxInt32: %v", err)
	}
}

func TestDecodePayArgs_FractionalAmount(t *testing.T) {
	// Schema says integer; the Go int decoder rejects fractional floats.
	_, err := DecodePayArgs(json.RawMessage(`{"recipient":"X","amount":3.5}`))
	if err == nil {
		t.Fatal("DecodePayArgs with fractional amount: want error, got nil")
	}
}

func TestDecodePayArgs_UnknownField(t *testing.T) {
	_, err := DecodePayArgs(json.RawMessage(`{"recipient":"X","amount":1,"item":"ale"}`))
	if err == nil {
		t.Fatal("DecodePayArgs with unknown field: want error, got nil")
	}
}

func TestDecodePayArgs_TrailingData(t *testing.T) {
	_, err := DecodePayArgs(json.RawMessage(`{"recipient":"X","amount":1} {"recipient":"Y","amount":1}`))
	if err == nil {
		t.Fatal("DecodePayArgs with trailing data: want error, got nil")
	}
	if !strings.Contains(err.Error(), "trailing") {
		t.Errorf("error message lacks 'trailing': %v", err)
	}
}

func TestDecodePayArgs_RecipientOverMax(t *testing.T) {
	long := strings.Repeat("a", MaxPayRecipientChars+1)
	body := `{"recipient":"` + long + `","amount":1}`
	_, err := DecodePayArgs(json.RawMessage(body))
	if err == nil {
		t.Fatal("DecodePayArgs with oversized recipient: want error, got nil")
	}
	if !strings.Contains(err.Error(), "cap") {
		t.Errorf("error message lacks 'cap': %v", err)
	}
}

func TestDecodePayArgs_ForOverMax(t *testing.T) {
	long := strings.Repeat("a", MaxPayForChars+1)
	body := `{"recipient":"X","amount":1,"for":"` + long + `"}`
	_, err := DecodePayArgs(json.RawMessage(body))
	if err == nil {
		t.Fatal("DecodePayArgs with oversized for: want error, got nil")
	}
}

func TestDecodePayArgs_NonObject(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"string", `"just a string"`},
		{"null", `null`},
		{"array", `[]`},
		{"number", `123`},
		{"bool", `true`},
		{"leading_whitespace_number", `   42`},
		{"empty", ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodePayArgs(json.RawMessage(tc.raw))
			if err == nil {
				t.Fatalf("DecodePayArgs(%q): want error, got nil", tc.raw)
			}
			if !strings.Contains(err.Error(), "JSON object") {
				t.Errorf("error lacks 'JSON object' guidance: %v", err)
			}
		})
	}
}

// TestDecodePayArgs_MultibyteRecipientAtCap covers the rune-vs-byte
// contract for the recipient field — 100 multibyte chars passes, 101 fails.
func TestDecodePayArgs_MultibyteRecipientAtCap(t *testing.T) {
	body := `{"recipient":"` + strings.Repeat("日", MaxPayRecipientChars) + `","amount":1}`
	if _, err := DecodePayArgs(json.RawMessage(body)); err != nil {
		t.Errorf("DecodePayArgs at recipient cap with multibyte chars: %v", err)
	}
	overBody := `{"recipient":"` + strings.Repeat("日", MaxPayRecipientChars+1) + `","amount":1}`
	if _, err := DecodePayArgs(json.RawMessage(overBody)); err == nil {
		t.Error("DecodePayArgs over recipient cap with multibyte chars: want error, got nil")
	}
}

// --- HandlePay --------------------------------------------------------

func handlePayInput(t *testing.T, recipient string, amount int, forText string) (sim.Command, error) {
	t.Helper()
	return HandlePay(HandlerInput{
		ActorID:   "hannah",
		AttemptID: "tk-test",
		Args:      PayArgs{Recipient: recipient, Amount: amount, For: forText},
	})
}

func TestHandlePay_BuildsCommandForValidInput(t *testing.T) {
	cmd, err := handlePayInput(t, "Ezekiel", 3, "ale")
	if err != nil {
		t.Fatalf("HandlePay: %v", err)
	}
	if cmd.Fn == nil {
		t.Error("Returned Command has nil Fn")
	}
}

func TestHandlePay_BuildsCommandForOmittedFor(t *testing.T) {
	cmd, err := handlePayInput(t, "Ezekiel", 3, "")
	if err != nil {
		t.Fatalf("HandlePay (no for): %v", err)
	}
	if cmd.Fn == nil {
		t.Error("Returned Command has nil Fn")
	}
}

func TestHandlePay_TrimsRecipient(t *testing.T) {
	if _, err := handlePayInput(t, "  Ezekiel  ", 3, ""); err != nil {
		t.Errorf("HandlePay with trimmable whitespace: %v", err)
	}
}

func TestHandlePay_RejectsWhitespaceOnlyRecipient(t *testing.T) {
	_, err := handlePayInput(t, "   \n\t  ", 3, "")
	if err == nil {
		t.Fatal("HandlePay: want error for whitespace-only recipient, got nil")
	}
	if !strings.Contains(err.Error(), "empty after trim") {
		t.Errorf("error lacks 'empty after trim': %v", err)
	}
}

func TestHandlePay_TrimsForText(t *testing.T) {
	// Trimmable surrounding whitespace shouldn't reject (the trim happens
	// before any other check on for). Empty-after-trim is allowed since
	// the field is optional.
	if _, err := handlePayInput(t, "Ezekiel", 1, "  ale  "); err != nil {
		t.Errorf("HandlePay with for whitespace: %v", err)
	}
}

func TestHandlePay_AllowsEmptyAfterTrimFor(t *testing.T) {
	// "  " trims to "" — that's a valid (optional) for field, NOT a reject.
	if _, err := handlePayInput(t, "Ezekiel", 1, "   "); err != nil {
		t.Errorf("HandlePay with for whitespace-only: %v (should be ok)", err)
	}
}

func TestHandlePay_RejectsControlCharInFor(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"null_byte", "ale\x00good"},
		{"bell", "ale\x07"},
		{"escape", "\x1B[31mred"},
		{"del", "ale\x7F"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := handlePayInput(t, "Ezekiel", 1, tc.text)
			if err == nil {
				t.Fatal("HandlePay: want error for control char in for, got nil")
			}
			if !strings.Contains(err.Error(), "control character") {
				t.Errorf("error lacks 'control character': %v", err)
			}
		})
	}
}

// TestHandlePay_NormalizesWhitespaceInFor verifies the `for` field collapses
// any run of Unicode whitespace (spaces / tabs / newlines / multi-spaces) to
// a single space, then strips leading/trailing. The for-text feeds the
// seller's perception prompt as inline metadata — a literal newline would
// split the warrant line and forge prompt layout. Normalization runs at
// intake so the substrate doesn't have to defend.
func TestHandlePay_NormalizesWhitespaceInFor(t *testing.T) {
	// We can't observe the normalized output without invoking Fn (which
	// needs a world). The structural guarantee: input shapes that contain
	// embedded \n / \t / multi-spaces all pass static validation (no
	// rejection); the Pay Command's downstream tests cover the persisted
	// SalientFact text shape directly.
	cases := []string{
		"line one\nline two",
		"\twith tab",
		"multi   space",
		"  leading\ttrailing  ",
		"normal text",
	}
	for _, text := range cases {
		t.Run(text, func(t *testing.T) {
			if _, err := handlePayInput(t, "Ezekiel", 1, text); err != nil {
				t.Errorf("HandlePay normalized-whitespace %q: %v", text, err)
			}
		})
	}
}

func TestHandlePay_WrongArgsType(t *testing.T) {
	_, err := HandlePay(HandlerInput{
		ActorID: "hannah",
		Args:    "not a PayArgs",
	})
	if err == nil {
		t.Fatal("HandlePay: want error for wrong args type, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected args type") {
		t.Errorf("error lacks 'unexpected args type': %v", err)
	}
}
