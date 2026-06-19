package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// deliver_order_test.go — Phase 3 PR S6 handler-side coverage.
// Mechanical decode + handler shape; the substantive validation
// happens in sim.DeliverOrder's Fn (covered by order_commands_test.go).

func TestDecodeDeliverOrderArgs_Valid(t *testing.T) {
	raw := json.RawMessage(`{"order_id": 42}`)
	got, err := DecodeDeliverOrderArgs(raw)
	if err != nil {
		t.Fatalf("Decode valid: %v", err)
	}
	args, ok := got.(DeliverOrderArgs)
	if !ok {
		t.Fatalf("Decode returned %T, want DeliverOrderArgs", got)
	}
	if args.OrderID != 42 {
		t.Errorf("OrderID = %d, want 42", args.OrderID)
	}
}

func TestDecodeDeliverOrderArgs_RejectsZeroOrderID(t *testing.T) {
	raw := json.RawMessage(`{"order_id": 0}`)
	_, err := DecodeDeliverOrderArgs(raw)
	if err == nil {
		t.Fatal("order_id=0: no error")
	}
	if !strings.Contains(err.Error(), "at least 1") {
		t.Errorf("err = %v, want 'at least 1'", err)
	}
}

// TestDecodeDeliverOrderArgs_LenientOrderID guards the LenientID coercion on
// order_id (LLM-42): the weak-model string "null" / "" coerces to 0 and trips
// the existing `< 1` reject (model-safe), and a stringified real id is honored.
func TestDecodeDeliverOrderArgs_LenientOrderID(t *testing.T) {
	for _, raw := range []string{`{"order_id":"null"}`, `{"order_id":""}`} {
		_, err := DecodeDeliverOrderArgs(json.RawMessage(raw))
		if err == nil {
			t.Fatalf("%s: want model-safe 'at least 1' error, got nil", raw)
		}
		if !strings.Contains(err.Error(), "at least 1") {
			t.Errorf("%s: err = %v, want 'at least 1'", raw, err)
		}
	}
	got, err := DecodeDeliverOrderArgs(json.RawMessage(`{"order_id":"7"}`))
	if err != nil {
		t.Fatalf("numeric-string order_id should decode: %v", err)
	}
	if id := got.(DeliverOrderArgs).OrderID; id != 7 {
		t.Errorf("OrderID = %d, want 7", id)
	}
}

func TestDecodeDeliverOrderArgs_RejectsNonObject(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `"123"`, `42`} {
		t.Run(raw, func(t *testing.T) {
			_, err := DecodeDeliverOrderArgs(json.RawMessage(raw))
			if err == nil {
				t.Errorf("%s: no error, want non-object reject", raw)
			}
		})
	}
}

func TestDecodeDeliverOrderArgs_RejectsUnknownFields(t *testing.T) {
	raw := json.RawMessage(`{"order_id": 1, "extra": "field"}`)
	_, err := DecodeDeliverOrderArgs(raw)
	if err == nil {
		t.Fatal("unknown field: no error")
	}
}

func TestDecodeDeliverOrderArgs_RejectsTrailingData(t *testing.T) {
	raw := json.RawMessage(`{"order_id": 1} garbage`)
	_, err := DecodeDeliverOrderArgs(raw)
	if err == nil {
		t.Fatal("trailing data: no error")
	}
}

func TestHandleDeliverOrder_BuildsCommand(t *testing.T) {
	args := DeliverOrderArgs{OrderID: 7}
	in := HandlerInput{ActorID: "hannah", Args: args}
	cmd, err := HandleDeliverOrder(in)
	if err != nil {
		t.Fatalf("HandleDeliverOrder: %v", err)
	}
	if cmd.Fn == nil {
		t.Error("returned Command has nil Fn")
	}
}

func TestHandleDeliverOrder_RejectsWrongArgsType(t *testing.T) {
	in := HandlerInput{ActorID: "hannah", Args: "not-a-DeliverOrderArgs"}
	_, err := HandleDeliverOrder(in)
	if err == nil {
		t.Fatal("wrong args type: no error")
	}
}

// TestRegisterDeliverOrder_ProducesUsableEntry pins that the tool
// registers cleanly with the registry (non-terminal commit tool).
func TestRegisterDeliverOrder_ProducesUsableEntry(t *testing.T) {
	r := NewRegistry()
	if err := RegisterDeliverOrder(r); err != nil {
		t.Fatalf("RegisterDeliverOrder: %v", err)
	}
	// Second registration must fail (duplicate name).
	if err := RegisterDeliverOrder(r); err == nil {
		t.Error("re-register: expected duplicate-name error")
	}
}

// Touch sim package so the import doesn't disappear if test bodies
// drop sim references later.
var _ = sim.OrderID(0)
