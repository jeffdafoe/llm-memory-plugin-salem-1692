package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

// summon_test.go — handler-package coverage of DecodeSummonArgs +
// HandleSummon static validation, plus the registration policy (summon is
// terminal). World-state validation (target exists, messenger free,
// summon_point exists, reachability) is tested at the sim.DispatchSummon
// Command level in sim/summon_test.go.

// --- DecodeSummonArgs --------------------------------------------------

func TestDecodeSummonArgs_Valid(t *testing.T) {
	args, err := DecodeSummonArgs(json.RawMessage(`{"target":"John Proctor","reason":"News."}`))
	if err != nil {
		t.Fatalf("DecodeSummonArgs: %v", err)
	}
	got, ok := args.(SummonArgs)
	if !ok {
		t.Fatalf("Decoded type = %T, want SummonArgs", args)
	}
	if got.Target != "John Proctor" || got.Reason != "News." {
		t.Errorf("decoded args = %+v, want target='John Proctor' reason='News.'", got)
	}
}

func TestDecodeSummonArgs_ReasonOptional(t *testing.T) {
	args, err := DecodeSummonArgs(json.RawMessage(`{"target":"T"}`))
	if err != nil {
		t.Fatalf("DecodeSummonArgs (no reason): %v", err)
	}
	if args.(SummonArgs).Reason != "" {
		t.Errorf("reason should default empty, got %q", args.(SummonArgs).Reason)
	}
}

func TestDecodeSummonArgs_MissingTarget(t *testing.T) {
	_, err := DecodeSummonArgs(json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("want target-required error, got %v", err)
	}
}

func TestDecodeSummonArgs_TargetOverCap(t *testing.T) {
	long := strings.Repeat("z", MaxSummonTargetChars+1)
	if _, err := DecodeSummonArgs(json.RawMessage(`{"target":"` + long + `"}`)); err == nil {
		t.Fatal("want error for target over cap, got nil")
	}
}

func TestDecodeSummonArgs_ReasonOverCap(t *testing.T) {
	long := strings.Repeat("z", MaxSummonReasonChars+1)
	if _, err := DecodeSummonArgs(json.RawMessage(`{"target":"T","reason":"` + long + `"}`)); err == nil {
		t.Fatal("want error for reason over cap, got nil")
	}
}

func TestDecodeSummonArgs_UnknownField(t *testing.T) {
	if _, err := DecodeSummonArgs(json.RawMessage(`{"target":"T","bogus":1}`)); err == nil {
		t.Fatal("want error for unknown field, got nil")
	}
}

func TestDecodeSummonArgs_TrailingData(t *testing.T) {
	if _, err := DecodeSummonArgs(json.RawMessage(`{"target":"T"}{}`)); err == nil {
		t.Fatal("want error for trailing data, got nil")
	}
}

func TestDecodeSummonArgs_NonObject(t *testing.T) {
	if _, err := DecodeSummonArgs(json.RawMessage(`"T"`)); err == nil {
		t.Fatal("want error for non-object payload, got nil")
	}
}

// --- HandleSummon ------------------------------------------------------

func TestHandleSummon_BuildsCommand(t *testing.T) {
	cmd, err := HandleSummon(HandlerInput{ActorID: "summoner", Args: SummonArgs{Target: "T", Reason: "r"}})
	if err != nil {
		t.Fatalf("HandleSummon: %v", err)
	}
	if cmd.Fn == nil {
		t.Error("HandleSummon returned a Command with a nil Fn")
	}
}

func TestHandleSummon_WrongArgsType(t *testing.T) {
	if _, err := HandleSummon(HandlerInput{ActorID: "summoner", Args: "nope"}); err == nil {
		t.Fatal("want error for unexpected args type, got nil")
	}
}

func TestHandleSummon_EmptyAfterTrim(t *testing.T) {
	if _, err := HandleSummon(HandlerInput{ActorID: "summoner", Args: SummonArgs{Target: "   "}}); err == nil {
		t.Fatal("want error for whitespace-only target, got nil")
	}
}

func TestHandleSummon_ControlCharInTarget(t *testing.T) {
	if _, err := HandleSummon(HandlerInput{ActorID: "summoner", Args: SummonArgs{Target: "T\x00"}}); err == nil {
		t.Fatal("want error for control char in target, got nil")
	}
}

func TestHandleSummon_ControlCharInReason(t *testing.T) {
	if _, err := HandleSummon(HandlerInput{ActorID: "summoner", Args: SummonArgs{Target: "T", Reason: "bad\x00"}}); err == nil {
		t.Fatal("want error for control char in reason, got nil")
	}
}

// --- RegisterSummon ----------------------------------------------------

func TestRegisterSummon(t *testing.T) {
	r := NewRegistry()
	if err := RegisterSummon(r); err != nil {
		t.Fatalf("RegisterSummon: %v", err)
	}
	if err := RegisterSummon(r); err == nil {
		t.Error("second RegisterSummon: want duplicate-name error, got nil")
	}
}

// TestRegisterSummon_Terminal pins the decision: summon ends the tick.
func TestRegisterSummon_Terminal(t *testing.T) {
	r := NewRegistry()
	if err := RegisterSummon(r); err != nil {
		t.Fatalf("RegisterSummon: %v", err)
	}
	entry, ok := r.Lookup("summon")
	if !ok {
		t.Fatal("summon not found in registry after RegisterSummon")
	}
	if entry.Class != ClassCommit {
		t.Errorf("Class = %v, want ClassCommit", entry.Class)
	}
	if entry.TerminalPolicy != TerminalOnSuccess {
		t.Errorf("TerminalPolicy = %v, want TerminalOnSuccess (summon ends the tick)", entry.TerminalPolicy)
	}
}
