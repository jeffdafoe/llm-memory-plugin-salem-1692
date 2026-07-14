package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// scene_quote_reconcile_test.go — LLM-409 coverage reconcile.
//
// reconcileQuoteCoverage runs on the world goroutine after every command
// (before republish), so these tests drive it through the real path: a command
// that changes the seller's coverable stock, then a read of the quote's state.
// A quote seeded via seedQuote is likewise reconciled at the end of the seed
// command, so an uncoverable seed is already shortfall by the time it returns.

// TestReconcileQuoteCoverage_CoverableLotStaysActive: a lot the seller can still
// cover is left Active — the reconcile only touches lots that fell short.
func TestReconcileQuoteCoverage_CoverableLotStaysActive(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	at := time.Now().UTC()
	res, err := w.Send(sim.SceneQuoteCreate("aldous", []sim.QuoteLineInput{{ItemName: "ale", Qty: 2}}, 4, false, "", nil, at))
	if err != nil {
		t.Fatalf("SceneQuoteCreate: %v", err)
	}
	qid := res.(sim.SceneQuoteCreateResult).QuoteID

	// A no-op command still runs the reconcile; the seller holds 3 ale ≥ 2.
	mustSend(t, w, func(*sim.World) {})

	view := readLiveQuotes(t, w)
	if q := view.Quotes[qid]; q.State != sim.SceneQuoteStateActive {
		t.Fatalf("State = %q, want active (seller still covers the lot)", q.State)
	}
	if len(view.SceneIdx["sc1"]) != 1 {
		t.Errorf("scene index = %v, want the lot still indexed", view.SceneIdx["sc1"])
	}
}

// TestReconcileQuoteCoverage_SpentBelowRemaining_FlipsShortfall: the seller
// spends a quoted good below the lot's quantity, so the reconcile flips the
// WHOLE lot (not a shrink) to terminal shortfall — ResolvedAt stamped, dropped
// from the scene index, SceneQuoteExpired{shortfall} emitted.
func TestReconcileQuoteCoverage_SpentBelowRemaining_FlipsShortfall(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 2}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	expired := captureSceneQuoteExpired(t, w)
	at := time.Now().UTC()
	res, err := w.Send(sim.SceneQuoteCreate("aldous", []sim.QuoteLineInput{{ItemName: "ale", Qty: 2}}, 4, false, "", nil, at))
	if err != nil {
		t.Fatalf("SceneQuoteCreate: %v", err)
	}
	qid := res.(sim.SceneQuoteCreateResult).QuoteID

	// Aldous spends one ale out from under his own 2-ale offer.
	mustSend(t, w, func(world *sim.World) {
		world.Actors["aldous"].Inventory["ale"] = 1
	})

	view := readLiveQuotes(t, w)
	q := view.Quotes[qid]
	if q.State != sim.SceneQuoteStateShortfall {
		t.Fatalf("State = %q, want shortfall", q.State)
	}
	if q.ResolvedAt.IsZero() {
		t.Errorf("ResolvedAt not stamped on shortfall flip")
	}
	if len(view.SceneIdx["sc1"]) != 0 {
		t.Errorf("scene index = %v, want empty after shortfall", view.SceneIdx["sc1"])
	}
	if len(*expired) != 1 || (*expired)[0].QuoteID != qid || (*expired)[0].Reason != sim.SceneQuoteExpiredReasonShortfall {
		t.Fatalf("expired events = %+v, want one shortfall flip for %d", *expired, qid)
	}
}

// TestReconcileQuoteCoverage_ReservedOrderMakesUncoverable: goods earmarked for
// a Ready order are not the seller's to re-sell, so a lot backed only by
// reserved stock is uncoverable and flips to shortfall. This is the reserved-
// aware coverage predicate (sellerCoverableStock) the reconcile shares with
// quote create and the accept fast path (LLM-409 Q3 unification).
func TestReconcileQuoteCoverage_ReservedOrderMakesUncoverable(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"stew": 2}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	at := time.Now().UTC()
	res, err := w.Send(sim.SceneQuoteCreate("aldous", []sim.QuoteLineInput{{ItemName: "stew", Qty: 2}}, 4, false, "", nil, at))
	if err != nil {
		t.Fatalf("SceneQuoteCreate: %v", err)
	}
	qid := res.(sim.SceneQuoteCreateResult).QuoteID

	// A Ready order reserves both stew for a prior buyer — Aldous holds the goods
	// but owes them, so his own standing lot is no longer coverable.
	mustSend(t, w, func(world *sim.World) {
		world.Orders[9001] = &sim.Order{
			ID: 9001, State: sim.OrderStateReady, SellerID: "aldous", BuyerID: "bea",
			Item: "stew", Qty: 2, ConsumerIDs: []sim.ActorID{"bea"},
		}
	})

	view := readLiveQuotes(t, w)
	if q := view.Quotes[qid]; q.State != sim.SceneQuoteStateShortfall {
		t.Fatalf("State = %q, want shortfall (stock reserved for a Ready order)", q.State)
	}
}

// TestReconcileQuoteCoverage_ServiceLotNeverShortfalls: a lodging/service lot
// carries no inventory (the grant is a capacity, not stock), so the reconcile
// skips it exactly as create does — a keeper with a room offer standing is never
// shortfall'd for want of "nights_stay" he never held.
func TestReconcileQuoteCoverage_ServiceLotNeverShortfalls(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "hannah", displayName: "Hannah", kind: sim.KindNPCStateful, huddleID: "h1"},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	// Register a service kind and seed a room lot directly (bypassing lodging-
	// specific create gates); Hannah holds zero nights_stay.
	mustSend(t, w, func(world *sim.World) {
		world.ItemKinds["nights_stay"] = &sim.ItemKindDef{Name: "nights_stay", Capabilities: []string{"service", "lodging"}}
	})
	at := time.Now().UTC()
	seedQuote(t, w, sim.SceneQuote{
		ID: 5, SceneID: "sc1", SellerID: "hannah", TargetBuyer: "bea",
		Lines: []sim.QuoteLine{{ItemKind: "nights_stay", Qty: 1}}, Amount: 4,
		State: sim.SceneQuoteStateActive, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
	})

	view := readLiveQuotes(t, w)
	if q := view.Quotes[5]; q.State != sim.SceneQuoteStateActive {
		t.Fatalf("State = %q, want active (service lot has no stock to fall short of)", q.State)
	}
}

// TestReconcileQuoteCoverage_BundleAnyLineShort_FlipsWholeLot: a multi-line
// bundle is honoured whole, so if the seller can't cover even one line the whole
// lot flips to shortfall (there is no coherent partial bundle to shrink to).
func TestReconcileQuoteCoverage_BundleAnyLineShort_FlipsWholeLot(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 5}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	at := time.Now().UTC()
	// Ale is well-stocked, bread is not held at all — the lot must die whole.
	seedQuote(t, w, sim.SceneQuote{
		ID: 6, SceneID: "sc1", SellerID: "aldous",
		Lines: []sim.QuoteLine{{ItemKind: "ale", Qty: 1}, {ItemKind: "bread", Qty: 1}}, Amount: 6,
		State: sim.SceneQuoteStateActive, CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute),
	})

	view := readLiveQuotes(t, w)
	if q := view.Quotes[6]; q.State != sim.SceneQuoteStateShortfall {
		t.Fatalf("State = %q, want shortfall (one bundle line uncoverable kills the whole lot)", q.State)
	}
}
