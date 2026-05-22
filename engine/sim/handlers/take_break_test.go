package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

// take_break_test.go — handler-package coverage of DecodeTakeBreakArgs +
// HandleTakeBreak static validation. World-state validation (already-on-break
// reject, until_hour resolution, mutation + emit) is tested at the
// sim.TakeBreak Command level in sim/take_break_test.go.

// --- DecodeTakeBreakArgs ----------------------------------------------

func TestDecodeTakeBreakArgs_Valid(t *testing.T) {
	args, err := DecodeTakeBreakArgs(json.RawMessage(`{"reason":"feeling unwell","until_hour":13}`))
	if err != nil {
		t.Fatalf("DecodeTakeBreakArgs: %v", err)
	}
	got, ok := args.(TakeBreakArgs)
	if !ok {
		t.Fatalf("Decoded type = %T, want TakeBreakArgs", args)
	}
	if got.Reason != "feeling unwell" {
		t.Errorf("Reason = %q, want 'feeling unwell'", got.Reason)
	}
	if got.UntilHour != 13 {
		t.Errorf("UntilHour = %d, want 13", got.UntilHour)
	}
}

func TestDecodeTakeBreakArgs_UntilHourOptional(t *testing.T) {
	// until_hour omitted decodes to 0 (→ default break length), not an error.
	args, err := DecodeTakeBreakArgs(json.RawMessage(`{"reason":"need a rest"}`))
	if err != nil {
		t.Fatalf("DecodeTakeBreakArgs without until_hour: %v", err)
	}
	if got := args.(TakeBreakArgs); got.UntilHour != 0 {
		t.Errorf("UntilHour = %d, want 0 (omitted)", got.UntilHour)
	}
}

func TestDecodeTakeBreakArgs_MissingReason(t *testing.T) {
	_, err := DecodeTakeBreakArgs(json.RawMessage(`{"until_hour":13}`))
	if err == nil {
		t.Fatal("want error for missing reason, got nil")
	}
	if !strings.Contains(err.Error(), "reason") {
		t.Errorf("error lacks 'reason': %v", err)
	}
}

func TestDecodeTakeBreakArgs_EmptyReason(t *testing.T) {
	_, err := DecodeTakeBreakArgs(json.RawMessage(`{"reason":""}`))
	if err == nil {
		t.Fatal("want error for empty reason, got nil")
	}
}

func TestDecodeTakeBreakArgs_ReasonOverCap(t *testing.T) {
	long := strings.Repeat("z", MaxTakeBreakReasonChars+1)
	_, err := DecodeTakeBreakArgs(json.RawMessage(`{"reason":"` + long + `"}`))
	if err == nil {
		t.Fatal("want error for reason over cap, got nil")
	}
}

func TestDecodeTakeBreakArgs_UntilHourOutOfRange(t *testing.T) {
	for _, h := range []string{"24", "-1", "100"} {
		_, err := DecodeTakeBreakArgs(json.RawMessage(`{"reason":"x and more","until_hour":` + h + `}`))
		if err == nil {
			t.Errorf("until_hour=%s: want error, got nil", h)
		}
	}
}

func TestDecodeTakeBreakArgs_UnknownField(t *testing.T) {
	_, err := DecodeTakeBreakArgs(json.RawMessage(`{"reason":"resting","bogus":1}`))
	if err == nil {
		t.Fatal("want error for unknown field, got nil")
	}
}

func TestDecodeTakeBreakArgs_TrailingData(t *testing.T) {
	_, err := DecodeTakeBreakArgs(json.RawMessage(`{"reason":"resting"}{}`))
	if err == nil {
		t.Fatal("want error for trailing data, got nil")
	}
}

func TestDecodeTakeBreakArgs_NonObject(t *testing.T) {
	_, err := DecodeTakeBreakArgs(json.RawMessage(`"resting"`))
	if err == nil {
		t.Fatal("want error for non-object payload, got nil")
	}
}

// --- HandleTakeBreak --------------------------------------------------

func TestHandleTakeBreak_BuildsCommand(t *testing.T) {
	cmd, err := HandleTakeBreak(HandlerInput{
		ActorID: "k",
		Args:    TakeBreakArgs{Reason: "feeling unwell", UntilHour: 13},
	})
	if err != nil {
		t.Fatalf("HandleTakeBreak: %v", err)
	}
	if cmd.Fn == nil {
		t.Error("HandleTakeBreak returned a Command with a nil Fn")
	}
}

func TestHandleTakeBreak_WrongArgsType(t *testing.T) {
	_, err := HandleTakeBreak(HandlerInput{ActorID: "k", Args: "not-take-break-args"})
	if err == nil {
		t.Fatal("want error for unexpected args type, got nil")
	}
}

func TestHandleTakeBreak_EmptyAfterTrim(t *testing.T) {
	_, err := HandleTakeBreak(HandlerInput{ActorID: "k", Args: TakeBreakArgs{Reason: "   "}})
	if err == nil {
		t.Fatal("want error for whitespace-only reason, got nil")
	}
}

func TestHandleTakeBreak_ControlCharInReason(t *testing.T) {
	// Bare NUL is a disallowed control char (the \n/\r/\t exemption doesn't
	// cover it).
	_, err := HandleTakeBreak(HandlerInput{ActorID: "k", Args: TakeBreakArgs{Reason: "rest\x00now"}})
	if err == nil {
		t.Fatal("want error for control char in reason, got nil")
	}
}

func TestRegisterTakeBreak(t *testing.T) {
	r := NewRegistry()
	if err := RegisterTakeBreak(r); err != nil {
		t.Fatalf("RegisterTakeBreak: %v", err)
	}
	// Double-register must fail (duplicate name) — a wiring guard.
	if err := RegisterTakeBreak(r); err == nil {
		t.Error("second RegisterTakeBreak: want duplicate-name error, got nil")
	}
}
