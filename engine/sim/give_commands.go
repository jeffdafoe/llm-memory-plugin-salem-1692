package sim

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// give_commands.go — LLM-138. The one-way gift command + its relationship-
// fact helpers.
//
// GiveItems mints a gift PayLedgerEntry: the giver hands goods to a
// co-present recipient for nothing in return. It is the sibling of
// PayWithItem (the buy-side front door) on the SAME pay-ledger substrate —
// a gift is Pending until the recipient resolves it with accept_gift /
// decline_gift (which reuse AcceptPay / DeclinePay), rides the same TTL
// sweep + terminal reaper, and stamps the recipient's PayOfferWarrantReason
// so they tick and can answer.
//
// It is deliberately NOT a lowering onto PayWithItem: PayWithItem hard-
// requires a bought item (the want leg), and is mostly buy-specific (quote
// fast-path, auto-match, eat-here clamp, consumers, lodging) — none of which
// a gift wants. The shared rails are the ledger entry, acceptPendingOffer
// (which skips the bought-item gates for a gift), and commitPayTransfer
// (whose existing PayItems swap moves the gift goods giver→recipient and
// whose gift branch skips the bought-item delivery). The gift carries the
// goods on PayLedgerEntry.PayItems with ItemKind/Qty empty, Amount 0, and
// IsGift true.

// GiveItems returns the Command for the one-way gift front door. The giver
// offers giftItems to a co-present recipient; the entry sits Pending until
// the recipient accepts (goods transfer) or declines (nothing moves).
// Mirrors PayWithItem's slow-path mint, stripped to the gift shape.
func GiveItems(
	giverID ActorID,
	recipientName string,
	giftItems []PayItemInput,
	forText string,
	at time.Time,
) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if len(giftItems) > MaxPayWithItemPayItems {
				return nil, fmt.Errorf(
					"GiveItems: too many goods lines (got %d, max %d) — combine into fewer lines.",
					len(giftItems), MaxPayWithItemPayItems,
				)
			}

			giver, ok := w.Actors[giverID]
			if !ok {
				return nil, fmt.Errorf("GiveItems: giver %q not in world", giverID)
			}
			if giver.MoveIntent != nil {
				return nil, errors.New(
					"you are walking — finish your move before offering a gift. " +
						"Either offer BEFORE the move_to, or wait until you arrive.",
				)
			}
			if giver.CurrentHuddleID == "" {
				return nil, errors.New(
					"you're not in a conversation — start one with the person you want to give to first.",
				)
			}

			// Anchor the scene the same way PayWithItem does — the entry
			// captures SceneID + HuddleID for accept-time co-presence.
			sceneID, ok := resolveSellerScene(w, giver.CurrentHuddleID)
			if !ok {
				return nil, errors.New(
					"your current conversation isn't anchored to a scene — wait for it to settle before offering a gift.",
				)
			}

			recipientID, ok, ambiguous := findHuddlePeerByDisplayName(w, giverID, giver.CurrentHuddleID, recipientName)
			if ambiguous {
				return nil, fmt.Errorf(
					"more than one person named %q is in this conversation — use a unique full name before giving.",
					recipientName,
				)
			}
			if !ok {
				return nil, fmt.Errorf(
					"no one named %q in this conversation — re-check who is here before giving.",
					recipientName,
				)
			}
			if recipientID == giverID {
				return nil, errors.New("you cannot give a gift to yourself")
			}
			recipient, ok := w.Actors[recipientID]
			if !ok {
				return nil, fmt.Errorf("GiveItems: recipient %q vanished mid-resolve", recipientID)
			}

			resolvedGiftItems, err := resolvePayItems(w, giftItems)
			if err != nil {
				return nil, err
			}
			// A gift must hand over at least one good — goods-only by design
			// (bare-coin generosity is the pay tool). This is the gift's
			// counterpart to the offer-validity "must offer something" rule, and
			// a gift sidesteps the free-goods hole by definition: the giver hands
			// goods AWAY and the recipient pays nothing, the opposite of the
			// all-zero buyer offer that hole guards against.
			if len(resolvedGiftItems) == 0 {
				return nil, errors.New(
					"a gift must include at least one item to give — name the goods you want to hand over.",
				)
			}
			// The giver must currently hold every gift good. Point-in-time, not
			// a reservation — acceptPendingOffer gate 12 revalidates at accept
			// and flips to failed_insufficient_goods if the giver no longer
			// holds them (the same backstop a barter pay-with leg gets).
			if !buyerHoldsPayItems(giver, resolvedGiftItems) {
				return nil, errors.New(
					"you don't hold all of those goods to give — check your pack.",
				)
			}

			// One pending gift per (giver, recipient) at a time — the gift
			// analogue of PayWithItem's cross-tick duplicate-offer gate. A weak
			// model that re-narrates the same generosity each tick would
			// otherwise stack duplicate gift offers the recipient then accepts
			// back-to-back. An entry past ExpiresAt is skipped (don't block the
			// giver on the sweep's cadence).
			for _, e := range w.PayLedger {
				if e == nil || !e.IsGift || e.State != PayLedgerStatePending {
					continue
				}
				if e.BuyerID != giverID || e.SellerID != recipientID {
					continue
				}
				if !e.ExpiresAt.IsZero() && !at.Before(e.ExpiresAt) {
					continue
				}
				return nil, fmt.Errorf(
					"you already offered %s a gift, awaiting their answer (offer id %d) — wait for their response, or withdraw_pay it before offering again.",
					recipient.DisplayName, e.ID,
				)
			}

			id := w.nextLedgerSeq()
			ttl := effectivePayLedgerTTL(w.Settings)
			expiresAt := at.Add(ttl)
			entry := &PayLedgerEntry{
				ID:       id,
				BuyerID:  giverID,
				SellerID: recipientID,
				IsGift:   true,
				PayItems: cloneItemKindQtys(resolvedGiftItems),
				Amount:   0,
				// The optional "for" note rides Message — unused by a pending gift
				// otherwise, and the accept path calls commitPayTransfer with an
				// empty forText param, so the entry must carry the note itself for
				// the gave/received_gift relationship facts to include it.
				Message:   truncatePayMessage(forText),
				State:     PayLedgerStatePending,
				CreatedAt: at,
				ExpiresAt: expiresAt,
				SceneID:   sceneID,
				HuddleID:  giver.CurrentHuddleID,
			}
			w.PayLedger[id] = entry

			// Reuse PayOfferReceived to wake the recipient (stamps their
			// PayOfferWarrantReason). The gift identity lives on the ledger
			// entry (IsGift) — the recipient's perception reads it off the live
			// ledger scan to render the gift cue and advertise accept_gift /
			// decline_gift instead of the buy-offer resolution tools.
			evt := &PayOfferReceived{
				LedgerID:  id,
				BuyerID:   giverID,
				SellerID:  recipientID,
				PayItems:  cloneItemKindQtys(resolvedGiftItems),
				SceneID:   sceneID,
				HuddleID:  giver.CurrentHuddleID,
				ExpiresAt: expiresAt,
				At:        at,
			}
			w.emit(evt)
			entry.RootEventID = evt.RootEventID()
			entry.SourceEventID = evt.EventID()

			return PayWithItemResult{
				LedgerID: id,
				State:    PayLedgerStatePending,
				FastPath: false,
			}, nil
		},
	}
}

// AcceptGift is the recipient's accept for a one-way gift (LLM-138). It shares
// the accept path with AcceptPay via acceptPayCommand, but requires the entry to
// BE a gift (expectGift true) — so accept_gift can't resolve a purchase offer,
// and accept_pay can't resolve a gift. The disposition boundary is enforced at
// the substrate, not merely the gateTools advertising layer.
func AcceptGift(callerID ActorID, ledgerID LedgerID, at time.Time) Command {
	return acceptPayCommand(callerID, ledgerID, at, true)
}

// DeclineGift is the recipient's decline for a one-way gift (LLM-138) — the gift
// counterpart to DeclinePay, sharing declinePayCommand with expectGift true.
func DeclineGift(callerID ActorID, ledgerID LedgerID, reason string, at time.Time) Command {
	return declinePayCommand(callerID, ledgerID, reason, at, true)
}

// giftFactText renders the relationship-memory line for a settled gift,
// from the giver's POV (fromGiverPOV true → "I gave Lewis Walker 3
// blueberries as a gift.") or the recipient's ("Ezekiel Crane gave me 3
// blueberries as a gift."). An optional forText appends the giver's stated
// reason.
//
// The note is elided HERE, against what the rest of the sentence leaves it, so
// the fact arrives at NewSalientFact already inside MaxSalientFactTextLen and
// takes no second cut (LLM-405). Cutting it downstream instead would clip the
// sentence at a rune offset that has nothing to do with its grammar: the give
// tool admits a 200-rune note, the sentence around it runs another ~45, so a
// long note routinely overran the 220-rune fact cap and the fact was stored with
// its closing paren — and the tail of the note — sliced off mid-word. The reader
// then meets a memory that simply stops, and answers the dangling clause.
func giftFactText(w *World, giverName, recipientName string, items []ItemKindQty, forText string, fromGiverPOV bool) string {
	goods := giftGoodsPhrase(w, items)
	var s string
	if fromGiverPOV {
		s = fmt.Sprintf("I gave %s %s as a gift", recipientName, goods)
	} else {
		s = fmt.Sprintf("%s gave me %s as a gift", giverName, goods)
	}
	if forText != "" {
		// What the note may spend: the fact cap, less the sentence already built
		// and the 4 runes of punctuation that wrap and close it (" (" … ")." ).
		const wrapRunes = 4
		budget := MaxSalientFactTextLen - utf8.RuneCountInString(s) - wrapRunes
		// A gift of enough distinct items can spend the whole budget on the goods
		// phrase alone. Drop the note rather than render an empty "(…)" — the goods
		// are the fact; NewSalientFact still marks whatever it has to cut off THAT.
		if budget > 0 {
			s += fmt.Sprintf(" (%s)", capRunesMarked(forText, budget))
		}
	}
	return s + "."
}

// giftGoodsPhrase renders a gift's goods lines as "3 blueberries, 2 nails",
// quantity-aware (singular vs plural display label), falling back to the
// canonical kind when an item carries no label.
func giftGoodsPhrase(w *World, items []ItemKindQty) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%d %s", it.Qty, giftItemLabel(w, it.Kind, it.Qty)))
	}
	return strings.Join(parts, ", ")
}

// giftItemLabel returns the lowercase display label for a kind at a given
// quantity (plural when qty != 1), suitable for inlining in a gift fact
// sentence. Falls back to DisplayLabel then the canonical kind string.
func giftItemLabel(w *World, kind ItemKind, qty int) string {
	def := w.ItemKinds[kind]
	if def == nil {
		return string(kind)
	}
	if qty == 1 && def.DisplayLabelSingular != "" {
		return def.DisplayLabelSingular
	}
	if qty != 1 && def.DisplayLabelPlural != "" {
		return def.DisplayLabelPlural
	}
	if def.DisplayLabel != "" {
		return strings.ToLower(def.DisplayLabel)
	}
	return string(kind)
}
