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
	if strings.Join(v.Goods, "|") != "Stew|Ale" {
		t.Errorf("Goods = %v, want [Stew Ale]", v.Goods)
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
			1: {ID: 1, SellerID: "seller", TargetBuyer: "buyerA", ItemKind: "stew", State: sim.SceneQuoteStateActive},
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
			1: {ID: 1, SellerID: "seller", TargetBuyer: "", ItemKind: "stew", State: sim.SceneQuoteStateActive},
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
		Goods:         []string{"Stew", "Ale"},
	})
	out := b.String()
	for _, want := range []string{
		"## Custom at hand",
		"Goodwife Mary is here with you",
		"scene_quote",
		"target_buyer",
		"Your goods to sell: Stew, Ale.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- got ---\n%s", want, out)
		}
	}
}

func TestRenderOfferableCustomers_MultipleCustomersPluralVerb(t *testing.T) {
	var b strings.Builder
	renderOfferableCustomers(&b, &OfferableCustomersView{
		CustomerNames: []string{"Goodwife Mary", "John Procter"},
		Goods:         []string{"Stew"},
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
