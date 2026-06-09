package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

// stay_open_test.go — handler-package coverage of DecodeStayOpenArgs static
// validation. World-state validation (already-committed reject, until_hour
// resolution, mutation + emit) is tested at the sim.StayOpen Command level in
// sim/stay_open_test.go. The contract that differs from take_break: until_hour
// is REQUIRED, and an explicit 0 (midnight) is a VALID close hour.

func TestDecodeStayOpenArgs_Valid(t *testing.T) {
	args, err := DecodeStayOpenArgs(json.RawMessage(`{"reason":"an order I still owe","until_hour":23}`))
	if err != nil {
		t.Fatalf("DecodeStayOpenArgs: %v", err)
	}
	got, ok := args.(StayOpenArgs)
	if !ok {
		t.Fatalf("Decoded type = %T, want StayOpenArgs", args)
	}
	if got.Reason != "an order I still owe" {
		t.Errorf("Reason = %q, want 'an order I still owe'", got.Reason)
	}
	if got.UntilHour == nil || *got.UntilHour != 23 {
		t.Errorf("UntilHour = %v, want 23", got.UntilHour)
	}
}

func TestDecodeStayOpenArgs_UntilHourRequired(t *testing.T) {
	// Unlike take_break, until_hour is required — committing to stay open means
	// committing to a closing hour.
	_, err := DecodeStayOpenArgs(json.RawMessage(`{"reason":"keeping the forge lit"}`))
	if err == nil {
		t.Fatal("want error for missing until_hour, got nil")
	}
	if !strings.Contains(err.Error(), "until_hour") {
		t.Errorf("error lacks 'until_hour': %v", err)
	}
}

func TestDecodeStayOpenArgs_MidnightAccepted(t *testing.T) {
	// An explicit until_hour:0 is midnight — a valid overnight close hour
	// (the inverse of take_break, where 0 is rejected).
	args, err := DecodeStayOpenArgs(json.RawMessage(`{"reason":"late customers","until_hour":0}`))
	if err != nil {
		t.Fatalf("until_hour:0 should be valid (midnight): %v", err)
	}
	if got := args.(StayOpenArgs); got.UntilHour == nil || *got.UntilHour != 0 {
		t.Errorf("UntilHour = %v, want 0", got.UntilHour)
	}
}

func TestDecodeStayOpenArgs_OutOfRange(t *testing.T) {
	_, err := DecodeStayOpenArgs(json.RawMessage(`{"reason":"x","until_hour":24}`))
	if err == nil {
		t.Fatal("want error for until_hour:24, got nil")
	}
}

func TestDecodeStayOpenArgs_MissingReason(t *testing.T) {
	_, err := DecodeStayOpenArgs(json.RawMessage(`{"until_hour":23}`))
	if err == nil {
		t.Fatal("want error for missing reason, got nil")
	}
	if !strings.Contains(err.Error(), "reason") {
		t.Errorf("error lacks 'reason': %v", err)
	}
}

func TestDecodeStayOpenArgs_UnknownField(t *testing.T) {
	_, err := DecodeStayOpenArgs(json.RawMessage(`{"reason":"x","until_hour":23,"foo":1}`))
	if err == nil {
		t.Fatal("want error for unknown field, got nil")
	}
}
