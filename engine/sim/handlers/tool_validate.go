package handlers

import (
	"fmt"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// DefaultMaxArgsBytes is the per-call arg-size cap when Validator's
// MaxArgsBytes is unset (zero). Sized to comfortably hold the realistic
// args shapes for PR 3d tools — a move_to with a long destination name,
// a recall with several query terms — while still rejecting payloads
// that would only make sense as an injection or runaway-output attempt.
const DefaultMaxArgsBytes = 4096

// Validator turns a raw llm.RawToolCall into a *ValidatedCall ready for
// the harness to dispatch, or a *ValidationError the harness surfaces
// as a typed "tool" message to the model (non-terminal per §5 invariant
// 4 — the budget still ticks and the model may retry next iteration).
//
// The Validator owns the middle stage of the 3-stage parse/validate
// ownership (per design note §5):
//   - LLM client decodes provider format; treats Arguments as opaque.
//   - Validator (this) — unknown/disabled tool detection, arg-size cap,
//     typed Decode.
//   - Harness — loop policy, routing, terminal evaluation,
//     CompleteReactorTick.
//
// Concurrency: safe for concurrent Validate calls (the underlying
// Registry is populate-once-then-read).
type Validator struct {
	// Registry is the source of truth for tool entries. Required.
	Registry *Registry

	// MaxArgsBytes is the per-call args byte cap. Zero means "use
	// DefaultMaxArgsBytes". Negative or zero values fall back to the
	// default; the field is never used to mean "no cap" — a cap is
	// mandatory under the multi-call execution invariants (§5).
	MaxArgsBytes int
}

// NewValidator returns a Validator bound to the given registry, using
// DefaultMaxArgsBytes for the arg-size cap.
func NewValidator(r *Registry) *Validator {
	return &Validator{Registry: r}
}

// Validate produces a ValidatedCall ready for harness dispatch, or a
// ValidationError describing why the call was rejected. Exactly one of
// the return values is non-nil.
//
// Order of checks (each is fatal — first failure wins):
//  1. Registry lookup by call.Name → UnknownTool
//  2. Entry.Availability == Available → ToolUnavailable
//  3. len(call.Arguments) <= cap → ArgsTooLarge
//  4. Entry.Decode(call.Arguments) → MalformedArgs (wraps Decode's err)
//
// On success the returned ValidatedCall carries everything the harness
// needs to dispatch: the entry, the typed decoded args, the opaque
// provider call ID, and the within-response index.
func (v *Validator) Validate(call llm.RawToolCall) (*ValidatedCall, *ValidationError) {
	if v == nil || v.Registry == nil {
		return nil, &ValidationError{
			Kind:    ValidationErrorUnknownTool,
			Tool:    call.Name,
			Message: "no registry configured",
		}
	}

	entry, ok := v.Registry.Lookup(call.Name)
	if !ok {
		return nil, &ValidationError{
			Kind:    ValidationErrorUnknownTool,
			Tool:    call.Name,
			Message: fmt.Sprintf("tool %q is not registered", call.Name),
		}
	}
	if entry.Availability != AvailabilityAvailable {
		return nil, &ValidationError{
			Kind:    ValidationErrorToolUnavailable,
			Tool:    call.Name,
			Message: fmt.Sprintf("tool %q is registered but not available in this build", call.Name),
		}
	}

	cap := v.MaxArgsBytes
	if cap <= 0 {
		cap = DefaultMaxArgsBytes
	}
	if len(call.Arguments) > cap {
		return nil, &ValidationError{
			Kind:    ValidationErrorArgsTooLarge,
			Tool:    call.Name,
			Message: fmt.Sprintf("arguments %d bytes exceed per-call cap %d", len(call.Arguments), cap),
		}
	}

	decoded, err := entry.Decode(call.Arguments)
	if err != nil {
		return nil, &ValidationError{
			Kind:    ValidationErrorMalformedArgs,
			Tool:    call.Name,
			Message: "argument decode failed",
			Cause:   err,
		}
	}

	return &ValidatedCall{
		Name:        call.Name,
		Entry:       entry,
		DecodedArgs: decoded,
		RawCallID:   call.ID,
		Index:       call.Index,
	}, nil
}
