package perception

import (
	"sort"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_cue_gate_test.go — LLM-1007. A cue that tells the subject to "call
// pay_with_item" must only render on a turn where the tool is offered. gateTools
// advertises pay_with_item on exactly CanPayWithItem (handlers'
// TestCanPayWithItemMatchesGate), so checking the predicate here checks the gate.
// Live, the two disagreed in two ways: a buyer mid-walk was told to take a quote
// with pay_with_item (Silence Walker, Joseph Scott, Abraham Warren), and a buyer in
// the same building as a seller but with no huddle yet was told "Buy it now" with
// the tool withheld (Lewis Walker at the Blacksmith).

// TestGoldensPayCueOnlyWhenPayOffered holds the property over every scenario.
func TestGoldensPayCueOnlyWhenPayOffered(t *testing.T) {
	saw := false
	for _, sc := range perceptionScenarios {
		sc := sc
		snap, actorID, warrants := sc.build()
		p := Build(snap, actorID, warrants)
		out := combinedPrompt(Render(p, DefaultRenderConfig()))
		if !strings.Contains(out, "call pay_with_item") {
			continue
		}
		saw = true
		if !CanPayWithItem(p) {
			t.Errorf("scenario %q: the prompt says \"call pay_with_item\" but the tool is not offered (walking=%v huddle=%d namesSeller=%v visitorStripped=%v)",
				sc.name, p.Actor.InFlightMove != nil, len(p.Surroundings.HuddleMembers), NamesCoPresentSeller(p), p.VisitorCommerceStripped)
		}
	}
	if !saw {
		t.Fatal("no scenario renders \"call pay_with_item\"; the invariant checks nothing")
	}
}

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "buyer_walking_with_standing_quote",
			summary: "LLM-1007: buyer_sees_standing_quote_after_warrant_spent with the buyer mid-walk. The walking " +
				"gate withdraws pay_with_item and offer_trade, so the standing quote's take-instruction says 'You are " +
				"walking — call stop first to take it.' instead of naming a call she cannot make (live: Silence Walker, " +
				"Joseph Scott, Abraham Warren walked out on a quote whose line said call pay_with_item).",
			build: walkingSubject(buyerSeesStandingQuoteAfterWarrantSpent),
		},
		perceptionScenario{
			name: "farm_owner_walking_with_shovel_seller",
			summary: "LLM-1007: farm_owner_owes_upkeep_seller_present with the owner mid-walk. The co-present buy " +
				"says 'You are walking — call stop first if you want to buy from Ezekiel Crane now.' rather than 'Buy it " +
				"now — call pay_with_item' (live: Elizabeth Ellis, walking with the smith, 2026-11-20).",
			build: walkingSubject(farmOwnerOwesUpkeepSellerPresent),
		},
	)
}

// walkingSubject wraps a scenario builder and puts its subject mid-walk to a
// structure in the fixture (her workplace, else home, else the lowest structure
// id), so InFlightMove renders a real place.
func walkingSubject(build func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta)) func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return func() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
		snap, id, warrants := build()
		a := snap.Actors[id]
		dest := a.WorkStructureID
		if dest == "" {
			dest = a.HomeStructureID
		}
		if dest == "" {
			ids := make([]string, 0, len(snap.Structures))
			for sid := range snap.Structures {
				ids = append(ids, string(sid))
			}
			sort.Strings(ids)
			if len(ids) > 0 {
				dest = sim.StructureID(ids[0])
			}
		}
		a.MoveDestKind = sim.MoveDestinationStructureVisit
		a.MoveDestStructureID = dest
		return snap, id, warrants
	}
}

// TestCoPresentBuyActionableMatchesRender pins NamesCoPresentSeller (which opens
// the no-huddle pay gate) to what each co-present renderer actually prints: for
// every view and branch, a "call pay_with_item" line renders iff the view counts as
// naming an actionable seller (code_review, LLM-1007).
func TestCoPresentBuyActionableMatchesRender(t *testing.T) {
	const seller = "Ezekiel Crane"
	type tc struct {
		name   string
		p      Payload
		render func(*strings.Builder, Payload)
	}
	restock := func(b *strings.Builder, p Payload) { renderRestocking(b, p.Restocking) }
	stall := func(b *strings.Builder, p Payload) { renderStallRepairBuy(b, p.StallRepairBuy) }
	upkeep := func(b *strings.Builder, p Payload) { renderFarmUpkeep(b, p.FarmUpkeep) }
	clothes := func(b *strings.Builder, p Payload) { renderWorkClothes(b, p.WorkClothes) }
	ward := func(b *strings.Builder, p Payload) { renderWard(b, p.WardWant, nil) }
	wright := func(b *strings.Builder, p Payload) { renderWrightStoneBuy(b, p.WrightRounds) }
	blocks := []struct {
		name  string
		block copresentBuyBlock
	}{{"ok", copresentBuyOK}, {"no stock", copresentBuyBlockedNoStock}, {"coin", copresentBuyBlockedCoin}, {"terms", copresentBuyBlockedTerms}}

	var cases []tc
	for _, bl := range blocks {
		bl := bl
		cases = append(cases,
			tc{"restock " + bl.name, Payload{Restocking: &RestockingView{Items: []RestockItemView{{Kind: "salt", ItemLabel: "salt", Cap: 6, CoPresentSeller: seller, SellerStock: 6, Block: bl.block}}}}, restock},
			tc{"stall repair " + bl.name, Payload{StallRepairBuy: &StallRepairBuyView{NailsNeeded: 5, NailsShort: 5, CoPresentSeller: seller, SellerStock: 9, Block: bl.block}}, stall},
			tc{"farm upkeep " + bl.name, Payload{FarmUpkeep: &FarmUpkeepView{ShovelsOwed: 2, ShovelsShort: 2, CoPresentSeller: seller, SellerStock: 4, Block: bl.block}}, upkeep},
			tc{"ward " + bl.name, Payload{WardWant: &WardWantView{Item: "horseshoe", CoPresentSeller: seller, Block: bl.block}}, ward},
			tc{"wright " + bl.name, Payload{WrightRounds: &WrightRoundsView{NoStone: true, StonesShort: 1, CoPresentSeller: seller, SellerStock: 2, Block: bl.block}}, wright},
			tc{"clothes seller " + bl.name, Payload{WorkClothes: &WorkClothesView{Tier: sim.WarmGarmentThreadbare, Item: "vest", ItemLabel: "vest", CoPresentSeller: seller, Block: bl.block}}, clothes},
			tc{"clothes mender " + bl.name, Payload{WorkClothes: &WorkClothesView{Tier: sim.WarmGarmentThreadbare, MendItem: "mending", CoPresentMender: seller, MendBlock: bl.block}}, clothes},
		)
	}
	cases = append(cases,
		// A mend resolution renders only the mender arm, even with a seller named.
		tc{"clothes mend wins over a named seller", Payload{WorkClothes: &WorkClothesView{Tier: sim.WarmGarmentThreadbare, MendItem: "mending", CoPresentMender: seller, MendPendingOffer: true, Item: "vest", ItemLabel: "vest", CoPresentSeller: seller}}, clothes},
		tc{"clothes seller with no item", Payload{WorkClothes: &WorkClothesView{Tier: sim.WarmGarmentThreadbare, CoPresentSeller: seller}}, clothes},
	)
	cases = append(cases,
		tc{"restock pending offer", Payload{Restocking: &RestockingView{Items: []RestockItemView{{Kind: "salt", ItemLabel: "salt", Cap: 6, CoPresentSeller: seller, SellerStock: 6, PendingOfferToCoPresentSeller: true}}}}, restock},
		tc{"restock capped", Payload{Restocking: &RestockingView{Items: []RestockItemView{{Kind: "salt", ItemLabel: "salt", Cap: 6, CoPresentSeller: seller, SellerStock: 1}}}}, restock},
		tc{"stall repair pending offer", Payload{StallRepairBuy: &StallRepairBuyView{NailsNeeded: 5, NailsShort: 5, CoPresentSeller: seller, SellerStock: 9, PendingOffer: true}}, stall},
		tc{"farm upkeep pending offer", Payload{FarmUpkeep: &FarmUpkeepView{ShovelsOwed: 2, ShovelsShort: 2, CoPresentSeller: seller, SellerStock: 4, PendingOffer: true}}, upkeep},
		tc{"wright pending offer", Payload{WrightRounds: &WrightRoundsView{NoStone: true, StonesShort: 1, CoPresentSeller: seller, SellerStock: 2, PendingOffer: true}}, wright},
		tc{"wright restocking buys here", Payload{WrightRounds: &WrightRoundsView{NoStone: true, StonesShort: 1, CoPresentSeller: seller, SellerStock: 2, RestockingBuysHere: true}}, wright},
	)
	for _, c := range cases {
		var b strings.Builder
		c.render(&b, c.p)
		rendered := strings.Contains(b.String(), "call pay_with_item")
		if named := NamesCoPresentSeller(c.p); named != rendered {
			t.Errorf("%s: NamesCoPresentSeller = %v but a \"call pay_with_item\" line rendered = %v:\n%s", c.name, named, rendered, b.String())
		}
	}
}

// TestPayWithheldNeverNamesTheCall covers the review's regressions: a quote take
// with pay withheld keeps its quote_id and names no call (both the warrant line and
// the standing section), and a baker or a walking merchant visitor with an
// otherwise actionable co-present seller gets the withheld form, not "stop first"
// (stopping would not lift a commerce strip).
func TestPayWithheldNeverNamesTheCall(t *testing.T) {
	lines := []sim.QuoteLine{{ItemKind: "water", Qty: 1}}
	for _, pay := range []PayAvailability{PayWalking, PayWithheld} {
		got := quoteTakeInstruction(5, lines, 1, quoteTakeAbility{canBarter: true, pay: pay})
		if strings.Contains(got, "call pay_with_item") || strings.Contains(got, "offer_trade") || !strings.Contains(got, "quote_id 5") {
			t.Errorf("pay=%d: take instruction %q names a withheld call or drops the quote_id", pay, got)
		}
	}
	var b strings.Builder
	renderStandingQuotesToMe(&b, []StandingQuoteToMeView{{QuoteID: 7, SellerName: "Hannah Boggs", Lines: lines, Amount: 1}}, nil, quoteTakeAbility{pay: PayWithheld})
	if s := b.String(); strings.Contains(s, "call pay_with_item") || !strings.Contains(s, "quote_id 7") {
		t.Errorf("standing quote with pay withheld:\n%s", s)
	}

	seller := &RestockingView{Items: []RestockItemView{{Kind: "salt", ItemLabel: "salt", Cap: 6, CoPresentSeller: "Josiah Thorne", SellerStock: 6}}}
	walkingVisitor := Payload{Restocking: seller, VisitorCommerceStripped: true, Actor: ActorView{InFlightMove: &InFlightMoveView{}}}
	baker := Payload{Restocking: seller, Actor: ActorView{InFlightSourceActivity: &InFlightSourceActivityView{Kind: sim.SourceActivityBake}}}
	for name, p := range map[string]Payload{"walking visitor": walkingVisitor, "baker": baker} {
		if got := payAvailabilityOf(p); got != PayWithheld {
			t.Errorf("%s: payAvailabilityOf = %d, want PayWithheld", name, got)
		}
		if CanPayWithItem(p) {
			t.Errorf("%s: CanPayWithItem = true, want false", name)
		}
	}
	var r strings.Builder
	renderCoPresentBuyUnavailable(&r, "Josiah Thorne", "salt", PayWithheld)
	if s := r.String(); strings.Contains(s, "pay_with_item") || strings.Contains(s, "stop") {
		t.Errorf("withheld co-present line names a call or stop: %q", s)
	}
}

// TestQuoteWarrantNeedsTheQuotedSellerAsPeer (code_review round 2): pay_with_item
// can be open because the buyer is huddled with someone else, but a quote settles
// only with its own seller in her huddle. Bob's quote with only Carol in her huddle
// keeps its quote_id and names neither the take call nor stop.
func TestQuoteWarrantNeedsTheQuotedSellerAsPeer(t *testing.T) {
	quote := sim.WarrantMeta{
		TriggerActorID: "bob",
		Reason: sim.SceneQuoteTargetedWarrantReason{
			QuoteID: 11, SellerID: "bob", Lines: []sim.QuoteLine{{ItemKind: "water", Qty: 1}}, Amount: 1,
		},
		SourceEventID: 40,
	}
	base := Payload{
		ActorID:           "alice",
		Warrants:          []sim.WarrantMeta{quote},
		WarrantActorNames: map[sim.ActorID]string{"bob": "Bob"},
		Baseline:          BaselinePresent,
		Surroundings:      SurroundingsView{HuddleMembers: []HuddleMember{{ID: "carol"}}},
	}
	for name, p := range map[string]Payload{
		"still":   base,
		"walking": func() Payload { q := base; q.Actor.InFlightMove = &InFlightMoveView{}; return q }(),
	} {
		out := combinedPrompt(Render(p, DefaultRenderConfig()))
		if !strings.Contains(out, "quote_id 11") {
			t.Errorf("%s: quote_id dropped:\n%s", name, out)
		}
		if strings.Contains(out, "call pay_with_item") || strings.Contains(out, "call stop") {
			t.Errorf("%s: a quote from a seller outside her huddle names the take call or stop:\n%s", name, out)
		}
	}
	// With Bob as her peer the take call is named again.
	withBob := base
	withBob.Surroundings = SurroundingsView{HuddleMembers: []HuddleMember{{ID: "bob"}}}
	if out := combinedPrompt(Render(withBob, DefaultRenderConfig())); !strings.Contains(out, "call pay_with_item with quote_id 11") {
		t.Errorf("quote from a huddle peer lost its take call:\n%s", out)
	}
}

// TestCoPresentBuyLeadInFollowsPay (code_review round 2) runs the full renderers
// with pay withheld and walking: no "Buy …" imperative lead-in renders on a turn
// the buyer cannot buy; the need is stated instead.
func TestCoPresentBuyLeadInFollowsPay(t *testing.T) {
	const seller = "Ezekiel Crane"
	for _, pay := range []PayAvailability{PayWalking, PayWithheld} {
		for name, render := range map[string]func(*strings.Builder){
			"farm upkeep": func(b *strings.Builder) {
				renderFarmUpkeep(b, &FarmUpkeepView{ShovelsOwed: 3, ShovelsShort: 3, CoPresentSeller: seller, SellerStock: 9, Pay: pay})
			},
			"stall repair": func(b *strings.Builder) {
				renderStallRepairBuy(b, &StallRepairBuyView{NailsNeeded: 5, NailsShort: 5, CoPresentSeller: seller, SellerStock: 9, Pay: pay})
			},
			"wright": func(b *strings.Builder) {
				renderWrightStoneBuy(b, &WrightRoundsView{NoStone: true, StonesShort: 1, CoPresentSeller: seller, SellerStock: 2, Pay: pay})
			},
			"capped": func(b *strings.Builder) {
				renderFarmUpkeep(b, &FarmUpkeepView{ShovelsOwed: 3, ShovelsShort: 3, CoPresentSeller: seller, SellerStock: 1, Pay: pay})
			},
		} {
			var b strings.Builder
			render(&b)
			out := b.String()
			if strings.Contains(out, "Buy ") || strings.Contains(out, "buy what he has") || strings.Contains(out, "call pay_with_item") {
				t.Errorf("pay=%d %s: a buy imperative renders on a turn she cannot buy:\n%s", pay, name, out)
			}
		}
	}
}
