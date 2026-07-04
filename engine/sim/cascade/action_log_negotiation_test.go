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
