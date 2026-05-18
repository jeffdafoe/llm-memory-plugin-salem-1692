package llm

import (
	"context"
	"errors"
	"fmt"
)

// Client is the provider-neutral LLM client interface. Every Complete
// call is stateless from the Client's perspective — the harness owns the
// transcript and passes the full conversation on every call (see §6
// transcript model in the PR 3d design note).
//
// Implementations: a real HTTP adapter (cutover layer, NOT in this
// package), and FakeClient (this package, for tests).
type Client interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// ToolResultPersister is an OPTIONAL interface adapters may implement
// when the backing transport keeps conversation history (e.g. memory-api
// writes chat_messages rows for every Complete call). The harness calls
// it after a terminal-class tool fires to write the last batch of
// tool-result rows without firing another LLM call.
//
// Why it exists: when a terminal tool (done(), unknown) ends the tick,
// the assistant message that contained that tool_call has already been
// written to provider history by the prior Complete. Without a
// persist-only follow-up, that tool_call sits in history with no
// matching tool_result — a corruption that breaks every subsequent
// tool-use call against the same VA (Anthropic 400 "tool_use without
// tool_result"). v1 hit this; v2 closes it via this interface.
//
// Adapters whose transports do not have a server-side history concept
// (e.g. a direct-provider adapter) do not implement this interface.
// Harness callers type-assert and skip the call when the assertion
// fails.
//
// FakeClient implements this for test inspection; PersistRequests() is
// the test-side accessor.
type ToolResultPersister interface {
	PersistToolResults(ctx context.Context, req PersistRequest) error
}

// ErrorClass categorizes Client.Complete failures into the cases the
// harness's CompleteReactorTick policy table reads. Every Complete error
// must classify; an error that returns ErrorUnknown is itself a bug to
// surface — extend Classify rather than silently coercing.
//
// The classes mirror the PR 3d design note §5.1 error table.
type ErrorClass int

const (
	// ErrorUnknown is the zero value — used only when Classify cannot
	// categorize an error. Callers should treat it as "the surface area
	// grew, fix Classify" rather than silently coercing to one of the
	// known classes.
	ErrorUnknown ErrorClass = iota

	// ErrorTransport — network failure, provider 5xx, or other
	// infrastructure error. The harness ends the tick with FailedBefore
	// or FailedAfterRender depending on whether any iteration completed.
	ErrorTransport

	// ErrorContextCancelled — ctx deadline exceeded or cancellation
	// (engine shutdown, pool stop). The harness ends the tick with
	// TerminalStatus=Shutdown.
	ErrorContextCancelled

	// ErrorMalformed — provider response failed to parse or violates the
	// provider format contract (missing required fields, wrong types,
	// etc.). The harness ends the tick with TerminalStatus=FailedAfter
	// Render and logs the raw response for debugging.
	ErrorMalformed

	// ErrorTooLarge — response or a single argument exceeded the gross
	// byte limit the Client enforces. The harness ends the tick with
	// TerminalStatus=FailedAfterRender.
	ErrorTooLarge

	// ErrorProviderRefusal — content-policy refusal from the provider.
	// Treated as a terminal failure; the harness ends the tick with
	// TerminalStatus=FailedAfterRender.
	ErrorProviderRefusal
)

// String renders the class as a stable lowercase label — used in
// TickResult.LLMErrorClass and in error messages. The labels are
// downstream-visible (telemetry, logs); changes are a contract break.
func (c ErrorClass) String() string {
	switch c {
	case ErrorTransport:
		return "transport"
	case ErrorContextCancelled:
		return "context_cancelled"
	case ErrorMalformed:
		return "malformed"
	case ErrorTooLarge:
		return "too_large"
	case ErrorProviderRefusal:
		return "provider_refusal"
	default:
		return "unknown"
	}
}

// Error is the typed error type Client implementations return for
// classifiable failures. Adapters that produce raw errors get classified
// via Classify; adapters that produce *Error get classified directly.
//
// Cause is optional — when set, errors.Is and errors.Unwrap walk to it,
// so callers can match against sentinel errors (context.Canceled, etc.)
// even when the immediate error is an *Error wrapper.
type Error struct {
	Class   ErrorClass
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil {
		return fmt.Sprintf("llm %s: %s: %v", e.Class, e.Message, e.Cause)
	}
	return fmt.Sprintf("llm %s: %s", e.Class, e.Message)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// Classify maps an error to its ErrorClass. It walks the error chain via
// errors.As to find a typed *Error first; otherwise it checks for context
// cancellation; otherwise ErrorUnknown.
//
// Adapters that produce raw errors should wrap them in *Error before
// returning. Classify is a fallback for adapters that pre-date the
// classification or pass through cancellation directly.
func Classify(err error) ErrorClass {
	if err == nil {
		return ErrorUnknown
	}
	var typed *Error
	if errors.As(err, &typed) && typed != nil {
		return typed.Class
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrorContextCancelled
	}
	return ErrorUnknown
}
