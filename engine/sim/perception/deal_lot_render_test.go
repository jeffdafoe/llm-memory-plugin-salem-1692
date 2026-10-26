package perception

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// TestRenderBundleTake_NamesTheGoods is LLM-867, the live 10-25 Inn shape: a
// bundle's ItemKind is empty, so both sides read "for ." until the lines rode
// the warrants. A single item renders as before.
func TestRenderBundleTake_NamesTheGoods(t *testing.T) {
	lines := []sim.QuoteLine{{ItemKind: "porridge", Qty: 1}, {ItemKind: "fried_meat", Qty: 1}}
	buyer := sim.PayResolvedWarrantReason{Amount: 8, TerminalState: sim.PayTerminalStateAccepted, Lines: lines}
	if got, want := renderPayResolvedWarrantLine(1, "Hannah Boggs", buyer, 400),
		"1. Hannah Boggs accepted your offer of 8 for porridge and fried_meat.\n"; got != want {
		t.Errorf("buyer line = %q, want %q", got, want)
	}
	seller := sim.ServeHandoverWarrantReason{Amount: 8, ConsumeNow: true, Lines: lines}
	if got, want := renderServeHandoverWarrantLine(1, "Elizabeth Ellis", seller),
		"1. Elizabeth Ellis paid you 8 coins for porridge and fried_meat, to eat here now. Hand it over with a word.\n"; got != want {
		t.Errorf("seller line = %q, want %q", got, want)
	}
	single := sim.PayResolvedWarrantReason{ItemKind: "firewood", Qty: 5, Amount: 10, TerminalState: sim.PayTerminalStateAccepted}
	if got, want := renderPayResolvedWarrantLine(1, "Ezekiel Crane", single, 400),
		"1. Ezekiel Crane accepted your offer of 10 for 5 firewood.\n"; got != want {
		t.Errorf("single-item line = %q, want %q", got, want)
	}
}

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
