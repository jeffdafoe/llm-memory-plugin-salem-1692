package cascade

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// action_log.go — append-only in-memory action log substrate driver.
// Wires seventeen event subscribers (Spoke / Paid / PayOfferReceived /
// PayWithItemResolved / PayCountered / ItemConsumed / ItemGathered /
// SourceActivityStarted / OrderDelivered / ActorArrived / ActorLeftStructure /
// TookBreak / StayingOpen / LaborOfferReceived / LaborOfferAccepted /
// LaborResolved) to translate engine
// events into sim.ActionLogEntry rows, and spawns a sweep goroutine that
// periodically compacts the log via sim.CompactActionLog. PayWithItemResolved
// has two subscribers — one for the Accepted terminal (the settled sale) and one
// for the Declined terminal (LLM-283 feed-only negotiation beat).
//
// Subscribers run inline on the world goroutine via emit dispatch;
// the sweep goroutine runs off-world and routes mutations through
// SendContext. Same split as cascade/idle_backstop.go and the rest of
// the cascade slices.
//
// Lifecycle:
//
//   RegisterActionLog(ctx, w)
//   ├─> w.Subscribe(handleSpokeActionLog)
//   ├─> w.Subscribe(handlePaidActionLog)
//   ├─> w.Subscribe(handleOfferedActionLog)
//   ├─> w.Subscribe(handlePayResolvedActionLog)
//   ├─> w.Subscribe(handleDeclinedActionLog)
//   ├─> w.Subscribe(handleCounteredActionLog)
//   ├─> w.Subscribe(handleConsumedActionLog)
//   ├─> w.Subscribe(handleGatheredActionLog)
//   ├─> w.Subscribe(handleRepairingActionLog)
//   ├─> w.Subscribe(handleOrderDeliveredActionLog)
//   ├─> w.Subscribe(handleActorArrivedActionLog)
//   ├─> w.Subscribe(handleActorLeftStructureActionLog)
//   ├─> w.Subscribe(handleTookBreakActionLog)
//   ├─> w.Subscribe(handleStayedOpenActionLog)
//   ├─> w.Subscribe(handleSolicitedWorkActionLog)
//   ├─> w.Subscribe(handleHiredActionLog)
//   ├─> w.Subscribe(handleLaborResolvedActionLog)
//   └─> go runActionLogSweep(ctx, w)
//        ├─> immediate first compaction
//        └─> time.Ticker @ ActionLogSweepInterval until ctx.Done
//
// What this slice does NOT do:
//
//   - No HuddleJoined / HuddleConcluded subscriber. v1 logged
//     engine-source enter_huddle rows; v1's chronicler digest didn't
//     consume them, and v2's MVP consumers don't either. Add when a
//     concrete consumer wants it.
//   - Pay-ledger deliberation outcomes DO port (LLM-283): offered /
//     declined / countered rows drive the Village activity feed so a
//     multi-round haggle no longer reads as dead air. They are FEED-ONLY —
//     isNegotiationActionType filters them out of the atmosphere digest and
//     narrative consolidation so NPC behavior is unchanged. Gift offers /
//     declines (IsGift) are skipped: a one-way give isn't a purchase haggle
//     and would render with backwards buy-phrasing.
//   - No Summon / SummonFailed — chronicler-dispatch is gone in v2;
//     summon may never port.
//   - No durable sink wire-through. The repo's ActionLogSink stays a
//     noop; the cascade slice is purely in-memory + read-back inside
//     the engine.

// defaultActionLogSweepInterval is the fallback cadence when
// WorldSettings.ActionLogSweepInterval is unset. 1h gives detection
// latency ≤ 1h against the 48h retention default; entries past
// retention are still tens of hours stale, the sweep cadence just
// controls how promptly memory is reclaimed.
const defaultActionLogSweepInterval = 1 * time.Hour

// RegisterActionLog wires the sixteen event subscribers and spawns the
// compaction sweep goroutine. Must run on the world goroutine — call
// before World.Run, or from inside a Command.Fn.
//
// Idempotency: registering twice would double-write every action log
// entry. The substrate doesn't dedup at append time (the slice is
// append-only by contract). Wiring guards live at the registration
// site — Don't register twice.
//
// Panics on nil w (wiring guard, mirrors RegisterAtmosphere /
// RegisterConsolidation / RegisterIdleBackstop).
func RegisterActionLog(ctx context.Context, w *sim.World) {
	if w == nil {
		panic("cascade: RegisterActionLog requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handleSpokeActionLog))
	w.Subscribe(sim.SubscriberFunc(handlePaidActionLog))
	w.Subscribe(sim.SubscriberFunc(handleOfferedActionLog))
	w.Subscribe(sim.SubscriberFunc(handlePayResolvedActionLog))
	w.Subscribe(sim.SubscriberFunc(handleDeclinedActionLog))
	w.Subscribe(sim.SubscriberFunc(handleCounteredActionLog))
	w.Subscribe(sim.SubscriberFunc(handleConsumedActionLog))
	w.Subscribe(sim.SubscriberFunc(handleGatheredActionLog))
	w.Subscribe(sim.SubscriberFunc(handleRepairingActionLog))
	w.Subscribe(sim.SubscriberFunc(handleOrderDeliveredActionLog))
	w.Subscribe(sim.SubscriberFunc(handleActorArrivedActionLog))
	w.Subscribe(sim.SubscriberFunc(handleActorLeftStructureActionLog))
	w.Subscribe(sim.SubscriberFunc(handleTookBreakActionLog))
	w.Subscribe(sim.SubscriberFunc(handleStayedOpenActionLog))
	w.Subscribe(sim.SubscriberFunc(handleSolicitedWorkActionLog))
	w.Subscribe(sim.SubscriberFunc(handleHiredActionLog))
	w.Subscribe(sim.SubscriberFunc(handleLaborResolvedActionLog))
	// Cadence contract, phase one — default now, settings-resolved interval once the
	// goroutine can read it (LLM-395; see RegisterIdleBackstop).
	w.RegisterTicker("action_log", defaultActionLogSweepInterval)
	go runActionLogSweep(ctx, w)
}

// handleSpokeActionLog appends a row when a speak tool call commits.
// One row per speaker — recipients don't get their own rows (the
// action is "spoke", not "heard"; cross-actor narrative pickup
// happens via C2's same-huddle peer filter).
func handleSpokeActionLog(w *sim.World, evt sim.Event) {
	spoke, ok := evt.(*sim.Spoke)
	if !ok {
		return
	}
	entry := sim.ActionLogEntry{
		ActorID:    spoke.SpeakerID,
		OccurredAt: spoke.At,
		ActionType: sim.ActionTypeSpoke,
		Text:       spoke.Text,
		HuddleID:   spoke.HuddleID,
	}
	if _, err := sim.AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("cascade/action_log: append spoke (speaker %q event %d): %v",
			spoke.SpeakerID, spoke.EventID(), err)
		return
	}
	// Durable mirror (ZBBS-WORK-376): structured row for the agent_action_log
	// audit table that feeds the stateful NPCs' dream memory. payload.text is
	// the verbatim utterance — the distiller renders it as quoted dialogue.
	display, source := actorDisplayAndSource(w, spoke.SpeakerID)
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID:     spoke.SpeakerID,
		OccurredAt:  spoke.At,
		ActionType:  sim.ActionTypeSpoke,
		Payload:     map[string]any{"text": spoke.Text},
		SpeakerName: display,
		HuddleID:    spoke.HuddleID,
		Source:      source,
	})
}

// handlePaidActionLog appends a row when a pay tool call commits.
// One row, buyer side — pay is buyer-initiated. The seller's
// reactor already gets a PaidWarrantReason; cross-actor narrative
// pickup happens via the same-huddle peer filter (HuddleID stamps
// from the buyer's CurrentHuddleID, which the same-huddle pay gate
// guarantees the seller shares).
func handlePaidActionLog(w *sim.World, evt sim.Event) {
	paid, ok := evt.(*sim.Paid)
	if !ok {
		return
	}
	huddleID := sim.HuddleID("")
	if buyer, ok := w.Actors[paid.BuyerID]; ok {
		huddleID = buyer.CurrentHuddleID
	}
	entry := sim.ActionLogEntry{
		ActorID:    paid.BuyerID,
		OccurredAt: paid.At,
		ActionType: sim.ActionTypePaid,
		Text:       paid.ForText,
		HuddleID:   huddleID,
		// ZBBS-WORK-377: recipient (seller) + amount for the PC talk-panel
		// narration ("X pays Y N coins for Z."). Empty name → renderer drops
		// the recipient; the lean ring keeps only ForText otherwise.
		CounterpartyName: actorDisplayNameOrEmpty(w, paid.SellerID),
		Amount:           paid.Amount,
	}
	if _, err := sim.AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("cascade/action_log: append paid (buyer %q event %d): %v",
			paid.BuyerID, paid.EventID(), err)
		return
	}
	// Durable mirror (ZBBS-WORK-376). Buyer side: recipient is the seller's
	// display name; amount + for-text carry the transaction detail the lean
	// ring drops (ring keeps only ForText).
	display, source := actorDisplayAndSource(w, paid.BuyerID)
	payload := map[string]any{
		"recipient": actorDisplayName(w, paid.SellerID),
		"amount":    paid.Amount,
	}
	if paid.ForText != "" {
		payload["for"] = paid.ForText
	}
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID:     paid.BuyerID,
		OccurredAt:  paid.At,
		ActionType:  sim.ActionTypePaid,
		Payload:     payload,
		SpeakerName: display,
		HuddleID:    huddleID,
		Source:      source,
	})
}

// handlePayResolvedActionLog appends a Paid row when a pay-with-item ledger
// entry settles ACCEPTED (ZBBS-HOME-434). The Paid event only fires from the
// bare-coin sim.Pay command — since ZBBS-HOME-430 NPC commerce goes
// exclusively through the ledger, so without this bridge no transaction ever
// reached the action log: the Village activity tab and the dream-feeding
// durable log both showed speech and consumption but never the sale between
// them. Same buyer-side single-row shape as handlePaidActionLog; the
// renderer's existing ActionTypePaid case narrates it ("X pays Y N coins for
// Z."). Non-accepted terminals (declined / withdrawn / expired / failed_*)
// append nothing — no money moved.
func handlePayResolvedActionLog(w *sim.World, evt sim.Event) {
	resolved, ok := evt.(*sim.PayWithItemResolved)
	if !ok {
		return
	}
	if resolved.TerminalState != sim.PayTerminalStateAccepted {
		return
	}
	// Total quantity across consumers — the event carries per-consumer qty
	// (group orders), and the narrated beat should state the whole bundle.
	// Empty ConsumerIDs is the buyer-only shape (the common single-buyer
	// purchase snapshots no consumer list) and counts as one consumer.
	consumerCount := len(resolved.ConsumerIDs)
	if consumerCount == 0 {
		consumerCount = 1
	}
	var forText string
	if len(resolved.Lines) > 0 {
		// Bundle quote-take (LLM-101): list every line with its consumer-scaled
		// qty, e.g. "2x blueberries, 2x raspberries".
		for i, ln := range resolved.Lines {
			if i > 0 {
				forText += ", "
			}
			forText += formatItemQty(ln.ItemKind, ln.Qty*consumerCount)
		}
	} else {
		qty := resolved.QtyPerConsumer * consumerCount
		forText = formatItemQty(resolved.ItemKind, qty)
	}
	entry := sim.ActionLogEntry{
		ActorID:          resolved.BuyerID,
		OccurredAt:       resolved.At,
		ActionType:       sim.ActionTypePaid,
		Text:             forText,
		HuddleID:         resolved.HuddleID,
		CounterpartyName: actorDisplayNameOrEmpty(w, resolved.SellerID),
		Amount:           resolved.Amount,
		// Snapshot the goods leg into the ring entry rather than aliasing the
		// event's slice — the entry outlives the event by many ticks, so it must
		// own its data (mirrors sim.cloneItemKindQtys at the event/entry boundary).
		PayItems: append([]sim.ItemKindQty(nil), resolved.PayItems...),
	}
	if _, err := sim.AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("cascade/action_log: append pay-resolved (buyer %q ledger %d event %d): %v",
			resolved.BuyerID, resolved.LedgerID, resolved.EventID(), err)
		return
	}
	// Durable mirror (ZBBS-WORK-376) — same shape as the bare-pay row so the
	// dream distiller reads both identically.
	display, source := actorDisplayAndSource(w, resolved.BuyerID)
	payload := map[string]any{
		"recipient": actorDisplayName(w, resolved.SellerID),
		"amount":    resolved.Amount,
		"for":       forText,
	}
	// LLM-105: record the FULL settlement terms so the durable audit trail can tell a
	// paid sale from a barter from a zero-value give-away. `amount` alone is ambiguous
	// — a 0-coin barter and a 0-coin free gift both read amount:0; only the goods leg
	// distinguishes them. pay_items is the barter goods the buyer paid WITH (omitted
	// for a pure-coin pay); ledger_id joins the row back to its pay-ledger entry;
	// consume_now marks an eat-here settlement (mints no Order; its durable
	// pay_ledger row is written by the accept-time write-through instead —
	// sim.OrderlessSettlementSink, LLM-246). These are audit-only additions — the
	// narration keys (recipient/amount/for) the renderer + dream distiller read are
	// unchanged, so the bare-pay and pay-with-item rows still narrate identically.
	// ItemKindQty has no json tags, so write the goods in an explicit {item,qty} shape.
	if len(resolved.PayItems) > 0 {
		goods := make([]map[string]any, 0, len(resolved.PayItems))
		for _, pi := range resolved.PayItems {
			goods = append(goods, map[string]any{"item": string(pi.Kind), "qty": pi.Qty})
		}
		payload["pay_items"] = goods
	}
	payload["ledger_id"] = resolved.LedgerID
	payload["consume_now"] = resolved.ConsumeNow
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID:     resolved.BuyerID,
		OccurredAt:  resolved.At,
		ActionType:  sim.ActionTypePaid,
		Payload:     payload,
		SpeakerName: display,
		HuddleID:    resolved.HuddleID,
		Source:      source,
	})
}

// handleOfferedActionLog appends a row when a buyer's slow-path pay_with_item
// mints (or renews via in_response_to) a pending pay-ledger offer — the
// PayOfferReceived event (LLM-283). Buyer-side single row: ActorID is the buyer
// (the offer is theirs), the seller is the counterparty, Amount is the coin offer
// (0 for goods-only barter), Text is the wanted-item summary, and PayItems carries
// the barter give-goods (LLM-431). The buyer + seller share
// the offer's HuddleID by construction (co-presence is required to offer), so the
// same-huddle talk-panel backload reaches the seller. Fast-path / auto-match
// quote-takes never emit PayOfferReceived (they mint already-accepted), so only a
// genuinely-pending offer — the dead-air the feed was missing — logs a row.
//
// Gift offers (give_goods, IsGift) also emit PayOfferReceived but are skipped
// here: a one-way give isn't a purchase/barter haggle, so it doesn't belong in
// the offer feed line.
func handleOfferedActionLog(w *sim.World, evt sim.Event) {
	offer, ok := evt.(*sim.PayOfferReceived)
	if !ok {
		return
	}
	if e := w.PayLedger[offer.LedgerID]; e != nil && e.IsGift {
		return
	}
	// Total quantity across consumers (group orders): the event carries
	// per-consumer qty; the narrated beat states the whole bundle. Empty
	// ConsumerIDs is the buyer-only shape and counts as one consumer.
	consumerCount := len(offer.ConsumerIDs)
	if consumerCount == 0 {
		consumerCount = 1
	}
	qty := offer.QtyPerConsumer * consumerCount
	entry := sim.ActionLogEntry{
		ActorID:          offer.BuyerID,
		OccurredAt:       offer.At,
		ActionType:       sim.ActionTypeOffered,
		Text:             formatItemQty(offer.ItemKind, qty),
		HuddleID:         offer.HuddleID,
		CounterpartyName: actorDisplayNameOrEmpty(w, offer.SellerID),
		Amount:           offer.Amount,
		// Snapshot the barter give-goods (LLM-431) so the feed renderer can show
		// what the buyer offered to hand over — a goods-only offer (Amount 0)
		// otherwise narrates as "<buyer> offers <seller> for <item>", dropping the
		// give side. Own the slice rather than alias the event's (the entry outlives
		// the event; mirrors the Paid path above).
		PayItems: append([]sim.ItemKindQty(nil), offer.PayItems...),
	}
	if _, err := sim.AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("cascade/action_log: append offered (buyer %q ledger %d event %d): %v",
			offer.BuyerID, offer.LedgerID, offer.EventID(), err)
		return
	}
	// Durable mirror (LLM-283): ledger_id + terms so the whole haggle is
	// reconstructable from agent_action_log alone (the side benefit) — buyer is
	// the speaker, seller the counterparty. The row lands in agent_action_log for
	// tracing/audit, but the sim-conversation distiller DROPS these unmapped kinds
	// from dream narration (memory-api, its unmapped-kind default), so it does NOT
	// feed NPC dream memory — this is durable audit only, not a dream beat.
	display, source := actorDisplayAndSource(w, offer.BuyerID)
	payload := map[string]any{
		"ledger_id": offer.LedgerID,
		"item":      string(offer.ItemKind),
		"qty":       qty,
		"amount":    offer.Amount,
		"seller":    actorDisplayName(w, offer.SellerID),
	}
	// LLM-431: record the barter give-goods so the durable audit captures the full
	// offer terms — a goods-only barter reads amount:0, and pay_items is what the
	// buyer offered in trade. Same {item,qty} shape + non-empty gate as the Paid
	// mirror above (ItemKindQty has no json tags, so serialize explicitly).
	if len(offer.PayItems) > 0 {
		goods := make([]map[string]any, 0, len(offer.PayItems))
		for _, pi := range offer.PayItems {
			goods = append(goods, map[string]any{"item": string(pi.Kind), "qty": pi.Qty})
		}
		payload["pay_items"] = goods
	}
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID:     offer.BuyerID,
		OccurredAt:  offer.At,
		ActionType:  sim.ActionTypeOffered,
		Payload:     payload,
		SpeakerName: display,
		HuddleID:    offer.HuddleID,
		Source:      source,
	})
}

// handleDeclinedActionLog appends a row when a seller's decline_pay flips a
// pending purchase offer to the Declined terminal — a PayWithItemResolved with
// TerminalState=Declined (LLM-283). It rides the same event as
// handlePayResolvedActionLog (which owns the Accepted terminal); the two split by
// TerminalState so each keeps a single, readable body. Seller-side single row:
// ActorID is the seller (the decline is theirs), the buyer is the counterparty.
// No coins move. Other terminals (withdrawn / expired / failed_*) log nothing.
//
// Gift declines (decline_gift, IsGift) also reach this terminal but are skipped —
// a declined give isn't a purchase haggle (see handleOfferedActionLog).
func handleDeclinedActionLog(w *sim.World, evt sim.Event) {
	resolved, ok := evt.(*sim.PayWithItemResolved)
	if !ok {
		return
	}
	if resolved.TerminalState != sim.PayTerminalStateDeclined {
		return
	}
	if e := w.PayLedger[resolved.LedgerID]; e != nil && e.IsGift {
		return
	}
	consumerCount := len(resolved.ConsumerIDs)
	if consumerCount == 0 {
		consumerCount = 1
	}
	qty := resolved.QtyPerConsumer * consumerCount
	entry := sim.ActionLogEntry{
		ActorID:          resolved.SellerID,
		OccurredAt:       resolved.At,
		ActionType:       sim.ActionTypeDeclined,
		Text:             formatItemQty(resolved.ItemKind, qty),
		HuddleID:         resolved.HuddleID,
		CounterpartyName: actorDisplayNameOrEmpty(w, resolved.BuyerID),
		Amount:           resolved.Amount,
	}
	if _, err := sim.AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("cascade/action_log: append declined (seller %q ledger %d event %d): %v",
			resolved.SellerID, resolved.LedgerID, resolved.EventID(), err)
		return
	}
	display, source := actorDisplayAndSource(w, resolved.SellerID)
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID:    resolved.SellerID,
		OccurredAt: resolved.At,
		ActionType: sim.ActionTypeDeclined,
		Payload: map[string]any{
			"ledger_id": resolved.LedgerID,
			"item":      string(resolved.ItemKind),
			"qty":       qty,
			"amount":    resolved.Amount,
			"buyer":     actorDisplayName(w, resolved.BuyerID),
		},
		SpeakerName: display,
		HuddleID:    resolved.HuddleID,
		Source:      source,
	})
}

// handleCounteredActionLog appends a row when a seller's counter_pay flips a
// pending offer to the Countered terminal — the PayCountered event (LLM-283).
// Seller-side single row: ActorID is the seller (the counter is theirs), the
// buyer is the counterparty, Amount is the seller's counter price (CounterAmount;
// a non-increasing counter is coerced to an accept upstream and emits no
// PayCountered, so a countered row always carries a genuine new price). ledger_id
// is the PARENT entry — the buyer's response is a fresh chained entry with its
// own id. Gift entries aren't counterable in practice, but skip IsGift for
// symmetry with the offered / declined handlers.
func handleCounteredActionLog(w *sim.World, evt sim.Event) {
	countered, ok := evt.(*sim.PayCountered)
	if !ok {
		return
	}
	if e := w.PayLedger[countered.ParentID]; e != nil && e.IsGift {
		return
	}
	consumerCount := len(countered.ConsumerIDs)
	if consumerCount == 0 {
		consumerCount = 1
	}
	qty := countered.QtyPerConsumer * consumerCount
	entry := sim.ActionLogEntry{
		ActorID:          countered.SellerID,
		OccurredAt:       countered.At,
		ActionType:       sim.ActionTypeCountered,
		Text:             formatItemQty(countered.ItemKind, qty),
		HuddleID:         countered.HuddleID,
		CounterpartyName: actorDisplayNameOrEmpty(w, countered.BuyerID),
		Amount:           countered.CounterAmount,
	}
	if _, err := sim.AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("cascade/action_log: append countered (seller %q ledger %d event %d): %v",
			countered.SellerID, countered.ParentID, countered.EventID(), err)
		return
	}
	// Durable mirror (LLM-283): original_amount alongside the counter amount so
	// the durable trail shows the price MOVE, not just the new number.
	display, source := actorDisplayAndSource(w, countered.SellerID)
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID:    countered.SellerID,
		OccurredAt: countered.At,
		ActionType: sim.ActionTypeCountered,
		Payload: map[string]any{
			"ledger_id":       countered.ParentID,
			"item":            string(countered.ItemKind),
			"qty":             qty,
			"amount":          countered.CounterAmount,
			"original_amount": countered.OriginalAmount,
			"buyer":           actorDisplayName(w, countered.BuyerID),
		},
		SpeakerName: display,
		HuddleID:    countered.HuddleID,
		Source:      source,
	})
}

// handleConsumedActionLog appends a row when a consume tool call
// commits. Text is the item kind (with qty prefix when qty > 1) —
// matches the digest-side rendering shape ("ate 1x ale").
func handleConsumedActionLog(w *sim.World, evt sim.Event) {
	consumed, ok := evt.(*sim.ItemConsumed)
	if !ok {
		return
	}
	huddleID := sim.HuddleID("")
	if actor, ok := w.Actors[consumed.ActorID]; ok {
		huddleID = actor.CurrentHuddleID
	}
	entry := sim.ActionLogEntry{
		ActorID:    consumed.ActorID,
		OccurredAt: consumed.At,
		ActionType: sim.ActionTypeConsumed,
		Text:       formatItemQty(consumed.Kind, consumed.Qty),
		HuddleID:   huddleID,
	}
	if _, err := sim.AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("cascade/action_log: append consumed (actor %q event %d): %v",
			consumed.ActorID, consumed.EventID(), err)
		return
	}
	// Durable mirror (ZBBS-WORK-376): item + qty as structured fields.
	// "kept" (ZBBS-WORK-391) records needs-clamp surplus the actor pocketed,
	// only when non-zero — keeps the common row shape unchanged.
	payload := map[string]any{"item": string(consumed.Kind), "qty": consumed.Qty}
	if consumed.Kept > 0 {
		payload["kept"] = consumed.Kept
	}
	display, source := actorDisplayAndSource(w, consumed.ActorID)
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID:     consumed.ActorID,
		OccurredAt:  consumed.At,
		ActionType:  sim.ActionTypeConsumed,
		Payload:     payload,
		SpeakerName: display,
		HuddleID:    huddleID,
		Source:      source,
	})
}

// handleGatheredActionLog appends a row when a gather commits — an NPC via
// the `gather` tool or a PC via POST /api/village/pc/gather, both of which
// emit ItemGathered post-validation. Mirrors handleConsumedActionLog: Text is
// the harvested item kind (with qty prefix when qty > 1). Text carries the item
// alone; the source object's display name rides in CounterpartyName so the
// talk-panel line can read "…from the Well" without overloading the parseable
// Text field the digest/consolidation readers consume.
func handleGatheredActionLog(w *sim.World, evt sim.Event) {
	gathered, ok := evt.(*sim.ItemGathered)
	if !ok {
		return
	}
	huddleID := sim.HuddleID("")
	if actor, ok := w.Actors[gathered.ActorID]; ok {
		huddleID = actor.CurrentHuddleID
	}
	sourceName := objectDisplayName(w, gathered.ObjectID)
	entry := sim.ActionLogEntry{
		ActorID:          gathered.ActorID,
		OccurredAt:       gathered.At,
		ActionType:       sim.ActionTypeGathered,
		Text:             formatItemQty(gathered.Item, gathered.Qty),
		HuddleID:         huddleID,
		CounterpartyName: sourceName,
	}
	if _, err := sim.AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("cascade/action_log: append gathered (actor %q event %d): %v",
			gathered.ActorID, gathered.EventID(), err)
		return
	}
	// Durable mirror (ZBBS-WORK-376): item + qty as structured fields, plus the
	// source object's display name when it resolves — "gathered 20 water from the
	// Well" beats "gathered 20 water" in the dream distiller. `source` is omitted
	// (not blank) when the object vanished, keeping the common row shape clean.
	display, src := actorDisplayAndSource(w, gathered.ActorID)
	payload := map[string]any{"item": string(gathered.Item), "qty": gathered.Qty}
	if sourceName != "" {
		payload["source"] = sourceName
	}
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID:     gathered.ActorID,
		OccurredAt:  gathered.At,
		ActionType:  sim.ActionTypeGathered,
		Payload:     payload,
		SpeakerName: display,
		HuddleID:    huddleID,
		Source:      src,
	})
}

// objectDisplayName resolves a village object to its display name, mirroring the
// resolution gather_commands.go does at emit time and sourceActivityObjectName
// does engine-side (EffectiveDisplayName over the asset catalog name). Returns ""
// for an object that no longer resolves in the world — callers attach the name
// conditionally rather than rendering an empty clause. Shared by the gathered row
// (the harvest source) and the repairing row (the business being mended).
func objectDisplayName(w *sim.World, objID sim.VillageObjectID) string {
	obj, ok := w.VillageObjects[objID]
	if !ok || obj == nil {
		return ""
	}
	catalogName := ""
	if a := w.Assets[obj.AssetID]; a != nil {
		catalogName = a.Name
	}
	return obj.EffectiveDisplayName(catalogName)
}

// handleRepairingActionLog appends a row when a `repair` tool call opens the
// mending window (LLM-354). Filtered to SourceActivityRepair: the harvest and
// refresh kinds share SourceActivityStarted but have no observer-facing beat —
// gather logs its yield at the mint instead, and eating is the actor's own affair.
//
// Logged at the START, which is a committed act even though the window may never
// finish: StartRepair validates responsibility, co-location, wear, and nails and
// consumes the nails before emitting. A mender who walks off has still spent them
// (no refund, wear unreset), so the row is never retracted and there is no
// cancellation beat to pair with it.
func handleRepairingActionLog(w *sim.World, evt sim.Event) {
	started, ok := evt.(*sim.SourceActivityStarted)
	if !ok || started.Kind != sim.SourceActivityRepair {
		return
	}
	huddleID := sim.HuddleID("")
	if actor, ok := w.Actors[started.ActorID]; ok {
		huddleID = actor.CurrentHuddleID
	}
	businessName := objectDisplayName(w, started.ObjectID)
	entry := sim.ActionLogEntry{
		ActorID:    started.ActorID,
		OccurredAt: started.At,
		ActionType: sim.ActionTypeRepairing,
		Text:       businessName,
		HuddleID:   huddleID,
	}
	if _, err := sim.AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("cascade/action_log: append repairing (actor %q event %d): %v",
			started.ActorID, started.EventID(), err)
		return
	}
	// Durable mirror: the business name as a structured field, omitted (not blank)
	// when the object no longer resolves, keeping the common row shape clean.
	display, src := actorDisplayAndSource(w, started.ActorID)
	payload := map[string]any{}
	if businessName != "" {
		payload["business"] = businessName
	}
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID:     started.ActorID,
		OccurredAt:  started.At,
		ActionType:  sim.ActionTypeRepairing,
		Payload:     payload,
		SpeakerName: display,
		HuddleID:    huddleID,
		Source:      src,
	})
}

// handleOrderDeliveredActionLog appends a row when a deliver_order
// tool call commits. ActorID = SellerID — the deliver action is
// theirs (they invoke the tool). Text is the item summary.
func handleOrderDeliveredActionLog(w *sim.World, evt sim.Event) {
	delivered, ok := evt.(*sim.OrderDelivered)
	if !ok {
		return
	}
	huddleID := sim.HuddleID("")
	if seller, ok := w.Actors[delivered.SellerID]; ok {
		huddleID = seller.CurrentHuddleID
	}
	entry := sim.ActionLogEntry{
		ActorID:    delivered.SellerID,
		OccurredAt: delivered.At,
		ActionType: sim.ActionTypeDelivered,
		Text:       formatItemQty(delivered.Item, delivered.Qty),
		HuddleID:   huddleID,
		// ZBBS-WORK-377: recipient (buyer) for the PC talk-panel narration
		// ("X delivers <item> to Y."). Amount intentionally omitted — the buyer
		// already paid at order time, so a price on delivery reads oddly.
		CounterpartyName: actorDisplayNameOrEmpty(w, delivered.BuyerID),
	}
	if _, err := sim.AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("cascade/action_log: append delivered (seller %q event %d): %v",
			delivered.SellerID, delivered.EventID(), err)
		return
	}
	// Durable mirror (ZBBS-WORK-376). Seller side: recipient is the buyer's
	// display name; item + qty + amount carry the fulfillment detail.
	display, source := actorDisplayAndSource(w, delivered.SellerID)
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID:    delivered.SellerID,
		OccurredAt: delivered.At,
		ActionType: sim.ActionTypeDelivered,
		Payload: map[string]any{
			"recipient": actorDisplayName(w, delivered.BuyerID),
			"item":      string(delivered.Item),
			"qty":       delivered.Qty,
			"amount":    delivered.Amount,
		},
		SpeakerName: display,
		HuddleID:    huddleID,
		Source:      source,
	})
}

// handleActorArrivedActionLog appends a row when an actor completes a
// movement at a destination. Text is the DESTINATION's DisplayName — the
// place the mover walked TO (ZBBS-WORK-359): the destination structure
// (StructureEnter/StructureVisit/knock — names the shop even when the actor
// stopped at a loiter slot OUTSIDE it, so InsideStructureID is empty) or, for
// an ObjectVisit, the destination village object (well/tree/gather pile).
// Falls back to FinalStructureID for a bare Position arrival that still landed
// inside a footprint. Empty only for an outdoor Position arrival with no
// nameable place. HuddleID is empty — arrival precedes any encounter-cascade
// huddle join that may follow.
func handleActorArrivedActionLog(w *sim.World, evt sim.Event) {
	arrived, ok := evt.(*sim.ActorArrived)
	if !ok {
		return
	}
	// Destination DisplayName, resolved by the shared helper so this backload
	// entry and the live arrival narration (ZBBS-WORK-422) name the same place.
	text := sim.ArrivalDestinationName(w, arrived)
	entry := sim.ActionLogEntry{
		ActorID:    arrived.ActorID,
		OccurredAt: arrived.At,
		ActionType: sim.ActionTypeWalked,
		Text:       text,
		HuddleID:   sim.HuddleID(""),
	}
	if _, err := sim.AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("cascade/action_log: append walked (actor %q event %d): %v",
			arrived.ActorID, arrived.EventID(), err)
		return
	}
	// Durable mirror (ZBBS-WORK-376): destination name (the resolved structure /
	// object display name) as a structured field. Omitted for a bare outdoor
	// arrival with no nameable place. HuddleID empty — arrival precedes any
	// encounter-cascade huddle join.
	display, source := actorDisplayAndSource(w, arrived.ActorID)
	payload := map[string]any{}
	if text != "" {
		payload["destination"] = text
	}
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID:     arrived.ActorID,
		OccurredAt:  arrived.At,
		ActionType:  sim.ActionTypeWalked,
		Payload:     payload,
		SpeakerName: display,
		HuddleID:    sim.HuddleID(""),
		Source:      source,
	})
}

// handleActorLeftStructureActionLog appends a row when an actor walks OUT of a
// structure (sim.ActorLeftStructure, emitted by the locomotion exit seam BEFORE
// the inside-flip). Text is the LEFT structure's DisplayName — the inverse of
// handleActorArrivedActionLog's destination. The row renders as
// "<name> leaves the <place>." in the talk-panel backload + admin Village tab.
// Because the event fires pre-flip, AppendActionLogEntry's central scope stamp
// still resolves to the structure being left, so a co-present PC sees the exit.
//
// No durable mirror (unlike arrival's ZBBS-WORK-376 row): the in-memory row is
// the talk-panel + narrative-consolidation source this feature needs; the durable
// agent_action_log feeds the cross-system dream/distillation pipeline, where a
// presence-departure beat doesn't warrant a new action_type. HuddleID empty — a
// departure leaves any huddle behind.
func handleActorLeftStructureActionLog(w *sim.World, evt sim.Event) {
	left, ok := evt.(*sim.ActorLeftStructure)
	if !ok {
		return
	}
	name := ""
	if s, ok := w.Structures[left.StructureID]; ok {
		name = s.DisplayName
	}
	if name == "" {
		return // nameless / vanished structure — nothing to render
	}
	entry := sim.ActionLogEntry{
		ActorID:    left.ActorID,
		OccurredAt: left.At,
		ActionType: sim.ActionTypeDeparted,
		Text:       name,
		HuddleID:   sim.HuddleID(""),
	}
	if _, err := sim.AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("cascade/action_log: append departed (actor %q event %d): %v",
			left.ActorID, left.EventID(), err)
	}
}

// handleTookBreakActionLog appends a row when a take_break tool call
// commits (ZBBS-HOME-284 #4). ActorID is the actor that stepped away;
// Text is the model-supplied reason; HuddleID is the actor's huddle at
// append time (a break closes the post, so usually empty).
func handleTookBreakActionLog(w *sim.World, evt sim.Event) {
	broke, ok := evt.(*sim.TookBreak)
	if !ok {
		return
	}
	huddleID := sim.HuddleID("")
	if actor, ok := w.Actors[broke.ActorID]; ok {
		huddleID = actor.CurrentHuddleID
	}
	entry := sim.ActionLogEntry{
		ActorID:    broke.ActorID,
		OccurredAt: broke.At,
		ActionType: sim.ActionTypeTookBreak,
		Text:       broke.Reason,
		HuddleID:   huddleID,
	}
	if _, err := sim.AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("cascade/action_log: append took_break (actor %q event %d): %v",
			broke.ActorID, broke.EventID(), err)
		return
	}
	// Durable mirror (ZBBS-WORK-376): the model-supplied reason as a structured
	// field (omitted when empty).
	display, source := actorDisplayAndSource(w, broke.ActorID)
	payload := map[string]any{}
	if broke.Reason != "" {
		payload["reason"] = broke.Reason
	}
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID:     broke.ActorID,
		OccurredAt:  broke.At,
		ActionType:  sim.ActionTypeTookBreak,
		Payload:     payload,
		SpeakerName: display,
		HuddleID:    huddleID,
		Source:      source,
	})
}

// handleStayedOpenActionLog appends a row when a stay_open tool call commits
// (ZBBS-WORK-387). ActorID is the keeper that committed to staying open late;
// Text is the model-supplied reason; HuddleID is the keeper's huddle at append
// time (usually a customer huddle — staying open keeps the post manned).
func handleStayedOpenActionLog(w *sim.World, evt sim.Event) {
	stayed, ok := evt.(*sim.StayingOpen)
	if !ok {
		return
	}
	huddleID := sim.HuddleID("")
	if actor, ok := w.Actors[stayed.ActorID]; ok {
		huddleID = actor.CurrentHuddleID
	}
	entry := sim.ActionLogEntry{
		ActorID:    stayed.ActorID,
		OccurredAt: stayed.At,
		ActionType: sim.ActionTypeStayedOpen,
		Text:       stayed.Reason,
		HuddleID:   huddleID,
	}
	if _, err := sim.AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("cascade/action_log: append stayed_open (actor %q event %d): %v",
			stayed.ActorID, stayed.EventID(), err)
		return
	}
	// Durable mirror (ZBBS-WORK-376): the model-supplied reason as a structured
	// field (omitted when empty).
	display, source := actorDisplayAndSource(w, stayed.ActorID)
	payload := map[string]any{}
	if stayed.Reason != "" {
		payload["reason"] = stayed.Reason
	}
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID:     stayed.ActorID,
		OccurredAt:  stayed.At,
		ActionType:  sim.ActionTypeStayedOpen,
		Payload:     payload,
		SpeakerName: display,
		HuddleID:    huddleID,
		Source:      source,
	})
}

// handleSolicitedWorkActionLog appends a row when a solicit_work tool call
// mints a live pending offer (LLM-213). The event fires ONLY on SolicitWork's
// live-pending path — the LLM-193 affordability auto-decline resolves the offer
// Declined WITHOUT emitting LaborOfferReceived, so a broke-employer solicit logs
// nothing here (the deliberate non-beat). Worker-side single row: ActorID is the
// worker (the offer is theirs), the employer is the counterparty, Amount is the
// reward asked. Worker + employer share the offer's HuddleID by construction
// (co-presence is required to solicit), so the same-huddle peer filter reaches
// the employer for narrative pickup — one row suffices, the handlePaidActionLog
// posture. No coins move at solicit.
func handleSolicitedWorkActionLog(w *sim.World, evt sim.Event) {
	received, ok := evt.(*sim.LaborOfferReceived)
	if !ok {
		return
	}
	entry := sim.ActionLogEntry{
		ActorID:          received.WorkerID,
		OccurredAt:       received.At,
		ActionType:       sim.ActionTypeSolicitedWork,
		HuddleID:         received.HuddleID,
		CounterpartyName: actorDisplayNameOrEmpty(w, received.EmployerID),
		Amount:           received.Reward,
	}
	if _, err := sim.AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("cascade/action_log: append solicited_work (worker %q labor %d event %d): %v",
			received.WorkerID, received.LaborID, received.EventID(), err)
		return
	}
	// Durable mirror (ZBBS-WORK-376): the worker offered the employer a
	// DurationMin job for the reward. employer + amount + duration_min give the
	// dream distiller the full arrangement; labor_id joins the row to its offer.
	display, source := actorDisplayAndSource(w, received.WorkerID)
	payload := map[string]any{
		"employer":     actorDisplayName(w, received.EmployerID),
		"amount":       received.Reward,
		"duration_min": received.DurationMin,
		"labor_id":     uint64(received.LaborID),
	}
	// LLM-225: the in-kind reward leg, omitted for a coins-only ask so the
	// pre-existing row shape is unchanged.
	if goods := laborRewardItemsPayload(received.RewardItems); goods != nil {
		payload["reward_items"] = goods
	}
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID:     received.WorkerID,
		OccurredAt:  received.At,
		ActionType:  sim.ActionTypeSolicitedWork,
		Payload:     payload,
		SpeakerName: display,
		HuddleID:    received.HuddleID,
		Source:      source,
	})
}

// laborRewardItemsPayload renders a labor reward's in-kind leg (LLM-225) in
// the explicit {item,qty} shape the durable payloads use for goods
// (ItemKindQty has no json tags — the same convention as the paid row's
// pay_items). Returns nil for a coins-only reward so callers can attach it
// conditionally and coins-only rows keep their exact pre-LLM-225 shape.
func laborRewardItemsPayload(items []sim.ItemKindQty) []map[string]any {
	if len(items) == 0 {
		return nil
	}
	goods := make([]map[string]any, 0, len(items))
	for _, ri := range items {
		goods = append(goods, map[string]any{"item": string(ri.Kind), "qty": ri.Qty})
	}
	return goods
}

// handleHiredActionLog appends a row when an accept_work tool call flips a
// pending offer to Working (LLM-213). The event fires only when every AcceptWork
// gate passes (TTL / co-presence / worker-free / funds), so a gate-failed accept
// — which reaches a terminal via finalizeLaborTerminal and never emits — logs
// nothing. Employer-side single row: ActorID is the employer (the hire is
// theirs), the worker is the counterparty, Amount is the agreed reward. The pair
// shares the offer's HuddleID (co-presence is required to accept), so the
// same-huddle peer filter reaches the worker. No coins move at accept — the
// reward settles at completion (handleLaborResolvedActionLog).
func handleHiredActionLog(w *sim.World, evt sim.Event) {
	accepted, ok := evt.(*sim.LaborOfferAccepted)
	if !ok {
		return
	}
	entry := sim.ActionLogEntry{
		ActorID:          accepted.EmployerID,
		OccurredAt:       accepted.At,
		ActionType:       sim.ActionTypeHired,
		HuddleID:         accepted.HuddleID,
		CounterpartyName: actorDisplayNameOrEmpty(w, accepted.WorkerID),
		Amount:           accepted.Reward,
	}
	if _, err := sim.AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("cascade/action_log: append hired (employer %q labor %d event %d): %v",
			accepted.EmployerID, accepted.LaborID, accepted.EventID(), err)
		return
	}
	// Durable mirror (ZBBS-WORK-376): the employer took the worker on for a
	// DurationMin job at the reward. worker + amount + duration_min give the
	// dream distiller the full arrangement; labor_id joins the row to its offer.
	display, source := actorDisplayAndSource(w, accepted.EmployerID)
	payload := map[string]any{
		"worker":       actorDisplayName(w, accepted.WorkerID),
		"amount":       accepted.Reward,
		"duration_min": accepted.DurationMin,
		"labor_id":     uint64(accepted.LaborID),
	}
	// LLM-225: the in-kind reward leg, omitted for a coins-only hire.
	if goods := laborRewardItemsPayload(accepted.RewardItems); goods != nil {
		payload["reward_items"] = goods
	}
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID:     accepted.EmployerID,
		OccurredAt:  accepted.At,
		ActionType:  sim.ActionTypeHired,
		Payload:     payload,
		SpeakerName: display,
		HuddleID:    accepted.HuddleID,
		Source:      source,
	})
}

// handleLaborResolvedActionLog appends a row when an LLM-26 labor contract
// settles Completed — the worker's accepted solicit_work job finished and the
// reward transferred from the employer to the worker (labor_settle.go's
// settleCompletedLabor). This is the LLM-162 audit fix: before it, a completed
// contract moved coins (employer −reward / worker +reward) with no durable
// trace, the one economic event in the engine that left the activity feed and
// the dream-feeding agent_action_log blank.
//
// Worker-side single row: ActorID is the worker (the labor is theirs, and the
// broke-NPC-earning beat is the salient one — LLM-83's commerce loop), the
// employer is the counterparty, Amount is the reward. The worker + employer
// share the offer's HuddleID by construction (co-presence is required to
// solicit and to accept), so cross-actor narrative pickup reaches the employer
// via the same-huddle peer filter — one row suffices, the same posture as
// handlePaidActionLog.
//
// Non-Completed terminals (declined / expired / failed_unavailable) move no
// coins and append nothing, mirroring handlePayResolvedActionLog's
// accepted-only rule.
func handleLaborResolvedActionLog(w *sim.World, evt sim.Event) {
	resolved, ok := evt.(*sim.LaborResolved)
	if !ok {
		return
	}
	if resolved.TerminalState != sim.LaborTerminalStateCompleted {
		return
	}
	entry := sim.ActionLogEntry{
		ActorID:          resolved.WorkerID,
		OccurredAt:       resolved.At,
		ActionType:       sim.ActionTypeLabored,
		HuddleID:         resolved.HuddleID,
		CounterpartyName: actorDisplayNameOrEmpty(w, resolved.EmployerID),
		Amount:           resolved.Reward,
	}
	if _, err := sim.AppendActionLogEntry(entry).Fn(w); err != nil {
		log.Printf("cascade/action_log: append labored (worker %q labor %d event %d): %v",
			resolved.WorkerID, resolved.LaborID, resolved.EventID(), err)
		return
	}
	// Durable mirror (ZBBS-WORK-376): the worker earned the reward from the
	// employer over a DurationMin job. employer + amount + duration_min give the
	// dream distiller the full economic fact a bare coin delta lacks (the
	// LLM-162 gap); labor_id joins the row back to its in-run offer.
	display, source := actorDisplayAndSource(w, resolved.WorkerID)
	payload := map[string]any{
		"employer":     actorDisplayName(w, resolved.EmployerID),
		"amount":       resolved.Reward,
		"duration_min": resolved.DurationMin,
		"labor_id":     uint64(resolved.LaborID),
	}
	// LLM-225: the in-kind reward leg that settled alongside (or instead of)
	// the coins — the durable audit trail for "did the promised porridge
	// actually change hands". Omitted for a coins-only reward.
	if goods := laborRewardItemsPayload(resolved.RewardItems); goods != nil {
		payload["reward_items"] = goods
	}
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID:     resolved.WorkerID,
		OccurredAt:  resolved.At,
		ActionType:  sim.ActionTypeLabored,
		Payload:     payload,
		SpeakerName: display,
		HuddleID:    resolved.HuddleID,
		Source:      source,
	})
}

// formatItemQty renders an item kind for the action log's Text field.
// Plain kind name when qty == 1, "Nx kind" otherwise. Matches the
// "1x ale" / "3x bread" shape consolidation can read back without
// further parsing.
func formatItemQty(kind sim.ItemKind, qty int) string {
	if qty <= 1 {
		return string(kind)
	}
	return fmt.Sprintf("%dx %s", qty, kind)
}

// actorDisplayAndSource resolves the acting actor's display name and the
// agent_action_log `source` value for a DurableActionLogRow: "player" for a PC
// (LoginUsername set), "agent" otherwise. An unknown actor (or one with no
// display name) falls back to the actor-id string — speaker_name is NOT NULL
// and labels the distilled transcript line, so a stable id beats a blank.
func actorDisplayAndSource(w *sim.World, id sim.ActorID) (display, source string) {
	source = "agent"
	display = string(id)
	if a, ok := w.Actors[id]; ok {
		if a.DisplayName != "" {
			display = a.DisplayName
		}
		if a.LoginUsername != "" {
			source = "player"
		}
	}
	return display, source
}

// actorDisplayName resolves just an actor's display name (for a counterparty —
// a pay recipient / delivery buyer), falling back to the id string.
func actorDisplayName(w *sim.World, id sim.ActorID) string {
	if a, ok := w.Actors[id]; ok && a.DisplayName != "" {
		return a.DisplayName
	}
	return string(id)
}

// actorDisplayNameOrEmpty resolves a counterparty's display name for the lean
// ring's CounterpartyName (ZBBS-WORK-377), returning "" when there's no display
// name. Unlike actorDisplayName (durable sink, speaker_name NOT NULL → id
// fallback), the PC talk-panel renderer is human-facing: a raw actor id reads
// worse than dropping the recipient, so the empty triggers the renderer's
// counterparty-less phrasing.
func actorDisplayNameOrEmpty(w *sim.World, id sim.ActorID) string {
	if a, ok := w.Actors[id]; ok {
		return a.DisplayName
	}
	return ""
}

// runActionLogSweep is the goroutine body. Immediate first compaction
// on entry (clears stale entries inherited from any prior session
// warmup), then ticks at ActionLogSweepInterval.
func runActionLogSweep(ctx context.Context, w *sim.World) {
	interval, retention := readActionLogSettings(ctx, w)
	if ctx.Err() != nil {
		return
	}
	// Cadence contract, phase two (LLM-395) — see runIdleBackstopSweep.
	w.RegisterTicker("action_log", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	runOneActionLogSweep(ctx, w, retention)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.BeatTicker("action_log")
			runOneActionLogSweep(ctx, w, retention)
		}
	}
}

// runOneActionLogSweep executes one CompactActionLog pass on the
// world goroutine. cutoff = now - retention.
func runOneActionLogSweep(ctx context.Context, w *sim.World, retention time.Duration) {
	if ctx.Err() != nil {
		return
	}
	cutoff := time.Now().UTC().Add(-retention)
	res, err := w.SendContext(ctx, sim.CompactActionLog(cutoff))
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("cascade/action_log: compact: %v", err)
		}
		return
	}
	dropped, ok := res.(int)
	if !ok {
		log.Printf("cascade/action_log: compact returned %T, want int", res)
		return
	}
	if dropped > 0 {
		log.Printf("cascade/action_log: compaction dropped=%d cutoff=%s",
			dropped, cutoff.Format(time.RFC3339))
	}
}

// readActionLogSettings reads
// WorldSettings.ActionLogSweepInterval and ActionLogRetention via a
// context-aware Command — the settings live on world-goroutine-owned
// state. Falls back to defaults when unset or when the read fails.
// Matches the readSweepInterval pattern in cascade/idle_backstop.go.
//
// Must be SendContext, not Send: this runs before the goroutine
// reaches its ctx.Done()-aware select loop. If the world isn't
// running (registration ordering off, shutdown racing startup), a
// plain Send blocks forever and the goroutine is unkillable.
func readActionLogSettings(ctx context.Context, w *sim.World) (interval, retention time.Duration) {
	res, err := w.SendContext(ctx, sim.Command{Fn: func(world *sim.World) (any, error) {
		iv := world.Settings.ActionLogSweepInterval
		if iv <= 0 {
			iv = defaultActionLogSweepInterval
		}
		rt := world.Settings.ActionLogRetention
		if rt <= 0 {
			rt = sim.DefaultActionLogRetention
		}
		return [2]time.Duration{iv, rt}, nil
	}})
	if err != nil {
		return defaultActionLogSweepInterval, sim.DefaultActionLogRetention
	}
	out, ok := res.([2]time.Duration)
	if !ok {
		return defaultActionLogSweepInterval, sim.DefaultActionLogRetention
	}
	return out[0], out[1]
}
