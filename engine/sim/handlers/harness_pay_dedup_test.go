package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_pay_dedup_test.go — ZBBS-HOME-395: the same-tick repeat-OFFER guard,
// the pay analogue of the WORK-375 speak guard in harness_dedup_test.go. Pre-395
// a placed pay_with_item offer returned a bare "[ok]" with no "now pending, await
// their answer" signal, so the model had no within-tick reason to stop and
// re-offered the same item to the same seller every round to the iteration
// budget — the live Josiah×Moses carrot storm (6 pay calls/tick, price drifting
// 5→10, each spawning a ledger row that later rendered its own "fell through"
// line). The guard keys on payOfferKey (seller, item, disposition — price
// EXCLUDED) plus dispatch success, independent of tool class. Like the speak
// dedup tests, these use an OBSERVATION tool named `pay_with_item` (decoding real
// PayWithItemArgs) to exercise the exact harness branches without the commit
// scaffolding a real pay dispatch needs; the steer in the tool result is covered
// by commit_result_content_test.go and the key by TestPayOfferKey.

// newPayOfferDedupHarness builds a harness whose registry has an OBSERVATION tool
// `pay_with_item` (decoding real PayWithItemArgs, logging the placed offer on
// success) plus the terminal `done`.
func newPayOfferDedupHarness(t *testing.T, client llm.Client) (*Harness, *[]string) {
	t.Helper()
	r := NewRegistry()
	var offerLog []string
	payFn := func(_ context.Context, in HandlerInput) (string, error) {
		args, ok := in.Args.(PayWithItemArgs)
		if !ok {
			return "", errors.New("pay test handler: unexpected args type")
		}
		offerLog = append(offerLog, args.Seller+"/"+args.Item)
		return "[offer: ok]", nil
	}
	if err := r.RegisterObservation("pay_with_item", payWithItemSchema, DecodePayWithItemArgs, payFn, WithDescription(payWithItemDescription)); err != nil {
		t.Fatalf("register pay_with_item: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	return h, &offerLog
}

func payOfferJSON(seller, item string, qty, amount int) string {
	b, _ := json.Marshal(PayWithItemArgs{Seller: seller, Item: item, Qty: qty, Amount: amount, ConsumeNow: false})
	return string(b)
}

// payOfferKey is correctness-critical and special-cased on BOTH the tool name and
// the decoded-args TYPE, so pin it against the PRODUCTION decoder. If a future
// decoder refactor returns *PayWithItemArgs instead of the value, the guard would
// silently disable for the real tool and the storm could return — this fails
// loudly instead. Mirrors TestSpeakUtteranceKey_ProductionDecoderShape.
func TestPayOfferKey_ProductionDecoderShape(t *testing.T) {
	decoded, err := DecodePayWithItemArgs(json.RawMessage(`{"seller":"Moses James","item":"Carrots","qty":20,"amount":10,"consume_now":false}`))
	if err != nil {
		t.Fatalf("DecodePayWithItemArgs: %v", err)
	}
	key, ok := payOfferKey(&ValidatedCall{Name: "pay_with_item", DecodedArgs: decoded})
	if !ok {
		t.Fatal("payOfferKey ok=false for a production-decoded offer — dedup would be silently disabled in production")
	}
	if want := "moses james\x00carrots\x00keep"; key != want {
		t.Errorf("key = %q, want %q", key, want)
	}
}

// The storm itself: a buyer re-offers the SAME item to the SAME seller on a later
// round at a DRIFTING price (5 coins, then 10). The second offer is rejected
// before dispatch (terms are excluded from the key), the model ends with done(),
// and exactly one ledger offer lands instead of one-per-round.
func TestHarness_PayDedup_RejectsRepeatOfferAcrossRoundsDespitePriceDrift(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "pay_with_item", payOfferJSON("Moses James", "carrots", 20, 5))}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "pay_with_item", payOfferJSON("Moses James", "carrots", 20, 10))}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, offerLog := newPayOfferDedupHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done (model ended with done() after the repeat offer was blocked)", result.TerminalStatus)
	}
	if got := *offerLog; len(got) != 1 {
		t.Errorf("offers placed: got %d %q, want exactly 1 (price-drift repeat blocked)", len(got), got)
	}
	if !contains(result.ToolsSucceeded, "pay_with_item") {
		t.Errorf("ToolsSucceeded should include the first offer, got %v", result.ToolsSucceeded)
	}
	if !contains(result.ToolsFailedRejected, "pay_with_item") {
		t.Errorf("ToolsFailedRejected should include the blocked repeat, got %v", result.ToolsFailedRejected)
	}
}

// A second offer for a DIFFERENT item to the same seller is a distinct, allowed
// purchase — both land (the buyer-stocks-two-goods case), the pay analogue of
// the speak "distinct follow-up allowed" test.
func TestHarness_PayDedup_AllowsDistinctItemOffer(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{
		newToolCall("c1", 0, "pay_with_item", payOfferJSON("Moses James", "carrots", 20, 10)),
		newToolCall("c2", 1, "pay_with_item", payOfferJSON("Moses James", "wheat", 5, 5)),
		newToolCall("c3", 2, "done", `{}`),
	}}})
	h, offerLog := newPayOfferDedupHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if got := *offerLog; len(got) != 2 {
		t.Errorf("offers placed: got %d %q, want 2 (distinct items both allowed)", len(got), got)
	}
}

// A bounced (failed-dispatch) offer does NOT enter the dedup set: only a
// successful offer is recorded, so the model may retry the SAME offer and have it
// land. Mirrors the speak "bounced not recorded" case.
func TestHarness_PayDedup_BouncedOfferNotRecorded(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "pay_with_item", payOfferJSON("Moses James", "carrots", 20, 5))}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "pay_with_item", payOfferJSON("Moses James", "carrots", 20, 5))}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)

	r := NewRegistry()
	var offerLog []string
	calls := 0
	payFn := func(_ context.Context, in HandlerInput) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("bounced: nobody named that here")
		}
		args := in.Args.(PayWithItemArgs)
		offerLog = append(offerLog, args.Seller+"/"+args.Item)
		return "[offer: ok]", nil
	}
	if err := r.RegisterObservation("pay_with_item", payWithItemSchema, DecodePayWithItemArgs, payFn); err != nil {
		t.Fatalf("register pay_with_item: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if calls != 2 {
		t.Errorf("pay handler calls: got %d, want 2 (retry must reach the handler — bounce did not poison the dedup set)", calls)
	}
	if got := offerLog; len(got) != 1 {
		t.Errorf("offers placed: got %v, want 1 (retry landed)", got)
	}
}
