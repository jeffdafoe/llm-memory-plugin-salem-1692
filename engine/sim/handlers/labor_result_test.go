package handlers

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// labor_result_test.go — LLM-163. The labor tools (solicit_work / accept_work /
// decline_work) must return a STEERED [ok], not a bare one, so the weak model
// stops re-firing them — the third recurrence of the offeredThisTick /
// quotedThisTick storm. Mirrors accept_pay_result_test.go.

func TestCommitResultContent_LaborSteers(t *testing.T) {
	cases := []struct {
		name   string
		vc     ValidatedCall
		result any
		want   string
	}{
		{
			name:   "solicit_work pending → offer-on-the-table steer",
			vc:     ValidatedCall{Name: "solicit_work"},
			result: sim.LaborSolicitResult{State: sim.LaborStatePending, EmployerName: "Hannah Boggs"},
			want:   "[ok] Your offer of labor to Hannah Boggs is on the table — they will answer on their turn.",
		},
		{
			// LLM-193: an unaffordable solicit auto-declines at mint (the employer
			// hasn't the coin and is never woken) — tell the worker the real reason
			// so it looks elsewhere instead of re-asking the same empty purse.
			name:   "solicit_work declined → broke-employer steer",
			vc:     ValidatedCall{Name: "solicit_work"},
			result: sim.LaborSolicitResult{State: sim.LaborStateDeclined, EmployerName: "Prudence Ward"},
			want:   "[ok] Prudence Ward cannot pay your requested reward just now — look to another shop for work.",
		},
		{
			name:   "accept_work working → hired + handover steer",
			vc:     ValidatedCall{Name: "accept_work"},
			result: sim.LaborAcceptResult{State: sim.LaborStateWorking, WorkerName: "Lewis Walker", Reward: 5},
			want:   "[ok] You hired Lewis Walker — they are at the work now for 5 coins, paid when they finish. Say a brief word, then call done(). Do not accept again.",
		},
		{
			name:   "accept_work expired → honest no-hire outcome, no false 'hired'",
			vc:     ValidatedCall{Name: "accept_work"},
			result: sim.LaborAcceptResult{State: sim.LaborStateExpired},
			want:   "[ok] That offer had already expired — too late to take them on.",
		},
		{
			name:   "accept_work failed_unavailable → honest no-hire outcome",
			vc:     ValidatedCall{Name: "accept_work"},
			result: sim.LaborAcceptResult{State: sim.LaborStateFailedUnavailable},
			want:   "[ok] That couldn't be arranged — one of you was no longer available, they were already at a job, or you couldn't cover the coin.",
		},
		{
			name:   "decline_work declined → refusal steer",
			vc:     ValidatedCall{Name: "decline_work"},
			result: sim.LaborDeclineResult{State: sim.LaborStateDeclined},
			want:   "[ok] You declined the work. Say a brief word of refusal, then call done(). Do not decline again.",
		},
		{
			// Defensive: a wrong/unexpected result shape must degrade to the bare
			// [ok], not panic or assert state.
			name:   "wrong result type degrades to bare ok",
			vc:     ValidatedCall{Name: "solicit_work"},
			result: struct{ X int }{X: 1},
			want:   "[ok]",
		},
		{
			// A non-pending solicit result (shouldn't occur — solicit errors
			// otherwise) must not claim the offer stands.
			name:   "solicit_work non-pending degrades to bare ok",
			vc:     ValidatedCall{Name: "solicit_work"},
			result: sim.LaborSolicitResult{State: sim.LaborStateExpired, EmployerName: "Hannah Boggs"},
			want:   "[ok]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commitResultContent(&tc.vc, tc.result); got != tc.want {
				t.Errorf("commitResultContent\n got:  %q\n want: %q", got, tc.want)
			}
		})
	}
}
