package cascade

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// action_log_negotiation_test.go — LLM-283: the offered / declined / countered
// subscribers make a pay-ledger haggle visible in the Village feed. Each asserts
// the in-memory ring row (what the feed renders) AND the durable mirror payload
// (the ledger_id-keyed barter-tracing side benefit), plus the terminal-state and
// gift filters that keep the rows scoped to genuine purchase negotiation.

type negotiationPayload struct {
	LedgerID       uint64 `json:"ledger_id"`
	Item           string `json:"item"`
	Qty            int    `json:"qty"`
	Amount         int    `json:"amount"`
	OriginalAmount int    `json:"original_amount"`
	Buyer          string `json:"buyer"`
	Seller         string `json:"seller"`
	PayItems       []struct {
		Item string `json:"item"`
		Qty  int    `json:"qty"`
	} `json:"pay_items"`
}

func decodeNegotiationPayload(t *testing.T, p map[string]any) negotiationPayload {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var out negotiationPayload
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return out
}

// TestHandleOfferedActionLog_RingAndDurable: a buyer's pending offer logs a
// buyer-side ring row (seller counterparty, coin amount, item summary) and a
// durable row carrying ledger_id + terms. Qty scales across group consumers.
func TestHandleOfferedActionLog_RingAndDurable(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	rec := &recordingActionLogSink{}
	invokeOnWorld(t, w, func(world *sim.World) { world.SetActionLogSink(rec) })

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		handleOfferedActionLog(world, &sim.PayOfferReceived{
			LedgerID: 501, BuyerID: "hannah", SellerID: "bob", ItemKind: "ale",
			QtyPerConsumer: 2, ConsumerIDs: []sim.ActorID{"hannah", "bob"},
			Amount: 8, HuddleID: "h1", At: at,
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("ring rows = %d, want 1", len(got))
	}
	e := got[0]
	if e.ActorID != "hannah" || e.ActionType != sim.ActionTypeOffered {
		t.Errorf("actor/type = %q/%q, want hannah/offered", e.ActorID, e.ActionType)
	}
	// qty = QtyPerConsumer(2) * consumers(2) = 4.
	if e.CounterpartyName != "Bob" || e.Amount != 8 || e.Text != "4x ale" {
		t.Errorf("counterparty/amount/text = %q/%d/%q, want Bob/8/4x ale", e.CounterpartyName, e.Amount, e.Text)
	}
	if e.HuddleID != "h1" {
		t.Errorf("huddle = %q, want h1", e.HuddleID)
	}

	rows := rec.snapshot()
	if len(rows) != 1 {
		t.Fatalf("durable rows = %d, want 1", len(rows))
	}
	if rows[0].ActionType != sim.ActionTypeOffered || rows[0].SpeakerName != "Hannah" {
		t.Errorf("durable type/speaker = %q/%q, want offered/Hannah", rows[0].ActionType, rows[0].SpeakerName)
	}
	p := decodeNegotiationPayload(t, rows[0].Payload)
	if p.LedgerID != 501 || p.Item != "ale" || p.Qty != 4 || p.Amount != 8 || p.Seller != "Bob" {
		t.Errorf("payload = %+v, want ledger 501 / ale / qty 4 / amount 8 / seller Bob", p)
	}
}

// TestHandleOfferedActionLog_BarterCarriesPayItems: a goods-only barter offer
// (Amount 0, give-goods in PayItems) carries the give side onto the ring entry —
// so the feed renders "offers X <goods> for Y" instead of dropping the give side
// (LLM-431) — and records it in the durable pay_items payload for audit parity.
func TestHandleOfferedActionLog_BarterCarriesPayItems(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	rec := &recordingActionLogSink{}
	invokeOnWorld(t, w, func(world *sim.World) { world.SetActionLogSink(rec) })

	at := time.Now().UTC()
	give := []sim.ItemKindQty{{Kind: "stew", Qty: 1}}
	invokeOnWorld(t, w, func(world *sim.World) {
		handleOfferedActionLog(world, &sim.PayOfferReceived{
			LedgerID: 511, BuyerID: "hannah", SellerID: "bob", ItemKind: "firewood",
			QtyPerConsumer: 3, Amount: 0, PayItems: give, HuddleID: "h1", At: at,
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("ring rows = %d, want 1", len(got))
	}
	e := got[0]
	// Amount 0 (goods-only barter); the wanted item stays in Text, the give-goods
	// ride in PayItems for the renderer.
	if e.Amount != 0 || e.Text != "3x firewood" {
		t.Errorf("amount/text = %d/%q, want 0/3x firewood", e.Amount, e.Text)
	}
	if len(e.PayItems) != 1 || e.PayItems[0].Kind != "stew" || e.PayItems[0].Qty != 1 {
		t.Errorf("ring PayItems = %+v, want [{stew 1}]", e.PayItems)
	}

	// Defensive-copy contract: mutating the caller's give slice after the handler
	// returns must not reach the stored entry — the handler snapshots PayItems
	// rather than aliasing the event's backing array.
	give[0].Qty = 99
	after := readActionLog(t, w)
	if len(after) != 1 || len(after[0].PayItems) != 1 || after[0].PayItems[0].Qty != 1 {
		t.Errorf("ring PayItems mutated through caller slice = %+v, want [{stew 1}]", after[0].PayItems)
	}

	rows := rec.snapshot()
	if len(rows) != 1 {
		t.Fatalf("durable rows = %d, want 1", len(rows))
	}
	p := decodeNegotiationPayload(t, rows[0].Payload)
	if p.Amount != 0 || p.Item != "firewood" || p.Qty != 3 || p.Seller != "Bob" {
		t.Errorf("payload terms = amount %d / item %q / qty %d / seller %q, want 0/firewood/3/Bob", p.Amount, p.Item, p.Qty, p.Seller)
	}
	if len(p.PayItems) != 1 || p.PayItems[0].Item != "stew" || p.PayItems[0].Qty != 1 {
		t.Errorf("durable pay_items = %+v, want [{stew 1}]", p.PayItems)
	}
}

// TestHandleDeclinedActionLog_OnlyDeclinedTerminal: only the Declined terminal
// logs a row (accepted/expired/etc. are handled elsewhere or not at all), and it
// is seller-side with the buyer as counterparty.
func TestHandleDeclinedActionLog_OnlyDeclinedTerminal(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		handleDeclinedActionLog(world, &sim.PayWithItemResolved{
			LedgerID: 601, BuyerID: "hannah", SellerID: "bob", ItemKind: "milk", QtyPerConsumer: 2,
			Amount: 4, TerminalState: sim.PayTerminalStateAccepted, HuddleID: "h1", At: at,
		})
		handleDeclinedActionLog(world, &sim.PayWithItemResolved{
			LedgerID: 602, BuyerID: "hannah", SellerID: "bob", ItemKind: "milk", QtyPerConsumer: 2,
			Amount: 4, TerminalState: sim.PayTerminalStateExpired, HuddleID: "h1", At: at,
		})
		handleDeclinedActionLog(world, &sim.PayWithItemResolved{
			LedgerID: 603, BuyerID: "hannah", SellerID: "bob", ItemKind: "milk", QtyPerConsumer: 2,
			Amount: 4, TerminalState: sim.PayTerminalStateDeclined, HuddleID: "h1", At: at,
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("ring rows = %d, want 1 (declined terminal only)", len(got))
	}
	e := got[0]
	if e.ActorID != "bob" || e.ActionType != sim.ActionTypeDeclined || e.CounterpartyName != "Hannah" {
		t.Errorf("actor/type/counterparty = %q/%q/%q, want bob/declined/Hannah", e.ActorID, e.ActionType, e.CounterpartyName)
	}
}

// TestHandleCounteredActionLog_RingAndDurable: a seller's counter logs a
// seller-side ring row at the counter price, and a durable row carrying the
// parent ledger_id plus the original amount (the price MOVE, not just the new
// number).
func TestHandleCounteredActionLog_RingAndDurable(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	rec := &recordingActionLogSink{}
	invokeOnWorld(t, w, func(world *sim.World) { world.SetActionLogSink(rec) })

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		handleCounteredActionLog(world, &sim.PayCountered{
			ParentID: 701, BuyerID: "hannah", SellerID: "bob", ItemKind: "milk", QtyPerConsumer: 3,
			OriginalAmount: 3, CounterAmount: 5, HuddleID: "h1", At: at,
		})
	})

	got := readActionLog(t, w)
	if len(got) != 1 {
		t.Fatalf("ring rows = %d, want 1", len(got))
	}
	e := got[0]
	if e.ActorID != "bob" || e.ActionType != sim.ActionTypeCountered || e.CounterpartyName != "Hannah" {
		t.Errorf("actor/type/counterparty = %q/%q/%q, want bob/countered/Hannah", e.ActorID, e.ActionType, e.CounterpartyName)
	}
	if e.Amount != 5 || e.Text != "3x milk" {
		t.Errorf("amount/text = %d/%q, want 5/3x milk", e.Amount, e.Text)
	}

	rows := rec.snapshot()
	if len(rows) != 1 {
		t.Fatalf("durable rows = %d, want 1", len(rows))
	}
	p := decodeNegotiationPayload(t, rows[0].Payload)
	if p.LedgerID != 701 || p.Amount != 5 || p.OriginalAmount != 3 || p.Buyer != "Hannah" {
		t.Errorf("payload = %+v, want ledger 701 / amount 5 / original 3 / buyer Hannah", p)
	}
}

// TestHandleNegotiationActionLog_SkipsGifts: give_goods / decline_gift ride the
// same PayOfferReceived / PayWithItemResolved{Declined} events, but a one-way
// gift isn't a purchase haggle — the IsGift entry is skipped so no negotiation
// row (with its backwards buy-phrasing) is written.
func TestHandleNegotiationActionLog_SkipsGifts(t *testing.T) {
	w, stop := buildActionLogCascadeWorld(t)
	defer stop()

	at := time.Now().UTC()
	invokeOnWorld(t, w, func(world *sim.World) {
		if world.PayLedger == nil {
			world.PayLedger = map[sim.LedgerID]*sim.PayLedgerEntry{}
		}
		world.PayLedger[801] = &sim.PayLedgerEntry{ID: 801, IsGift: true}
		world.PayLedger[802] = &sim.PayLedgerEntry{ID: 802, IsGift: true}
		handleOfferedActionLog(world, &sim.PayOfferReceived{
			LedgerID: 801, BuyerID: "hannah", SellerID: "bob", ItemKind: "milk", QtyPerConsumer: 1,
			HuddleID: "h1", At: at,
		})
		handleDeclinedActionLog(world, &sim.PayWithItemResolved{
			LedgerID: 802, BuyerID: "hannah", SellerID: "bob", ItemKind: "milk", QtyPerConsumer: 1,
			TerminalState: sim.PayTerminalStateDeclined, HuddleID: "h1", At: at,
		})
	})

	got := readActionLog(t, w)
	if len(got) != 0 {
		t.Fatalf("ring rows = %d, want 0 (gifts skipped)", len(got))
	}
}
