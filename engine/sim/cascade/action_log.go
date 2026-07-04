package cascade

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// action_log.go — append-only in-memory action log substrate driver.
// Wires twelve event subscribers (Spoke / Paid / PayWithItemResolved /
// ItemConsumed / OrderDelivered / ActorArrived / ActorLeftStructure /
// TookBreak / StayingOpen / LaborOfferReceived / LaborOfferAccepted /
// LaborResolved) to translate engine events into sim.ActionLogEntry rows,
// and spawns a sweep goroutine that periodically compacts the log via
// sim.CompactActionLog.
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
//   ├─> w.Subscribe(handlePayResolvedActionLog)
//   ├─> w.Subscribe(handleConsumedActionLog)
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
//   - No deliberation outcomes (declined / countered pay) — those
//     handlers haven't ported yet.
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

// RegisterActionLog wires the twelve event subscribers and spawns the
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
	w.Subscribe(sim.SubscriberFunc(handlePayResolvedActionLog))
	w.Subscribe(sim.SubscriberFunc(handleConsumedActionLog))
	w.Subscribe(sim.SubscriberFunc(handleOrderDeliveredActionLog))
	w.Subscribe(sim.SubscriberFunc(handleActorArrivedActionLog))
	w.Subscribe(sim.SubscriberFunc(handleActorLeftStructureActionLog))
	w.Subscribe(sim.SubscriberFunc(handleTookBreakActionLog))
	w.Subscribe(sim.SubscriberFunc(handleStayedOpenActionLog))
	w.Subscribe(sim.SubscriberFunc(handleSolicitedWorkActionLog))
	w.Subscribe(sim.SubscriberFunc(handleHiredActionLog))
	w.Subscribe(sim.SubscriberFunc(handleLaborResolvedActionLog))
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
