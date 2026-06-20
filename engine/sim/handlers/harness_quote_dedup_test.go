package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_quote_dedup_test.go — ZBBS-HOME-433: the same-tick repeat-QUOTE
// guard, the seller-side analogue of the HOME-395 offer guard in
// harness_pay_dedup_test.go. Pre-433 a posted scene_quote returned a bare
// "[ok]" with no standing-offer signal, so the model re-posted the identical
// quote every round to the iteration budget — the live John×Ezekiel bread
// storm (five scene_quote calls in one tick, all succeeding, terminal
// budget_forced). The guard keys on sceneQuoteKey (item, disposition, target
// — price and qty EXCLUDED) plus dispatch success. Like the pay dedup tests,
// these use an OBSERVATION tool named `scene_quote` (decoding real
// SceneQuoteArgs) to exercise the exact harness branches without the commit
// scaffolding a real quote dispatch needs; the steer in the tool result is
// covered by commit_result_content_test.go and the key by TestSceneQuoteKey.

// newQuoteDedupHarness builds a harness whose registry has an OBSERVATION
// tool `scene_quote` (decoding real SceneQuoteArgs, logging the posted quote
// on success) plus the terminal `done`.
func newQuoteDedupHarness(t *testing.T, client llm.Client) (*Harness, *[]string) {
	t.Helper()
	r := NewRegistry()
	var quoteLog []string
	quoteFn := func(_ context.Context, in HandlerInput) (string, error) {
		args, ok := in.Args.(SceneQuoteArgs)
		if !ok {
			return "", errors.New("scene_quote test handler: unexpected args type")
		}
		quoteLog = append(quoteLog, args.ItemKind)
		return "[quote: ok]", nil
	}
	if err := r.RegisterObservation("sell", sceneQuoteSchema, DecodeSceneQuoteArgs, quoteFn, WithDescription(sceneQuoteDescription)); err != nil {
		t.Fatalf("register scene_quote: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	return h, &quoteLog
}

func sceneQuoteJSON(item string, qty, amount int, consumeNow bool) string {
	b, _ := json.Marshal(SceneQuoteArgs{ItemKind: item, Qty: qty, Amount: amount, ConsumeNow: consumeNow})
	return string(b)
}

// sceneQuoteKey is special-cased on BOTH the tool name and the decoded-args
// TYPE, so pin it against the PRODUCTION decoder — if a future decoder
// refactor returns *SceneQuoteArgs instead of the value, the guard would
// silently disable for the real tool and the storm could return. Mirrors
// TestPayOfferKey_ProductionDecoderShape.
func TestSceneQuoteKey_ProductionDecoderShape(t *testing.T) {
	decoded, err := DecodeSceneQuoteArgs(json.RawMessage(`{"item_kind":"Bread","qty":1,"amount":4,"consume_now":false}`))
	if err != nil {
		t.Fatalf("DecodeSceneQuoteArgs: %v", err)
	}
	key, ok := sceneQuoteKey(&ValidatedCall{Name: "sell", DecodedArgs: decoded})
	if !ok {
		t.Fatal("sceneQuoteKey ok=false for a production-decoded quote — dedup would be silently disabled in production")
	}
	if want := "bread\x00keep\x00\x001"; key != want {
		t.Errorf("key = %q, want %q", key, want)
	}
}

// TestSceneQuoteKey_TargetAndDisposition: a targeted quote and a public one
// for the same goods key differently, as do consume-now vs take-away and
// different lot sizes — but price is excluded.
func TestSceneQuoteKey_TargetAndDisposition(t *testing.T) {
	base := SceneQuoteArgs{ItemKind: "Bread", Qty: 1, Amount: 4}
	key := func(a SceneQuoteArgs) string {
		k, ok := sceneQuoteKey(&ValidatedCall{Name: "sell", DecodedArgs: a})
		if !ok {
			t.Fatalf("sceneQuoteKey ok=false for %+v", a)
		}
		return k
	}

	repriced := base
	repriced.Amount = 9
	if key(base) != key(repriced) {
		t.Error("price drift must not change the key")
	}

	// A different lot size is a genuinely distinct standing offer — the
	// substrate's supersede/coexist rules decide its fate, not the guard
	// (code_review #415).
	differentLot := base
	differentLot.Qty = 3
	if key(base) == key(differentLot) {
		t.Error("different qty must change the key")
	}

	targeted := base
	targeted.TargetBuyer = "Ezekiel Crane"
	if key(base) == key(targeted) {
		t.Error("targeted vs public must key differently")
	}

	eatHere := base
	eatHere.ConsumeNow = true
	if key(base) == key(eatHere) {
		t.Error("consume-now vs take-away must key differently")
	}

	if _, ok := sceneQuoteKey(&ValidatedCall{Name: "pay_with_item", DecodedArgs: PayWithItemArgs{Item: "Bread"}}); ok {
		t.Error("sceneQuoteKey must not match non-quote calls")
	}
}

// The storm itself: the seller re-posts the SAME quote on later rounds at a
// DRIFTING price (4 coins, then 5). The repeat is rejected before dispatch,
// the model ends with done(), and exactly one quote lands instead of
// one-per-round.
func TestHarness_QuoteDedup_RejectsRepeatQuoteAcrossRoundsDespitePriceDrift(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "sell", sceneQuoteJSON("Bread", 1, 4, false))}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "sell", sceneQuoteJSON("Bread", 1, 5, false))}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, quoteLog := newQuoteDedupHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done (model ended with done() after the repeat quote was blocked)", result.TerminalStatus)
	}
	if got := *quoteLog; len(got) != 1 {
		t.Errorf("quotes posted: got %d %q, want exactly 1 (price-drift repeat blocked)", len(got), got)
	}
	if !contains(result.ToolsSucceeded, "sell") {
		t.Errorf("ToolsSucceeded should include the first quote, got %v", result.ToolsSucceeded)
	}
	if !contains(result.ToolsFailedRejected, "sell") {
		t.Errorf("ToolsFailedRejected should include the blocked repeat, got %v", result.ToolsFailedRejected)
	}
}

// A quote for a DIFFERENT item on a later round is a distinct, allowed
// posting — both land (the vendor-stocks-two-goods case).
func TestHarness_QuoteDedup_AllowsDistinctItemQuote(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{
		newToolCall("c1", 0, "sell", sceneQuoteJSON("Bread", 1, 4, false)),
		newToolCall("c2", 1, "sell", sceneQuoteJSON("Ale", 1, 2, true)),
		newToolCall("c3", 2, "done", `{}`),
	}}})
	h, quoteLog := newQuoteDedupHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if got := *quoteLog; len(got) != 2 {
		t.Errorf("quotes posted: got %d %q, want 2 (distinct items both allowed)", len(got), got)
	}
}
