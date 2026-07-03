package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

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
		// Decode errors split three ways. Hand-authored validation failures
		// (missing required field, out-of-range bound, length cap) are
		// model-safe by construction — they echo only the model's own
		// arguments — so the decoder tags them modelSafeError and we
		// surface the reason verbatim, letting a weak model self-correct
		// instead of looping on an opaque "decode failed" (ZBBS-WORK-413).
		// Two raw encoding/json shapes — an unknown field and a type
		// mismatch — are also structurally safe to explain (LLM-221):
		// decodeReasonForModel rebuilds them from the model's own field
		// name and fixed structural descriptors, never the offending value.
		// Every other raw encoding/json failure (syntax error, truncated
		// JSON) stays generic because its text can quote arbitrary input.
		// Cause carries the detail for logs either way. Mirrors the command
		// layer's sim.ModelFacingError handling in the harness dispatch path.
		msg := "argument decode failed"
		var safe modelSafeError
		if errors.As(err, &safe) {
			msg = safe.Error()
		} else if reason, ok := decodeReasonForModel(call.Name, err); ok {
			msg = reason
		}
		return nil, &ValidationError{
			Kind:    ValidationErrorMalformedArgs,
			Tool:    call.Name,
			Message: msg,
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

// modelSafeError marks a validation failure whose message is safe to show
// the model verbatim. Two layers return it (via modelSafef):
//   - decoders, for hand-authored argument checks (required field, min/max
//     bound, length cap, structural shape); Validate surfaces the reason as
//     the malformed_args message.
//   - commit/observation handlers, for post-decode static checks (empty
//     after trim, control character, duplicate name); the harness dispatch
//     surfaces the reason instead of the generic handler_failed.
//
// Either way the message only ever echoes the model's own arguments — never
// internal state, file paths, or secrets — so a weak model can self-correct.
// Every OTHER error stays generic: a raw encoding/json failure (which can
// quote arbitrary input) or a genuinely internal handler error (a search,
// API, or registration failure). Fail-closed — only an explicit
// modelSafeError surfaces. This is the handlers-package analogue of
// sim.ModelFacingError, which does the same for world-command rejections.
type modelSafeError struct {
	msg string
}

func (e modelSafeError) Error() string {
	return e.msg
}

// modelSafef builds a model-safe error (see modelSafeError) — a tool-error
// reason the model is allowed to read. Use it for hand-authored argument and
// post-decode static-validation checks; NEVER use it to wrap a
// json.Decode/Unmarshal error or any internal failure — those must stay
// generic, so keep them on fmt.Errorf("...: %w", err) (or a plain error the
// dispatch genericizes).
func modelSafef(format string, a ...any) error {
	return modelSafeError{msg: fmt.Sprintf(format, a...)}
}

// unknownFieldPrefix is the stable leading text of encoding/json's
// DisallowUnknownFields error, whose full form is `json: unknown field "x"`.
// encoding/json exposes no typed error for this case, so we match on the
// prefix; the only token we echo back is the field name the model itself
// supplied — the same safety class as the hand-authored decoder messages.
const unknownFieldPrefix = `json: unknown field "`

// jsonKindWords are the fixed value-kind tokens encoding/json places at the
// front of an UnmarshalTypeError.Value. The number path appends the offending
// literal (e.g. "number 3.5"), so we only ever surface this leading token —
// never the remainder, which would echo model-supplied input into the
// transcript and defeat the fail-closed policy.
var jsonKindWords = map[string]bool{
	"array":  true,
	"object": true,
	"bool":   true,
	"string": true,
	"number": true,
	"null":   true,
}

// decodeReasonForModel classifies the two structurally-safe encoding/json
// decode failures — a DisallowUnknownFields violation and a type mismatch —
// and renders a model-safe reason so a weak model can self-correct instead of
// looping on the opaque "argument decode failed" (LLM-221). It returns
// ok=false for every other error (syntax errors, truncated JSON, internal
// failures), which must stay generic because their text can quote arbitrary
// input. The rendered message echoes only the model's own field name and
// fixed structural descriptors — never an offending value.
func decodeReasonForModel(tool string, err error) (string, bool) {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		got := ""
		// UnmarshalTypeError.Value is a fixed kind word except on the number
		// path, where it is "number <literal>"; surface only the leading
		// token, and only if it is a known kind, so the value never leaks.
		if parts := strings.Fields(typeErr.Value); len(parts) > 0 && jsonKindWords[parts[0]] {
			got = ", got " + parts[0]
		}
		return fmt.Sprintf("%s: field %q must be %s%s", tool, typeErr.Field, jsonTypeExpectation(typeErr.Type), got), true
	}
	// encoding/json exposes no typed error for a DisallowUnknownFields
	// violation, so we match its stable leaf message. Unwrap to the leaf first
	// — the decoders wrap it with %w, so the prefix sits mid-string on the
	// wrapped error — then require a prefix match, not a substring: a
	// substring match would misfire on wrapper text that merely contains it.
	leaf := err
	for {
		next := errors.Unwrap(leaf)
		if next == nil {
			break
		}
		leaf = next
	}
	if s := leaf.Error(); strings.HasPrefix(s, unknownFieldPrefix) {
		rest := strings.TrimPrefix(s, unknownFieldPrefix)
		// Require the closing quote to end the string, matching the exact
		// stdlib shape `json: unknown field "x"`. A trailing remainder means
		// the leaf isn't that message — stay generic rather than mis-parse.
		if field, tail, ok := strings.Cut(rest, `"`); ok && tail == "" {
			// The key is model-supplied. Only echo it if it is a short, plain
			// identifier; otherwise fall back to generic. This keeps the real
			// self-correction cases (message, consume_now, …) while refusing
			// spaces, quotes, control characters, or prompt-like content into
			// the transcript.
			if safe, ok := safeModelFieldName(field); ok {
				return fmt.Sprintf("%s: unknown field %q", tool, safe), true
			}
		}
	}
	return "", false
}

// safeModelFieldName accepts a model-supplied JSON key for echoing back only
// when it is a short, plain identifier — ASCII letters, digits, and _-. — with
// no spaces, quotes, or control characters. Anything else returns ok=false so
// the caller stays generic. This bounds the unknown-field message to benign,
// self-correction-useful content and refuses arbitrary transcript injection.
func safeModelFieldName(field string) (string, bool) {
	if field == "" || len(field) > 64 {
		return "", false
	}
	for _, r := range field {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return "", false
		}
	}
	return field, true
}

// jsonTypeExpectation maps the Go type a decoder expected to a plain-language
// description the model can act on, avoiding Go-specific type syntax. Pointer
// types are unwrapped to their element so an optional field still reads as its
// underlying kind.
func jsonTypeExpectation(t reflect.Type) string {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return "a different type"
	}
	switch t.Kind() {
	case reflect.Bool:
		return "a boolean (true or false)"
	case reflect.String:
		return "a string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		// Distinguished from float so a fractional or over-range value —
		// which json reports as Value "number" too — reads as "must be a
		// whole number, got number" rather than the tautological "a number".
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Map, reflect.Struct:
		return "an object"
	default:
		return "a different type"
	}
}
