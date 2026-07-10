package handlers

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// decline_counter_pay_result_test.go — LLM-13, the HOME-473 follow-up. The two
// sibling seller pay-responses (decline_pay, counter_pay) resolve to a bare
// sim.PayLedgerState the same way accept_pay does, and before that change fell
// through to a plain [ok] — a declined or countered offer with no spoken word
// reads to the buyer as being ignored. commitResultContent reports each outcome
// and forbids the re-fire.
//
// LLM-350: these results no longer ask for "a brief spoken beat + done()". Every
// pay response is terminal-on-success, so the tick ends the instant one lands and
// neither verb was ever reachable. The words ride on each tool's own `say`, and
// come back echoed on the result instead.

func TestCommitResultContent_DeclinePay_DeclinedReportsRefusal(t *testing.T) {
	got := commitResultContent(&ValidatedCall{Name: "decline_pay"}, sim.PayLedgerStateDeclined)
	if got == "[ok]" {
		t.Fatalf("declined decline_pay returned a bare [ok] with no refusal report")
	}
	for _, want := range []string{"declined", "Do not decline again"} {
		if !strings.Contains(got, want) {
			t.Errorf("declined decline_pay result %q missing %q", got, want)
		}
	}
	for _, banned := range []string{"word of refusal", "done()"} {
		if strings.Contains(got, banned) {
			t.Errorf("declined decline_pay result %q asks for %q — unreachable after the terminal decline (LLM-350)", got, banned)
		}
	}
	// The refusal the seller actually spoke comes back on the result.
	got = commitResultContent(
		&ValidatedCall{Name: "decline_pay", DecodedArgs: DeclinePayArgs{LedgerID: 1, Say: "None to spare today."}},
		payResponseResult{State: sim.PayLedgerStateDeclined, Announced: true})
	if !strings.Contains(got, `You said: "None to spare today."`) {
		t.Errorf("declined decline_pay result %q does not echo the spoken refusal", got)
	}
}

func TestCommitResultContent_CounterPay_CounteredReportsWait(t *testing.T) {
	got := commitResultContent(&ValidatedCall{Name: "counter_pay"}, sim.PayLedgerStateCountered)
	if got == "[ok]" {
		t.Fatalf("countered counter_pay returned a bare [ok] with no report")
	}
	for _, want := range []string{"counter stands", "Await their answer", "Do not counter again"} {
		if !strings.Contains(got, want) {
			t.Errorf("countered counter_pay result %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "call speak") || strings.Contains(got, "done()") {
		t.Errorf("countered counter_pay result %q asks for a verb the terminal counter has "+
			"already made unreachable (LLM-350)", got)
	}
	got = commitResultContent(
		&ValidatedCall{Name: "counter_pay", DecodedArgs: CounterPayArgs{LedgerID: 1, Amount: 6, Say: "Six, and it's yours."}},
		payResponseResult{State: sim.PayLedgerStateCountered, Announced: true})
	if !strings.Contains(got, `You said: "Six, and it's yours."`) {
		t.Errorf("countered counter_pay result %q does not echo the spoken terms", got)
	}
}

// A non-increasing pure-coin counter coerces to an accept in sim.CounterPay, so
// the sale settles under the counter_pay name. It must earn the same settle
// report a real accept_pay does — the gap the HOME-473 ticket glossed.
func TestCommitResultContent_CounterPay_CoercedAcceptReportsSettle(t *testing.T) {
	got := commitResultContent(&ValidatedCall{Name: "counter_pay"}, sim.PayLedgerStateAccepted)
	if got == "[ok]" {
		t.Fatalf("coerced-accept counter_pay returned a bare [ok] with no settle report")
	}
	for _, want := range []string{"settled", "Do not counter again"} {
		if !strings.Contains(got, want) {
			t.Errorf("coerced-accept counter_pay result %q missing %q", got, want)
		}
	}
	for _, banned := range []string{"Say a brief word", "done()"} {
		if strings.Contains(got, banned) {
			t.Errorf("coerced-accept counter_pay result %q asks for %q — unreachable (LLM-350)", got, banned)
		}
	}
}

// That same coercion can fail a gate at settle time (no stock, buyer short of
// coins, either party moved on, offer lapsed) — it flips to a fell-through
// terminal that settles under the counter_pay name. Before LLM-302 that dropped
// to a bare [ok] reading as a completed sale (the accept_pay misread that let a
// seller "confirm" goods it never held); commitResultContent now reports the
// real outcome, reusing accept_pay's fell-through echoes. Goods-shortfall is
// omitted — the coercion is pure-coin, so the buyer never owes barter goods.
func TestCommitResultContent_CounterPay_CoercionFellThroughReported(t *testing.T) {
	cases := []struct {
		state sim.PayLedgerState
		want  string
	}{
		{sim.PayLedgerStateFailedInsufficientStock, "enough stock"},
		{sim.PayLedgerStateFailedInsufficientFunds, "couldn't cover"},
		{sim.PayLedgerStateFailedUnavailable, "moved on"},
		{sim.PayLedgerStateExpired, "expired"},
	}
	for _, tc := range cases {
		got := commitResultContent(&ValidatedCall{Name: "counter_pay"}, tc.state)
		if got == "[ok]" {
			t.Fatalf("state %s: coerced counter_pay returned a bare [ok]", tc.state)
		}
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
