package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_generic_dedup_test.go — ZBBS-HOME-414: the same-tick identical-call
// guard for an explicit allowlist of action tools (accept_pay / decline_pay /
// counter_pay / deliver_order / withdraw_pay / consume / move_to). The weak model
// re-fires a byte-identical call until the iteration budget — accept_pay(234)
// after it's already accepted, consume(Milk x1) six times — and every attempt +
// result bloats the durable transcript later ticks replay. speak and the offer
// family keep their own (broader, success-only) guards.

// newActionDedupHarness builds a harness whose registry has a single NON-terminal
// commit tool on the HOME-414 allowlist (deliver_order), logging each dispatch,
// plus the terminal `done`. Non-terminal so the tick continues to the repeat.
func newActionDedupHarness(t *testing.T, client llm.Client) (*Harness, *[]string) {
	t.Helper()
	r := NewRegistry()
	var log []string
	fn := func(in HandlerInput) (sim.Command, error) {
		log = append(log, "deliver_order")
		return sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }}, nil
	}
	if err := r.RegisterCommit("deliver_order", json.RawMessage(`{"type":"object"}`), passthroughDecode, fn, false); err != nil {
		t.Fatalf("register deliver_order: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	return h, &log
}

// TestGenericCallKey covers the predicate's scope: it applies to the allowlisted
// action tools with a stable, arg-sensitive JSON key, and EXCLUDES everything
// else — speak (own guard), the offer family (own guard), observation-class calls
// (thinking is not penalized, ZBBS-WORK-321), AND any non-allowlisted commit tool
// (so a newly-added tool doesn't silently inherit same-args dedup).
func TestGenericCallKey(t *testing.T) {
	commit := &RegistryEntry{Class: ClassCommit}
	obs := &RegistryEntry{Class: ClassObservation}

	// Applies to an allowlisted action tool, with a stable + arg-sensitive key.
	k1, ok := genericCallKey(&ValidatedCall{Name: "accept_pay", Entry: commit, DecodedArgs: AcceptPayArgs{LedgerID: 234}})
	if !ok {
		t.Fatal("genericCallKey ok=false for an allowlisted accept_pay — the guard would be silently disabled")
	}
	k1b, _ := genericCallKey(&ValidatedCall{Name: "accept_pay", Entry: commit, DecodedArgs: AcceptPayArgs{LedgerID: 234}})
	if k1 != k1b {
		t.Errorf("identical calls keyed differently: %q vs %q", k1, k1b)
	}
	if k2, _ := genericCallKey(&ValidatedCall{Name: "accept_pay", Entry: commit, DecodedArgs: AcceptPayArgs{LedgerID: 235}}); k1 == k2 {
		t.Error("different ledger ids must key differently")
	}

	// Arg-sensitive across fields: a different consume item is a different key.
	cMilk, _ := genericCallKey(&ValidatedCall{Name: "consume", Entry: commit, DecodedArgs: ConsumeArgs{Item: "Milk", Qty: 1}})
	cBread, _ := genericCallKey(&ValidatedCall{Name: "consume", Entry: commit, DecodedArgs: ConsumeArgs{Item: "Bread", Qty: 1}})
	if cMilk == cBread {
		t.Error("different consume items must key differently")
	}

	// Excluded: speak (owned by speakUtteranceKey).
	if _, ok := genericCallKey(&ValidatedCall{Name: "speak", Entry: commit, DecodedArgs: SpeakArgs{Text: "hi"}}); ok {
		t.Error("speak must be excluded — its own guard owns it")
	}
	// Excluded: the offer family (owned by payOfferKey). offer_trade lowers onto
	// PayWithItemArgs, so it is covered too.
	if _, ok := genericCallKey(&ValidatedCall{Name: "pay_with_item", Entry: commit, DecodedArgs: PayWithItemArgs{Seller: "Moses", Item: "carrots", Qty: 1, Amount: 5}}); ok {
		t.Error("pay_with_item must be excluded — payOfferKey owns it")
	}
	if _, ok := genericCallKey(&ValidatedCall{Name: "offer_trade", Entry: commit, DecodedArgs: PayWithItemArgs{Seller: "Moses", Item: "carrots", Qty: 1}}); ok {
		t.Error("offer_trade must be excluded — payOfferKey owns it")
	}
	// Excluded: observation-class calls (thinking is not penalized).
	if _, ok := genericCallKey(&ValidatedCall{Name: "recall", Entry: obs, DecodedArgs: "anything"}); ok {
		t.Error("observation-class calls must be excluded")
	}
	// Excluded: a commit tool that is NOT on the allowlist — the guard must not
	// silently broaden to every future commit action (code_review HOME-414).
	if _, ok := genericCallKey(&ValidatedCall{Name: "note", Entry: commit, DecodedArgs: "x"}); ok {
		t.Error("a non-allowlisted commit tool (note) must be excluded")
	}
	if _, ok := genericCallKey(&ValidatedCall{Name: "gather", Entry: commit, DecodedArgs: "x"}); ok {
		t.Error("a non-allowlisted commit tool (gather) must be excluded")
	}
	// Nil safety.
	if _, ok := genericCallKey(nil); ok {
		t.Error("nil call: want ok=false")
	}
	if _, ok := genericCallKey(&ValidatedCall{Name: "accept_pay", Entry: nil, DecodedArgs: 1}); ok {
		t.Error("nil entry: want ok=false")
	}
}

// An identical allowlisted commit call on a LATER round is rejected before
// dispatch; the model then ends with done().
func TestHarness_GenericDedup_RejectsIdenticalCommitRepeat(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	const args = `{"order_id":7}`
	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "deliver_order", args)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "deliver_order", args)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, log := newActionDedupHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done (model ended with done() after the repeat was blocked)", result.TerminalStatus)
	}
	if got := *log; len(got) != 1 {
		t.Errorf("commits dispatched: got %d %v, want 1 (identical repeat blocked before dispatch)", len(got), got)
	}
	if !contains(result.ToolsFailedRejected, "deliver_order") {
		t.Errorf("ToolsFailedRejected should include the blocked repeat, got %v", result.ToolsFailedRejected)
	}
	if result.IterationCount != 3 {
		t.Errorf("IterationCount: got %d, want 3 (deliver_order, blocked repeat, done)", result.IterationCount)
	}
}

// Two DISTINCT allowlisted commit calls both dispatch — the guard only ever
// blocks the provably-useless byte-identical repeat, never a different action.
func TestHarness_GenericDedup_AllowsDistinctArgs(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "deliver_order", `{"order_id":7}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "deliver_order", `{"order_id":8}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, log := newActionDedupHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if got := *log; len(got) != 2 {
		t.Errorf("commits dispatched: got %d %v, want 2 (distinct args both allowed)", len(got), got)
	}
}

// The key differentiator from the speak/offer guards: this records on the FIRST
// attempt, NOT on success. A commit whose dispatch FAILS, re-issued identically,
// is still rejected — record-on-success would have let it through. Models the
// live accept_pay(234) re-fired after "no longer pending". The tool is the
// allowlisted `consume` (the live consume-Milk spam) and its command always fails
// on the world goroutine; the CommitFn (which logs) runs only when a call is
// actually dispatched, so a log length of 1 proves the round-2 identical call was
// blocked.
func TestHarness_GenericDedup_RecordsOnAttemptNotSuccess(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	r := NewRegistry()
	var actLog []string
	failFn := func(in HandlerInput) (sim.Command, error) {
		actLog = append(actLog, "consume")
		return sim.Command{Fn: func(*sim.World) (any, error) {
			return nil, errors.New("consume always fails in this test")
		}}, nil
	}
	if err := r.RegisterCommit("consume", json.RawMessage(`{"type":"object"}`), passthroughDecode, failFn, false); err != nil {
		t.Fatalf("register consume: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}

	const args = `{"item":"Milk","qty":1}`
	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "consume", args)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "consume", args)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if len(actLog) != 1 {
		t.Errorf("CommitFn invocations: got %d, want 1 — the round-2 identical call must be blocked even though round-1 FAILED (proves record-on-attempt, not on-success)", len(actLog))
	}
	if !contains(result.ToolsFailedRejected, "consume") {
		t.Errorf("ToolsFailedRejected should include the failed first call and the blocked repeat, got %v", result.ToolsFailedRejected)
	}
}
