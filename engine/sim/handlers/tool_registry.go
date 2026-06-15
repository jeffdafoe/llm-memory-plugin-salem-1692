package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// RegistryEntry is the registered metadata + handler for one tool. Build
// only via the typed constructors on Registry (RegisterObservation,
// RegisterCommit, RegisterTerminal) — the per-class handler fields are
// unexported so callers can't escape the constructor's class/policy
// invariants by writing a struct literal.
//
// Field invariants (enforced at registration):
//
//   - Name is non-empty and unique within the registry.
//   - Class is exactly one of ClassObservation / ClassCommit / ClassTerminal.
//   - TerminalPolicy matches Class:
//     ClassObservation → TerminalNever
//     ClassCommit      → TerminalNever or TerminalOnSuccess
//     ClassTerminal    → TerminalAlways
//   - Schema is non-empty (terminal tools get a default empty-object
//     schema).
//   - Decode is non-nil for ClassObservation and ClassCommit; for
//     ClassTerminal a strict default decoder is installed that accepts
//     only empty arg objects.
//   - Exactly one of observation/commit is non-nil for ClassObservation /
//     ClassCommit respectively; ClassTerminal has neither (the harness
//     handles `done` semantics directly).
type RegistryEntry struct {
	Name           string
	Description    string
	Class          ToolClass
	TerminalPolicy TerminalPolicy
	Availability   ToolAvailability

	// Schema is the JSON schema bytes shipped to the provider in
	// llm.ToolSpec.Schema. Opaque to the Client; validation lives in
	// Decode. Non-nil after registration.
	Schema json.RawMessage

	// Decode turns the raw JSON arguments from RawToolCall.Arguments
	// into a typed value. Implementations should use json.Decoder with
	// DisallowUnknownFields plus manual enum/range/required checks.
	Decode func(json.RawMessage) (any, error)

	// observation is the handler for ClassObservation entries; nil
	// otherwise. Access via the Observation accessor.
	observation ObservationFn

	// commit is the handler for ClassCommit entries; nil otherwise.
	// Access via the Commit accessor.
	commit CommitFn
}

// Observation returns the ObservationFn for this entry. Nil unless
// Class == ClassObservation. The harness must check Class before
// dispatching; calling this for a non-observation entry is a logic bug.
func (e *RegistryEntry) Observation() ObservationFn {
	if e == nil {
		return nil
	}
	return e.observation
}

// Commit returns the CommitFn for this entry. Nil unless Class ==
// ClassCommit. The harness must check Class before dispatching.
func (e *RegistryEntry) Commit() CommitFn {
	if e == nil {
		return nil
	}
	return e.commit
}

// Registry holds the tool entries the harness can dispatch. Build with
// NewRegistry; populate with the typed registration methods; query with
// Lookup and AdvertisedSpecs.
//
// Concurrency: the registry is a populate-once-then-read-many structure.
// All Register calls should happen at startup (single-threaded); Lookup
// and AdvertisedSpecs are safe to call concurrently after that.
// Concurrent Register + Lookup is NOT supported and not defensively
// guarded.
type Registry struct {
	// entries holds the actual entries in insertion order. AdvertisedSpecs
	// walks this for deterministic spec order — which matters for
	// provider prompt cache stability (a different order is a different
	// system prompt).
	entries []*RegistryEntry

	// byName indexes entries by Name for O(1) Lookup.
	byName map[string]int
}

// NewRegistry returns an empty registry ready for registration calls.
func NewRegistry() *Registry {
	return &Registry{
		byName: make(map[string]int),
	}
}

// RegisterOption customizes a registration call. Construct with the
// With* helpers; the constructor applies them after setting the
// class-mandatory defaults.
type RegisterOption func(*RegistryEntry)

// WithAvailability overrides the default AvailabilityAvailable. Use
// AvailabilityDisabled for tools that are in the registry but not yet
// dispatchable (e.g. speak / pay in PR 3d, pending Phase 3 subsystems).
func WithAvailability(a ToolAvailability) RegisterOption {
	return func(e *RegistryEntry) { e.Availability = a }
}

// WithDescription sets the entry's Description (advertised to the model
// in llm.ToolSpec.Description). Empty by default.
func WithDescription(d string) RegisterOption {
	return func(e *RegistryEntry) { e.Description = d }
}

// RegisterObservation adds a ClassObservation tool. TerminalPolicy is
// always TerminalNever — observations never end the tick on their own.
// Schema and decode are required; both must be non-nil/non-empty.
//
// Returns an error on:
//   - empty name
//   - duplicate name (already registered)
//   - nil schema / nil decode / nil fn
//
// The error is a configuration bug — registration is single-threaded
// startup code; the caller should panic or exit on failure.
func (r *Registry) RegisterObservation(name string, schema json.RawMessage, decode func(json.RawMessage) (any, error), fn ObservationFn, opts ...RegisterOption) error {
	if fn == nil {
		return fmt.Errorf("RegisterObservation(%q): nil fn", name)
	}
	e := &RegistryEntry{
		Name:           name,
		Class:          ClassObservation,
		TerminalPolicy: TerminalNever,
		Availability:   AvailabilityAvailable,
		Schema:         schema,
		Decode:         decode,
		observation:    fn,
	}
	for _, opt := range opts {
		opt(e)
	}
	return r.add(e)
}

// RegisterCommit adds a ClassCommit tool. terminalOnSuccess selects
// TerminalOnSuccess (true) or TerminalNever (false). Schema and decode
// are required.
//
// The fn returns a sim.Command — the harness submits via
// sim.RunTickToolCommand on the world goroutine. The fn does NOT touch
// the world.
func (r *Registry) RegisterCommit(name string, schema json.RawMessage, decode func(json.RawMessage) (any, error), fn CommitFn, terminalOnSuccess bool, opts ...RegisterOption) error {
	if fn == nil {
		return fmt.Errorf("RegisterCommit(%q): nil fn", name)
	}
	policy := TerminalNever
	if terminalOnSuccess {
		policy = TerminalOnSuccess
	}
	e := &RegistryEntry{
		Name:           name,
		Class:          ClassCommit,
		TerminalPolicy: policy,
		Availability:   AvailabilityAvailable,
		Schema:         schema,
		Decode:         decode,
		commit:         fn,
	}
	for _, opt := range opts {
		opt(e)
	}
	return r.add(e)
}

// RegisterTerminal adds a ClassTerminal tool (e.g. `done`). No handler —
// the harness ends the tick when one is dispatched. Schema defaults to
// the empty-object schema; Decode defaults to a strict no-args decoder
// (rejects non-empty argument objects so the model can't smuggle data
// through `done`).
func (r *Registry) RegisterTerminal(name string, opts ...RegisterOption) error {
	e := &RegistryEntry{
		Name:           name,
		Class:          ClassTerminal,
		TerminalPolicy: TerminalAlways,
		Availability:   AvailabilityAvailable,
		Schema:         append(json.RawMessage(nil), emptyObjectSchema...),
		Decode:         strictNoArgsDecode,
	}
	for _, opt := range opts {
		opt(e)
	}
	return r.add(e)
}

// add is the shared invariant-checking insertion path. Runs AFTER any
// RegisterOption funcs have applied — so it catches options that
// (accidentally or maliciously) put the entry into a class/policy/handler
// state the typed constructor would not have built. RegistryEntry's
// fields are exported so callers CAN reach into the struct via an option;
// add() is the canonical guard against ending up with e.g. a ClassCommit
// entry that has no commit handler.
func (r *Registry) add(e *RegistryEntry) error {
	if e.Name == "" {
		return fmt.Errorf("registry: empty tool name")
	}
	if _, dup := r.byName[e.Name]; dup {
		return fmt.Errorf("registry: tool %q already registered", e.Name)
	}
	if len(e.Schema) == 0 {
		return fmt.Errorf("registry: tool %q has empty schema", e.Name)
	}
	// Schema bytes ship verbatim to the provider in ToolSpec.Schema.
	// Catch malformed JSON at registration so a typo is a startup error,
	// not a runtime provider rejection mid-tick.
	if !json.Valid(e.Schema) {
		return fmt.Errorf("registry: tool %q has invalid JSON schema", e.Name)
	}
	if e.Decode == nil {
		return fmt.Errorf("registry: tool %q has nil Decode", e.Name)
	}

	// Availability must be one of the legal values.
	switch e.Availability {
	case AvailabilityAvailable, AvailabilityDisabled:
	default:
		return fmt.Errorf("registry: tool %q has invalid availability %v", e.Name, e.Availability)
	}

	// Class / TerminalPolicy / handler-population must form a consistent
	// triple. The typed constructors build consistent triples; an option
	// that reaches in and mutates one of the three could break that.
	switch e.Class {
	case ClassObservation:
		if e.TerminalPolicy != TerminalNever {
			return fmt.Errorf("registry: observation tool %q must have TerminalPolicy=Never, got %v", e.Name, e.TerminalPolicy)
		}
		if e.observation == nil {
			return fmt.Errorf("registry: observation tool %q has nil observation handler", e.Name)
		}
		if e.commit != nil {
			return fmt.Errorf("registry: observation tool %q has a commit handler set", e.Name)
		}
	case ClassCommit:
		if e.TerminalPolicy != TerminalNever && e.TerminalPolicy != TerminalOnSuccess {
			return fmt.Errorf("registry: commit tool %q must have TerminalPolicy=Never or OnSuccess, got %v", e.Name, e.TerminalPolicy)
		}
		if e.commit == nil {
			return fmt.Errorf("registry: commit tool %q has nil commit handler", e.Name)
		}
		if e.observation != nil {
			return fmt.Errorf("registry: commit tool %q has an observation handler set", e.Name)
		}
	case ClassTerminal:
		if e.TerminalPolicy != TerminalAlways {
			return fmt.Errorf("registry: terminal tool %q must have TerminalPolicy=Always, got %v", e.Name, e.TerminalPolicy)
		}
		if e.observation != nil || e.commit != nil {
			return fmt.Errorf("registry: terminal tool %q must not have any handlers", e.Name)
		}
	default:
		return fmt.Errorf("registry: tool %q has invalid class %v", e.Name, e.Class)
	}

	r.byName[e.Name] = len(r.entries)
	r.entries = append(r.entries, e)
	return nil
}

// Lookup returns the entry for the given tool name, or (nil, false) if
// no entry exists. Safe for concurrent use after registration completes.
func (r *Registry) Lookup(name string) (*RegistryEntry, bool) {
	if r == nil {
		return nil, false
	}
	idx, ok := r.byName[name]
	if !ok {
		return nil, false
	}
	return r.entries[idx], true
}

// AdvertisedSpecs returns the []llm.ToolSpec the harness passes in
// Request.Tools — entries with AvailabilityAvailable, in registration
// order (for prompt cache stability). Disabled entries are omitted.
//
// Returns a fresh slice each call; callers may safely mutate the
// returned slice (but should not mutate the Schema bytes — those are
// shared with the registry entry and shipping different bytes per call
// would defeat caching).
func (r *Registry) AdvertisedSpecs() []llm.ToolSpec {
	if r == nil {
		return nil
	}
	out := make([]llm.ToolSpec, 0, len(r.entries))
	for _, e := range r.entries {
		if e.Availability != AvailabilityAvailable {
			continue
		}
		out = append(out, llm.ToolSpec{
			Name:        e.Name,
			Description: e.Description,
			Schema:      e.Schema,
		})
	}
	return out
}

// Len returns the total number of registered entries (advertised or not).
// Useful for tests and telemetry.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.entries)
}

// strictNoArgsDecode is the default Decode for ClassTerminal entries.
// Accepts empty/missing arguments and the literal "{}"; rejects anything
// else (so the model can't smuggle structured data through `done`).
func strictNoArgsDecode(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) || bytes.Equal(trimmed, []byte("null")) {
		return struct{}{}, nil
	}
	// Don't echo the raw args back: arbitrary tool-call argument text can carry
	// injection / secrets / large blobs, and there's no field-level cap here.
	// Keep the correction structural — tell the model how to call it right.
	return nil, modelSafef("terminal tool takes no arguments; pass {} or null")
}
