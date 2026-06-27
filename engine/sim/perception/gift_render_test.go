package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// gift_render_test.go — LLM-138. Renders the three gift sections so the exact
// model-facing copy is asserted (and visible via -v).

func giftGoods(item sim.ItemKind, qty int) []sim.ItemKindQty {
	return []sim.ItemKindQty{{Kind: item, Qty: qty}}
}

func TestRenderGiftsForMe_RecipientCue(t *testing.T) {
	var b strings.Builder
	renderGiftsForMe(&b, []GiftOfferView{
		{LedgerID: 17, GiverName: "Ezekiel Crane", Goods: giftGoods("blueberries", 3)},
	})
	out := b.String()
	t.Logf("\n%s", out)
	for _, want := range []string{
		"## Gifts offered to you",
		"Ezekiel Crane offers to give you 3 blueberries, free (offer id 17).",
		"call accept_gift with the offer id as ledger_id",
		"decline_gift",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("recipient gift cue missing %q\n%s", want, out)
		}
	}
}

func TestRenderGiftsFromMe_GiverStanding(t *testing.T) {
	var b strings.Builder
	renderGiftsFromMe(&b, []StandingGiftView{
		{LedgerID: 17, RecipientName: "Lewis Walker", Goods: giftGoods("blueberries", 3)},
	})
	out := b.String()
	t.Logf("\n%s", out)
	for _, want := range []string{
		"## Gifts you have offered",
		"You have offered 3 blueberries to Lewis Walker as a gift — they have yet to answer (offer id 17).",
		"withdraw_pay recalls it",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("giver standing gift cue missing %q\n%s", want, out)
		}
	}
}

func TestRenderSettledGiftsFromMe_GiverResolution(t *testing.T) {
	var b strings.Builder
	renderSettledGiftsFromMe(&b, []SettledGiftView{
		{LedgerID: 17, RecipientName: "Lewis Walker", Goods: giftGoods("blueberries", 3), Accepted: true},
		{LedgerID: 18, RecipientName: "Prudence Ward", Goods: giftGoods("bread", 1), Accepted: false},
	})
	out := b.String()
	t.Logf("\n%s", out)
	for _, want := range []string{
		"## Gifts you have given",
		"Lewis Walker accepted your gift of 3 blueberries — it is in their hands now (offer id 17).",
		"Prudence Ward did not take your gift of 1 bread — it stays in your pack (offer id 18).",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("giver resolution gift cue missing %q\n%s", want, out)
		}
	}
}

// TestGiftSections_ContentGated: each section renders nothing for an empty view.
func TestGiftSections_ContentGated(t *testing.T) {
	var b strings.Builder
	renderGiftsForMe(&b, nil)
	renderGiftsFromMe(&b, nil)
	renderSettledGiftsFromMe(&b, nil)
	if b.Len() != 0 {
		t.Errorf("gift sections should content-gate to empty, got:\n%s", b.String())
	}
}
