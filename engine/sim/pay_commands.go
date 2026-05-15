package sim

import (
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"
)

// MaxPayAmount is the upper bound on amount accepted by the Pay Command,
// mirroring the handler-side cap. Re-enforced inside the Command Fn because
// Pay is exported — non-handler callers (tests, admin paths, future
// in-engine cascades) could otherwise mint or overdraw coins.
const MaxPayAmount = math.MaxInt32

// Pay returns a Command that commits a coin transfer from buyerID to the
// huddle peer whose DisplayName matches recipientName (case-insensitive).
// Phase 3 PR B — the port of v1's `case "pay":` commit arm from
// agent_tick.go to the v2 in-memory substrate, scoped to **pure coin
// transfer**: no items, no qty, no consume_now, no consumers, no
// in_response_to, no deliberation tick. The mismatched-pay haggling chain
// + ledger + inventory port to later PRs alongside their substrate.
//
// Pre-conditions the caller (the pay handlers.CommitFn) normalizes but
// the Command Fn ALSO re-validates because Pay is exported — non-handler
// callers (tests, admin paths, future in-engine cascades) must not be
// able to mint coins via a negative amount or smuggle a no-op event via
// amount=0:
//
//   - recipientName trimmed, non-empty
//   - amount >= 1 and <= MaxPayAmount (re-checked here)
//   - forText trimmed; control-char-rejected; length <= MaxPayForChars
//
// World-state pre-conditions checked here:
//
//   - buyerID resolves to a real actor in w.Actors
//   - buyer.MoveIntent == nil (not walk-in-flight)
//   - buyer.CurrentHuddleID != "" (must be in a conversation)
//   - recipientName resolves to a single huddle peer (case-insensitive
//     DisplayName; ambiguity → reject)
//   - resolved seller != buyer (no self-pay)
//   - buyer.Coins >= amount (sufficient balance)
//   - seller.Coins + amount does not overflow int (balance overflow guard)
//
// On success:
//
//   - buyer.Coins -= amount, seller.Coins += amount
//   - emits Paid{BuyerID, SellerID, Amount, ForText, At}
//   - RecordInteraction(buyer, seller, InteractionPaid, "<text>", at)
//   - RecordInteraction(seller, buyer, InteractionPaidBy, "<text>", at)
//     (the KindNPCShared gate inside RecordInteraction filters which writes
//     actually persist — stateful-VA NPCs get pay continuity from their VA's
//     own memory; the engine-side gate silently no-ops for them)
//   - no warrants stamped here — the Paid event subscriber
//     (handlers/pay_reactor.go) mints a PaidWarrantReason warrant on the
//     seller
//
// Same-huddle gate (locked at PR B design walkthrough): pay is a
// transactional act between conversation participants. v1 lacked this gate
// (an artifact of an early "tip jar" framing that never materialized into a
// working flow), letting PCs tip a beggar from across the village. PR B
// fixes that — proximity matters for payments the same way it matters for
// speech.
func Pay(buyerID ActorID, recipientName string, amount int, forText string, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			// Re-validate amount inside the Command Fn — Pay is exported,
			// so non-handler callers (tests, admin paths) could otherwise
			// pass amount<=0 (mint coins via negative-amount underflow on
			// the buyer side) or amount>MaxInt32 (silent int32 wrap in
			// any future int32 ledger column). Both rejected at decode
			// for the handler path; defense in depth here.
			if amount < 1 {
				return nil, fmt.Errorf("Pay: amount must be at least 1 (got %d)", amount)
			}
			if amount > MaxPayAmount {
				return nil, fmt.Errorf("Pay: amount exceeds maximum (got %d, max %d)", amount, MaxPayAmount)
			}

			buyer, ok := w.Actors[buyerID]
			if !ok {
				return nil, fmt.Errorf("Pay: buyer %q not in world", buyerID)
			}
			if buyer.MoveIntent != nil {
				return nil, errors.New(
					"you are walking — finish your move before paying. " +
						"Either pay BEFORE the move_to, or wait until you arrive.",
				)
			}
			if buyer.CurrentHuddleID == "" {
				return nil, errors.New(
					"you're not in a conversation — start one with the person you want to pay first.",
				)
			}

			// Resolve seller against huddle peers only. Tighter than v1's
			// village-wide name lookup AND eliminates cross-village collisions
			// (two NPCs named "John" in different rooms can't be confused).
			// Ambiguity (two co-huddled peers with case-insensitive equal
			// DisplayName) → reject: money transfers must not pick a
			// recipient non-deterministically.
			sellerID, ok, ambiguous := findHuddlePeerByDisplayName(w, buyerID, buyer.CurrentHuddleID, recipientName)
			if ambiguous {
				return nil, fmt.Errorf(
					"more than one person named %q is in this conversation — use a unique full name before paying.",
					recipientName,
				)
			}
			if !ok {
				return nil, fmt.Errorf(
					"no one named %q in this conversation — re-check who is here before paying.",
					recipientName,
				)
			}
			if sellerID == buyerID {
				// Defensive — findHuddlePeerByDisplayName excludes the buyer
				// from the peer scan, so this can only fire if the peer set
				// invariant ever drifts. Cheap to keep.
				return nil, errors.New("you cannot pay yourself")
			}
			seller, ok := w.Actors[sellerID]
			if !ok {
				return nil, fmt.Errorf("Pay: seller %q vanished mid-resolve", sellerID)
			}

			if buyer.Coins < amount {
				return nil, fmt.Errorf(
					"insufficient coins (have %d, need %d) — agree on a lower amount before paying.",
					buyer.Coins, amount,
				)
			}
			// Seller balance overflow guard. amount is bounded by
			// MaxPayAmount (MaxInt32), but seller.Coins is `int` so on a
			// platform where int >= int64 the sum can still wrap into
			// negative territory if seller already holds a near-MaxInt
			// balance. Theoretical at current village scale, but mint-
			// path-adjacent — a wrapped negative balance is the same
			// failure mode as the amount<1 path the validation above
			// covers.
			if seller.Coins > math.MaxInt-amount {
				return nil, fmt.Errorf(
					"Pay: would overflow seller balance (have %d, adding %d)",
					seller.Coins, amount,
				)
			}

			// Transfer. Single-threaded on the world goroutine, so the two
			// updates are atomic by construction — no FOR UPDATE locks
			// needed like v1's executePayTransfer.
			buyer.Coins -= amount
			seller.Coins += amount

			// Emit the Paid event. World.emit stamps EventID + RootEventID
			// and dispatches subscribers synchronously inside the world
			// goroutine.
			w.emit(&Paid{
				BuyerID:  buyerID,
				SellerID: sellerID,
				Amount:   amount,
				ForText:  forText,
				At:       at,
			})

			// Bidirectional relationship writes. Texts mirror v1's
			// recordPayInteractions: first person from each actor's POV,
			// optional ForText folded in.
			buyerName := buyer.DisplayName
			sellerName := seller.DisplayName
			buyerFact := payFactText("I", "paid", sellerName, amount, forText)
			sellerFact := payFactText(buyerName, "paid", "me", amount, forText)
			if _, err := RecordInteraction(buyerID, sellerID, InteractionPaid, buyerFact, at).Fn(w); err != nil {
				log.Printf("sim.Pay: RecordInteraction buyer→seller %q→%q: %v", buyerID, sellerID, err)
			}
			if _, err := RecordInteraction(sellerID, buyerID, InteractionPaidBy, sellerFact, at).Fn(w); err != nil {
				log.Printf("sim.Pay: RecordInteraction seller→buyer %q→%q: %v", sellerID, buyerID, err)
			}
			return nil, nil
		},
	}
}

// findHuddlePeerByDisplayName resolves a case-insensitive DisplayName to a
// peer ActorID within the buyer's huddle. The buyer is excluded from the
// scan so "pay yourself" with the buyer's own name reads as "no one named X"
// rather than silently matching — keeps the error message accurate to the
// model's intent (a self-pay attempt looks the same as a typo of another
// peer's name).
//
// Trailing whitespace on the lookup string is tolerated — the handler
// already trims, but defense in depth keeps the lookup robust to a future
// caller that forgets. Case-insensitive match uses `strings.EqualFold`,
// the Unicode-aware standard comparison (handles Turkic I, German ß, etc.
// correctly and avoids allocating lowercased copies of each peer name).
//
// Returns (sellerID, ok, ambiguous):
//
//   - (id, true, false)  — single match found
//   - ("", false, false) — no match (recipient not in huddle / typo)
//   - ("", false, true)  — TWO OR MORE peers share this name; the caller
//     rejects with an "ambiguous" error rather than picking a recipient
//     non-deterministically. Money-transfer paths must not be ambiguous;
//     village-scale data has unique display names so this is currently
//     theoretical, but the cost of guarding is zero.
func findHuddlePeerByDisplayName(w *World, buyerID ActorID, huddleID HuddleID, name string) (ActorID, bool, bool) {
	target := strings.TrimSpace(name)
	if target == "" {
		return "", false, false
	}
	members, ok := w.actorsByHuddle[huddleID]
	if !ok {
		return "", false, false
	}
	var found ActorID
	for peerID := range members {
		if peerID == buyerID {
			continue
		}
		peer, ok := w.Actors[peerID]
		if !ok {
			continue
		}
		if strings.EqualFold(peer.DisplayName, target) {
			if found != "" {
				return "", false, true
			}
			found = peerID
		}
	}
	if found == "" {
		return "", false, false
	}
	return found, true, false
}

// payFactText renders the SalientFact text for a pay write. Both sides use
// the same shape — the caller supplies the subject ("I" / buyer name) and
// the object ("seller name" / "me"). ForText is folded in as " for {trim}"
// when non-empty (handler-trimmed already, but defensive).
//
//	payFactText("I",       "paid", "Ezekiel", 5, "")    → "I paid Ezekiel 5 coins."
//	payFactText("Hannah",  "paid", "me",      5, "ale") → "Hannah paid me 5 coins for ale."
func payFactText(subject, verb, object string, amount int, forText string) string {
	for_ := strings.TrimSpace(forText)
	coins := "coins"
	if amount == 1 {
		coins = "coin"
	}
	if for_ == "" {
		return fmt.Sprintf("%s %s %s %d %s.", subject, verb, object, amount, coins)
	}
	return fmt.Sprintf("%s %s %s %d %s for %s.", subject, verb, object, amount, coins, for_)
}
