package handlers

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// labor_result_test.go — LLM-163. The labor tools (solicit_work / accept_work /
// decline_work) must return a STEERED [ok], not a bare one, so the weak model
// stops re-firing them — the third recurrence of the offeredThisTick /
// quotedThisTick storm. Mirrors accept_pay_result_test.go.
//
// LLM-350: the "Say a brief word, then call done()" tail is gone from the
// accept_work / decline_work results. Both are terminal-on-success, so the tick
// returns the moment one lands and the model never got a round in which to obey
// either verb. The acceptor's words ride on the tool's own `say` instead, and
// come back echoed on the result — TestCommitResultContent_LaborSay_EchoesSaid.

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
			result: sim.LaborAcceptResult{State: sim.LaborStateWorking, WorkerName: "Lewis Walker", Reward: 5, Payment: "5 coins"},
			want:   "[ok] You hired Lewis Walker — they are at the work now for 5 coins, paid when they finish. Do not accept again.",
		},
		{
			// LLM-225: an in-kind reward names both legs in the hired steer.
			name:   "accept_work working with in-kind reward → payment phrase in steer",
			vc:     ValidatedCall{Name: "accept_work"},
			result: sim.LaborAcceptResult{State: sim.LaborStateWorking, WorkerName: "Anne Walker", Reward: 2, Payment: "1 porridge and 2 coins"},
			want:   "[ok] You hired Anne Walker — they are at the work now for 1 porridge and 2 coins, paid when they finish. Do not accept again.",
		},
		{
			// Defensive: a result built without the pre-formatted Payment falls
			// back to the coin leg rather than rendering "for ,".
			name:   "accept_work working without Payment falls back to coins",
			vc:     ValidatedCall{Name: "accept_work"},
			result: sim.LaborAcceptResult{State: sim.LaborStateWorking, WorkerName: "Lewis Walker", Reward: 5},
			want:   "[ok] You hired Lewis Walker — they are at the work now for 5 coins, paid when they finish. Do not accept again.",
		},
		{
			// The copy is role-neutral because either party may be the acceptor
			// (LLM-346) — "too late to take them on" reads wrong for a worker.
			name:   "accept_work expired → honest no-hire outcome, no false 'hired'",
			vc:     ValidatedCall{Name: "accept_work"},
			result: sim.LaborAcceptResult{State: sim.LaborStateExpired},
			want:   "[ok] That offer had already expired — too late to take it up.",
		},
		{
			name:   "accept_work failed_unavailable → honest no-hire outcome",
			vc:     ValidatedCall{Name: "accept_work"},
			result: sim.LaborAcceptResult{State: sim.LaborStateFailedUnavailable},
			want:   "[ok] That couldn't be arranged — one of you was no longer available, the worker was already at a job, or the employer couldn't cover the pay agreed.",
		},
		{
			// LLM-346: the worker is the acceptor, so the sentence is written from
			// their side — they took a job on, they did not hire anyone.
			name:   "accept_work working, worker accepted an offered job",
			vc:     ValidatedCall{Name: "accept_work"},
			result: sim.LaborAcceptResult{State: sim.LaborStateWorking, WorkerName: "Lewis Walker", EmployerName: "Prudence Ward", AcceptorIsWorker: true, Payment: "4 coins"},
			want:   "[ok] You took on the job for Prudence Ward — you are at the work now, paid 4 coins when you finish. Do not accept again.",
		},
		{
			name:   "accept_work en_route, worker must walk to the employer's post",
			vc:     ValidatedCall{Name: "accept_work"},
			result: sim.LaborAcceptResult{State: sim.LaborStateEnRoute, WorkerName: "Lewis Walker", EmployerName: "Prudence Ward", AcceptorIsWorker: true, Payment: "4 coins"},
			want:   "[ok] You took on the job for Prudence Ward — make your way to their workplace and get to work once you're both there, paid 4 coins when you finish. Do not accept again.",
		},
		{
			name:   "offer_work placed → the worker answers on their turn",
			vc:     ValidatedCall{Name: "offer_work"},
			result: sim.LaborOfferResult{ID: 3, State: sim.LaborStatePending, WorkerName: "Lewis Walker", Announced: true},
			want:   "[ok] Your offer of work to Lewis Walker stands — they will answer on their turn.",
		},
		{
			// The offer survives a refused `say` (SpeakTo's vocative / owed-a-reply
			// gates); the keeper is told her words did not carry rather than left to
			// assume the room heard her.
			name:   "offer_work placed but say refused → offer stands, refusal surfaced",
			vc:     ValidatedCall{Name: "offer_work"},
			result: sim.LaborOfferResult{ID: 3, State: sim.LaborStatePending, WorkerName: "Lewis Walker", SayRefused: "you are owed a reply"},
			want:   "[ok] Your offer of work to Lewis Walker stands — they will answer on their turn. Your words did not carry: you are owed a reply",
		},
		{
			name:   "decline_work declined → refusal steer",
			vc:     ValidatedCall{Name: "decline_work"},
			result: sim.LaborDeclineResult{State: sim.LaborStateDeclined},
			want:   "[ok] You declined the work. Do not decline again.",
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

// TestCommitResultContent_LaborSay_EchoesSaid pins the LLM-350 echo on the two
// labor responses: the acceptor's own words come back on the result (the only
// signal it gets that the room heard), and a refused line says so rather than
// letting the NPC believe it spoke. The hire or the decline stands either way.
func TestCommitResultContent_LaborSay_EchoesSaid(t *testing.T) {
	accepted := sim.LaborAcceptResult{
		State: sim.LaborStateEnRoute, WorkerName: "Lewis Walker", EmployerName: "Prudence Ward",
		AcceptorIsWorker: true, Reward: 4, Payment: "4 coins",
	}
	acceptVC := &ValidatedCall{
		Name:        "accept_work",
		DecodedArgs: AcceptWorkArgs{LaborID: 1, Say: "It would be my pleasure."},
	}

	accepted.Announced = true
	got := commitResultContent(acceptVC, accepted)
	if !strings.Contains(got, `You said: "It would be my pleasure."`) {
		t.Errorf("accept_work result %q does not echo the acceptor's line", got)
	}
	if !strings.Contains(got, "make your way to their workplace") {
		t.Errorf("accept_work result %q lost the relocate instruction", got)
	}

	accepted.Announced, accepted.SayRefused = false, "you are walking"
	got = commitResultContent(acceptVC, accepted)
	if !strings.Contains(got, "Your words went unsaid: you are walking") {
		t.Errorf("accept_work result %q does not report the refused line", got)
	}
	if !strings.Contains(got, "You took on the job") {
		t.Errorf("accept_work result %q dropped the hire when the words were refused — the hire stands", got)
	}

	declineVC := &ValidatedCall{
		Name:        "decline_work",
		DecodedArgs: DeclineWorkArgs{LaborID: 1, Say: "Not today."},
	}
	got = commitResultContent(declineVC, sim.LaborDeclineResult{
		ID: 1, State: sim.LaborStateDeclined, Announced: true,
	})
	if !strings.Contains(got, `You said: "Not today."`) {
		t.Errorf("decline_work result %q does not echo the spoken refusal", got)
	}

	// A wordless response invents nothing.
	got = commitResultContent(&ValidatedCall{Name: "decline_work", DecodedArgs: DeclineWorkArgs{LaborID: 1}},
		sim.LaborDeclineResult{ID: 1, State: sim.LaborStateDeclined})
	if strings.Contains(got, "You said") || strings.Contains(got, "unsaid") {
		t.Errorf("wordless decline_work result %q invented an utterance", got)
	}
}
