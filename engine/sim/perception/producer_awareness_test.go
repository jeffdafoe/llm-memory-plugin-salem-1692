package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// LLM-171 — buyer-side producer-awareness: the engine strips the actionable take
// from a buy-quote for a good the buyer makes itself or already holds at cap, so
// a co-present seller's mis-pitched quote can't drive a buy-back of the buyer's
// own ware (the live Ezekiel skillet buy-back).

func TestBuildOwnProducedKinds(t *testing.T) {
	actorSnap := &sim.ActorSnapshot{
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "skillet", Source: sim.RestockSourceProduce, Max: 5},
			{Item: "nail", Source: sim.RestockSourceProduce, Max: 20},
			{Item: "ale", Source: sim.RestockSourceBuy, Target: 10}, // bought, not made
		}},
	}
	got := buildOwnProducedKinds(actorSnap)
	if !got["skillet"] || !got["nail"] {
		t.Errorf("expected skillet + nail produced, got %v", got)
	}
	if got["ale"] {
		t.Errorf("ale is bought, not produced — must not be in OwnProducedKinds")
	}
	if buildOwnProducedKinds(nil) != nil {
		t.Errorf("nil actorSnap should give nil")
	}
	if buildOwnProducedKinds(&sim.ActorSnapshot{}) != nil {
		t.Errorf("nil RestockPolicy should give nil")
	}
}

func TestBuildAtCapKinds(t *testing.T) {
	actorSnap := &sim.ActorSnapshot{
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "skillet", Source: sim.RestockSourceProduce, Max: 5}, // at cap (5 on hand)
			{Item: "nail", Source: sim.RestockSourceProduce, Max: 20},   // below cap (3 on hand)
			{Item: "ale", Source: sim.RestockSourceBuy, Target: 10},     // at cap via the legacy Target alias
			{Item: "water", Source: sim.RestockSourceBuy},               // no cap configured → never "at cap"
		}},
	}
	inv := []InventoryItem{
		{Label: "skillet", Qty: 5, kind: "skillet"},
		{Label: "nail", Qty: 3, kind: "nail"},
		{Label: "ale", Qty: 12, kind: "ale"},
		{Label: "water", Qty: 99, kind: "water"},
	}
	got := buildAtCapKinds(actorSnap, inv)
	if !got["skillet"] || !got["ale"] {
		t.Errorf("expected skillet + ale at cap, got %v", got)
	}
	if got["nail"] {
		t.Errorf("nail is below cap (3/20) but was flagged at cap")
	}
	if got["water"] {
		t.Errorf("water has no cap configured but was flagged at cap")
	}
	if buildAtCapKinds(nil, inv) != nil {
		t.Errorf("nil actorSnap should give nil")
	}
}

func TestBuyQuoteRedundancyReason(t *testing.T) {
	mkLines := func(kinds ...sim.ItemKind) []sim.QuoteLine {
		out := make([]sim.QuoteLine, len(kinds))
		for i, k := range kinds {
			out[i] = sim.QuoteLine{ItemKind: k, Qty: 1}
		}
		return out
	}
	produced := map[sim.ItemKind]bool{"skillet": true}
	atCap := map[sim.ItemKind]bool{"nail": true}
	redundant := func(k sim.ItemKind) (bool, bool) { return produced[k], atCap[k] }

	cases := []struct {
		name  string
		lines []sim.QuoteLine
		want  string
	}{
		{"all produced", mkLines("skillet"), "produced"},
		{"all at cap", mkLines("nail"), "atcap"},
		{"produced + at-cap mix leads with carry reason", mkLines("skillet", "nail"), "atcap"},
		{"one genuinely wanted good keeps the take", mkLines("skillet", "ale"), ""},
		{"plain wanted good", mkLines("ale"), ""},
		{"empty quote", nil, ""},
	}
	for _, c := range cases {
		if got := buyQuoteRedundancyReason(c.lines, redundant); got != c.want {
			t.Errorf("%s: buyQuoteRedundancyReason = %q, want %q", c.name, got, c.want)
		}
	}
	// A nil predicate degrades to "" (never redundant — every quote keeps its take).
	if got := buyQuoteRedundancyReason(mkLines("skillet"), nil); got != "" {
		t.Errorf("nil predicate: got %q, want \"\"", got)
	}
}

// TestRender_QuoteWarrantLine_BuyBackSteer drives the full Render path: a quote
// for a good the buyer MAKES drops the take with the maker steer; a good they're
// at CAP on gets the carry steer; a good they neither make nor cap keeps the
// normal actionable take.
func TestRender_QuoteWarrantLine_BuyBackSteer(t *testing.T) {
	quote := sim.WarrantMeta{
		TriggerActorID: "john",
		Reason: sim.SceneQuoteTargetedWarrantReason{
			QuoteID: 1, SellerID: "john", Lines: []sim.QuoteLine{{ItemKind: "skillet", Qty: 1}}, Amount: 2,
		},
		SourceEventID: 7,
	}
	base := func() Payload {
		return Payload{
			ActorID:           "ezekiel",
			Warrants:          []sim.WarrantMeta{quote},
			WarrantActorNames: map[sim.ActorID]string{"john": "John Ellis"},
			Baseline:          BaselinePresent,
		}
	}

	// Own-produced → "wares you make yourself" steer, no take.
	p := base()
	p.OwnProducedKinds = map[sim.ItemKind]bool{"skillet": true}
	out := combinedPrompt(Render(p, DefaultRenderConfig()))
	if !strings.Contains(out, "these are wares you make yourself") {
		t.Errorf("own-produced quote missing the maker steer\n%s", out)
	}
	if strings.Contains(out, "pay_with_item with quote_id") {
		t.Errorf("own-produced quote should withhold the actionable take\n%s", out)
	}

	// At cap (not produced) → carry-reason steer, no take.
	p = base()
	p.AtCapKinds = map[sim.ItemKind]bool{"skillet": true}
	out = combinedPrompt(Render(p, DefaultRenderConfig()))
	if !strings.Contains(out, "all of these you can carry") {
		t.Errorf("at-cap quote missing the carry steer\n%s", out)
	}
	if strings.Contains(out, "pay_with_item with quote_id") {
		t.Errorf("at-cap quote should withhold the actionable take\n%s", out)
	}

	// Neither produced nor at cap → normal actionable take, no steer.
	p = base()
	out = combinedPrompt(Render(p, DefaultRenderConfig()))
	if !strings.Contains(out, "pay_with_item with quote_id") {
		t.Errorf("a good the buyer neither makes nor caps should keep its take\n%s", out)
	}
	if strings.Contains(out, "there's no reason to buy") {
		t.Errorf("non-redundant quote should not show the buy-back steer\n%s", out)
	}
}
