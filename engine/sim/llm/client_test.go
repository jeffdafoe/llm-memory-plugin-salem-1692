package llm

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestClassify_ReadsTypedError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want ErrorClass
	}{
		{"transport", &Error{Class: ErrorTransport}, ErrorTransport},
		{"malformed", &Error{Class: ErrorMalformed}, ErrorMalformed},
		{"too_large", &Error{Class: ErrorTooLarge}, ErrorTooLarge},
		{"refusal", &Error{Class: ErrorProviderRefusal}, ErrorProviderRefusal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.in); got != tc.want {
				t.Errorf("Classify(%v): got %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestClassify_DetectsContextCancellation(t *testing.T) {
	if got := Classify(context.Canceled); got != ErrorContextCancelled {
		t.Errorf("context.Canceled: got %s, want context_cancelled", got)
	}
	if got := Classify(context.DeadlineExceeded); got != ErrorContextCancelled {
		t.Errorf("context.DeadlineExceeded: got %s, want context_cancelled", got)
	}
}

func TestClassify_WrappedErrorChainResolves(t *testing.T) {
	// A *Error nested inside a fmt.Errorf wrap should still classify —
	// errors.As walks the chain.
	wrapped := fmt.Errorf("outer wrap: %w", &Error{Class: ErrorMalformed, Message: "bad shape"})
	if got := Classify(wrapped); got != ErrorMalformed {
		t.Errorf("wrapped malformed: got %s, want malformed", got)
	}
}

func TestClassify_WrappedContextCancellationResolves(t *testing.T) {
	// A *Error with Cause=context.Canceled should classify as the *Error's
	// Class, not as context cancellation (the typed wrapper is the
	// authoritative classification — adapters use it to say "this was a
	// transport error caused by ctx cancel" with full specificity).
	e := &Error{Class: ErrorTransport, Cause: context.Canceled}
	if got := Classify(e); got != ErrorTransport {
		t.Errorf("typed wrapper over ctx cancel: got %s, want transport (the Class wins)", got)
	}
}

func TestClassify_NilIsUnknown(t *testing.T) {
	if got := Classify(nil); got != ErrorUnknown {
		t.Errorf("Classify(nil): got %s, want unknown", got)
	}
}

func TestClassify_UnclassifiableIsUnknown(t *testing.T) {
	if got := Classify(errors.New("plain error")); got != ErrorUnknown {
		t.Errorf("Classify(plain): got %s, want unknown", got)
	}
}

func TestErrorClassString_StableLabels(t *testing.T) {
	// Pin the labels — TickResult.LLMErrorClass is downstream-visible
	// (telemetry, logs). Changes are a contract break.
	cases := map[ErrorClass]string{
		ErrorTransport:        "transport",
		ErrorContextCancelled: "context_cancelled",
		ErrorMalformed:        "malformed",
		ErrorTooLarge:         "too_large",
		ErrorProviderRefusal:  "provider_refusal",
		ErrorUnknown:          "unknown",
	}
	for c, want := range cases {
		if got := c.String(); got != want {
			t.Errorf("%d.String(): got %q, want %q", c, got, want)
		}
	}
	// Out-of-range value also stringifies to "unknown" rather than panicking.
	if got := ErrorClass(999).String(); got != "unknown" {
		t.Errorf("ErrorClass(999).String(): got %q, want \"unknown\"", got)
	}
}

func TestError_FormatWithCause(t *testing.T) {
	cause := errors.New("root cause")
	e := &Error{Class: ErrorTransport, Message: "connect timeout", Cause: cause}
	if got := e.Error(); got != "llm transport: connect timeout: root cause" {
		t.Errorf("Error() with cause: got %q", got)
	}
}

func TestError_FormatWithoutCause(t *testing.T) {
	e := &Error{Class: ErrorMalformed, Message: "bad json"}
	if got := e.Error(); got != "llm malformed: bad json" {
		t.Errorf("Error() without cause: got %q", got)
	}
}

func TestError_NilSafe(t *testing.T) {
	var e *Error
	if got := e.Error(); got != "" {
		t.Errorf("nil Error.Error(): got %q, want empty", got)
	}
	if got := e.Unwrap(); got != nil {
		t.Errorf("nil Error.Unwrap(): got %v, want nil", got)
	}
}

func TestError_UnwrapWalksToCause(t *testing.T) {
	cause := errors.New("root")
	e := &Error{Class: ErrorTransport, Cause: cause}
	if !errors.Is(e, cause) {
		t.Errorf("errors.Is should walk through Error.Unwrap to Cause")
	}
}
