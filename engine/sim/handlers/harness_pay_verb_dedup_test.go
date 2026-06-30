package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_pay_verb_dedup_test.go — LLM-202: the same-tick repeat-PAY guard for
// the bare `pay` verb, the pay analogue of the pay_with_item offer guard in
// harness_pay_dedup_test.go. A bare pay settles coins instantly and irreversibly
// with no "now pending" signal, so a weak model that re-emits the identical call
// settles it a SECOND time — the live John Ellis double (4 coins to Silence
// Walker twice in one tick, 8 coins for one verbal arrangement). The guard keys
// on payDedupKey (recipient, for — amount EXCLUDED) plus dispatch success. Like
// the offer dedup tests, these register an OBSERVATION tool named `pay` (decoding
// real PayArgs) to exercise the exact harness branches without the commit
// scaffolding a real pay dispatch needs; the (recipient, for) key is pinned by
// TestPayDedupKey_ProductionDecoderShape.

// newPayVerbDedupHarness builds a harness whose registry has an OBSERVATION tool
// `pay` (decoding real PayArgs, logging the settled pay on success) plus the
// terminal `done`.
func newPayVerbDedupHarness(t *testing.T, client llm.Client) (*Harness, *[]string) {
	t.Helper()
	r := NewRegistry()
	var payLog []string
	payFn := func(_ context.Context, in HandlerInput) (string, error) {
		args, ok := in.Args.(PayArgs)
		if !ok {
			return "", errors.New("pay test handler: unexpected args type")
		}
		payLog = append(payLog, args.Recipient+"/"+args.For)
		return "[pay: ok]", nil
	}
	if err := r.RegisterObservation("pay", paySchema, DecodePayArgs, payFn, WithDescription(payDescription)); err != nil {
		t.Fatalf("register pay: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	return h, &payLog
}

func payVerbJSON(recipient, forText string, amount int) string {
	b, _ := json.Marshal(PayArgs{Recipient: recipient, Amount: amount, For: forText})
	return string(b)
}

// payDedupKey is correctness-critical and special-cased on BOTH the tool name and
// the decoded-args TYPE, so pin it against the PRODUCTION decoder. If a future
// decoder refactor returns *PayArgs instead of the value, the guard would
// silently disable for the real tool and the double-pay could return — this fails
// loudly instead. Mirrors TestPayOfferKey_ProductionDecoderShape.
func TestPayDedupKey_ProductionDecoderShape(t *testing.T) {
	decoded, err := DecodePayArgs(json.RawMessage(`{"recipient":"Silence Walker","amount":4,"for":"helping with serving ale and preparing stew"}`))
	if err != nil {
		t.Fatalf("DecodePayArgs: %v", err)
	}
	key, ok := payDedupKey(&ValidatedCall{Name: "pay", DecodedArgs: decoded})
	if !ok {
		t.Fatal("payDedupKey ok=false for a production-decoded pay — dedup would be silently disabled in production")
	}
	if want := "silence walker\x00helping with serving ale and preparing stew"; key != want {
		t.Errorf("key = %q, want %q", key, want)
	}
}

// The live double: John pays Silence the SAME (recipient, reason) twice across
// rounds at a drifting amount (4, then 8). The second pay is rejected before
// dispatch (amount is excluded from the key), the model ends with done(), and
// exactly one pay settles instead of one-per-round.
func TestHarness_PayVerbDedup_RejectsRepeatPayDespiteAmountDrift(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "pay", payVerbJSON("Silence Walker", "serving ale", 4))}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "pay", payVerbJSON("Silence Walker", "serving ale", 8))}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, payLog := newPayVerbDedupHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done (model ended with done() after the repeat pay was blocked)", result.TerminalStatus)
	}
	if got := *payLog; len(got) != 1 {
		t.Errorf("pays settled: got %d %q, want exactly 1 (amount-drift repeat blocked)", len(got), got)
	}
	if !contains(result.ToolsSucceeded, "pay") {
		t.Errorf("ToolsSucceeded should include the first pay, got %v", result.ToolsSucceeded)
	}
	if !contains(result.ToolsFailedRejected, "pay") {
		t.Errorf("ToolsFailedRejected should include the blocked repeat, got %v", result.ToolsFailedRejected)
	}
}

// A second pay to the same recipient for a DIFFERENT reason is a distinct,
// allowed payment (a wage AND a separate gift) — both land. The pay analogue of
// the offer "distinct item allowed" test.
func TestHarness_PayVerbDedup_AllowsDistinctReason(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{
		newToolCall("c1", 0, "pay", payVerbJSON("Silence Walker", "serving ale", 4)),
		newToolCall("c2", 1, "pay", payVerbJSON("Silence Walker", "a kind word", 1)),
		newToolCall("c3", 2, "done", `{}`),
	}}})
	h, payLog := newPayVerbDedupHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if got := *payLog; len(got) != 2 {
		t.Errorf("pays settled: got %d %q, want 2 (distinct reasons both allowed)", len(got), got)
	}
}

// A bounced (failed-dispatch) pay does NOT enter the dedup set: only a settled
// pay is recorded, so the model may retry the SAME pay and have it land. Mirrors
// the offer "bounced not recorded" case.
func TestHarness_PayVerbDedup_BouncedPayNotRecorded(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "pay", payVerbJSON("Silence Walker", "serving ale", 4))}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "pay", payVerbJSON("Silence Walker", "serving ale", 4))}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)

	r := NewRegistry()
	var payLog []string
	calls := 0
	payFn := func(_ context.Context, in HandlerInput) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("bounced: nobody named that here")
		}
		args := in.Args.(PayArgs)
		payLog = append(payLog, args.Recipient+"/"+args.For)
		return "[pay: ok]", nil
	}
	if err := r.RegisterObservation("pay", paySchema, DecodePayArgs, payFn); err != nil {
		t.Fatalf("register pay: %v", err)
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
	if got := payLog; len(got) != 1 {
		t.Errorf("pays settled: got %v, want 1 (retry landed)", got)
	}
}

// TestHarness_PayVerbDedup_AliasDriftNotDeduped documents a KNOWN LIMITATION
// (LLM-202 / code_review): the same-tick guard keys on the normalized recipient
// TEXT, not the resolved actor id, because the harness guard runs BEFORE dispatch
// — before sim.Pay resolves the name to a huddle peer. This is the same
// text-keying every sibling guard uses (speak/offer/quote all key on the typed
// text), so a model that pays "Silence Walker" then "Silence" in one tick — both
// resolving to the same actor — settles BOTH; this guard catches only
// casing/spacing drift, not aliasing. The identity-aware coverage for the
// labor-double case lives in sim.Pay's activeLaborBetween guard, not here. This
// test pins the current behavior so a future identity-aware change (keying on the
// resolved recipient id) is a deliberate, visible flip rather than a silent one.
func TestHarness_PayVerbDedup_AliasDriftNotDeduped(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{
		newToolCall("c1", 0, "pay", payVerbJSON("Silence Walker", "serving ale", 4)),
		newToolCall("c2", 1, "pay", payVerbJSON("Silence", "serving ale", 4)),
		newToolCall("c3", 2, "done", `{}`),
	}}})
	h, payLog := newPayVerbDedupHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if got := *payLog; len(got) != 2 {
		t.Errorf("pays settled: got %d %q, want 2 — alias drift is NOT deduped by the text key (documented limitation)", len(got), got)
	}
}
