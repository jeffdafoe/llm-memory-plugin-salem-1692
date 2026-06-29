package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_ledger_resolution_dedup_test.go — LLM-104: the same-tick pay-offer
// resolution guard (resolvedLedgerThisTick). The four resolution tools (accept_pay
// / decline_pay / counter_pay / withdraw_pay) each answer one pending pay-offer by
// id; the first answer this tick moves the ledger out of `pending`, so any second
// answer against that id — same tool or a different one — is a guaranteed no-op
// that only reaches the command's "no longer pending (currently …)" error. The
// guard keys on the LEDGER ID alone (shared across the family), strictly broader
// than the ZBBS-HOME-414 genericCallKey guard these tools used to ride: that one
// keys on name + full decoded args, so a counter re-fired with a `message` added
// (different args) and a counter then an accept of the same ledger (different tool
// name) both slipped through. These tests pin both gaps + the per-ledger scope.

// TestLedgerResolutionID covers the predicate: each resolution-family (name, arg
// shape) pair yields its ledger id + true; everything else yields (0, false). The
// match binds the tool NAME and the arg shape and fails closed on a mismatch.
func TestLedgerResolutionID(t *testing.T) {
	cases := []struct {
		name string
		args any
		want LenientID
	}{
		{"accept_pay", AcceptPayArgs{LedgerID: 7}, 7},
		{"decline_pay", DeclinePayArgs{LedgerID: 8}, 8},
		{"counter_pay", CounterPayArgs{LedgerID: 9}, 9},
		{"withdraw_pay", WithdrawPayArgs{LedgerID: 10}, 10},
	}
	for _, tc := range cases {
		id, ok := ledgerResolutionID(&ValidatedCall{Name: tc.name, DecodedArgs: tc.args})
		if !ok || id != tc.want {
			t.Errorf("%s: got (%d, %v), want (%d, true)", tc.name, id, ok, tc.want)
		}
	}
	// speak is not a resolution call (its own guard owns it).
	if _, ok := ledgerResolutionID(&ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: "hi"}}); ok {
		t.Error("speak must not be a ledger-resolution call")
	}
	// The OFFER family (pay_with_item) stakes a NEW offer, it does not answer one —
	// payOfferKey owns it, not this guard.
	if _, ok := ledgerResolutionID(&ValidatedCall{Name: "pay_with_item", DecodedArgs: PayWithItemArgs{Seller: "Moses", Item: "carrots", Qty: 1, Amount: 5}}); ok {
		t.Error("pay_with_item must not be a ledger-resolution call")
	}
	// Name binding: a non-resolution tool name carrying a resolution arg struct must
	// NOT be treated as a resolution (over-blocking a dispatch guard is worse than
	// under-blocking).
	if _, ok := ledgerResolutionID(&ValidatedCall{Name: "not_counter_pay", DecodedArgs: CounterPayArgs{LedgerID: 9}}); ok {
		t.Error("a non-resolution tool name must not be treated as ledger resolution")
	}
	// Shape binding: a resolution name with a mismatched arg shape fails closed.
	if _, ok := ledgerResolutionID(&ValidatedCall{Name: "counter_pay", DecodedArgs: SpeakArgs{Text: "hi"}}); ok {
		t.Error("counter_pay with a non-CounterPayArgs shape must fail closed")
	}
	// Nil safety.
	if _, ok := ledgerResolutionID(nil); ok {
		t.Error("nil call: want ok=false")
	}
}

// newLedgerResolutionHarness registers the resolution-family tools under test with
// their REAL decoders (so DecodedArgs lands as the typed arg struct the guard type-
// switches on) but no-op command fns. The guard fires before dispatch, so a CommitFn
// runs only for a call actually let through; each dispatch is logged.
func newLedgerResolutionHarness(t *testing.T, client llm.Client) (*Harness, *[]string) {
	t.Helper()
	r := NewRegistry()
	var log []string
	mk := func(name string) func(HandlerInput) (sim.Command, error) {
		return func(in HandlerInput) (sim.Command, error) {
			log = append(log, name)
			return sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }}, nil
		}
	}
	reg := func(name string, decode func(json.RawMessage) (any, error)) {
		if err := r.RegisterCommit(name, json.RawMessage(`{"type":"object"}`), decode, mk(name), false); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	reg("counter_pay", DecodeCounterPayArgs)
	reg("accept_pay", DecodeAcceptPayArgs)
	reg("decline_pay", DecodeDeclinePayArgs)
	reg("withdraw_pay", DecodeWithdrawPayArgs)
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	return h, &log
}

// A counter on ledger N followed by an accept of that SAME ledger this tick: the
// accept is rejected before dispatch. The live John×Ezekiel case — John countered
// offer 332, then tried to accept his own just-countered ledger, which the engine
// could only answer with "no longer pending (currently countered)".
func TestHarness_LedgerResolution_RejectsAcceptOfJustCounteredLedger(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "counter_pay", `{"ledger_id":332,"amount":2}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "accept_pay", `{"ledger_id":332}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, log := newLedgerResolutionHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if got := *log; len(got) != 1 || got[0] != "counter_pay" {
		t.Errorf("dispatched: got %v, want [counter_pay] (accept of the just-countered ledger blocked before dispatch)", got)
	}
	if !contains(result.ToolsFailedRejected, "accept_pay") {
		t.Errorf("ToolsFailedRejected should include the blocked accept_pay, got %v", result.ToolsFailedRejected)
	}
}

// A counter on ledger N re-fired with a `message` added — byte-DIFFERENT args, so
// the old genericCallKey guard would have let it through. The ledger-id guard
// rejects it: one answer per offer per tick. This is the exact slip-through that
// drove the live double-counter (the perception's "say a brief word with the
// counter" cue, satisfied by stuffing the word into a second counter_pay).
func TestHarness_LedgerResolution_RejectsMessageDecoratedRecounter(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "counter_pay", `{"ledger_id":332,"amount":2}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "counter_pay", `{"ledger_id":332,"amount":2,"message":"how about this"}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, log := newLedgerResolutionHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if got := *log; len(got) != 1 || got[0] != "counter_pay" {
		t.Errorf("dispatched: got %v, want [counter_pay] (message-decorated re-counter blocked before dispatch)", got)
	}
	if !contains(result.ToolsFailedRejected, "counter_pay") {
		t.Errorf("ToolsFailedRejected should include the blocked re-counter, got %v", result.ToolsFailedRejected)
	}
}

// withdraw_pay is part of the guarded family and left genericCallKey too: a withdraw
// of a ledger already answered this tick (here, just countered) is blocked before
// dispatch — the cross-family shared-ledger key in action.
func TestHarness_LedgerResolution_RejectsWithdrawOfAlreadyAnsweredLedger(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "counter_pay", `{"ledger_id":332,"amount":2}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "withdraw_pay", `{"ledger_id":332}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, log := newLedgerResolutionHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if got := *log; len(got) != 1 || got[0] != "counter_pay" {
		t.Errorf("dispatched: got %v, want [counter_pay] (withdraw of the already-answered ledger blocked before dispatch)", got)
	}
	if !contains(result.ToolsFailedRejected, "withdraw_pay") {
		t.Errorf("ToolsFailedRejected should include the blocked withdraw_pay, got %v", result.ToolsFailedRejected)
	}
}

// Per-ledger scope: answering a DIFFERENT offer is always allowed — the guard only
// ever blocks a second answer to the SAME ledger.
func TestHarness_LedgerResolution_DifferentLedgerAllowed(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "counter_pay", `{"ledger_id":332,"amount":2}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "decline_pay", `{"ledger_id":331}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, log := newLedgerResolutionHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if got := *log; len(got) != 2 {
		t.Errorf("dispatched: got %v, want both counter_pay + decline_pay (distinct ledgers each allowed)", got)
	}
}

// TestResolvedPayOfferIDs covers the LLM-173 projection from the resolvedLedgerThisTick
// guard set (keyed by the lenient wire id) onto the sim.LedgerID set the within-tick
// re-render feeds perception.WithResolvedPayOffers. An empty set returns nil so the
// option is a no-op on a refresh that follows a non-resolution commit.
func TestResolvedPayOfferIDs(t *testing.T) {
	if got := resolvedPayOfferIDs(nil); got != nil {
		t.Errorf("nil set: got %v, want nil", got)
	}
	if got := resolvedPayOfferIDs(map[LenientID]struct{}{}); got != nil {
		t.Errorf("empty set: got %v, want nil", got)
	}
	got := resolvedPayOfferIDs(map[LenientID]struct{}{377: {}, 5: {}})
	if len(got) != 2 {
		t.Fatalf("converted set size = %d, want 2", len(got))
	}
	for _, id := range []sim.LedgerID{377, 5} {
		if _, ok := got[id]; !ok {
			t.Errorf("converted set missing ledger %d", id)
		}
	}
}
