package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_with_item_bundle_test.go — LLM-101 multi-item bundle coverage on the
// TAKE side: a buyer takes a multi-line scene quote WHOLE via its quote_id.
// Asserts per-line delivery (take-home + eat-here), the no-durable-Order
// posture, the per-line stock + amount-floor gates, that the NPC bare-offer
// auto-match skips bundles, and that a single-line quote still exact-matches.

// pwiBundleState is the slice of an actor's state these tests assert on.
type pwiBundleState struct {
	coins int
	inv   map[sim.ItemKind]int
}

func readBundleActorState(t *testing.T, w *sim.World, id sim.ActorID) pwiBundleState {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		st := pwiBundleState{inv: map[sim.ItemKind]int{}}
		if a := world.Actors[id]; a != nil {
			st.coins = a.Coins
			for k, v := range a.Inventory {
				st.inv[k] = v
			}
		}
		return st, nil
	}})
	if err != nil {
		t.Fatalf("readBundleActorState %q: %v", id, err)
	}
	return res.(pwiBundleState)
}

func bundleOrderCount(t *testing.T, w *sim.World) int {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return len(world.Orders), nil
	}})
	if err != nil {
		t.Fatalf("bundleOrderCount: %v", err)
	}
	return res.(int)
}

// addBundleCatalogKinds registers portable food kinds for bundle tests; the
// mem seed only carries one portable kind (bread).
func addBundleCatalogKinds(t *testing.T, w *sim.World, names ...sim.ItemKind) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		for _, n := range names {
			world.ItemKinds[n] = &sim.ItemKindDef{
				Name:         n,
				DisplayLabel: string(n),
				Category:     sim.ItemCategoryFood,
				Capabilities: []string{"portable"},
				Satisfies:    []sim.ItemSatisfaction{{Attribute: "hunger", Immediate: 2}},
			}
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("addBundleCatalogKinds: %v", err)
	}
}

func TestPayWithItem_Bundle_TakeHome_GrantsEachLine(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "jeff", displayName: "Jefferey", kind: sim.KindPC, huddleID: "h1", coins: 50},
		{id: "pru", displayName: "Prudence", kind: sim.KindNPCShared, huddleID: "h1",
			inventory: map[sim.ItemKind]int{"blueberries": 5, "raspberries": 5}},
	})
	defer stop()
	addBundleCatalogKinds(t, w, "blueberries", "raspberries")

	at := time.Now().UTC()
	seedQuote(t, w, sim.SceneQuote{
		ID:        7,
		SceneID:   "sc1",
		SellerID:  "pru",
		Lines:     []sim.QuoteLine{{ItemKind: "blueberries", Qty: 2}, {ItemKind: "raspberries", Qty: 2}},
		Amount:    8,
		State:     sim.SceneQuoteStateActive,
		ExpiresAt: at.Add(10 * time.Minute),
	})

	events := capturePayWithItemEvents(t, w)
	// The buyer echoes a representative line (blueberries, 2) + the bundle total;
	// the engine grants the WHOLE bundle off quote_id 7.
	res, err := w.Send(sim.PayWithItem("jeff", "Prudence", "blueberries", 2, 8, false, nil, nil, 7, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem (bundle take): %v", err)
	}
	out := res.(sim.PayWithItemResult)
	if !out.FastPath || out.State != sim.PayLedgerStateAccepted {
		t.Fatalf("result = %+v, want fast-path accepted", out)
	}
	if !out.TookHome {
		t.Error("TookHome = false, want true for a take-home bundle")
	}
	if len(out.Lines) != 2 {
		t.Errorf("result Lines = %d, want 2", len(out.Lines))
	}

	buyer := readBundleActorState(t, w, "jeff")
	seller := readBundleActorState(t, w, "pru")
	if buyer.inv["blueberries"] != 2 || buyer.inv["raspberries"] != 2 {
		t.Errorf("buyer inventory = %v, want blueberries:2 raspberries:2", buyer.inv)
	}
	if seller.inv["blueberries"] != 3 || seller.inv["raspberries"] != 3 {
		t.Errorf("seller inventory = %v, want blueberries:3 raspberries:3", seller.inv)
	}
	if buyer.coins != 42 || seller.coins != 8 {
		t.Errorf("coins: buyer=%d seller=%d, want 42 / 8", buyer.coins, seller.coins)
	}
	// No durable Order for a bundle take (LLM-101 design decision).
	if n := bundleOrderCount(t, w); n != 0 {
		t.Errorf("Orders = %d, want 0 (a bundle take mints no Order)", n)
	}
	// The resolved event carries the bundle for the action log / audit.
	if len(events.Resolved) != 1 || len(events.Resolved[0].Lines) != 2 {
		t.Errorf("resolved event lines = %v, want one event with 2 lines", events.Resolved)
	}
	// The accepted ledger entry carries the lines; no other entry minted.
	ledger := readPayLedger(t, w)
	if len(ledger) != 1 {
		t.Fatalf("ledger entries = %d, want 1", len(ledger))
	}
	for _, e := range ledger {
		if len(e.Lines) != 2 {
			t.Errorf("ledger entry Lines = %d, want 2", len(e.Lines))
		}
	}
}

func TestPayWithItem_Bundle_EatHere_ConsumesEachLine(t *testing.T) {
	// ale + bread, eat-here (consume_now true). Each line is consumed in place
	// (floor of one unit per line), no Order minted.
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "jeff", displayName: "Jefferey", kind: sim.KindPC, huddleID: "h1", coins: 50},
		{id: "pru", displayName: "Prudence", kind: sim.KindNPCShared, huddleID: "h1",
			inventory: map[sim.ItemKind]int{"ale": 3, "bread": 3}},
	})
	defer stop()

	at := time.Now().UTC()
	seedQuote(t, w, sim.SceneQuote{
		ID:         9,
		SceneID:    "sc1",
		SellerID:   "pru",
		Lines:      []sim.QuoteLine{{ItemKind: "ale", Qty: 1}, {ItemKind: "bread", Qty: 1}},
		Amount:     6,
		ConsumeNow: true,
		State:      sim.SceneQuoteStateActive,
		ExpiresAt:  at.Add(10 * time.Minute),
	})

	res, err := w.Send(sim.PayWithItem("jeff", "Prudence", "ale", 1, 6, true, nil, nil, 9, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem (eat-here bundle): %v", err)
	}
	out := res.(sim.PayWithItemResult)
	if !out.FastPath || out.TookHome {
		t.Fatalf("result = %+v, want fast-path eat-here (not take-home)", out)
	}
	if out.BuyerAte != 2 {
		t.Errorf("BuyerAte = %d, want 2 (one ale + one bread)", out.BuyerAte)
	}
	seller := readBundleActorState(t, w, "pru")
	if seller.inv["ale"] != 2 || seller.inv["bread"] != 2 {
		t.Errorf("seller inventory = %v, want ale:2 bread:2 (one of each consumed)", seller.inv)
	}
	if n := bundleOrderCount(t, w); n != 0 {
		t.Errorf("Orders = %d, want 0", n)
	}
}

func TestPayWithItem_Bundle_PerLineStockReject(t *testing.T) {
	// Seller can cover blueberries but not raspberries — the take rejects whole.
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "jeff", displayName: "Jefferey", kind: sim.KindPC, huddleID: "h1", coins: 50},
		{id: "pru", displayName: "Prudence", kind: sim.KindNPCShared, huddleID: "h1",
			inventory: map[sim.ItemKind]int{"blueberries": 5, "raspberries": 1}},
	})
	defer stop()
	addBundleCatalogKinds(t, w, "blueberries", "raspberries")

	at := time.Now().UTC()
	seedQuote(t, w, sim.SceneQuote{
		ID:        7,
		SceneID:   "sc1",
		SellerID:  "pru",
		Lines:     []sim.QuoteLine{{ItemKind: "blueberries", Qty: 2}, {ItemKind: "raspberries", Qty: 2}},
		Amount:    8,
		State:     sim.SceneQuoteStateActive,
		ExpiresAt: at.Add(10 * time.Minute),
	})

	_, err := w.Send(sim.PayWithItem("jeff", "Prudence", "blueberries", 2, 8, false, nil, nil, 7, 0, "", at))
	if err == nil {
		t.Fatal("expected reject when one bundle line is out of stock")
	}
	// Strict reject: no ledger entry minted, no goods moved.
	if n := len(readPayLedger(t, w)); n != 0 {
		t.Errorf("ledger entries = %d, want 0 (strict reject)", n)
	}
	buyer := readBundleActorState(t, w, "jeff")
	if buyer.coins != 50 || len(buyer.inv) != 0 {
		t.Errorf("buyer changed on a rejected take: coins=%d inv=%v", buyer.coins, buyer.inv)
	}
}

func TestPayWithItem_Bundle_AmountFloor(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "jeff", displayName: "Jefferey", kind: sim.KindPC, huddleID: "h1", coins: 50},
		{id: "pru", displayName: "Prudence", kind: sim.KindNPCShared, huddleID: "h1",
			inventory: map[sim.ItemKind]int{"blueberries": 5, "raspberries": 5}},
	})
	defer stop()
	addBundleCatalogKinds(t, w, "blueberries", "raspberries")

	at := time.Now().UTC()
	seedQuote(t, w, sim.SceneQuote{
		ID:        7,
		SceneID:   "sc1",
		SellerID:  "pru",
		Lines:     []sim.QuoteLine{{ItemKind: "blueberries", Qty: 2}, {ItemKind: "raspberries", Qty: 2}},
		Amount:    8,
		State:     sim.SceneQuoteStateActive,
		ExpiresAt: at.Add(10 * time.Minute),
	})

	// Offer below the bundle total floor.
	if _, err := w.Send(sim.PayWithItem("jeff", "Prudence", "blueberries", 2, 5, false, nil, nil, 7, 0, "", at)); err == nil {
		t.Fatal("expected reject when offering below the bundle total")
	}
}

func TestPayWithItem_AutoMatch_SkipsBundle(t *testing.T) {
	// A bare single-item offer cannot auto-match a multi-line bundle — it mints
	// a normal pending offer instead of taking the bundle.
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "jeff", displayName: "Jefferey", kind: sim.KindNPCShared, huddleID: "h1", coins: 50},
		{id: "pru", displayName: "Prudence", kind: sim.KindNPCShared, huddleID: "h1",
			inventory: map[sim.ItemKind]int{"blueberries": 5, "raspberries": 5}},
	})
	defer stop()
	addBundleCatalogKinds(t, w, "blueberries", "raspberries")

	at := time.Now().UTC()
	seedQuote(t, w, sim.SceneQuote{
		ID:        7,
		SceneID:   "sc1",
		SellerID:  "pru",
		Lines:     []sim.QuoteLine{{ItemKind: "blueberries", Qty: 2}, {ItemKind: "raspberries", Qty: 2}},
		Amount:    8,
		State:     sim.SceneQuoteStateActive,
		ExpiresAt: at.Add(10 * time.Minute),
	})

	// Bare offer (quote_id 0) for just blueberries.
	res, err := w.Send(sim.PayWithItem("jeff", "Prudence", "blueberries", 2, 8, false, nil, nil, 0, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem (bare offer): %v", err)
	}
	out := res.(sim.PayWithItemResult)
	if out.FastPath || out.State != sim.PayLedgerStatePending {
		t.Errorf("result = %+v, want a pending offer (auto-match must skip the bundle)", out)
	}
}

func TestPayWithItem_SingleLineQuote_StillExactMatch(t *testing.T) {
	// Regression: a single-line quote still requires exact term match and mints
	// an entry with no Lines (the single-item scalar path is unchanged).
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "jeff", displayName: "Jefferey", kind: sim.KindPC, huddleID: "h1", coins: 50},
		{id: "pru", displayName: "Prudence", kind: sim.KindNPCShared, huddleID: "h1",
			inventory: map[sim.ItemKind]int{"bread": 5}},
	})
	defer stop()

	at := time.Now().UTC()
	seedQuote(t, w, sim.SceneQuote{
		ID:        7,
		SceneID:   "sc1",
		SellerID:  "pru",
		Lines:     []sim.QuoteLine{{ItemKind: "bread", Qty: 2}},
		Amount:    4,
		State:     sim.SceneQuoteStateActive,
		ExpiresAt: at.Add(10 * time.Minute),
	})

	// Wrong qty must be rejected (exact match still applies to single-line).
	if _, err := w.Send(sim.PayWithItem("jeff", "Prudence", "bread", 3, 4, false, nil, nil, 7, 0, "", at)); err == nil {
		t.Fatal("expected reject for a single-line quote taken with the wrong qty")
	}
	// Correct terms settle, with no bundle Lines on the entry.
	res, err := w.Send(sim.PayWithItem("jeff", "Prudence", "bread", 2, 4, false, nil, nil, 7, 0, "", at))
	if err != nil {
		t.Fatalf("PayWithItem (single-line take): %v", err)
	}
	out := res.(sim.PayWithItemResult)
	if !out.FastPath || len(out.Lines) != 0 {
		t.Errorf("result = %+v, want fast-path with no bundle Lines", out)
	}
}
