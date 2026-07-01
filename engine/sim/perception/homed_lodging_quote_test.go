package perception

import (
	"strings"
	"testing"
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
