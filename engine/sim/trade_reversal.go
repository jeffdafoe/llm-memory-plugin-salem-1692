package sim

import "time"

// trade_reversal.go — LLM-555. A seller's experiential memory of what they just
// sold a particular person, so the two of them stop churning one good back and
// forth. Live 2026-07-28: iron went factor -> Josiah at 19:53, back Josiah ->
// factor at 20:50, and Josiah was buying it off him again at 21:32 — three legs
// of the same bar of iron in ninety minutes, each leg a fresh negotiation.
//
// The churn predicate itself already existed and was correctly shaped:
// callerSellsItemTo's arm 2 (LLM-189) asks "did I sell this item to this person",
// and refuses the buyer verb when the answer is yes. What it could not do is
// remember. It reads World.PayLedger scoped to the CURRENT huddle, so the moment
// the conversation ends the fact is gone — and every observed reversal crosses a
// huddle boundary (55 of 55 in the ledger since 2026-05-06). The gate was looking
// in the one place this never happens.
//
// So the fix is the shape LLM-198 and LLM-525 already established: a refusal the
// actor REMEMBERS rather than one legible only inside the conversation that
// produced it. This is the CAPTURE half (a PayWithItemResolved subscriber,
// additive). The SURFACE halves are the buy directory (peerRememberedSoldTo in
// perception/consumable_vendors.go, read by findItemVendors) and the reverse-pay
// role-gate's arm 3 (pay_with_item_commands.go). The store is the unified
// observed-state memory in observed_state.go (the ObservedSoldToPeer condition).
//
// PERSON-keyed, not structure-keyed like ObservedSaleStandoff. Two reasons, both
// load-bearing: the churn is a fact about a trading PARTNER rather than about a
// shop, and the live case is a visiting factor who has no workplace at all — a
// structure key would have missed the very trade that prompted the ticket.
//
// DIRECTIONAL. The memory says "I sold kind to P", and only ever suppresses the
// reverse leg (buying kind back off P). It says nothing about selling P more of
// the same, nor about buying kind from anyone else. That is what keeps the
// distributor whole: Josiah buying iron from the importing factor and selling it
// on to Ezekiel is his entire function, and he restocked flour from the mill
// twice in thirty-five minutes on the same afternoon. Neither is touched. Only
// the round trip with the same partner is.

// SoldToPeerMemoryTTL is how long a seller remembers selling an item to someone
// before perception will route them back to that person to buy the same good.
// 3 game-hours, chosen from the ledger rather than guessed: of 58 same-pair
// same-item reversals since 2026-05-06, 19 fall inside three hours and the rest
// spread out across a day or more, where the pair are simply trading regularly
// and a reversal is ordinary village commerce rather than churn. Three hours also
// clears the ~1h PayLedgerInResponseToWindow that reaps the terminal entries arm
// 2 reads, which is the ceiling a ledger-based recency scan could not have gone
// past.
const SoldToPeerMemoryTTL = 3 * time.Hour

// handleTradeReversalOnResolved is the PayWithItemResolved subscriber that
// records, on the SELLER, what they just sold to whom. Accepted terminals only:
// a declined or expired offer moved no goods and gives the pair nothing to churn.
//
// Gifts are excluded, and not merely as a matter of taste — a gift entry inverts
// the roles (BuyerID is the giver, SellerID the recipient) and carries its goods
// in PayItems with an empty ItemKind, so stamping one would write a backwards
// memory under an empty key. `give` is ungated in both directions by design
// (LLM-544), and handing someone a wheel of cheese is not a sale that a later
// purchase reverses. The discriminator is read off the EVENT, never looked up in
// the ledger: the LLM-525 rule for this subscriber family is that a resolved
// event must not depend on the resolving entry still being present, nor on
// whether the subscriber runs before or after that entry's own writes.
//
// A no-op for non-agent sellers: a PC carries its own continuity through the
// player, and a decorative actor never takes a turn, so neither has an Observed
// store worth writing.
func handleTradeReversalOnResolved(w *World, evt Event) {
	res, ok := evt.(*PayWithItemResolved)
	if !ok || res.TerminalState != PayTerminalStateAccepted || res.IsGift {
		return
	}
	seller := w.Actors[res.SellerID]
	if seller == nil || !isAgentNPC(seller) {
		return
	}
	if res.BuyerID == "" || res.BuyerID == res.SellerID {
		return // defensive: nothing to remember about trading with yourself
	}
	for _, kind := range soldItemKinds(res) {
		seller.Observed.Observe(
			ObservedStateKey{PeerID: res.BuyerID, ItemKind: kind, Condition: ObservedSoldToPeer},
			res.At,
		)
	}
}

// soldItemKinds returns the item kinds that actually changed hands on an accepted
// sale. A single-item terminal carries its kind in ItemKind; a bundle quote-take
// (LLM-101) leaves ItemKind empty and carries the lines instead, and every line of
// a bundle is as much a sale as a single item is — a bundle would otherwise be a
// hole the churn slips straight through. Empty kinds are skipped so a malformed
// line can never stamp a memory under an empty key, which would suppress every
// item for that partner at once.
func soldItemKinds(res *PayWithItemResolved) []ItemKind {
	if res.ItemKind != "" {
		return []ItemKind{res.ItemKind}
	}
	out := make([]ItemKind, 0, len(res.Lines))
	for _, line := range res.Lines {
		if line.ItemKind != "" {
			out = append(out, line.ItemKind)
		}
	}
	return out
}

// ActorRecentlySoldTo reports whether seller carries a live memory (within
// SoldToPeerMemoryTTL of now) of selling kind to peer. The single definition of
// the churn question on the live world, shared by the reverse-pay role-gate;
// perception asks the same question of its snapshot through peerRememberedSoldTo.
// Exported so the two stay one predicate rather than two copies of a key literal.
func ActorRecentlySoldTo(seller *Actor, peer ActorID, kind ItemKind, now time.Time) bool {
	if seller == nil || peer == "" || kind == "" {
		return false
	}
	return seller.Observed.Active(
		ObservedStateKey{PeerID: peer, ItemKind: kind, Condition: ObservedSoldToPeer},
		now,
	)
}

// RegisterTradeReversalSubscriber wires the sold-to-peer-memory subscriber. Call
// before World.Run or from inside a Command (world-goroutine-safe). Mirrors
// RegisterSaleStandoffSubscriber — another observed-state capture subscriber on
// the same event. LLM-555.
func RegisterTradeReversalSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterTradeReversalSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleTradeReversalOnResolved))
}
