package pg

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// visitors_plan_test.go — pure round-trip coverage for the visitor.plan jsonb
// codec (LLM-373): encodeVisitorPlan (off the live Actor) → applyVisitorPlan (onto
// a LoadedVisitor). No DB — the DB integration path is covered by
// visitors_integration_test.go.

func TestVisitorPlanRoundTrip(t *testing.T) {
	roomExpiry := time.Now().UTC().Add(8 * time.Hour)
	created := time.Now().UTC().Add(-time.Hour)
	a := &sim.Actor{
		ID:        "vstr-00001234",
		Inventory: map[sim.ItemKind]int{"cheese": 3, "ale": 2},
		Coins:     42,
		RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 5, Source: sim.AccessSourceLedger}: {
				RoomID: 5, Source: sim.AccessSourceLedger, LedgerID: 12,
				ExpiresAt: &roomExpiry, Active: true, CreatedAt: created,
			},
		},
		VisitorState: &sim.VisitorState{
			VisitedBusinesses: []sim.StructureID{"str-a", "str-b"},
			// LLM-455 merchant errand rides the plan jsonb.
			Trade: &sim.TradeErrand{Direction: sim.TradeDirectionBuy, Good: "cheese", Counterparty: "str-a", Settled: true},
		},
	}

	js, err := encodeVisitorPlan(a)
	if err != nil {
		t.Fatalf("encodeVisitorPlan: %v", err)
	}
	lv := &sim.LoadedVisitor{VisitorState: &sim.VisitorState{}}
	if err := applyVisitorPlan([]byte(js), lv); err != nil {
		t.Fatalf("applyVisitorPlan: %v", err)
	}

	if len(lv.VisitorState.VisitedBusinesses) != 2 ||
		lv.VisitorState.VisitedBusinesses[0] != "str-a" || lv.VisitorState.VisitedBusinesses[1] != "str-b" {
		t.Errorf("VisitedBusinesses = %v; want [str-a str-b]", lv.VisitorState.VisitedBusinesses)
	}
	if lv.Coins != 42 {
		t.Errorf("Coins = %d; want 42", lv.Coins)
	}
	if lv.VisitorState.Trade == nil {
		t.Fatal("Trade errand did not round-trip through the plan jsonb")
	}
	if lv.VisitorState.Trade.Direction != sim.TradeDirectionBuy || lv.VisitorState.Trade.Good != "cheese" ||
		lv.VisitorState.Trade.Counterparty != "str-a" || !lv.VisitorState.Trade.Settled {
		t.Errorf("Trade round-trip = %+v; want buy cheese @ str-a settled", lv.VisitorState.Trade)
	}
	if lv.Inventory["cheese"] != 3 || lv.Inventory["ale"] != 2 {
		t.Errorf("Inventory = %v; want cheese:3 ale:2", lv.Inventory)
	}
	g := lv.RoomAccess[sim.RoomAccessKey{RoomID: 5, Source: sim.AccessSourceLedger}]
	if g == nil || g.LedgerID != 12 || !g.Active || g.ExpiresAt == nil || !g.ExpiresAt.Equal(roomExpiry) {
		t.Errorf("restored grant = %+v; want active ledger grant ledger=12 expiry=%v", g, roomExpiry)
	}
}

// TestVisitorPlanEmpty — an absent / empty plan (an old-engine row, or a
// freshly-spawned visitor before its first checkpoint) applies as a clean no-op,
// leaving the LoadedVisitor at its zero plan.
func TestVisitorPlanEmpty(t *testing.T) {
	lv := &sim.LoadedVisitor{VisitorState: &sim.VisitorState{Phase: sim.VisitorPhasePresent}}
	if err := applyVisitorPlan(nil, lv); err != nil {
		t.Fatalf("applyVisitorPlan(nil): %v", err)
	}
	if err := applyVisitorPlan([]byte("{}"), lv); err != nil {
		t.Fatalf("applyVisitorPlan({}): %v", err)
	}
	if lv.Coins != 0 || lv.Inventory != nil || lv.RoomAccess != nil ||
		lv.VisitorState.VisitedBusinesses != nil {
		t.Errorf("empty plan mutated the visitor: coins=%d inv=%v room=%v visited=%v",
			lv.Coins, lv.Inventory, lv.RoomAccess, lv.VisitorState.VisitedBusinesses)
	}

	// An actor carrying nothing encodes to a minimal object that round-trips clean.
	empty := &sim.Actor{ID: "vstr-0000eeee", VisitorState: &sim.VisitorState{}}
	js, err := encodeVisitorPlan(empty)
	if err != nil {
		t.Fatalf("encodeVisitorPlan(empty): %v", err)
	}
	lv2 := &sim.LoadedVisitor{VisitorState: &sim.VisitorState{}}
	if err := applyVisitorPlan([]byte(js), lv2); err != nil {
		t.Fatalf("applyVisitorPlan(empty): %v", err)
	}
	if lv2.Coins != 0 || len(lv2.Inventory) != 0 || len(lv2.RoomAccess) != 0 {
		t.Errorf("empty actor did not round-trip clean: %+v", lv2)
	}
}
