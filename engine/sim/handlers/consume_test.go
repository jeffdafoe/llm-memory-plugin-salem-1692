package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// consume_test.go — handler-package coverage of DecodeConsumeArgs +
// HandleConsume static validation. World-state validation (case-insensitive
// ItemKind resolution, Consumable check, walk-in-flight gate, inventory
// check, mutation + emit, dwell-credit upsert) is tested at the sim.Consume
// Command level in sim/item_commands_test.go.

// --- DecodeConsumeArgs ------------------------------------------------

func TestDecodeConsumeArgs_Valid(t *testing.T) {
	args, err := DecodeConsumeArgs(json.RawMessage(`{"item":"ale","qty":2}`))
	if err != nil {
		t.Fatalf("DecodeConsumeArgs: %v", err)
	}
	got, ok := args.(ConsumeArgs)
	if !ok {
		t.Fatalf("Decoded type = %T, want ConsumeArgs", args)
	}
	if got.Item != "ale" {
		t.Errorf("Item = %q, want ale", got.Item)
	}
	if got.Qty != 2 {
		t.Errorf("Qty = %d, want 2", got.Qty)
	}
}

func TestDecodeConsumeArgs_MissingItem(t *testing.T) {
	_, err := DecodeConsumeArgs(json.RawMessage(`{"qty":1}`))
	if err == nil {
		t.Fatal("DecodeConsumeArgs without item: want error, got nil")
	}
	if !strings.Contains(err.Error(), "item") {
		t.Errorf("error lacks 'item': %v", err)
	}
}

func TestDecodeConsumeArgs_EmptyItemField(t *testing.T) {
	_, err := DecodeConsumeArgs(json.RawMessage(`{"item":"","qty":1}`))
	if err == nil {
		t.Fatal("DecodeConsumeArgs with empty item: want error, got nil")
	}
}

func TestDecodeConsumeArgs_MissingQty(t *testing.T) {
	// qty omitted decodes as 0, which fails the >=1 check.
	_, err := DecodeConsumeArgs(json.RawMessage(`{"item":"ale"}`))
	if err == nil {
		t.Fatal("DecodeConsumeArgs without qty: want error, got nil")
	}
	if !strings.Contains(err.Error(), "at least 1") {
		t.Errorf("error lacks 'at least 1': %v", err)
	}
}

func TestDecodeConsumeArgs_ZeroQty(t *testing.T) {
	_, err := DecodeConsumeArgs(json.RawMessage(`{"item":"ale","qty":0}`))
	if err == nil {
		t.Fatal("DecodeConsumeArgs with qty=0: want error, got nil")
	}
	if !strings.Contains(err.Error(), "at least 1") {
		t.Errorf("error lacks 'at least 1': %v", err)
	}
}

func TestDecodeConsumeArgs_NegativeQty(t *testing.T) {
	_, err := DecodeConsumeArgs(json.RawMessage(`{"item":"ale","qty":-3}`))
	if err == nil {
		t.Fatal("DecodeConsumeArgs with negative qty: want error, got nil")
	}
}

func TestDecodeConsumeArgs_QtyOverMax(t *testing.T) {
	// 2147483648 = MaxInt32 + 1.
	_, err := DecodeConsumeArgs(json.RawMessage(`{"item":"ale","qty":2147483648}`))
	if err == nil {
		t.Fatal("DecodeConsumeArgs with qty > MaxInt32: want error, got nil")
	}
}

func TestDecodeConsumeArgs_QtyAtMax(t *testing.T) {
	if _, err := DecodeConsumeArgs(json.RawMessage(`{"item":"ale","qty":2147483647}`)); err != nil {
		t.Errorf("DecodeConsumeArgs at MaxInt32: %v", err)
	}
}

func TestDecodeConsumeArgs_FractionalQty(t *testing.T) {
	_, err := DecodeConsumeArgs(json.RawMessage(`{"item":"ale","qty":1.5}`))
	if err == nil {
		t.Fatal("DecodeConsumeArgs with fractional qty: want error, got nil")
	}
}

func TestDecodeConsumeArgs_UnknownField(t *testing.T) {
	_, err := DecodeConsumeArgs(json.RawMessage(`{"item":"ale","qty":1,"recipient":"X"}`))
	if err == nil {
		t.Fatal("DecodeConsumeArgs with unknown field: want error, got nil")
	}
}

func TestDecodeConsumeArgs_TrailingData(t *testing.T) {
	_, err := DecodeConsumeArgs(json.RawMessage(`{"item":"ale","qty":1} {"item":"bread","qty":1}`))
	if err == nil {
		t.Fatal("DecodeConsumeArgs with trailing data: want error, got nil")
	}
	if !strings.Contains(err.Error(), "trailing") {
		t.Errorf("error lacks 'trailing': %v", err)
	}
}

func TestDecodeConsumeArgs_ItemOverMax(t *testing.T) {
	long := strings.Repeat("a", MaxConsumeItemChars+1)
	body := `{"item":"` + long + `","qty":1}`
	_, err := DecodeConsumeArgs(json.RawMessage(body))
	if err == nil {
		t.Fatal("DecodeConsumeArgs with oversized item: want error, got nil")
	}
	if !strings.Contains(err.Error(), "cap") {
		t.Errorf("error lacks 'cap': %v", err)
	}
}

func TestDecodeConsumeArgs_NonObject(t *testing.T) {
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
			_, err := DecodeConsumeArgs(json.RawMessage(tc.raw))
			if err == nil {
				t.Fatalf("DecodeConsumeArgs(%q): want error, got nil", tc.raw)
			}
			if !strings.Contains(err.Error(), "JSON object") {
				t.Errorf("error lacks 'JSON object' guidance: %v", err)
			}
		})
	}
}

// TestDecodeConsumeArgs_MultibyteItemAtCap covers the rune-vs-byte contract:
// 64 multibyte chars passes; 65 fails.
func TestDecodeConsumeArgs_MultibyteItemAtCap(t *testing.T) {
	body := `{"item":"` + strings.Repeat("日", MaxConsumeItemChars) + `","qty":1}`
	if _, err := DecodeConsumeArgs(json.RawMessage(body)); err != nil {
		t.Errorf("DecodeConsumeArgs at item cap with multibyte chars: %v", err)
	}
	overBody := `{"item":"` + strings.Repeat("日", MaxConsumeItemChars+1) + `","qty":1}`
	if _, err := DecodeConsumeArgs(json.RawMessage(overBody)); err == nil {
		t.Error("DecodeConsumeArgs over item cap with multibyte chars: want error, got nil")
	}
}

// --- HandleConsume ----------------------------------------------------

func handleConsumeInput(t *testing.T, item string, qty int) (sim.Command, error) {
	t.Helper()
	return HandleConsume(HandlerInput{
		ActorID:   "hannah",
		AttemptID: "tk-test",
		Args:      ConsumeArgs{Item: item, Qty: qty},
	})
}

func TestHandleConsume_BuildsCommandForValidInput(t *testing.T) {
	cmd, err := handleConsumeInput(t, "ale", 2)
	if err != nil {
		t.Fatalf("HandleConsume: %v", err)
	}
	if cmd.Fn == nil {
		t.Error("Returned Command has nil Fn")
	}
}

func TestHandleConsume_TrimEmptyItem(t *testing.T) {
	_, err := handleConsumeInput(t, "   ", 1)
	if err == nil {
		t.Fatal("HandleConsume with whitespace-only item: want error, got nil")
	}
	if !strings.Contains(err.Error(), "empty after trim") {
		t.Errorf("error lacks 'empty after trim' guidance: %v", err)
	}
}

func TestHandleConsume_RejectsControlChar(t *testing.T) {
	// Newline in item is a typo-or-forge attempt; reject. Pay's `for` allows
	// \n / \r / \t but consume's `item` is an identifier — strictest scan.
	_, err := handleConsumeInput(t, "ale\nbread", 1)
	if err == nil {
		t.Fatal("HandleConsume with embedded newline: want error, got nil")
	}
	if !strings.Contains(err.Error(), "control character") {
		t.Errorf("error lacks 'control character' guidance: %v", err)
	}
}

func TestHandleConsume_RejectsTab(t *testing.T) {
	// Tab in item rejects same as newline — short-form identifier, strict
	// scan rejects all C0 controls + DEL + C1.
	_, err := handleConsumeInput(t, "ale\tbread", 1)
	if err == nil {
		t.Fatal("HandleConsume with embedded tab: want error, got nil")
	}
}

func TestHandleConsume_WrongArgsType(t *testing.T) {
	// Passing PayArgs in by mistake (defense in depth — the harness should
	// have routed correctly, but a misroute should surface as a typed error,
	// not a nil-pointer panic in the type assertion).
	_, err := HandleConsume(HandlerInput{
		ActorID:   "hannah",
		AttemptID: "tk-test",
		Args:      PayArgs{Recipient: "X", Amount: 1},
	})
	if err == nil {
		t.Fatal("HandleConsume with wrong args type: want error, got nil")
	}
	if !strings.Contains(err.Error(), "unexpected args type") {
		t.Errorf("error lacks 'unexpected args type' guidance: %v", err)
	}
}
