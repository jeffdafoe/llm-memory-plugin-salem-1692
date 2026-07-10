package handlers

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// accept_pay_result_test.go — ZBBS-WORK-432 Fix 2. sim.AcceptPay returns the
// resolved PayLedgerState with a nil error: a gate-fail (e.g. the seller is out
// of stock) is a RESOLUTION, not a tool error. commitResultContent must tell the
// seller the sale fell through instead of a bare [ok] that reads as "accepted"
// (the 271 dry-seller case: Josiah "accepted" water he no longer had and was
// told [ok], learning nothing).

func TestCommitResultContent_AcceptPay_FailedTerminalsReported(t *testing.T) {
	cases := []struct {
		state sim.PayLedgerState
		want  string
	}{
		{sim.PayLedgerStateFailedInsufficientStock, "enough stock"},
		{sim.PayLedgerStateFailedInsufficientFunds, "couldn't cover"},
		{sim.PayLedgerStateFailedInsufficientGoods, "the goods they offered"},
		{sim.PayLedgerStateFailedUnavailable, "moved on"},
		{sim.PayLedgerStateExpired, "expired"},
	}
	for _, tc := range cases {
		got := commitResultContent(&ValidatedCall{Name: "accept_pay"}, tc.state)
		if !strings.Contains(got, "fell through") &&
			!strings.Contains(got, "couldn't be completed") &&
			!strings.Contains(got, "too late") {
			t.Errorf("state %s: result %q should say the sale didn't complete", tc.state, got)
		}
		if !strings.Contains(got, tc.want) {
			t.Errorf("state %s: result %q missing %q", tc.state, got, tc.want)
		}
	}
}

// A genuine acceptance (ZBBS-HOME-473) reports the settle and forbids
// re-accepting — a bare [ok] left the weak model re-firing accept_pay to the
// budget and closing the sale mute (live: Josiah×Prudence bread, the seller
// accepted then walked off without a word).
//
// It does NOT ask for a speak, and does not ask for done(). accept_pay is
// terminal-on-success, so the tick returns the instant it lands and the model
// never gets a round in which to obey either — the old "Say a brief word …, then
// call done()" tail was text no NPC could act on (LLM-350). The seller's word
// rides on accept_pay's own `say` instead; the echo is covered below.
func TestCommitResultContent_AcceptPay_AcceptedReportsSettle(t *testing.T) {
	got := commitResultContent(&ValidatedCall{Name: "accept_pay"}, sim.PayLedgerStateAccepted)
	if got == "[ok]" {
		t.Fatalf("accepted accept_pay returned a bare [ok] with no settle report")
	}
	for _, want := range []string{"settled", "Do not accept again"} {
		if !strings.Contains(got, want) {
			t.Errorf("accepted accept_pay result %q missing %q", got, want)
		}
	}
	for _, banned := range []string{"Say a brief word", "done()"} {
		if strings.Contains(got, banned) {
			t.Errorf("accepted accept_pay result %q asks for %q, which the terminal accept "+
				"has already made unreachable (LLM-350)", got, banned)
		}
	}
}

// The seller's folded `say` is echoed back on the result — the only signal the
// model gets that the room heard it — and a refused line says so rather than
// letting the seller believe it spoke.
func TestCommitResultContent_AcceptPay_EchoesSaid(t *testing.T) {
	vc := &ValidatedCall{
		Name:        "accept_pay",
		DecodedArgs: AcceptPayArgs{LedgerID: 1, Say: "Four coins it is."},
	}
	got := commitResultContent(vc, payResponseResult{
		State: sim.PayLedgerStateAccepted, Announced: true,
	})
	if !strings.Contains(got, `You said: "Four coins it is."`) {
		t.Errorf("result %q does not echo the seller's spoken line", got)
	}

	got = commitResultContent(vc, payResponseResult{
		State: sim.PayLedgerStateAccepted, SayRefused: "you are walking",
	})
	if !strings.Contains(got, "Your words went unsaid: you are walking") {
		t.Errorf("result %q does not report the refused line, or drops SpeakTo's own reason", got)
	}
	if !strings.Contains(got, "settled") {
		t.Errorf("result %q dropped the settle report when the words were refused — the sale still stands", got)
	}

	// A wordless accept keeps the plain sentence.
	got = commitResultContent(&ValidatedCall{Name: "accept_pay", DecodedArgs: AcceptPayArgs{LedgerID: 1}},
		payResponseResult{State: sim.PayLedgerStateAccepted})
	if strings.Contains(got, "You said") || strings.Contains(got, "unsaid") {
		t.Errorf("wordless accept result %q invented an utterance", got)
	}
}
