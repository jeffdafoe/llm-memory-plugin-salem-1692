package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

// move_to_test.go — handler-package coverage of DecodeMoveToArgs + HandleMoveTo
// static validation, plus the registration policy (move_to is terminal). World-
// state validation (structure-exists / already-there / already-walking rejects,
// enter-vs-visit derivation, MoveActor dispatch) is tested at the
// sim.MoveToStructure Command level in sim/move_to_test.go.

// --- DecodeMoveToArgs --------------------------------------------------

func TestDecodeMoveToArgs_Valid(t *testing.T) {
	args, err := DecodeMoveToArgs(json.RawMessage(`{"structure_id":"inn"}`))
	if err != nil {
		t.Fatalf("DecodeMoveToArgs: %v", err)
	}
	got, ok := args.(MoveToArgs)
	if !ok {
		t.Fatalf("Decoded type = %T, want MoveToArgs", args)
	}
	if got.StructureID != "inn" {
		t.Errorf("StructureID = %q, want 'inn'", got.StructureID)
	}
}

func TestDecodeMoveToArgs_MissingStructureID(t *testing.T) {
	_, err := DecodeMoveToArgs(json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("want error for missing structure_id, got nil")
	}
	if !strings.Contains(err.Error(), "structure_id") {
		t.Errorf("error lacks 'structure_id': %v", err)
	}
}

func TestDecodeMoveToArgs_EmptyStructureID(t *testing.T) {
	_, err := DecodeMoveToArgs(json.RawMessage(`{"structure_id":""}`))
	if err == nil {
		t.Fatal("want error for empty structure_id, got nil")
	}
}

func TestDecodeMoveToArgs_OverCap(t *testing.T) {
	long := strings.Repeat("z", MaxMoveToStructureIDChars+1)
	_, err := DecodeMoveToArgs(json.RawMessage(`{"structure_id":"` + long + `"}`))
	if err == nil {
		t.Fatal("want error for structure_id over cap, got nil")
	}
}

func TestDecodeMoveToArgs_UnknownField(t *testing.T) {
	_, err := DecodeMoveToArgs(json.RawMessage(`{"structure_id":"inn","bogus":1}`))
	if err == nil {
		t.Fatal("want error for unknown field, got nil")
	}
}

func TestDecodeMoveToArgs_TrailingData(t *testing.T) {
	_, err := DecodeMoveToArgs(json.RawMessage(`{"structure_id":"inn"}{}`))
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

// --- structure_name path (ZBBS-HOME-356) -------------------------------

func TestDecodeMoveToArgs_NameOnlyValid(t *testing.T) {
	args, err := DecodeMoveToArgs(json.RawMessage(`{"structure_name":"the Tavern"}`))
	if err != nil {
		t.Fatalf("DecodeMoveToArgs: %v", err)
	}
	got := args.(MoveToArgs)
	if got.StructureName != "the Tavern" || got.StructureID != "" {
		t.Errorf("got %+v, want name-only {StructureName:'the Tavern'}", got)
	}
}

func TestDecodeMoveToArgs_BothRejected(t *testing.T) {
	_, err := DecodeMoveToArgs(json.RawMessage(`{"structure_id":"inn","structure_name":"the Inn"}`))
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("want 'not both' error for id+name, got %v", err)
	}
}

func TestDecodeMoveToArgs_WhitespaceOnlyNameRejected(t *testing.T) {
	// A whitespace-only name must read as ABSENT at decode (not present-but-empty),
	// so it falls into the "provide structure_id or structure_name" branch.
	_, err := DecodeMoveToArgs(json.RawMessage(`{"structure_name":"   "}`))
	if err == nil || !strings.Contains(err.Error(), "provide structure_id") {
		t.Fatalf("want 'provide' error for whitespace-only name, got %v", err)
	}
}

func TestDecodeMoveToArgs_NameControlCharRejectedAtDecode(t *testing.T) {
	// Build the payload from a Go value carrying a real NUL (a \x00 Go escape),
	// so json.Marshal emits a valid JSON unicode escape — the decode-time
	// control-char scan must then reject the decoded NUL. Constructing it this
	// way avoids embedding a raw NUL in the source.
	raw, err := json.Marshal(map[string]string{"structure_name": "bad\x00name"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = DecodeMoveToArgs(json.RawMessage(raw))
	if err == nil || !strings.Contains(err.Error(), "control character") {
		t.Fatalf("want control-char rejection at decode, got %v", err)
	}
}

func TestDecodeMoveToArgs_NameOverCap(t *testing.T) {
	long := strings.Repeat("z", MaxMoveToStructureIDChars+1)
	_, err := DecodeMoveToArgs(json.RawMessage(`{"structure_name":"` + long + `"}`))
	if err == nil || !strings.Contains(err.Error(), "structure_name") {
		t.Fatalf("want structure_name over-cap error, got %v", err)
	}
}

// --- HandleMoveTo ------------------------------------------------------

func TestHandleMoveTo_BuildsCommand(t *testing.T) {
	cmd, err := HandleMoveTo(HandlerInput{
		ActorID: "walker",
		Args:    MoveToArgs{StructureID: "inn"},
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
		Args:    MoveToArgs{StructureName: "the Tavern"},
	})
	if err != nil {
		t.Fatalf("HandleMoveTo (name): %v", err)
	}
	if cmd.Fn == nil {
		t.Error("HandleMoveTo (name) returned a Command with a nil Fn")
	}
}

func TestHandleMoveTo_NameControlCharRejected(t *testing.T) {
	_, err := HandleMoveTo(HandlerInput{
		ActorID: "walker",
		Args:    MoveToArgs{StructureName: "bad\x00name"},
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
	_, err := HandleMoveTo(HandlerInput{ActorID: "walker", Args: MoveToArgs{StructureID: "   "}})
	if err == nil {
		t.Fatal("want error for whitespace-only structure_id, got nil")
	}
}

func TestHandleMoveTo_ControlCharInStructureID(t *testing.T) {
	// Bare NUL is a disallowed control char.
	_, err := HandleMoveTo(HandlerInput{ActorID: "walker", Args: MoveToArgs{StructureID: "in\x00n"}})
	if err == nil {
		t.Fatal("want error for control char in structure_id, got nil")
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
