package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestHomedGuestLodgingQuoteSuppressed pins the LLM-208 buyer-side guard: a homed
// guest carrying a targeted nights_stay quote must NOT be shown the room-offer
// take — filterHomedLodgingQuoteWarrants drops the lodging quote warrant at build,
// because a homed buyer can't take a room (the pay_with_item guard rejects it,
// LLM-182). Clearing the guest's home restores the take, proving the home is the
// sole cause and that the suppression is scoped to homed viewers — a homeless
// seeker in the same scene still perceives the room offer.
func TestHomedGuestLodgingQuoteSuppressed(t *testing.T) {
	snap, actorID, warrants := homedGuestLodgingQuoteSuppressed()

	homed := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))
	if strings.Contains(homed, "pay_with_item with quote_id") {
		t.Errorf("homed guest was shown a lodging room-offer take (should be suppressed):\n%s", homed)
	}
	if strings.Contains(homed, "nights_stay") {
		t.Errorf("homed guest's prompt still mentions the nights_stay offer (warrant not dropped):\n%s", homed)
	}

	// Control: clear the home. The same nights_stay quote must now surface — proving
	// the home is what suppressed it (not an unrelated gate) and that a homeless
	// seeker still perceives the room offer.
	snap.Actors[actorID].HomeStructureID = ""
	homeless := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))
	if !strings.Contains(homeless, "pay_with_item with quote_id") {
		t.Errorf("homeless seeker was NOT shown the lodging room-offer take (guard over-suppresses):\n%s", homeless)
	}
}

// TestHomedGuestLodgingQuoteSuppressed_OverheardPublic covers the PUBLIC/overheard
// path: a nights_stay quote that reaches the homed guest via huddle fan-out
// (Overheard=true) rather than a target_buyer stamp. The build-time filter must
// drop it too — the design relies on SceneQuoteTargetedWarrantReason representing
// both targeted and public quote warrants, so this pins that a public room offer
// is suppressed for a homed viewer and still surfaces for a homeless one. Guards
// against a future reactor split of public quotes into a separate reason type
// silently bypassing the guard (code_review, LLM-208).
func TestHomedGuestLodgingQuoteSuppressed_OverheardPublic(t *testing.T) {
	snap, actorID, _ := homedGuestLodgingQuoteSuppressed()
	warrants := []sim.WarrantMeta{{
		TriggerActorID: "john",
		Reason: sim.SceneQuoteTargetedWarrantReason{
			QuoteID: 1, SellerID: "john", Overheard: true,
			Lines:  []sim.QuoteLine{{ItemKind: "nights_stay", Qty: 1}},
			Amount: 4,
		},
		SourceEventID: 1,
	}}

	homed := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))
	if strings.Contains(homed, "pay_with_item with quote_id") {
		t.Errorf("homed guest was shown an overheard/public lodging room-offer take (should be suppressed):\n%s", homed)
	}

	snap.Actors[actorID].HomeStructureID = ""
	homeless := combinedPrompt(Render(Build(snap, actorID, warrants), DefaultRenderConfig()))
	if !strings.Contains(homeless, "pay_with_item with quote_id") {
		t.Errorf("homeless seeker was NOT shown the overheard/public lodging room-offer take:\n%s", homeless)
	}
}
