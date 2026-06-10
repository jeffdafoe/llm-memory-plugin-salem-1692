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

// TestRender_PayOfferSection_AboveAffordances (ZBBS-HOME-424): the decision
// section renders ABOVE the affordance dumps (satiation, lodging) — a buyer's
// coin on the table is the seller's most actionable fact, and burying it
// under eat/drink cues let a hungry seller ignore a waiting customer for
// minutes. The triage coda carries the matching settle-first imperative.
func TestRender_PayOfferSection_AboveAffordances(t *testing.T) {
	p := Payload{
		ActorID:           "seller",
		Actor:             ActorView{State: sim.StateIdle},
		Warrants:          []sim.WarrantMeta{payOfferWarrant(17, "bob", "stew", 2, 12, true)},
		WarrantActorNames: map[sim.ActorID]string{"bob": "bob"},
		Satiation: &SatiationView{Needs: []SatiationNeedView{{
			Need: "hunger", Verb: "eat",
			OwnStock: []OwnStockItem{{Label: "bread", Magnitude: 6}},
		}}},
		Baseline: BaselinePresent,
	}
	out := combinedPrompt(Render(p, DefaultRenderConfig()))

	offerIdx := strings.Index(out, "## Offers awaiting your decision")
	eatIdx := strings.Index(out, "## What you can eat or drink")
	if offerIdx == -1 || eatIdx == -1 {
		t.Fatalf("both sections must render (offer %d, eat %d)\n%s", offerIdx, eatIdx, out)
	}
	if offerIdx > eatIdx {
		t.Errorf("decision section must render above the satiation dump (offer %d, eat %d)\n%s", offerIdx, eatIdx, out)
	}
	if !strings.Contains(out, "A buyer's offer awaits your answer — settle it first") {
		t.Errorf("triage coda missing the settle-first imperative\n%s", out)
	}
}

// TestRender_QuoteWarrantLine_CarriesQuoteID (ZBBS-HOME-424): the targeted-
// quote warrant line names the fast-path take with its quote_id. Without it
// the buyer model answered a standing quote with a bare pay_with_item,
// minting a crossing offer that deadlocked against the quote.
func TestRender_QuoteWarrantLine_CarriesQuoteID(t *testing.T) {
	quote := sim.WarrantMeta{
		TriggerActorID: "john",
		Reason: sim.SceneQuoteTargetedWarrantReason{
			QuoteID: 9, SellerID: "john", ItemKind: "water", Qty: 1, Amount: 4, ConsumeNow: true,
		},
		SourceEventID: 31,
	}
	out := combinedPrompt(Render(Payload{
		ActorID:           "hannah",
		Warrants:          []sim.WarrantMeta{quote},
		WarrantActorNames: map[sim.ActorID]string{"john": "John Ellis"},
		Baseline:          BaselinePresent,
	}, DefaultRenderConfig()))

	if !strings.Contains(out, "John Ellis offers you water for 4 coins.") {
		t.Errorf("quote warrant line missing terms\n%s", out)
	}
	if !strings.Contains(out, "quote_id 9") || !strings.Contains(out, "settles at once") {
		t.Errorf("quote warrant line missing the fast-path take instruction\n%s", out)
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

// TestRender_PayOffer_Barter — a barter offer (goods, or coins + goods)
// renders the goods the seller is being asked to weigh (ZBBS-HOME-393),
// joined into the payment phrase, with the load-bearing ledger_id intact.
func TestRender_PayOffer_Barter(t *testing.T) {
	// Pure barter: 5 nails for 1 stew.
	pureGoods := sim.WarrantMeta{
		TriggerActorID: "bob",
		Reason: sim.PayOfferWarrantReason{
			LedgerID: 7, Buyer: "bob", Item: "stew", Qty: 1, Amount: 0,
			PayItems: []sim.ItemKindQty{{Kind: "nail", Qty: 5}},
		},
		SourceEventID: 7,
	}
	out := combinedPrompt(Render(Payload{
		ActorID:           "seller",
		Warrants:          []sim.WarrantMeta{pureGoods},
		WarrantActorNames: map[sim.ActorID]string{"bob": "bob"},
		Baseline:          BaselinePresent,
	}, DefaultRenderConfig()))
	if !strings.Contains(out, "offers 5 nail for 1 stew") {
		t.Errorf("pure-barter offer line missing goods\n%s", out)
	}
	if !strings.Contains(out, "offer id 7") {
		t.Errorf("ledger_id missing on barter offer\n%s", out)
	}

	// Mixed: 3 nails + 2 coins.
	mixed := sim.WarrantMeta{
		TriggerActorID: "bob",
		Reason: sim.PayOfferWarrantReason{
			LedgerID: 8, Buyer: "bob", Item: "ale", Qty: 1, Amount: 2,
			PayItems: []sim.ItemKindQty{{Kind: "nail", Qty: 3}},
		},
		SourceEventID: 8,
	}
	out = combinedPrompt(Render(Payload{
		ActorID:           "seller",
		Warrants:          []sim.WarrantMeta{mixed},
		WarrantActorNames: map[sim.ActorID]string{"bob": "bob"},
		Baseline:          BaselinePresent,
	}, DefaultRenderConfig()))
	if !strings.Contains(out, "offers 3 nail and 2 coins for 1 ale") {
		t.Errorf("mixed coins+goods offer line wrong\n%s", out)
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
