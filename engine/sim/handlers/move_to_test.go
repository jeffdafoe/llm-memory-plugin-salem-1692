package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

// move_to_test.go — handler-package coverage of DecodeMoveToArgs + HandleMoveTo
// static validation, plus the registration policy (move_to is terminal). World-
// state validation (id-vs-name resolution, structure-exists / already-there /
// already-walking rejects, enter-vs-visit derivation, MoveActor dispatch) is
// tested at the sim.MoveToDestination Command level in sim/move_to_test.go.

// --- DecodeMoveToArgs --------------------------------------------------

func TestDecodeMoveToArgs_Valid(t *testing.T) {
	args, err := DecodeMoveToArgs(json.RawMessage(`{"destination":"inn"}`))
	if err != nil {
		t.Fatalf("DecodeMoveToArgs: %v", err)
	}
	got, ok := args.(MoveToArgs)
	if !ok {
		t.Fatalf("Decoded type = %T, want MoveToArgs", args)
	}
	if got.Destination != "inn" {
		t.Errorf("Destination = %q, want 'inn'", got.Destination)
	}
}

// A place NAME goes in the same `destination` field — the engine resolves
// id-vs-name in the Command, not by which field the model chose.
func TestDecodeMoveToArgs_NameValid(t *testing.T) {
	args, err := DecodeMoveToArgs(json.RawMessage(`{"destination":"the Tavern"}`))
	if err != nil {
		t.Fatalf("DecodeMoveToArgs: %v", err)
	}
	got := args.(MoveToArgs)
	if got.Destination != "the Tavern" {
		t.Errorf("Destination = %q, want 'the Tavern'", got.Destination)
	}
}

// Aliases (LLM-320): location / place / structure_id / structure_name all fold
// into destination so a model reaching for a synonym (or the old jargon fields)
// still lands the walk instead of looping on "unknown field".
func TestDecodeMoveToArgs_Aliases(t *testing.T) {
	for _, tc := range []struct {
		field string
		want  string
	}{
		{"location", "the Well"},
		{"place", "the Well"},
		{"structure_id", "well"},
		{"structure_name", "the Well"},
	} {
		raw := json.RawMessage(`{"` + tc.field + `":"` + tc.want + `"}`)
		args, err := DecodeMoveToArgs(raw)
		if err != nil {
			t.Fatalf("alias %q: DecodeMoveToArgs: %v", tc.field, err)
		}
		if got := args.(MoveToArgs); got.Destination != tc.want {
			t.Errorf("alias %q: Destination = %q, want %q", tc.field, got.Destination, tc.want)
		}
	}
}

// Canonical `destination` wins over any alias present in the same call.
func TestDecodeMoveToArgs_DestinationWinsOverAlias(t *testing.T) {
	args, err := DecodeMoveToArgs(json.RawMessage(`{"destination":"inn","location":"the Tavern"}`))
	if err != nil {
		t.Fatalf("DecodeMoveToArgs: %v", err)
	}
	if got := args.(MoveToArgs); got.Destination != "inn" {
		t.Errorf("Destination = %q, want 'inn' (canonical wins over alias)", got.Destination)
	}
}

// A whitespace-only field is treated as ABSENT: a blank canonical destination
// alongside a valid alias still lands the walk (forgiving on emptiness).
func TestDecodeMoveToArgs_WhitespaceCanonicalFallsToAlias(t *testing.T) {
	args, err := DecodeMoveToArgs(json.RawMessage(`{"destination":"   ","location":"inn"}`))
	if err != nil {
		t.Fatalf("DecodeMoveToArgs: %v", err)
	}
	if got := args.(MoveToArgs); got.Destination != "inn" {
		t.Errorf("Destination = %q, want 'inn' (blank canonical falls to a valid alias)", got.Destination)
	}
}

// Every field the model sends is validated, even an alias that won't be
// selected — a malformed alias is a malformed tool call (strict on content).
func TestDecodeMoveToArgs_InvalidUnusedAliasRejected(t *testing.T) {
	// Valid canonical + a control-char-bearing alias → rejected, naming the alias.
	raw, err := json.Marshal(map[string]string{"destination": "inn", "location": "bad\x00name"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = DecodeMoveToArgs(json.RawMessage(raw))
	if err == nil || !strings.Contains(err.Error(), "location") || !strings.Contains(err.Error(), "control character") {
		t.Fatalf("want 'location ... control character' rejection, got %v", err)
	}
}

func TestDecodeMoveToArgs_OverCapUnusedAliasRejected(t *testing.T) {
	long := strings.Repeat("z", MaxMoveToDestinationChars+1)
	_, err := DecodeMoveToArgs(json.RawMessage(`{"destination":"inn","structure_id":"` + long + `"}`))
	if err == nil || !strings.Contains(err.Error(), "structure_id") {
		t.Fatalf("want 'structure_id exceeds cap' rejection, got %v", err)
	}
}

func TestDecodeMoveToArgs_MissingDestination(t *testing.T) {
	_, err := DecodeMoveToArgs(json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("want error for missing destination, got nil")
	}
	if !strings.Contains(err.Error(), "destination") {
		t.Errorf("error lacks 'destination': %v", err)
	}
}

func TestDecodeMoveToArgs_EmptyDestination(t *testing.T) {
	_, err := DecodeMoveToArgs(json.RawMessage(`{"destination":""}`))
	if err == nil {
		t.Fatal("want error for empty destination, got nil")
	}
}

// A whitespace-only value must read as ABSENT at decode (not present-but-empty),
// so it falls into the "destination is required" branch.
func TestDecodeMoveToArgs_WhitespaceOnlyRejected(t *testing.T) {
	_, err := DecodeMoveToArgs(json.RawMessage(`{"destination":"   "}`))
	if err == nil || !strings.Contains(err.Error(), "destination is required") {
		t.Fatalf("want 'destination is required' for whitespace-only, got %v", err)
	}
}

func TestDecodeMoveToArgs_OverCap(t *testing.T) {
	long := strings.Repeat("z", MaxMoveToDestinationChars+1)
	_, err := DecodeMoveToArgs(json.RawMessage(`{"destination":"` + long + `"}`))
	if err == nil || !strings.Contains(err.Error(), "destination") {
		t.Fatalf("want destination over-cap error, got %v", err)
	}
}

func TestDecodeMoveToArgs_UnknownField(t *testing.T) {
	_, err := DecodeMoveToArgs(json.RawMessage(`{"destination":"inn","bogus":1}`))
	if err == nil {
		t.Fatal("want error for unknown field, got nil")
	}
}

func TestDecodeMoveToArgs_TrailingData(t *testing.T) {
	_, err := DecodeMoveToArgs(json.RawMessage(`{"destination":"inn"}{}`))
	if err == nil {
		t.Fatal("want error for trailing data, got nil")
	}
}

func TestDecodeMoveToArgs_NonObject(t *testing.T) {
	_, err := DecodeMoveToArgs(json.RawMessage(`"inn"`))
	if err == nil {
		t.Fatal("want error for non-object payload, got nil")
	}
}

func TestDecodeMoveToArgs_ControlCharRejectedAtDecode(t *testing.T) {
	// Build the payload from a Go value carrying a real NUL (a \x00 Go escape),
	// so json.Marshal emits a valid JSON unicode escape — the decode-time
	// control-char scan must then reject the decoded NUL. Constructing it this
	// way avoids embedding a raw NUL in the source.
	raw, err := json.Marshal(map[string]string{"destination": "bad\x00name"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = DecodeMoveToArgs(json.RawMessage(raw))
	if err == nil || !strings.Contains(err.Error(), "control character") {
		t.Fatalf("want control-char rejection at decode, got %v", err)
	}
}

// --- HandleMoveTo ------------------------------------------------------

func TestHandleMoveTo_BuildsCommand(t *testing.T) {
	cmd, err := HandleMoveTo(HandlerInput{
		ActorID: "walker",
		Args:    MoveToArgs{Destination: "inn"},
	})
	if err != nil {
		t.Fatalf("HandleMoveTo: %v", err)
	}
	if cmd.Fn == nil {
		t.Error("HandleMoveTo returned a Command with a nil Fn")
	}
}

func TestHandleMoveTo_NameBuildsCommand(t *testing.T) {
	cmd, err := HandleMoveTo(HandlerInput{
		ActorID: "walker",
		Args:    MoveToArgs{Destination: "the Tavern"},
	})
	if err != nil {
		t.Fatalf("HandleMoveTo (name): %v", err)
	}
	if cmd.Fn == nil {
		t.Error("HandleMoveTo (name) returned a Command with a nil Fn")
	}
}

func TestHandleMoveTo_ControlCharRejected(t *testing.T) {
	_, err := HandleMoveTo(HandlerInput{
		ActorID: "walker",
		Args:    MoveToArgs{Destination: "bad\x00name"},
	})
	if err == nil || !strings.Contains(err.Error(), "control character") {
		t.Fatalf("want control-char rejection, got %v", err)
	}
}

func TestHandleMoveTo_WrongArgsType(t *testing.T) {
	_, err := HandleMoveTo(HandlerInput{ActorID: "walker", Args: "not-move-to-args"})
	if err == nil {
		t.Fatal("want error for unexpected args type, got nil")
	}
}

func TestHandleMoveTo_EmptyAfterTrim(t *testing.T) {
	_, err := HandleMoveTo(HandlerInput{ActorID: "walker", Args: MoveToArgs{Destination: "   "}})
	if err == nil {
		t.Fatal("want error for whitespace-only destination, got nil")
	}
}

// --- RegisterMoveTo ----------------------------------------------------

func TestRegisterMoveTo(t *testing.T) {
	r := NewRegistry()
	if err := RegisterMoveTo(r); err != nil {
		t.Fatalf("RegisterMoveTo: %v", err)
	}
	// Double-register must fail (duplicate name) — a wiring guard.
	if err := RegisterMoveTo(r); err == nil {
		t.Error("second RegisterMoveTo: want duplicate-name error, got nil")
	}
}

// TestRegisterMoveTo_Terminal pins the key ZBBS-HOME-285 decision: move_to ends
// the tick (TerminalOnSuccess), unlike the non-terminal take_break it was
// modeled on. A regression here would let a model chain a post-move speak that
// broadcasts at the room it just left (the v1 ZBBS-HOME-237 footgun).
func TestRegisterMoveTo_Terminal(t *testing.T) {
	r := NewRegistry()
	if err := RegisterMoveTo(r); err != nil {
		t.Fatalf("RegisterMoveTo: %v", err)
	}
	entry, ok := r.Lookup("move_to")
	if !ok {
		t.Fatal("move_to not found in registry after RegisterMoveTo")
	}
	if entry.Class != ClassCommit {
		t.Errorf("Class = %v, want ClassCommit", entry.Class)
	}
	if entry.TerminalPolicy != TerminalOnSuccess {
		t.Errorf("TerminalPolicy = %v, want TerminalOnSuccess (move_to ends the tick)", entry.TerminalPolicy)
	}
}
