package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// scene_quote_test.go — handler-package coverage of DecodeSceneQuoteArgs
// + HandleSceneQuote static validation. World-state validation (10
// gates) is tested at the sim.SceneQuoteCreate Command level in
// sim/scene_quote_test.go.

// ---- DecodeSceneQuoteArgs ----

func TestDecodeSceneQuoteArgs_Valid_Minimal(t *testing.T) {
	args, err := DecodeSceneQuoteArgs(json.RawMessage(
		`{"lines":[{"item":"ale","qty":1}],"amount":2,"consume_now":false}`))
	if err != nil {
		t.Fatalf("DecodeSceneQuoteArgs: %v", err)
	}
	got, ok := args.(SceneQuoteArgs)
	if !ok {
		t.Fatalf("Decoded type = %T, want SceneQuoteArgs", args)
	}
	if len(got.Lines) != 1 || got.Lines[0].ItemKind != "ale" || got.Lines[0].Qty != 1 || got.Amount != 2 || got.ConsumeNow != false {
		t.Errorf("decoded = %+v", got)
	}
	if got.TargetBuyer != "" || len(got.Consumers) != 0 {
		t.Errorf("optional fields populated unexpectedly: %+v", got)
	}
}

func TestDecodeSceneQuoteArgs_Valid_Full(t *testing.T) {
	raw := `{"lines":[{"item":"stew","qty":2}],"amount":10,"consume_now":true,"target_buyer":"Bea","consumers":["Bea","Cyrus"]}`
	args, err := DecodeSceneQuoteArgs(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("DecodeSceneQuoteArgs: %v", err)
	}
	got := args.(SceneQuoteArgs)
	if got.TargetBuyer != "Bea" {
		t.Errorf("TargetBuyer = %q", got.TargetBuyer)
	}
	if len(got.Consumers) != 2 || got.Consumers[0] != "Bea" || got.Consumers[1] != "Cyrus" {
		t.Errorf("Consumers = %v", got.Consumers)
	}
}

func TestDecodeSceneQuoteArgs_MissingItemKind(t *testing.T) {
	_, err := DecodeSceneQuoteArgs(json.RawMessage(`{"lines":[{"qty":1}],"amount":2,"consume_now":false}`))
	if err == nil || !strings.Contains(err.Error(), "item is required") {
		t.Fatalf("err = %v, want item required", err)
	}
}

func TestDecodeSceneQuoteArgs_QtyZero(t *testing.T) {
	_, err := DecodeSceneQuoteArgs(json.RawMessage(`{"lines":[{"item":"ale","qty":0}],"amount":2,"consume_now":false}`))
	if err == nil || !strings.Contains(err.Error(), "qty must be at least 1") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodeSceneQuoteArgs_NegativeAmount(t *testing.T) {
	_, err := DecodeSceneQuoteArgs(json.RawMessage(`{"lines":[{"item":"ale","qty":1}],"amount":-5,"consume_now":false}`))
	if err == nil || !strings.Contains(err.Error(), "amount must be at least 1") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodeSceneQuoteArgs_AmountOverMax(t *testing.T) {
	_, err := DecodeSceneQuoteArgs(json.RawMessage(`{"lines":[{"item":"ale","qty":1}],"amount":2147483648,"consume_now":false}`))
	if err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodeSceneQuoteArgs_TooManyConsumers(t *testing.T) {
	raw := `{"lines":[{"item":"ale","qty":1}],"amount":2,"consume_now":false,"consumers":["a","b","c","d","e","f","g","h","i"]}`
	_, err := DecodeSceneQuoteArgs(json.RawMessage(raw))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want too-many-consumers cap", err)
	}
}

func TestDecodeSceneQuoteArgs_UnknownField(t *testing.T) {
	_, err := DecodeSceneQuoteArgs(json.RawMessage(`{"lines":[{"item":"ale","qty":1}],"amount":2,"consume_now":false,"sneaky":"x"}`))
	if err == nil {
		t.Fatal("DecodeSceneQuoteArgs with unknown field: want error")
	}
}

func TestDecodeSceneQuoteArgs_NonObject(t *testing.T) {
	for _, bad := range []string{`null`, `[]`, `42`, `"string"`} {
		if _, err := DecodeSceneQuoteArgs(json.RawMessage(bad)); err == nil {
			t.Errorf("%q: want non-object reject", bad)
		}
	}
}

func TestDecodeSceneQuoteArgs_TrailingData(t *testing.T) {
	_, err := DecodeSceneQuoteArgs(json.RawMessage(`{"lines":[{"item":"ale","qty":1}],"amount":2,"consume_now":false}{"x":1}`))
	if err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("err = %v, want trailing data reject", err)
	}
}

func TestDecodeSceneQuoteArgs_LongItemKind(t *testing.T) {
	long := strings.Repeat("a", MaxSceneQuoteItemChars+1)
	raw := `{"lines":[{"item":"` + long + `","qty":1}],"amount":2,"consume_now":false}`
	_, err := DecodeSceneQuoteArgs(json.RawMessage(raw))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want item cap reject", err)
	}
}

// LLM-326: the canonical per-line field is `item`, but `item_kind` (the older
// engine-jargon name) is tolerated as a decode-only alias so a model that
// reaches for it still lands the sell. It folds into the same Go field.
func TestDecodeSceneQuoteArgs_ItemKindAlias(t *testing.T) {
	args, err := DecodeSceneQuoteArgs(json.RawMessage(
		`{"lines":[{"item_kind":"ale","qty":1}],"amount":2,"consume_now":false}`))
	if err != nil {
		t.Fatalf("DecodeSceneQuoteArgs with item_kind alias: %v", err)
	}
	got := args.(SceneQuoteArgs)
	if len(got.Lines) != 1 || got.Lines[0].ItemKind != "ale" {
		t.Errorf("item_kind alias not folded to canonical: %+v", got)
	}
}

// LLM-326: when both are present the canonical `item` wins over the alias.
func TestDecodeSceneQuoteArgs_ItemWinsOverAlias(t *testing.T) {
	args, err := DecodeSceneQuoteArgs(json.RawMessage(
		`{"lines":[{"item":"ale","item_kind":"beer","qty":1}],"amount":2,"consume_now":false}`))
	if err != nil {
		t.Fatalf("DecodeSceneQuoteArgs: %v", err)
	}
	got := args.(SceneQuoteArgs)
	if got.Lines[0].ItemKind != "ale" {
		t.Errorf("canonical item should win over alias, got %q", got.Lines[0].ItemKind)
	}
}

// LLM-326: an exact-empty canonical `item` falls back to the `item_kind` alias.
func TestDecodeSceneQuoteArgs_EmptyItemFallsBackToAlias(t *testing.T) {
	args, err := DecodeSceneQuoteArgs(json.RawMessage(
		`{"lines":[{"item":"","item_kind":"ale","qty":1}],"amount":2,"consume_now":false}`))
	if err != nil {
		t.Fatalf("DecodeSceneQuoteArgs: %v", err)
	}
	got := args.(SceneQuoteArgs)
	if got.Lines[0].ItemKind != "ale" {
		t.Fatalf("empty item should fall back to item_kind alias, got %q", got.Lines[0].ItemKind)
	}
}

// LLM-326: selection is exact-empty, not trimmed — a whitespace-only canonical
// `item` still selects (over a usable alias) and passes decode; HandleSceneQuote
// is what rejects it "empty after trim". Locks in the decode/handler split.
func TestDecodeSceneQuoteArgs_WhitespaceItemWinsOverAlias(t *testing.T) {
	args, err := DecodeSceneQuoteArgs(json.RawMessage(
		`{"lines":[{"item":"   ","item_kind":"ale","qty":1}],"amount":2,"consume_now":false}`))
	if err != nil {
		t.Fatalf("DecodeSceneQuoteArgs: %v", err)
	}
	got := args.(SceneQuoteArgs)
	if got.Lines[0].ItemKind != "   " {
		t.Fatalf("canonical whitespace item should win before handler trim, got %q", got.Lines[0].ItemKind)
	}
}

// LLM-326: strict-decode posture — an over-cap alias is rejected even when the
// canonical `item` would win, since every present item field is cap-validated.
func TestDecodeSceneQuoteArgs_OverCapAliasRejectedEvenWhenItemPresent(t *testing.T) {
	long := strings.Repeat("a", MaxSceneQuoteItemChars+1)
	raw := `{"lines":[{"item":"ale","item_kind":"` + long + `","qty":1}],"amount":2,"consume_now":false}`
	_, err := DecodeSceneQuoteArgs(json.RawMessage(raw))
	if err == nil || !strings.Contains(err.Error(), "lines[0].item_kind exceeds") {
		t.Fatalf("err = %v, want item_kind cap reject", err)
	}
}

// ---- HandleSceneQuote (pure builder) ----

func TestHandleSceneQuote_HappyPath_BuildsCommand(t *testing.T) {
	in := HandlerInput{
		ActorID: "aldous",
		Args: SceneQuoteArgs{
			Lines:      []SceneQuoteLineArg{{ItemKind: "ale", Qty: 1}},
			Amount:     2,
			ConsumeNow: false,
		},
	}
	cmd, err := HandleSceneQuote(in)
	if err != nil {
		t.Fatalf("HandleSceneQuote: %v", err)
	}
	if cmd.Fn == nil {
		t.Error("returned Command has nil Fn")
	}
}

func TestHandleSceneQuote_WrongArgsType(t *testing.T) {
	in := HandlerInput{
		ActorID: "aldous",
		Args:    "not a SceneQuoteArgs",
	}
	_, err := HandleSceneQuote(in)
	if err == nil || !strings.Contains(err.Error(), "unexpected args type") {
		t.Fatalf("err = %v", err)
	}
}

func TestHandleSceneQuote_TrimEmptyItemKind(t *testing.T) {
	in := HandlerInput{
		ActorID: "aldous",
		Args: SceneQuoteArgs{
			Lines:      []SceneQuoteLineArg{{ItemKind: "   ", Qty: 1}},
			Amount:     2,
			ConsumeNow: false,
		},
	}
	_, err := HandleSceneQuote(in)
	if err == nil || !strings.Contains(err.Error(), "empty after trim") {
		t.Fatalf("err = %v", err)
	}
}

func TestHandleSceneQuote_ControlCharInItemKind(t *testing.T) {
	in := HandlerInput{
		ActorID: "aldous",
		Args: SceneQuoteArgs{
			Lines:      []SceneQuoteLineArg{{ItemKind: "ale\nhack", Qty: 1}},
			Amount:     2,
			ConsumeNow: false,
		},
	}
	_, err := HandleSceneQuote(in)
	if err == nil || !strings.Contains(err.Error(), "control character") {
		t.Fatalf("err = %v", err)
	}
}

func TestHandleSceneQuote_ControlCharInTargetBuyer(t *testing.T) {
	in := HandlerInput{
		ActorID: "aldous",
		Args: SceneQuoteArgs{
			Lines:       []SceneQuoteLineArg{{ItemKind: "ale", Qty: 1}},
			Amount:      2,
			ConsumeNow:  false,
			TargetBuyer: "Bea\nInjected",
		},
	}
	_, err := HandleSceneQuote(in)
	if err == nil || !strings.Contains(err.Error(), "target_buyer") {
		t.Fatalf("err = %v", err)
	}
}

func TestHandleSceneQuote_DupConsumerName(t *testing.T) {
	in := HandlerInput{
		ActorID: "aldous",
		Args: SceneQuoteArgs{
			Lines:      []SceneQuoteLineArg{{ItemKind: "ale", Qty: 1}},
			Amount:     2,
			ConsumeNow: false,
			Consumers:  []string{"Bea", "bea"}, // case-insensitive dup
		},
	}
	_, err := HandleSceneQuote(in)
	if err == nil || !strings.Contains(err.Error(), "appears more than once") {
		t.Fatalf("err = %v, want dup reject", err)
	}
}

func TestHandleSceneQuote_EmptyConsumerEntry(t *testing.T) {
	in := HandlerInput{
		ActorID: "aldous",
		Args: SceneQuoteArgs{
			Lines:      []SceneQuoteLineArg{{ItemKind: "ale", Qty: 1}},
			Amount:     2,
			ConsumeNow: false,
			Consumers:  []string{"Bea", "   "},
		},
	}
	_, err := HandleSceneQuote(in)
	if err == nil || !strings.Contains(err.Error(), "empty after trim") {
		t.Fatalf("err = %v", err)
	}
}

// ---- Registration ----

func TestRegisterSceneQuote_AddsTool(t *testing.T) {
	r := NewRegistry()
	if err := RegisterSceneQuote(r); err != nil {
		t.Fatalf("RegisterSceneQuote: %v", err)
	}
	// LLM-184: sell is tick-terminal — a posted quote stands until a buyer
	// answers on their turn, so a forced re-quote (sell x3, observed live) can't
	// storm the round budget.
	e, ok := r.Lookup("sell")
	if !ok {
		t.Fatal("sell not registered")
	}
	if e.TerminalPolicy != TerminalOnSuccess {
		t.Errorf("sell TerminalPolicy = %v, want TerminalOnSuccess (LLM-184)", e.TerminalPolicy)
	}
	if err := RegisterSceneQuote(r); err == nil {
		t.Error("re-registration should reject duplicate tool name")
	}
}

// ---- Schema sync with substrate constants ----

func TestSceneQuoteSchema_CapsMatchSubstrate(t *testing.T) {
	if MaxSceneQuoteConsumers != sim.SceneQuoteMaxConsumers {
		t.Errorf("MaxSceneQuoteConsumers = %d, sim.SceneQuoteMaxConsumers = %d (must stay in sync)",
			MaxSceneQuoteConsumers, sim.SceneQuoteMaxConsumers)
	}
	// The schema is a static literal, so say's ADVERTISED cap can drift away
	// from the cap the decoder enforces (LLM-343) — the model would be told it
	// may send 1000 runes and then be rejected at some other number.
	var schema struct {
		Properties struct {
			Say struct {
				MaxLength int `json:"maxLength"`
			} `json:"say"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(sceneQuoteSchema, &schema); err != nil {
		t.Fatalf("unmarshal sceneQuoteSchema: %v", err)
	}
	if schema.Properties.Say.MaxLength != MaxSpeakTextChars {
		t.Errorf("schema say.maxLength = %d, MaxSpeakTextChars = %d (must stay in sync)",
			schema.Properties.Say.MaxLength, MaxSpeakTextChars)
	}
}

// ---- say (LLM-343) ----

func TestDecodeSceneQuoteArgs_Say(t *testing.T) {
	args, err := DecodeSceneQuoteArgs(json.RawMessage(
		`{"lines":[{"item":"stew","qty":1}],"amount":4,"consume_now":true,"say":"Four coins the bowl."}`))
	if err != nil {
		t.Fatalf("DecodeSceneQuoteArgs: %v", err)
	}
	if got := args.(SceneQuoteArgs).Say; got != "Four coins the bowl." {
		t.Errorf("Say = %q", got)
	}
}

func TestDecodeSceneQuoteArgs_SayOverMax(t *testing.T) {
	long := strings.Repeat("a", MaxSpeakTextChars+1)
	_, err := DecodeSceneQuoteArgs(json.RawMessage(
		`{"lines":[{"item":"stew","qty":1}],"amount":4,"consume_now":true,"say":"` + long + `"}`))
	if err == nil || !strings.Contains(err.Error(), "say exceeds") {
		t.Fatalf("err = %v, want a say length rejection", err)
	}
}

// A say carrying control characters must be refused for the same reason speak
// refuses one: it is re-rendered into every listener's perception prompt.
func TestHandleSceneQuote_SayControlChar(t *testing.T) {
	_, err := HandleSceneQuote(HandlerInput{
		ActorID: "seller",
		Args: SceneQuoteArgs{
			Lines:      []SceneQuoteLineArg{{ItemKind: "stew", Qty: 1}},
			Amount:     4,
			ConsumeNow: true,
			Say:        "Four coins\x00the bowl.",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "say contains a disallowed control character") {
		t.Fatalf("err = %v, want a control-char rejection", err)
	}
}
