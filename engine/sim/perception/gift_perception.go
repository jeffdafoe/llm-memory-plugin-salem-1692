package perception

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// gift_perception.go — LLM-138. The one-way gift lane of the pay-ledger
// perception surface:
//
//   - recipient: "## Gifts offered to you" — the decision cue carrying the
//     ledger_id the model echoes into accept_gift / decline_gift (the gated
//     tools read the same PendingGiftsForMe view, so cue and tools can't drift).
//   - giver:     "## Gifts you have offered" — standing cue (don't re-offer).
//   - giver:     "## Gifts you have given"   — resolution cue (taken or not).
//
// Gift entries are IsGift PayLedgerEntries. They are EXCLUDED from the buy-side
// pay scans (buildPayOffersForMe / buildPendingOffersFromMe /
// buildRecentlyResolvedOffersFromMe) and rendered here instead, so a gift —
// which carries no bought item and no coins — never reads through buy-shaped
// copy. The gift goods live on PayItems and render via the shared
// formatOfferPayment (amount 0 → goods only), the same "3 blueberries" /
// "3 blueberries and 2 nails" shape the pay sections use.

// GiftOfferView is one pending gift offered TO the subject (subject =
// recipient). GiverName is the acquaintance-gated label; Goods are the items
// offered.
type GiftOfferView struct {
	LedgerID  sim.LedgerID
	GiverName string
	Goods     []sim.ItemKindQty
}

// StandingGiftView is one of the subject's OWN pending gifts (subject = giver),
// awaiting the recipient's answer.
type StandingGiftView struct {
	LedgerID      sim.LedgerID
	RecipientName string
	Goods         []sim.ItemKindQty
}

// SettledGiftView is one of the subject's OWN gifts that just resolved
// (subject = giver). Accepted distinguishes "they took it" from "they didn't."
type SettledGiftView struct {
	LedgerID      sim.LedgerID
	RecipientName string
	Goods         []sim.ItemKindQty
	Accepted      bool
}

// giftPeerLabel resolves an actor to the acquaintance-gated descriptor label
// the other ledger views use (mirrors the resolveSeller closures in build.go).
func giftPeerLabel(snap *sim.Snapshot, subjectSnap *sim.ActorSnapshot, id sim.ActorID) string {
	a := snap.Actors[id]
	if a == nil {
		return string(id)
	}
	acquainted := false
	if subjectSnap != nil && a.DisplayName != "" {
		_, acquainted = subjectSnap.Acquaintances[a.DisplayName]
	}
	return descriptorLabel(a.DisplayName, a.Role, acquainted)
}

// giftLedgerIDs collects the matching ledger ids, sorted ascending for
// deterministic render order.
func giftLedgerIDs(snap *sim.Snapshot, match func(*sim.PayLedgerEntry) bool) []sim.LedgerID {
	if snap == nil || len(snap.PayLedger) == 0 {
		return nil
	}
	var ids []sim.LedgerID
	for id, e := range snap.PayLedger {
		if e == nil || !match(e) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// buildGiftsForMe scans snap.PayLedger for pending gifts offered TO the subject
// (IsGift, Pending, SellerID == subject) — the gift counterpart to
// buildPayOffersForMe. snap.PayLedger is deep-cloned at publish, so aliasing
// PayItems into the read-only per-tick view is safe. Returns nil for none.
func buildGiftsForMe(snap *sim.Snapshot, subject sim.ActorID, subjectSnap *sim.ActorSnapshot) []GiftOfferView {
	ids := giftLedgerIDs(snap, func(e *sim.PayLedgerEntry) bool {
		return e.IsGift && e.State == sim.PayLedgerStatePending && e.SellerID == subject
	})
	if len(ids) == 0 {
		return nil
	}
	out := make([]GiftOfferView, 0, len(ids))
	for _, id := range ids {
		e := snap.PayLedger[id]
		out = append(out, GiftOfferView{
			LedgerID:  e.ID,
			GiverName: giftPeerLabel(snap, subjectSnap, e.BuyerID),
			Goods:     e.PayItems,
		})
	}
	return out
}

// buildGiftsFromMe scans snap.PayLedger for the subject's OWN pending gifts
// (IsGift, Pending, BuyerID == subject) — the giver-side standing view, the
// gift counterpart to buildPendingOffersFromMe.
func buildGiftsFromMe(snap *sim.Snapshot, subject sim.ActorID, subjectSnap *sim.ActorSnapshot) []StandingGiftView {
	ids := giftLedgerIDs(snap, func(e *sim.PayLedgerEntry) bool {
		return e.IsGift && e.State == sim.PayLedgerStatePending && e.BuyerID == subject
	})
	if len(ids) == 0 {
		return nil
	}
	out := make([]StandingGiftView, 0, len(ids))
	for _, id := range ids {
		e := snap.PayLedger[id]
		out = append(out, StandingGiftView{
			LedgerID:      e.ID,
			RecipientName: giftPeerLabel(snap, subjectSnap, e.SellerID),
			Goods:         e.PayItems,
		})
	}
	return out
}

// buildSettledGiftsFromMe scans snap.PayLedger for the subject's OWN gifts that
// just resolved (IsGift, terminal, BuyerID == subject, within
// recentlyResolvedOfferWindow of snap.PublishedAt) — the giver-side resolution
// view, the gift counterpart to buildRecentlyResolvedOffersFromMe.
func buildSettledGiftsFromMe(snap *sim.Snapshot, subject sim.ActorID, subjectSnap *sim.ActorSnapshot) []SettledGiftView {
	if snap == nil {
		return nil
	}
	ids := giftLedgerIDs(snap, func(e *sim.PayLedgerEntry) bool {
		if !e.IsGift || e.BuyerID != subject {
			return false
		}
		if e.State == sim.PayLedgerStatePending {
			return false
		}
		if e.ResolvedAt.IsZero() || snap.PublishedAt.Sub(e.ResolvedAt) > recentlyResolvedOfferWindow {
			return false
		}
		return true
	})
	if len(ids) == 0 {
		return nil
	}
	out := make([]SettledGiftView, 0, len(ids))
	for _, id := range ids {
		e := snap.PayLedger[id]
		out = append(out, SettledGiftView{
			LedgerID:      e.ID,
			RecipientName: giftPeerLabel(snap, subjectSnap, e.SellerID),
			Goods:         e.PayItems,
			Accepted:      e.State == sim.PayLedgerStateAccepted,
		})
	}
	return out
}

// PendingGiftsForMe returns the pending gifts offered to the subject — the
// shared predicate for the "## Gifts offered to you" section (renderGiftsForMe)
// and the handlers tool-gate (gateTools advertises accept_gift / decline_gift
// off the same view), so the cue and the advertised tools cannot drift.
func PendingGiftsForMe(p Payload) []GiftOfferView {
	return p.GiftsForMe
}

// renderGiftsForMe renders the recipient's "## Gifts offered to you" decision
// section — one line per pending gift carrying the ledger_id the model echoes
// into accept_gift / decline_gift. Uncapped, like renderPayOffers. Goods reuse
// formatOfferPayment (amount 0 → goods only); the giver name is acquaintance-
// gated at build time and sanitized inline here.
func renderGiftsForMe(b *strings.Builder, gifts []GiftOfferView) {
	if len(gifts) == 0 {
		return
	}
	b.WriteString("## Gifts offered to you\n")
	for i, g := range gifts {
		giver := sanitizeInline(g.GiverName)
		if giver == "" {
			giver = "someone"
		}
		fmt.Fprintf(b, "%d. %s offers to give you %s, free (offer id %d).\n",
			i+1, giver, formatOfferPayment(0, g.Goods), g.LedgerID)
	}
	// Action first, then an explicit speak — the same pattern renderPayOffers
	// uses: the gift response itself passes in silence, so a co-present gift
	// should still surface as a spoken beat (a speech bubble spawns only from
	// the speak tool).
	b.WriteString("To take a gift, call accept_gift with the offer id as ledger_id; to turn it down, call decline_gift. Then also use speak for a brief word, because the gift response itself passes in silence.\n")
}

// renderGiftsFromMe renders the giver's "## Gifts you have offered" standing
// section — the subject's own gifts awaiting the recipient's answer. Its job is
// suppression (don't re-offer the same gift): the gift mirror of
// renderPendingOffersFromMe.
func renderGiftsFromMe(b *strings.Builder, gifts []StandingGiftView) {
	if len(gifts) == 0 {
		return
	}
	b.WriteString("## Gifts you have offered\n")
	for i, g := range gifts {
		recipient := sanitizeInline(g.RecipientName)
		if recipient == "" {
			recipient = "someone"
		}
		fmt.Fprintf(b, "%d. You have offered %s to %s as a gift — they have yet to answer (offer id %d).\n",
			i+1, formatOfferPayment(0, g.Goods), recipient, g.LedgerID)
	}
	b.WriteString("Bide for their answer; do not offer the same gift again while this one stands. Should you think better of it, withdraw_pay recalls it.\n")
}

// renderSettledGiftsFromMe renders the giver's "## Gifts you have given"
// resolution section — the subject's gifts that just settled. Accepted → it's
// in their hands; otherwise it stays in the giver's pack. The gift counterpart
// to renderRecentlyResolvedOffersFromMe; plain modern English for the weak
// stateful models.
func renderSettledGiftsFromMe(b *strings.Builder, gifts []SettledGiftView) {
	if len(gifts) == 0 {
		return
	}
	b.WriteString("## Gifts you have given\n")
	for i, g := range gifts {
		recipient := sanitizeInline(g.RecipientName)
		if recipient == "" {
			recipient = "someone"
		}
		goods := formatOfferPayment(0, g.Goods)
		if g.Accepted {
			fmt.Fprintf(b, "%d. %s accepted your gift of %s — it is in their hands now (offer id %d).\n",
				i+1, recipient, goods, g.LedgerID)
			continue
		}
		fmt.Fprintf(b, "%d. %s did not take your gift of %s — it stays in your pack (offer id %d).\n",
			i+1, recipient, goods, g.LedgerID)
	}
}
