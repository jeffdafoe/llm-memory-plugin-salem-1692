package sim

import (
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"
)

// pay_with_item_commands.go — Phase 3 PR S4 step 5.
//
// Five Command Fns that drive the buyer-initiated pay-with-item commerce
// flow on top of the PR S4 substrate (pay_ledger.go +
// events_pay_with_item.go) and the PR S3 scene_quote substrate. Mirrors
// the established pattern from PR B (Pay) and PR S3 (SceneQuoteCreate):
// every Command Fn re-validates everything its handler did, mutates on
// the world goroutine, emits events, and kicks off relationship writes
// via RecordInteraction.
//
// Design spec:
//   - shared/tasks/engine-in-memory-rewrite/pay-with-item-architecture-design
//   - shared/tasks/engine-in-memory-rewrite/pay-ledger-substrate-design
//   - shared/tasks/engine-in-memory-rewrite/scene-quote-design § 8 (fast-path)
//
// PR S4 step-5 design locks (settled 2026-05-16 EOS-26):
//
//   - One PR — substrate + Command Fns + handlers + subscribers + sweep
//     ship together. No S4a/S4b split.
//
//   - sim.AcceptPay revalidates 10 gates first-failure-wins, in this
//     order: auth → state → TTL → co-presence (huddle, both sides) →
//     seller break → ItemKind catalog → consumer departure → stock →
//     funds. Auth + state are idempotent rejects (tool error, NO
//     transition). The other eight all drive a terminal flip (expired,
//     failed_unavailable, failed_insufficient_stock,
//     failed_insufficient_funds).
//
//   - No structure-level closed-shop check inside AcceptPay. Movement
//     layer's EntryPolicy + huddle co-presence already enforces that —
//     a buyer not allowed inside the seller's shop can't share the
//     seller's huddle, and the co-presence gate catches it.
//
//   - PayCountered owns the countered transition (separate event family
//     from PayWithItemResolved). Per architecture § 10 + ledger-substrate
//     § 6 ambiguity resolved in favor of separate events at EOS-26:
//     PayCountered fires on counter; PayWithItemResolved fires on every
//     other terminal. PayTerminalStateCountered exists for type
//     completeness but PayWithItemResolved is never emitted with that
//     value.
//
//   - withdraw_pay ships in S4 — buyer-callable, pending-only, no
//     co-presence required, optional message reuses entry.Message.
//
//   - in_response_to chain validation lives in PayWithItem (not the
//     handler): parent exists, parent State == countered, same buyer +
//     seller, parent.ResolvedAt within PayLedgerInResponseToWindow,
//     parent has no pre-existing child (O(N) scan over World.PayLedger;
//     defer reverse index per ledger-substrate § 6).
//
// Fast-path quote matching (scene-quote-design § 8) runs inside
// PayWithItem when quote_id != 0: six predicates, any failure is a
// strict-reject tool error, NO silent fall-through to the slow path.
// On all-pass: ledger entry minted ALREADY in Accepted state (skips
// Pending), atomic transfer fires, PayWithItemResolved emits with
// TerminalState=Accepted.

// MaxPayWithItemAmount caps the offered total in coins. Same ceiling as
// MaxPayAmount and MaxSceneQuoteAmount so handler-side bounds, fast-path
// matching, and downstream pg-impl int4 columns all share one mental
// model. Re-enforced inside each Command Fn — PayWithItem / AcceptPay /
// CounterPay are exported and non-handler callers (tests, admin paths,
// future cascades) could otherwise pass an unbounded amount.
const MaxPayWithItemAmount = math.MaxInt32

// MaxPayWithItemQty caps qty-per-consumer. Mirrors MaxConsumeQty and
// MaxSceneQuoteQty. The Command Fn additionally enforces that
// Qty * effectiveConsumerCount doesn't overflow int before the stock
// check uses the product.
const MaxPayWithItemQty = math.MaxInt32

// MaxPayWithItemConsumers caps len(ConsumerIDs) on a group order.
// Matches SceneQuoteMaxConsumers so the consumer-set equality predicate
// in the fast path doesn't have to special-case quote-side vs offer-side
// caps. Architecture § 9 caps at 8 — small enough to keep per-tick
// prompt cost bounded, generous enough for "round of ale at the tavern."
const MaxPayWithItemConsumers = SceneQuoteMaxConsumers

// MaxPayWithItemPayItems caps len(PayItems) on a barter offer — the
// distinct goods lines a buyer may pay WITH (or a seller may demand in a
// counter). ZBBS-HOME-393. 8 matches MaxPayWithItemConsumers: small
// enough to keep the seller's offer-decision prompt line bounded,
// generous enough for a realistic mixed-goods payment.
const MaxPayWithItemPayItems = 8

// PayItemInput is one goods line on a barter offer as it arrives from the
// tool layer: a free-text item NAME (resolved to a canonical ItemKind
// inside the Command Fn via resolveItemKind) and a positive quantity.
// The Command turns a []PayItemInput into the resolved []ItemKindQty it
// stores on the entry. ZBBS-HOME-393.
type PayItemInput struct {
	Item string
	Qty  int
}

// MaxPayMessageRunes caps the rune length of model-controlled free text
// stored on PayLedgerEntry.Message — counter message, decline reason,
// withdraw note. 220 runes matches MaxSalientFactTextLen so a counter /
// decline / withdraw message can be included whole in a downstream
// SalientFact write without secondary truncation.
const MaxPayMessageRunes = 220

// PayLedgerInResponseToWindow bounds how far back a parent ledger entry
// may have been countered for a buyer's in_response_to follow-up to be
// accepted. Architecture § 7's "~1 hour" — long enough to let the buyer
// step away, think, come back; short enough that an abandoned counter
// chain doesn't resurrect across game-sessions or natural conversation
// turnover.
const PayLedgerInResponseToWindow = 1 * time.Hour

// MaxPayCounterChainDepth bounds how deep a buyer↔seller counter chain
// may go. The initial offer is depth 0; each buyer in_response_to
// response increments depth (parent.Depth + 1). A parent already at this
// depth can't be responded to, so the chain tops out at 3 rounds of
// countering — matching v1's cap (bound escalation so an LLM buyer and
// seller can't haggle unboundedly).
//
// v1 also dropped counter_pay from the seller's toolset once the chain
// reached the cap. That seller-side prompt gating belongs to the v2
// deliberation-prompt surface, which isn't built yet — so this
// buyer-side gate in validateInResponseTo is what actually bounds the
// chain today. (Without toolset gating a seller can still emit one final
// counter on a depth-cap offer, but the buyer can't respond to it, so the
// chain still terminates.)
const MaxPayCounterChainDepth = 3

// PayWithItemResult is the value returned by the PayWithItem Command Fn.
// The handler narrates LedgerID + State back to the LLM so the model has
// a stable identifier to reference in a follow-up tool call (acceptance
// awaiting, counter response via in_response_to, withdraw, etc.) and
// knows whether the offer is mid-flight (pending) or already resolved
// (fast-path accepted).
type PayWithItemResult struct {
	LedgerID LedgerID
	State    PayLedgerState
	// FastPath is true when a non-zero quote_id matched all six fast-path
	// predicates and the entry was minted in Accepted state. False for
	// slow-path (pending) entries, including those that referenced a
	// quote_id which failed any predicate (those return an error before
	// any entry is minted — no silent fall-through, scene-quote-design
	// § 8).
	FastPath bool
	// EatHereClamped is true when the buyer asked for take-home but the
	// engine forced eat-here (non-portable consumable, ZBBS-WORK-403/405).
	// Carried on the result so an LLM buyer's tool feedback can state the
	// adjusted disposition instead of leaving the model believing it
	// carried the goods off.
	EatHereClamped bool
	// Fast-path settle summary (ZBBS-HOME-436). A quote-take settles, pays,
	// and (consume_now) feeds the buyer inside this one tool call, but the
	// buyer's feedback was a bare [ok] — and the within-tick perception body
	// re-renders from the tick-start snapshot, so the buyer's felt needs
	// never legibly move. The model read "nothing happened" and re-bought to
	// the iteration budget (the Ezekiel six-meat morning, live 2026-06-12).
	// These fields let the harness voice what the settle actually did,
	// computed on the world goroutine from LIVE post-commit state. All zero
	// for slow-path (pending) entries.
	BuyerAte        int     // units the buyer themself ate now
	KeptToInventory int     // surplus units pocketed into the buyer's pack (needs-clamp)
	TookHome        bool    // physical goods handed over at accept
	Booked          bool    // lodging Order minted, awaiting keeper check-in
	SatisfiesNeed   NeedKey // primary need the consumed item satisfies ("" when n/a)
	FeltAfter       string  // buyer's post-meal felt label(s) for the item's needs; "" = sated
	// MealMinutes is the buyer's eat-here dwell duration in minutes when this
	// settle started a sit-down meal/drink (0 otherwise — take-home, immediate-
	// only items, or eating-while-walking). The slow-burn payoff is collected
	// only by staying put, so the settle feedback uses this to tell the buyer to
	// stay and finish instead of walking off and wasting it (ZBBS-WORK-409).
	MealMinutes int
	// ReroutedSellerName is the worker's display name when the model named a
	// building (its workplace) instead of the person and the engine rerouted
	// the offer to them (ZBBS-HOME-460). Empty on the common path. The harness
	// echo prefers it over the original seller arg so "bide for their answer"
	// names a person who can actually answer.
	ReroutedSellerName string
}

// PayWithItem returns the Command for the buyer-initiated pay-with-item
// commerce surface. Two paths:
//
//   - Slow path (quoteID == 0): mints a Pending PayLedgerEntry, emits
//     PayOfferReceived. The seller's reactor tick perceives the offer
//     via PayOfferWarrantReason and decides accept_pay / decline_pay /
//     counter_pay on a subsequent tick. Returns
//     {LedgerID, Pending, FastPath:false}.
//
//   - Fast path (quoteID != 0): runs the six-predicate match
//     (scene-quote-design § 8) against world state. On all-pass: mints
//     an entry directly in Accepted state, performs the atomic coin +
//     item transfer + ConsumeNow per-consumer application, emits
//     PayWithItemResolved{TerminalState: Accepted}, and any per-consumer
//     ItemConsumed events. Returns {LedgerID, Accepted, FastPath:true}.
//
//     Any predicate failure on the fast path is a STRICT-REJECT tool
//     error — the call never falls through to slow path. Buyer who
//     wanted slow-path semantics must call again with quoteID=0.
//
// in_response_to (parentID) is the counter-chain link: a non-zero
// parentID asserts this offer is the buyer's response to a previously
// countered parent ledger. Validation: parent exists, parent
// State == countered, parent.BuyerID == buyerID, parent.SellerID
// resolves to the same seller, parent.ResolvedAt within
// PayLedgerInResponseToWindow, parent has no pre-existing child (O(N)
// scan; ledger-substrate § 6 defers the reverse index).
//
// PayWithItem itself does NOT enforce parent matching on item terms —
// the buyer may legitimately change the item/qty/consume_now/consumers
// on their counter-response (architecture § 7). The seller can decline
// if the terms drift unfavorably.
func PayWithItem(
	buyerID ActorID,
	sellerName string,
	itemName string,
	qty int,
	amount int,
	consumeNow bool,
	consumerNames []string,
	payItems []PayItemInput,
	quoteID QuoteID,
	parentID LedgerID,
	forText string,
	at time.Time,
	// readyInDays is the optional advance-booking offset (ZBBS-HOME-403):
	// 0/absent = deliver/check-in today, N = book N days ahead (lodging only).
	// Variadic so the ~30 existing call sites that don't book ahead compile
	// unchanged; only the buyer-facing tool + PC routes pass it.
	readyInDays ...int,
) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			// Numeric defense. PayWithItem is exported — non-handler
			// callers could pass shapes the decode side rejects.
			//
			// Coins are now OPTIONAL (>= 0): an offer may pay with coins,
			// goods (payItems), or both, but must carry at least one of the
			// two. The "must offer something" rule is enforced after
			// payItems are validated (it also closes the free-goods hole —
			// an all-zero offer was the ZBBS-HOME-391 economy bug).
			if amount < 0 {
				return nil, fmt.Errorf("PayWithItem: amount cannot be negative (got %d)", amount)
			}
			if amount > MaxPayWithItemAmount {
				return nil, fmt.Errorf("PayWithItem: amount exceeds maximum (got %d, max %d)", amount, MaxPayWithItemAmount)
			}
			if len(payItems) > MaxPayWithItemPayItems {
				return nil, fmt.Errorf(
					"PayWithItem: too many goods lines (got %d, max %d) — combine into fewer lines.",
					len(payItems), MaxPayWithItemPayItems,
				)
			}
			if qty < 1 {
				return nil, fmt.Errorf("PayWithItem: qty must be at least 1 (got %d)", qty)
			}
			if qty > MaxPayWithItemQty {
				return nil, fmt.Errorf("PayWithItem: qty exceeds maximum (got %d, max %d)", qty, MaxPayWithItemQty)
			}
			if len(consumerNames) > MaxPayWithItemConsumers {
				return nil, fmt.Errorf(
					"PayWithItem: too many consumers (got %d, max %d) — split the order into smaller offers.",
					len(consumerNames), MaxPayWithItemConsumers,
				)
			}
			// Conflicting offer-mode guard. quote_id selects the fast-path
			// quote accept; in_response_to links this offer into a counter
			// chain. They express different lifecycle intents and the fast
			// path returns before any counter-chain handling, so it would
			// silently win — reject the ambiguous combination outright. The
			// pc/pay handler rejects this earlier (400); enforced here too
			// because NPC / tool callers reach the substrate directly.
			if quoteID != 0 && parentID != 0 {
				return nil, errors.New(
					"an offer can either accept a quote (quote_id) or respond to a counter (in_response_to), not both — drop one.",
				)
			}
			// Barter is slow-path only (ZBBS-HOME-393). A posted quote is a
			// coin-priced sale; taking it with goods has no defined match
			// semantics (the fast-path's exact-term predicate is coin-only).
			// Reject the combination outright rather than silently dropping
			// the goods.
			if quoteID != 0 && len(payItems) > 0 {
				return nil, errors.New(
					"you can't pay a posted quote with goods — a quote is a coin price. Drop quote_id to make a barter offer, or drop the goods to take the quote.",
				)
			}

			buyer, ok := w.Actors[buyerID]
			if !ok {
				return nil, fmt.Errorf("PayWithItem: buyer %q not in world", buyerID)
			}
			if buyer.MoveIntent != nil {
				return nil, errors.New(
					"you are walking — finish your move before making an offer. " +
						"Either offer BEFORE the move_to, or wait until you arrive.",
				)
			}
			if buyer.CurrentHuddleID == "" {
				return nil, errors.New(
					"you're not in a conversation — start one with the person you want to offer to first.",
				)
			}

			// Resolve scene anchor first; quote lookup needs SceneID.
			sceneID, ok := resolveSellerScene(w, buyer.CurrentHuddleID)
			if !ok {
				return nil, errors.New(
					"your current conversation isn't anchored to a scene — wait for the scene to be established before making an offer.",
				)
			}

			// Resolve seller against huddle peers — tight scope (same
			// huddle, case-insensitive, ambiguity reject). Mirrors PR B's
			// Pay.
			sellerID, ok, ambiguous := findHuddlePeerByDisplayName(w, buyerID, buyer.CurrentHuddleID, sellerName)
			if ambiguous {
				return nil, fmt.Errorf(
					"more than one person named %q is in this conversation — use a unique full name before offering.",
					sellerName,
				)
			}
			// reroutedSellerName carries the worker's display name when the
			// model named a building instead of the person (set below), so the
			// tool-result echo names the real recipient. Empty on the common
			// path. ZBBS-HOME-460.
			var reroutedSellerName string
			if !ok {
				// Reroute a workplace name to the worker — see sim.Pay's note.
				// The restock/satiation buy cues name the structure ("buy from
				// Ellis Farm"), so the model offers to the place where the
				// co-present worker is wanted.
				peerID, structureName, peerOK, peerAmbiguous := findHuddlePeerByWorkplaceName(w, buyerID, buyer.CurrentHuddleID, sellerName)
				if peerAmbiguous {
					return nil, fmt.Errorf(
						"more than one person here works at %q — name the person you want to offer to.",
						sellerName,
					)
				}
				if !peerOK {
					return nil, fmt.Errorf(
						"no one named %q in this conversation — re-check who is here before offering.",
						sellerName,
					)
				}
				sellerID = peerID
				if peer, peerExists := w.Actors[peerID]; peerExists {
					reroutedSellerName = peer.DisplayName
				}
				log.Printf("sim.PayWithItem: rerouted offer from building %q to its worker %q (buyer %q)",
					structureName, peerID, buyerID)
			}
			if sellerID == buyerID {
				return nil, errors.New("you cannot make an offer to yourself")
			}
			seller, ok := w.Actors[sellerID]
			if !ok {
				return nil, fmt.Errorf("PayWithItem: seller %q vanished mid-resolve", sellerID)
			}

			// ZBBS-WORK-412 deliberately does NOT mint here. This is the BUY
			// path (the buyer names the good to receive); unlike the sell /
			// pay-with sites, a mint wouldn't reject the same tick — PayWithItem
			// would register a pending offer the seller can't fill, recreating
			// the poisoned-ledger retry loop (salem-multi-item-pay-protocol).
			// Discovery mint is scoped to same-tick-rejecting sites: consume,
			// scene_quote, and pay_items goods (resolvePayItems).
			kind, ok := resolveItemKind(w, itemName)
			if !ok {
				return nil, fmt.Errorf(
					"unknown item kind %q — check the items available in this world before offering.",
					itemName,
				)
			}

			// ZBBS-WORK-403: a purchase of a non-portable consumable always
			// settles eat-here, clamped HERE on the world goroutine so
			// it holds regardless of what the client sent — a failed catalog
			// fetch or a direct API call must not carry stew out of the
			// tavern (the `portable` capability was seeded in the item data
			// precisely to prevent that; code_review). ZBBS-WORK-405 widened
			// the clamp from PC-only to every buyer: v1 gated take-home of
			// non-portables for all actors (v1 pay.go/serve.go rejected it
			// outright), and no valid NPC flow buys non-portables take-home
			// — such a purchase is a config bug, not a disposition to
			// preserve. Sits before the duplicate-offer gate and both settle
			// paths, so the clamped value is the offer's identity
			// everywhere. Service kinds are excluded — they clamp the OTHER
			// way (fast path, WORK-402). eatHereClamped rides the result so
			// an LLM buyer's tool feedback can say what actually happened
			// (the consume-clamp precedent: a silently adjusted action
			// leaves the model believing it carried off goods it never
			// held).
			eatHereClamped := false
			if !consumeNow && w.ItemKinds[kind].EatHereOnly() {
				consumeNow = true
				eatHereClamped = true
			}

			// Consumer resolution. Empty list = buyer is implicit single
			// consumer. Non-empty enforces huddle membership +
			// case-insensitive name resolution + duplicate rejection +
			// seller-as-consumer rejection. Buyer-as-consumer is
			// allowed.
			consumerIDs, err := resolvePayConsumers(w, buyer, sellerID, consumerNames)
			if err != nil {
				return nil, err
			}

			// Resolve the barter goods the buyer offers to pay WITH
			// (ZBBS-HOME-393). Each free-text name → canonical ItemKind;
			// duplicate kinds and non-positive qty reject. Empty for a
			// pure-coin offer.
			resolvedPayItems, err := resolvePayItems(w, payItems)
			if err != nil {
				return nil, err
			}

			// Must offer something. An offer with no coins AND no goods is
			// the free-goods hole (ZBBS-HOME-391) — reject it. Coins-only,
			// goods-only, and mixed all pass.
			if amount == 0 && len(resolvedPayItems) == 0 {
				return nil, errors.New(
					"an offer must include coins or goods — set an amount, add pay_items, or both.",
				)
			}

			// Lodging-shape intake gates (ZBBS-WORK-343 + WORK-344). Both
			// reject upfront rather than commit coin into an Order that
			// deliver_order will refuse forever (failure mode: Order stays
			// Ready, keeper LLM burns ticks retrying).
			//
			// Keyed on the "lodging" capability rather than hardcoded
			// "nights_stay" so a future operator-defined lodging kind
			// inherits both guards. Matches deliver_order's own capability
			// check at order_commands.go.
			if itemHasCapability(w, kind, "lodging") {
				// WORK-343 — operator-data guard. A keeper whose
				// work_structure has zero private bedrooms (or no work
				// structure at all) is structurally unable to fulfill any
				// lodging Order. Distinct from "all rooms occupied" — that
				// case is transient and stays at delivery time
				// (AssignBedroomForLodger returns RoomID=0 → "try again
				// shortly"). Zero-rooms is the deliberate v1 scope.
				if seller.WorkStructureID == "" {
					return nil, fmt.Errorf(
						"%s has no work structure set up for lodging — ask an operator to fix.",
						seller.DisplayName,
					)
				}
				sellerStructure, ok := w.Structures[seller.WorkStructureID]
				if !ok {
					return nil, fmt.Errorf(
						"%s's work structure %q not found — ask an operator to fix.",
						seller.DisplayName, seller.WorkStructureID,
					)
				}
				privateRoomCount := 0
				for _, r := range sellerStructure.Rooms {
					if r != nil && r.Kind == RoomKindPrivate {
						privateRoomCount++
					}
				}
				if privateRoomCount == 0 {
					return nil, fmt.Errorf(
						"%s isn't set up for boarding — no bedrooms in their establishment for %s. Ask an operator to add rooms before booking here.",
						seller.DisplayName, kind,
					)
				}

				// WORK-344 — lodging take-home with non-buyer consumers
				// is a guaranteed-impossible Order: deliver_order's
				// lodging branch (order_commands.go) enforces single-self
				// consumer. The redundant consumerNames=[buyer] case is
				// permitted; only non-buyer consumers are rejected.
				// consume_now is incoherent for lodging service items but
				// not a fulfillment-impossibility, so left alone.
				if !consumeNow {
					for _, cid := range consumerIDs {
						if cid != buyerID {
							return nil, fmt.Errorf(
								"%s can't be booked for someone else — only the buyer can take the room (drop the consumers list).",
								kind,
							)
						}
					}

					// ZBBS-HOME-403: a deferred lodging booking must be paid in
					// coins. A barter (pay_items) leg can't be reversed on expiry —
					// the Order carries only the coin Amount, not the goods — so an
					// expired goods-paid booking couldn't refund them. Reject up
					// front rather than mint an unrefundable order. Mixed coin+goods
					// is rejected too: the goods leg would still be lost.
					if len(resolvedPayItems) > 0 {
						return nil, errors.New(
							"a room booking must be paid in coins — set an amount instead of offering goods (pay_items).",
						)
					}
				}
			}

			// Resolve the booked date (ZBBS-HOME-403). Advance booking
			// (ready_in_days > 0) is lodging-only — a physical good is handed
			// over when paid for, so a future date would just strand the
			// order — and a counter-response carries the parent's booked date
			// forward (a price haggle never moves the date the buyer asked
			// for). Validated here, before any entry is staked.
			if len(readyInDays) > 1 {
				return nil, errors.New("PayWithItem: at most one ready_in_days value may be passed")
			}
			days := 0
			if len(readyInDays) == 1 {
				days = readyInDays[0]
			}
			// Advance booking requires a deferred order to hold the future date;
			// a consume_now (eat/check-in-on-the-spot) offer mints no Order, so a
			// future ready_in_days would be silently lost. Reject it (days==0 is
			// fine — that's same-day). ZBBS-HOME-403.
			if consumeNow && days > 0 {
				return nil, errors.New(
					"ready_in_days needs consume_now=false — an advance booking is a deferred order, not taken on the spot.",
				)
			}
			// A consume_now offer mints no Order to hold a booked date, so it is
			// always same-day — never carry a parent's future booking onto it
			// (that would write a future ReadyBy onto an accepted entry with no
			// order waiting for it). Only the deferred path resolves carry/advance.
			readyBy := orderDateUTC(at, w.Settings.Location)
			if !consumeNow {
				readyBy, err = resolveOrderReadyBy(w, kind, parentID, days, at)
				if err != nil {
					return nil, err
				}
			}

			// Overflow guard on qty * effectiveConsumers — Inventory[kind]
			// is plain int, so a wrapped product could silently pass the
			// stock check.
			effectiveConsumers := effectivePayConsumerCount(consumerIDs)
			if qty > math.MaxInt/effectiveConsumers {
				return nil, fmt.Errorf(
					"PayWithItem: qty %d × %d consumers overflows int — split the order.",
					qty, effectiveConsumers,
				)
			}
			needed := qty * effectiveConsumers

			// in_response_to validation (architecture § 7). Doesn't
			// require the buyer to keep matching terms — they can shift
			// qty/item/consume_now if they want their counter-response
			// to propose different terms.
			if parentID != 0 {
				if err := validateInResponseTo(w, parentID, buyerID, sellerID, at); err != nil {
					return nil, err
				}
			}

			// Fast-path quote matching (scene-quote-design § 8). All six
			// predicates must pass; any failure is a strict-reject tool
			// error.
			if quoteID != 0 {
				res, fastErr := runPayWithItemFastPath(
					w, buyer, seller, sellerID, sceneID, kind, qty,
					amount, consumeNow, consumerIDs, needed, quoteID,
					parentID, forText, at, readyBy,
				)
				if fastErr != nil {
					return nil, fastErr
				}
				return stampEatHereClamped(res, eatHereClamped), nil
			}

			// Opportunistic quote auto-match (ZBBS-HOME-424). The explicit
			// fast path requires the model to echo a quote_id, but a weak
			// buyer model routinely answers a seller's standing quote with a
			// bare pay_with_item — minting a CROSSING offer for goods the
			// seller is already offering. The two pending intents then
			// deadlock: the seller's quote waits on the buyer, the buyer's
			// offer waits on the seller, and the duplicate gate below
			// rejects every retry (observed live: nine minutes to settle a
			// 4-coin water, conversation hud-6c849d…). When a bare coin
			// offer matches an open quote on every term predicate, take the
			// quote instead of minting its mirror image. Barter offers are
			// exempt (a quote is a coin price — ZBBS-HOME-393), as are
			// counter-responses (their lifecycle is the counter chain). On
			// any fast-path failure the offer falls through to the slow
			// path unchanged — runPayWithItemFastPath mutates nothing
			// before all predicates pass, and the strict-reject contract
			// only binds an EXPLICIT quote_id.
			if parentID == 0 && len(resolvedPayItems) == 0 {
				if matchID := findAutoMatchQuote(w, buyer, sellerID, sceneID, kind, qty, amount, consumeNow, consumerIDs, at); matchID != 0 {
					res, fastErr := runPayWithItemFastPath(
						w, buyer, seller, sellerID, sceneID, kind, qty,
						amount, consumeNow, consumerIDs, needed, matchID,
						parentID, forText, at, readyBy,
					)
					if fastErr == nil {
						withdrawCrossingOffers(w, buyerID, sellerID, sceneID, kind, qty, consumeNow, consumerIDs, at)
						return stampEatHereClamped(res, eatHereClamped), nil
					}
				}
			}

			// Cross-tick duplicate-offer gate (ZBBS-WORK-391). The same-tick
			// repeat-offer guard (ZBBS-HOME-395, harness) resets every tick by
			// design, and the buyer-side pending-offers cue (ZBBS-HOME-413) is
			// perception-only — a weak model reads "make no second offer for
			// the same goods while this one stands" and offers again anyway (observed
			// live: Prudence staked three identical meat offers across three
			// ticks while the first sat unanswered, then ate the accepted
			// duplicates back-to-back). This is the ledger-backed rung: a NEW
			// offer matching a still-Pending entry on (buyer, seller, item,
			// disposition) is rejected model-facing. The key deliberately
			// mirrors payOfferKey — price and qty excluded, so a re-offer at
			// drifted terms still matches; counter-responses (parentID != 0)
			// are a distinct lifecycle and exempt, and quote-takes never reach
			// the slow path. Entries past ExpiresAt are skipped rather than
			// blocking the buyer on the sweep's cadence.
			if parentID == 0 {
				for _, e := range w.PayLedger {
					if e == nil || e.State != PayLedgerStatePending {
						continue
					}
					if e.BuyerID != buyerID || e.SellerID != sellerID || e.ItemKind != kind || e.ConsumeNow != consumeNow {
						continue
					}
					// A zero ExpiresAt (legacy/seeded entry) is treated as
					// never-expiring rather than always-expired — the gate
					// must not wave a duplicate through just because an entry
					// predates TTL stamping.
					if !e.ExpiresAt.IsZero() && !at.Before(e.ExpiresAt) {
						continue
					}
					return nil, fmt.Errorf(
						"you already have an offer for %s before %s, awaiting their answer (offer id %d) — wait for their response, or withdraw_pay it before offering again.",
						kind, seller.DisplayName, e.ID,
					)
				}
			}

			// Slow path: mint pending entry + emit PayOfferReceived.
			//
			// Offer-time funds fast-fail (ZBBS-WORK-231) — an
			// OPTIMIZATION, not a correctness gate. Pending entries
			// reserve nothing and AcceptPay's gate 11 stays the
			// authoritative funds check at acceptance time. But minting a
			// pending offer the buyer demonstrably can't cover wastes a
			// seller deliberation tick: the offer stamps the seller's
			// warrant, the seller's reactor burns an LLM round-trip, and
			// accept_pay then resolves to failed_insufficient_funds.
			// Rejecting here spares that tick. This is a point-in-time
			// snapshot — the buyer's balance can still change before
			// accept — so it neither replaces nor weakens gate 11.
			//
			// Stock and seller-break are deliberately NOT fast-failed
			// here: stock is contended (reservation accounting lives at
			// accept) and break is transient, so both stay deferred to
			// acceptance. A pending offer staked against an on-break or
			// out-of-stock seller is harmless — it resolves at accept
			// time, or is withdrawn / expires first.
			if err := payOfferShortfall(buyer, amount, qty, resolvedPayItems); err != nil {
				return nil, err
			}

			id := w.nextLedgerSeq()
			ttl := effectivePayLedgerTTL(w.Settings)
			expiresAt := at.Add(ttl)
			depth, parentRefForLineage := 0, LedgerID(0)
			if parentID != 0 {
				if parent := w.PayLedger[parentID]; parent != nil {
					depth = parent.Depth + 1
					parentRefForLineage = parent.ID
				}
			}
			entry := &PayLedgerEntry{
				ID:          id,
				BuyerID:     buyerID,
				SellerID:    sellerID,
				ItemKind:    kind,
				Qty:         qty,
				ConsumeNow:  consumeNow,
				ConsumerIDs: append([]ActorID(nil), consumerIDs...),
				ReadyBy:     readyBy,
				Amount:      amount,
				PayItems:    cloneItemKindQtys(resolvedPayItems),
				QuoteID:     0, // slow path didn't reference a quote
				ParentID:    parentRefForLineage,
				Depth:       depth,
				State:       PayLedgerStatePending,
				CreatedAt:   at,
				ExpiresAt:   expiresAt,
				SceneID:     sceneID,
				HuddleID:    buyer.CurrentHuddleID,
			}
			w.PayLedger[id] = entry

			evt := &PayOfferReceived{
				LedgerID:       id,
				BuyerID:        buyerID,
				SellerID:       sellerID,
				ItemKind:       kind,
				QtyPerConsumer: qty,
				ConsumeNow:     consumeNow,
				ConsumerIDs:    cloneActorIDs(consumerIDs),
				Amount:         amount,
				PayItems:       cloneItemKindQtys(resolvedPayItems),
				QuoteID:        0,
				ParentID:       parentRefForLineage,
				Depth:          depth,
				SceneID:        sceneID,
				HuddleID:       buyer.CurrentHuddleID,
				ExpiresAt:      expiresAt,
				At:             at,
			}
			w.emit(evt)
			entry.RootEventID = evt.RootEventID()
			entry.SourceEventID = evt.EventID()

			return PayWithItemResult{
				LedgerID:           id,
				State:              PayLedgerStatePending,
				FastPath:           false,
				EatHereClamped:     eatHereClamped,
				ReroutedSellerName: reroutedSellerName,
			}, nil
		},
	}
}

// stampEatHereClamped marks a settle-path result with the upstream
// disposition clamp (ZBBS-WORK-405) so tool feedback can state what
// actually happened. The fast path doesn't know about the clamp — it
// received the already-clamped consumeNow — so the stamp happens at the
// call sites that do. No-op when nothing was clamped or the value isn't
// a PayWithItemResult.
func stampEatHereClamped(res any, clamped bool) any {
	if !clamped {
		return res
	}
	if r, ok := res.(PayWithItemResult); ok {
		r.EatHereClamped = true
		return r
	}
	return res
}

// runPayWithItemFastPath validates the six fast-path predicates and, on
// all-pass, mints the entry directly in Accepted state + commits the
// atomic transfer + emits PayWithItemResolved{Accepted}. Any predicate
// failure is a strict-reject tool error (scene-quote-design § 8 — "no
// silent fall-through").
//
// The six predicates:
//
//  1. Quote takeable. Quote exists, State == Active, not past
//     ExpiresAt.
//  2. Buyer eligible. Quote is public OR explicitly targets this
//     buyer.
//  3. Co-presence. Buyer's scene matches quote's scene, AND buyer +
//     seller share a non-empty huddle.
//  4. Exact term match. ItemKind, Qty, and consumer set
//     (order-independent) all agree. ConsumeNow is deliberately NOT
//     matched (ZBBS-WORK-402): disposition is the buyer's term — the
//     buyer's value rides the entry, and service kinds clamp to the
//     service shape.
//  5. Amount at-or-above floor. Quote's Amount is the minimum;
//     overpayment is tipping.
//  6. Independent preconditions — seller stock, buyer coins, seller
//     not on break. These are also revalidated by the slow-path
//     acceptance flow; centralizing here keeps the fast path
//     symmetric.
func runPayWithItemFastPath(
	w *World,
	buyer, seller *Actor,
	sellerID ActorID,
	sceneID SceneID,
	kind ItemKind,
	qty int,
	amount int,
	consumeNow bool,
	consumerIDs []ActorID,
	needed int,
	quoteID QuoteID,
	parentID LedgerID,
	forText string,
	at time.Time,
	readyBy time.Time,
) (any, error) {
	quote, ok := w.Quotes[quoteID]
	if !ok || quote == nil {
		return nil, fmt.Errorf("quote %d not available", quoteID)
	}
	if quote.State != SceneQuoteStateActive {
		return nil, fmt.Errorf("quote %d is no longer active", quoteID)
	}
	if !quote.ExpiresAt.IsZero() && !at.Before(quote.ExpiresAt) {
		return nil, fmt.Errorf("quote %d expired", quoteID)
	}

	if quote.TargetBuyer != "" && quote.TargetBuyer != buyer.ID {
		return nil, fmt.Errorf("quote %d is not for you", quoteID)
	}

	if quote.SellerID != sellerID {
		return nil, fmt.Errorf(
			"quote %d is from a different seller — drop the quote_id or call the right seller.",
			quoteID,
		)
	}

	if buyer.CurrentHuddleID == "" || seller.CurrentHuddleID == "" ||
		buyer.CurrentHuddleID != seller.CurrentHuddleID {
		return nil, fmt.Errorf(
			"need to be in %s's huddle to take quote %d",
			seller.DisplayName, quoteID,
		)
	}
	if sceneID != quote.SceneID {
		return nil, fmt.Errorf(
			"you're not in the same scene as quote %d — get back to the conversation that quote was posted in.",
			quoteID,
		)
	}

	// Exact term match. Consumer set comparison is order-independent
	// (matches the supersede key in scene_quote_commands.go).
	if quote.ItemKind != kind {
		return nil, fmt.Errorf(
			"quote %d has different terms: item %q, not %q",
			quoteID, quote.ItemKind, kind,
		)
	}
	if quote.Qty != qty {
		return nil, fmt.Errorf(
			"quote %d has different terms: qty %d, not %d",
			quoteID, quote.Qty, qty,
		)
	}
	// Disposition is the BUYER's term, not the quote's (ZBBS-WORK-402 —
	// the quote's ConsumeNow survives as the UI row default, but a take no
	// longer has to match it: whether goods are consumed on the spot or
	// carried home is the buyer's call, same as the slow path's
	// buyer-supplied consume_now). Service kinds have no choice — no
	// physical good exists, so the engine forces the service shape rather
	// than rejecting a confused caller (mirrors the client's forced
	// handling for bookings, HOME-423).
	if itemHasCapability(w, kind, "service") {
		consumeNow = false
	}
	if !actorIDSetsEqual(quote.ConsumerIDs, consumerIDs) {
		return nil, fmt.Errorf(
			"quote %d has different consumer set — re-check who the quote is for.",
			quoteID,
		)
	}

	if amount < quote.Amount {
		return nil, fmt.Errorf(
			"quote %d requires at least %d coins (you offered %d)",
			quoteID, quote.Amount, amount,
		)
	}

	// Independent preconditions — symmetric with AcceptPay.
	if seller.BreakUntil != nil && seller.BreakUntil.After(at) {
		return nil, fmt.Errorf(
			"%s is on break — try again after their break ends.",
			seller.DisplayName,
		)
	}
	// Stock reservation accounting (PR S6 R1 code_review fix): post-S6,
	// accepted-but-not-yet-delivered Orders keep goods in the seller's
	// inventory. The visible Inventory doesn't reflect those obligations,
	// so we subtract outstandingReadyOrderQty before comparing against
	// `needed` — otherwise two concurrent fast-path accepts against the
	// same physical stew could both pass and only one could deliver.
	//
	// "service"-capability items (e.g. nights_stay) carry no inventory —
	// they're infinite-stock, so the stock check is skipped (ZBBS-HOME-296).
	// Must match the slow-path skip in acceptPendingOffer's gate 10 so a
	// service item that fast-paths can't later hit a stock reject there.
	if !itemHasCapability(w, kind, "service") {
		reserved := outstandingReadyOrderQty(w, seller.ID, kind)
		available := seller.Inventory[kind] - reserved
		if available < needed {
			// ZBBS-HOME-363: the buyer walked here and found the seller dry on
			// this item. This quote-payment fast-path rejects with a bare error
			// and emits NO PayWithItemResolved (no ledger entry), so the
			// out-of-stock subscriber would miss it — record the experiential
			// memory inline through the shared recorder.
			noteOutOfStock(w, buyer.ID, seller.ID, kind, at)
			return nil, fmt.Errorf(
				"%s doesn't have enough %s (have %d, reserved %d, need %d)",
				seller.DisplayName, kind, seller.Inventory[kind], reserved, needed,
			)
		}
	}
	if !buyerCanAfford(buyer, amount) {
		return nil, fmt.Errorf(
			"insufficient coins (have %d, need %d) — quote a smaller offer.",
			buyer.Coins, amount,
		)
	}
	if seller.Coins > math.MaxInt-amount {
		return nil, fmt.Errorf(
			"PayWithItem: would overflow seller balance (have %d, adding %d)",
			seller.Coins, amount,
		)
	}

	// All six predicates passed. Mint entry directly in Accepted state
	// (skips Pending). The entry's CreatedAt and ResolvedAt are both
	// `at` to capture the fast-path's single-instant resolution.
	id := w.nextLedgerSeq()
	depth, parentRefForLineage := 0, LedgerID(0)
	if parentID != 0 {
		if parent := w.PayLedger[parentID]; parent != nil {
			depth = parent.Depth + 1
			parentRefForLineage = parent.ID
		}
	}
	entry := &PayLedgerEntry{
		ID:          id,
		BuyerID:     buyer.ID,
		SellerID:    sellerID,
		ItemKind:    kind,
		Qty:         qty,
		ConsumeNow:  consumeNow,
		ConsumerIDs: append([]ActorID(nil), consumerIDs...),
		ReadyBy:     readyBy,
		Amount:      amount,
		QuoteID:     quoteID,
		ParentID:    parentRefForLineage,
		Depth:       depth,
		State:       PayLedgerStateAccepted,
		CreatedAt:   at,
		ResolvedAt:  at,
		// ExpiresAt left zero — entry is already terminal, sweep skips
		// non-pending entries (pay_ledger_sweep.go in step 8). The TTL
		// concept doesn't apply to a fast-path accept.
		SceneID:  sceneID,
		HuddleID: buyer.CurrentHuddleID,
	}
	w.PayLedger[id] = entry

	// Atomic transfer. Coin debit first (smaller blast radius if a
	// subsequent step somehow drifts), then item movement + ConsumeNow
	// application + relationship writes. All on the world goroutine,
	// serialized by construction — no rollback needed.
	out, err := commitPayTransfer(w, buyer, seller, entry, at, forText)
	if err != nil {
		// Theoretically unreachable — predicates 6 covered every
		// mutation failure mode. If it ever fires, that's a bug, not
		// a domain error.
		return nil, fmt.Errorf("PayWithItem fast-path transfer: %w", err)
	}

	// Emit PayWithItemResolved{Accepted}. Fast path skips
	// PayOfferReceived because the offer never sat pending (architecture
	// § 4 + events_pay_with_item.go).
	evt := &PayWithItemResolved{
		LedgerID:       id,
		BuyerID:        buyer.ID,
		SellerID:       sellerID,
		ItemKind:       kind,
		QtyPerConsumer: qty,
		ConsumeNow:     consumeNow,
		ConsumerIDs:    cloneActorIDs(consumerIDs),
		Amount:         amount,
		TerminalState:  PayTerminalStateAccepted,
		BuyerTookQuote: true, // ZBBS-WORK-420: this IS the instant quote-take path
		SceneID:        sceneID,
		HuddleID:       buyer.CurrentHuddleID,
		At:             at,
	}
	w.emit(evt)
	entry.RootEventID = evt.RootEventID()
	entry.SourceEventID = evt.EventID()

	return PayWithItemResult{
		LedgerID:        id,
		State:           PayLedgerStateAccepted,
		FastPath:        true,
		BuyerAte:        out.buyerAte,
		KeptToInventory: out.keptToInventory,
		TookHome:        out.tookHome,
		Booked:          out.booked,
		SatisfiesNeed:   out.satisfiesNeed,
		FeltAfter:       out.feltAfter,
		MealMinutes:     out.mealMinutes,
	}, nil
}

// AcceptPay returns the Command for the seller's accept-pending-offer
// path. Phase 3 PR S4 step 5. Runs the 10-gate revalidation matrix
// (active-work § Settled at EOS-26) first-failure-wins, then commits
// the atomic transfer + relationship writes + warrant-stamping event.
//
// The 10 gates, in evaluation order:
//
//  1. Caller exists in the world.
//  2. Ledger entry exists.
//  3. Caller == entry.SellerID (auth — idempotent reject, NO transition).
//  4. entry.State == Pending (state — idempotent reject, NO transition).
//  5. now < entry.ExpiresAt (TTL — flip to Expired terminal).
//  6. Buyer + seller still in entry.HuddleID (co-presence — flip to
//     failed_unavailable). Both halves checked.
//  7. seller.BreakUntil <= now (break — flip to failed_unavailable).
//  8. w.ItemKinds[entry.ItemKind] still present (catalog — flip to
//     failed_unavailable).
//  9. All entry.ConsumerIDs still in entry.HuddleID (consumer
//     departure — flip to failed_unavailable). Skipped when no
//     consumers were specified (buyer-as-implicit-consumer covered by
//     the co-presence gate).
//  10. seller.Inventory[ItemKind] >= Qty * effectiveConsumerCount
//     (stock — flip to failed_insufficient_stock).
//  11. buyer.Coins >= entry.Amount (funds — flip to
//     failed_insufficient_funds).
//
// (Gates 10-11 are stock-first / funds-second per active-work — seller-
// knowable state checked first on the seller's tick. The "10 gate"
// nomenclature collapses the auth + state idempotent rejects together;
// the breakdown above expands them for implementation clarity.)
//
// On all-pass: atomic coin debit + item transfer + ConsumeNow per-
// consumer apply, flip entry.State to Accepted, emit
// PayWithItemResolved{Accepted}.
//
// On gate 5-11 fail: flip entry to the specific terminal state, emit
// PayWithItemResolved with matching TerminalState. NOT
// a tool error — the gate failure IS the terminal resolution. Returns
// nil err with the PayLedgerEntry's new state so callers can inspect
// the outcome.
func AcceptPay(callerID ActorID, ledgerID LedgerID, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			// Gate 1: caller exists.
			caller, ok := w.Actors[callerID]
			if !ok {
				return nil, fmt.Errorf("AcceptPay: caller %q not in world", callerID)
			}

			// Gate 2: entry exists.
			entry, ok := w.PayLedger[ledgerID]
			if !ok || entry == nil {
				return nil, fmt.Errorf(
					"AcceptPay: ledger %d not found — re-check the ledger_id.",
					ledgerID,
				)
			}

			// Gate 3: auth (idempotent reject — NO transition).
			if entry.SellerID != callerID {
				return nil, fmt.Errorf(
					"AcceptPay: only the seller of ledger %d may accept it",
					ledgerID,
				)
			}

			// Gate 4: state idempotent reject (NO transition).
			if entry.State != PayLedgerStatePending {
				return nil, fmt.Errorf(
					"AcceptPay: ledger %d is no longer pending (currently %s) — nothing to accept.",
					ledgerID, entry.State,
				)
			}

			// Gates 5-11 + the atomic transfer / flip / emit live in
			// acceptPendingOffer, shared with CounterPay's
			// non-increasing-counter coercion (a seller counter at or
			// below the offered amount is "yes, deal" and resolves as an
			// accept). Gates 1-4 above are AcceptPay-specific (auth +
			// state idempotent rejects) and stay inline.
			state, err := acceptPendingOffer(w, caller, entry, at)
			return state, err
		},
	}
}

// acceptPendingOffer runs AcceptPay gates 5-11 (TTL, co-presence, seller
// break, ItemKind catalog, consumer departure, stock, funds) on an
// already-pending entry, first-failure-wins, then on all-pass commits
// the atomic transfer, flips the entry to Accepted, and emits
// PayWithItemResolved{Accepted}. On a gate 5-11 failure it flips the
// entry to the matching terminal and emits PayWithItemResolved with that
// state — NOT a tool error; the gate failure IS the resolution. Returns
// the entry's resulting state.
//
// The caller guarantees gates 1-4 (caller exists, entry exists,
// seller == entry.SellerID, entry.State == Pending). seller is the
// accepting party (== entry.SellerID).
//
// Shared by AcceptPay (the seller's explicit accept) and CounterPay's
// non-increasing-counter coercion. Both reach this point having already
// verified the same four preconditions, and both want identical
// accept-time semantics, so the gate matrix lives here once.
func acceptPendingOffer(w *World, seller *Actor, entry *PayLedgerEntry, at time.Time) (PayLedgerState, error) {
	// Gate 5: TTL. From here on, gate failures DRIVE terminal
	// transitions rather than idempotent rejects.
	if !entry.ExpiresAt.IsZero() && !at.Before(entry.ExpiresAt) {
		return finalizePayLedgerTerminal(w, entry, PayTerminalStateExpired, "", at), nil
	}

	// Gate 6: co-presence. Both buyer and seller must still be
	// in entry.HuddleID. (Architecture § 3 — accept requires
	// co-presence; offer creation captured HuddleID and we
	// re-check it here.)
	buyer, buyerOK := w.Actors[entry.BuyerID]
	if !buyerOK ||
		buyer.CurrentHuddleID != entry.HuddleID ||
		seller.CurrentHuddleID != entry.HuddleID ||
		entry.HuddleID == "" {
		return finalizePayLedgerTerminal(w, entry, PayTerminalStateFailedUnavailable, "", at), nil
	}

	// Gate 7: seller break (simple-strict, ledger-substrate § 11).
	if seller.BreakUntil != nil && seller.BreakUntil.After(at) {
		return finalizePayLedgerTerminal(w, entry, PayTerminalStateFailedUnavailable, "", at), nil
	}

	// Gate 8: ItemKind catalog still has this kind.
	if _, ok := w.ItemKinds[entry.ItemKind]; !ok {
		return finalizePayLedgerTerminal(w, entry, PayTerminalStateFailedUnavailable, "", at), nil
	}

	// Gate 9: consumer departure. Only relevant when a non-
	// empty consumer set was specified (buyer-as-implicit-
	// consumer is covered by gate 6's co-presence check).
	if len(entry.ConsumerIDs) > 0 {
		if !allConsumersInHuddle(w, entry.HuddleID, entry.ConsumerIDs) {
			return finalizePayLedgerTerminal(w, entry, PayTerminalStateFailedUnavailable, "", at), nil
		}
	}

	// Gate 10: stock. Skipped for "service"-capability items (e.g.
	// nights_stay), which carry no inventory — infinite-stock
	// (ZBBS-HOME-296). Must mirror the fast-path skip in
	// runPayWithItemFastPath so the two paths agree. Funds (gate 11),
	// co-presence, catalog, TTL, and counter-chain gates all still run
	// for service items — only the stock/inventory check is bypassed.
	if !itemHasCapability(w, entry.ItemKind, "service") {
		effectiveConsumers := effectivePayConsumerCount(entry.ConsumerIDs)
		// Defensive overflow guard — entry.Qty was capped at intake,
		// but a future repo could load entries with whatever shape;
		// re-check before the multiplication.
		if effectiveConsumers > 0 && entry.Qty > math.MaxInt/effectiveConsumers {
			return finalizePayLedgerTerminal(w, entry, PayTerminalStateFailedUnavailable, "", at), nil
		}
		needed := entry.Qty * effectiveConsumers
		// Stock reservation accounting (PR S6 R1 code_review fix):
		// subtract Ready-Order obligations on this seller+item so
		// two pending offers against the same physical stock cannot
		// both accept. See outstandingReadyOrderQty in order.go.
		reserved := outstandingReadyOrderQty(w, seller.ID, entry.ItemKind)
		if seller.Inventory[entry.ItemKind]-reserved < needed {
			return finalizePayLedgerTerminal(w, entry, PayTerminalStateFailedInsufficientStock, "", at), nil
		}
	}

	// Gate 11: funds. buyerCanAfford is the shared predicate; the
	// failure ACTION here is a terminal flip (an entry already
	// exists), not the tool-error reject the offer-time sites use.
	if !buyerCanAfford(buyer, entry.Amount) {
		return finalizePayLedgerTerminal(w, entry, PayTerminalStateFailedInsufficientFunds, "", at), nil
	}
	// Gate 12: barter goods (ZBBS-HOME-393). The buyer must still hold
	// every PayItem they offered to pay WITH. Like funds, this is a
	// drift backstop — the mint fast-fail already rejected an
	// uncoverable offer, but the buyer's holdings can change between
	// mint and accept. Flip to the goods-specific terminal so admin /
	// telemetry can distinguish a goods shortfall from a coin one.
	if !buyerHoldsPayItems(buyer, entry.PayItems) {
		return finalizePayLedgerTerminal(w, entry, PayTerminalStateFailedInsufficientGoods, "", at), nil
	}
	// Seller balance overflow guard — symmetric with PR B's Pay
	// and the fast-path predicate 6.
	if seller.Coins > math.MaxInt-entry.Amount {
		return finalizePayLedgerTerminal(w, entry, PayTerminalStateFailedUnavailable, "", at), nil
	}

	// All gates pass. Atomic transfer + state flip + emit.
	if _, err := commitPayTransfer(w, buyer, seller, entry, at, ""); err != nil {
		// Theoretically unreachable — gates covered every path.
		return entry.State, fmt.Errorf("acceptPendingOffer: transfer for ledger %d: %w", entry.ID, err)
	}
	entry.State = PayLedgerStateAccepted
	entry.ResolvedAt = at
	// ZBBS-HOME-417: a completed transaction is conversational activity —
	// reset the huddle's silence-sweep dormancy clock so a busy-but-quiet
	// commerce huddle isn't concluded mid-trade. (Speech usually accompanies
	// commerce here, but a silent accept must still count.)
	touchHuddleActivity(w, entry.HuddleID, at)

	evt := &PayWithItemResolved{
		LedgerID:       entry.ID,
		BuyerID:        entry.BuyerID,
		SellerID:       entry.SellerID,
		ItemKind:       entry.ItemKind,
		QtyPerConsumer: entry.Qty,
		ConsumeNow:     entry.ConsumeNow,
		ConsumerIDs:    cloneActorIDs(entry.ConsumerIDs),
		Amount:         entry.Amount,
		TerminalState:  PayTerminalStateAccepted,
		SceneID:        entry.SceneID,
		HuddleID:       entry.HuddleID,
		At:             at,
	}
	w.emit(evt)

	return entry.State, nil
}

// DeclinePay returns the Command for the seller's pending-decline path.
// Phase 3 PR S4 step 5. Three gates first-failure-wins:
//
//  1. Caller exists, ledger exists, caller == entry.SellerID (auth
//     idempotent reject — tool error, NO transition).
//  2. entry.State == Pending (state idempotent reject — tool error,
//     NO transition).
//  3. reason rune-length ≤ MaxPayMessageRunes.
//
// No co-presence / break / catalog / stock / funds gates — decline is a
// seller-side rejection with no transfer. A seller can decline a
// pending offer even after wandering off (architecture § 3 — only
// accept requires co-presence).
//
// On all-pass: flip entry to Declined terminal, populate entry.Message
// with the decline reason (trim + rune-truncate), emit
// PayWithItemResolved{Declined}. Returns the new state.
func DeclinePay(callerID ActorID, ledgerID LedgerID, reason string, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if _, ok := w.Actors[callerID]; !ok {
				return nil, fmt.Errorf("DeclinePay: caller %q not in world", callerID)
			}
			entry, ok := w.PayLedger[ledgerID]
			if !ok || entry == nil {
				return nil, fmt.Errorf(
					"DeclinePay: ledger %d not found — re-check the ledger_id.",
					ledgerID,
				)
			}
			if entry.SellerID != callerID {
				return nil, fmt.Errorf(
					"DeclinePay: only the seller of ledger %d may decline it",
					ledgerID,
				)
			}
			if entry.State != PayLedgerStatePending {
				return nil, fmt.Errorf(
					"DeclinePay: ledger %d is no longer pending (currently %s) — nothing to decline.",
					ledgerID, entry.State,
				)
			}
			normalizedReason := truncatePayMessage(reason)
			return finalizePayLedgerTerminal(w, entry, PayTerminalStateDeclined, normalizedReason, at), nil
		},
	}
}

// CounterPay returns the Command for the seller's pending-counter path.
// Phase 3 PR S4 step 5. Five gates first-failure-wins:
//
//  1. Caller exists.
//  2. Ledger exists.
//  3. Caller == entry.SellerID (auth — tool error, NO transition).
//  4. entry.State == Pending (state — tool error, NO transition).
//  5. counterAmount in [1, MaxPayWithItemAmount].
//  6. message rune-length ≤ MaxPayMessageRunes.
//
// No co-presence / break / catalog / stock / funds gates — counter is
// terms-only; the buyer's optional response via PayWithItem
// (in_response_to=parent_id) is what re-runs validation.
//
// On all-pass: flip parent entry to Countered terminal, populate
// entry.CounterAmount + entry.CounterPayItems with the seller's terms +
// entry.Message with the trimmed/truncated counter text + entry.ResolvedAt,
// emit PayCountered (NOT PayWithItemResolved — distinct event family per
// EOS-26 architecture lock).
//
// Symmetric barter (ZBBS-HOME-393): a counter may propose coins
// (counterAmount), goods (counterPayItems), or both — but must propose at
// least one. counterAmount is now optional (>= 0): a seller can counter
// with pure goods terms ("I want 6 nails, not 5"). The buyer's optional
// response is a fresh PayWithItem (in_response_to=parent_id) restating
// whatever payment they choose.
//
// PayCountered.OriginalAmount carries the buyer's original coin offer for
// the buyer's perception prompt; CounterAmount + CounterPayItems carry the
// seller's counter terms.
func CounterPay(callerID ActorID, ledgerID LedgerID, counterAmount int, counterPayItems []PayItemInput, message string, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			caller, ok := w.Actors[callerID]
			if !ok {
				return nil, fmt.Errorf("CounterPay: caller %q not in world", callerID)
			}
			entry, ok := w.PayLedger[ledgerID]
			if !ok || entry == nil {
				return nil, fmt.Errorf(
					"CounterPay: ledger %d not found — re-check the ledger_id.",
					ledgerID,
				)
			}
			if entry.SellerID != callerID {
				return nil, fmt.Errorf(
					"CounterPay: only the seller of ledger %d may counter it",
					ledgerID,
				)
			}
			if entry.State != PayLedgerStatePending {
				return nil, fmt.Errorf(
					"CounterPay: ledger %d is no longer pending (currently %s) — nothing to counter.",
					ledgerID, entry.State,
				)
			}
			if counterAmount < 0 {
				return nil, fmt.Errorf("CounterPay: counter amount cannot be negative (got %d)", counterAmount)
			}
			if counterAmount > MaxPayWithItemAmount {
				return nil, fmt.Errorf(
					"CounterPay: counter amount exceeds maximum (got %d, max %d)",
					counterAmount, MaxPayWithItemAmount,
				)
			}
			if len(counterPayItems) > MaxPayWithItemPayItems {
				return nil, fmt.Errorf(
					"CounterPay: too many goods lines (got %d, max %d) — combine into fewer lines.",
					len(counterPayItems), MaxPayWithItemPayItems,
				)
			}
			resolvedCounterItems, err := resolvePayItems(w, counterPayItems)
			if err != nil {
				return nil, err
			}
			// A counter must propose something — coins, goods, or both
			// (symmetric with the offer-side rule, ZBBS-HOME-393).
			if counterAmount == 0 && len(resolvedCounterItems) == 0 {
				return nil, errors.New(
					"a counter must propose coins or goods — set an amount, add pay_items, or both.",
				)
			}

			// Non-increasing-counter coercion (v1 LLM-behavior scar,
			// observed 2026-05-08). A seller "countering" at or below the
			// buyer's offered amount isn't proposing a new price — it's
			// agreeing ("I can let you have it for 1 coin" at the offered
			// price, or volunteering a discount). Treat it as an accept at
			// the buyer's offered amount rather than recording a pointless
			// counter the buyer then has to re-accept. The counter message
			// is dropped — no undermining counter-speak on what is really a
			// yes. Gates 1-4 are already satisfied above (caller exists,
			// entry exists, caller == seller, state == pending), so the
			// shared accept path takes it from gate 5.
			//
			// Coercion applies ONLY to pure-coin haggles: if either the
			// original offer OR the counter involves goods (ZBBS-HOME-393),
			// the coin comparison is meaningless (you can't say "5 nails <=
			// 4 coins"), so any goods-bearing counter is a real counter and
			// flips to Countered for the buyer to weigh.
			pureCoinHaggle := len(entry.PayItems) == 0 && len(resolvedCounterItems) == 0
			if pureCoinHaggle && counterAmount <= entry.Amount {
				state, err := acceptPendingOffer(w, caller, entry, at)
				return state, err
			}

			normalizedMessage := truncatePayMessage(message)
			entry.State = PayLedgerStateCountered
			entry.CounterAmount = counterAmount
			entry.CounterPayItems = cloneItemKindQtys(resolvedCounterItems)
			entry.Message = normalizedMessage
			entry.ResolvedAt = at

			evt := &PayCountered{
				ParentID:        entry.ID,
				BuyerID:         entry.BuyerID,
				SellerID:        entry.SellerID,
				ItemKind:        entry.ItemKind,
				QtyPerConsumer:  entry.Qty,
				ConsumeNow:      entry.ConsumeNow,
				ConsumerIDs:     cloneActorIDs(entry.ConsumerIDs),
				OriginalAmount:  entry.Amount,
				CounterAmount:   counterAmount,
				CounterPayItems: cloneItemKindQtys(resolvedCounterItems),
				Message:         normalizedMessage,
				SceneID:         entry.SceneID,
				HuddleID:        entry.HuddleID,
				At:              at,
			}
			w.emit(evt)

			// Bidirectional relationship writes (KindNPCShared gate
			// filters which writes persist). Counter is a non-trivial
			// social move — worth capturing on both sides.
			buyerName := actorDisplayName(w, entry.BuyerID)
			sellerName := actorDisplayName(w, entry.SellerID)
			buyerFact := payCounteredFactText(buyerName, sellerName, entry.Amount, entry.PayItems, counterAmount, resolvedCounterItems, entry.ItemKind, entry.Qty, normalizedMessage, true)
			sellerFact := payCounteredFactText(buyerName, sellerName, entry.Amount, entry.PayItems, counterAmount, resolvedCounterItems, entry.ItemKind, entry.Qty, normalizedMessage, false)
			if _, err := RecordInteraction(entry.BuyerID, entry.SellerID, InteractionCounteredBy, buyerFact, at).Fn(w); err != nil {
				log.Printf("sim.CounterPay: RecordInteraction buyer→seller %q→%q: %v", entry.BuyerID, entry.SellerID, err)
			}
			if _, err := RecordInteraction(entry.SellerID, entry.BuyerID, InteractionCountered, sellerFact, at).Fn(w); err != nil {
				log.Printf("sim.CounterPay: RecordInteraction seller→buyer %q→%q: %v", entry.SellerID, entry.BuyerID, err)
			}
			return entry.State, nil
		},
	}
}

// WithdrawPay returns the Command for the buyer's pending-withdraw path.
// Phase 3 PR S4 step 5 (ledger-substrate § 9 — bundled into S4 to keep
// the offer state machine complete in one PR).
//
// Three gates first-failure-wins:
//
//  1. Caller exists, ledger exists, caller == entry.BuyerID (auth —
//     tool error, NO transition; buyer-callable only).
//  2. entry.State == Pending (state — tool error, NO transition).
//  3. message rune-length ≤ MaxPayMessageRunes.
//
// No co-presence — withdraw is unilateral; the buyer can withdraw an
// offer they made even after wandering off (ledger-substrate § 9).
//
// On all-pass: flip entry to WithdrawnByBuyer terminal, populate
// entry.Message with the trimmed/truncated withdraw note, emit
// PayWithItemResolved{WithdrawnByBuyer}.
func WithdrawPay(callerID ActorID, ledgerID LedgerID, message string, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if _, ok := w.Actors[callerID]; !ok {
				return nil, fmt.Errorf("WithdrawPay: caller %q not in world", callerID)
			}
			entry, ok := w.PayLedger[ledgerID]
			if !ok || entry == nil {
				return nil, fmt.Errorf(
					"WithdrawPay: ledger %d not found — re-check the ledger_id.",
					ledgerID,
				)
			}
			if entry.BuyerID != callerID {
				return nil, fmt.Errorf(
					"WithdrawPay: only the buyer of ledger %d may withdraw it",
					ledgerID,
				)
			}
			if entry.State != PayLedgerStatePending {
				return nil, fmt.Errorf(
					"WithdrawPay: ledger %d is no longer pending (currently %s) — nothing to withdraw.",
					ledgerID, entry.State,
				)
			}
			normalizedMessage := truncatePayMessage(message)
			return finalizePayLedgerTerminal(w, entry, PayTerminalStateWithdrawnByBuyer, normalizedMessage, at), nil
		},
	}
}

// ---- shared helpers --------------------------------------------------

// findAutoMatchQuote returns the id of an open quote a bare (quote_id-less)
// coin offer can take — same seller, same scene, buyer-eligible, exact term
// match, amount at or above the quote's floor — or 0 when none qualifies.
// The predicates mirror the fast path's 1–5; gate 6 (stock, coins, break)
// stays in runPayWithItemFastPath, which re-validates everything, so a miss
// there falls back to the slow path rather than erroring. A below-floor
// amount is a haggle, not a take — the slow path stakes it for the seller to
// counter. Cheapest floor wins, then lowest id, so a duplicate-quote field
// resolves deterministically. ZBBS-HOME-424.
func findAutoMatchQuote(
	w *World,
	buyer *Actor,
	sellerID ActorID,
	sceneID SceneID,
	kind ItemKind,
	qty int,
	amount int,
	consumeNow bool,
	consumerIDs []ActorID,
	at time.Time,
) QuoteID {
	var best *SceneQuote
	for _, q := range w.Quotes {
		if q == nil || q.State != SceneQuoteStateActive {
			continue
		}
		if !q.ExpiresAt.IsZero() && !at.Before(q.ExpiresAt) {
			continue
		}
		if q.SellerID != sellerID || q.SceneID != sceneID {
			continue
		}
		if q.TargetBuyer != "" && q.TargetBuyer != buyer.ID {
			continue
		}
		if q.ItemKind != kind || q.Qty != qty || q.ConsumeNow != consumeNow {
			continue
		}
		if !actorIDSetsEqual(q.ConsumerIDs, consumerIDs) {
			continue
		}
		if amount < q.Amount {
			continue
		}
		if best == nil || q.Amount < best.Amount || (q.Amount == best.Amount && q.ID < best.ID) {
			best = q
		}
	}
	if best == nil {
		return 0
	}
	return best.ID
}

// withdrawCrossingOffers resolves the buyer's OWN still-pending offers that
// mirror the transaction a quote auto-match just settled: left pending they
// invite a later double-settle (the seller accepting a stale mirror of a
// sale already made). "Mirror" is the settled take's term identity — same
// scene, kind, qty, disposition, and consumer set — NOT every same-goods
// offer: a distinct live order (different qty/disposition, e.g. a 10-water
// take-home staked before a 1-water consume-now quote take) must survive
// (code_review). Amount is excluded because above-floor overpayment is
// allowed on the take, and the duplicate gate's own key excludes price.
// Counter-chain entries (ParentID != 0) are skipped — a distinct lifecycle,
// same exemption the duplicate gate applies. WithdrawnByBuyer is the
// buyer-drove-this terminal — the reactor skips notifying the seller of it,
// and the seller's stale offer warrant drops via
// filterStalePayOfferWarrants. ZBBS-HOME-424.
func withdrawCrossingOffers(
	w *World,
	buyerID, sellerID ActorID,
	sceneID SceneID,
	kind ItemKind,
	qty int,
	consumeNow bool,
	consumerIDs []ActorID,
	at time.Time,
) {
	for _, e := range w.PayLedger {
		if e == nil || e.State != PayLedgerStatePending || e.ParentID != 0 {
			continue
		}
		if e.BuyerID != buyerID || e.SellerID != sellerID || e.ItemKind != kind {
			continue
		}
		if e.SceneID != sceneID || e.Qty != qty || e.ConsumeNow != consumeNow {
			continue
		}
		if !actorIDSetsEqual(e.ConsumerIDs, consumerIDs) {
			continue
		}
		finalizePayLedgerTerminal(w, e, PayTerminalStateWithdrawnByBuyer, "superseded — settled against the seller's open offer", at)
	}
}

// buyerCanAfford reports whether buyer holds at least amount coins. It
// is the single definition of the funds comparison, shared by all three
// sites that ask it: the offer-time fast-fail in PayWithItem's slow
// path, the fast-path predicate 6, and AcceptPay's gate 11. Centralizing
// the predicate keeps those three from drifting on what "can afford"
// means (e.g. if a future escrow/reserved-coins concept lands).
//
// The ACTION taken when this returns false is intentionally NOT shared,
// because it differs by lifecycle stage: the two offer-time sites reject
// with a tool error (no ledger entry is minted, or a would-be fast-path
// accept is aborted), while AcceptPay flips an already-pending entry to
// the failed_insufficient_funds terminal. Sharing the action would force
// one of those behaviors onto the other.
func buyerCanAfford(buyer *Actor, amount int) bool {
	return buyer.Coins >= amount
}

// buyerHoldsPayItems reports whether the buyer currently holds every goods
// line they offered to pay WITH (the barter leg, ZBBS-HOME-393). The
// goods counterpart to buyerCanAfford. Like the coin check, the ACTION on
// false differs by lifecycle stage (mint → tool-error reject; accept →
// failed_insufficient_goods terminal), so only the predicate is shared.
// An empty list (pure-coin offer) trivially holds.
func buyerHoldsPayItems(buyer *Actor, payItems []ItemKindQty) bool {
	for _, pi := range payItems {
		if buyer.Inventory[pi.Kind] < pi.Qty {
			return false
		}
	}
	return true
}

// payOfferShortfall returns a descriptive tool error if the buyer can't
// cover the offer's coins + goods at this instant, or nil if they can.
// The offer-time fast-fail (mint + fast-path) — an OPTIMIZATION that
// spares a wasted seller deliberation tick, not a reservation. Coins are
// reported first, then the first goods line short. ZBBS-HOME-393.
func payOfferShortfall(buyer *Actor, amount, qty int, payItems []ItemKindQty) error {
	if !buyerCanAfford(buyer, amount) {
		// Name the quantity the purse actually covers at the offered unit price,
		// so the model lowers the QUANTITY rather than just the coins
		// (ZBBS-HOME-459). The old "offer fewer coins" steer pointed at the wrong
		// lever: the buyer dropped coins, kept the quantity, and re-offered
		// underpriced (the John Ellis 25-meat-on-248-coins case). amount>=1 here
		// (amount==0 can't be unaffordable) and qty>=1 (validated upstream).
		// Multiply before dividing to keep the floor honest; int64 guards the
		// product against overflow on a 32-bit int build, clamped back into int
		// range (code_review).
		affordable64 := int64(buyer.Coins) * int64(qty) / int64(amount)
		if affordable64 > int64(math.MaxInt32) {
			affordable64 = int64(math.MaxInt32)
		}
		affordable := int(affordable64)
		if affordable < 1 {
			return fmt.Errorf(
				"insufficient coins (have %d, need %d) — you can't afford even one at this price; lower the quantity or pay with goods you carry.",
				buyer.Coins, amount,
			)
		}
		return fmt.Errorf(
			"insufficient coins (have %d, need %d) — at this price you can afford %d; lower the quantity or pay with goods you carry.",
			buyer.Coins, amount, affordable,
		)
	}
	for _, pi := range payItems {
		if buyer.Inventory[pi.Kind] < pi.Qty {
			return fmt.Errorf(
				"you don't have %d %s to offer (you carry %d) — offer goods you actually hold.",
				pi.Qty, pi.Kind, buyer.Inventory[pi.Kind],
			)
		}
	}
	return nil
}

// resolvePayItems resolves a barter offer's free-text goods lines to
// canonical ItemKindQty. Each name → resolveItemKind (case-insensitive +
// trim); a kind may appear at most once (callers should net duplicates);
// qty must be positive and within MaxPayWithItemQty. Empty input returns
// nil (a pure-coin offer). ZBBS-HOME-393.
//
// Shared by PayWithItem (the buyer's pay_items) and CounterPay (the
// seller's counter goods) so the two resolve goods identically.
func resolvePayItems(w *World, payItems []PayItemInput) ([]ItemKindQty, error) {
	if len(payItems) == 0 {
		return nil, nil
	}
	resolved := make([]ItemKindQty, 0, len(payItems))
	seen := make(map[ItemKind]struct{}, len(payItems))
	for _, pi := range payItems {
		if pi.Qty < 1 {
			return nil, fmt.Errorf("pay_items: quantity must be at least 1 (got %d)", pi.Qty)
		}
		if pi.Qty > MaxPayWithItemQty {
			return nil, fmt.Errorf("pay_items: quantity exceeds maximum (got %d, max %d)", pi.Qty, MaxPayWithItemQty)
		}
		// ZBBS-WORK-412: mint an unknown pay-with good at qty 0 (an NPC offering
		// to pay with a good it names is a discovery signal). The offerer holds 0
		// of a freshly-minted kind, so the holdings shortfall check rejects the
		// offer with a "you have no X to give" steer.
		kind, ok := resolveOrMintItemKind(w, pi.Item)
		if !ok {
			return nil, fmt.Errorf(
				"unknown item kind %q in pay_items — check the items you carry before offering.",
				pi.Item,
			)
		}
		if _, dup := seen[kind]; dup {
			return nil, fmt.Errorf(
				"%q appears more than once in pay_items — combine it into a single line.",
				pi.Item,
			)
		}
		// A "service" item is not a transferable good — its delivery is an
		// effect bound to the SELLER's establishment (lodging grants a room
		// there), so handing one over as payment is meaningless. Without this
		// gate an innkeeper holding a vestigial nights_stay token could barter
		// a room for a drink (observed live: "1 nights_stay for 1 water",
		// conversation hud-6c849d…). Applies to counter goods too — both
		// directions resolve through here. ZBBS-HOME-424.
		if itemHasCapability(w, kind, "service") {
			return nil, fmt.Errorf(
				"%q is a service, not a carryable good — it can't be offered as payment. Pay with coins or physical goods.",
				pi.Item,
			)
		}
		seen[kind] = struct{}{}
		resolved = append(resolved, ItemKindQty{Kind: kind, Qty: pi.Qty})
	}
	return resolved, nil
}

// formatPayment renders an offer's payment terms as prose for SalientFact
// memory and perception lines: coins, goods, or both. ZBBS-HOME-393.
//
//	formatPayment(5, nil)                       → "5 coins"
//	formatPayment(0, [{nails,5}])               → "5 nails"
//	formatPayment(3, [{nails,5}])               → "5 nails and 3 coins"
//	formatPayment(3, [{nails,5},{hammer,2}])    → "5 nails, 2 hammers and 3 coins"
//
// Returns "nothing" only for an all-empty payment, a state the intake
// gates reject — so callers always get a non-empty phrase in practice.
func formatPayment(amount int, payItems []ItemKindQty) string {
	parts := make([]string, 0, len(payItems)+1)
	for _, pi := range payItems {
		parts = append(parts, fmt.Sprintf("%d %s", pi.Qty, pi.Kind))
	}
	if amount > 0 {
		coins := "coins"
		if amount == 1 {
			coins = "coin"
		}
		parts = append(parts, fmt.Sprintf("%d %s", amount, coins))
	}
	switch len(parts) {
	case 0:
		return "nothing"
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

// effectivePayConsumerCount returns max(1, len(consumerIDs)). Empty
// consumer set = buyer is implicit single consumer.
func effectivePayConsumerCount(consumerIDs []ActorID) int {
	if len(consumerIDs) == 0 {
		return 1
	}
	return len(consumerIDs)
}

// truncatePayMessage trims surrounding whitespace and rune-truncates to
// MaxPayMessageRunes. The handler intake also trims + validates control
// characters; this is defense-in-depth for non-handler callers.
func truncatePayMessage(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) > MaxPayMessageRunes {
		return string(runes[:MaxPayMessageRunes])
	}
	return s
}

// resolvePayConsumers resolves each consumer name to a huddle-peer
// ActorID. Same rules as resolveQuoteConsumers (case-insensitive trim,
// ambiguity reject, missing reject, duplicate reject, seller-as-consumer
// reject). Buyer-as-consumer IS allowed (the buyer often is one of the
// consumers in "round at the table" semantics).
//
// Returns the resolved IDs in input order; empty input returns nil.
func resolvePayConsumers(w *World, buyer *Actor, sellerID ActorID, names []string) ([]ActorID, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if buyer.CurrentHuddleID == "" {
		return nil, errors.New(
			"consumers can only be specified within an active huddle.",
		)
	}
	members, ok := w.actorsByHuddle[buyer.CurrentHuddleID]
	if !ok {
		return nil, errors.New(
			"consumers can only be specified within an active huddle.",
		)
	}
	resolved := make([]ActorID, 0, len(names))
	seen := make(map[ActorID]struct{}, len(names))
	for _, raw := range names {
		target := strings.TrimSpace(raw)
		if target == "" {
			return nil, errors.New("consumer name is empty after trim — every consumer must have a name.")
		}
		var found ActorID
		for peerID := range members {
			peer, ok := w.Actors[peerID]
			if !ok {
				continue
			}
			if strings.EqualFold(peer.DisplayName, target) {
				if found != "" {
					return nil, fmt.Errorf(
						"more than one person named %q is in this conversation — use a unique full name.",
						target,
					)
				}
				found = peerID
			}
		}
		if found == "" {
			return nil, fmt.Errorf(
				"no one named %q in this conversation — re-check who is here before offering.",
				target,
			)
		}
		if found == sellerID {
			return nil, errors.New(
				"the seller can't be a consumer of their own sale — drop their name from the consumer list.",
			)
		}
		if _, dup := seen[found]; dup {
			return nil, fmt.Errorf(
				"%q appears more than once in the consumer list — list each person only once.",
				target,
			)
		}
		seen[found] = struct{}{}
		resolved = append(resolved, found)
	}
	return resolved, nil
}

// validateInResponseTo runs the architecture § 7 in_response_to chain
// rules. The buyer is NOT required to keep matching item terms across
// the counter chain — they can shift qty/item/consume_now between
// rounds. Validation pins identity (same buyer, same seller),
// freshness (parent.ResolvedAt within
// PayLedgerInResponseToWindow), and chain shape (parent has been
// countered, hasn't already been answered).
func validateInResponseTo(w *World, parentID LedgerID, buyerID, sellerID ActorID, at time.Time) error {
	parent, ok := w.PayLedger[parentID]
	if !ok || parent == nil {
		return fmt.Errorf(
			"in_response_to: parent ledger %d not found",
			parentID,
		)
	}
	if parent.State != PayLedgerStateCountered {
		return fmt.Errorf(
			"in_response_to: parent ledger %d is not countered (currently %s) — you can only respond to a counter.",
			parentID, parent.State,
		)
	}
	if parent.BuyerID != buyerID {
		return fmt.Errorf(
			"in_response_to: ledger %d isn't your offer — you can't respond to someone else's counter.",
			parentID,
		)
	}
	if parent.SellerID != sellerID {
		return fmt.Errorf(
			"in_response_to: ledger %d was countered by a different seller — respond to the original seller.",
			parentID,
		)
	}
	if parent.ResolvedAt.IsZero() || at.Sub(parent.ResolvedAt) > PayLedgerInResponseToWindow {
		return fmt.Errorf(
			"in_response_to: ledger %d's counter is too old (older than %s) — make a fresh offer instead.",
			parentID, PayLedgerInResponseToWindow,
		)
	}
	// Depth cap. The response this validates would be at parent.Depth+1;
	// a parent already at MaxPayCounterChainDepth can't be answered, which
	// bounds the haggle chain. See MaxPayCounterChainDepth.
	if parent.Depth >= MaxPayCounterChainDepth {
		return fmt.Errorf(
			"in_response_to: ledger %d is at the counter-chain depth limit (%d rounds) — make a fresh offer instead of haggling further.",
			parentID, MaxPayCounterChainDepth,
		)
	}
	// Parent-uniqueness scan (ledger-substrate § 6 — O(N) over
	// World.PayLedger). Cheap at expected sizes; reverse index
	// deferred.
	for _, e := range w.PayLedger {
		if e == nil || e.ID == parentID {
			continue
		}
		if e.ParentID == parentID {
			return fmt.Errorf(
				"in_response_to: ledger %d has already been answered by ledger %d — make a fresh offer instead.",
				parentID, e.ID,
			)
		}
	}
	return nil
}

// allConsumersInHuddle reports whether every consumerID is currently in
// huddleID. Used by AcceptPay's gate 9 (consumer departure between
// offer creation and accept).
func allConsumersInHuddle(w *World, huddleID HuddleID, consumerIDs []ActorID) bool {
	if huddleID == "" {
		return false
	}
	members, ok := w.actorsByHuddle[huddleID]
	if !ok {
		return false
	}
	for _, cid := range consumerIDs {
		if _, in := members[cid]; !in {
			return false
		}
	}
	return true
}

// finalizePayLedgerTerminal flips entry to the given terminal state,
// stamps ResolvedAt + Message, emits PayWithItemResolved, and returns
// the new state. Used by every terminal flip path EXCEPT counter
// (which has its own event family and lives inline in CounterPay).
//
// Caller guarantees entry.State is currently Pending — this helper
// performs no state-check itself.
func finalizePayLedgerTerminal(
	w *World,
	entry *PayLedgerEntry,
	terminal PayTerminalState,
	message string,
	at time.Time,
) PayLedgerState {
	entry.State = PayLedgerState(terminal)
	entry.ResolvedAt = at
	entry.Message = message

	evt := &PayWithItemResolved{
		LedgerID:       entry.ID,
		BuyerID:        entry.BuyerID,
		SellerID:       entry.SellerID,
		ItemKind:       entry.ItemKind,
		QtyPerConsumer: entry.Qty,
		ConsumeNow:     entry.ConsumeNow,
		ConsumerIDs:    cloneActorIDs(entry.ConsumerIDs),
		Amount:         entry.Amount,
		TerminalState:  terminal,
		Message:        message,
		SceneID:        entry.SceneID,
		HuddleID:       entry.HuddleID,
		At:             at,
	}
	w.emit(evt)

	// Relationship writes for the decline path — both directions, so a
	// shared-VA NPC's perception can later render "Bob declined my offer
	// for ale" + "I declined Alice's offer for ale" appropriately.
	// Other terminal kinds (expired / withdrawn / failed_*) don't get
	// relationship writes — they're low-signal lifecycle events rather
	// than social moves.
	if terminal == PayTerminalStateDeclined {
		buyerName := actorDisplayName(w, entry.BuyerID)
		sellerName := actorDisplayName(w, entry.SellerID)
		buyerFact := payDeclinedFactText(buyerName, sellerName, entry.Amount, entry.PayItems, entry.ItemKind, entry.Qty, message, true)
		sellerFact := payDeclinedFactText(buyerName, sellerName, entry.Amount, entry.PayItems, entry.ItemKind, entry.Qty, message, false)
		if _, err := RecordInteraction(entry.BuyerID, entry.SellerID, InteractionPayDeclinedBy, buyerFact, at).Fn(w); err != nil {
			log.Printf("sim.finalizePayLedgerTerminal: RecordInteraction buyer→seller %q→%q: %v", entry.BuyerID, entry.SellerID, err)
		}
		if _, err := RecordInteraction(entry.SellerID, entry.BuyerID, InteractionDeclinedPay, sellerFact, at).Fn(w); err != nil {
			log.Printf("sim.finalizePayLedgerTerminal: RecordInteraction seller→buyer %q→%q: %v", entry.SellerID, entry.BuyerID, err)
		}
	}
	return entry.State
}

// payTransferOutcome is commitPayTransfer's buyer-visible summary of the
// atomic commit, carried onto PayWithItemResult for fast-path settles so the
// buyer's tool feedback can voice what actually happened (ZBBS-HOME-436).
type payTransferOutcome struct {
	buyerAte        int     // units the buyer themself consumed now
	keptToInventory int     // surplus units pocketed into the buyer's pack
	tookHome        bool    // physical take-home handed over at accept
	booked          bool    // lodging Order minted for keeper check-in
	satisfiesNeed   NeedKey // primary need the consumed item satisfies
	feltAfter       string  // buyer's post-consume felt label(s); "" = sated
	mealMinutes     int     // buyer's eat-here dwell duration in minutes; 0 = no ongoing meal/drink (ZBBS-WORK-409)
}

// maxDwellMinutes returns the longest remaining dwell duration in minutes across
// the stamped item-dwell snapshots (0 when none carry a countdown). An eat-here
// meal or drink keeps easing a need for this long after the first bite, but the
// buyer collects it only by staying put — walking off deletes the credit. The
// settle feedback uses this to tell the buyer to stay and finish rather than
// bolt and forfeit the food and the coins (ZBBS-WORK-409).
func maxDwellMinutes(stamped []DwellCreditSnapshot) int {
	best := 0
	for _, s := range stamped {
		if s.RemainingTicks == nil {
			continue
		}
		m := (*s.RemainingTicks) * s.PeriodMinutes
		if m > best {
			best = m
		}
	}
	return best
}

// buyerFeltAfterConsume reports the buyer's post-consume felt state for the
// need(s) the item satisfies: the primary need key (largest per-unit restore)
// and the joined felt labels for every satisfied need still at or above the
// awareness floor. An empty felt string means the meal left the buyer below
// the floor — sated, nothing to voice. Runs on the world goroutine against
// live post-commit needs, which the once-per-tick perception render cannot
// show the model mid-tick. ZBBS-HOME-436.
func buyerFeltAfterConsume(buyer *Actor, def *ItemKindDef, thresholds NeedThresholds) (NeedKey, string) {
	if buyer == nil || def == nil {
		return "", ""
	}
	// Strict > keeps the FIRST def.Satisfies entry on an Immediate tie —
	// item def order is the authoritative priority, matching how
	// consumableUnits and the dwell-credit upsert walk the same slice.
	primary, best := NeedKey(""), 0
	var felt []string
	seen := make(map[string]bool)
	for _, s := range def.Satisfies {
		if s.Immediate <= 0 {
			continue
		}
		if s.Immediate > best {
			best, primary = s.Immediate, s.Attribute
		}
		n, ok := FindNeed(s.Attribute)
		if !ok {
			continue
		}
		// Dedup so two Satisfies lines on the same attribute (or needs
		// sharing a vocabulary word) can't render "hungry, hungry".
		if label := n.Label(n.Tier(buyer.Needs[s.Attribute], thresholds.Get(s.Attribute))); label != "" && !seen[label] {
			seen[label] = true
			felt = append(felt, label)
		}
	}
	return primary, strings.Join(felt, ", ")
}

// commitPayTransfer performs the AcceptPay / fast-path-accept atomic
// commit: coin debit + item movement + ConsumeNow per-consumer apply +
// bidirectional buyer/seller relationship writes + per-consumer
// ItemConsumed emit when ConsumeNow=true.
//
// The world goroutine serializes everything, so by the time this
// function is called the gates have already pre-validated stock,
// funds, and overflow. A nonzero error return here is a bug, not a
// domain failure.
//
// Item flow:
//
//   - ConsumeNow=false, "lodging" item: a deferred booking. The Order is
//     minted at Ready and left for the keeper to check the guest in via
//     deliver_order (the room grant happens there; the eviction exemption is
//     gated on that check-in). This is the designed two-phase lodging flow —
//     see the salem-engine-v2/lodging codebase note — and is NOT flattened.
//   - ConsumeNow=false, everything else (physical takeaway): the Order is
//     minted AND immediately delivered in the same tick via
//     fulfillTakeHomeOrderAtAccept (ZBBS-HOME-398) — goods move to the buyer
//     right here at accept, and the Order is flipped to Delivered so its
//     durable pay_ledger row still persists (the price-book restart seed reads
//     accepted rows). At accept the buyer is co-present and the seller holds
//     the stock, so there is nothing to defer and no window for the HOME-396
//     takeaway-expiry robbery. Phase 3 PR S6 originally deferred even this to
//     a separate deliver_order beat for seller narrative agency, but the
//     buyer-seller rendezvous gap made deferral a routine robbery path.
//   - ConsumeNow=true: stock leaves seller's inventory, but does NOT
//     land on the consumer's. Instead, the consumer's needs decrement
//     per the ItemKind's Satisfies list (mirrors sim.Consume), dwell
//     credits are upserted at the consumer's nearest VillageObject, and
//     one ItemConsumed event per consumer is emitted with the applied
//     deltas. No Order minted — eat-on-the-spot has no post-accept
//     fulfillment to track.
//
// forText (slow-path: empty; fast-path: the buyer's flavor text on the
// pay_with_item call) is folded into the buyer/seller SalientFact via
// payAcceptedFactText.
//
// The returned payTransferOutcome summarizes the buyer-visible effects so
// the fast path can voice them in the buyer's tool feedback (ZBBS-HOME-436).
// AcceptPay discards it — the buyer isn't the actor reading that result.
// On a non-nil error the outcome is meaningless and MUST be discarded:
// error paths return a zero outcome, but some fire after partial mutation,
// so the outcome never doubles as a rollback record (code_review).
func commitPayTransfer(
	w *World,
	buyer, seller *Actor,
	entry *PayLedgerEntry,
	at time.Time,
	forText string,
) (payTransferOutcome, error) {
	var out payTransferOutcome
	// Two-way swap (ZBBS-HOME-393): the buyer pays with coins AND/OR goods.
	// Validate the goods leg in full BEFORE mutating anything so a single
	// bad line aborts the whole swap (validate-all-then-apply, mirroring
	// AdjustActorHoldings). The coin leg's seller-overflow was already
	// guarded by the caller's gates; buyer-holds was revalidated at gate 12,
	// but re-check here defensively — a subscriber firing mid-accept could
	// have moved the buyer's holdings.
	type payItemMove struct {
		kind          ItemKind
		buyerPostQty  int // >= 0, validated
		sellerPostQty int // overflow-checked
	}
	// Aggregate by kind FIRST. resolvePayItems rejects duplicate canonical
	// kinds at intake, but commitPayTransfer is the atomicity boundary and
	// takes ledger data directly (seeded entries, future persisted reloads),
	// so it must not assume uniqueness: without aggregation two lines of the
	// same kind would each compute their post-quantity from the ORIGINAL
	// inventory and the second apply would clobber the first (lost quantity).
	totals := make(map[ItemKind]int, len(entry.PayItems))
	for _, pi := range entry.PayItems {
		if pi.Qty < 1 {
			return payTransferOutcome{}, fmt.Errorf("commitPayTransfer: invalid pay_item qty %d for %s", pi.Qty, pi.Kind)
		}
		next, err := addChecked(totals[pi.Kind], pi.Qty)
		if err != nil {
			return payTransferOutcome{}, fmt.Errorf("commitPayTransfer: pay_item total for %s would overflow", pi.Kind)
		}
		totals[pi.Kind] = next
	}
	moves := make([]payItemMove, 0, len(totals))
	for kind, qty := range totals {
		have := buyer.Inventory[kind]
		if have < qty {
			return payTransferOutcome{}, fmt.Errorf("commitPayTransfer: buyer %q lacks %d %s mid-commit (have %d)", buyer.ID, qty, kind, have)
		}
		sellerPost, err := addChecked(seller.Inventory[kind], qty)
		if err != nil {
			return payTransferOutcome{}, fmt.Errorf("commitPayTransfer: seller %q %s balance would overflow", seller.ID, kind)
		}
		moves = append(moves, payItemMove{kind: kind, buyerPostQty: have - qty, sellerPostQty: sellerPost})
	}

	// All legs validated — apply coins + goods together. Coin overflow
	// guarded by caller.
	buyer.Coins -= entry.Amount
	seller.Coins += entry.Amount
	for _, m := range moves {
		if m.buyerPostQty == 0 {
			delete(buyer.Inventory, m.kind) // delete-on-zero invariant
		} else {
			buyer.Inventory[m.kind] = m.buyerPostQty
		}
		if seller.Inventory == nil {
			seller.Inventory = make(map[ItemKind]int)
		}
		seller.Inventory[m.kind] = m.sellerPostQty
	}

	consumers := entry.ConsumerIDs
	implicitBuyerConsumer := len(consumers) == 0
	if implicitBuyerConsumer {
		consumers = []ActorID{entry.BuyerID}
	}

	// deliveredTakeHome holds a physical take-home Order that was minted +
	// goods-transferred this tick but NOT yet flipped to Delivered — the flip
	// is deferred until after the Paid/PaidBy facts below so OrderDelivered
	// fires after the payment facts exist (ZBBS-HOME-398; code_review).
	var deliveredTakeHome *Order

	def := w.ItemKinds[entry.ItemKind]
	if entry.ConsumeNow {
		// Eat-on-the-spot: stock leaves seller, consumer needs
		// satisfied directly. Per-consumer apply + dwell stamp +
		// ItemConsumed emit. No Order minted.
		//
		// ZBBS-WORK-391: each consumer eats only what their needs can absorb
		// (consumableUnits); the surplus goes to the BUYER's inventory instead
		// of burning into an already-zeroed need (the Prudence case: a
		// seller-pitched 10-meat bundle eaten in one go against a hunger one
		// unit would have cleared). The purchase itself is untouched — the
		// buyer pays the full amount and the seller's stock drops by the full
		// qty; only the eat-vs-pocket split changes. Surplus is routed to the
		// buyer (not the consumer) because the buyer paid for it; for the
		// common implicit-self-consumer offer they are the same actor anyway.
		//
		// The split is pre-computed for ALL consumers before this branch
		// mutates anything, so the one new failure mode — buyer-inventory
		// overflow pocketing the surplus — rejects up front rather than
		// mid-loop after stock/needs moved (code_review). The splits are
		// order-independent: resolvePayConsumers rejects duplicate consumers,
		// so no consumer's eat affects another's needs.
		type consumeSplit struct {
			cid      ActorID
			consumer *Actor
			eat      int
			kept     int
		}
		splits := make([]consumeSplit, 0, len(consumers))
		totalKept := 0
		for _, cid := range consumers {
			consumer, ok := w.Actors[cid]
			if !ok {
				// Shouldn't happen — gate 9 verified consumer presence
				// in the huddle. Conservative skip.
				continue
			}
			eat := consumableUnits(consumer, def, entry.Qty)
			splits = append(splits, consumeSplit{cid: cid, consumer: consumer, eat: eat, kept: entry.Qty - eat})
			totalKept += entry.Qty - eat
		}
		if totalKept > 0 {
			if _, err := addChecked(buyer.Inventory[entry.ItemKind], totalKept); err != nil {
				return payTransferOutcome{}, fmt.Errorf("commitPayTransfer: buyer %q %s balance would overflow pocketing surplus", buyer.ID, entry.ItemKind)
			}
		}
		out.keptToInventory = totalKept
		for _, sp := range splits {
			cid, consumer, eat, kept := sp.cid, sp.consumer, sp.eat, sp.kept
			// "service"-capability items (e.g. nights_stay) carry no
			// inventory — infinite-stock, so there's nothing to deplete
			// (ZBBS-HOME-296; mirrors the gate-10 stock skip). Without this
			// guard a consume_now service offer would trip the drained-
			// inventory error below. Lodging is always ConsumeNow=false (it
			// mints an Order), so this guard is defensive for the unusual
			// consume_now+service combo, not a lodging path.
			if !itemHasCapability(w, entry.ItemKind, "service") {
				have := seller.Inventory[entry.ItemKind]
				if have < entry.Qty {
					// Defensive — gate 10 ensured `seller.Inventory[kind]
					// >= Qty * effectiveConsumers`. If a subscriber fired
					// mid-loop somehow drained inventory, abort transfer.
					return payTransferOutcome{}, fmt.Errorf("commitPayTransfer: seller %q inventory drained mid-commit", seller.ID)
				}
				seller.Inventory[entry.ItemKind] = have - entry.Qty
				if seller.Inventory[entry.ItemKind] == 0 {
					delete(seller.Inventory, entry.ItemKind)
				}
			}
			if kept > 0 {
				if buyer.Inventory == nil {
					buyer.Inventory = make(map[ItemKind]int)
				}
				// Preflighted above against totalKept; per-split adds can't
				// overflow if the total didn't.
				buyer.Inventory[entry.ItemKind] += kept
			}
			applied := applyConsumeSatisfactions(consumer, def, eat)
			structureID, _ := resolveLoiteringObject(w, consumer.Pos, LoiterAttributionTiles)
			var stamped []DwellCreditSnapshot
			if structureID != "" && def != nil {
				stamped = UpsertItemDwellCredits(consumer, entry.ItemKind, def.Satisfies, structureID, at)
			}
			// Kept is stamped only on the BUYER's own consume event — the
			// surplus lands in the buyer's inventory, so a non-buyer
			// consumer's event claiming "you kept N" would be false (and its
			// narration beat would tell the wrong actor they pocketed food).
			// Group-order surplus from non-buyer consumers reaches the buyer
			// silently (their inventory shows it next tick). code_review.
			eventKept := 0
			if cid == entry.BuyerID {
				eventKept = kept
				out.buyerAte = eat
				out.mealMinutes = maxDwellMinutes(stamped)
			}
			w.emit(&ItemConsumed{
				ActorID: cid,
				Kind:    entry.ItemKind,
				Qty:     eat,
				Kept:    eventKept,
				Applied: applied,
				At:      at,
			})
			if len(stamped) > 0 {
				narration := ""
				if def != nil {
					narration = def.ConsumeDwellNarration
				}
				w.emit(&DwellStarted{
					ActorID:       cid,
					Kind:          entry.ItemKind,
					StructureID:   structureID,
					Credits:       stamped,
					NarrationText: narration,
					At:            at,
				})
			}
		}
		if out.buyerAte > 0 {
			out.satisfiesNeed, out.feltAfter = buyerFeltAfterConsume(buyer, def, w.Settings.NeedThresholds)
		}
	} else if itemHasCapability(w, entry.ItemKind, "lodging") {
		// Lodging is a deferred booking, NOT an immediate handover: mint the
		// Order at Ready and leave it for the keeper to check the guest in via
		// deliver_order. That check-in is the designed mechanic — the room
		// grant (AssignBedroomForLodger) happens there, and the eviction
		// exemption is gated on it ("the keeper has to do their job"). See the
		// salem-engine-v2/lodging codebase note. Restoring the rest of the v1
		// order book (ready_by advance booking, future-bookings perception,
		// overdue cues) is tracked separately (ZBBS-HOME-399).
		createOrderForPayWithItem(w, entry, at)
		out.booked = true
	} else {
		// Physical take-home (ZBBS-HOME-398): mint the Order and move the goods
		// to the buyer right now, at accept, while the parties are co-present and
		// the seller holds the stock. Nothing to defer, so no window for the
		// takeaway-expiry robbery (HOME-396). The Order is minted (so its durable
		// pay_ledger row persists for the price-book restart seed) but the flip to
		// Delivered is held until after the Paid/PaidBy facts below. A non-nil
		// return is a substrate invariant violation (gates guaranteed
		// fulfillment), handled like the ConsumeNow drift errors above.
		o, err := mintAndTransferTakeHomeOrder(w, entry, seller, at)
		if err != nil {
			return payTransferOutcome{}, err
		}
		deliveredTakeHome = o
		out.tookHome = true
	}

	// Bidirectional Paid / PaidBy SalientFacts for the buyer↔seller
	// pair. Per-consumer relationship writes (buyer↔consumer,
	// seller↔consumer) intentionally NOT performed — the bookkeeping
	// gets thorny on a 6-person group order and the per-consumer
	// ItemConsumed event already gives subscribers the per-consumer
	// signal they need.
	buyerName := buyer.DisplayName
	sellerName := seller.DisplayName
	buyerFact := payAcceptedFactText(buyerName, sellerName, entry.Amount, entry.PayItems, entry.ItemKind, entry.Qty, len(entry.ConsumerIDs), forText, true)
	sellerFact := payAcceptedFactText(buyerName, sellerName, entry.Amount, entry.PayItems, entry.ItemKind, entry.Qty, len(entry.ConsumerIDs), forText, false)
	if _, err := RecordInteraction(entry.BuyerID, entry.SellerID, InteractionPaid, buyerFact, at).Fn(w); err != nil {
		log.Printf("sim.commitPayTransfer: RecordInteraction buyer→seller %q→%q: %v", entry.BuyerID, entry.SellerID, err)
	}
	if _, err := RecordInteraction(entry.SellerID, entry.BuyerID, InteractionPaidBy, sellerFact, at).Fn(w); err != nil {
		log.Printf("sim.commitPayTransfer: RecordInteraction seller→buyer %q→%q: %v", entry.SellerID, entry.BuyerID, err)
	}

	// ZBBS-HOME-398: now that the Paid/PaidBy facts exist, flip the
	// immediately-delivered physical take-home Order to Delivered — so
	// OrderDelivered fires AFTER the payment facts (code_review). The
	// Delivered/Received facts are intentionally NOT written: paid and received
	// coincide in this same instant, so the Paid/PaidBy pair above already
	// records the exchange (unlike the deferred deliver_order path, where the
	// handover is a separate later beat with its own facts).
	if deliveredTakeHome != nil {
		flipOrderTerminal(w, deliveredTakeHome, OrderStateDelivered, at)
	}
	return out, nil
}

// consumableUnits returns how many of maxQty units the actor's current
// needs can actually absorb — the largest per-attribute ceil(need /
// per-unit-restore) across the item's immediate satisfactions. A positive
// maxQty is clamped to [1, maxQty]; a non-positive maxQty returns 0 (both
// callers validate qty >= 1 first, but this helper is shared logic and must
// not promise a floor it can't keep). The floor of 1 is deliberate: a
// consume was asked for, so one unit is always eaten even when every need
// is already at zero (the in-world beat is "you eat one and are full; the
// rest you keep", not a purchase that eats nothing). Both consume paths
// share this so the accept-time clamp can't be defeated by re-consuming
// the pocketed surplus next tick (ZBBS-WORK-391).
func consumableUnits(actor *Actor, def *ItemKindDef, maxQty int) int {
	if maxQty <= 0 {
		return 0
	}
	if maxQty == 1 || actor == nil || def == nil {
		return maxQty
	}
	units := 0
	for _, s := range def.Satisfies {
		if s.Immediate <= 0 {
			continue
		}
		need := actor.Needs[s.Attribute]
		if need <= 0 {
			continue
		}
		n := (need + s.Immediate - 1) / s.Immediate
		if n > units {
			units = n
		}
	}
	if units < 1 {
		units = 1
	}
	if units > maxQty {
		units = maxQty
	}
	return units
}

// applyConsumeSatisfactions applies a Consume's per-qty satisfaction
// effect to actor.Needs, mirroring the inner loop of sim.Consume. Used
// by the ConsumeNow branch of commitPayTransfer when items are
// consumed at acceptance time rather than added to inventory.
//
// Returns the per-need post-clamp deltas (positive magnitudes; absent
// from the map when the need didn't actually move). nil def is a
// no-op (def is nil only for an ItemKind whose catalog entry vanished,
// which the AcceptPay gate-8 + fast-path predicate-6 both check before
// reaching this helper).
func applyConsumeSatisfactions(actor *Actor, def *ItemKindDef, qty int) map[NeedKey]int {
	if actor == nil || def == nil {
		return nil
	}
	if actor.Needs == nil {
		actor.Needs = make(map[NeedKey]int)
	}
	var applied map[NeedKey]int
	for _, s := range def.Satisfies {
		if s.Immediate <= 0 {
			continue
		}
		pre := actor.Needs[s.Attribute]
		post := ClampNeed(pre - s.Immediate*qty)
		if pre == post {
			continue
		}
		actor.Needs[s.Attribute] = post
		if applied == nil {
			applied = make(map[NeedKey]int)
		}
		applied[s.Attribute] = pre - post
	}
	return applied
}

// actorDisplayName returns the actor's DisplayName, or "" if the actor
// isn't in the world. Used by SalientFact text builders for declined /
// countered paths where the helper doesn't have the *Actor pointer
// already in hand.
func actorDisplayName(w *World, id ActorID) string {
	a, ok := w.Actors[id]
	if !ok || a == nil {
		return ""
	}
	return a.DisplayName
}

// payAcceptedFactText renders the SalientFact text for an accepted pay-
// with-item transfer. The two sides share the same shape with subject /
// object pronouns flipped via buyerSide.
//
//	buyerSide=true:  "I paid Aldous 5 coins for 2 stew." / with consumers:
//	                  "I paid Aldous 5 coins for 2 stew × 3."
//	buyerSide=false: "Hannah paid me 5 coins for 2 stew."
//
// payItems folds the barter goods into the payment phrase via formatPayment
// ("5 nails", "5 nails and 3 coins"), so a goods trade reads "I paid Aldous
// 5 nails for 2 stew." (ZBBS-HOME-393).
//
// forText is folded in as " for <trim>" before the final period when
// non-empty (mirrors PR B's payFactText).
func payAcceptedFactText(buyerName, sellerName string, amount int, payItems []ItemKindQty, kind ItemKind, qty, consumerCount int, forText string, buyerSide bool) string {
	subject, object, verb := buyerName, sellerName, "paid"
	if buyerSide {
		subject = "I"
	} else {
		object = "me"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s %s for %d %s", subject, verb, object, formatPayment(amount, payItems), qty, kind)
	if consumerCount > 1 {
		fmt.Fprintf(&b, " × %d", consumerCount)
	}
	forText = strings.TrimSpace(forText)
	if forText != "" {
		fmt.Fprintf(&b, " for %s", forText)
	}
	b.WriteString(".")
	return b.String()
}

// payDeclinedFactText renders a SalientFact for a declined offer.
//
//	buyerSide=true:  "Aldous declined my offer of 5 coins for 2 stew."
//	                  + " Reason: <reason>." when reason non-empty.
//	buyerSide=false: "I declined Hannah's offer of 5 coins for 2 stew."
//	                  + " Reason: <reason>." when reason non-empty.
//
// payItems folds the barter goods into the offer phrase (ZBBS-HOME-393).
func payDeclinedFactText(buyerName, sellerName string, amount int, payItems []ItemKindQty, kind ItemKind, qty int, reason string, buyerSide bool) string {
	offer := formatPayment(amount, payItems)
	var b strings.Builder
	if buyerSide {
		fmt.Fprintf(&b, "%s declined my offer of %s for %d %s", sellerName, offer, qty, kind)
	} else {
		fmt.Fprintf(&b, "I declined %s's offer of %s for %d %s", buyerName, offer, qty, kind)
	}
	if reason != "" {
		fmt.Fprintf(&b, ". Reason: %s", reason)
	}
	b.WriteString(".")
	return b.String()
}

// payCounteredFactText renders a SalientFact for a counter.
//
//	buyerSide=true:  "Aldous countered my offer of 5 coins for 2 stew with 7 coins."
//	                  + " Note: <message>." when message non-empty.
//	buyerSide=false: "I countered Hannah's offer of 5 coins for 2 stew with 7 coins."
//	                  + " Note: <message>." when message non-empty.
//
// originalPayItems / counterPayItems fold the barter goods into the offer
// and counter phrases respectively (ZBBS-HOME-393), so a goods haggle reads
// "Aldous countered my offer of 5 nails for 2 stew with 6 nails."
func payCounteredFactText(buyerName, sellerName string, originalAmount int, originalPayItems []ItemKindQty, counterAmount int, counterPayItems []ItemKindQty, kind ItemKind, qty int, message string, buyerSide bool) string {
	offer := formatPayment(originalAmount, originalPayItems)
	counter := formatPayment(counterAmount, counterPayItems)
	var b strings.Builder
	if buyerSide {
		fmt.Fprintf(&b, "%s countered my offer of %s for %d %s with %s", sellerName, offer, qty, kind, counter)
	} else {
		fmt.Fprintf(&b, "I countered %s's offer of %s for %d %s with %s", buyerName, offer, qty, kind, counter)
	}
	if message != "" {
		fmt.Fprintf(&b, ". Note: %s", message)
	}
	b.WriteString(".")
	return b.String()
}
