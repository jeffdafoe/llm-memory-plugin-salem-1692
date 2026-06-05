package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// payOfferWarrant builds a seller-side pending-offer warrant for tests.
func payOfferWarrant(ledger sim.LedgerID, buyer sim.ActorID, item sim.ItemKind, qty, amount int, consumeNow bool) sim.WarrantMeta {
	return sim.WarrantMeta{
		TriggerActorID: buyer,
		Reason: sim.PayOfferWarrantReason{
			LedgerID:   ledger,
			Buyer:      buyer,
			Item:       item,
			Qty:        qty,
			Amount:     amount,
			ConsumeNow: consumeNow,
		},
		SourceEventID: sim.EventID(ledger),
	}
}

// TestPayOfferWarrants_FiltersBatch — the shared predicate returns only the
// pay-offer warrants in the consumed batch (and is the same set both the
// render section and the handlers tool-gate key off, so they can't drift).
func TestPayOfferWarrants_FiltersBatch(t *testing.T) {
	p := Payload{
		ActorID: "seller",
		Warrants: []sim.WarrantMeta{
			speechWarrant(1, "s1", "bob", "hello"),
			payOfferWarrant(17, "bob", "stew", 2, 12, true),
			payOfferWarrant(18, "cara", "ale", 1, 4, false),
		},
	}
	offers := PayOfferWarrants(p)
	if len(offers) != 2 {
		t.Fatalf("PayOfferWarrants len = %d, want 2", len(offers))
	}
	if offers[0].LedgerID != 17 || offers[1].LedgerID != 18 {
		t.Errorf("offer ledger ids = %d, %d; want 17, 18", offers[0].LedgerID, offers[1].LedgerID)
	}

	// Empty / no-offer batch.
	none := Payload{ActorID: "seller", Warrants: []sim.WarrantMeta{speechWarrant(1, "s1", "bob", "hi")}}
	if got := PayOfferWarrants(none); len(got) != 0 {
		t.Errorf("PayOfferWarrants on no-offer batch = %d, want 0", len(got))
	}
}

// TestRender_PayOfferDecisionSection — an offer renders in the dedicated
// "Offers awaiting your decision" section carrying the load-bearing
// ledger_id plus buyer/qty/item/amount/disposition, and does NOT also appear
// as a generic "what just happened" warrant line.
func TestRender_PayOfferDecisionSection(t *testing.T) {
	p := Payload{
		ActorID:           "seller",
		Actor:             ActorView{State: sim.StateIdle},
		Warrants:          []sim.WarrantMeta{payOfferWarrant(17, "bob", "stew", 2, 12, true)},
		WarrantActorNames: map[sim.ActorID]string{"bob": "bob"},
		Baseline:          BaselinePresent,
	}
	out := combinedPrompt(Render(p, DefaultRenderConfig()))

	if !strings.Contains(out, "## Offers awaiting your decision") {
		t.Errorf("offer decision section header missing\n%s", out)
	}
	// The ledger_id MUST be present — the model echoes it into accept_pay.
	if !strings.Contains(out, "offer id 17") {
		t.Errorf("ledger_id not rendered\n%s", out)
	}
	for _, want := range []string{"bob", "12 coins", "2 stew", "to consume now"} {
		if !strings.Contains(out, want) {
			t.Errorf("offer line missing %q\n%s", want, out)
		}
	}
	if !strings.Contains(out, "accept_pay") || !strings.Contains(out, "ledger_id") {
		t.Errorf("respond instruction missing\n%s", out)
	}
	// ZBBS-HOME-388: order the pay response before speech and name the speak TOOL
	// explicitly, so an NPC-to-NPC trade is visible as a bubble (bubbles spawn only
	// from speak; the pay_* frames render only for the PC's own transactions).
	if !strings.Contains(out, "Respond first with accept_pay") || !strings.Contains(out, "use speak") {
		t.Errorf("pay offer response should order pay response before speech\n%s", out)
	}
	// A solo pay offer covers the whole batch → the generic warrant block is
	// suppressed (no contradictory "nothing specific" line).
	if strings.Contains(out, "## What just happened") {
		t.Errorf("generic warrant block should be suppressed for a solo pay offer\n%s", out)
	}
	if strings.Contains(out, "nothing specific") {
		t.Errorf("routine-check-in line leaked despite a pending offer\n%s", out)
	}
}

// TestRender_PayOfferSingularCoin — amount of 1 renders "coin", not "coins".
func TestRender_PayOfferSingularCoin(t *testing.T) {
	p := Payload{
		ActorID:  "seller",
		Warrants: []sim.WarrantMeta{payOfferWarrant(5, "bob", "ale", 1, 1, false)},
	}
	out := combinedPrompt(Render(p, DefaultRenderConfig()))
	if !strings.Contains(out, "1 coin for") {
		t.Errorf("singular coin not rendered\n%s", out)
	}
	if !strings.Contains(out, "to keep") {
		t.Errorf("consume_now=false disposition not rendered\n%s", out)
	}
}

// TestRender_PayOfferPlusOtherWarrant — an offer and a non-offer warrant in
// the same batch both render: the offer in its decision section, the other in
// the generic "what just happened" list (which is NOT suppressed).
func TestRender_PayOfferPlusOtherWarrant(t *testing.T) {
	p := Payload{
		ActorID: "seller",
		Warrants: []sim.WarrantMeta{
			payOfferWarrant(17, "bob", "stew", 2, 12, true),
			speechWarrant(20, "s1", "cara", "good evening"),
		},
		Baseline: BaselinePresent,
	}
	out := combinedPrompt(Render(p, DefaultRenderConfig()))

	if !strings.Contains(out, "## Offers awaiting your decision") || !strings.Contains(out, "offer id 17") {
		t.Errorf("offer section missing\n%s", out)
	}
	if !strings.Contains(out, "## What just happened") {
		t.Errorf("generic warrant block missing (a non-offer warrant is present)\n%s", out)
	}
	if !strings.Contains(out, "good evening") {
		t.Errorf("co-batch speech warrant not rendered\n%s", out)
	}
	// The offer must not be double-rendered as a generic [pay_offer] line.
	if strings.Contains(out, "[pay_offer]") {
		t.Errorf("pay offer leaked into the generic warrant list\n%s", out)
	}
}
