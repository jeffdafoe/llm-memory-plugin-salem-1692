package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

// disperse_test.go — LLM-453 arg-decode coverage for the disperse tool, mirroring
// take_break_test.go. The world-state behavior (leave, cooldown, part-reason) is
// covered by sim/disperse_test.go.

func TestDecodeDisperseArgs_Valid(t *testing.T) {
	got, err := DecodeDisperseArgs(json.RawMessage(`{"say":"I'll see you at supper, then"}`))
	if err != nil {
		t.Fatalf("valid disperse args: %v", err)
	}
	args, ok := got.(DisperseArgs)
	if !ok {
		t.Fatalf("decoded type = %T, want DisperseArgs", got)
	}
	if args.Say != "I'll see you at supper, then" {
		t.Errorf("Say = %q, want the parting line", args.Say)
	}
}

func TestDecodeDisperseArgs_MissingSay(t *testing.T) {
	if _, err := DecodeDisperseArgs(json.RawMessage(`{}`)); err == nil {
		t.Error("missing say should reject — a disperse carries a parting word")
	}
}

func TestDecodeDisperseArgs_EmptySay(t *testing.T) {
	if _, err := DecodeDisperseArgs(json.RawMessage(`{"say":""}`)); err == nil {
		t.Error("empty say should reject")
	}
}

func TestDecodeDisperseArgs_OverCap(t *testing.T) {
	long := strings.Repeat("x", MaxDisperseSayChars+1)
	if _, err := DecodeDisperseArgs(json.RawMessage(`{"say":"` + long + `"}`)); err == nil {
		t.Errorf("say over the %d-char cap should reject", MaxDisperseSayChars)
	}
}

func TestDecodeDisperseArgs_UnknownField(t *testing.T) {
	if _, err := DecodeDisperseArgs(json.RawMessage(`{"say":"bye","until_hour":13}`)); err == nil {
		t.Error("unknown field should reject (strict decode)")
	}
}

func TestDecodeDisperseArgs_NonObject(t *testing.T) {
	if _, err := DecodeDisperseArgs(json.RawMessage(`"bye"`)); err == nil {
		t.Error("non-object payload should reject with a crisp message")
	}
}

func TestDecodeDisperseArgs_TrailingData(t *testing.T) {
	if _, err := DecodeDisperseArgs(json.RawMessage(`{"say":"bye"} {}`)); err == nil {
		t.Error("trailing data after the JSON object should reject")
	}
}
