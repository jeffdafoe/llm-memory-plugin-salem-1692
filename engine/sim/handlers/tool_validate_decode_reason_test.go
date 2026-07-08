package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// ZBBS-WORK-413: a decode-stage validation rejection must surface the
// decoder's hand-authored, model-safe reason so a weak NPC can self-correct,
// while a raw encoding/json failure stays generic (its message can quote
// arbitrary input, which must not reach the transcript).
//
// LLM-221 extends the model-safe set to two structurally-safe encoding/json
// shapes — an unknown field and a type mismatch — because their reason can be
// rebuilt from the model's own field name and fixed descriptors without
// echoing any offending value. Syntax errors and truncated JSON still stay
// generic, and a type mismatch must never leak the value that triggered it.

func newDecodeReasonRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	if err := RegisterSceneQuote(r); err != nil {
		t.Fatalf("register scene_quote: %v", err)
	}
	if err := RegisterOfferTrade(r); err != nil {
		t.Fatalf("register offer_trade: %v", err)
	}
	return r
}

func TestValidator_Validate_SurfacesModelSafeDecodeReason(t *testing.T) {
	v := NewValidator(newDecodeReasonRegistry(t))

	cases := []struct {
		name    string
		tool    string
		args    string
		wantSub string // the surfaced reason must mention this field
	}{
		{
			// The live loop-driver (2026-06-15): Hannah called scene_quote
			// amount:0 four times in one tick, each getting only the opaque
			// "argument decode failed", and kept retrying.
			name:    "scene_quote amount below minimum",
			tool:    "sell",
			args:    `{"lines":[{"item":"Porridge","qty":1}],"amount":0}`,
			wantSub: "amount",
		},
		{
			// Spot-check the generalization across another tool's decoder.
			name:    "offer_trade want_qty below minimum",
			tool:    "offer_trade",
			args:    `{"with":"Ezekiel","want_item":"Skillet","want_qty":0}`,
			wantSub: "want_qty",
		},
		{
			// LLM-221: unknown field — the live loop was 6× speak with
			// {"message":...} instead of {"text":...}. The surfaced reason
			// must name the offending key so the model drops it.
			name:    "unknown field names the key",
			tool:    "sell",
			args:    `{"lines":[{"item":"Porridge","qty":1}],"amount":1,"message":"hi"}`,
			wantSub: `unknown field "message"`,
		},
		{
			// LLM-221: type mismatch, string into a bool field — one of the
			// cited offenders ("consume_now":"true").
			name:    "bool field given a string",
			tool:    "sell",
			args:    `{"lines":[{"item":"Porridge","qty":1}],"amount":1,"consume_now":"true"}`,
			wantSub: "consume_now",
		},
		{
			// LLM-221: type mismatch, string into an array field
			// (another cited offender, "mentions":"" — here consumers).
			name:    "array field given a string",
			tool:    "sell",
			args:    `{"lines":[{"item":"Porridge","qty":1}],"amount":1,"consumers":"Ezekiel"}`,
			wantSub: "consumers",
		},
		{
			// LLM-221: type mismatch on a nested field — the surfaced field
			// path still names the offender (qty lives inside lines[]).
			name:    "nested number field given a string",
			tool:    "sell",
			args:    `{"lines":[{"item":"Porridge","qty":"lots"}],"amount":1}`,
			wantSub: "qty",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vc, verr := v.Validate(llm.RawToolCall{Name: tc.tool, Arguments: json.RawMessage(tc.args)})
			if vc != nil {
				t.Fatalf("expected rejection, got a ValidatedCall")
			}
			if verr == nil || verr.Kind != ValidationErrorMalformedArgs {
				t.Fatalf("expected malformed_args, got %v", verr)
			}
			if verr.Message == "argument decode failed" {
				t.Errorf("decode reason was swallowed; model got only the generic message")
			}
			if !strings.Contains(verr.Message, tc.wantSub) {
				t.Errorf("Message should mention %q so the model can self-correct; got %q", tc.wantSub, verr.Message)
			}
			if verr.Cause == nil {
				t.Errorf("Cause should still carry the decoder error for logs")
			}
		})
	}
}

func TestValidator_Validate_RawDecodeStaysGeneric(t *testing.T) {
	v := NewValidator(newDecodeReasonRegistry(t))

	// A syntax error / truncated payload is a raw encoding/json failure with no
	// structural classification (unlike an unknown field or type mismatch,
	// which LLM-221 surfaces). Its error can quote arbitrary input fragments,
	// so it must NOT reach the model. The sentinel stands in for that input —
	// it must not appear in the surfaced message, which stays the fixed
	// generic string.
	const sentinel = "LEAK_SENTINEL_d41d8cd9"
	cases := []struct {
		name string
		args string
	}{
		{"truncated json", `{"lines":[{"item":"` + sentinel + `",`},
		{"invalid syntax", `{"amount": ` + sentinel + `}`},
		// Unknown key that is not a plain identifier (contains a space) must
		// fall back to generic rather than echo the model's raw key.
		{"unknown field non-identifier key", `{"lines":[{"item":"P","qty":1}],"amount":1,"` + sentinel + ` z":"y"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := v.Validate(llm.RawToolCall{Name: "sell", Arguments: json.RawMessage(tc.args)})
			if verr == nil || verr.Kind != ValidationErrorMalformedArgs {
				t.Fatalf("expected malformed_args, got %v", verr)
			}
			if verr.Message != "argument decode failed" {
				t.Errorf("raw decode error should stay generic; got %q", verr.Message)
			}
			if strings.Contains(verr.Message, sentinel) {
				t.Errorf("raw decoder input LEAKED into the model-facing message: %q", verr.Message)
			}
			if verr.Cause == nil {
				t.Errorf("Cause should carry the raw decoder error for logs")
			}
		})
	}
}

// TestDecodeReasonForModel_UnknownField exercises the unknown-field classifier
// directly: it must match the stdlib leaf message even when the decoder wraps
// it with %w, must NOT match on wrapper text that merely contains the prefix
// mid-string (the Contains→HasPrefix hazard), and must refuse to echo any key
// that is not a short plain identifier.
func TestDecodeReasonForModel_UnknownField(t *testing.T) {
	// The stdlib leaf form of a DisallowUnknownFields violation.
	leaf := func(key string) error { return errors.New(`json: unknown field "` + key + `"`) }
	// How the decoders present it: wrapped with %w.
	wrapped := func(key string) error { return fmt.Errorf("scene_quote: malformed arguments: %w", leaf(key)) }

	cases := []struct {
		name    string
		err     error
		wantOK  bool
		wantMsg string
	}{
		{"wrapped clean key", wrapped("message"), true, `sell: unknown field "message"`},
		{"plain clean key", leaf("consume_now"), true, `sell: unknown field "consume_now"`},
		// Prefix appears mid-string, not at the leaf head — must not classify
		// (this is exactly what a substring match would wrongly surface).
		{"prefix mid-string is not a match", errors.New(`context: json: unknown field "x"`), false, ""},
		{"trailing text after closing quote is not a match", errors.New(`json: unknown field "x" extra`), false, ""},
		{"key with space rejected", wrapped("bad key"), false, ""},
		{"key with quote-ish content rejected", wrapped(`a b c prompt`), false, ""},
		{"over-length key rejected", wrapped(strings.Repeat("a", 65)), false, ""},
		{"unrelated error stays generic", errors.New("some internal failure"), false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, ok := decodeReasonForModel("sell", tc.err)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (msg %q)", ok, tc.wantOK, msg)
			}
			if ok && msg != tc.wantMsg {
				t.Errorf("msg = %q, want %q", msg, tc.wantMsg)
			}
		})
	}
}

// TestValidator_Validate_TypeMismatchDoesNotLeakValue locks in the one leak
// hazard in the LLM-221 type-mismatch path: encoding/json's
// UnmarshalTypeError.Value is a fixed kind word ("string", "bool", …) EXCEPT
// on the number path, where it becomes "number <literal>" — embedding the
// value the model sent. The surfaced reason must name the field and its
// expected type but must never carry that literal.
func TestValidator_Validate_TypeMismatchDoesNotLeakValue(t *testing.T) {
	v := NewValidator(newDecodeReasonRegistry(t))

	cases := []struct {
		name    string
		args    string
		leak    string // the numeric literal that must NOT reach the model
		wantSub string // the field the reason should still name
	}{
		{
			// Fractional value into the int `amount` field triggers the
			// "number 3.5" Value path.
			name:    "fractional into int",
			args:    `{"lines":[{"item":"Porridge","qty":1}],"amount":3.5}`,
			leak:    "3.5",
			wantSub: "amount",
		},
		{
			// Overflowing literal into the int `amount` field triggers the
			// "number <literal>" Value path with a long, distinctive token.
			name:    "overflow into int",
			args:    `{"lines":[{"item":"Porridge","qty":1}],"amount":99999999999999999999}`,
			leak:    "99999999999999999999",
			wantSub: "amount",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := v.Validate(llm.RawToolCall{Name: "sell", Arguments: json.RawMessage(tc.args)})
			if verr == nil || verr.Kind != ValidationErrorMalformedArgs {
				t.Fatalf("expected malformed_args, got %v", verr)
			}
			// The reason is surfaced (model-safe), not swallowed to generic.
			if verr.Message == "argument decode failed" {
				t.Errorf("type-mismatch reason was swallowed; model got only the generic message")
			}
			if !strings.Contains(verr.Message, tc.wantSub) {
				t.Errorf("Message should name field %q; got %q", tc.wantSub, verr.Message)
			}
			// …but the offending numeric literal must never appear.
			if strings.Contains(verr.Message, tc.leak) {
				t.Errorf("offending value %q LEAKED into the model-facing message: %q", tc.leak, verr.Message)
			}
			if verr.Cause == nil {
				t.Errorf("Cause should carry the raw decoder error for logs")
			}
		})
	}
}
