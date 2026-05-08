package main

// Pay tool — coin transfer between villagers, with optional goods
// transfer and immediate-consumption side-effects (ZBBS-093 collapsed
// the separate buy() tool into this).
//
// Three flows controlled by the optional `item` parameter:
//
//   pay(target, amount)
//       Generic coin transfer — tip, service, news, gift. No goods,
//       no need-drop. Optional `for` is flavor text on the audit row.
//
//   pay(target, amount, item, qty, consume_now=true)
//       At-source consumption — the tavern verb. Decrements the
//       seller's stock by qty, applies the item's satisfies_amount to
//       the buyer's matching need (hunger/thirst). No inventory
//       transfer to the buyer. Works for portable AND non-portable
//       items (you can drink ale at the bar OR eat stew there).
//
//   pay(target, amount, item, qty, consume_now=false)
//       Take-home — the merchant flow. Validates the item is
//       portable, then moves qty units from seller to buyer's
//       inventory. No consumption (the buyer can `consume()` later).
//       Non-portable items reject with a clean error.
//
// `amount` is the negotiated coin total typed by the buyer after
// dialogue. There's no static price column (ZBBS-092). Supply
// pressure produces price pressure through conversation.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// payResult captures the outcome of an attempted pay so the dispatcher can
// build the audit-row (result, errStr) pair without duplicating switch logic.
//
// ZBBS-129 step 2 removed ItemTransferred / ItemConsumed / HungerReduction /
// ThirstReduction / TirednessReduce — pay-accept no longer transfers items
// or applies consumption (those moved to executeDeliverOrder). Item delivery
// fires its own broadcasts at deliver_order time.
type payResult struct {
	Result        string // "ok" | "rejected" | "failed"
	Err           string // human-readable, empty when Result == "ok"
	BuyerNewCoins int    // post-transfer coin balance for log/broadcast
	// Recipient bookkeeping (ZBBS-126 post-pay reactor). Populated only
	// when Result == "ok"; lets callers trigger a follow-up agent tick
	// on the recipient so they can speak a thanks/farewell after the
	// transaction lands. RecipientIsAgent is false for PC recipients and
	// for NPCs without llm_memory_agent set; callers gate the tick on it.
	RecipientID      string
	RecipientIsAgent bool
	// LedgerID (ZBBS-128) is the pay_ledger row id assigned to this
	// attempt. Zero on early arg-validation rejections (no recipient
	// name, can't pay yourself, etc.) where the schema's NOT NULL
	// seller_id can't be satisfied; populated for everything else.
	LedgerID int64
	// Message (ZBBS-128 step 3) carries the recipient's spoken reply
	// when the deliberation tick produces a decline reason or counter
	// message. Populated only when Result is "declined" or "countered";
	// empty for the mechanical paths (Result "ok"/"rejected"/"failed").
	Message string
	// CounterAmount (ZBBS-128 step 3) carries the recipient's proposed
	// new total when Result is "countered". Stored on the ledger row's
	// counter_amount column and read back by callers (e.g. the buyer's
	// next pay tool call) when extending the haggling chain. Zero for
	// every other Result kind.
	CounterAmount int
	// CommitUnknown (ZBBS-128) signals that Tx B's tx.Commit returned
	// an error — Postgres may have committed the transfer before the
	// network/connection failed, so the app can't authoritatively
	// say whether coins moved. The caller leaves the pay_ledger row
	// in 'pending' rather than stamping an authoritative-looking lie;
	// the aging sweep eventually flips it to 'withdrawn' and an
	// operator can reconcile from logs.
	CommitUnknown bool
}

// payRequest groups the pay arguments so executePay's signature stays
// readable as new optional parameters land.
//
// ConsumerNames (Phase C of sales-and-gifts): optional list of display
// names for at-source group orders (consume_now=true). When non-empty,
// the seller's stock is decremented by Qty*len(consumers), each named
// consumer's matching need is dropped, and the buyer pays the full
// negotiated Amount. Default empty → the buyer is the implicit single
// consumer (legacy at-source behavior). Take-home (consume_now=false)
// ignores ConsumerNames — the items go to the buyer's inventory and
// can be redistributed via give() in a future phase.
type payRequest struct {
	RecipientName string
	Amount        int
	ForText       string   // optional flavor text for audit
	Item          string   // optional item kind name
	Qty           int      // per consumer; defaults to 1 when Item is set
	ConsumeNow    bool     // tavern (true) vs take-home (false)
	ConsumerNames []string // at-source group order; empty → buyer
	// SceneID (ZBBS-128) is the cascade UUID this pay belongs to,
	// threaded through to pay_ledger.scene_id. Empty for callers
	// without a scene in scope (PC pay; the recipient's reactor tick
	// mints its own scene_id).
	SceneID string
	// InResponseTo (ZBBS-128 step 3) declares this pay as the buyer's
	// response to a prior `countered` ledger row, extending the
	// haggling chain. The new pending row links to the parent via
	// parent_id and increments depth. Validated in pre-Tx-A: the
	// referenced row must exist, be in state `countered`, share the
	// same buyer/seller pair, and be recent (within 1 hour of NOW).
	// Validation failures reject the pay before any ledger insert.
	// Zero (the default) means "root pay attempt; no parent."
	InResponseTo int64
}

// executePay carries out the transfer and any goods/consumption side-
// effects. Returns a payResult describing what happened. Never partial:
// if any leg fails, the transaction rolls back and the buyer keeps
// their coins.
//
// ZBBS-128 splits the work into four phases (step 2 introduced phases
// 1-3, step 3 added the deliberation gate at phase 2.5):
//
//  1. Pre-Tx-A identity resolution (recipient lookup, in_response_to
//     parent validation, buyer huddle, scene_quote snapshot). Failures
//     here are arg-validation-class rejections and don't produce a
//     pay_ledger row — the schema's NOT NULL seller_id can't be
//     satisfied without a resolved recipient anyway.
//  2. Tx A inserts a pending pay_ledger row capturing every attempt
//     with identifiable participants. From this point every return
//     path flows through either the deliberation handler or the
//     post-Tx-B update so the row gets a terminal state and
//     resolved_at stamp.
//  2.5. Deliberation gate (step 3) — for item purchases where the
//     offer doesn't match a quote (mismatch or no quote on file), fire
//     a synchronous LLM tick on an agent recipient. On decline /
//     counter, the ledger row flips terminal here and we skip Tx B
//     entirely. On accept (or lenient timeout-accept), fall through.
//     PC recipients, non-agent NPCs, pure coin transfers, and
//     quote-match pays all skip this gate.
//  3. Tx B (executePayTransfer below) holds the validation + transfer
//     logic, parameterized on the pre-Tx-A lookups so the recipient
//     and quote aren't re-fetched. Its returned payResult.Result maps
//     to the terminal ledger state: ok→accepted, rejected→declined,
//     failed→failed. Step 4 will introduce withdrawn (aging sweep)
//     through its own path.
func (app *App) executePay(ctx context.Context, buyer *agentNPCRow, req payRequest) payResult {
	if req.Amount < 0 {
		return payResult{Result: "rejected", Err: "amount cannot be negative"}
	}
	// pay_ledger.offered_amount is `integer` (int32). Reject before
	// the ledger insert so an oversized amount surfaces as a clean
	// rejection, not a mystery DB constraint error. ZBBS-124's int
	// arithmetic also doesn't expect amounts that large.
	if req.Amount > math.MaxInt32 {
		return payResult{Result: "rejected", Err: "amount too large"}
	}
	recipientName := strings.TrimSpace(req.RecipientName)
	if recipientName == "" {
		return payResult{Result: "rejected", Err: "missing recipient"}
	}
	itemKind := strings.TrimSpace(strings.ToLower(req.Item))
	qty := req.Qty
	if itemKind != "" && qty <= 0 {
		qty = 1
	}
	// pay_ledger.qty is `integer` (int32). Same rationale as the
	// amount guard above — reject huge qty values before the ledger
	// insert so we don't silently wrap and log wrong quantities.
	if qty > math.MaxInt32 {
		return payResult{Result: "rejected", Err: "quantity too large"}
	}

	// Phase C: ConsumerNames is at-source only. Reject early if the
	// model paired it with take-home — the semantics ("buy take-home for
	// these other people") aren't supported and would silently fall
	// through to a buyer-credit if we let it pass.
	if len(req.ConsumerNames) > 0 && (!req.ConsumeNow || itemKind == "") {
		return payResult{
			Result: "rejected",
			Err:    "consumers is only valid for at-source group orders (consume_now=true with an item). For take-home, omit consumers — the goods go to your inventory.",
		}
	}

	// Pre-Tx-A: recipient lookup. After ZBBS-084 the unified actor
	// table holds every villager. Also pull llm_memory_agent so the
	// caller's post-pay reactor tick (ZBBS-126) knows whether the
	// recipient is agent-driven, and break_until for the closed-shop
	// gate below. No FOR UPDATE here — the tx that actually moves
	// coins (executePayTransfer below) doesn't read any recipient
	// field for validation, so we don't need to hold a lock between
	// the lookup and the credit UPDATE.
	var (
		recipientID        string
		recipientAgentName sql.NullString
		recipientBreakUntil sql.NullTime
	)
	err := app.DB.QueryRow(ctx,
		`SELECT id, llm_memory_agent, break_until FROM actor
		 WHERE LOWER(display_name) = LOWER($1)
		 LIMIT 1`,
		recipientName,
	).Scan(&recipientID, &recipientAgentName, &recipientBreakUntil)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return payResult{Result: "rejected", Err: fmt.Sprintf("no villager named %q", recipientName)}
		}
		return payResult{Result: "failed", Err: fmt.Sprintf("look up recipient: %v", err)}
	}
	if recipientID == buyer.ID {
		return payResult{Result: "rejected", Err: "cannot pay yourself"}
	}

	// Closed-shop gate. A vendor on take_break shouldn't be
	// accepting new item orders that will then sit at
	// fulfillment_status='ready' until the break ends — buyers
	// pile up paid-but-undelivered rows against a closed shop, and
	// observed 2026-05-08 04:01-06:04 UTC: John Ellis took
	// "closing for the night" at 03:03, then accepted 5 stew/water/
	// bread orders from Ezekiel that all wedged at ready until the
	// 07:01 deliver_order rejected on co-location.
	//
	// Bypasses:
	//   - Pure coin transfers (itemKind == ""): tips, gifts, news
	//     payments, condolences. No shop-hours semantic.
	//   - nights_stay (service capability): advance bookings cross
	//     "closing for the night" by design. executeDeliverOrder's
	//     check-in-hour gate handles fulfillment timing separately.
	if itemKind != "" && itemKind != "nights_stay" &&
		recipientBreakUntil.Valid && recipientBreakUntil.Time.After(time.Now()) {
		whenBack := formatBreakUntilLocal(ctx, app, recipientBreakUntil.Time)
		return payResult{
			Result: "rejected",
			Err:    fmt.Sprintf("%s is closed (back at %s) — come back later.", recipientName, whenBack),
		}
	}

	// Pre-Tx-A: in_response_to validation (ZBBS-128 step 3). When the
	// buyer declares this pay as a response to a prior countered row,
	// look up the parent and verify it's a legitimate continuation:
	//   - row exists
	//   - state is `countered` (not pending/accepted/declined/etc.)
	//   - same buyer/seller pair (no chain hijacking)
	//   - depth below the cap (defensive — chains naturally bound, but
	//     bad historical / manually-edited data shouldn't extend forever)
	//   - fresh (within 1 hour) so a buyer can't dig up an old chain.
	//     Freshness is computed against DB time (NOW() in SQL) to avoid
	//     app/DB clock-drift inconsistencies and to reject future-stamped
	//     rows that would otherwise pass with negative time.Since.
	// On success, capture parent_id and depth = parent.depth + 1 for
	// the pending row insert below. Failures reject before any ledger
	// write — the buyer's offered amount is preserved (they can pay
	// again as a root attempt if they want).
	var parentID sql.NullInt64
	var newRowDepth int
	var parentCounterAmount sql.NullInt32
	if req.InResponseTo > 0 {
		var (
			parentState    string
			parentBuyerID  string
			parentSellerID string
			parentRowDepth int
			parentFresh    bool
		)
		err := app.DB.QueryRow(ctx,
			`SELECT state, buyer_id::text, seller_id::text, depth, counter_amount,
			        (created_at BETWEEN NOW() - INTERVAL '1 hour' AND NOW()) AS fresh
			   FROM pay_ledger WHERE id = $1`,
			req.InResponseTo,
		).Scan(&parentState, &parentBuyerID, &parentSellerID, &parentRowDepth, &parentCounterAmount, &parentFresh)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return payResult{Result: "rejected", Err: fmt.Sprintf("in_response_to %d: no such pay_ledger row", req.InResponseTo)}
			}
			return payResult{Result: "failed", Err: fmt.Sprintf("lookup parent ledger: %v", err)}
		}
		if parentState != "countered" {
			return payResult{Result: "rejected", Err: fmt.Sprintf("in_response_to %d: parent state is %q, not 'countered'", req.InResponseTo, parentState)}
		}
		if parentBuyerID != buyer.ID {
			return payResult{Result: "rejected", Err: fmt.Sprintf("in_response_to %d: parent's buyer doesn't match", req.InResponseTo)}
		}
		if parentSellerID != recipientID {
			return payResult{Result: "rejected", Err: fmt.Sprintf("in_response_to %d: parent's seller doesn't match", req.InResponseTo)}
		}
		if parentRowDepth >= payDeliberationMaxDepth {
			return payResult{Result: "rejected", Err: fmt.Sprintf("in_response_to %d: counter chain is already at max depth (%d)", req.InResponseTo, payDeliberationMaxDepth)}
		}
		if !parentFresh {
			return payResult{Result: "rejected", Err: fmt.Sprintf("in_response_to %d: parent is too old to extend (chain expires after 1 hour)", req.InResponseTo)}
		}
		parentID = sql.NullInt64{Int64: req.InResponseTo, Valid: true}
		newRowDepth = parentRowDepth + 1
	}

	// Pre-Tx-A: buyer's current huddle, used both as the ledger row's
	// huddle_id and as the scope for the scene_quote lookup below.
	//
	// Behavior change vs ZBBS-124: the OLD code read this inside the
	// transfer tx while the buyer was FOR UPDATE-locked, transitively
	// serializing concurrent huddle-change txs against pay txs.
	// Reading pre-Tx-A (no lock) opens a microsecond window where
	// the buyer could change huddles between the snapshot and the
	// transfer. In practice this is theoretical — pay() runs in
	// milliseconds and huddle changes are PC-driven — and the
	// snapshot semantics are reasonable history (the ledger captures
	// where the buyer was when they tried to pay). If a concurrent
	// huddle change ever produces a surprising quote-mismatch,
	// revisit by re-reading huddle inside Tx B and updating the
	// ledger's quoted_unit_amount in the post-Tx-B handler.
	var buyerHuddle sql.NullString
	if err := app.DB.QueryRow(ctx,
		`SELECT current_huddle_id FROM actor WHERE id = $1`,
		buyer.ID,
	).Scan(&buyerHuddle); err != nil {
		return payResult{Result: "failed", Err: fmt.Sprintf("lookup buyer huddle: %v", err)}
	}

	// Pre-Tx-A: phase-C consumer resolution (ZBBS-130). For at-source
	// group orders the buyer names other consumers ("Jefferey buys 4
	// ales for himself + 3 friends"). Resolve names → actor IDs at
	// pay-accept and stash them on the ledger row so executeDeliverOrder
	// can apply consumption to the right people without re-resolving
	// names that may have drifted. Co-location is validated here
	// (consumers must share the buyer's huddle at pay-time); not
	// re-checked at deliver time. Returns nil for the legacy single-
	// consumer flow (the buyer is the implicit consumer at delivery).
	var consumerActorIDs []string
	if itemKind != "" && req.ConsumeNow && len(req.ConsumerNames) > 0 {
		ids, rejectErr := app.resolveConsumerIDsForPay(ctx, buyer, buyerHuddle, req.ConsumerNames)
		if rejectErr != "" {
			return payResult{Result: "rejected", Err: rejectErr}
		}
		consumerActorIDs = ids
	}

	// Pre-Tx-A: scene_quote snapshot. Same opportunistic semantics as
	// before — a lookup error logs and proceeds as if no quote were on
	// file. Snapshotted onto the ledger row's quoted_unit_amount and
	// reused inside Tx B for quote enforcement so we don't re-query.
	var quotedUnit sql.NullInt32
	if buyerHuddle.Valid && itemKind != "" {
		q, ok, qErr := app.lookupSceneQuote(ctx, buyerHuddle.String, recipientID, itemKind)
		if qErr != nil {
			log.Printf("scene_quote lookup (huddle=%s recipient=%s item=%s): %v",
				buyerHuddle.String, recipientID, itemKind, qErr)
		} else if ok {
			// pay_ledger.quoted_unit_amount is `integer` (int32). The
			// scene_quote source column is also `integer` so values
			// above MaxInt32 shouldn't be storable, but guard the
			// cast defensively — silently wrapping would produce a
			// negative `required` in quote enforcement below.
			if q > math.MaxInt32 {
				log.Printf("scene_quote value too large (huddle=%s recipient=%s item=%s quoted=%d) — proceeding without quote snapshot",
					buyerHuddle.String, recipientID, itemKind, q)
			} else {
				quotedUnit = sql.NullInt32{Int32: int32(q), Valid: true}
			}
		}
	}

	// ZBBS-138: item_kind existence pre-check. The pay_ledger row's
	// FK to item_kind fires inside insertPayLedgerPending below if
	// the buyer named a non-existent item — surfacing the raw
	// "violates foreign key constraint pay_ledger_item_kind_fkey"
	// SQLSTATE 23503 to the LLM as a tool result is ugly and obscures
	// the actual problem (model hallucinated an item). The Tx-B item
	// validation at line ~660 already produces the clean
	// `no such item "%s"` rejection but only runs after the ledger
	// insert, so it never gets reached on FK miss. Pre-check here so
	// the rejection path is the same regardless of which gate trips.
	if itemKind != "" {
		var exists bool
		if err := app.DB.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM item_kind WHERE name = $1)`,
			itemKind,
		).Scan(&exists); err != nil {
			return payResult{Result: "failed", Err: fmt.Sprintf("look up item: %v", err)}
		}
		if !exists {
			return payResult{Result: "rejected", Err: fmt.Sprintf("no such item %q", itemKind)}
		}
	}

	// Tx A: pending ledger row. Nullable columns get NULL for the
	// pure-coin-transfer surface (no item_kind/qty), the no-scene
	// surface (req.SceneID empty for PC pay), and the no-quote
	// surface (quotedUnit.Valid == false).
	var sceneIDArg sql.NullString
	if req.SceneID != "" {
		sceneIDArg = sql.NullString{String: req.SceneID, Valid: true}
	}
	var itemKindArg sql.NullString
	if itemKind != "" {
		itemKindArg = sql.NullString{String: itemKind, Valid: true}
	}
	var qtyArg sql.NullInt32
	if itemKind != "" {
		qtyArg = sql.NullInt32{Int32: int32(qty), Valid: true}
	}
	ledgerID, err := app.insertPayLedgerPending(ctx, payLedgerInsert{
		BuyerID:          buyer.ID,
		SellerID:         recipientID,
		HuddleID:         buyerHuddle,
		SceneID:          sceneIDArg,
		ItemKind:         itemKindArg,
		Qty:              qtyArg,
		OfferedAmount:    req.Amount,
		QuotedUnitAmount: quotedUnit,
		ConsumeNow:       req.ConsumeNow,
		ParentID:         parentID,
		Depth:            newRowDepth,
		ConsumerActorIDs: consumerActorIDs,
	})
	if err != nil {
		log.Printf("pay_ledger insert (buyer=%s recipient=%s amount=%d): %v",
			buyer.ID, recipientID, req.Amount, err)
		return payResult{Result: "failed", Err: fmt.Sprintf("insert pay_ledger: %v", err)}
	}

	// Pre-Tx-B deliberation gate (ZBBS-128 step 3). For item purchases
	// where the offer doesn't match a quote (mismatch or no quote on
	// file at all), fire a synchronous LLM tick on the recipient with
	// accept_pay/decline_pay/counter_pay. No DB locks held during the
	// call — the pending ledger row is the only persistent state.
	//
	// Skipped entirely for:
	//   - pure coin transfers (itemKind == ""): tips/gifts stay snappy
	//   - PC recipients / non-agent NPCs (no LLM agent to ask): silent
	//     accept matches today's behavior
	//   - quote-match offers (offer ≥ quoted * totalQty): fast path,
	//     Tx B handles the transfer directly
	//
	// On accept (or timeout-accept), fall through to Tx B. On decline
	// or counter, skip Tx B entirely — the recipient has spoken via
	// broadcastDeliberationSpeak, and the ledger row flips to declined
	// or countered with the reply text in `message` (and counter_amount
	// for the countered case).
	consumerCount := 1
	if req.ConsumeNow && len(req.ConsumerNames) > 0 {
		consumerCount = len(req.ConsumerNames)
	}
	totalQty := qty * consumerCount
	// Determine if deliberation is needed:
	//   - Pure coin transfer (no item): never deliberate.
	//   - Counter-response (parent_id set, parent had counter_amount):
	//     the relevant comparison is the parent's counter total, not
	//     the original quote. Buyer paying ≥ counter is acceptance —
	//     fast path through Tx B. Below the counter is a counter-counter,
	//     deliberate with the parent counter as context.
	//   - Root pay with no quote on file for an item: defer to LLM.
	//   - Root pay below original quote: defer to LLM.
	//   - Root pay at/above quote: fast path.
	deliberate := false
	if itemKind != "" {
		if parentCounterAmount.Valid {
			if req.Amount < int(parentCounterAmount.Int32) {
				deliberate = true
			}
		} else if !quotedUnit.Valid {
			deliberate = true
		} else if req.Amount < int(quotedUnit.Int32)*totalQty {
			deliberate = true
		}
	}
	recipientIsAgent := recipientAgentName.Valid && recipientAgentName.String != ""
	if deliberate && recipientIsAgent {
		quoted := 0
		if quotedUnit.Valid {
			quoted = int(quotedUnit.Int32)
		}
		// Counter chain depth: root pays land at depth 0, each response
		// to a counter increments. payDeliberationMaxDepth caps the
		// recipient's escalation: at depth 0/1/2 they may emit
		// counter_pay (creating a depth-1/2/3 child); at depth 3 they
		// must accept or decline (counter_pay is excluded from the tool
		// set). Max ledger depth is therefore 3, total chain length 4
		// (root + 3 counter rounds).
		includeCounter := newRowDepth < payDeliberationMaxDepth
		priorCounter := 0
		if parentCounterAmount.Valid {
			priorCounter = int(parentCounterAmount.Int32)
		}
		decision := app.runPayDeliberation(
			ctx,
			recipientAgentName.String,
			recipientName,
			buyer.DisplayName,
			itemKind,
			totalQty,
			req.Amount,
			quoted,
			quotedUnit.Valid,
			priorCounter,
			includeCounter,
		)
		switch decision.Outcome {
		case payDeliberationAccept, payDeliberationTimeoutAccept:
			// Fall through to Tx B normally.
		case payDeliberationDecline:
			app.broadcastDeliberationSpeak(ctx, recipientID, recipientName, decision.Reason)
			if uerr := app.updatePayLedger(ctx, ledgerID, "declined", decision.Reason, sql.NullInt32{}); uerr != nil {
				// Update failure means the row stays `pending` despite
				// the recipient having spoken. Returning "declined"
				// would mislead the caller into a terminal state that
				// isn't reflected on disk; report failure instead so
				// the audit row carries the right shape and the aging
				// sweep eventually flips the orphan to `withdrawn`.
				log.Printf("pay_ledger update (id=%d state=declined): %v — reporting failure to caller", ledgerID, uerr)
				return payResult{
					Result:           "failed",
					Err:              fmt.Sprintf("record decline outcome: %v", uerr),
					LedgerID:         ledgerID,
					BuyerNewCoins:    buyer.Coins,
					RecipientID:      recipientID,
					RecipientIsAgent: recipientIsAgent,
				}
			}
			// No transfer happened, so the buyer's coin balance is
			// unchanged. Snapshot it onto the result so the PC pay
			// response (and any audit consumer) sees the right number
			// instead of a default-zero that looks like a wipeout.
			return payResult{
				Result:           "declined",
				Err:              decision.Reason,
				Message:          decision.Reason,
				LedgerID:         ledgerID,
				BuyerNewCoins:    buyer.Coins,
				RecipientID:      recipientID,
				RecipientIsAgent: recipientIsAgent,
			}
		case payDeliberationCounter:
			// Sanity-cap the counter amount before squeezing into the
			// schema's int32 column. Tool-driven inputs already pass
			// through coerceIntInput's positivity check, but a value
			// at/above MaxInt32 would wrap silently here. Degrade to a
			// generic decline rather than ship a bogus counter price.
			if decision.NewAmount <= 0 || decision.NewAmount > math.MaxInt32 {
				log.Printf("pay-deliberation: %s emitted out-of-range counter amount %d — degrading to decline",
					recipientName, decision.NewAmount)
				fallback := "I'd rather not sell at that price."
				app.broadcastDeliberationSpeak(ctx, recipientID, recipientName, fallback)
				if uerr := app.updatePayLedger(ctx, ledgerID, "declined", fallback, sql.NullInt32{}); uerr != nil {
					log.Printf("pay_ledger update (id=%d state=declined): %v — reporting failure to caller", ledgerID, uerr)
					return payResult{
						Result:           "failed",
						Err:              fmt.Sprintf("record decline outcome: %v", uerr),
						LedgerID:         ledgerID,
						BuyerNewCoins:    buyer.Coins,
						RecipientID:      recipientID,
						RecipientIsAgent: recipientIsAgent,
					}
				}
				return payResult{
					Result:           "declined",
					Err:              fallback,
					Message:          fallback,
					LedgerID:         ledgerID,
					BuyerNewCoins:    buyer.Coins,
					RecipientID:      recipientID,
					RecipientIsAgent: recipientIsAgent,
				}
			}
			app.broadcastDeliberationSpeak(ctx, recipientID, recipientName, decision.Message)
			counterAmt := sql.NullInt32{Int32: int32(decision.NewAmount), Valid: true}
			if uerr := app.updatePayLedger(ctx, ledgerID, "countered", decision.Message, counterAmt); uerr != nil {
				// Same rationale as the decline path — don't lie to the
				// caller about a terminal state when the row is still
				// pending. A countered-but-pending row would be worse
				// than a declined-but-pending one because the buyer
				// might retry with in_response_to=ledgerID and immediately
				// get rejected for "parent state is pending."
				log.Printf("pay_ledger update (id=%d state=countered): %v — reporting failure to caller", ledgerID, uerr)
				return payResult{
					Result:           "failed",
					Err:              fmt.Sprintf("record counter outcome: %v", uerr),
					LedgerID:         ledgerID,
					BuyerNewCoins:    buyer.Coins,
					RecipientID:      recipientID,
					RecipientIsAgent: recipientIsAgent,
				}
			}
			return payResult{
				Result:           "countered",
				Err:              decision.Message,
				Message:          decision.Message,
				CounterAmount:    decision.NewAmount,
				LedgerID:         ledgerID,
				BuyerNewCoins:    buyer.Coins,
				RecipientID:      recipientID,
				RecipientIsAgent: recipientIsAgent,
			}
		}
	}

	// Tx B: existing validation + transfer.
	result := app.executePayTransfer(ctx, buyer, req, payTxContext{
		RecipientName:      recipientName,
		RecipientID:        recipientID,
		RecipientAgentName: recipientAgentName,
		ItemKind:           itemKind,
		Qty:                qty,
		QuotedUnit:         quotedUnit,
	})

	// Post-Tx-B: terminal state. ok→accepted, rejected→declined,
	// failed→failed. Defensive default for any future result kind
	// flips the row out of pending so the aging sweep doesn't pick
	// it up.
	//
	// CommitUnknown short-circuits the update entirely: when Tx B's
	// tx.Commit returns an error, Postgres may have committed the
	// transfer before the connection failed, so the app can't
	// authoritatively label the row 'accepted' or 'failed'. We log
	// loudly and leave the row 'pending'; the aging sweep eventually
	// flips it to 'withdrawn' (also wrong-ish but matches "we don't
	// know"), and an operator can reconcile from logs.
	if result.CommitUnknown {
		log.Printf("pay_ledger commit outcome unknown (id=%d buyer=%s recipient=%s amount=%d): %s — row left pending for ops review",
			ledgerID, buyer.ID, recipientID, req.Amount, result.Err)
	} else {
		var terminalState string
		switch result.Result {
		case "ok":
			terminalState = "accepted"
		case "rejected":
			terminalState = "declined"
		case "failed":
			terminalState = "failed"
		default:
			terminalState = "failed"
		}
		if uerr := app.updatePayLedger(ctx, ledgerID, terminalState, result.Err, sql.NullInt32{}); uerr != nil {
			// Bookkeeping inconsistency: aging sweep will eventually
			// flip the row to withdrawn even if the transfer
			// succeeded. Better to report the actual transfer
			// outcome than to fail the pay because of a journaling
			// miss.
			log.Printf("pay_ledger update (id=%d state=%s): %v", ledgerID, terminalState, uerr)
		}
	}

	result.LedgerID = ledgerID
	return result
}

// payTxContext carries the values executePayTransfer needs from the
// pre-Tx-A resolution stage. Keeping them in a struct makes the
// helper's dependencies obvious vs. recomputing from req.
type payTxContext struct {
	RecipientName      string         // trimmed display name (for error messages)
	RecipientID        string         // resolved actor.id
	RecipientAgentName sql.NullString // for RecipientIsAgent flag
	ItemKind           string         // trimmed/lowered req.Item
	Qty                int            // resolved qty (defaults to 1 when itemKind != "" and req.Qty <= 0)
	QuotedUnit         sql.NullInt32  // scene_quote snapshot, NULL when no quote
}

// executePayTransfer runs the validation + transfer transaction.
// Inputs come from executePay's pre-Tx-A resolution so the recipient
// and quote aren't re-fetched. Returns a payResult; the caller maps
// Result to a pay_ledger terminal state.
func (app *App) executePayTransfer(ctx context.Context, buyer *agentNPCRow, req payRequest, pctx payTxContext) payResult {
	recipientName := pctx.RecipientName
	recipientID := pctx.RecipientID
	recipientAgentName := pctx.RecipientAgentName
	itemKind := pctx.ItemKind
	qty := pctx.Qty

	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return payResult{Result: "failed", Err: fmt.Sprintf("begin tx: %v", err)}
	}
	defer tx.Rollback(ctx)

	// Lock the buyer row so a concurrent pay from the same NPC can't
	// race us into a negative balance.
	var buyerCoins int
	if err := tx.QueryRow(ctx,
		`SELECT coins FROM actor WHERE id = $1 FOR UPDATE`,
		buyer.ID,
	).Scan(&buyerCoins); err != nil {
		return payResult{Result: "failed", Err: fmt.Sprintf("lock buyer: %v", err)}
	}
	if buyerCoins < req.Amount {
		return payResult{Result: "rejected", Err: fmt.Sprintf("insufficient coins (have %d, need %d)", buyerCoins, req.Amount)}
	}

	// Recipient was resolved pre-Tx-A; no re-lookup or explicit lock.
	// The credit UPDATE below acquires its own row-level lock when it
	// executes, and we don't read any recipient field for validation
	// in this tx, so the pre-ZBBS-128 explicit FOR UPDATE on the
	// recipient row was redundant.

	// Validate item if provided. Pull capabilities from item_kind, and
	// the multi-attribute satisfactions from item_satisfies (ZBBS-125).
	// Materials (wheat, flour, iron) have an item_kind row but no
	// item_satisfies rows — same "not a consumable" rejection as before.
	var (
		itemSatisfactions []itemSatisfaction
		itemCapabilities  []string
	)
	if itemKind != "" {
		err := tx.QueryRow(ctx,
			`SELECT capabilities FROM item_kind WHERE name = $1`,
			itemKind,
		).Scan(&itemCapabilities)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return payResult{Result: "rejected", Err: fmt.Sprintf("no such item %q", itemKind)}
			}
			return payResult{Result: "failed", Err: fmt.Sprintf("look up item: %v", err)}
		}
		itemSatisfactions, err = loadItemSatisfactions(ctx, tx, itemKind)
		if err != nil {
			return payResult{Result: "failed", Err: fmt.Sprintf("load satisfactions: %v", err)}
		}

		// Take-home flow needs the item to be portable. Non-portables
		// (stew, water at present) get rejected with a clean error so
		// the LLM can either retry with consume_now=true or drop the
		// take-home framing.
		if !req.ConsumeNow && !hasCapability(itemCapabilities, "portable") {
			return payResult{Result: "rejected", Err: fmt.Sprintf("%s cannot be carried; consume at source with consume_now=true", itemKind)}
		}

		// At-source flow needs the item to be a consumable. Materials
		// have an item_kind row but no item_satisfies rows; you can't
		// "consume" raw flour at the merchant's stand. The buyer needs
		// to take it home (consume_now=false) and use it later.
		//
		// Exception: items with the `service` capability (e.g. nights_stay)
		// pass through with no satisfactions — they materialize status
		// from the ledger row at query time rather than dropping a need
		// at purchase. ZBBS-131 introduced the tag.
		if req.ConsumeNow && len(itemSatisfactions) == 0 && !hasCapability(itemCapabilities, "service") {
			return payResult{Result: "rejected", Err: fmt.Sprintf("%s isn't consumable; set consume_now=false to take it home", itemKind)}
		}

		// Service items (ZBBS-131) bypass the stock check + decrement.
		// nights_stay et al don't carry actor_inventory rows; their
		// "stock" is the structure / time / keeper's willingness, not a
		// quantity column. The ledger row IS the artifact of the sale.
		if !hasCapability(itemCapabilities, "service") {
			// Lock the seller's inventory row. NoRows = seller doesn't
			// stock this item; cleaner error than the FK noise.
			var sellerQty int
			if err := tx.QueryRow(ctx,
				`SELECT quantity FROM actor_inventory
				  WHERE actor_id = $1 AND item_kind = $2
				  FOR UPDATE`,
				recipientID, itemKind,
			).Scan(&sellerQty); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return payResult{Result: "rejected", Err: fmt.Sprintf("%s has no %s to sell", recipientName, itemKind)}
				}
				return payResult{Result: "failed", Err: fmt.Sprintf("lock seller inventory: %v", err)}
			}
			// Phase C: at-source group orders multiply the unit qty by the
			// number of consumers (default 1 = buyer-only). Take-home stays
			// at qty; the buyer's inventory is credited the requested
			// amount regardless of who else might eat it later.
			consumerCount := 1
			if req.ConsumeNow && len(req.ConsumerNames) > 0 {
				consumerCount = len(req.ConsumerNames)
			}
			totalQty := qty * consumerCount
			if sellerQty < totalQty {
				return payResult{Result: "rejected", Err: fmt.Sprintf("%s has only %d %s (asked for %d)", recipientName, sellerQty, itemKind, totalQty)}
			}

			// Quote enforcement (ZBBS-124, refactored ZBBS-128 step 3).
			// Underpayment / no-quote-on-file rejection is now handled
			// pre-Tx-B via the deliberation gate in executePay. By the time
			// Tx B runs we know either (a) the offer matched the quote and
			// no LLM tick was needed, or (b) deliberation accepted (real or
			// timeout-accept), in which case the recipient has agreed to
			// the offered amount regardless of the original quote. Either
			// way, no quote check belongs here. pctx.QuotedUnit is still
			// snapshotted on the ledger row for audit, just not enforced.
			newSellerQty := sellerQty - totalQty
			if newSellerQty == 0 {
				if _, err := tx.Exec(ctx,
					`DELETE FROM actor_inventory WHERE actor_id = $1 AND item_kind = $2`,
					recipientID, itemKind,
				); err != nil {
					return payResult{Result: "failed", Err: fmt.Sprintf("delete seller stock: %v", err)}
				}
			} else {
				if _, err := tx.Exec(ctx,
					`UPDATE actor_inventory SET quantity = $1
					  WHERE actor_id = $2 AND item_kind = $3`,
					newSellerQty, recipientID, itemKind,
				); err != nil {
					return payResult{Result: "failed", Err: fmt.Sprintf("decrement seller stock: %v", err)}
				}
			}
		}
	}

	// Coin transfer. Two UPDATEs serialized by the FOR UPDATE locks
	// above. Skipped when amount is zero (a "free" gift / sample).
	if req.Amount > 0 {
		if _, err := tx.Exec(ctx, `UPDATE actor SET coins = coins - $1 WHERE id = $2`, req.Amount, buyer.ID); err != nil {
			return payResult{Result: "failed", Err: fmt.Sprintf("debit buyer: %v", err)}
		}
		if _, err := tx.Exec(ctx, `UPDATE actor SET coins = coins + $1 WHERE id = $2`, req.Amount, recipientID); err != nil {
			return payResult{Result: "failed", Err: fmt.Sprintf("credit recipient: %v", err)}
		}
	}

	// ZBBS-129 step 2: item-to-inventory and at-source consumption no
	// longer fire here. Goods stay on a `state=accepted,
	// fulfillment_status=ready` ledger row until the seller calls
	// deliver_order(ledger_id), at which point executeDeliverOrder
	// either credits the buyer's inventory (take-home) or applies
	// applyConsumption to each consumer (at-source). Seller stock has
	// already been decremented above to reserve the units the buyer
	// claimed at pay-accept.

	if err := tx.Commit(ctx); err != nil {
		// CommitUnknown: Postgres may have committed before the
		// network/connection failed. The caller leaves the pay_ledger
		// row pending rather than stamping an authoritative-looking
		// terminal state.
		return payResult{
			Result:        "failed",
			Err:           fmt.Sprintf("commit tx: %v", err),
			CommitUnknown: true,
		}
	}

	// Hub broadcast. Single npc_paid event covers both coin and item
	// flows; downstream consumers branch on item != "" if they care.
	// Need-after values come from the post-update consumeResult so
	// listeners don't have to recompute deltas.
	app.Hub.Broadcast(WorldEvent{
		Type: "npc_paid",
		Data: map[string]interface{}{
			"buyer":        buyer.DisplayName,
			"buyer_id":     buyer.ID,
			"recipient":    recipientName,
			"recipient_id": recipientID,
			"amount":       req.Amount,
			"for":          req.ForText,
			"item":         itemKind,
			"qty":          qty,
			"consume_now":  req.ConsumeNow,
			"at":           time.Now().UTC().Format(time.RFC3339),
		},
	})

	// Seller's inventory broadcast — their stock decremented above. Buyer
	// inventory broadcasts and per-consumer npc_needs_changed move to
	// executeDeliverOrder where the actual transfer / consumption fires.
	// Service items (ZBBS-131) skip this — their "stock" wasn't touched.
	if itemKind != "" && !hasCapability(itemCapabilities, "service") {
		app.Hub.Broadcast(WorldEvent{
			Type: "actor_inventory_changed",
			Data: map[string]any{
				"actor_id":  recipientID,
				"item_kind": itemKind,
			},
		})
	}

	return payResult{
		Result:           "ok",
		BuyerNewCoins:    buyerCoins - req.Amount,
		RecipientID:      recipientID,
		RecipientIsAgent: recipientAgentName.Valid && strings.TrimSpace(recipientAgentName.String) != "",
	}
}

// resolveConsumerIDsForPay (ZBBS-130) is the pre-Tx-A consumer name →
// actor.id resolver. Each named consumer must (a) exist in the actor
// table and (b) share the buyer's current_huddle_id at pay-accept time
// (you can't fund an ale for someone in another room). Returns the
// list of resolved IDs in input order (deduped case-insensitively, with
// any reference to the buyer's own name collapsed to buyer.ID).
//
// No locks: pay-accept is a snapshot-style validation. Consumption
// fires later in executeDeliverOrder, which re-locks each consumer row
// for the actual applyConsumption call. A consumer who walks out of
// the room between pay-accept and delivery doesn't get blocked here.
//
// The empty-string second return is the success path; a non-empty
// string is a rejection message the caller should surface as a
// payResult.
func (app *App) resolveConsumerIDsForPay(ctx context.Context, buyer *agentNPCRow, buyerHuddle sql.NullString, names []string) ([]string, string) {
	if !buyerHuddle.Valid {
		return nil, "consumers requires you to be in a room with them; you're not in a scene huddle right now"
	}

	// Normalize: trim, drop empties, dedupe (case-insensitive).
	seen := make(map[string]bool, len(names))
	var clean []string
	for _, n := range names {
		t := strings.TrimSpace(n)
		if t == "" {
			continue
		}
		k := strings.ToLower(t)
		if seen[k] {
			continue
		}
		seen[k] = true
		clean = append(clean, t)
	}
	if len(clean) == 0 {
		return nil, ""
	}

	resolved := make([]string, 0, len(clean))
	for _, name := range clean {
		var cid string
		var chuddle sql.NullString
		err := app.DB.QueryRow(ctx,
			`SELECT id, current_huddle_id
			   FROM actor
			  WHERE LOWER(display_name) = LOWER($1)
			  LIMIT 1`,
			name,
		).Scan(&cid, &chuddle)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, fmt.Sprintf("no villager named %q", name)
			}
			return nil, fmt.Sprintf("look up consumer %q: %v", name, err)
		}
		if !chuddle.Valid || chuddle.String != buyerHuddle.String {
			return nil, fmt.Sprintf("%s is not in the room with you", name)
		}
		resolved = append(resolved, cid)
	}
	return resolved, ""
}

// hasCapability reports whether the given capability tag is in the
// item's capabilities array.
func hasCapability(capabilities []string, want string) bool {
	for _, c := range capabilities {
		if c == want {
			return true
		}
	}
	return false
}

// formatBreakUntilLocal renders a break_until timestamp in the world's
// wall-clock for inclusion in a closed-shop rejection message. Falls
// back to a UTC formatting on world-config load failure so the buyer
// still gets a meaningful "back at HH:MM" cue rather than a raw RFC3339.
func formatBreakUntilLocal(ctx context.Context, app *App, breakUntil time.Time) string {
	cfg, err := app.loadWorldConfig(ctx)
	if err == nil && cfg != nil && cfg.Location != nil {
		return breakUntil.In(cfg.Location).Format("3:04 PM")
	}
	return breakUntil.UTC().Format("15:04 UTC")
}
