package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_offer_resolved_test.go — ZBBS-WORK-432. The buyer-side "## Offers lately
// settled" view (buildRecentlyResolvedOffersFromMe + renderRecentlyResolvedOffersFromMe).
// It closes the blind window in which the buyer re-buys a need already met
// because the offer left the pending scan (PendingOffersFromMe) but the
// PayResolvedWarrantReason event hasn't surfaced yet — it can ride a tick behind
// an in-flight deliberation (design: shared/tasks/salem-buyer-reoffer-late-acceptance-warrant).
// The build function takes NO warrant input, so the resolution is legible from
// the SNAPSHOT alone — warrant-independent by construction.
//
// Reuses offerSnap (pay_offer_pending_test.go); resolvedSnap stamps PublishedAt,
// the wall clock buildRecentlyResolvedOffersFromMe measures the recency window against.

func resolvedEntry(id sim.LedgerID, buyer, seller sim.ActorID, item sim.ItemKind, qty, amount int, state sim.PayLedgerState, consumeNow bool, resolvedAt time.Time) *sim.PayLedgerEntry {
	return &sim.PayLedgerEntry{
		ID:         id,
		BuyerID:    buyer,
		SellerID:   seller,
		ItemKind:   item,
		Qty:        qty,
		Amount:     amount,
		State:      state,
		ConsumeNow: consumeNow,
		ResolvedAt: resolvedAt,
	}
}

func resolvedSnap(now time.Time, ledger map[sim.LedgerID]*sim.PayLedgerEntry) *sim.Snapshot {
	s := offerSnap(ledger)
	s.PublishedAt = now
	return s
}

// A just-accepted consume_now offer is surfaced — this is the exact 270 case
// (Hannah's water): the buyer must see it was bought so it doesn't re-offer.
func TestBuildRecentlyResolvedOffersFromMe_AcceptedWithinWindow(t *testing.T) {
	now := time.Now().UTC()
	snap := resolvedSnap(now, map[sim.LedgerID]*sim.PayLedgerEntry{
		270: resolvedEntry(270, "prudence", "elizabeth", "water", 1, 10, sim.PayLedgerStateAccepted, true, now.Add(-30*time.Second)),
	})
	views := buildRecentlyResolvedOffersFromMe(snap, "prudence", snap.Actors["prudence"])
	if len(views) != 1 {
		t.Fatalf("views = %v, want one recently-resolved offer", views)
	}
	v := views[0]
	if v.LedgerID != 270 || v.Item != "water" || v.Qty != 1 || v.Amount != 10 || !v.Accepted || !v.ConsumeNow {
		t.Errorf("view = %+v, want ledger 270 / water / qty 1 / amount 10 / accepted / consume_now", v)
	}
}

// An offer resolved before the recency window is dropped — the view is a brief
// bridge, not a purchase log (terminal entries linger up to 1h in the ledger).
func TestBuildRecentlyResolvedOffersFromMe_StaleExcluded(t *testing.T) {
	now := time.Now().UTC()
	snap := resolvedSnap(now, map[sim.LedgerID]*sim.PayLedgerEntry{
		270: resolvedEntry(270, "prudence", "elizabeth", "water", 1, 10, sim.PayLedgerStateAccepted, true, now.Add(-30*time.Minute)),
	})
	if got := buildRecentlyResolvedOffersFromMe(snap, "prudence", snap.Actors["prudence"]); got != nil {
		t.Errorf("resolved 30m ago (> window) must be excluded; got %v", got)
	}
}

// Pending offers belong to the pending view; countered offers are an active
// response flow (a fresh pending entry the buyer answers) — neither belongs here.
func TestBuildRecentlyResolvedOffersFromMe_PendingAndCounteredExcluded(t *testing.T) {
	now := time.Now().UTC()
	snap := resolvedSnap(now, map[sim.LedgerID]*sim.PayLedgerEntry{
		270: resolvedEntry(270, "prudence", "elizabeth", "water", 1, 10, sim.PayLedgerStatePending, false, time.Time{}),
		271: resolvedEntry(271, "prudence", "elizabeth", "meat", 1, 5, sim.PayLedgerStateCountered, false, now.Add(-10*time.Second)),
	})
	if got := buildRecentlyResolvedOffersFromMe(snap, "prudence", snap.Actors["prudence"]); got != nil {
		t.Errorf("pending + countered must be excluded; got %v", got)
	}
}

// A close-without-a-deal terminal (here insufficient stock) surfaces too, as a
// "stop waiting" signal for the buyer.
func TestBuildRecentlyResolvedOffersFromMe_FailedSurfacesAsClosed(t *testing.T) {
	now := time.Now().UTC()
	snap := resolvedSnap(now, map[sim.LedgerID]*sim.PayLedgerEntry{
		271: resolvedEntry(271, "prudence", "elizabeth", "water", 1, 10, sim.PayLedgerStateFailedInsufficientStock, true, now.Add(-15*time.Second)),
	})
	views := buildRecentlyResolvedOffersFromMe(snap, "prudence", snap.Actors["prudence"])
	if len(views) != 1 || views[0].Accepted {
		t.Fatalf("views = %+v, want one non-accepted (closed) view", views)
	}
}

func TestBuildRecentlyResolvedOffersFromMe_NilAndEmpty(t *testing.T) {
	if got := buildRecentlyResolvedOffersFromMe(nil, "prudence", nil); got != nil {
		t.Errorf("nil snap: got %v, want nil", got)
	}
	snap := resolvedSnap(time.Now().UTC(), nil)
	if got := buildRecentlyResolvedOffersFromMe(snap, "prudence", snap.Actors["prudence"]); got != nil {
		t.Errorf("empty ledger: got %v, want nil", got)
	}
}

// Render: the accepted line states the bargain is struck (eat-here → taken on
// the spot) and the closed line tells the buyer to stop waiting.
func TestRenderRecentlyResolvedOffersFromMe_AcceptedAndClosed(t *testing.T) {
	var b strings.Builder
	renderRecentlyResolvedOffersFromMe(&b, []ResolvedOfferView{
		{LedgerID: 270, SellerName: "Josiah Thorne", Item: "water", Qty: 1, Amount: 10, Accepted: true, ConsumeNow: true},
		{LedgerID: 271, SellerName: "the storekeeper", Item: "bread", Qty: 2, Amount: 4, Accepted: false},
	})
	out := b.String()
	if !strings.Contains(out, "## Recently settled offers") {
		t.Fatalf("missing section header; got:\n%s", out)
	}
	for _, want := range []string{
		"Josiah Thorne accepted your offer", "you had it right away", "don't offer for it again", "offer id 270",
		// LLM-296: the closed line now names what was OFFERED (4 coins for 2
		// bread), not just the want-item, so two declines aren't byte-identical.
		"Your offer of 4 coins to the storekeeper for 2 bread", "didn't go through", "stop waiting on it", "offer id 271",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered text missing %q; got:\n%s", want, out)
		}
	}
	// No seller-stock info on these views (SellerStocks false), so no shortfall clause.
	if strings.Contains(out, "hold only") {
		t.Errorf("unexpected stock-shortfall clause with no SellerStocks set; got:\n%s", out)
	}
}

// LLM-188: a consume_now buy whose needs-clamp ate fewer units than purchased
// must reconcile the eaten-vs-kept split in the line, so it agrees with the
// buyer's carried inventory instead of asserting all Qty were had on the spot
// (the contradiction that drove the "missing blueberry" confabulation). Anne's
// repro: paid 10 for 5 blueberries, ate 1, kept 4.
func TestRenderRecentlyResolvedOffersFromMe_ConsumeRemainderSplit(t *testing.T) {
	var b strings.Builder
	renderRecentlyResolvedOffersFromMe(&b, []ResolvedOfferView{
		{LedgerID: 449, SellerName: "Prudence Ward", Item: "blueberries", Qty: 5, Amount: 10, Accepted: true, ConsumeNow: true, KeptUnits: 4},
	})
	out := b.String()
	if !strings.Contains(out, "you ate 1 on the spot and kept the other 4") {
		t.Errorf("missing reconciling split clause; got:\n%s", out)
	}
	if strings.Contains(out, "you had it right away") {
		t.Errorf("clamped consume_now must not claim it was all had right away; got:\n%s", out)
	}
	if !strings.Contains(out, "you paid 10 coins for 5 blueberries") {
		t.Errorf("purchase facts must stay; got:\n%s", out)
	}
}

// The split clause is gated to a coherent self-consume split. A fully-consumed
// buy (KeptUnits 0) keeps "you had it right away"; a group-order split that
// breaks the 0 < KeptUnits < Qty invariant falls back to the plain line rather
// than print a nonsensical eaten count.
func TestRenderRecentlyResolvedOffersFromMe_ConsumeRemainderFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		view ResolvedOfferView
	}{
		{"ate all (kept 0)", ResolvedOfferView{LedgerID: 1, SellerName: "s", Item: "water", Qty: 3, Amount: 6, Accepted: true, ConsumeNow: true, KeptUnits: 0}},
		{"kept >= qty (group)", ResolvedOfferView{LedgerID: 2, SellerName: "s", Item: "meat", Qty: 2, Amount: 8, Accepted: true, ConsumeNow: true, KeptUnits: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			renderRecentlyResolvedOffersFromMe(&b, []ResolvedOfferView{tc.view})
			out := b.String()
			if !strings.Contains(out, "you had it right away") {
				t.Errorf("expected plain consume_now line; got:\n%s", out)
			}
			if strings.Contains(out, "on the spot and kept") {
				t.Errorf("did not expect split clause; got:\n%s", out)
			}
		})
	}
}

// The clamp surplus carried on the ledger entry reaches the view so the render
// can reconcile it (LLM-188).
func TestBuildRecentlyResolvedOffersFromMe_CarriesKeptUnits(t *testing.T) {
	now := time.Now().UTC()
	e := resolvedEntry(449, "anne", "prudence", "blueberries", 5, 10, sim.PayLedgerStateAccepted, true, now.Add(-20*time.Second))
	e.KeptUnits = 4
	snap := resolvedSnap(now, map[sim.LedgerID]*sim.PayLedgerEntry{449: e})
	views := buildRecentlyResolvedOffersFromMe(snap, "anne", snap.Actors["anne"])
	if len(views) != 1 {
		t.Fatalf("views = %+v, want one", views)
	}
	if views[0].KeptUnits != 4 {
		t.Errorf("KeptUnits = %d, want 4 (carried from the ledger entry)", views[0].KeptUnits)
	}
}

func TestRenderRecentlyResolvedOffersFromMe_EmptyGated(t *testing.T) {
	var b strings.Builder
	renderRecentlyResolvedOffersFromMe(&b, nil)
	if b.Len() != 0 {
		t.Errorf("empty input must render nothing; got %q", b.String())
	}
}

// LLM-296: a closed (declined/expired/failed) line names what was OFFERED — not
// just the want-item — so two declines aren't byte-identical (the repeat the
// thin line drove), and where the engine knows the seller is short the bought
// kind, it appends the shortfall as the informed "why". The shortfall only shows
// when it bites: a close whose seller holds enough names the bundle but no stock.
func TestRenderRecentlyResolvedOffersFromMe_ClosedNamesOfferAndShortfall(t *testing.T) {
	var b strings.Builder
	renderRecentlyResolvedOffersFromMe(&b, []ResolvedOfferView{
		// The live case: Josiah offered 6 carrots + 1 coin for 5 nails to Ezekiel,
		// who holds only 1 — the line names the bundle AND the shortfall.
		{LedgerID: 866, SellerName: "Ezekiel Crane", Item: "nail", Qty: 5, Amount: 1,
			PayItems: []sim.ItemKindQty{{Kind: "carrots", Qty: 6}}, Accepted: false,
			SellerStock: 1, SellerStocks: true, SellerStockNoun: "nails"},
		// A close where the seller holds enough (Qty <= stock): bundle named, no shortfall.
		{LedgerID: 867, SellerName: "the storekeeper", Item: "bread", Qty: 2, Amount: 4,
			Accepted: false, SellerStock: 9, SellerStocks: true, SellerStockNoun: "loaves of bread"},
		// LLM-303: the non-vendor case — a seller holding NONE of the asked good.
		// Names it "they hold no nails" (plural noun), not the awkward "only 0 nail".
		{LedgerID: 871, SellerName: "Elizabeth Reade", Item: "nail", Qty: 5, Amount: 0,
			PayItems: []sim.ItemKindQty{{Kind: "sage", Qty: 1}}, Accepted: false,
			SellerStock: 0, SellerStocks: true, SellerStockNoun: "nails"},
	})
	out := b.String()
	for _, want := range []string{
		"Your offer of 6 carrots and 1 coin to Ezekiel Crane for 5 nail", "didn't go through",
		"they hold only 1 nail", "offer id 866",
		"Your offer of 4 coins to the storekeeper for 2 bread", "offer id 867",
		"Your offer of 1 sage to Elizabeth Reade for 5 nail", "they hold no nails", "offer id 871",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered text missing %q; got:\n%s", want, out)
		}
	}
	// The sufficient-stock close must NOT carry a shortfall clause. Assert the
	// exact bad clause a regression would emit — the render prints the seller's
	// actual stock (9), so a wrongly-fired shortfall reads "they hold only 9
	// bread", not "2 bread". ("they hold only" alone is present from the nail
	// view above, so a bare substring check would false-fail.)
	if strings.Contains(out, "they hold only 9 bread") {
		t.Errorf("sufficient-stock close must not show a shortfall; got:\n%s", out)
	}
}

// LLM-296/LLM-303: the build side carries the seller's on-hand of the bought kind
// onto a CLOSED view (so the render can surface a shortfall), but not onto an
// accepted one (the reason clause is for closes). LLM-303: it names the shortfall
// for any real good the seller is short on, INCLUDING zero held (a non-vendor
// seller), and only a service kind (no inventory backing) leaves SellerStocks false.
func TestBuildRecentlyResolvedOffersFromMe_SellerStockOnClose(t *testing.T) {
	now := time.Now().UTC()
	resolved := now.Add(-20 * time.Second)

	// Declined, seller short: SellerStocks true, count carried.
	snap := resolvedSnap(now, map[sim.LedgerID]*sim.PayLedgerEntry{
		880: resolvedEntry(880, "prudence", "elizabeth", "nail", 5, 1, sim.PayLedgerStateDeclined, false, resolved),
	})
	snap.Actors["elizabeth"].Inventory = map[sim.ItemKind]int{"nail": 1}
	v := buildRecentlyResolvedOffersFromMe(snap, "prudence", snap.Actors["prudence"])
	if len(v) != 1 || v[0].Accepted {
		t.Fatalf("views = %+v, want one closed view", v)
	}
	if !v[0].SellerStocks || v[0].SellerStock != 1 {
		t.Errorf("closed view must carry seller stock 1; got SellerStocks=%v SellerStock=%d", v[0].SellerStocks, v[0].SellerStock)
	}

	// Accepted: no seller-stock lookup even when the seller is short.
	snap = resolvedSnap(now, map[sim.LedgerID]*sim.PayLedgerEntry{
		881: resolvedEntry(881, "prudence", "elizabeth", "nail", 5, 10, sim.PayLedgerStateAccepted, false, resolved),
	})
	snap.Actors["elizabeth"].Inventory = map[sim.ItemKind]int{"nail": 1}
	v = buildRecentlyResolvedOffersFromMe(snap, "prudence", snap.Actors["prudence"])
	if len(v) != 1 || !v[0].Accepted {
		t.Fatalf("views = %+v, want one accepted view", v)
	}
	if v[0].SellerStocks {
		t.Errorf("accepted view must not carry seller-stock reason; got SellerStocks=%v", v[0].SellerStocks)
	}

	// LLM-303: declined, seller holds NONE of a real good (asked 5, holds 0 nails):
	// the shortfall IS named now (SellerStocks true, stock 0, plural noun), so the
	// render can say "they hold no nails" — the non-vendor case the bare close hid.
	snap = resolvedSnap(now, map[sim.LedgerID]*sim.PayLedgerEntry{
		882: resolvedEntry(882, "prudence", "elizabeth", "nail", 5, 1, sim.PayLedgerStateDeclined, false, resolved),
	})
	snap.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{
		"nail": {Name: "nail", DisplayLabelSingular: "nail", DisplayLabelPlural: "nails"},
	}
	snap.Actors["elizabeth"].Inventory = map[sim.ItemKind]int{"milk": 3}
	v = buildRecentlyResolvedOffersFromMe(snap, "prudence", snap.Actors["prudence"])
	if len(v) != 1 || !v[0].SellerStocks || v[0].SellerStock != 0 || v[0].SellerStockNoun != "nails" {
		t.Errorf("real good held at zero must name the shortfall (SellerStocks true, stock 0, noun 'nails'); got %+v", v)
	}

	// LLM-303: a service kind (nights_stay, no inventory backing) still leaves
	// SellerStocks false — "they hold no ..." would be a false alarm for a service.
	snap = resolvedSnap(now, map[sim.LedgerID]*sim.PayLedgerEntry{
		883: resolvedEntry(883, "prudence", "elizabeth", "nights_stay", 1, 6, sim.PayLedgerStateDeclined, false, resolved),
	})
	snap.ItemKinds = map[sim.ItemKind]*sim.ItemKindDef{
		"nights_stay": {Name: "nights_stay", Capabilities: []string{"service", "lodging"}},
	}
	v = buildRecentlyResolvedOffersFromMe(snap, "prudence", snap.Actors["prudence"])
	if len(v) != 1 || v[0].SellerStocks {
		t.Errorf("a service kind must leave SellerStocks false; got %+v", v)
	}
}
