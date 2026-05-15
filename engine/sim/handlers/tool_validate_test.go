package handlers

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// --- per-tool decoder + handler used by these tests ---------------------

type moveToArgs struct {
	Destination string `json:"destination"`
}

func decodeMoveTo(raw json.RawMessage) (any, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var args moveToArgs
	if err := dec.Decode(&args); err != nil {
		return nil, err
	}
	if args.Destination == "" {
		return nil, errors.New("destination is required")
	}
	return args, nil
}

func newMoveToRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	commitFn := func(_ HandlerInput) (sim.Command, error) { return sim.Command{}, nil }
	schema := schemaBytes(t, `{"type":"object","properties":{"destination":{"type":"string"}},"required":["destination"],"additionalProperties":false}`)
	if err := r.RegisterCommit("move_to", schema, decodeMoveTo, commitFn, true); err != nil {
		t.Fatalf("register move_to: %v", err)
	}
	return r
}

// --- happy path ----------------------------------------------------------

func TestValidator_Validate_Happy(t *testing.T) {
	r := newMoveToRegistry(t)
	v := NewValidator(r)

	args := json.RawMessage(`{"destination":"Tavern"}`)
	vc, verr := v.Validate(llm.RawToolCall{
		ID:        "call_abc",
		Index:     0,
		Name:      "move_to",
		Arguments: args,
	})
	if verr != nil {
		t.Fatalf("Validate: unexpected error %v", verr)
	}
	if vc == nil {
		t.Fatal("Validate: nil ValidatedCall on success")
	}
	if vc.Name != "move_to" {
		t.Errorf("Name: got %q, want \"move_to\"", vc.Name)
	}
	if vc.Entry == nil || vc.Entry.Class != ClassCommit {
		t.Errorf("Entry: got %+v, want ClassCommit entry", vc.Entry)
	}
	if vc.RawCallID != "call_abc" {
		t.Errorf("RawCallID: got %q, want \"call_abc\"", vc.RawCallID)
	}
	if vc.Index != 0 {
		t.Errorf("Index: got %d, want 0", vc.Index)
	}
	dec, ok := vc.DecodedArgs.(moveToArgs)
	if !ok {
		t.Fatalf("DecodedArgs: got %T, want moveToArgs", vc.DecodedArgs)
	}
	if dec.Destination != "Tavern" {
		t.Errorf("DecodedArgs.Destination: got %q, want \"Tavern\"", dec.Destination)
	}
}

// --- unknown tool --------------------------------------------------------

func TestValidator_Validate_UnknownTool(t *testing.T) {
	r := newMoveToRegistry(t)
	v := NewValidator(r)

	vc, verr := v.Validate(llm.RawToolCall{Name: "ghost", Arguments: json.RawMessage(`{}`)})
	if vc != nil {
		t.Errorf("expected nil ValidatedCall on unknown tool")
	}
	if verr == nil {
		t.Fatal("expected ValidationError, got nil")
	}
	if verr.Kind != ValidationErrorUnknownTool {
		t.Errorf("Kind: got %s, want unknown_tool", verr.Kind)
	}
	if verr.Tool != "ghost" {
		t.Errorf("Tool: got %q, want \"ghost\"", verr.Tool)
	}
}

// --- disabled tool -------------------------------------------------------

func TestValidator_Validate_DisabledTool(t *testing.T) {
	r := NewRegistry()
	commitFn := func(_ HandlerInput) (sim.Command, error) { return sim.Command{}, nil }
	if err := r.RegisterCommit("speak", schemaBytes(t, `{"type":"object"}`), trivialDecode, commitFn, true, WithAvailability(AvailabilityDisabled)); err != nil {
		t.Fatalf("register speak: %v", err)
	}
	v := NewValidator(r)

	vc, verr := v.Validate(llm.RawToolCall{Name: "speak", Arguments: json.RawMessage(`{}`)})
	if vc != nil {
		t.Errorf("expected nil ValidatedCall on disabled tool")
	}
	if verr == nil || verr.Kind != ValidationErrorToolUnavailable {
		t.Fatalf("expected tool_unavailable_in_this_build, got %v", verr)
	}
	// The Kind.String() surface label must match the design contract — it's
	// what the model sees as the tool error.
	if verr.Kind.String() != "tool_unavailable_in_this_build" {
		t.Errorf("Kind label: got %q, want \"tool_unavailable_in_this_build\"", verr.Kind.String())
	}
}

// --- args size cap -------------------------------------------------------

func TestValidator_Validate_ArgsTooLarge_Default(t *testing.T) {
	r := newMoveToRegistry(t)
	v := NewValidator(r)

	// Build a JSON object whose serialized size exceeds DefaultMaxArgsBytes.
	big := strings.Repeat("A", DefaultMaxArgsBytes+1)
	args := json.RawMessage(`{"destination":"` + big + `"}`)

	vc, verr := v.Validate(llm.RawToolCall{Name: "move_to", Arguments: args})
	if vc != nil {
		t.Errorf("expected nil ValidatedCall on oversize args")
	}
	if verr == nil || verr.Kind != ValidationErrorArgsTooLarge {
		t.Fatalf("expected args_too_large, got %v", verr)
	}
}

func TestValidator_Validate_ArgsTooLarge_CustomCap(t *testing.T) {
	r := newMoveToRegistry(t)
	v := &Validator{Registry: r, MaxArgsBytes: 16}

	// Args of exactly 17 bytes (>16) trigger the custom cap.
	args := json.RawMessage(`{"destination":"X"}`) // 19 bytes
	vc, verr := v.Validate(llm.RawToolCall{Name: "move_to", Arguments: args})
	if vc != nil || verr == nil || verr.Kind != ValidationErrorArgsTooLarge {
		t.Fatalf("expected args_too_large with custom cap; got vc=%v verr=%v", vc, verr)
	}
}

func TestValidator_Validate_NonPositiveCap_FallsBackToDefault(t *testing.T) {
	r := newMoveToRegistry(t)
	// MaxArgsBytes=0 → default. MaxArgsBytes=-5 → default.
	for _, cap := range []int{0, -5} {
		v := &Validator{Registry: r, MaxArgsBytes: cap}
		// Args well under the default — should validate fine.
		args := json.RawMessage(`{"destination":"X"}`)
		_, verr := v.Validate(llm.RawToolCall{Name: "move_to", Arguments: args})
		if verr != nil {
			t.Errorf("cap=%d should fall back to default, but Validate failed: %v", cap, verr)
		}
	}
}

// --- decode failures -----------------------------------------------------

func TestValidator_Validate_MalformedArgs_UnknownField(t *testing.T) {
	r := newMoveToRegistry(t)
	v := NewValidator(r)

	// "extra" is an unknown field; the decoder is DisallowUnknownFields.
	args := json.RawMessage(`{"destination":"Tavern","extra":"junk"}`)
	vc, verr := v.Validate(llm.RawToolCall{Name: "move_to", Arguments: args})
	if vc != nil {
		t.Errorf("expected nil ValidatedCall on unknown field")
	}
	if verr == nil || verr.Kind != ValidationErrorMalformedArgs {
		t.Fatalf("expected malformed_args, got %v", verr)
	}
	if verr.Cause == nil {
		t.Errorf("expected Cause to be set (decoder error)")
	}
}

func TestValidator_Validate_MalformedArgs_TypeMismatch(t *testing.T) {
	r := newMoveToRegistry(t)
	v := NewValidator(r)

	// destination should be a string; passing an integer is a type mismatch.
	args := json.RawMessage(`{"destination":42}`)
	_, verr := v.Validate(llm.RawToolCall{Name: "move_to", Arguments: args})
	if verr == nil || verr.Kind != ValidationErrorMalformedArgs {
		t.Fatalf("expected malformed_args on type mismatch, got %v", verr)
	}
}

func TestValidator_Validate_MalformedArgs_RequiredField(t *testing.T) {
	r := newMoveToRegistry(t)
	v := NewValidator(r)

	// Empty object — destination is required (per decodeMoveTo's manual check).
	args := json.RawMessage(`{}`)
	_, verr := v.Validate(llm.RawToolCall{Name: "move_to", Arguments: args})
	if verr == nil || verr.Kind != ValidationErrorMalformedArgs {
		t.Fatalf("expected malformed_args on missing required, got %v", verr)
	}
}

// --- terminal-class dispatch via Validate -------------------------------

func TestValidator_Validate_TerminalAcceptsEmptyArgs(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	v := NewValidator(r)

	cases := []json.RawMessage{
		json.RawMessage(``),
		json.RawMessage(`{}`),
		json.RawMessage(`null`),
	}
	for _, args := range cases {
		t.Run(string(args), func(t *testing.T) {
			vc, verr := v.Validate(llm.RawToolCall{Name: "done", Arguments: args})
			if verr != nil {
				t.Errorf("expected success, got %v", verr)
			}
			if vc == nil || vc.Entry.Class != ClassTerminal {
				t.Errorf("expected terminal ValidatedCall, got %+v", vc)
			}
		})
	}
}

func TestValidator_Validate_TerminalRejectsArgs(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	v := NewValidator(r)

	_, verr := v.Validate(llm.RawToolCall{Name: "done", Arguments: json.RawMessage(`{"smuggled":true}`)})
	if verr == nil || verr.Kind != ValidationErrorMalformedArgs {
		t.Fatalf("expected malformed_args for terminal with args, got %v", verr)
	}
}

// --- nil safety ----------------------------------------------------------

func TestValidator_Validate_NilValidator(t *testing.T) {
	var v *Validator
	_, verr := v.Validate(llm.RawToolCall{Name: "anything"})
	if verr == nil {
		t.Fatal("nil validator: expected ValidationError, got nil")
	}
	if verr.Kind != ValidationErrorUnknownTool {
		t.Errorf("Kind: got %s, want unknown_tool", verr.Kind)
	}
}

func TestValidator_Validate_NilRegistry(t *testing.T) {
	v := &Validator{Registry: nil}
	_, verr := v.Validate(llm.RawToolCall{Name: "anything"})
	if verr == nil || verr.Kind != ValidationErrorUnknownTool {
		t.Fatalf("nil registry: expected unknown_tool, got %v", verr)
	}
}

// --- check ordering: availability checked before size + decode ----------

func TestValidator_Validate_DisabledChecked_BeforeSizeOrDecode(t *testing.T) {
	r := NewRegistry()
	commitFn := func(_ HandlerInput) (sim.Command, error) { return sim.Command{}, nil }
	// Decoder that always fails — would fire if we got that far.
	failingDecode := func(_ json.RawMessage) (any, error) { return nil, errors.New("decode should not run") }
	if err := r.RegisterCommit("speak", schemaBytes(t, `{}`), failingDecode, commitFn, true, WithAvailability(AvailabilityDisabled)); err != nil {
		t.Fatalf("register: %v", err)
	}
	v := NewValidator(r)

	// Args that would also exceed the size cap if we reached that check.
	oversize := json.RawMessage(`"` + strings.Repeat("A", DefaultMaxArgsBytes+1) + `"`)
	_, verr := v.Validate(llm.RawToolCall{Name: "speak", Arguments: oversize})
	if verr == nil || verr.Kind != ValidationErrorToolUnavailable {
		t.Fatalf("disabled-tool check should win even with oversize args; got %v", verr)
	}
}
