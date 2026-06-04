package cascade

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// action_log.go — append-only in-memory action log substrate driver.
// Wires six event subscribers (Spoke / Paid / ItemConsumed /
// OrderDelivered / ActorArrived / TookBreak) to translate engine events
// into sim.ActionLogEntry rows, and spawns a sweep goroutine that
// periodically compacts the log via sim.CompactActionLog.
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
//   ├─> w.Subscribe(handleConsumedActionLog)
//   ├─> w.Subscribe(handleOrderDeliveredActionLog)
//   ├─> w.Subscribe(handleActorArrivedActionLog)
//   ├─> w.Subscribe(handleTookBreakActionLog)
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

// RegisterActionLog wires the five event subscribers and spawns the
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
	w.Subscribe(sim.SubscriberFunc(handleConsumedActionLog))
	w.Subscribe(sim.SubscriberFunc(handleOrderDeliveredActionLog))
	w.Subscribe(sim.SubscriberFunc(handleActorArrivedActionLog))
	w.Subscribe(sim.SubscriberFunc(handleTookBreakActionLog))
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
	display, source := actorDisplayAndSource(w, consumed.ActorID)
	w.AppendActionLogDurable(sim.DurableActionLogRow{
		ActorID:     consumed.ActorID,
		OccurredAt:  consumed.At,
		ActionType:  sim.ActionTypeConsumed,
		Payload:     map[string]any{"item": string(consumed.Kind), "qty": consumed.Qty},
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
	text := ""
	switch {
	case arrived.DestStructureID != "":
		if s, ok := w.Structures[arrived.DestStructureID]; ok {
			text = s.DisplayName
		}
	case arrived.DestObjectID != "":
		if o, ok := w.VillageObjects[arrived.DestObjectID]; ok {
			text = o.DisplayName
		}
	}
	// Bare Position arrival that nonetheless ended inside a structure footprint:
	// name that structure (the destination union named no place).
	if text == "" && arrived.FinalStructureID != "" {
		if s, ok := w.Structures[arrived.FinalStructureID]; ok {
			text = s.DisplayName
		}
	}
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
