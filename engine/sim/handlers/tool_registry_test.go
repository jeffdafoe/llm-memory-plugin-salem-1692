package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// schemaBytes is a tiny helper to keep test setup readable; real tools
// will ship the bytes baked into their registration callsite.
func schemaBytes(t *testing.T, s string) json.RawMessage {
	t.Helper()
	if !json.Valid([]byte(s)) {
		t.Fatalf("test bug: schema %q is not valid JSON", s)
	}
	return json.RawMessage(s)
}

// trivialDecode passes the raw bytes through unchanged — useful when a
// test cares about the registration shape, not the decoder behavior.
func trivialDecode(raw json.RawMessage) (any, error) { return string(raw), nil }

func TestRegistry_RegisterObservation_Happy(t *testing.T) {
	r := NewRegistry()
	fn := func(_ context.Context, _ HandlerInput) (string, error) { return "ok", nil }

	if err := r.RegisterObservation("recall", schemaBytes(t, `{"type":"object"}`), trivialDecode, fn); err != nil {
		t.Fatalf("RegisterObservation: %v", err)
	}

	entry, ok := r.Lookup("recall")
	if !ok {
		t.Fatal("Lookup(recall): not found")
	}
	if entry.Class != ClassObservation {
		t.Errorf("Class: got %s, want observation", entry.Class)
	}
	if entry.TerminalPolicy != TerminalNever {
		t.Errorf("TerminalPolicy: got %s, want never", entry.TerminalPolicy)
	}
	if entry.Availability != AvailabilityAvailable {
		t.Errorf("Availability: got %s, want available", entry.Availability)
	}
	if entry.Observation() == nil {
		t.Errorf("Observation accessor returned nil")
	}
	if entry.Commit() != nil {
		t.Errorf("Commit accessor returned non-nil for observation class")
	}
}

func TestRegistry_RegisterCommit_TerminalOnSuccess(t *testing.T) {
	r := NewRegistry()
	fn := func(_ HandlerInput) (sim.Command, error) { return sim.Command{}, nil }

	if err := r.RegisterCommit("move_to", schemaBytes(t, `{"type":"object"}`), trivialDecode, fn, true); err != nil {
		t.Fatalf("RegisterCommit: %v", err)
	}
	entry, _ := r.Lookup("move_to")
	if entry.Class != ClassCommit {
		t.Errorf("Class: got %s, want commit", entry.Class)
	}
	if entry.TerminalPolicy != TerminalOnSuccess {
		t.Errorf("TerminalPolicy: got %s, want on_success", entry.TerminalPolicy)
	}
	if entry.Commit() == nil {
		t.Errorf("Commit accessor returned nil")
	}
	if entry.Observation() != nil {
		t.Errorf("Observation accessor returned non-nil for commit class")
	}
}

func TestRegistry_RegisterCommit_NeverTerminal(t *testing.T) {
	r := NewRegistry()
	fn := func(_ HandlerInput) (sim.Command, error) { return sim.Command{}, nil }

	if err := r.RegisterCommit("note", schemaBytes(t, `{"type":"object"}`), trivialDecode, fn, false); err != nil {
		t.Fatalf("RegisterCommit: %v", err)
	}
	entry, _ := r.Lookup("note")
	if entry.TerminalPolicy != TerminalNever {
		t.Errorf("TerminalPolicy: got %s, want never (terminalOnSuccess=false)", entry.TerminalPolicy)
	}
}

func TestRegistry_RegisterTerminal_DefaultsSchemaAndDecode(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("RegisterTerminal: %v", err)
	}
	entry, _ := r.Lookup("done")
	if entry.Class != ClassTerminal {
		t.Errorf("Class: got %s, want terminal", entry.Class)
	}
	if entry.TerminalPolicy != TerminalAlways {
		t.Errorf("TerminalPolicy: got %s, want always", entry.TerminalPolicy)
	}
	if len(entry.Schema) == 0 {
		t.Errorf("Schema should be the empty-object default, got empty")
	}
	if !json.Valid(entry.Schema) {
		t.Errorf("default Schema is not valid JSON: %s", entry.Schema)
	}
	if entry.Decode == nil {
		t.Errorf("Decode should be the strict no-args default, got nil")
	}
	if entry.Observation() != nil || entry.Commit() != nil {
		t.Errorf("Terminal entry should have no handlers")
	}
}

func TestRegistry_RejectDuplicate(t *testing.T) {
	r := NewRegistry()
	fn := func(_ context.Context, _ HandlerInput) (string, error) { return "", nil }
	if err := r.RegisterObservation("recall", schemaBytes(t, `{}`), trivialDecode, fn); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := r.RegisterObservation("recall", schemaBytes(t, `{}`), trivialDecode, fn)
	if err == nil {
		t.Fatalf("duplicate register: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("duplicate error message: got %q", err.Error())
	}
}

func TestRegistry_RejectEmptyName(t *testing.T) {
	r := NewRegistry()
	fn := func(_ context.Context, _ HandlerInput) (string, error) { return "", nil }
	if err := r.RegisterObservation("", schemaBytes(t, `{}`), trivialDecode, fn); err == nil {
		t.Errorf("RegisterObservation(\"\"): expected error, got nil")
	}
}

func TestRegistry_RejectNilFns(t *testing.T) {
	r := NewRegistry()
	if err := r.RegisterObservation("recall", schemaBytes(t, `{}`), trivialDecode, nil); err == nil {
		t.Errorf("RegisterObservation nil fn: expected error")
	}
	if err := r.RegisterCommit("move", schemaBytes(t, `{}`), trivialDecode, nil, true); err == nil {
		t.Errorf("RegisterCommit nil fn: expected error")
	}
}

func TestRegistry_RejectNilSchemaAndDecode(t *testing.T) {
	r := NewRegistry()
	fn := func(_ context.Context, _ HandlerInput) (string, error) { return "", nil }

	if err := r.RegisterObservation("a", nil, trivialDecode, fn); err == nil {
		t.Errorf("nil schema: expected error")
	}
	if err := r.RegisterObservation("b", schemaBytes(t, `{}`), nil, fn); err == nil {
		t.Errorf("nil decode: expected error")
	}
}

// R3 regression: RegisterOption funcs can reach exported RegistryEntry
// fields. Validate that add()'s post-options invariant check catches an
// option that mutates Class away from what the typed constructor set,
// leaving a no-handler state for the new class.
func TestRegistry_R3_OptionEscapeHatchCaughtByInvariantCheck(t *testing.T) {
	r := NewRegistry()
	obsFn := func(_ context.Context, _ HandlerInput) (string, error) { return "", nil }
	hostile := func(e *RegistryEntry) {
		// An option that flips the class to commit. The observation handler
		// stays set; the commit handler does NOT — add() must reject.
		e.Class = ClassCommit
		e.TerminalPolicy = TerminalOnSuccess
	}
	err := r.RegisterObservation("trick", schemaBytes(t, `{}`), trivialDecode, obsFn, hostile)
	if err == nil {
		t.Fatalf("expected invariant-check failure on class-mutating option, got nil")
	}
	if !strings.Contains(err.Error(), "commit tool") {
		t.Errorf("error message: got %q, want it to mention commit-tool invariant", err.Error())
	}
}

// R3 regression: invariant check also covers TerminalPolicy mismatch
// for an observation tool.
func TestRegistry_R3_ObservationTerminalPolicyMismatchRejected(t *testing.T) {
	r := NewRegistry()
	obsFn := func(_ context.Context, _ HandlerInput) (string, error) { return "", nil }
	hostile := func(e *RegistryEntry) {
		e.TerminalPolicy = TerminalAlways // observations must be Never
	}
	err := r.RegisterObservation("trick", schemaBytes(t, `{}`), trivialDecode, obsFn, hostile)
	if err == nil {
		t.Fatalf("expected TerminalPolicy mismatch rejection, got nil")
	}
}

// R3 regression: invariant check catches an option that nulls out the
// commit handler for a ClassCommit entry.
func TestRegistry_R3_CommitNilHandlerRejected(t *testing.T) {
	r := NewRegistry()
	commitFn := func(_ HandlerInput) (sim.Command, error) { return sim.Command{}, nil }
	hostile := func(e *RegistryEntry) { e.Class = ClassObservation }
	err := r.RegisterCommit("trick", schemaBytes(t, `{}`), trivialDecode, commitFn, true, hostile)
	if err == nil {
		t.Fatalf("expected invariant failure after option mutated class, got nil")
	}
}

// R3 regression: invariant check catches an invalid Availability value.
func TestRegistry_R3_InvalidAvailabilityRejected(t *testing.T) {
	r := NewRegistry()
	obsFn := func(_ context.Context, _ HandlerInput) (string, error) { return "", nil }
	hostile := func(e *RegistryEntry) { e.Availability = ToolAvailability(99) }
	err := r.RegisterObservation("trick", schemaBytes(t, `{}`), trivialDecode, obsFn, hostile)
	if err == nil {
		t.Fatalf("expected invalid-availability rejection, got nil")
	}
}

// R2 regression: schema bytes must be valid JSON at registration time,
// not deferred to a runtime provider rejection.
func TestRegistry_R2_RejectInvalidJSONSchema(t *testing.T) {
	r := NewRegistry()
	fn := func(_ context.Context, _ HandlerInput) (string, error) { return "", nil }
	bad := json.RawMessage(`{not json`)
	err := r.RegisterObservation("recall", bad, trivialDecode, fn)
	if err == nil {
		t.Fatalf("invalid JSON schema: expected registration error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid JSON schema") {
		t.Errorf("error message: got %q, want it to mention invalid JSON schema", err.Error())
	}
}

func TestRegistry_WithAvailability(t *testing.T) {
	r := NewRegistry()
	fn := func(_ HandlerInput) (sim.Command, error) { return sim.Command{}, nil }

	if err := r.RegisterCommit("speak", schemaBytes(t, `{}`), trivialDecode, fn, true, WithAvailability(AvailabilityDisabled)); err != nil {
		t.Fatalf("RegisterCommit disabled: %v", err)
	}
	entry, _ := r.Lookup("speak")
	if entry.Availability != AvailabilityDisabled {
		t.Errorf("Availability: got %s, want disabled", entry.Availability)
	}
}

func TestRegistry_WithDescription(t *testing.T) {
	r := NewRegistry()
	fn := func(_ context.Context, _ HandlerInput) (string, error) { return "", nil }

	want := "Recall a memory by query."
	if err := r.RegisterObservation("recall", schemaBytes(t, `{}`), trivialDecode, fn, WithDescription(want)); err != nil {
		t.Fatalf("RegisterObservation: %v", err)
	}
	entry, _ := r.Lookup("recall")
	if entry.Description != want {
		t.Errorf("Description: got %q, want %q", entry.Description, want)
	}
}

func TestRegistry_Lookup_NotFound(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Lookup("nope"); ok {
		t.Errorf("Lookup(nope) on empty registry: expected ok=false")
	}
}

func TestRegistry_Lookup_NilSafe(t *testing.T) {
	var r *Registry
	if _, ok := r.Lookup("anything"); ok {
		t.Errorf("Lookup on nil registry: expected ok=false")
	}
}

func TestRegistry_AdvertisedSpecs_FilterAndOrder(t *testing.T) {
	r := NewRegistry()
	obsFn := func(_ context.Context, _ HandlerInput) (string, error) { return "", nil }
	commitFn := func(_ HandlerInput) (sim.Command, error) { return sim.Command{}, nil }

	mustReg := func(name string, opts ...RegisterOption) {
		t.Helper()
		if err := r.RegisterObservation(name, schemaBytes(t, `{"type":"object"}`), trivialDecode, obsFn, opts...); err != nil {
			t.Fatalf("register %q: %v", name, err)
		}
	}
	mustReg("recall")
	mustReg("disabled_tool", WithAvailability(AvailabilityDisabled))
	if err := r.RegisterCommit("move_to", schemaBytes(t, `{"type":"object"}`), trivialDecode, commitFn, true, WithDescription("move there")); err != nil {
		t.Fatalf("register move_to: %v", err)
	}
	if err := r.RegisterTerminal("done", WithDescription("end turn")); err != nil {
		t.Fatalf("register done: %v", err)
	}

	specs := r.AdvertisedSpecs()
	// Expect: recall, move_to, done (in registration order). disabled_tool omitted.
	wantNames := []string{"recall", "move_to", "done"}
	if len(specs) != len(wantNames) {
		t.Fatalf("AdvertisedSpecs len: got %d, want %d (%v)", len(specs), len(wantNames), specs)
	}
	for i, spec := range specs {
		if spec.Name != wantNames[i] {
			t.Errorf("specs[%d].Name: got %q, want %q", i, spec.Name, wantNames[i])
		}
	}
	if specs[1].Description != "move there" {
		t.Errorf("move_to Description: got %q, want \"move there\"", specs[1].Description)
	}
}

func TestRegistry_AdvertisedSpecs_NilSafe(t *testing.T) {
	var r *Registry
	if got := r.AdvertisedSpecs(); got != nil {
		t.Errorf("AdvertisedSpecs on nil registry: got %v, want nil", got)
	}
}

func TestRegistry_AdvertisedSpecs_EmptyRegistry(t *testing.T) {
	r := NewRegistry()
	if got := r.AdvertisedSpecs(); len(got) != 0 {
		t.Errorf("empty registry AdvertisedSpecs: got %v, want empty", got)
	}
}

func TestRegistry_Len(t *testing.T) {
	r := NewRegistry()
	if r.Len() != 0 {
		t.Errorf("empty Len: got %d, want 0", r.Len())
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if r.Len() != 1 {
		t.Errorf("after register Len: got %d, want 1", r.Len())
	}
	var nilReg *Registry
	if nilReg.Len() != 0 {
		t.Errorf("nil registry Len: got %d, want 0", nilReg.Len())
	}
}

func TestRegistryEntry_Accessors_NilSafe(t *testing.T) {
	var e *RegistryEntry
	if e.Observation() != nil {
		t.Errorf("nil entry Observation: got non-nil")
	}
	if e.Commit() != nil {
		t.Errorf("nil entry Commit: got non-nil")
	}
}

func TestStrictNoArgsDecode_Accepts(t *testing.T) {
	cases := []string{
		"",
		"{}",
		" {} ",
		"\t{}\n",
		"null",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			got, err := strictNoArgsDecode(json.RawMessage(in))
			if err != nil {
				t.Errorf("strictNoArgsDecode(%q): unexpected err %v", in, err)
			}
			if _, ok := got.(struct{}); !ok {
				t.Errorf("strictNoArgsDecode(%q): got %T, want struct{}", in, got)
			}
		})
	}
}

func TestStrictNoArgsDecode_RejectsNonEmpty(t *testing.T) {
	cases := []string{
		`{"x":1}`,
		`{"foo":"bar"}`,
		`[]`,
		`"string"`,
		`42`,
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			_, err := strictNoArgsDecode(json.RawMessage(in))
			if err == nil {
				t.Errorf("strictNoArgsDecode(%q): expected error, got nil", in)
			}
			if !strings.Contains(err.Error(), "takes no arguments") {
				t.Errorf("err message: got %q", err.Error())
			}
		})
	}
}

func TestEmptyObjectSchema_IsValidJSON(t *testing.T) {
	if !json.Valid(emptyObjectSchema) {
		t.Errorf("emptyObjectSchema not valid JSON: %s", emptyObjectSchema)
	}
}

func TestToolClass_String(t *testing.T) {
	cases := map[ToolClass]string{
		ClassObservation: "observation",
		ClassCommit:      "commit",
		ClassTerminal:    "terminal",
		ClassUnknown:     "unknown",
		ToolClass(999):   "unknown",
	}
	for c, want := range cases {
		if got := c.String(); got != want {
			t.Errorf("%d.String(): got %q, want %q", c, got, want)
		}
	}
}

func TestTerminalPolicy_String(t *testing.T) {
	cases := map[TerminalPolicy]string{
		TerminalNever:      "never",
		TerminalAlways:     "always",
		TerminalOnSuccess:  "on_success",
		TerminalPolicy(99): "unknown",
	}
	for p, want := range cases {
		if got := p.String(); got != want {
			t.Errorf("%d.String(): got %q, want %q", p, got, want)
		}
	}
}

func TestToolAvailability_String(t *testing.T) {
	cases := map[ToolAvailability]string{
		AvailabilityAvailable: "available",
		AvailabilityDisabled:  "disabled",
		ToolAvailability(99):  "unknown",
	}
	for a, want := range cases {
		if got := a.String(); got != want {
			t.Errorf("%d.String(): got %q, want %q", a, got, want)
		}
	}
}

func TestValidationError_FormatAndUnwrap(t *testing.T) {
	cause := errors.New("bad json")
	e := &ValidationError{
		Kind:    ValidationErrorMalformedArgs,
		Tool:    "move_to",
		Message: "decode failed",
		Cause:   cause,
	}
	got := e.Error()
	for _, want := range []string{"malformed_args", "move_to", "decode failed", "bad json"} {
		if !strings.Contains(got, want) {
			t.Errorf("ValidationError.Error() should contain %q; got %q", want, got)
		}
	}
	if !errors.Is(e, cause) {
		t.Errorf("errors.Is should walk Unwrap to cause")
	}
	// Nil safety.
	var nilE *ValidationError
	if nilE.Error() != "" {
		t.Errorf("nil ValidationError.Error(): got %q, want empty", nilE.Error())
	}
	if nilE.Unwrap() != nil {
		t.Errorf("nil ValidationError.Unwrap(): got non-nil")
	}
}

func TestValidationErrorKind_String(t *testing.T) {
	cases := map[ValidationErrorKind]string{
		ValidationErrorUnknownTool:     "unknown_tool",
		ValidationErrorToolUnavailable: "tool_unavailable_in_this_build",
		ValidationErrorArgsTooLarge:    "args_too_large",
		ValidationErrorMalformedArgs:   "malformed_args",
		ValidationErrorKind(0):         "unknown",
		ValidationErrorKind(999):       "unknown",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("%d.String(): got %q, want %q", k, got, want)
		}
	}
}
