package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ZBBS-HOME-404 — seller-side "offer your wares" cue. Build gating + the two
// storm-suppression guards, and the render shape.

func TestBuildOfferableCustomers_NotBusinessowner(t *testing.T) {
	members := []HuddleMember{{ID: "buyer", DisplayName: "Goodwife Mary", Acquainted: true}}
	inv := []InventoryItem{{Label: "Stew", Qty: 5}}
	if v := buildOfferableCustomers(nil, "seller", false, members, inv); v != nil {
		t.Fatalf("non-businessowner should get nil cue, got %+v", v)
	}
}

func TestBuildOfferableCustomers_NoCustomers(t *testing.T) {
	inv := []InventoryItem{{Label: "Stew", Qty: 5}}
	if v := buildOfferableCustomers(nil, "seller", true, nil, inv); v != nil {
		t.Fatalf("no co-present customers should get nil cue, got %+v", v)
	}
}

func TestBuildOfferableCustomers_NoGoods(t *testing.T) {
	members := []HuddleMember{{ID: "buyer", DisplayName: "Goodwife Mary", Acquainted: true}}
	if v := buildOfferableCustomers(nil, "seller", true, members, nil); v != nil {
		t.Fatalf("nothing to sell should get nil cue, got %+v", v)
	}
	// Inventory entries that resolve to no label collapse to no sellable goods.
	if v := buildOfferableCustomers(nil, "seller", true, members, []InventoryItem{{Label: "", Qty: 3}}); v != nil {
		t.Fatalf("empty-label inventory should get nil cue, got %+v", v)
	}
}

func TestBuildOfferableCustomers_HappyPath(t *testing.T) {
	members := []HuddleMember{{ID: "buyer", DisplayName: "Goodwife Mary", Acquainted: true}}
	inv := []InventoryItem{{Label: "Stew", Qty: 5}, {Label: "Ale", Qty: 20}}
	v := buildOfferableCustomers(&sim.Snapshot{}, "seller", true, members, inv)
	if v == nil {
		t.Fatal("expected a cue view, got nil")
	}
	if len(v.CustomerNames) != 1 || v.CustomerNames[0] != "Goodwife Mary" {
		t.Errorf("CustomerNames = %v, want [Goodwife Mary]", v.CustomerNames)
	}
	if len(v.Goods) != 2 ||
		v.Goods[0] != (OfferableGood{Label: "Stew", OnHand: 5}) ||
		v.Goods[1] != (OfferableGood{Label: "Ale", OnHand: 20}) {
		t.Errorf("Goods = %v, want [{Stew 5} {Ale 20}]", v.Goods)
	}
}

func TestBuildOfferableCustomers_AcquaintanceGatedNames(t *testing.T) {
	members := []HuddleMember{
		{ID: "known", DisplayName: "Goodwife Mary", Acquainted: true},
		{ID: "byrole", DisplayName: "Ezekiel", Role: "blacksmith", Acquainted: false},
		{ID: "strange", Acquainted: false},
	}
	inv := []InventoryItem{{Label: "Stew", Qty: 5}}
	v := buildOfferableCustomers(&sim.Snapshot{}, "seller", true, members, inv)
	if v == nil {
		t.Fatal("expected a cue view, got nil")
	}
	want := "Goodwife Mary|the blacksmith|a stranger"
	if got := strings.Join(v.CustomerNames, "|"); got != want {
		t.Errorf("CustomerNames = %q, want %q", got, want)
	}
}

func TestBuildOfferableCustomers_SuppressPendingOffer(t *testing.T) {
	members := []HuddleMember{
		{ID: "buyerA", DisplayName: "Goodwife Mary", Acquainted: true},
		{ID: "buyerB", DisplayName: "John Procter", Acquainted: true},
	}
	inv := []InventoryItem{{Label: "Stew", Qty: 5}}
	snap := &sim.Snapshot{
		PayLedger: map[sim.LedgerID]*sim.PayLedgerEntry{
			1: {ID: 1, BuyerID: "buyerA", SellerID: "seller", State: sim.PayLedgerStatePending},
		},
	}
	v := buildOfferableCustomers(snap, "seller", true, members, inv)
	if v == nil {
		t.Fatal("expected a cue view, got nil")
	}
	if len(v.CustomerNames) != 1 || v.CustomerNames[0] != "John Procter" {
		t.Errorf("CustomerNames = %v, want [John Procter] (buyerA already has a pending offer)", v.CustomerNames)
	}
}

func TestBuildOfferableCustomers_PendingOfferToOtherSellerNotSuppressed(t *testing.T) {
	members := []HuddleMember{{ID: "buyerA", DisplayName: "Goodwife Mary", Acquainted: true}}
	inv := []InventoryItem{{Label: "Stew", Qty: 5}}
	// The pending offer is to a DIFFERENT seller — it must not suppress here.
	snap := &sim.Snapshot{
		PayLedger: map[sim.LedgerID]*sim.PayLedgerEntry{
			1: {ID: 1, BuyerID: "buyerA", SellerID: "otherSeller", State: sim.PayLedgerStatePending},
		},
	}
	v := buildOfferableCustomers(snap, "seller", true, members, inv)
	if v == nil || len(v.CustomerNames) != 1 {
		t.Fatalf("an offer to another seller should not suppress; got %+v", v)
	}
}

func TestBuildOfferableCustomers_SuppressActiveTargetedQuote(t *testing.T) {
	members := []HuddleMember{
		{ID: "buyerA", DisplayName: "Goodwife Mary", Acquainted: true},
		{ID: "buyerB", DisplayName: "John Procter", Acquainted: true},
	}
	inv := []InventoryItem{{Label: "Stew", Qty: 5}}
	snap := &sim.Snapshot{
		Quotes: map[sim.QuoteID]*sim.SceneQuote{
			1: {ID: 1, SellerID: "seller", TargetBuyer: "buyerA", Lines: []sim.QuoteLine{{ItemKind: "stew", Qty: 1}}, State: sim.SceneQuoteStateActive},
		},
	}
	v := buildOfferableCustomers(snap, "seller", true, members, inv)
	if v == nil {
		t.Fatal("expected a cue view, got nil")
	}
	if len(v.CustomerNames) != 1 || v.CustomerNames[0] != "John Procter" {
		t.Errorf("CustomerNames = %v, want [John Procter] (buyerA already has a live quote)", v.CustomerNames)
	}
}

func TestBuildOfferableCustomers_PublicQuoteDoesNotSuppress(t *testing.T) {
	members := []HuddleMember{{ID: "buyerA", DisplayName: "Goodwife Mary", Acquainted: true}}
	inv := []InventoryItem{{Label: "Stew", Qty: 5}}
	// An untargeted (public) quote isn't directed at this customer — no suppression.
	snap := &sim.Snapshot{
		Quotes: map[sim.QuoteID]*sim.SceneQuote{
			1: {ID: 1, SellerID: "seller", TargetBuyer: "", Lines: []sim.QuoteLine{{ItemKind: "stew", Qty: 1}}, State: sim.SceneQuoteStateActive},
		},
	}
	v := buildOfferableCustomers(snap, "seller", true, members, inv)
	if v == nil || len(v.CustomerNames) != 1 {
		t.Fatalf("a public quote should not suppress a directed offer; got %+v", v)
	}
}

func TestBuildOfferableCustomers_ExpiredQuoteDoesNotSuppress(t *testing.T) {
	members := []HuddleMember{{ID: "buyerA", DisplayName: "Goodwife Mary", Acquainted: true}}
	inv := []InventoryItem{{Label: "Stew", Qty: 5}}
	snap := &sim.Snapshot{
		Quotes: map[sim.QuoteID]*sim.SceneQuote{
			1: {ID: 1, SellerID: "seller", TargetBuyer: "buyerA", State: sim.SceneQuoteStateExpired},
		},
	}
	v := buildOfferableCustomers(snap, "seller", true, members, inv)
	if v == nil || len(v.CustomerNames) != 1 {
		t.Fatalf("an expired quote should not suppress; got %+v", v)
	}
}

func TestBuildOfferableCustomers_AllSuppressedReturnsNil(t *testing.T) {
	members := []HuddleMember{{ID: "buyerA", DisplayName: "Goodwife Mary", Acquainted: true}}
	inv := []InventoryItem{{Label: "Stew", Qty: 5}}
	snap := &sim.Snapshot{
		PayLedger: map[sim.LedgerID]*sim.PayLedgerEntry{
			1: {ID: 1, BuyerID: "buyerA", SellerID: "seller", State: sim.PayLedgerStatePending},
		},
	}
	if v := buildOfferableCustomers(snap, "seller", true, members, inv); v != nil {
		t.Fatalf("all customers suppressed should return nil (render content-gates), got %+v", v)
	}
}

// LLM-171: a co-present customer who MAKES one of the seller's goods is flagged
// in ProducerNotes (only the produced good, not the rest of the stock), so Render
// can steer the seller off pitching it back at its maker.
func TestBuildOfferableCustomers_ProducerNoteForMakerCustomer(t *testing.T) {
	members := []HuddleMember{{ID: "ezekiel", DisplayName: "Ezekiel Crane", Acquainted: true}}
	inv := []InventoryItem{{Label: "skillet", Qty: 4, kind: "skillet"}, {Label: "ale", Qty: 20, kind: "ale"}}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"ezekiel": {RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: "skillet", Source: sim.RestockSourceProduce, Max: 5},
			}}},
		},
	}
	v := buildOfferableCustomers(snap, "seller", true, members, inv)
	if v == nil {
		t.Fatal("expected a cue view, got nil")
	}
	if len(v.ProducerNotes) != 1 {
		t.Fatalf("ProducerNotes = %+v, want exactly 1 note", v.ProducerNotes)
	}
	note := v.ProducerNotes[0]
	if note.CustomerName != "Ezekiel Crane" {
		t.Errorf("note customer = %q, want Ezekiel Crane", note.CustomerName)
	}
	if len(note.Goods) != 1 || note.Goods[0] != "skillet" {
		t.Errorf("note goods = %v, want [skillet] (the produced good only, not ale)", note.Goods)
	}
}

// A co-present customer who makes NONE of the seller's goods draws no note.
func TestBuildOfferableCustomers_NoProducerNoteForNonMaker(t *testing.T) {
	members := []HuddleMember{{ID: "mary", DisplayName: "Goodwife Mary", Acquainted: true}}
	inv := []InventoryItem{{Label: "skillet", Qty: 4, kind: "skillet"}}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"mary": {RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
				{Item: "bread", Source: sim.RestockSourceProduce, Max: 5},
			}}},
		},
	}
	v := buildOfferableCustomers(snap, "seller", true, members, inv)
	if v == nil {
		t.Fatal("expected a cue view, got nil")
	}
	if len(v.ProducerNotes) != 0 {
		t.Errorf("ProducerNotes = %+v, want none (Mary makes bread, not skillet)", v.ProducerNotes)
	}
}

func TestRenderOfferableCustomers_ProducerNote(t *testing.T) {
	var b strings.Builder
	renderOfferableCustomers(&b, &OfferableCustomersView{
		CustomerNames: []string{"Ezekiel Crane"},
		Goods:         []OfferableGood{{Label: "nail", OnHand: 38}, {Label: "skillet", OnHand: 4}},
		ProducerNotes: []ProducerNote{{CustomerName: "Ezekiel Crane", Goods: []string{"nail", "skillet"}}},
	})
	out := b.String()
	want := "Ezekiel Crane makes nail and skillet themselves — don't pitch those back to their own maker; offer them to other customers instead."
	if !strings.Contains(out, want) {
		t.Errorf("output missing producer note %q\n--- got ---\n%s", want, out)
	}
}

func TestRenderOfferableCustomers_NilAndEmptySkip(t *testing.T) {
	var b strings.Builder
	renderOfferableCustomers(&b, nil)
	if b.Len() != 0 {
		t.Errorf("nil view should render nothing, got %q", b.String())
	}
	b.Reset()
	renderOfferableCustomers(&b, &OfferableCustomersView{})
	if b.Len() != 0 {
		t.Errorf("empty view should render nothing, got %q", b.String())
	}
	b.Reset()
	renderOfferableCustomers(&b, &OfferableCustomersView{CustomerNames: []string{"Mary"}})
	if b.Len() != 0 {
		t.Errorf("view with no goods should render nothing, got %q", b.String())
	}
}

func TestRenderOfferableCustomers_SingleCustomer(t *testing.T) {
	var b strings.Builder
	renderOfferableCustomers(&b, &OfferableCustomersView{
		CustomerNames: []string{"Goodwife Mary"},
		Goods:         []OfferableGood{{Label: "Stew", OnHand: 5}, {Label: "Ale", OnHand: 20}},
	})
	out := b.String()
	for _, want := range []string{
		"## Custom at hand",
		"Goodwife Mary is here with you",
		"call sell with",
		"target_buyer",
		// ZBBS-HOME-467: quote trigger gated on a named good; a generic opener
		// gets the menu (present wares + let them choose), not a guessed-item quote.
		"names a specific good they want",
		"let them choose",
		"do not sell unless the buyer has named the good",
		"Your goods to sell: Stew (5 on hand), Ale (20 on hand).",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- got ---\n%s", want, out)
		}
	}
}

// LLM-166: a for-sale inedible ingredient carries its use in the sell-list,
// folded into the on-hand parens, consistent with the carry readout.
func TestRenderOfferableCustomers_IngredientUse(t *testing.T) {
	var b strings.Builder
	renderOfferableCustomers(&b, &OfferableCustomersView{
		CustomerNames: []string{"Goodwife Mary"},
		Goods: []OfferableGood{
			{Label: "Meat", OnHand: 7, Use: "used to produce stew"},
			{Label: "Cheese", OnHand: 15},
		},
	})
	out := b.String()
	if !strings.Contains(out, "Your goods to sell: Meat (7 on hand, used to produce stew), Cheese (15 on hand).") {
		t.Errorf("want folded use annotation in sell-list, got:\n%s", out)
	}
}

func TestRenderOfferableCustomers_MultipleCustomersPluralVerb(t *testing.T) {
	var b strings.Builder
	renderOfferableCustomers(&b, &OfferableCustomersView{
		CustomerNames: []string{"Goodwife Mary", "John Procter"},
		Goods:         []OfferableGood{{Label: "Stew", OnHand: 3}},
	})
	out := b.String()
	if !strings.Contains(out, "Goodwife Mary and John Procter are here with you") {
		t.Errorf("expected plural 'are here' with joined names, got:\n%s", out)
	}
}

// Full-Build integration: the cue is wired into Build and keys on huddle
// co-presence (an active interaction), confirming both the wiring and the
// huddle-not-loiter design point (code_review #4).

func TestBuild_OfferableCustomers_WiredForBusinessownerInHuddle(t *testing.T) {
	seller := &sim.ActorSnapshot{
		DisplayName:        "Goodwife Ellis",
		Kind:               sim.KindNPCShared,
		CurrentHuddleID:    "h1",
		BusinessownerState: &sim.BusinessownerState{},
		Inventory:          map[sim.ItemKind]int{"stew": 5},
		Acquaintances:      map[string]sim.Acquaintance{"Goodwife Mary": {}},
		// At her own post — the vendor cues gate on AtOwnBusiness (ZBBS-WORK-385).
		WorkStructureID:   "tavern",
		InsideStructureID: "tavern",
	}
	customer := &sim.ActorSnapshot{
		DisplayName:     "Goodwife Mary",
		Kind:            sim.KindNPCStateful,
		CurrentHuddleID: "h1",
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{"ellis": seller, "mary": customer},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"ellis": {}, "mary": {}}},
		},
	}
	p := Build(snap, "ellis", nil)
	if p.OfferableCustomers == nil {
		t.Fatal("expected OfferableCustomers cue for a businessowner huddled with a customer")
	}
	if got := p.OfferableCustomers.CustomerNames; len(got) != 1 || got[0] != "Goodwife Mary" {
		t.Errorf("CustomerNames = %v, want [Goodwife Mary]", got)
	}
	if len(p.OfferableCustomers.Goods) == 0 {
		t.Error("expected sellable goods in the cue, got none")
	}
}

func TestBuild_OfferableCustomers_NilForBusinessownerNotInHuddle(t *testing.T) {
	// A businessowner with stock but NOT in a huddle (a customer merely
	// loitering nearby, no active interaction) gets no cue — the cue keys on
	// huddle co-presence, not loiter-presence, so it can't pitch at a passerby.
	seller := &sim.ActorSnapshot{
		DisplayName:        "Goodwife Ellis",
		Kind:               sim.KindNPCShared,
		BusinessownerState: &sim.BusinessownerState{},
		Inventory:          map[sim.ItemKind]int{"stew": 5},
	}
	snap := &sim.Snapshot{
		Actors:  map[sim.ActorID]*sim.ActorSnapshot{"ellis": seller},
		Huddles: map[sim.HuddleID]*sim.Huddle{},
	}
	p := Build(snap, "ellis", nil)
	if p.OfferableCustomers != nil {
		t.Errorf("expected no cue for a businessowner not in a huddle, got %+v", p.OfferableCustomers)
	}
}
