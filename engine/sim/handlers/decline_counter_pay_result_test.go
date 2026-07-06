package handlers

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// decline_counter_pay_result_test.go — LLM-13, the HOME-473 follow-up. The two
// sibling seller pay-responses (decline_pay, counter_pay) resolve to a bare
// sim.PayLedgerState the same way accept_pay does, and before this change fell
// through to a plain [ok] — a declined or countered offer with no spoken word
// reads to the buyer as being ignored. commitResultContent now steers each to a
// brief spoken beat + done() and forbids the re-fire.

func TestCommitResultContent_DeclinePay_DeclinedSteersRefusal(t *testing.T) {
	got := commitResultContent(&ValidatedCall{Name: "decline_pay"}, sim.PayLedgerStateDeclined)
	if got == "[ok]" {
		t.Fatalf("declined decline_pay returned a bare [ok] with no refusal steer")
	}
	for _, want := range []string{"declined", "word of refusal", "done()", "Do not decline again"} {
		if !strings.Contains(got, want) {
			t.Errorf("declined decline_pay result %q missing %q", got, want)
		}
	}
}

func TestCommitResultContent_CounterPay_CounteredSteersWait(t *testing.T) {
	got := commitResultContent(&ValidatedCall{Name: "counter_pay"}, sim.PayLedgerStateCountered)
	if got == "[ok]" {
		t.Fatalf("countered counter_pay returned a bare [ok] with no steer")
	}
	for _, want := range []string{"counter stands", "done()", "Do not counter again"} {
		if !strings.Contains(got, want) {
			t.Errorf("countered counter_pay result %q missing %q", got, want)
		}
	}
}

// A non-increasing pure-coin counter coerces to an accept in sim.CounterPay, so
// the sale settles under the counter_pay name. It must earn the same handover
// steer a real accept_pay does — the gap the HOME-473 ticket glossed.
func TestCommitResultContent_CounterPay_CoercedAcceptSteersHandover(t *testing.T) {
	got := commitResultContent(&ValidatedCall{Name: "counter_pay"}, sim.PayLedgerStateAccepted)
	if got == "[ok]" {
		t.Fatalf("coerced-accept counter_pay returned a bare [ok] with no handover steer")
	}
	for _, want := range []string{"settled", "Say a brief word", "done()", "Do not counter again"} {
		if !strings.Contains(got, want) {
			t.Errorf("coerced-accept counter_pay result %q missing %q", got, want)
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
