package sim

import (
	"testing"
	"time"
)

// trade_reversal_internal_test.go — LLM-555. The capture half: what the seller
// remembers after a sale, and what they deliberately do not.

func tradeReversalWorld(t *testing.T) (*World, *Actor, *Actor) {
	t.Helper()
	w := &World{
		Actors:    map[ActorID]*Actor{},
		PayLedger: map[LedgerID]*PayLedgerEntry{},
	}
	seller := &Actor{ID: "seller", Kind: KindNPCStateful, DisplayName: "Hannah Boggs"}
	buyer := &Actor{ID: "buyer", Kind: KindNPCStateful, DisplayName: "Josiah Thorne"}
	w.Actors[seller.ID] = seller
	w.Actors[buyer.ID] = buyer
	return w, seller, buyer
}

func TestTradeReversalStampsSellerOnAcceptedSale(t *testing.T) {
	w, seller, buyer := tradeReversalWorld(t)
	at := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)

	handleTradeReversalOnResolved(w, &PayWithItemResolved{
		LedgerID: 1, BuyerID: buyer.ID, SellerID: seller.ID,
		ItemKind: "firewood", TerminalState: PayTerminalStateAccepted, At: at,
	})

	if !ActorRecentlySoldTo(seller, buyer.ID, "firewood", at) {
		t.Fatal("an accepted sale must leave the seller remembering it")
	}
	// The memory is the SELLER's. The buyer learns nothing that would stop him
	// buying more firewood from her, which is an ordinary repeat purchase.
	if ActorRecentlySoldTo(buyer, seller.ID, "firewood", at) {
		t.Error("the memory must not be stamped on the buyer — it would suppress an ordinary repeat purchase")
	}
}

// TestTradeReversalMemoryIsDirectionalAndScoped is the sim-side statement of the
// property that keeps the distributor whole: buying in and selling on is his
// entire function, and only the round trip with ONE partner over ONE good is the
// defect. Josiah restocked flour from the mill twice in thirty-five minutes on the
// afternoon this was diagnosed; nothing here may touch that.
func TestTradeReversalMemoryIsDirectionalAndScoped(t *testing.T) {
	w, seller, buyer := tradeReversalWorld(t)
	other := &Actor{ID: "other", Kind: KindNPCStateful, DisplayName: "Martha Reed"}
	w.Actors[other.ID] = other
	at := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)

	handleTradeReversalOnResolved(w, &PayWithItemResolved{
		LedgerID: 1, BuyerID: buyer.ID, SellerID: seller.ID,
		ItemKind: "firewood", TerminalState: PayTerminalStateAccepted, At: at,
	})

	if ActorRecentlySoldTo(seller, buyer.ID, "flour", at) {
		t.Error("a different good from the same partner must stay buyable")
	}
	if ActorRecentlySoldTo(seller, other.ID, "firewood", at) {
		t.Error("the same good from a different partner must stay buyable — the defect is the round trip, not the resale")
	}
	if ActorRecentlySoldTo(seller, buyer.ID, "firewood", at.Add(SoldToPeerMemoryTTL+time.Minute)) {
		t.Error("the memory must lapse at SoldToPeerMemoryTTL so a pair can trade again later")
	}
}

func TestTradeReversalIgnoresNonSales(t *testing.T) {
	at := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)

	t.Run("declined offer moves no goods", func(t *testing.T) {
		w, seller, buyer := tradeReversalWorld(t)
		handleTradeReversalOnResolved(w, &PayWithItemResolved{
			LedgerID: 1, BuyerID: buyer.ID, SellerID: seller.ID,
			ItemKind: "firewood", TerminalState: PayTerminalStateDeclined, At: at,
		})
		if ActorRecentlySoldTo(seller, buyer.ID, "firewood", at) {
			t.Error("a declined offer gives the pair nothing to churn")
		}
	})

	// A gift inverts the roles on the ledger entry (BuyerID is the GIVER) and
	// carries its goods in PayItems with an empty ItemKind, so stamping one would
	// write a backwards memory under an empty key — which, being empty-keyed, is
	// not merely useless but would read as a memory about no item at all.
	t.Run("gift is not a sale", func(t *testing.T) {
		w, seller, buyer := tradeReversalWorld(t)
		w.PayLedger[2] = &PayLedgerEntry{ID: 2, IsGift: true, BuyerID: buyer.ID, SellerID: seller.ID}
		handleTradeReversalOnResolved(w, &PayWithItemResolved{
			LedgerID: 2, BuyerID: buyer.ID, SellerID: seller.ID,
			ItemKind: "firewood", TerminalState: PayTerminalStateAccepted, At: at,
		})
		if ActorRecentlySoldTo(seller, buyer.ID, "firewood", at) {
			t.Error("give is ungated by design (LLM-544); handing someone goods is not a sale a later purchase reverses")
		}
	})

	t.Run("non-agent seller has no memory to write", func(t *testing.T) {
		w, seller, buyer := tradeReversalWorld(t)
		seller.Kind = KindPC
		handleTradeReversalOnResolved(w, &PayWithItemResolved{
			LedgerID: 1, BuyerID: buyer.ID, SellerID: seller.ID,
			ItemKind: "firewood", TerminalState: PayTerminalStateAccepted, At: at,
		})
		if seller.Observed.Len() != 0 {
			t.Error("a PC carries its own continuity through the player")
		}
	})
}

// TestTradeReversalStampsEveryBundleLine covers the hole a bundle would otherwise
// be: a quote-take (LLM-101) leaves ItemKind empty and carries its goods in Lines,
// so keying only on ItemKind would let the churn through whenever the sale was a
// bundle.
func TestTradeReversalStampsEveryBundleLine(t *testing.T) {
	w, seller, buyer := tradeReversalWorld(t)
	at := time.Date(2026, 7, 28, 20, 0, 0, 0, time.UTC)

	handleTradeReversalOnResolved(w, &PayWithItemResolved{
		LedgerID: 1, BuyerID: buyer.ID, SellerID: seller.ID,
		Lines: []QuoteLine{
			{ItemKind: "firewood", Qty: 2},
			{ItemKind: "cheese", Qty: 1},
			{ItemKind: "", Qty: 1}, // malformed: must not stamp an empty key
		},
		TerminalState: PayTerminalStateAccepted, At: at,
	})

	for _, kind := range []ItemKind{"firewood", "cheese"} {
		if !ActorRecentlySoldTo(seller, buyer.ID, kind, at) {
			t.Errorf("every line of a bundle is as much a sale as a single item: %s not remembered", kind)
		}
	}
	if seller.Observed.Len() != 2 {
		t.Errorf("an empty line kind must not stamp a memory: got %d entries, want 2", seller.Observed.Len())
	}
}
