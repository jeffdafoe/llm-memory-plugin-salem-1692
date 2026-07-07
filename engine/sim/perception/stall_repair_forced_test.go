package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// forcedRepairPayload builds a keeper at their OWN business who is simultaneously
// being pitched a sale (## Custom at hand) AND holds a waiting pay offer
// (## Offers awaiting your decision) — the mobbed-at-the-stall shape from the
// live Josiah Thorne case (LLM-312). degraded controls whether the shop is shut
// (wear past the degrade line); degraded + nails-in-hand is the ForcesRepair
// state where the customer-service frame is suppressed.
func forcedRepairPayload(degraded bool) Payload {
	return Payload{
		ActorID: "josiah",
		Actor:   ActorView{State: sim.StateIdle},
		StallRepair: &StallRepairView{
			Name: "General Store", Degraded: degraded,
			HasEnoughNails: true, NailsNeeded: 5, NailsHeld: 5,
		},
		OfferableCustomers: &OfferableCustomersView{
			CustomerNames: []string{"Moses James"},
			Goods:         []OfferableGood{{Label: "Carrots", OnHand: 5}},
		},
		PayOffersForMe:    []sim.PayOfferWarrantReason{payOfferReason(17, "bob", "stew", 2, 12, true)},
		WarrantActorNames: map[sim.ActorID]string{"bob": "bob"},
		Baseline:          BaselinePresent,
		Surroundings:      SurroundingsView{HuddleMembers: []HuddleMember{{ID: "moses"}}},
	}
}

// TestRender_ForcedRepair_SuppressesCustomerServiceFrame — LLM-312: an owner at
// their own DEGRADED (shut-for-trade) business holding enough nails to mend gets
// the customer-service frame suppressed — no "## Custom at hand" offer-wares cue,
// no "## Offers awaiting your decision" pay-offer section — leaving the imperative
// repair line as the sole call to action. Perception drops both on the same
// StallRepair.ForcesRepair() signal that gateTools strips the matching pay/trade
// tools from, so cue and tool move together (discussion-109). This is the
// render-side of the live case where 37 straight ticks with 5 nails in hand,
// mobbed by customers, never produced a repair call. Worn-but-open (control)
// keeps both frames — the shop can still trade.
func TestRender_ForcedRepair_SuppressesCustomerServiceFrame(t *testing.T) {
	shut := combinedPrompt(Render(forcedRepairPayload(true), DefaultRenderConfig()))
	if strings.Contains(shut, "## Custom at hand") {
		t.Errorf("offer-wares cue must be suppressed in the shut-shop forced-repair state\n%s", shut)
	}
	if strings.Contains(shut, "## Offers awaiting your decision") {
		t.Errorf("pay-offer decision section must be suppressed in the shut-shop forced-repair state\n%s", shut)
	}
	if !strings.Contains(shut, "stop tending customers and call the repair tool now") {
		t.Errorf("imperative repair line missing in the shut-shop forced-repair state\n%s", shut)
	}

	// Control: worn but NOT degraded — the shop still trades, so neither frame is
	// suppressed and the imperative shut-shop wording does not fire.
	open := combinedPrompt(Render(forcedRepairPayload(false), DefaultRenderConfig()))
	if !strings.Contains(open, "## Custom at hand") {
		t.Errorf("offer-wares cue should render at a worn-but-open business\n%s", open)
	}
	if !strings.Contains(open, "## Offers awaiting your decision") {
		t.Errorf("pay-offer decision section should render at a worn-but-open business\n%s", open)
	}
	if strings.Contains(open, "stop tending customers") {
		t.Errorf("imperative shut-shop wording must not fire at a worn-but-open business\n%s", open)
	}
}

// TestRenderStallRepair_HiredDegradedWithNails_NoOwnerImperative — a HIRED worker
// at a degraded employer business with nails is NOT the owner ForcesRepair state
// (ForcesRepair excludes Hired): the hired branch returns the "## The business
// you're working at" cue and must never emit the owner-exclusive imperative
// ("stop tending customers" / "reopens your doors"), which would falsely imply
// ownership and mismatch the tools (gateTools does not strip trade tools for a
// hired worker). Pins the hired early-return that keeps the owner branch out of
// reach (LLM-312 review follow-up).
func TestRenderStallRepair_HiredDegradedWithNails_NoOwnerImperative(t *testing.T) {
	var b strings.Builder
	renderStallRepair(&b, &StallRepairView{
		Hired: true, Degraded: true, HasEnoughNails: true,
		NailsNeeded: 5, NailsHeld: 5, Name: "Ellis Farm",
	})
	out := b.String()
	for _, ownerOnly := range []string{"stop tending customers", "reopens your doors"} {
		if strings.Contains(out, ownerOnly) {
			t.Errorf("hired worker got owner-exclusive wording %q\n%s", ownerOnly, out)
		}
	}
	if !strings.Contains(out, "## The business you're working at") {
		t.Errorf("hired repair cue missing\n%s", out)
	}
}
