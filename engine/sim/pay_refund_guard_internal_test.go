package sim

import (
	"testing"
	"time"
)

// pay_refund_guard_internal_test.go — LLM-659. The memo classifier behind the
// bare-pay refund guard, and the World-side coin read it depends on.

// The classifier reads only whether the memo CLAIMS the coin is going back.
// The ordinary debt-memo shapes — the payer owing, a purchase "in return for"
// goods — are the ones it must leave alone, because they are most of the memos
// a bare pay carries.
func TestIsRepaymentClaim(t *testing.T) {
	cases := []struct {
		memo string
		want bool
	}{
		{"refund for the whetstone that never arrived", true},
		{"Refunding your deposit", true},
		{"repayment of the loan", true},
		{"paying you back for the ale", true},
		{"the coins I said I'd give back", true},
		{"handing back your deposit", true},
		{"returning your coins", true},
		{"your money back", true},
		{"to reimburse you for the flour", true},

		{"", false},
		{"ale", false},
		{"in return for the ale", false},
		{"what I owe you for the flour", false},
		{"the coins I owed you", false},
		{"welcome back to the tavern", false},
		{"news from back at the mill", false},
		{"a tip for the news", false},
		{"the repair of my shovel", false},
	}
	for _, c := range cases {
		if got := isRepaymentClaim(c.memo); got != c.want {
			t.Errorf("isRepaymentClaim(%q) = %v, want %v", c.memo, got, c.want)
		}
	}
}

// The Command reads the live record on the world goroutine; perception reads
// the published snapshot. Both go through coinDealingsFrom, and this pins that
// the World read applies the same window the Snapshot read does — including
// the default when the setting is unset.
func TestWorldCoinDealingsFor_MatchesSnapshotRead(t *testing.T) {
	w := newCoinWorld(0)
	at := time.Date(2026, 9, 14, 16, 0, 0, 0, time.UTC)
	w.RecordCoinPaid("josiah", "lewis", 5, at.Add(-3*24*time.Hour), CoinPaymentUnstated)
	w.RecordCoinPaid("josiah", "lewis", 2, at.Add(-9*24*time.Hour), CoinPaymentUnstated)

	got := w.CoinDealingsFor("lewis", "josiah", at)
	if got.ReceivedCount != 1 || got.ReceivedTotal != 5 {
		t.Fatalf("World read = %+v, want one 5-coin payment inside the default window", got)
	}
	snap := &Snapshot{CoinRecord: w.CoinRecord, CoinRecordWindow: w.Settings.CoinRecordWindow}
	if want := snap.CoinDealingsFor("lewis", "josiah", at); got != want {
		t.Errorf("World read %+v != Snapshot read %+v", got, want)
	}
	if d := w.CoinDealingsFor("lewis", "nobody", at); d.Any() {
		t.Errorf("unknown pair = %+v, want empty", d)
	}
	var nilWorld *World
	if d := nilWorld.CoinDealingsFor("lewis", "josiah", at); d.Any() {
		t.Errorf("nil world = %+v, want empty", d)
	}
}
