package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// scene_quote_bundle_test.go — LLM-101 multi-item bundle coverage on the
// SELL side: SceneQuoteCreate over lines (multi-line mint, duplicate-kind
// merge, per-line stock check, order-independent supersede, the bundle
// service-kind reject, the whole-bundle eat-here clamp, the distinct-kind
// cap, and the empty-lines reject). Take-side bundle coverage lives in
// pay_with_item_bundle_test.go.

func TestSceneQuoteCreate_Bundle_HappyPath(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 3, "bread": 3}},
		{id: "bea", displayName: "Bea", kind: sim.KindNPCStateful, huddleID: "h1"},
	})
	defer stop()

	captured := captureSceneQuoteCreated(t, w)
	at := time.Now().UTC()
	res, err := w.Send(sim.SceneQuoteCreate("aldous",
		[]sim.QuoteLineInput{{ItemName: "ale", Qty: 1}, {ItemName: "bread", Qty: 2}},
		8, true, "", nil, at))
	if err != nil {
		t.Fatalf("SceneQuoteCreate (bundle): %v", err)
	}
	result := res.(sim.SceneQuoteCreateResult)

	// One quote, two lines, one event.
	if len(*captured) != 1 {
		t.Fatalf("SceneQuoteCreated events = %d, want 1", len(*captured))
	}
	evt := (*captured)[0]
	if len(evt.Lines) != 2 {
		t.Fatalf("event lines = %d, want 2", len(evt.Lines))
	}
	view := readLiveQuotes(t, w)
	q := view.Quotes[result.QuoteID]
	if len(q.Lines) != 2 {
		t.Fatalf("stored quote lines = %d, want 2", len(q.Lines))
	}
	if q.Lines[0].ItemKind != "ale" || q.Lines[0].Qty != 1 || q.Lines[1].ItemKind != "bread" || q.Lines[1].Qty != 2 {
		t.Errorf("lines = %+v, want [{ale 1} {bread 2}]", q.Lines)
	}
	if q.Amount != 8 {
		t.Errorf("amount = %d, want 8 (one bundle total)", q.Amount)
	}
}

func TestSceneQuoteCreate_Bundle_MergesDuplicateKinds(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 5}},
	})
	defer stop()

	at := time.Now().UTC()
	res, err := w.Send(sim.SceneQuoteCreate("aldous",
		[]sim.QuoteLineInput{{ItemName: "ale", Qty: 2}, {ItemName: "ale", Qty: 1}},
		6, true, "", nil, at))
	if err != nil {
		t.Fatalf("SceneQuoteCreate (dup kinds): %v", err)
	}
	result := res.(sim.SceneQuoteCreateResult)
	q := readLiveQuotes(t, w).Quotes[result.QuoteID]
	if len(q.Lines) != 1 {
		t.Fatalf("merged lines = %d, want 1 (duplicate ale merged)", len(q.Lines))
	}
	if q.Lines[0].ItemKind != "ale" || q.Lines[0].Qty != 3 {
		t.Errorf("merged line = %+v, want {ale 3}", q.Lines[0])
	}
}

func TestSceneQuoteCreate_Bundle_PerLineStockReject(t *testing.T) {
	// Seller has ale but no bread — the whole bundle must reject (atomic).
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 5}},
	})
	defer stop()

	at := time.Now().UTC()
	_, err := w.Send(sim.SceneQuoteCreate("aldous",
		[]sim.QuoteLineInput{{ItemName: "ale", Qty: 1}, {ItemName: "bread", Qty: 1}},
		8, true, "", nil, at))
	if err == nil {
		t.Fatal("expected reject when one bundle line is out of stock")
	}
	if !strings.Contains(err.Error(), "bread") {
		t.Errorf("error = %q, want it to name the out-of-stock line (bread)", err)
	}
	if n := len(readLiveQuotes(t, w).Quotes); n != 0 {
		t.Errorf("World.Quotes = %d, want 0 (nothing minted on reject)", n)
	}
}

func TestSceneQuoteCreate_Bundle_SupersedeOrderIndependent(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 5, "bread": 5}},
	})
	defer stop()

	at := time.Now().UTC()
	res1, err := w.Send(sim.SceneQuoteCreate("aldous",
		[]sim.QuoteLineInput{{ItemName: "ale", Qty: 1}, {ItemName: "bread", Qty: 1}},
		8, true, "", nil, at))
	if err != nil {
		t.Fatalf("first bundle: %v", err)
	}
	first := res1.(sim.SceneQuoteCreateResult).QuoteID

	// Re-post the same bundle with lines in the OPPOSITE order and a new price.
	// The non-Amount key is order-independent, so this supersedes the first.
	res2, err := w.Send(sim.SceneQuoteCreate("aldous",
		[]sim.QuoteLineInput{{ItemName: "bread", Qty: 1}, {ItemName: "ale", Qty: 1}},
		10, true, "", nil, at))
	if err != nil {
		t.Fatalf("second bundle: %v", err)
	}
	second := res2.(sim.SceneQuoteCreateResult).QuoteID

	view := readLiveQuotes(t, w)
	if view.Quotes[first].State != sim.SceneQuoteStateSuperseded {
		t.Errorf("first quote state = %q, want superseded", view.Quotes[first].State)
	}
	if view.Quotes[second].State != sim.SceneQuoteStateActive {
		t.Errorf("second quote state = %q, want active", view.Quotes[second].State)
	}
	if ids := view.SceneIdx["sc1"]; len(ids) != 1 || ids[0] != second {
		t.Errorf("scene index = %v, want only the new bundle [%d]", ids, second)
	}
}

func TestSceneQuoteCreate_Bundle_RejectsServiceKind(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 5}},
	})
	defer stop()

	// Add a service kind to the catalog, then try to bundle it with ale.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.ItemKinds["room"] = &sim.ItemKindDef{
			Name:         "room",
			DisplayLabel: "Room",
			Capabilities: []string{"service", "lodging"},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed service kind: %v", err)
	}

	at := time.Now().UTC()
	_, err := w.Send(sim.SceneQuoteCreate("aldous",
		[]sim.QuoteLineInput{{ItemName: "ale", Qty: 1}, {ItemName: "room", Qty: 1}},
		8, true, "", nil, at))
	if err == nil {
		t.Fatal("expected reject for a service kind inside a bundle")
	}
	if !strings.Contains(err.Error(), "room") {
		t.Errorf("error = %q, want it to name the service line (room)", err)
	}
}

func TestSceneQuoteCreate_Bundle_EatHereClampWholeBundle(t *testing.T) {
	// bread is portable, stew is eat-here-only. A take-home (consume_now=false)
	// bundle holding stew clamps the WHOLE bundle to eat-here.
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"bread": 3, "stew": 3}},
	})
	defer stop()

	at := time.Now().UTC()
	res, err := w.Send(sim.SceneQuoteCreate("aldous",
		[]sim.QuoteLineInput{{ItemName: "bread", Qty: 1}, {ItemName: "stew", Qty: 1}},
		8, false, "", nil, at))
	if err != nil {
		t.Fatalf("SceneQuoteCreate (clamp): %v", err)
	}
	result := res.(sim.SceneQuoteCreateResult)
	if !result.EatHereClamped {
		t.Error("EatHereClamped = false, want true (stew is non-portable)")
	}
	q := readLiveQuotes(t, w).Quotes[result.QuoteID]
	if !q.ConsumeNow {
		t.Error("stored quote ConsumeNow = false, want true (clamped)")
	}
}

func TestSceneQuoteCreate_Bundle_TooManyDistinctKinds(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 5}},
	})
	defer stop()

	// 9 distinct kinds exceeds the 8-line cap (unknown names mint at qty 0;
	// the cap is enforced during resolution, before the stock gate).
	lines := make([]sim.QuoteLineInput, 0, 9)
	for _, name := range []string{"k1", "k2", "k3", "k4", "k5", "k6", "k7", "k8", "k9"} {
		lines = append(lines, sim.QuoteLineInput{ItemName: name, Qty: 1})
	}
	at := time.Now().UTC()
	_, err := w.Send(sim.SceneQuoteCreate("aldous", lines, 9, true, "", nil, at))
	if err == nil {
		t.Fatal("expected reject for >8 distinct item kinds")
	}
	if !strings.Contains(err.Error(), "too many item kinds") {
		t.Errorf("error = %q, want the distinct-kind cap message", err)
	}
}

func TestSceneQuoteCreate_EmptyLines_Reject(t *testing.T) {
	w, stop := buildQuoteTestWorld(t, "h1", "sc1", []quoteTestActor{
		{id: "aldous", displayName: "Aldous", kind: sim.KindNPCStateful, huddleID: "h1", inventory: map[sim.ItemKind]int{"ale": 5}},
	})
	defer stop()

	at := time.Now().UTC()
	if _, err := w.Send(sim.SceneQuoteCreate("aldous", nil, 4, true, "", nil, at)); err == nil {
		t.Fatal("expected reject for a quote with no lines")
	}
}
