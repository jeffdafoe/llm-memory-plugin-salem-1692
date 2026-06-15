package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// ZBBS-WORK-413: a decode-stage validation rejection must surface the
// decoder's hand-authored, model-safe reason so a weak NPC can self-correct,
// while a raw encoding/json failure stays generic (its message can quote
// arbitrary input, which must not reach the transcript).

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
			tool:    "scene_quote",
			args:    `{"item_kind":"Porridge","qty":1,"amount":0}`,
			wantSub: "amount",
		},
		{
			// Spot-check the generalization across another tool's decoder.
			name:    "offer_trade want_qty below minimum",
			tool:    "offer_trade",
			args:    `{"with":"Ezekiel","want_item":"Skillet","want_qty":0}`,
			wantSub: "want_qty",
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

	// A type mismatch / truncated payload is a raw encoding/json failure. Its
	// error can quote arbitrary input fragments, so it must NOT reach the
	// model. The sentinel stands in for that input — it must not appear in the
	// surfaced message, which must stay the fixed generic string.
	const sentinel = "LEAK_SENTINEL_d41d8cd9"
	cases := []struct {
		name string
		args string
	}{
		{"type mismatch", `{"item_kind":"` + sentinel + `","qty":"not-a-number","amount":1}`},
		{"truncated json", `{"item_kind":"` + sentinel + `",`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := v.Validate(llm.RawToolCall{Name: "scene_quote", Arguments: json.RawMessage(tc.args)})
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
