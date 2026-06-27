package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_labor_resolution_dedup_test.go — LLM-164: the same-tick labor-offer
// resolution guard (resolvedLaborThisTick), the labor mirror of the LLM-104
// pay-offer guard. The employer-side tools (accept_work / decline_work) each
// answer one pending labor offer by labor_id; the first answer this tick moves the
// offer out of `pending` (to working or declined), so any second answer against
// that id — same tool or a different one — is a guaranteed no-op that only reaches
// AcceptWork's raw "no longer pending (currently working) — nothing to accept"
// error, which the weak model re-fires past (live: John Ellis accept_work×6 in one
// turn against a single offer). LLM-163 left these two unguarded on the theory the
// substrate gate sufficed; it does not. The guard keys on the LABOR ID alone,
// shared across the pair.

// TestLaborResolutionID covers the predicate: each (name, arg shape) pair yields
// its labor id + true; everything else yields (0, false). The match binds the tool
// NAME and the arg shape and fails closed on a mismatch.
func TestLaborResolutionID(t *testing.T) {
	cases := []struct {
		name string
		args any
		want LenientID
	}{
		{"accept_work", AcceptWorkArgs{LaborID: 1}, 1},
		{"decline_work", DeclineWorkArgs{LaborID: 2}, 2},
	}
	for _, tc := range cases {
		id, ok := laborResolutionID(&ValidatedCall{Name: tc.name, DecodedArgs: tc.args})
		if !ok || id != tc.want {
			t.Errorf("%s: got (%d, %v), want (%d, true)", tc.name, id, ok, tc.want)
		}
	}
	// solicit_work STAKES a new offer, it does not answer one — solicitedThisTick
	// owns it, not this guard.
	if _, ok := laborResolutionID(&ValidatedCall{Name: "solicit_work", DecodedArgs: SolicitWorkArgs{Employer: "John Ellis", Reward: 5, DurationMinutes: 30}}); ok {
		t.Error("solicit_work must not be a labor-resolution call")
	}
	// Name binding: a non-resolution tool name carrying a resolution arg struct must
	// NOT be treated as a resolution (over-blocking a dispatch guard is worse than
	// under-blocking — the substrate not-pending gate still backstops it).
	if _, ok := laborResolutionID(&ValidatedCall{Name: "not_accept_work", DecodedArgs: AcceptWorkArgs{LaborID: 1}}); ok {
		t.Error("a non-resolution tool name must not be treated as labor resolution")
	}
	// Shape binding: a resolution name with a mismatched arg shape fails closed.
	if _, ok := laborResolutionID(&ValidatedCall{Name: "accept_work", DecodedArgs: SpeakArgs{Text: "hi"}}); ok {
		t.Error("accept_work with a non-AcceptWorkArgs shape must fail closed")
	}
	// Nil safety.
	if _, ok := laborResolutionID(nil); ok {
		t.Error("nil call: want ok=false")
	}
}

// newLaborResolutionHarness registers accept_work / decline_work with their REAL
// decoders (so DecodedArgs lands as the typed arg struct the guard type-switches
// on) but no-op command fns. The guard fires before dispatch and records on the
// first attempt regardless of outcome, so a CommitFn runs only for a call actually
// let through; each dispatch is logged.
func newLaborResolutionHarness(t *testing.T, client llm.Client) (*Harness, *[]string) {
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
	reg("accept_work", DecodeAcceptWorkArgs)
	reg("decline_work", DecodeDeclineWorkArgs)
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	return h, &log
}

// A second accept_work of the SAME offer this tick is rejected before dispatch —
// the live John Ellis storm: he accepted offer 1, then re-accepted it five more
// times, each of which the engine could only answer with "no longer pending
// (currently working)".
func TestHarness_LaborResolution_RejectsReAcceptSameOffer(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "accept_work", `{"labor_id":1}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "accept_work", `{"labor_id":1}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, log := newLaborResolutionHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if got := *log; len(got) != 1 || got[0] != "accept_work" {
		t.Errorf("dispatched: got %v, want [accept_work] (re-accept of the same offer blocked before dispatch)", got)
	}
	if !contains(result.ToolsFailedRejected, "accept_work") {
		t.Errorf("ToolsFailedRejected should include the blocked re-accept, got %v", result.ToolsFailedRejected)
	}
}

// A decline_work of an offer already accepted this tick is blocked before dispatch
// — the cross-tool shared-labor-id key in action (the answer was given, in either
// direction, so the second is a no-op).
func TestHarness_LaborResolution_RejectsDeclineAfterAccept(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "accept_work", `{"labor_id":1}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "decline_work", `{"labor_id":1}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, log := newLaborResolutionHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if got := *log; len(got) != 1 || got[0] != "accept_work" {
		t.Errorf("dispatched: got %v, want [accept_work] (decline of the already-answered offer blocked before dispatch)", got)
	}
	if !contains(result.ToolsFailedRejected, "decline_work") {
		t.Errorf("ToolsFailedRejected should include the blocked decline_work, got %v", result.ToolsFailedRejected)
	}
}

// Per-offer scope: answering a DIFFERENT offer is always allowed — the guard only
// ever blocks a second answer to the SAME labor id.
func TestHarness_LaborResolution_DifferentOfferAllowed(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "accept_work", `{"labor_id":1}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "decline_work", `{"labor_id":2}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, log := newLaborResolutionHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if got := *log; len(got) != 2 {
		t.Errorf("dispatched: got %v, want both accept_work + decline_work (distinct offers each allowed)", got)
	}
}
