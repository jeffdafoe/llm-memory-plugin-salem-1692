package handlers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ToolClass describes what a tool *does* — the harness uses it to route
// dispatch. Set at registration time by the typed constructor; never
// inferred from handler signatures (see RegisterObservation /
// RegisterCommit / RegisterTerminal).
type ToolClass int

const (
	// ClassUnknown is the zero value — registry construction always
	// overrides it. Reaching ClassUnknown at dispatch time is an
	// invariant breach (registration would have rejected it).
	ClassUnknown ToolClass = iota

	// ClassObservation — no world mutation (e.g. recall). The handler
	// returns model-visible content directly. TerminalPolicy is always
	// TerminalNever for this class.
	ClassObservation

	// ClassCommit — world mutation. The handler builds a sim.Command
	// which the harness submits via sim.RunTickToolCommand. The handler
	// never receives a *sim.World — separation enforced by the typed
	// constructor.
	ClassCommit

	// ClassTerminal — explicit done-class (the `done` tool). No handler;
	// the harness ends the tick when one is dispatched. TerminalPolicy is
	// always TerminalAlways.
	ClassTerminal
)

// String renders the class as a stable lowercase label — used in
// telemetry Detail, error messages, and debug output.
func (c ToolClass) String() string {
	switch c {
	case ClassObservation:
		return "observation"
	case ClassCommit:
		return "commit"
	case ClassTerminal:
		return "terminal"
	default:
		return "unknown"
	}
}

// TerminalPolicy says whether a successful dispatch ends the tick. The
// harness reads it after dispatch to decide whether to continue the
// within-tick iteration loop (§6 transcript model) or finalize via
// CompleteReactorTick.
type TerminalPolicy int

const (
	// TerminalNever — the call result NEVER ends the tick on its own.
	// Observation tools always carry this policy; Commit tools may.
	TerminalNever TerminalPolicy = iota

	// TerminalAlways — the call always ends the tick (Terminal class).
	TerminalAlways

	// TerminalOnSuccess — the call ends the tick iff the handler
	// succeeded. A failed Commit (e.g. move_to to an invalid destination)
	// returns a typed tool error and the loop continues — the model
	// learns its mistake within budget and may retry.
	TerminalOnSuccess
)

// String renders the policy as a stable lowercase label.
func (p TerminalPolicy) String() string {
	switch p {
	case TerminalNever:
		return "never"
	case TerminalAlways:
		return "always"
	case TerminalOnSuccess:
		return "on_success"
	default:
		return "unknown"
	}
}

// ToolAvailability gates whether the tool is advertised to the model. A
// Disabled tool stays in the registry — dispatch returns a typed
// `tool_unavailable_in_this_build` rather than `unknown_tool` — but is
// omitted from Request.Tools (so the model never sees it in its tool
// list).
type ToolAvailability int

const (
	// AvailabilityAvailable — the tool is advertised AND dispatchable.
	// This is the zero value; the default constructor produces Available
	// entries.
	AvailabilityAvailable ToolAvailability = iota

	// AvailabilityDisabled — the tool is in the registry but NOT
	// advertised; dispatches against it return tool_unavailable.
	AvailabilityDisabled
)

// String renders the availability as a stable lowercase label.
func (a ToolAvailability) String() string {
	switch a {
	case AvailabilityAvailable:
		return "available"
	case AvailabilityDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}

// HandlerInput carries the per-call context every handler needs. Built
// by the harness from the tickJob (actor / attempt / root) plus the
// ValidatedCall's DecodedArgs. Handlers MUST treat all fields as read-
// only; the harness reuses the input value across iterations within a
// tick when convenient.
type HandlerInput struct {
	ActorID     sim.ActorID
	AttemptID   sim.TickAttemptID
	RootEventID sim.EventID

	// LLMMemoryAgent is the acting actor's llm-memory namespace slug
	// (ActorSnapshot.LLMAgent), resolved by the harness from the tick's
	// snapshot. Empty for actors with no VA backing. Carried for
	// observation tools that read the actor's own memory — recall is the
	// first (ZBBS-WORK-321). Commit handlers that need actor state read it
	// on the world goroutine inside their sim.Command; this field exists
	// because observation handlers run OFF the world goroutine with no
	// world access, so "who am I" must be threaded in.
	LLMMemoryAgent string

	// Args is the typed value produced by RegistryEntry.Decode — already
	// validated, ready to consume. The handler should type-assert to its
	// per-tool args struct.
	Args any

	// PerceivedStructureIDs / PerceivedObjectIDs are the move targets this tick's
	// perception surfaced to the actor (ZBBS-HOME-389) — every structure_id /
	// object_id a vendor / rest / restock / anchor cue named. The move_to commit
	// resolves a structure_name against these (in addition to anchors + scene
	// radius) so the model can walk by NAME to any place it was actually shown,
	// distant or not. Empty for a tick with no move-target cues. Threaded from the
	// harness (off-world); the move_to Command consults them on the world goroutine.
	PerceivedStructureIDs []sim.StructureID
	PerceivedObjectIDs    []sim.VillageObjectID

	// RememberedPlaces is the actor's DURABLE known-places set (LLM-77) split by
	// kind — the places it has personally experienced across its life, threaded as
	// the move_to name-resolver's memory-backed FALLBACK source (LLM-78). Distinct
	// from Perceived* above (what THIS tick showed): the resolver tries Perceived*
	// first and falls back to these, so a live cue wins a name shared with a
	// remembered place. Empty when the actor knows no places. Threaded from the
	// harness off the published snapshot, like Perceived*; the move_to Command
	// consults it on the world goroutine (and re-validates liveness there).
	RememberedPlaces sim.RememberedPlaces

	// HasNewNews is the turn-state gate's new-news signal (ZBBS-WORK-370): true
	// when the tick's consumed warrant batch carries any fresh stimulus (a Force
	// warrant or any high-information kind), false when it is only low-info
	// liveness/idle warrants. The speak commit (sim.SpeakTo) reads it to exempt a
	// legitimate event-driven follow-up from the "you already spoke and are
	// awaiting a reply" backstop, suppressing only idle re-pitches. The harness
	// computes it once per tick from job.warrants via batchHasNewNews; non-speak
	// handlers ignore it.
	HasNewNews bool
}

// ObservationFn is the handler signature for ClassObservation tools. It
// runs OFF the world goroutine (worker pool context). Returns the
// content string that becomes the "tool" message content in the
// transcript, or an error (which the harness surfaces as a typed tool
// error — non-terminal per §5 invariant 4).
//
// ctx may carry deadlines (LLM call ctx) or be cancellation-only
// (shutdown). Handlers MUST honor ctx.Err() between I/O steps.
type ObservationFn func(ctx context.Context, in HandlerInput) (content string, err error)

// CommitFn is the handler signature for ClassCommit tools. It is a pure
// "build the command" function — the handler does NOT touch the world.
// It returns a sim.Command which the harness submits via
// sim.RunTickToolCommand (the attempt-guarded world-goroutine entry from
// PR 3b).
//
// An error here is a typed validation/business error — distinct from a
// command's runtime error from the world goroutine. The harness routes
// both into the transcript as tool errors per §5 invariant 4.
type CommitFn func(in HandlerInput) (sim.Command, error)

// ValidatedCall is what the validator produces — a registered, available,
// schema-passing tool call ready for the harness to dispatch. The harness
// has everything it needs to route, dispatch, and produce the matching
// "tool" message: the entry (for Class/TerminalPolicy/handler), the
// decoded args, and the call ID/index for transcript attribution.
type ValidatedCall struct {
	// Name is the tool name as the model emitted it (and the entry's
	// Name — they match, since the validator looked up the entry by name).
	Name string

	// Entry is the registry entry — gives the harness Class / TerminalPolicy
	// + the handler fns + everything else.
	Entry *RegistryEntry

	// DecodedArgs is the typed value returned by Entry.Decode. The handler
	// type-asserts to its per-tool args struct.
	DecodedArgs any

	// RawCallID is the opaque provider call ID (RawToolCall.ID). The harness
	// uses it as the ToolCallID on the "tool" message that returns this
	// call's result — that's how the provider attributes multi-call results
	// under native transcript continuation.
	RawCallID string

	// Index is the within-response position of the call (0-based). A
	// secondary disambiguator for missing/duplicate IDs.
	Index int
}

// ValidationErrorKind enumerates the validation failure modes the
// registry can report. Each kind corresponds to a distinct typed-tool-
// error message the harness surfaces to the model (so the model can
// learn what it did wrong).
type ValidationErrorKind int

const (
	// ValidationErrorUnknownTool — the tool name is not in the registry.
	// The model dispatched something we don't have an entry for.
	ValidationErrorUnknownTool ValidationErrorKind = iota + 1

	// ValidationErrorToolUnavailable — the tool exists but is Disabled.
	// Surfaces as `tool_unavailable_in_this_build`.
	ValidationErrorToolUnavailable

	// ValidationErrorArgsTooLarge — the Arguments byte length exceeded
	// the per-call cap.
	ValidationErrorArgsTooLarge

	// ValidationErrorMalformedArgs — Entry.Decode failed (bad JSON,
	// DisallowUnknownFields violation, type mismatch, missing required
	// field, enum/range violation — the decoder owns the granularity).
	ValidationErrorMalformedArgs
)

// String renders the kind as a stable lowercase label — used in
// ValidationError messages and in the typed tool error surfaced to the
// model.
func (k ValidationErrorKind) String() string {
	switch k {
	case ValidationErrorUnknownTool:
		return "unknown_tool"
	case ValidationErrorToolUnavailable:
		return "tool_unavailable_in_this_build"
	case ValidationErrorArgsTooLarge:
		return "args_too_large"
	case ValidationErrorMalformedArgs:
		return "malformed_args"
	default:
		return "unknown"
	}
}

// ValidationError is a typed validation failure. The harness reads Kind
// to choose the surface label and Message for the explanation; the model
// sees `{kind}: {message}` as the tool result content.
//
// Cause is optional — populated when Kind is ValidationErrorMalformedArgs
// to carry the decoder's underlying error (so logs/debug can see the
// actual JSON failure).
type ValidationError struct {
	Kind    ValidationErrorKind
	Tool    string // tool name as the model emitted (may not be in the registry)
	Message string
	Cause   error
}

func (e *ValidationError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return fmt.Sprintf("validation %s (tool %q): %s: %v", e.Kind, e.Tool, e.Message, e.Cause)
	}
	return fmt.Sprintf("validation %s (tool %q): %s", e.Kind, e.Tool, e.Message)
}

func (e *ValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// emptyObjectSchema is the JSON schema used for terminal tools by
// default — no properties, no required, no extra fields. Providers
// (Anthropic, OpenAI) require an input_schema even for no-arg tools.
var emptyObjectSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
