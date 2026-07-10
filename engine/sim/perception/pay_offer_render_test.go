package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// payOfferReason builds a seller-side pending-offer view entry for tests —
// the shape Build's snap.PayLedger scan (buildPayOffersForMe) produces.
func payOfferReason(ledger sim.LedgerID, buyer sim.ActorID, item sim.ItemKind, qty, amount int, consumeNow bool) sim.PayOfferWarrantReason {
	return sim.PayOfferWarrantReason{
		LedgerID:   ledger,
		Buyer:      buyer,
		Item:       item,
		Qty:        qty,
		Amount:     amount,
		ConsumeNow: consumeNow,
	}
}

// payOfferWarrant builds a seller-side pending-offer warrant for tests.
func payOfferWarrant(ledger sim.LedgerID, buyer sim.ActorID, item sim.ItemKind, qty, amount int, consumeNow bool) sim.WarrantMeta {
	return sim.WarrantMeta{
		TriggerActorID: buyer,
		Reason:         payOfferReason(ledger, buyer, item, qty, amount, consumeNow),
		SourceEventID:  sim.EventID(ledger),
	}
}

// TestPendingPayOffers_ReadsStandingViewNotWarrants — the shared predicate
// (the same set both the render section and the handlers tool-gate key off,
// so they can't drift) returns the standing ledger view, and ONLY the
// standing ledger view: a pay-offer warrant in the consumed batch without a
// matching view entry contributes nothing. ZBBS-HOME-453 — the warrant is a
// one-shot wake-up; the view is the cross-tick memory.
func TestPendingPayOffers_ReadsStandingViewNotWarrants(t *testing.T) {
	p := Payload{
		ActorID:  "seller",
		Warrants: []sim.WarrantMeta{speechWarrant(1, "s1", "bob", "hello")},
		PayOffersForMe: []sim.PayOfferWarrantReason{
			payOfferReason(17, "bob", "stew", 2, 12, true),
			payOfferReason(18, "cara", "ale", 1, 4, false),
		},
	}
	offers := PendingPayOffers(p)
	if len(offers) != 2 {
		t.Fatalf("PendingPayOffers len = %d, want 2", len(offers))
	}
	if offers[0].LedgerID != 17 || offers[1].LedgerID != 18 {
		t.Errorf("offer ledger ids = %d, %d; want 17, 18", offers[0].LedgerID, offers[1].LedgerID)
	}

	// A warrant alone (no standing view entry) no longer drives the predicate.
	warrantOnly := Payload{ActorID: "seller", Warrants: []sim.WarrantMeta{payOfferWarrant(17, "bob", "stew", 2, 12, true)}}
	if got := PendingPayOffers(warrantOnly); len(got) != 0 {
		t.Errorf("PendingPayOffers on warrant-only payload = %d, want 0 (view is the only source)", len(got))
	}
}

// TestRender_PayOfferDecisionSection — an offer renders in the dedicated
// "Offers awaiting your decision" section carrying the load-bearing
// ledger_id plus buyer/qty/item/amount/disposition, and does NOT also appear
// as a generic "since your last turn" warrant line.
func TestRender_PayOfferDecisionSection(t *testing.T) {
	// First-tick shape: the wake-up warrant AND the standing view both carry
	// the offer (Build scans the ledger on every tick, warranted or not).
	p := Payload{
		ActorID:           "seller",
		Actor:             ActorView{State: sim.StateIdle},
		Warrants:          []sim.WarrantMeta{payOfferWarrant(17, "bob", "stew", 2, 12, true)},
		PayOffersForMe:    []sim.PayOfferWarrantReason{payOfferReason(17, "bob", "stew", 2, 12, true)},
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
	// ZBBS-HOME-388 wanted the trade visible as a speech bubble and asked for the
	// speak TOOL by name, after the response. Since LLM-321 made speak terminal,
	// that instruction could not be followed: the response ended the tick and the
	// speak was skipped, or the speak ended it and the offer went unanswered.
	// LLM-350 folds the words into the response tool's own `say`, which reaches the
	// same utterance path — so the bubble still spawns, from a single call.
	if !strings.Contains(out, "Respond with accept_pay") {
		t.Errorf("pay offer response instruction missing\n%s", out)
	}
	if !strings.Contains(out, "the words you speak aloud in say") {
		t.Errorf("pay offer cue does not route the reply through `say`\n%s", out)
	}
	if strings.Contains(out, "Then also use speak") {
		t.Errorf("pay offer cue asks for a second terminal verb (LLM-350)\n%s", out)
	}
	// A solo pay offer covers the whole batch → the generic warrant block is
	// suppressed (no contradictory "nothing specific" line).
	if strings.Contains(out, "## Since your last turn") {
		t.Errorf("generic warrant block should be suppressed for a solo pay offer\n%s", out)
	}
	if strings.Contains(out, "nothing specific") {
		t.Errorf("routine-check-in line leaked despite a pending offer\n%s", out)
	}
}

// TestRender_PayOfferStockAnnotation (ZBBS-HOME-459 / LLM-303): renderPayOffers
// turns a Payload.PayOfferShortfalls entry into the offer-line warning — "you hold
// only N <kind>" above zero, "you hold no <plural>" at zero (the non-vendor
// offeree case). No entry → no annotation. The shortfall itself is computed in
// Build (buildPayOfferShortfalls, tested below); this pins the render copy.
func TestRender_PayOfferStockAnnotation(t *testing.T) {
	render := func(item sim.ItemKind, askQty int, sf *StockShortfall) string {
		p := Payload{
			ActorID:           "seller",
			Actor:             ActorView{State: sim.StateIdle},
			PayOffersForMe:    []sim.PayOfferWarrantReason{payOfferReason(7, "john", item, askQty, 250, false)},
			WarrantActorNames: map[sim.ActorID]string{"john": "John Ellis"},
			Baseline:          BaselinePresent,
		}
		if sf != nil {
			p.PayOfferShortfalls = map[sim.LedgerID]StockShortfall{7: *sf}
		}
		return combinedPrompt(Render(p, DefaultRenderConfig()))
	}
	// Short by some (holds 20 of 25 asked) → "you hold only 20 meat".
	if out := render("meat", 25, &StockShortfall{Held: 20, Noun: "meat"}); !strings.Contains(out, "you hold only 20 meat") {
		t.Errorf("over-ask offer should surface the stock gap\n%s", out)
	}
	// Holds none of the asked kind → "you hold no nails" (the LLM-303 non-vendor case).
	if out := render("nail", 5, &StockShortfall{Held: 0, Noun: "nails"}); !strings.Contains(out, "you hold no nails") {
		t.Errorf("zero-held offeree should get the hold-no warning\n%s", out)
	}
	// No shortfall entry (sufficient stock) → no annotation at all.
	if out := render("meat", 5, nil); strings.Contains(out, "you hold only") || strings.Contains(out, "you hold no") {
		t.Errorf("no shortfall entry should not annotate\n%s", out)
	}
}

// TestBuildPayOfferShortfalls (LLM-303): the seller-side shortfall map fires for
// any real good the offeree is short on — INCLUDING zero held, the non-vendor case
// that used to be skipped — and excludes a service kind (no inventory backing) and
// a good the seller holds enough of.
func TestBuildPayOfferShortfalls(t *testing.T) {
	subject := &sim.ActorSnapshot{Inventory: map[sim.ItemKind]int{"meat": 20, "milk": 3}}
	snap := &sim.Snapshot{
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"nail":        {Name: "nail", DisplayLabelSingular: "nail", DisplayLabelPlural: "nails"},
			"meat":        {Name: "meat", DisplayLabelSingular: "meat", DisplayLabelPlural: "meat"},
			"nights_stay": {Name: "nights_stay", Capabilities: []string{"service", "lodging"}},
		},
	}
	offers := []sim.PayOfferWarrantReason{
		payOfferReason(1, "b", "nail", 5, 1, false),        // holds 0 → short, plural noun
		payOfferReason(2, "b", "meat", 25, 1, false),       // holds 20 < 25 → short
		payOfferReason(3, "b", "meat", 5, 1, false),        // holds 20 >= 5 → not short
		payOfferReason(4, "b", "nights_stay", 1, 6, false), // service → skipped
	}
	sf := buildPayOfferShortfalls(snap, offers, subject)
	if got, ok := sf[1]; !ok || got.Held != 0 || got.Noun != "nails" {
		t.Errorf("zero-held real good must be short with plural noun; got %+v ok=%v", got, ok)
	}
	if got, ok := sf[2]; !ok || got.Held != 20 {
		t.Errorf("partially-short good must carry the held count; got %+v ok=%v", got, ok)
	}
	if _, ok := sf[3]; ok {
		t.Errorf("a sufficiently-stocked good must not be flagged short")
	}
	if _, ok := sf[4]; ok {
		t.Errorf("a service kind must be skipped (no inventory backing)")
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
		PayOffersForMe:    []sim.PayOfferWarrantReason{payOfferReason(17, "bob", "stew", 2, 12, true)},
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
			QuoteID: 9, SellerID: "john", Lines: []sim.QuoteLine{{ItemKind: "water", Qty: 1}}, Amount: 4, ConsumeNow: true,
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

// TestRender_QuoteWarrantLine_BarterAlternative (LLM-136): a single-item coin
// quote also names the goods route — a coin-short buyer is pointed at a SEPARATE
// offer_trade with the concrete want_item, and told goods can't ride the
// quote_id. This is the highest-risk routing text (it broke the live
// coinless-lodger livelock), so it must carry the machine value, not "this".
func TestRender_QuoteWarrantLine_BarterAlternative(t *testing.T) {
	quote := sim.WarrantMeta{
		TriggerActorID: "john",
		Reason: sim.SceneQuoteTargetedWarrantReason{
			QuoteID: 2, SellerID: "john", Lines: []sim.QuoteLine{{ItemKind: "nights_stay", Qty: 1}}, Amount: 4,
		},
		SourceEventID: 33,
	}
	out := combinedPrompt(Render(Payload{
		ActorID:           "ezekiel",
		Warrants:          []sim.WarrantMeta{quote},
		WarrantActorNames: map[sim.ActorID]string{"john": "John Ellis"},
		Baseline:          BaselinePresent,
	}, DefaultRenderConfig()))

	// Coin path still named.
	if !strings.Contains(out, "quote_id 2") || !strings.Contains(out, "settles at once") {
		t.Errorf("quote line dropped the coin take instruction\n%s", out)
	}
	// Goods path: a separate offer_trade naming the concrete want_item, not "this".
	if !strings.Contains(out, "offer_trade") || !strings.Contains(out, `want_item "nights_stay"`) {
		t.Errorf("quote line missing the offer_trade goods alternative with the concrete want_item\n%s", out)
	}
	if strings.Contains(out, "as want_item") {
		t.Errorf("quote line used the vague 'this as want_item' phrasing\n%s", out)
	}
	// Routing guard: goods must not be attached to a quote_id (that path rejects).
	if !strings.Contains(out, "Don't put goods on a quote_id") {
		t.Errorf("quote line missing the do-not-attach-goods-to-quote_id guard\n%s", out)
	}
}

// TestRender_QuoteWarrantLine_BundleNoBarterAlt (LLM-136): a multi-item bundle
// quote stays coin-only — offer_trade takes a single item kind, so a bundle has
// no single want_item to name. The barter alternative is single-item-scoped.
func TestRender_QuoteWarrantLine_BundleNoBarterAlt(t *testing.T) {
	quote := sim.WarrantMeta{
		TriggerActorID: "john",
		Reason: sim.SceneQuoteTargetedWarrantReason{
			QuoteID: 7, SellerID: "john", Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 2}, {ItemKind: "stew", Qty: 1}}, Amount: 9,
		},
		SourceEventID: 34,
	}
	out := combinedPrompt(Render(Payload{
		ActorID:           "ezekiel",
		Warrants:          []sim.WarrantMeta{quote},
		WarrantActorNames: map[sim.ActorID]string{"john": "John Ellis"},
		Baseline:          BaselinePresent,
	}, DefaultRenderConfig()))

	if !strings.Contains(out, "quote_id 7") {
		t.Errorf("bundle quote line dropped the take instruction\n%s", out)
	}
	if strings.Contains(out, "offer_trade") {
		t.Errorf("bundle quote line should not advertise offer_trade (no single want_item)\n%s", out)
	}
}

// TestRender_QuoteWarrantLine_Overheard (ZBBS-HOME-431): a public quote that
// reached this actor via the huddle fan-out renders "offers" — not the
// targeted "offers you" — so an overheard ad isn't perceived as a direct
// address. The fast-path take instruction is unchanged.
func TestRender_QuoteWarrantLine_Overheard(t *testing.T) {
	quote := sim.WarrantMeta{
		TriggerActorID: "john",
		Reason: sim.SceneQuoteTargetedWarrantReason{
			QuoteID: 12, SellerID: "john", Lines: []sim.QuoteLine{{ItemKind: "bread", Qty: 1}}, Amount: 4, Overheard: true,
		},
		SourceEventID: 32,
	}
	out := combinedPrompt(Render(Payload{
		ActorID:           "ezekiel",
		Warrants:          []sim.WarrantMeta{quote},
		WarrantActorNames: map[sim.ActorID]string{"john": "John Ellis"},
		Baseline:          BaselinePresent,
	}, DefaultRenderConfig()))

	if !strings.Contains(out, "John Ellis offers bread for 4 coins.") {
		t.Errorf("overheard quote warrant line missing neutral terms\n%s", out)
	}
	if strings.Contains(out, "offers you") {
		t.Errorf("overheard quote rendered as a direct address\n%s", out)
	}
	if !strings.Contains(out, "quote_id 12") || !strings.Contains(out, "settles at once") {
		t.Errorf("overheard quote warrant line missing the fast-path take instruction\n%s", out)
	}
}

// TestRender_QuoteWarrantLine_EatHereFact (ZBBS-WORK-405): when the quoted
// kind is in the payload's EatHereKinds set, the line states the disposition
// fact so the buyer never plans a carry-out the clamp would quietly rewrite.
// Absent from the set (portable / unknown kind), the line is unchanged.
func TestRender_QuoteWarrantLine_EatHereFact(t *testing.T) {
	quote := sim.WarrantMeta{
		TriggerActorID: "john",
		Reason: sim.SceneQuoteTargetedWarrantReason{
			QuoteID: 9, SellerID: "john", Lines: []sim.QuoteLine{{ItemKind: "stew", Qty: 1}}, Amount: 4, ConsumeNow: true,
		},
		SourceEventID: 31,
	}
	p := Payload{
		ActorID:           "hannah",
		Warrants:          []sim.WarrantMeta{quote},
		WarrantActorNames: map[sim.ActorID]string{"john": "John Ellis"},
		Baseline:          BaselinePresent,
	}

	p.EatHereKinds = map[sim.ItemKind]bool{"stew": true}
	out := combinedPrompt(Render(p, DefaultRenderConfig()))
	if !strings.Contains(out, "John Ellis offers you stew for 4 coins, to eat here (it can't be carried away).") {
		t.Errorf("quote warrant line missing the eat-here fact\n%s", out)
	}

	p.EatHereKinds = nil
	out = combinedPrompt(Render(p, DefaultRenderConfig()))
	if !strings.Contains(out, "John Ellis offers you stew for 4 coins.") {
		t.Errorf("quote warrant line should be tag-free when the kind isn't eat-here-only\n%s", out)
	}
}

// TestRender_PayOfferSingularCoin — amount of 1 renders "coin", not "coins".
// Also exercises the warrant-less standing tick (view only, empty batch).
func TestRender_PayOfferSingularCoin(t *testing.T) {
	p := Payload{
		ActorID:        "seller",
		PayOffersForMe: []sim.PayOfferWarrantReason{payOfferReason(5, "bob", "ale", 1, 1, false)},
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
	pureGoods := sim.PayOfferWarrantReason{
		LedgerID: 7, Buyer: "bob", Item: "stew", Qty: 1, Amount: 0,
		PayItems: []sim.ItemKindQty{{Kind: "nail", Qty: 5}},
	}
	out := combinedPrompt(Render(Payload{
		ActorID:           "seller",
		PayOffersForMe:    []sim.PayOfferWarrantReason{pureGoods},
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
	mixed := sim.PayOfferWarrantReason{
		LedgerID: 8, Buyer: "bob", Item: "ale", Qty: 1, Amount: 2,
		PayItems: []sim.ItemKindQty{{Kind: "nail", Qty: 3}},
	}
	out = combinedPrompt(Render(Payload{
		ActorID:           "seller",
		PayOffersForMe:    []sim.PayOfferWarrantReason{mixed},
		WarrantActorNames: map[sim.ActorID]string{"bob": "bob"},
		Baseline:          BaselinePresent,
	}, DefaultRenderConfig()))
	if !strings.Contains(out, "offers 3 nail and 2 coins for 1 ale") {
		t.Errorf("mixed coins+goods offer line wrong\n%s", out)
	}
}

// TestRender_PayOfferPlusOtherWarrant — an offer and a non-offer warrant in
// the same batch both render: the offer in its decision section, the other in
// the generic "since your last turn" list (which is NOT suppressed).
func TestRender_PayOfferPlusOtherWarrant(t *testing.T) {
	p := Payload{
		ActorID: "seller",
		Warrants: []sim.WarrantMeta{
			payOfferWarrant(17, "bob", "stew", 2, 12, true),
			speechWarrant(20, "s1", "cara", "good evening"),
		},
		PayOffersForMe: []sim.PayOfferWarrantReason{payOfferReason(17, "bob", "stew", 2, 12, true)},
		Baseline:       BaselinePresent,
	}
	out := combinedPrompt(Render(p, DefaultRenderConfig()))

	if !strings.Contains(out, "## Offers awaiting your decision") || !strings.Contains(out, "offer id 17") {
		t.Errorf("offer section missing\n%s", out)
	}
	if !strings.Contains(out, "## Since your last turn") {
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

// TestRender_StandingOfferWithoutWarrant — the ZBBS-HOME-453 regression: a
// later tick whose consumed batch carries NO pay-offer warrant (the seller
// already burned it speaking — the 06-12 Ellis deadlock shape) still renders
// the full decision section off the standing ledger view, with the
// load-bearing ledger_id and the response instruction intact.
func TestRender_StandingOfferWithoutWarrant(t *testing.T) {
	p := Payload{
		ActorID:           "seller",
		Actor:             ActorView{State: sim.StateIdle},
		Warrants:          []sim.WarrantMeta{speechWarrant(21, "s1", "bob", "two fifty and that is robbery")},
		PayOffersForMe:    []sim.PayOfferWarrantReason{payOfferReason(17, "bob", "stew", 2, 12, false)},
		WarrantActorNames: map[sim.ActorID]string{"bob": "bob"},
		Baseline:          BaselinePresent,
	}
	out := combinedPrompt(Render(p, DefaultRenderConfig()))

	if !strings.Contains(out, "## Offers awaiting your decision") || !strings.Contains(out, "offer id 17") {
		t.Errorf("standing offer section missing on a warrant-less tick\n%s", out)
	}
	if !strings.Contains(out, "Respond with accept_pay") {
		t.Errorf("response instruction missing on a warrant-less tick\n%s", out)
	}
	// The triage coda keeps the settle-first imperative standing too.
	if !strings.Contains(out, "A buyer's offer awaits your answer — settle it first") {
		t.Errorf("triage settle-first imperative missing on a warrant-less tick\n%s", out)
	}
}
