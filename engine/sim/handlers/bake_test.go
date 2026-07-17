package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

// bake_test.go — LLM-454 arg-decode coverage for the bake tool. The world-state
// behavior (start/join session, occupy, completion) is covered by sim/bake_test.go.

func TestDecodeBakeArgs_EmptyIsValid(t *testing.T) {
	// bake takes no required args — an empty object is a valid "just start baking".
	got, err := DecodeBakeArgs(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("empty bake args should be valid: %v", err)
	}
	if args, ok := got.(BakeArgs); !ok || args.Say != "" {
		t.Errorf("decoded = %+v (%T), want empty BakeArgs", got, got)
	}
}

func TestDecodeBakeArgs_WithSay(t *testing.T) {
	got, err := DecodeBakeArgs(json.RawMessage(`{"say":"I'll get the bread on for us"}`))
	if err != nil {
		t.Fatalf("valid say: %v", err)
	}
	if args, ok := got.(BakeArgs); !ok || args.Say != "I'll get the bread on for us" {
		t.Errorf("decoded = %+v, want the say set", got)
	}
}

func TestDecodeBakeArgs_SayOverCap(t *testing.T) {
	long := strings.Repeat("x", MaxBakeSayChars+1)
	if _, err := DecodeBakeArgs(json.RawMessage(`{"say":"` + long + `"}`)); err == nil {
		t.Errorf("say over the %d-char cap should reject", MaxBakeSayChars)
	}
}

func TestDecodeBakeArgs_UnknownField(t *testing.T) {
	if _, err := DecodeBakeArgs(json.RawMessage(`{"loaves":3}`)); err == nil {
		t.Error("unknown field should reject (strict decode)")
	}
}

func TestDecodeBakeArgs_NonObject(t *testing.T) {
	if _, err := DecodeBakeArgs(json.RawMessage(`"bake"`)); err == nil {
		t.Error("non-object payload should reject")
	}
}
