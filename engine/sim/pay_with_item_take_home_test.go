package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_with_item_take_home_test.go — LLM-744: a good that eases no need (a
// skirt) is never eaten on the spot. Live trigger: wendy paid Josiah 8 coins
// for a frilly skirt with the Pay box's default "have it here", and the
// eat-on-the-spot branch took the coins and burned the skirt.

func addSkirtKind(t *testing.T, w *sim.World) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds["frilly_skirt"] = &sim.ItemKindDef{
			Name: "frilly_skirt", DisplayLabel: "Frilly skirt", Category: "clothing",
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("addSkirtKind: %v", err)
	}
}

func buildSkirtWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "wendy", displayName: "Wendy", kind: sim.KindPC, huddleID: "h1", coins: 30},
		{id: "josiah", displayName: "Josiah", kind: sim.KindNPCShared, huddleID: "h1",
			inventory: map[sim.ItemKind]int{"frilly_skirt": 1, "bread": 3}},
	})
	addSkirtKind(t, w)
	return w, stop
}

// A quote take asking eat-here hands the skirt over instead.
func TestPayWithItem_TakeHomeOnly_QuoteTakeClampsConsumeNow(t *testing.T) {
	w, stop := buildSkirtWorld(t)
	defer stop()
	at := time.Now().UTC()
	seedQuote(t, w, sim.SceneQuote{
		ID: 7, SceneID: "sc1", SellerID: "josiah",
		Lines: []sim.QuoteLine{{ItemKind: "frilly_skirt", Qty: 1}}, Amount: 8,
		ConsumeNow: true, State: sim.SceneQuoteStateActive, ExpiresAt: at.Add(10 * time.Minute),
	})
	events := capturePayWithItemEvents(t, w)

	res, err := w.Send(sim.PayWithItem("wendy", "Josiah", "frilly_skirt", 1, 8, true, nil, nil, 7, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem: %v", err)
	}
	r := res.(sim.PayWithItemResult)
	if entry := readPayLedger(t, w)[r.LedgerID]; entry.ConsumeNow {
		t.Error("ledger ConsumeNow = true, want false (take-home-only clamp)")
	}
	if len(events.Consumed) != 0 {
		t.Errorf("ItemConsumed on a skirt: %+v", events.Consumed)
	}
	buyer := readBundleActorState(t, w, "wendy")
	seller := readBundleActorState(t, w, "josiah")
	if buyer.inv["frilly_skirt"] != 1 || seller.inv["frilly_skirt"] != 0 {
		t.Errorf("skirt: buyer=%d seller=%d, want 1 / 0", buyer.inv["frilly_skirt"], seller.inv["frilly_skirt"])
	}
	if buyer.coins != 22 {
		t.Errorf("buyer coins = %d, want 22", buyer.coins)
	}
}

// A slow-path offer (no quote) is clamped at intake too, so the pending entry
// already carries the take-home shape the seller accepts.
func TestPayWithItem_TakeHomeOnly_OfferClampsConsumeNow(t *testing.T) {
	w, stop := buildSkirtWorld(t)
	defer stop()
	at := time.Now().UTC()

	res, err := w.Send(sim.PayWithItem("wendy", "Josiah", "frilly_skirt", 1, 8, true, nil, nil, 0, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem: %v", err)
	}
	r := res.(sim.PayWithItemResult)
	if entry := readPayLedger(t, w)[r.LedgerID]; entry.ConsumeNow {
		t.Error("pending ledger ConsumeNow = true, want false (take-home-only clamp)")
	}
}

// The settle backstop: an eat-here bundle adopts the quote's disposition for
// every line, so the skirt line reaches settle with ConsumeNow set. The bread
// is eaten; the skirt goes into the buyer's pack.
func TestPayWithItem_TakeHomeOnly_EatHereBundlePocketsTheSkirt(t *testing.T) {
	w, stop := buildSkirtWorld(t)
	defer stop()
	at := time.Now().UTC()
	seedQuote(t, w, sim.SceneQuote{
		ID: 9, SceneID: "sc1", SellerID: "josiah",
		Lines:  []sim.QuoteLine{{ItemKind: "bread", Qty: 1}, {ItemKind: "frilly_skirt", Qty: 1}},
		Amount: 10, ConsumeNow: true, State: sim.SceneQuoteStateActive, ExpiresAt: at.Add(10 * time.Minute),
	})
	events := capturePayWithItemEvents(t, w)

	res, err := w.Send(sim.PayWithItem("wendy", "Josiah", "bread", 1, 10, true, nil, nil, 9, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem (bundle): %v", err)
	}
	r := res.(sim.PayWithItemResult)
	if r.BuyerAte != 1 {
		t.Errorf("BuyerAte = %d, want 1 (the bread only)", r.BuyerAte)
	}
	if len(events.Consumed) != 1 || events.Consumed[0].Kind != "bread" {
		t.Errorf("ItemConsumed = %+v, want one bread event", events.Consumed)
	}
	buyer := readBundleActorState(t, w, "wendy")
	if buyer.inv["frilly_skirt"] != 1 {
		t.Errorf("buyer skirt = %d, want 1 (pocketed, not eaten)", buyer.inv["frilly_skirt"])
	}
}

// The same bundle with the skirt as the representative line the buyer echoes:
// the intake clamp sees "frilly_skirt", but a bundle take adopts the quote's
// own disposition, so the bread is still eaten (code_review round 1).
func TestPayWithItem_TakeHomeOnly_SkirtFirstBundleStillEatsTheBread(t *testing.T) {
	w, stop := buildSkirtWorld(t)
	defer stop()
	at := time.Now().UTC()
	seedQuote(t, w, sim.SceneQuote{
		ID: 9, SceneID: "sc1", SellerID: "josiah",
		Lines:  []sim.QuoteLine{{ItemKind: "frilly_skirt", Qty: 1}, {ItemKind: "bread", Qty: 1}},
		Amount: 10, ConsumeNow: true, State: sim.SceneQuoteStateActive, ExpiresAt: at.Add(10 * time.Minute),
	})
	events := capturePayWithItemEvents(t, w)

	res, err := w.Send(sim.PayWithItem("wendy", "Josiah", "frilly_skirt", 1, 10, true, nil, nil, 9, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem (bundle): %v", err)
	}
	r := res.(sim.PayWithItemResult)
	if entry := readPayLedger(t, w)[r.LedgerID]; !entry.ConsumeNow {
		t.Error("bundle ledger ConsumeNow = false, want the quote's true")
	}
	if r.BuyerAte != 1 || len(events.Consumed) != 1 || events.Consumed[0].Kind != "bread" {
		t.Errorf("BuyerAte = %d, ItemConsumed = %+v, want the bread eaten", r.BuyerAte, events.Consumed)
	}
	if buyer := readBundleActorState(t, w, "wendy"); buyer.inv["frilly_skirt"] != 1 {
		t.Errorf("buyer skirt = %d, want 1", buyer.inv["frilly_skirt"])
	}
}

// A pending eat-here offer whose kind has left the catalog never reaches the
// settle branch: the accept gate fails it unavailable and no coin or good
// moves (code_review round 1 asked whether such an entry still burns).
func TestPayWithItem_UnknownKindAtAcceptMovesNothing(t *testing.T) {
	w, stop := buildSkirtWorld(t)
	defer stop()
	at := time.Now().UTC()
	seedLedgerEntry(t, w, sim.PayLedgerEntry{
		ID: 1, BuyerID: "wendy", SellerID: "josiah",
		ItemKind: "frilly_skirt", Qty: 1, Amount: 8, ConsumeNow: true,
		State: sim.PayLedgerStatePending, ExpiresAt: at.Add(3 * time.Minute),
		SceneID: "sc1", HuddleID: "h1",
	})
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		delete(world.ItemKinds, "frilly_skirt")
		return nil, nil
	}}); err != nil {
		t.Fatalf("drop kind: %v", err)
	}
	events := capturePayWithItemEvents(t, w)
	res, err := w.Send(sim.AcceptPay("josiah", 1, at))
	if err != nil {
		t.Fatalf("AcceptPay: %v", err)
	}
	if res != sim.PayLedgerStateFailedUnavailable {
		t.Errorf("AcceptPay = %v, want %v", res, sim.PayLedgerStateFailedUnavailable)
	}
	if len(events.Consumed) != 0 {
		t.Errorf("ItemConsumed on an unknown kind: %+v", events.Consumed)
	}
	buyer := readBundleActorState(t, w, "wendy")
	seller := readBundleActorState(t, w, "josiah")
	if buyer.coins != 30 || seller.inv["frilly_skirt"] != 1 {
		t.Errorf("buyer coins = %d, seller skirt = %d, want 30 / 1 (nothing moved)", buyer.coins, seller.inv["frilly_skirt"])
	}
}
