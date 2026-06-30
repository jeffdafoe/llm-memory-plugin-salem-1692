package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_solicit_refire_dedup_test.go — LLM-195: the same-tick FAILED-solicit
// guard (solicitAttemptedThisTick). solicit_work is terminal-on-success (LLM-180),
// so a placed offer ends the tick; the ONLY way to reach a second solicit_work in
// one tick is a FAILED first one (e.g. the co-resident gate bounced it). LLM-163's
// success-only solicitedThisTick never recorded a failed attempt, so the weak model
// re-fired the rejected offer to the round budget (live: solicit_work x6 to a
// co-resident "employer", rewards 5 then 10). This guard records the first attempt
// regardless of outcome and blocks the second — name-only, so a varied reward
// cannot dodge it the way it dodges a byte-identical guard.

// newSolicitRefireHarness registers solicit_work (terminal-on-success, like the real
// RegisterSolicitWork) with a command fn that ALWAYS fails on the world goroutine —
// standing in for the co-resident gate rejecting the offer — plus a no-op speak and
// a terminal done. Each dispatch is logged, so a call the guard blocks before
// dispatch leaves no entry.
func newSolicitRefireHarness(t *testing.T, client llm.Client) (*Harness, *[]string) {
	t.Helper()
	r := NewRegistry()
	var log []string
	// solicit_work: log the dispatch, then fail on the world goroutine. A failed
	// command is not a success, so terminal-on-success does NOT end the tick — exactly
	// the live shape where the co-resident gate bounces the offer and the turn rolls on.
	solicit := func(in HandlerInput) (sim.Command, error) {
		log = append(log, "solicit_work")
		return sim.Command{Fn: func(*sim.World) (any, error) {
			return nil, errors.New("you live with them — offer your labor to someone outside your own household")
		}}, nil
	}
	if err := r.RegisterCommit("solicit_work", json.RawMessage(`{"type":"object"}`), DecodeSolicitWorkArgs, solicit, true); err != nil {
		t.Fatalf("register solicit_work: %v", err)
	}
	// speak: a non-terminal no-op success, to prove the guard blocks only a SECOND
	// solicit and still lets a different action through after a failed one.
	speak := func(in HandlerInput) (sim.Command, error) {
		log = append(log, "speak")
		return sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }}, nil
	}
	if err := r.RegisterCommit("speak", json.RawMessage(`{"type":"object"}`), DecodeSpeakArgs, speak, false); err != nil {
		t.Fatalf("register speak: %v", err)
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

// A second solicit_work after the first one FAILED is rejected before dispatch — the
// LLM-195 storm: Anne re-offered to the same co-resident six times, each bounced by
// the co-resident gate, the weak model re-firing to the round budget. The re-fire
// here even varies the reward (5 then 10) to prove the guard is name-only and a
// changed argument does not slip past it.
func TestHarness_SolicitWork_RejectsRefireAfterFailure(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "solicit_work", `{"employer":"Patience Walker","reward":5,"duration_minutes":120}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "solicit_work", `{"employer":"Patience Walker","reward":10,"duration_minutes":120}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, log := newSolicitRefireHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if got := *log; len(got) != 1 || got[0] != "solicit_work" {
		t.Errorf("dispatched: got %v, want [solicit_work] (the re-fired offer is blocked before dispatch even with a different reward)", got)
	}
	if !contains(result.ToolsFailedRejected, "solicit_work") {
		t.Errorf("ToolsFailedRejected should include the blocked re-solicit, got %v", result.ToolsFailedRejected)
	}
}

// A DIFFERENT action after a failed solicit is still allowed — the guard caps solicit
// attempts at one per tick, it does not freeze the actor. The worker who got bounced
// can say a word (here) or do anything else, then call done().
func TestHarness_SolicitWork_AllowsOtherActionAfterFailure(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "solicit_work", `{"employer":"Patience Walker","reward":5,"duration_minutes":120}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "speak", `{"text":"Then I'll seek work elsewhere."}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, log := newSolicitRefireHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if got := *log; len(got) != 2 || got[0] != "solicit_work" || got[1] != "speak" {
		t.Errorf("dispatched: got %v, want [solicit_work speak] (a different action after a failed solicit is allowed)", got)
	}
}
