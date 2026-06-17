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

// A genuine acceptance (ZBBS-HOME-473) steers the seller to voice a brief
// handover then done(), and forbids re-accepting — a bare [ok] left the weak
// model re-firing accept_pay to the budget and closing the sale mute (live:
// Josiah×Prudence bread, the seller accepted then walked off without a word).
func TestCommitResultContent_AcceptPay_AcceptedSteersHandover(t *testing.T) {
	got := commitResultContent(&ValidatedCall{Name: "accept_pay"}, sim.PayLedgerStateAccepted)
	if got == "[ok]" {
		t.Fatalf("accepted accept_pay returned a bare [ok] with no handover steer")
	}
	for _, want := range []string{"settled", "Say a brief word", "done()", "Do not accept again"} {
		if !strings.Contains(got, want) {
			t.Errorf("accepted accept_pay result %q missing %q", got, want)
		}
	}
}
