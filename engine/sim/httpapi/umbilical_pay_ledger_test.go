package httpapi

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func TestUmbilicalPayLedgerFromSnapshot(t *testing.T) {
	now := time.Date(2026, 6, 5, 16, 46, 0, 0, time.UTC)
	snap := &sim.Snapshot{
		PayLedger: map[sim.LedgerID]*sim.PayLedgerEntry{
			1: {ID: 1, BuyerID: "buyerA", SellerID: "sellerA", ItemKind: "meat", Qty: 1, Amount: 5, State: sim.PayLedgerStateAccepted, CreatedAt: now},
			3: {ID: 3, BuyerID: "buyerB", SellerID: "sellerB", ItemKind: "stew", Qty: 2, ConsumeNow: true, ConsumerIDs: []sim.ActorID{"eaterX", "eaterY"}, Amount: 8, State: sim.PayLedgerStatePending, CreatedAt: now},
			2: nil, // a nil entry must be skipped, not panic
		},
	}

	out := umbilicalPayLedgerFromSnapshot(snap, 0)
	if out.Total != 2 || out.Returned != 2 {
		t.Fatalf("total/returned = %d/%d, want 2/2 (nil entry skipped)", out.Total, out.Returned)
	}
	// Most-recent first: id 3 before id 1.
	if out.Entries[0].ID != 3 || out.Entries[1].ID != 1 {
		t.Fatalf("order = [%d,%d], want [3,1] (id desc)", out.Entries[0].ID, out.Entries[1].ID)
	}
	// The buyer/CONSUMER split is surfaced — the field that distinguishes a paid
	// purchase from a non-paying-consumer ride (ZBBS-HOME-391's prime suspect).
	e3 := out.Entries[0]
	if e3.BuyerID != "buyerB" || !e3.ConsumeNow || len(e3.ConsumerIDs) != 2 || e3.ConsumerIDs[0] != "eaterX" {
		t.Errorf("entry 3 buyer/consumer mapping wrong: %+v", e3)
	}
	if e3.Amount != 8 || e3.State != "pending" {
		t.Errorf("entry 3 amount/state wrong: amount=%d state=%q", e3.Amount, e3.State)
	}

	// limit caps to the newest.
	if got := umbilicalPayLedgerFromSnapshot(snap, 1); got.Returned != 1 || got.Total != 2 || got.Entries[0].ID != 3 {
		t.Errorf("limit=1 should return only the newest (id 3): total=%d returned=%d", got.Total, got.Returned)
	}

	// nil snapshot → empty, no panic.
	if got := umbilicalPayLedgerFromSnapshot(nil, 0); got.Total != 0 || len(got.Entries) != 0 {
		t.Errorf("nil snapshot should yield empty, got %+v", got)
	}
}
