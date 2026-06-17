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

// A genuine acceptance falls through to the generic [ok] — the transfer happened
// and next-tick perception (+ the buyer's "## Recently settled offers" view)
// reflects it.
func TestCommitResultContent_AcceptPay_AcceptedIsGenericOk(t *testing.T) {
	if got := commitResultContent(&ValidatedCall{Name: "accept_pay"}, sim.PayLedgerStateAccepted); got != "[ok]" {
		t.Errorf("accepted accept_pay = %q, want generic [ok]", got)
	}
}
