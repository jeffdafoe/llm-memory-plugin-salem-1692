package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

func gatingTestRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	if err := RegisterSpeak(r); err != nil {
		t.Fatalf("RegisterSpeak: %v", err)
	}
	if err := RegisterPayWithItemFamily(r); err != nil {
		t.Fatalf("RegisterPayWithItemFamily: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("RegisterTerminal: %v", err)
	}
	return r
}

func specNameSet(specs []llm.ToolSpec) map[string]int {
	counts := make(map[string]int, len(specs))
	for _, s := range specs {
		counts[s.Name]++
	}
	return counts
}

func payOfferPayload(ledgers ...sim.LedgerID) perception.Payload {
	var warrants []sim.WarrantMeta
	for _, id := range ledgers {
		warrants = append(warrants, sim.WarrantMeta{
			TriggerActorID: "bob",
			Reason:         sim.PayOfferWarrantReason{LedgerID: id, Buyer: "bob", Item: "stew", Qty: 1, Amount: 5},
		})
	}
	return perception.Payload{ActorID: "seller", Warrants: warrants}
}

// payOfferPayloadDepths builds a payload with one pending offer per supplied
// depth (ledger ids assigned 1..n). Used by the scar-#4 counter-cap tests.
func payOfferPayloadDepths(depths ...int) perception.Payload {
	var warrants []sim.WarrantMeta
	for i, d := range depths {
		warrants = append(warrants, sim.WarrantMeta{
			TriggerActorID: "bob",
			Reason: sim.PayOfferWarrantReason{
				LedgerID: sim.LedgerID(i + 1), Buyer: "bob", Item: "stew", Qty: 1, Amount: 5, Depth: d,
			},
		})
	}
	return perception.Payload{ActorID: "seller", Warrants: warrants}
}

// TestGateTools_NoOffer_DropsSellerResponseTools — with no pending offer in
// the payload, the seller-response tools (accept/decline/counter) are not
// advertised; everything else (incl. buyer-side withdraw_pay) still is.
func TestGateTools_NoOffer_DropsSellerResponseTools(t *testing.T) {
	r := gatingTestRegistry(t)
	specs := gateTools(r, perception.Payload{ActorID: "seller"}, nil)
	names := specNameSet(specs)

	for _, gated := range []string{"accept_pay", "decline_pay", "counter_pay"} {
		if names[gated] != 0 {
			t.Errorf("%q advertised with no pending offer (count %d)", gated, names[gated])
		}
	}
	for _, always := range []string{"speak", "pay_with_item", "withdraw_pay", "done"} {
		if names[always] != 1 {
			t.Errorf("%q should always be advertised; count %d", always, names[always])
		}
	}
}

// TestGateTools_PendingOffer_AddsSellerResponseTools — a pending offer in the
// payload re-adds the seller-response tools.
func TestGateTools_PendingOffer_AddsSellerResponseTools(t *testing.T) {
	r := gatingTestRegistry(t)
	specs := gateTools(r, payOfferPayload(17), nil)
	names := specNameSet(specs)

	for _, want := range []string{"accept_pay", "decline_pay", "counter_pay", "withdraw_pay", "speak", "pay_with_item", "done"} {
		if names[want] != 1 {
			t.Errorf("%q should be advertised when an offer is pending; count %d", want, names[want])
		}
	}
}

// TestGateTools_MultipleOffers_NoDuplicateTools — multiple pending offers
// still advertise each response tool exactly once (the tools take ledger_id
// as a param; the offers are enumerated in the prompt, not in the tool list).
func TestGateTools_MultipleOffers_NoDuplicateTools(t *testing.T) {
	r := gatingTestRegistry(t)
	specs := gateTools(r, payOfferPayload(1, 2, 3), nil)
	names := specNameSet(specs)
	for _, tool := range []string{"accept_pay", "decline_pay", "counter_pay"} {
		if names[tool] != 1 {
			t.Errorf("%q advertised %d times across multiple offers, want 1", tool, names[tool])
		}
	}
}

// TestGateTools_PendingOffer_MatchesAdvertisedSpecs — when an offer is
// present the gate returns exactly the registry's full Available set, in
// registration order (prompt-cache stability).
func TestGateTools_PendingOffer_MatchesAdvertisedSpecs(t *testing.T) {
	r := gatingTestRegistry(t)
	got := gateTools(r, payOfferPayload(1), nil)
	want := r.AdvertisedSpecs()
	if len(got) != len(want) {
		t.Fatalf("len(gated)=%d, len(advertised)=%d", len(got), len(want))
	}
	for i := range got {
		if got[i].Name != want[i].Name {
			t.Errorf("order mismatch at %d: %q vs %q", i, got[i].Name, want[i].Name)
		}
	}
}

// TestGateTools_NoOffer_PreservesOrderOfRemaining — dropping the gated tools
// leaves the remaining tools in their registration order.
func TestGateTools_NoOffer_PreservesOrderOfRemaining(t *testing.T) {
	r := gatingTestRegistry(t)
	got := gateTools(r, perception.Payload{ActorID: "seller"}, nil)

	var want []string
	for _, s := range r.AdvertisedSpecs() {
		if _, gated := payOfferResponseTools[s.Name]; gated {
			continue
		}
		want = append(want, s.Name)
	}
	if len(got) != len(want) {
		t.Fatalf("len(gated)=%d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].Name != want[i] {
			t.Errorf("order mismatch at %d: %q vs %q", i, got[i].Name, want[i])
		}
	}
}

// TestGateTools_OfferAtDepthCap_DropsCounterPay — an offer already at the
// counter-chain depth cap can't be usefully countered (the buyer's response
// would be rejected by validateInResponseTo, parent.Depth >= cap), so
// counter_pay is dropped; accept_pay / decline_pay stay advertised (an offer
// at the cap is still acceptable / declinable). ZBBS-WORK-320 scar #4.
func TestGateTools_OfferAtDepthCap_DropsCounterPay(t *testing.T) {
	r := gatingTestRegistry(t)
	specs := gateTools(r, payOfferPayloadDepths(sim.MaxPayCounterChainDepth), nil)
	names := specNameSet(specs)
	if names[counterPayToolName] != 0 {
		t.Errorf("counter_pay advertised for an offer at the depth cap; count %d", names[counterPayToolName])
	}
	for _, want := range []string{"accept_pay", "decline_pay"} {
		if names[want] != 1 {
			t.Errorf("%q should stay advertised at the depth cap; count %d", want, names[want])
		}
	}
}

// TestGateTools_OfferJustBelowCap_KeepsCounterPay — boundary: an offer at
// cap-1 can still be countered (the buyer's response at depth==cap is allowed,
// since validateInResponseTo rejects only parent.Depth >= cap), so counter_pay
// stays.
func TestGateTools_OfferJustBelowCap_KeepsCounterPay(t *testing.T) {
	r := gatingTestRegistry(t)
	specs := gateTools(r, payOfferPayloadDepths(sim.MaxPayCounterChainDepth-1), nil)
	names := specNameSet(specs)
	if names[counterPayToolName] != 1 {
		t.Errorf("counter_pay should stay advertised at depth cap-1; count %d", names[counterPayToolName])
	}
}

// TestGateTools_MixedDepth_KeepsCounterPayWhenAnyCounterable — with one offer
// at the cap and one below it, counter_pay stays advertised: the seller can
// still counter the below-cap offer by ledger_id.
func TestGateTools_MixedDepth_KeepsCounterPayWhenAnyCounterable(t *testing.T) {
	r := gatingTestRegistry(t)
	specs := gateTools(r, payOfferPayloadDepths(sim.MaxPayCounterChainDepth, 0), nil)
	names := specNameSet(specs)
	if names[counterPayToolName] != 1 {
		t.Errorf("counter_pay should stay advertised when at least one offer is counterable; count %d", names[counterPayToolName])
	}
}

// TestGateTools_AllOffersAtCap_DropsCounterPay — every pending offer at the
// cap drops counter_pay; accept/decline remain.
func TestGateTools_AllOffersAtCap_DropsCounterPay(t *testing.T) {
	r := gatingTestRegistry(t)
	specs := gateTools(r, payOfferPayloadDepths(sim.MaxPayCounterChainDepth, sim.MaxPayCounterChainDepth), nil)
	names := specNameSet(specs)
	if names[counterPayToolName] != 0 {
		t.Errorf("counter_pay advertised when all offers at the cap; count %d", names[counterPayToolName])
	}
	for _, want := range []string{"accept_pay", "decline_pay"} {
		if names[want] != 1 {
			t.Errorf("%q should stay advertised; count %d", want, names[want])
		}
	}
}

// gatingRegistryWithRecall extends the gating test registry with a recall
// observation tool, for the dedicated-VA gate tests (ZBBS-WORK-321).
func gatingRegistryWithRecall(t *testing.T) *Registry {
	t.Helper()
	r := gatingTestRegistry(t)
	if err := r.RegisterObservation(
		"recall",
		json.RawMessage(`{"type":"object"}`),
		func(json.RawMessage) (any, error) { return nil, nil },
		func(_ context.Context, _ HandlerInput) (string, error) { return "", nil },
	); err != nil {
		t.Fatalf("RegisterObservation recall: %v", err)
	}
	return r
}

func snapWithActorKind(id sim.ActorID, kind sim.ActorKind) *sim.Snapshot {
	return &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{id: {Kind: kind}}}
}

// TestGateTools_Recall_AdvertisedOnlyToDedicatedVA — recall is offered to a
// KindNPCStateful (own VA + memory) actor, dropped for a KindNPCShared actor
// (no personal memory), and dropped conservatively when the actor can't be
// resolved (nil snapshot). ZBBS-WORK-321.
func TestGateTools_Recall_AdvertisedOnlyToDedicatedVA(t *testing.T) {
	r := gatingRegistryWithRecall(t)
	payload := perception.Payload{ActorID: "npc"}

	stateful := specNameSet(gateTools(r, payload, snapWithActorKind("npc", sim.KindNPCStateful)))
	if stateful["recall"] != 1 {
		t.Errorf("recall should be advertised to a KindNPCStateful actor; count %d", stateful["recall"])
	}

	shared := specNameSet(gateTools(r, payload, snapWithActorKind("npc", sim.KindNPCShared)))
	if shared["recall"] != 0 {
		t.Errorf("recall must NOT be advertised to a KindNPCShared actor; count %d", shared["recall"])
	}

	nilSnap := specNameSet(gateTools(r, payload, nil))
	if nilSnap["recall"] != 0 {
		t.Errorf("recall must be dropped when the actor can't be resolved (nil snapshot); count %d", nilSnap["recall"])
	}
}

// gatingRegistryWithDeliverOrder extends the gating test registry with the
// deliver_order tool, for the ZBBS-HOME-398 narrow-advertising gate.
func gatingRegistryWithDeliverOrder(t *testing.T) *Registry {
	t.Helper()
	r := gatingTestRegistry(t)
	if err := RegisterDeliverOrder(r); err != nil {
		t.Fatalf("RegisterDeliverOrder: %v", err)
	}
	return r
}

// TestGateTools_DeliverOrder_AdvertisedOnlyWithReadyOrder — ZBBS-HOME-398:
// deliver_order is advertised only to a keeper who actually has a Ready order
// to fulfill (a pending delivery in the payload). After 397 that's a lodging
// check-in; physical takeaway delivers at accept and never sits Ready, so a
// keeper with no pending delivery never sees the tool — it's no longer
// advertised every tick to every NPC.
func TestGateTools_DeliverOrder_AdvertisedOnlyWithReadyOrder(t *testing.T) {
	r := gatingRegistryWithDeliverOrder(t)

	none := specNameSet(gateTools(r, perception.Payload{ActorID: "keeper"}, nil))
	if none[deliverOrderToolName] != 0 {
		t.Errorf("deliver_order advertised with no pending delivery; count %d", none[deliverOrderToolName])
	}

	withOrder := perception.Payload{
		ActorID:                 "keeper",
		PendingDeliveriesFromMe: []perception.OrderView{{ID: 1, Item: "nights_stay", Qty: 1}},
	}
	got := specNameSet(gateTools(r, withOrder, nil))
	if got[deliverOrderToolName] != 1 {
		t.Errorf("deliver_order should be advertised when a Ready order is pending; count %d", got[deliverOrderToolName])
	}
}
