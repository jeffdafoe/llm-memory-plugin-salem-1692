package perception

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestRenderPayResolved_SupersededByLot: the buyer's line when the seller's lot
// set their offer aside names the old offer and the lot's terms; with no terms it
// still says the offer is gone and a lot stands instead.
func TestRenderPayResolved_SupersededByLot(t *testing.T) {
	r := sim.PayResolvedWarrantReason{ItemKind: "fried_meat", Qty: 1,
		TerminalState: sim.PayTerminalStateSupersededByLot, Message: "1 fried meat and 1 flask of water for 8 coins"}
	if got, want := renderPayResolvedWarrantLine(1, "Hannah Boggs", r, 400),
		"1. Hannah Boggs set your offer for fried_meat aside and offers you a lot instead: 1 fried meat and 1 flask of water for 8 coins.\n"; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
	r.Message = ""
	if got, want := renderPayResolvedWarrantLine(1, "Hannah Boggs", r, 400),
		"1. Hannah Boggs set your offer for fried_meat aside and offers you a lot instead.\n"; got != want {
		t.Errorf("line without terms = %q, want %q", got, want)
	}
}
