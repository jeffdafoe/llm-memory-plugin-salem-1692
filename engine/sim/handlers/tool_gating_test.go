package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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

// speakAudience is a SurroundingsView carrying one awake huddle peer — enough to
// satisfy BOTH the LLM-106 speak gate (HasAudience) and the LLM-329 pay-verb gate
// (HuddleMembers non-empty). Used by gating tests that assert commerce/speak tool
// presence but are not about the co-presence gates themselves. A huddle peer, not a
// bare CoPresent walk-in, is the realistic co-presence for these scenarios: the pay
// substrate resolves against huddle peers, and pay_with_item_reactor stamps an
// offer only when buyer and seller share a huddle — so a pending offer implies the
// parties are huddled. (The dedicated speak gate test builds its own CoPresent
// fixture to cover the not-yet-huddled walk-in path.)
func speakAudience() perception.SurroundingsView {
	return perception.SurroundingsView{HuddleMembers: []perception.HuddleMember{{ID: "peer"}}}
}

// payOfferPayload builds a payload whose standing ledger view carries one
// pending offer per supplied ledger id. Since ZBBS-HOME-453 the gate keys off
// Payload.PayOffersForMe (the per-tick snap.PayLedger scan), not the consumed
// warrant batch — so the fixture populates the view directly.
func payOfferPayload(ledgers ...sim.LedgerID) perception.Payload {
	var offers []sim.PayOfferWarrantReason
	for _, id := range ledgers {
		offers = append(offers, sim.PayOfferWarrantReason{LedgerID: id, Buyer: "bob", Item: "stew", Qty: 1, Amount: 5})
	}
	return perception.Payload{ActorID: "seller", PayOffersForMe: offers, Surroundings: speakAudience()}
}

// payOfferPayloadDepths builds a payload with one pending offer per supplied
// depth (ledger ids assigned 1..n). Used by the scar-#4 counter-cap tests.
func payOfferPayloadDepths(depths ...int) perception.Payload {
	var offers []sim.PayOfferWarrantReason
	for i, d := range depths {
		offers = append(offers, sim.PayOfferWarrantReason{
			LedgerID: sim.LedgerID(i + 1), Buyer: "bob", Item: "stew", Qty: 1, Amount: 5, Depth: d,
		})
	}
	return perception.Payload{ActorID: "seller", PayOffersForMe: offers}
}

// TestGateTools_NoOffer_DropsSellerResponseTools — with no pending offer in
// the payload, the seller-response tools (accept/decline/counter) are not
// advertised, and neither is buyer-side withdraw_pay (no own offer to retract,
// LLM-322); the ungated commerce/utility tools still are.
func TestGateTools_NoOffer_DropsSellerResponseTools(t *testing.T) {
	r := gatingTestRegistry(t)
	specs := gateTools(r, perception.Payload{ActorID: "seller", Surroundings: speakAudience()}, nil)
	names := specNameSet(specs)

	for _, gated := range []string{"accept_pay", "decline_pay", "counter_pay", "withdraw_pay"} {
		if names[gated] != 0 {
			t.Errorf("%q advertised with no pending offer (count %d)", gated, names[gated])
		}
	}
	for _, always := range []string{"speak", "pay_with_item", "done"} {
		if names[always] != 1 {
			t.Errorf("%q should always be advertised; count %d", always, names[always])
		}
	}
}

// TestGateTools_PendingOffer_AddsSellerResponseTools — a pending offer against
// the seller (PayOffersForMe) re-adds the seller-response tools. It does NOT
// unlock buyer-side withdraw_pay, which keys off the actor's OWN outgoing offers
// (PendingOffersFromMe) — see TestGateTools_WithdrawPay_* (LLM-322).
func TestGateTools_PendingOffer_AddsSellerResponseTools(t *testing.T) {
	r := gatingTestRegistry(t)
	specs := gateTools(r, payOfferPayload(17), nil)
	names := specNameSet(specs)

	for _, want := range []string{"accept_pay", "decline_pay", "counter_pay", "speak", "pay_with_item", "done"} {
		if names[want] != 1 {
			t.Errorf("%q should be advertised when an offer is pending; count %d", want, names[want])
		}
	}
	if names["withdraw_pay"] != 0 {
		t.Errorf("withdraw_pay should NOT be advertised for a seller-side offer with no own outgoing offer; count %d", names["withdraw_pay"])
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

// TestGateTools_PendingOffer_MatchesAdvertisedSpecs — when the actor holds both
// a seller-side pending offer (PayOffersForMe) and an own outgoing offer
// (PendingOffersFromMe), every gated pay tool is satisfied, so the gate returns
// exactly the registry's full Available set, in registration order (prompt-cache
// stability).
func TestGateTools_PendingOffer_MatchesAdvertisedSpecs(t *testing.T) {
	r := gatingTestRegistry(t)
	payload := payOfferPayload(1)
	payload.PendingOffersFromMe = []perception.PendingOfferView{{}} // unlock withdraw_pay too (LLM-322)
	got := gateTools(r, payload, nil)
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
	got := gateTools(r, perception.Payload{ActorID: "seller", Surroundings: speakAudience()}, nil)

	var want []string
	for _, s := range r.AdvertisedSpecs() {
		if _, gated := payOfferResponseTools[s.Name]; gated {
			continue
		}
		if s.Name == withdrawPayToolName {
			continue // buyer-side, gated on own pending offer (none in this payload) — LLM-322
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

// gatingRegistryWithMemoryTools extends the gating test registry with the recall
// and memorize observation tools, for the memory-partition gate tests (LLM-356).
func gatingRegistryWithMemoryTools(t *testing.T) *Registry {
	t.Helper()
	r := gatingTestRegistry(t)
	for _, name := range []string{"recall", "memorize"} {
		if err := r.RegisterObservation(
			name,
			json.RawMessage(`{"type":"object"}`),
			func(json.RawMessage) (any, error) { return nil, nil },
			func(_ context.Context, _ HandlerInput) (string, error) { return "", nil },
		); err != nil {
			t.Fatalf("RegisterObservation %s: %v", name, err)
		}
	}
	return r
}

func snapWithActorKind(id sim.ActorID, kind sim.ActorKind) *sim.Snapshot {
	return snapWithActorKindName(id, kind, "")
}

func snapWithActorKindName(id sim.ActorID, kind sim.ActorKind, name string) *sim.Snapshot {
	return &sim.Snapshot{Actors: map[sim.ActorID]*sim.ActorSnapshot{id: {Kind: kind, DisplayName: name}}}
}

// TestGateTools_MemoryTools_AdvertisedToActorsWithMemory — recall AND memorize
// are offered to any actor with a private memory partition: a dedicated-VA NPC
// (KindNPCStateful) and a shared-VA NPC (KindNPCShared) with a slugifiable name.
// They are dropped for a PC, a decorative, a shared-VA actor whose name won't
// slugify (no partition to key), and conservatively when the actor can't be
// resolved. This is the cross-kind invariant: the two memory tools appear
// together iff the actor has memory (LLM-356).
func TestGateTools_MemoryTools_AdvertisedToActorsWithMemory(t *testing.T) {
	r := gatingRegistryWithMemoryTools(t)
	payload := perception.Payload{ActorID: "npc"}
	memoryTools := []string{"recall", "memorize"}

	assertBoth := func(t *testing.T, snap *sim.Snapshot, wantAdvertised bool) {
		t.Helper()
		got := specNameSet(gateTools(r, payload, snap))
		for _, name := range memoryTools {
			want := 0
			if wantAdvertised {
				want = 1
			}
			if got[name] != want {
				t.Errorf("%q advertised count = %d, want %d", name, got[name], want)
			}
		}
	}

	// Has memory → both advertised.
	assertBoth(t, snapWithActorKindName("npc", sim.KindNPCStateful, "Josiah Thorne"), true)
	assertBoth(t, snapWithActorKindName("npc", sim.KindNPCShared, "Anne Walker"), true)

	// No memory → both dropped.
	assertBoth(t, snapWithActorKindName("npc", sim.KindNPCShared, ""), false) // name won't slugify → no partition
	assertBoth(t, snapWithActorKind("npc", sim.KindPC), false)
	assertBoth(t, snapWithActorKind("npc", sim.KindDecorative), false)
	assertBoth(t, nil, false) // actor can't be resolved
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

// TestGateTools_DeliverOrder_AdvertisedOnlyWhenDeliverableNow — ZBBS-HOME-398 +
// LLM-338: deliver_order is advertised only to a keeper who has a Ready order
// deliverable RIGHT NOW (OrderView.DeliverableNow — good on hand, recipient
// co-present), NOT merely any Ready order. An unforged commission (AwaitingMake)
// or an absent-recipient order would bounce DeliverOrder's gate 5 / gate 6, so
// the tool is withheld until it can be used — locking the tool to the same
// DeliverableNow predicate the "## Orders to deliver" instruction reads (they
// can't drift). A keeper with no pending delivery still never sees the tool.
func TestGateTools_DeliverOrder_AdvertisedOnlyWhenDeliverableNow(t *testing.T) {
	r := gatingRegistryWithDeliverOrder(t)

	adForOrders := func(orders []perception.OrderView) int {
		p := perception.Payload{ActorID: "keeper", PendingDeliveriesFromMe: orders}
		return specNameSet(gateTools(r, p, nil))[deliverOrderToolName]
	}

	// No pending delivery: never advertised.
	if got := adForOrders(nil); got != 0 {
		t.Errorf("deliver_order advertised with no pending delivery; count %d", got)
	}
	// A deliverable order (good on hand, recipient present): advertised.
	if got := adForOrders([]perception.OrderView{{ID: 1, Item: "nights_stay", Qty: 1}}); got != 1 {
		t.Errorf("deliver_order should be advertised when a deliverable order is pending; count %d", got)
	}
	// An unforged commission (AwaitingMake) is the ONLY order: withheld.
	if got := adForOrders([]perception.OrderView{{ID: 1, Item: "shovel", Qty: 1, AwaitingMake: true}}); got != 0 {
		t.Errorf("deliver_order advertised for an unforged commission (gate 5 would bounce); count %d", got)
	}
	// An absent-recipient order is the ONLY order: withheld.
	if got := adForOrders([]perception.OrderView{{ID: 1, Item: "stew", Qty: 1, AbsentRecipientNames: []string{"Jefferey"}}}); got != 0 {
		t.Errorf("deliver_order advertised for an absent-recipient order (gate 6 would bounce); count %d", got)
	}
	// Mixed — one unforged commission + one deliverable order: advertised
	// (there IS something to hand over).
	mixed := []perception.OrderView{
		{ID: 1, Item: "shovel", Qty: 1, AwaitingMake: true},
		{ID: 2, Item: "nails", Qty: 5},
	}
	if got := adForOrders(mixed); got != 1 {
		t.Errorf("deliver_order should be advertised when at least one order is deliverable now; count %d", got)
	}
}

// gatingRegistryWithStayOpen extends the gating test registry with the
// stay_open tool, for the LLM-66 cue-driven advertising gate.
func gatingRegistryWithStayOpen(t *testing.T) *Registry {
	t.Helper()
	r := gatingTestRegistry(t)
	if err := RegisterStayOpen(r); err != nil {
		t.Fatalf("RegisterStayOpen: %v", err)
	}
	return r
}

// TestGateTools_StayOpen_AdvertisedOnlyWithOfferCue — LLM-66: stay_open is
// advertised only when the off-shift wind-down cue offers it
// (DutySteer.OfferStayOpen, built on !ToWork && !AtPost && AtOwnBusiness). With
// no DutySteer, or one whose OfferStayOpen is false, the tool is dropped — so a
// keeper off-post, on-shift, or a non-keeper never sees it. Reading the same
// signal the prose renders from keeps tool and cue from drifting. Before LLM-66
// stay_open had no gate and was advertised to every actor every tick.
func TestGateTools_StayOpen_AdvertisedOnlyWithOfferCue(t *testing.T) {
	r := gatingRegistryWithStayOpen(t)

	noSteer := specNameSet(gateTools(r, perception.Payload{ActorID: "keeper"}, nil))
	if noSteer[stayOpenToolName] != 0 {
		t.Errorf("stay_open advertised with no DutySteer; count %d", noSteer[stayOpenToolName])
	}

	notOffered := specNameSet(gateTools(r, perception.Payload{
		ActorID:   "keeper",
		DutySteer: &perception.DutySteerView{OfferStayOpen: false},
	}, nil))
	if notOffered[stayOpenToolName] != 0 {
		t.Errorf("stay_open advertised when OfferStayOpen is false; count %d", notOffered[stayOpenToolName])
	}

	offered := specNameSet(gateTools(r, perception.Payload{
		ActorID:   "keeper",
		DutySteer: &perception.DutySteerView{OfferStayOpen: true},
	}, nil))
	if offered[stayOpenToolName] != 1 {
		t.Errorf("stay_open should be advertised when the wind-down cue offers it; count %d", offered[stayOpenToolName])
	}
}

// gatingRegistryWithTakeBreak extends the gating test registry with the
// take_break tool, for the LLM-100 cue-driven advertising gate.
func gatingRegistryWithTakeBreak(t *testing.T) *Registry {
	t.Helper()
	r := gatingTestRegistry(t)
	if err := RegisterTakeBreak(r); err != nil {
		t.Fatalf("RegisterTakeBreak: %v", err)
	}
	return r
}

// TestGateTools_TakeBreak_AdvertisedOnlyWithRestInPlaceCue — LLM-100: take_break is
// advertised only when the recovery cue offers in-place rest
// (RecoveryOptions.RestInPlace, built on tired + at-own-post + on-shift). With no
// RecoveryOptions view, or one whose RestInPlace is false, the tool is dropped — so
// an off-shift actor with no shift to step away from never sees it. Reading the same
// field the "rest where you are" prose renders from keeps tool and cue from
// drifting. Before LLM-100 take_break had no gate and was advertised every tick.
func TestGateTools_TakeBreak_AdvertisedOnlyWithRestInPlaceCue(t *testing.T) {
	r := gatingRegistryWithTakeBreak(t)

	noView := specNameSet(gateTools(r, perception.Payload{ActorID: "keeper"}, nil))
	if noView[takeBreakToolName] != 0 {
		t.Errorf("take_break advertised with no RecoveryOptions view; count %d", noView[takeBreakToolName])
	}

	notOffered := specNameSet(gateTools(r, perception.Payload{
		ActorID:         "keeper",
		RecoveryOptions: &perception.RecoveryOptionsView{RestInPlace: false},
	}, nil))
	if notOffered[takeBreakToolName] != 0 {
		t.Errorf("take_break advertised when RestInPlace is false; count %d", notOffered[takeBreakToolName])
	}

	offered := specNameSet(gateTools(r, perception.Payload{
		ActorID:         "keeper",
		RecoveryOptions: &perception.RecoveryOptionsView{RestInPlace: true},
	}, nil))
	if offered[takeBreakToolName] != 1 {
		t.Errorf("take_break should be advertised when the recovery cue offers in-place rest; count %d", offered[takeBreakToolName])
	}

	// LLM-214: RestAtHome (weary NPC inside its own home) unlocks take_break the
	// same way — the tool gate reads OffersTakeBreak (RestInPlace OR RestAtHome), so
	// a homed vendor resting in its own bed gets the verb it was previously denied.
	offeredAtHome := specNameSet(gateTools(r, perception.Payload{
		ActorID:         "vendor",
		RecoveryOptions: &perception.RecoveryOptionsView{RestAtHome: true},
	}, nil))
	if offeredAtHome[takeBreakToolName] != 1 {
		t.Errorf("take_break should be advertised when the recovery cue offers rest-at-home; count %d", offeredAtHome[takeBreakToolName])
	}
}

// TestGateTools_Speak_DroppedWhenNoAudience — LLM-106: speak is advertised only
// when the actor has an awake, addressable audience (huddle peers, or co-present
// actors within earshot — Surroundings.HasAudience()). The substrate already
// rejects a no-listener speak, so a lone actor handed speak just burns a turn on a
// doomed greeting (the live Josiah Thorne empty-room case). A walk-in customer in
// CoPresent re-enables it, so a keeper can still greet a newcomer; a lone sleeper
// does not (this NPC's speech can't rouse it).
func TestGateTools_Speak_DroppedWhenNoAudience(t *testing.T) {
	r := gatingTestRegistry(t) // registers speak

	alone := specNameSet(gateTools(r, perception.Payload{ActorID: "keeper"}, nil))
	if alone[speakToolName] != 0 {
		t.Errorf("speak advertised to a lone actor with no audience; count %d", alone[speakToolName])
	}

	huddled := specNameSet(gateTools(r, perception.Payload{
		ActorID:      "keeper",
		Surroundings: perception.SurroundingsView{HuddleMembers: []perception.HuddleMember{{ID: "peer"}}},
	}, nil))
	if huddled[speakToolName] != 1 {
		t.Errorf("speak should be advertised to a huddled actor; count %d", huddled[speakToolName])
	}

	newcomer := specNameSet(gateTools(r, perception.Payload{
		ActorID:      "keeper",
		Surroundings: perception.SurroundingsView{CoPresent: []perception.HuddleMember{{ID: "customer"}}},
	}, nil))
	if newcomer[speakToolName] != 1 {
		t.Errorf("speak should be advertised when an awake actor is co-present (greet a newcomer); count %d", newcomer[speakToolName])
	}

	sleeperOnly := specNameSet(gateTools(r, perception.Payload{
		ActorID:      "keeper",
		Surroundings: perception.SurroundingsView{CoPresentAsleep: []perception.HuddleMember{{ID: "sleeper"}}},
	}, nil))
	if sleeperOnly[speakToolName] != 0 {
		t.Errorf("speak advertised with only a sleeper present (not addressable); count %d", sleeperOnly[speakToolName])
	}

	// A resting actor stays in the shared audience (a PC can wake it) but this
	// NPC's speech can't rouse it, so it must NOT re-enable speak — symmetric with
	// the sleeper case.
	restingOnly := specNameSet(gateTools(r, perception.Payload{
		ActorID:      "keeper",
		Surroundings: perception.SurroundingsView{CoPresentResting: []perception.HuddleMember{{ID: "resting"}}},
	}, nil))
	if restingOnly[speakToolName] != 0 {
		t.Errorf("speak advertised with only a resting actor present (not addressable); count %d", restingOnly[speakToolName])
	}
}

// gatingRegistryWithPay extends the gating test registry with the bare-coin pay
// tool. RegisterPayWithItemFamily registers pay_with_item but NOT bare pay (that's
// RegisterPay), so the LLM-329 test registers it to exercise both pay verbs.
func gatingRegistryWithPay(t *testing.T) *Registry {
	t.Helper()
	r := gatingTestRegistry(t)
	if err := RegisterPay(r); err != nil {
		t.Fatalf("RegisterPay: %v", err)
	}
	return r
}

// TestGateTools_PayVerbs_DroppedWithoutHuddlePeer — LLM-329: pay / pay_with_item
// are advertised only when the actor has a co-present huddle peer to transact with
// (Surroundings.HuddleMembers non-empty). Both hard-require CurrentHuddleID != ""
// at the substrate — pay resolves the recipient among huddle peers, pay_with_item
// (offer and quote fast-path alike) rejects a non-huddled buyer — so a not-huddled
// actor cued to restock/settle storms a doomed call across ticks (Hannah Boggs:
// pay_with_item at an absent seller 23× / 4 min). The pay analog of the speak
// audience gate, narrowed to the huddle subset: a co-present but NOT-yet-huddled
// walk-in (CoPresent) can be greeted but not paid — speak stays, the pay verbs do
// not — because the buyer must form the conversation before a payment can resolve.
func TestGateTools_PayVerbs_DroppedWithoutHuddlePeer(t *testing.T) {
	r := gatingRegistryWithPay(t)

	// Alone with no audience at all → both pay verbs dropped.
	alone := specNameSet(gateTools(r, perception.Payload{ActorID: "keeper"}, nil))
	for _, v := range []string{"pay", "pay_with_item"} {
		if alone[v] != 0 {
			t.Errorf("%q advertised to a lone actor with no huddle peer; count %d", v, alone[v])
		}
	}

	// A co-present but not-yet-huddled walk-in (CoPresent) enables speak (greet the
	// newcomer) but NOT the pay verbs — the buyer isn't in a huddle yet.
	walkin := specNameSet(gateTools(r, perception.Payload{
		ActorID:      "keeper",
		Surroundings: perception.SurroundingsView{CoPresent: []perception.HuddleMember{{ID: "customer"}}},
	}, nil))
	for _, v := range []string{"pay", "pay_with_item"} {
		if walkin[v] != 0 {
			t.Errorf("%q advertised with only a not-yet-huddled walk-in present; count %d", v, walkin[v])
		}
	}
	if walkin["speak"] != 1 {
		t.Errorf("speak should stay advertised to greet a co-present walk-in; count %d", walkin["speak"])
	}

	// In a huddle with a peer → both pay verbs advertised (a resolvable co-present party).
	huddled := specNameSet(gateTools(r, perception.Payload{
		ActorID:      "keeper",
		Surroundings: perception.SurroundingsView{HuddleMembers: []perception.HuddleMember{{ID: "peer"}}},
	}, nil))
	for _, v := range []string{"pay", "pay_with_item"} {
		if huddled[v] != 1 {
			t.Errorf("%q should be advertised to a huddled actor with a co-present peer; count %d", v, huddled[v])
		}
	}
}

// TestGateTools_WithdrawPay_AdvertisedOnlyWithOwnPendingOffer — LLM-322:
// withdraw_pay (buyer-side) is advertised only when the actor holds an own
// still-pending offer to retract (payload.PendingOffersFromMe, the same standing
// view the "## Offers you have standing" cue renders from). A seller-side
// pending offer (PayOffersForMe) does NOT unlock it — that gates the
// accept/decline/counter group. Before LLM-322 it was advertised every tick.
func TestGateTools_WithdrawPay_AdvertisedOnlyWithOwnPendingOffer(t *testing.T) {
	r := gatingTestRegistry(t) // RegisterPayWithItemFamily registers withdraw_pay

	// A seller-side offer present but no OWN outgoing offer → dropped.
	none := specNameSet(gateTools(r, payOfferPayload(1), nil))
	if none[withdrawPayToolName] != 0 {
		t.Errorf("withdraw_pay advertised with no own pending offer; count %d", none[withdrawPayToolName])
	}

	// The actor holds an own pending offer → advertised.
	withOwn := specNameSet(gateTools(r, perception.Payload{
		ActorID:             "buyer",
		PendingOffersFromMe: []perception.PendingOfferView{{}},
	}, nil))
	if withOwn[withdrawPayToolName] != 1 {
		t.Errorf("withdraw_pay should be advertised when the buyer holds an own pending offer; count %d", withOwn[withdrawPayToolName])
	}
}

// gatingRegistryWithSummon extends the gating test registry with the summon
// tool, for the LLM-322 dead-affordance gate.
func gatingRegistryWithSummon(t *testing.T) *Registry {
	t.Helper()
	r := gatingTestRegistry(t)
	if err := RegisterSummon(r); err != nil {
		t.Fatalf("RegisterSummon: %v", err)
	}
	return r
}

// TestGateTools_Summon_Advertised — LLM-323: summon is a live affordance again.
// A messenger is provisioned in the live village (a non-VA NPC carrying
// AttrMessenger) and DispatchSummon resolves a display name → actor id, so the
// LLM-322 advertising gate that dropped summon everywhere is removed. gateTools
// now keeps summon in the advertised set for a stationary actor (it is not
// walk-incompatible, so the mid-walk axis is separate and not asserted here).
func TestGateTools_Summon_Advertised(t *testing.T) {
	r := gatingRegistryWithSummon(t)

	// Precondition: summon IS in the registry's raw advertised specs.
	if specNameSet(r.AdvertisedSpecs())[summonToolName] != 1 {
		t.Fatalf("precondition: summon should be registered in AdvertisedSpecs")
	}

	// gateTools advertises it now — no more LLM-322 drop.
	payloads := []perception.Payload{
		{ActorID: "npc", Surroundings: speakAudience()},
		payOfferPayload(1),
	}
	for i, p := range payloads {
		if got := specNameSet(gateTools(r, p, nil))[summonToolName]; got != 1 {
			t.Errorf("payload %d: summon should be advertised after LLM-323; count %d", i, got)
		}
	}
}

// TestGateTools_LaboringWorkerKeepsPayResponseTools is the tool half of LLM-460
// (code_review): the reactor now wakes a laboring worker for a buyer's pay offer, and
// that wake is only worth anything if the worker can actually SETTLE — "wake and answer",
// not merely "wake". laborAbandonTools strips the commerce tools that would walk a worker
// off her job, and the pay-response group is deliberately excluded from it because those
// settle in place. That exclusion was incidental before LLM-460 (its old rationale
// assumed a busy worker "holds no offer"); it is load-bearing now, so pin it.
//
// Also asserts the strip itself still bites on the same payload — otherwise a regression
// that emptied laborAbandonTools would leave this passing while silently letting a
// mid-job worker start new trades.
func TestGateTools_LaboringWorkerKeepsPayResponseTools(t *testing.T) {
	r := gatingTestRegistry(t)
	p := payOfferPayload(1)
	p.Laboring = &perception.LaboringView{Employer: "boss", Until: time.Now().UTC().Add(2 * time.Hour)}
	names := specNameSet(gateTools(r, p, nil))

	for _, want := range []string{"accept_pay", "decline_pay", "counter_pay"} {
		if names[want] == 0 {
			t.Errorf("laboring worker with a pending offer: %q not advertised — the LLM-460 reactor "+
				"wake exists so this worker can answer the buyer, and stripping the response tools "+
				"makes the wake a wasted tick (the offer then expires exactly as before)", want)
		}
	}
	// The strip must still apply to the tools that WOULD walk her off the job.
	for _, gone := range []string{"pay_with_item", "offer_trade", "sell"} {
		if names[gone] > 0 {
			t.Errorf("laboring worker: %q advertised, want stripped — laborAbandonTools keeps a "+
				"worker mid-job from starting new commerce; only ANSWERING an offer staked against "+
				"her is allowed (LLM-230/LLM-460)", gone)
		}
	}
}
