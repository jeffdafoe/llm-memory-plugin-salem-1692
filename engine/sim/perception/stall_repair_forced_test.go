package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// degradedKeeperPayload builds a keeper at their OWN business who is simultaneously
// being pitched a sale (## Custom at hand) AND holds a waiting pay offer
// (## Offers awaiting your decision) — the mobbed-at-the-stall shape from the live
// Josiah Thorne case. degraded controls whether the shop is worn past the degrade
// line. LLM-304: a degraded shop still sells on-hand stock, so the customer-service
// frame renders whether or not it's degraded.
func degradedKeeperPayload(degraded bool) Payload {
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

// TestRender_DegradedKeeper_KeepsCustomerServiceFrame — LLM-304: a degraded shop
// still SELLS what's on hand (degrade blocks refill, not selling), so the LLM-312
// forced-repair suppression is gone. An owner at their own degraded business with
// nails in hand, mobbed by customers, keeps the full customer-service frame — the
// "## Custom at hand" offer-wares cue AND the "## Offers awaiting your decision"
// pay-offer section render alongside the repair cue — and the old "stop tending
// customers" imperative no longer appears. Serving earns the coin for the nails;
// mending reopens the refill. Degraded now renders like worn-but-open.
func TestRender_DegradedKeeper_KeepsCustomerServiceFrame(t *testing.T) {
	for _, degraded := range []bool{true, false} {
		out := combinedPrompt(Render(degradedKeeperPayload(degraded), DefaultRenderConfig()))
		if !strings.Contains(out, "## Custom at hand") {
			t.Errorf("offer-wares cue must render (degraded=%v; LLM-304 keeps customer service)\n%s", degraded, out)
		}
		if !strings.Contains(out, "## Offers awaiting your decision") {
			t.Errorf("pay-offer decision section must render (degraded=%v)\n%s", degraded, out)
		}
		if strings.Contains(out, "stop tending customers") {
			t.Errorf("obsolete forced-repair wording must not fire (degraded=%v)\n%s", degraded, out)
		}
	}
}

// TestRenderStallRepair_HiredDegradedWithNails_NoOwnerImperative — a HIRED worker at
// a degraded employer business with nails takes the hired branch ("## The business
// you're working at") and must never emit owner-exclusive wording ("stop tending
// customers" / "reopens your doors" — obsolete after LLM-304, guarded here against
// reintroduction), which would falsely imply ownership and mismatch the tools
// (gateTools does not strip trade tools for a hired worker).
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
