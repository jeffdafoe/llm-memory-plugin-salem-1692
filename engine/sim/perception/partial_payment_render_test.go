package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// partial_payment_render_test.go — LLM-357. The three perception surfaces that
// must make a partial-payment commission legible: the seller's incoming-offer
// cue (the payment is a deposit, not a full-price sale), the seller's
// deliver cue (a balance to collect on handover), and the buyer's waiting cue
// (a balance they still owe and must bring).

// TestRenderPayOffers_PartialPaymentDepositClause: the seller's "## Offers
// awaiting your decision" line surfaces that only a deposit lands now, with the
// balance due on collection, so the seller weighs the deal honestly.
func TestRenderPayOffers_PartialPaymentDepositClause(t *testing.T) {
	var b strings.Builder
	offers := []sim.PayOfferWarrantReason{{
		LedgerID: 12, Buyer: "elizabeth", Item: "shovel", Qty: 3, Amount: 15, Deposit: 5,
	}}
	nameOf := func(id sim.ActorID) string {
		if id == "elizabeth" {
			return "Elizabeth Ellis"
		}
		return string(id)
	}
	renderPayOffers(&b, offers, nameOf, nil, nil)
	out := b.String()
	if !strings.Contains(out, "5 down now as a deposit") || !strings.Contains(out, "remaining 10") {
		t.Errorf("seller offer cue missing the deposit split:\n%s", out)
	}
}

// TestRenderOrders_PartialPaymentBalanceClauses: both the seller's deliver cue
// and the buyer's waiting cue carry the outstanding balance, in each party's
// voice — the seller collects it, the buyer brings it.
func TestRenderOrders_PartialPaymentBalanceClauses(t *testing.T) {
	now := time.Now().UTC()

	// Seller side: a forged, deliverable partial order shows the balance to collect.
	sellerView := OrderView{
		ID: 12, Item: "shovel", Qty: 3, BuyerName: "Elizabeth Ellis",
		BalanceDue: 10, DepositPaid: 5,
	}
	var sb strings.Builder
	renderOrdersReadyToHandOver(&sb, []OrderView{sellerView}, now)
	if got := sb.String(); !strings.Contains(got, "5 coins down") || !strings.Contains(got, "10 still to collect") {
		t.Errorf("seller deliver cue missing balance clause:\n%s", got)
	}

	// Buyer side: the waiting-on cue shows what they still owe.
	buyerView := OrderView{
		ID: 12, Item: "shovel", Qty: 3, SellerName: "Ezekiel Crane",
		BalanceDue: 10, DepositPaid: 5,
	}
	var bb strings.Builder
	renderOrdersWaitingOn(&bb, []OrderView{buyerView}, now)
	if got := bb.String(); !strings.Contains(got, "5 coins down") || !strings.Contains(got, "10 still to settle") {
		t.Errorf("buyer waiting cue missing balance clause:\n%s", got)
	}

	// A full-prepay order (no balance) carries NO balance clause on either side.
	full := OrderView{ID: 13, Item: "nail", Qty: 1, BuyerName: "Bob"}
	var fb strings.Builder
	renderOrdersReadyToHandOver(&fb, []OrderView{full}, now)
	if got := fb.String(); strings.Contains(got, "deposit") || strings.Contains(got, "to collect") {
		t.Errorf("full-prepay order should not render a balance clause:\n%s", got)
	}
}
