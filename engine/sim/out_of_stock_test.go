package sim

import (
	"testing"
	"time"
)

// out_of_stock_test.go — ZBBS-HOME-363 capture subscriber. White-box (package
// sim) so it drives handleOutOfStockOnResolved directly with a
// PayWithItemResolved event.

func oosWorld() *World {
	return &World{Actors: make(map[ActorID]*Actor)}
}

func resolved(buyer, seller ActorID, item ItemKind, state PayTerminalState, at time.Time) *PayWithItemResolved {
	return &PayWithItemResolved{BuyerID: buyer, SellerID: seller, ItemKind: item, TerminalState: state, At: at}
}

func TestOutOfStock_RecordsOnInsufficientStock(t *testing.T) {
	w := oosWorld()
	now := time.Now()
	buyer := &Actor{ID: "prudence", Kind: KindNPCStateful}
	w.Actors["prudence"] = buyer
	w.Actors["moses"] = &Actor{ID: "moses", Kind: KindNPCStateful, WorkStructureID: "james_farm"}

	handleOutOfStockOnResolved(w, resolved("prudence", "moses", "carrots", PayTerminalStateFailedInsufficientStock, now))

	if _, ok := buyer.OutOfStockObs[OutOfStockKey{StructureID: "james_farm", ItemKind: "carrots"}]; !ok {
		t.Fatalf("expected out-of-stock memory for (james_farm, carrots), got %v", buyer.OutOfStockObs)
	}
}

func TestOutOfStock_ClearsOnAccepted(t *testing.T) {
	w := oosWorld()
	now := time.Now()
	buyer := &Actor{ID: "prudence", Kind: KindNPCStateful, OutOfStockObs: map[OutOfStockKey]time.Time{
		{StructureID: "james_farm", ItemKind: "carrots"}: now.Add(-time.Hour),
	}}
	w.Actors["prudence"] = buyer
	w.Actors["moses"] = &Actor{ID: "moses", Kind: KindNPCStateful, WorkStructureID: "james_farm"}

	handleOutOfStockOnResolved(w, resolved("prudence", "moses", "carrots", PayTerminalStateAccepted, now))

	if _, ok := buyer.OutOfStockObs[OutOfStockKey{StructureID: "james_farm", ItemKind: "carrots"}]; ok {
		t.Fatalf("successful buy should clear the out-of-stock memory, got %v", buyer.OutOfStockObs)
	}
}

func TestOutOfStock_SkipsCoPresentPeer(t *testing.T) {
	w := oosWorld()
	now := time.Now()
	buyer := &Actor{ID: "prudence", Kind: KindNPCStateful}
	w.Actors["prudence"] = buyer
	// Seller has no workplace → a co-present peer, nothing to walk-avoid.
	w.Actors["peer"] = &Actor{ID: "peer", Kind: KindNPCStateful}

	handleOutOfStockOnResolved(w, resolved("prudence", "peer", "carrots", PayTerminalStateFailedInsufficientStock, now))

	if len(buyer.OutOfStockObs) != 0 {
		t.Fatalf("no-workplace seller should record nothing, got %v", buyer.OutOfStockObs)
	}
}

func TestOutOfStock_SkipsNonAgentBuyer(t *testing.T) {
	w := oosWorld()
	now := time.Now()
	buyer := &Actor{ID: "player", Kind: KindPC} // PCs don't get experiential memory
	w.Actors["player"] = buyer
	w.Actors["moses"] = &Actor{ID: "moses", Kind: KindNPCStateful, WorkStructureID: "james_farm"}

	handleOutOfStockOnResolved(w, resolved("player", "moses", "carrots", PayTerminalStateFailedInsufficientStock, now))

	if len(buyer.OutOfStockObs) != 0 {
		t.Fatalf("PC buyer should record nothing, got %v", buyer.OutOfStockObs)
	}
}

func TestOutOfStock_IgnoresUnrelatedTerminal(t *testing.T) {
	w := oosWorld()
	now := time.Now()
	buyer := &Actor{ID: "prudence", Kind: KindNPCStateful}
	w.Actors["prudence"] = buyer
	w.Actors["moses"] = &Actor{ID: "moses", Kind: KindNPCStateful, WorkStructureID: "james_farm"}

	// Insufficient FUNDS is not a stock signal → neither record nor clear.
	handleOutOfStockOnResolved(w, resolved("prudence", "moses", "carrots", PayTerminalStateFailedInsufficientFunds, now))

	if len(buyer.OutOfStockObs) != 0 {
		t.Fatalf("non-stock terminal should record nothing, got %v", buyer.OutOfStockObs)
	}
}
