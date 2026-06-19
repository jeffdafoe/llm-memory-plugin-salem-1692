package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// pay_offer_counter_test.go — LLM-21. The buyer-side "## A counter to your offer"
// view (buildCountersAwaitingMyResponse + renderCountersAwaitingMyResponse). A
// seller's counter reaches the buyer ONLY via the PayResolvedWarrantReason
// {Countered} event — the recently-settled scan deliberately excludes Countered —
// and that warrant can ride a tick behind an in-flight deliberation (or be
// evicted while the buyer is shelved). This scan surfaces an un-answered counter
// from the SNAPSHOT alone, warrant-independent by construction, so a buyer can't
// re-offer a need already in negotiation or miss the counter entirely.
//
// Reuses offerSnap / resolvedSnap (Prudence Ward buyer, Elizabeth Ellis seller).

// counterEntry builds a terminal Countered PayLedgerEntry for tests. Depth,
// ParentID and CounterPayItems default to the zero value; tests set them inline
// where a case needs them.
func counterEntry(id sim.LedgerID, buyer, seller sim.ActorID, item sim.ItemKind, qty, counterAmount int, resolvedAt time.Time) *sim.PayLedgerEntry {
	return &sim.PayLedgerEntry{
		ID:            id,
		BuyerID:       buyer,
		SellerID:      seller,
		ItemKind:      item,
		Qty:           qty,
		State:         sim.PayLedgerStateCountered,
		CounterAmount: counterAmount,
		ResolvedAt:    resolvedAt,
	}
}

// A fresh, un-answered counter below the depth cap surfaces, with the seller's
// counter terms and a role-gated seller name (Prudence doesn't know Elizabeth).
func TestBuildCountersAwaitingMyResponse_SurfacedWithinWindow(t *testing.T) {
	now := time.Now().UTC()
	snap := resolvedSnap(now, map[sim.LedgerID]*sim.PayLedgerEntry{
		236: counterEntry(236, "prudence", "elizabeth", "meat", 10, 60, now.Add(-30*time.Second)),
	})
	views := buildCountersAwaitingMyResponse(snap, "prudence", snap.Actors["prudence"])
	if len(views) != 1 {
		t.Fatalf("views = %v, want one counter awaiting response", views)
	}
	v := views[0]
	if v.LedgerID != 236 || v.Item != "meat" || v.Qty != 10 || v.CounterAmount != 60 {
		t.Errorf("view = %+v, want ledger 236 / meat / qty 10 / counter 60", v)
	}
	if v.SellerName != "the dairykeeper" {
		t.Errorf("SellerName = %q, want role-gated %q", v.SellerName, "the dairykeeper")
	}
}

// Once acquainted, the seller's display name is used (parity with the pending view).
func TestBuildCountersAwaitingMyResponse_AcquaintedUsesDisplayName(t *testing.T) {
	now := time.Now().UTC()
	snap := resolvedSnap(now, map[sim.LedgerID]*sim.PayLedgerEntry{
		236: counterEntry(236, "prudence", "elizabeth", "meat", 10, 60, now.Add(-30*time.Second)),
	})
	snap.Actors["prudence"].Acquaintances = map[string]sim.Acquaintance{"Elizabeth Ellis": {}}
	views := buildCountersAwaitingMyResponse(snap, "prudence", snap.Actors["prudence"])
	if len(views) != 1 || views[0].SellerName != "Elizabeth Ellis" {
		t.Errorf("SellerName = %q, want %q once acquainted", views[0].SellerName, "Elizabeth Ellis")
	}
}

// A counter older than the response window is dropped — the view is a brief
// decision bridge, not a backlog (terminal entries linger up to 1h in the ledger).
func TestBuildCountersAwaitingMyResponse_StaleExcluded(t *testing.T) {
	now := time.Now().UTC()
	snap := resolvedSnap(now, map[sim.LedgerID]*sim.PayLedgerEntry{
		236: counterEntry(236, "prudence", "elizabeth", "meat", 10, 60, now.Add(-30*time.Minute)),
	})
	if got := buildCountersAwaitingMyResponse(snap, "prudence", snap.Actors["prudence"]); got != nil {
		t.Errorf("counter resolved 30m ago (> window) must be excluded; got %v", got)
	}
}

// Once the buyer answers (a fresh entry chained via ParentID), the counter is no
// longer awaiting a response and drops out.
func TestBuildCountersAwaitingMyResponse_AnsweredExcluded(t *testing.T) {
	now := time.Now().UTC()
	child := offerEntry(237, "prudence", "elizabeth", "meat", 10, 60, sim.PayLedgerStatePending)
	child.ParentID = 236
	snap := resolvedSnap(now, map[sim.LedgerID]*sim.PayLedgerEntry{
		236: counterEntry(236, "prudence", "elizabeth", "meat", 10, 60, now.Add(-20*time.Second)),
		237: child,
	})
	if got := buildCountersAwaitingMyResponse(snap, "prudence", snap.Actors["prudence"]); got != nil {
		t.Errorf("an answered counter (child chained via ParentID) must be excluded; got %v", got)
	}
}

// A counter already at the chain depth cap can't be answered (validateInResponseTo
// rejects it), so it isn't surfaced as awaiting a response.
func TestBuildCountersAwaitingMyResponse_DepthCapExcluded(t *testing.T) {
	now := time.Now().UTC()
	capped := counterEntry(236, "prudence", "elizabeth", "meat", 10, 60, now.Add(-20*time.Second))
	capped.Depth = sim.MaxPayCounterChainDepth
	snap := resolvedSnap(now, map[sim.LedgerID]*sim.PayLedgerEntry{236: capped})
	if got := buildCountersAwaitingMyResponse(snap, "prudence", snap.Actors["prudence"]); got != nil {
		t.Errorf("a counter at the depth cap must be excluded; got %v", got)
	}
}

// Only Countered entries for the subject-as-buyer surface: a pending/accepted
// entry and a counter staked against a different buyer are all ignored.
func TestBuildCountersAwaitingMyResponse_NonCounteredAndWrongBuyerExcluded(t *testing.T) {
	now := time.Now().UTC()
	otherBuyer := counterEntry(238, "mary", "elizabeth", "meat", 1, 9, now.Add(-10*time.Second))
	snap := resolvedSnap(now, map[sim.LedgerID]*sim.PayLedgerEntry{
		236: offerEntry(236, "prudence", "elizabeth", "meat", 10, 48, sim.PayLedgerStatePending),
		237: offerEntry(237, "prudence", "elizabeth", "water", 1, 10, sim.PayLedgerStateAccepted),
		238: otherBuyer,
	})
	if got := buildCountersAwaitingMyResponse(snap, "prudence", snap.Actors["prudence"]); got != nil {
		t.Errorf("non-countered + wrong-buyer entries must be excluded; got %v", got)
	}
}

// Multiple counters surface in deterministic LedgerID-ascending order (the map
// scan is unordered; the view contract promises a stable sort).
func TestBuildCountersAwaitingMyResponse_SortedByLedgerID(t *testing.T) {
	now := time.Now().UTC()
	snap := resolvedSnap(now, map[sim.LedgerID]*sim.PayLedgerEntry{
		240: counterEntry(240, "prudence", "elizabeth", "water", 1, 12, now.Add(-10*time.Second)),
		236: counterEntry(236, "prudence", "elizabeth", "meat", 10, 60, now.Add(-20*time.Second)),
		238: counterEntry(238, "prudence", "elizabeth", "bread", 2, 6, now.Add(-15*time.Second)),
	})
	views := buildCountersAwaitingMyResponse(snap, "prudence", snap.Actors["prudence"])
	got := make([]sim.LedgerID, len(views))
	for i, v := range views {
		got[i] = v.LedgerID
	}
	want := []sim.LedgerID{236, 238, 240}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ordering = %v, want ascending %v", got, want)
		}
	}
}

func TestBuildCountersAwaitingMyResponse_NilAndEmpty(t *testing.T) {
	if got := buildCountersAwaitingMyResponse(nil, "prudence", nil); got != nil {
		t.Errorf("nil snap: got %v, want nil", got)
	}
	snap := resolvedSnap(time.Now().UTC(), nil)
	if got := buildCountersAwaitingMyResponse(snap, "prudence", snap.Actors["prudence"]); got != nil {
		t.Errorf("empty ledger: got %v, want nil", got)
	}
}

// Render: a coin counter and a goods counter both state the new terms and the
// load-bearing offer id, and the closing line names the response path.
func TestRenderCountersAwaitingMyResponse_CoinAndGoods(t *testing.T) {
	var b strings.Builder
	renderCountersAwaitingMyResponse(&b, []CounterOfferView{
		{LedgerID: 236, SellerName: "the dairykeeper", Item: "meat", Qty: 10, CounterAmount: 60},
		{LedgerID: 240, SellerName: "Josiah Thorne", Item: "horseshoe", Qty: 4, CounterPayItems: []sim.ItemKindQty{{Kind: "nails", Qty: 5}}},
	})
	out := b.String()
	if !strings.Contains(out, "## A counter to your offer") {
		t.Fatalf("missing section header; got:\n%s", out)
	}
	for _, want := range []string{
		"the dairykeeper countered your offer for 10 meat", "60 coins", "offer id 236",
		"Josiah Thorne countered your offer for 4 horseshoe", "5 nails", "offer id 240",
		"pay_with_item", "in_response_to", "let it go",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered text missing %q; got:\n%s", want, out)
		}
	}
}

func TestRenderCountersAwaitingMyResponse_EmptyGated(t *testing.T) {
	var b strings.Builder
	renderCountersAwaitingMyResponse(&b, nil)
	if b.Len() != 0 {
		t.Errorf("empty input must render nothing; got %q", b.String())
	}
}
