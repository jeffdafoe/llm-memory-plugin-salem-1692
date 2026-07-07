package handlers

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// TestGateTools_RepairOnlyWithStallCue — the repair tool is advertised in EXACTLY
// the situation that renders the "## Your business" cue (payload.StallRepair
// non-nil: the owner stands at their own worn business), and nowhere else. Same
// discussion-109 "advertise a tool only with its triggering perception" invariant
// as craft/gather.
func TestGateTools_RepairOnlyWithStallCue(t *testing.T) {
	r := NewRegistry()
	if err := RegisterRepair(r); err != nil {
		t.Fatalf("RegisterRepair: %v", err)
	}

	// No StallRepair cue → repair is not advertised.
	none := specNameSet(gateTools(r, perception.Payload{ActorID: "ezekiel"}, nil))
	if none["repair"] != 0 {
		t.Errorf("repair advertised with no '## Your business' cue (count %d)", none["repair"])
	}

	// At the owner's own worn business (StallRepair present) → repair is advertised once.
	at := specNameSet(gateTools(r, perception.Payload{
		ActorID:     "ezekiel",
		StallRepair: &perception.StallRepairView{NailsNeeded: 5, NailsHeld: 2},
	}, nil))
	if at["repair"] != 1 {
		t.Errorf("repair not advertised at the owner's worn business (count %d)", at["repair"])
	}
}

// TestGateTools_RepairAdvertisedToLaboringHiredWorker (LLM-271) — a hired worker
// mid-job (payload.Laboring set) at their employer's worn business (StallRepair set,
// Hired) is STILL advertised the repair tool. repair is not in laborAbandonTools, so
// the laboring speak-only strip must not remove it — otherwise the surfaced "## The
// business you're working at" cue would have no tool behind it, and the wake would
// be wasted. The buildStallRepair cue is what sets StallRepair (hired or owner); the
// gate keys only on its presence, so this pins that the laboring gate leaves it be.
func TestGateTools_RepairAdvertisedToLaboringHiredWorker(t *testing.T) {
	r := NewRegistry()
	if err := RegisterRepair(r); err != nil {
		t.Fatalf("RegisterRepair: %v", err)
	}
	got := specNameSet(gateTools(r, perception.Payload{
		ActorID:     "lewis",
		Laboring:    &perception.LaboringView{},
		StallRepair: &perception.StallRepairView{Hired: true, NailsNeeded: 5, NailsHeld: 5},
	}, nil))
	if got["repair"] != 1 {
		t.Errorf("repair not advertised to a laboring hired worker at the employer's worn business (count %d)", got["repair"])
	}
}

// TestGateTools_ForcedRepair_StripsCustomerServiceTools — LLM-312: an owner at
// their OWN degraded (shut-for-trade) business holding enough nails to mend
// (StallRepairView.ForcesRepair()) has the customer-service decision tools
// stripped — sell, offer_trade, and the pay-response group (accept/decline/
// counter_pay) — EVEN with a live pending offer that would otherwise advertise
// them. Perception drops the matching cues (renderOfferableCustomers,
// renderPayOffers) on the same ForcesRepair() signal, so cue and tool move
// together. repair stays (the move); speak/pay_with_item/done stay for a word or
// a red survival need. This is the tool-side of the live Josiah Thorne case
// where 37 straight ticks with 5 nails in hand never produced a repair call.
func TestGateTools_ForcedRepair_StripsCustomerServiceTools(t *testing.T) {
	r := gatingTestRegistry(t) // speak, pay_with_item family (incl. accept/decline/counter_pay), done
	if err := RegisterRepair(r); err != nil {
		t.Fatalf("RegisterRepair: %v", err)
	}
	if err := RegisterSceneQuote(r); err != nil {
		t.Fatalf("RegisterSceneQuote: %v", err) // sell
	}
	if err := RegisterOfferTrade(r); err != nil {
		t.Fatalf("RegisterOfferTrade: %v", err)
	}

	// Own DEGRADED business + enough nails + a live pending offer (would normally
	// advertise the pay-response group).
	forced := payOfferPayload(17)
	forced.StallRepair = &perception.StallRepairView{Degraded: true, HasEnoughNails: true, NailsNeeded: 5, NailsHeld: 5}
	got := specNameSet(gateTools(r, forced, nil))

	for _, stripped := range []string{"sell", "offer_trade", "accept_pay", "decline_pay", "counter_pay"} {
		if got[stripped] != 0 {
			t.Errorf("%q advertised in the shut-shop forced-repair state; count %d", stripped, got[stripped])
		}
	}
	if got["repair"] != 1 {
		t.Errorf("repair must stay advertised in the forced-repair state; count %d", got["repair"])
	}
	for _, kept := range []string{"speak", "done"} {
		if got[kept] != 1 {
			t.Errorf("%q should stay advertised (a word / the terminal); count %d", kept, got[kept])
		}
	}

	// Control: worn but NOT degraded — the shop still trades, so ForcesRepair is
	// false and nothing is stripped: sell/trade + the pay-response group all stand
	// alongside repair.
	open := payOfferPayload(17)
	open.StallRepair = &perception.StallRepairView{Degraded: false, HasEnoughNails: true, NailsNeeded: 5, NailsHeld: 5}
	openGot := specNameSet(gateTools(r, open, nil))
	for _, present := range []string{"sell", "offer_trade", "accept_pay", "decline_pay", "counter_pay", "repair"} {
		if openGot[present] != 1 {
			t.Errorf("%q must stay advertised at a worn-but-open business; count %d", present, openGot[present])
		}
	}
}
