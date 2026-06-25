package cascade

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// action_log_settlement_test.go — LLM-105: the `paid` durable mirror now records the
// FULL settlement terms (pay_items, ledger_id, consume_now) so the audit trail can
// tell a paid sale from a barter from a zero-value give-away. This marshals the
// emitted payload and re-parses it with the same json contract the pg settlements
// read (fillSettlementPayload) relies on, so a key-name drift between writer and
// reader is caught here.
func TestHandlePayResolvedActionLog_DurablePayloadRecordsSettlementTerms(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	rec := &recordingActionLogSink{}
	invokeOnWorld(t, w, func(world *sim.World) { world.SetActionLogSink(rec) })

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		// barter eat-here: 0 coins, 1 skillet for stew, consume-now.
		handlePayResolvedActionLog(world, &sim.PayWithItemResolved{
			LedgerID: 332, BuyerID: "hannah", SellerID: "bob", ItemKind: "stew", QtyPerConsumer: 1,
			ConsumeNow: true, Amount: 0, PayItems: []sim.ItemKindQty{{Kind: "skillet", Qty: 1}},
			TerminalState: sim.PayTerminalStateAccepted, At: at,
		})
		// free give-away: 0 coins, no goods — the free-food signal.
		handlePayResolvedActionLog(world, &sim.PayWithItemResolved{
			LedgerID: 331, BuyerID: "hannah", SellerID: "bob", ItemKind: "stew", QtyPerConsumer: 1,
			ConsumeNow: true, Amount: 0, TerminalState: sim.PayTerminalStateAccepted, At: at,
		})
	})

	rows := rec.snapshot()
	if len(rows) != 2 {
		t.Fatalf("recorded %d durable rows, want 2", len(rows))
	}

	type payItem struct {
		Item string `json:"item"`
		Qty  int    `json:"qty"`
	}
	type parsed struct {
		Recipient  string    `json:"recipient"`
		Amount     int       `json:"amount"`
		For        string    `json:"for"`
		ConsumeNow bool      `json:"consume_now"`
		LedgerID   uint64    `json:"ledger_id"`
		PayItems   []payItem `json:"pay_items"`
	}
	decode := func(p map[string]any) parsed {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		var out parsed
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		return out
	}

	// barter — goods recorded, amount 0, ledger + consume_now present, narration
	// keys (recipient/for) preserved.
	b := decode(rows[0].Payload)
	if b.Amount != 0 || b.LedgerID != 332 || !b.ConsumeNow {
		t.Errorf("barter coins/ledger/consume_now = %d/%d/%v, want 0/332/true", b.Amount, b.LedgerID, b.ConsumeNow)
	}
	if len(b.PayItems) != 1 || b.PayItems[0].Item != "skillet" || b.PayItems[0].Qty != 1 {
		t.Errorf("barter pay_items = %+v, want [{skillet 1}]", b.PayItems)
	}
	if b.Recipient != "Bob" || b.For != "stew" {
		t.Errorf("barter recipient/for = %q/%q, want Bob/stew", b.Recipient, b.For)
	}

	// give-away — no goods leg → empty pay_items + amount 0 (the unambiguous
	// free-food signal: amount 0 AND no pay_items).
	g := decode(rows[1].Payload)
	if g.Amount != 0 || g.LedgerID != 331 || len(g.PayItems) != 0 {
		t.Errorf("give-away coins/ledger/pay_items = %d/%d/%v, want 0/331/none", g.Amount, g.LedgerID, g.PayItems)
	}
}
