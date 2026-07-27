package handlers

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_budget_forced_contract_test.go — LLM-542. Guards the premise that
// justifies discharging a rendered stimulus on TickStatusBudgetForced.
//
// sim.terminalStatusAnswered puts budget-forced on the ANSWERED side of the
// discharge gate, alongside success and done, on the strength of a claim about
// this package: both budget-forced assignment sites sit inside the round loop
// and are reached only AFTER at least one LLM round completed against this
// prompt. It is exhaustion, not failure — which is why it is safe to prune a
// post-emit warrant whose line the model saw, and why failed-after-render is
// NOT.
//
// sim.TestTerminalStatusAnsweredContract pins the predicate's membership;
// nothing there proves the harness invariant, so this does (code_review).
// If a future path ever ends a tick budget-forced without an LLM round,
// this fails and terminalStatusAnswered must drop the status.
func TestHarness_BudgetForcedAlwaysFollowsAnLLMRound(t *testing.T) {
	cases := []struct {
		name  string
		turns []llm.ScriptedTurn
		// iterationBudget for the harness under test.
		budget int
	}{
		{
			// Action-round exhaustion: every round commits, so actionRounds
			// climbs to the budget and the loop returns budget-forced.
			name:   "action rounds exhaust the budget",
			budget: 2,
			turns: []llm.ScriptedTurn{
				{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "move_to", `{"structure_name":"nowhere"}`)}}},
				{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "move_to", `{"structure_name":"nowhere"}`)}}},
			},
		},
		{
			// Hard-ceiling exhaustion: recall is an observation round and does
			// not consume the action budget, so the tick ends at
			// IterationBudget + MaxObservationRounds instead.
			name:   "observation rounds exhaust the hard ceiling",
			budget: 2,
			turns:  recallTurns(2 + DefaultMaxObservationRounds),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, cancel := newHarnessWorldWithAgent(t, "attempt-A", "zbbs-josiah")
			defer cancel()

			client := llm.NewFakeClient(tc.turns...)
			h, _ := newTestHarness(t, client, tc.budget, 0)

			result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

			if result.TerminalStatus != sim.TickStatusBudgetForced {
				t.Fatalf("status = %v, want BudgetForced", result.TerminalStatus)
			}
			if result.IterationCount < 1 {
				t.Errorf("IterationCount = %d, want >= 1 — budget-forced must mean the model "+
					"deliberated on this prompt and ran out of rounds, not that it never ran. "+
					"sim.terminalStatusAnswered discharges on this status; if this can be 0, "+
					"move budget-forced to the unanswered side.", result.IterationCount)
			}
			if !result.BudgetHit {
				t.Error("BudgetHit = false on a budget-forced completion")
			}
		})
	}
}

func recallTurns(n int) []llm.ScriptedTurn {
	turns := make([]llm.ScriptedTurn, n)
	for i := range turns {
		turns[i] = llm.ScriptedTurn{Response: llm.Response{
			ToolCalls: []llm.RawToolCall{newToolCall("c"+string(rune('a'+i)), 0, "recall", `{}`)},
		}}
	}
	return turns
}
