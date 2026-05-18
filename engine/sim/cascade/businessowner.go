package cascade

import (
	"context"
	"log"
	"math/rand"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// businessowner.go — Phase 3 Group A businessowner cascade driver. Wires
// three event subscribers that translate engine huddle / order events
// into engine-authored hospitality speech:
//
//   - HuddleJoined → greet
//   - OrderDelivered → handover
//   - HuddleLeft → farewell
//
// Each subscriber gates the fire (joiner/leaver not a keeper themselves,
// at least one keeper present in scope, at-post check, cooldown clear)
// then calls sim.EmitBusinessownerSpeech for each eligible keeper. The
// substrate Command does the atomic cooldown-check + render + Spoke-emit
// + cooldown-stamp + suppression-stamp.
//
// The Spoke event we emit reuses the standard speech subscribers:
//   - cascade/action_log writes an ActionTypeSpoke entry.
//   - handlers/speech_reactor stamps NPCSpeechWarrantReason on each
//     recipient so co-present NPCs react to the keeper's hello.
//
// Subscribers run inline on the world goroutine via emit dispatch — no
// SendContext round-trips. Cascade origin is the inbound event (the
// keeper's Spoke we emit inherits the same RootEventID).
//
// Lifecycle:
//
//   RegisterBusinessowner(ctx, w)
//   ├─> w.Subscribe(handleHuddleJoinedBusinessowner)
//   ├─> w.Subscribe(handleOrderDeliveredBusinessowner)
//   └─> w.Subscribe(handleHuddleLeftBusinessowner)
//
// No goroutine, no ticker — this slice is purely event-driven. The ctx
// parameter is kept for signature symmetry with other cascade Register*
// helpers; today it's unused. If a future enhancement adds a sweep
// (e.g. cooldown-map GC) the goroutine wires onto ctx.

// RegisterBusinessowner wires the three event subscribers. Must run on
// the world goroutine — call before World.Run, or from inside a
// Command.Fn.
//
// Idempotency: registering twice would fire the engine line twice per
// triggering event. EmitBusinessownerSpeech's cooldown gate would catch
// the duplicate greet/farewell (the first call stamps; the second
// observes the stamp), but handover has no cooldown by design so
// double-emit would land. Wiring guards live at the registration site —
// don't register twice.
//
// Panics on nil w (wiring guard, mirrors RegisterVisitor /
// RegisterAtmosphere / etc.).
//
// A per-driver *rand.Rand is seeded once with time.Now().UnixNano() and
// shared across all three handlers via closure. Subscriber dispatch is
// serial on the world goroutine, so the shared rand has no concurrency
// concern.
func RegisterBusinessowner(_ context.Context, w *sim.World) {
	if w == nil {
		panic("cascade: RegisterBusinessowner requires a non-nil world")
	}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	w.Subscribe(sim.SubscriberFunc(func(world *sim.World, evt sim.Event) {
		handleHuddleJoinedBusinessowner(world, evt, r)
	}))
	w.Subscribe(sim.SubscriberFunc(func(world *sim.World, evt sim.Event) {
		handleOrderDeliveredBusinessowner(world, evt, r)
	}))
	w.Subscribe(sim.SubscriberFunc(func(world *sim.World, evt sim.Event) {
		handleHuddleLeftBusinessowner(world, evt, r)
	}))
}

// handleHuddleJoinedBusinessowner fires greet lines when a non-keeper
// joins a huddle a keeper is in.
//
// Gates (any failure → skip the whole event):
//
//  1. The joining actor is NOT themselves a businessowner. Two keepers
//     trading "welcome friend!" is silly; the gate avoids it.
//  2. At least one OtherMember of the new huddle has BusinessownerState
//     != nil AND WorkStructureID == event.StructureID (at-post check).
//  3. The keeper is not sleeping or resting.
//
// Cooldown check + emit happen inside EmitBusinessownerSpeech atomically.
func handleHuddleJoinedBusinessowner(w *sim.World, evt sim.Event, r *rand.Rand) {
	joined, ok := evt.(*sim.HuddleJoined)
	if !ok {
		return
	}
	joiner, ok := w.Actors[joined.ActorID]
	if !ok {
		return
	}
	// Gate 1: joiner is not a keeper themselves.
	if joiner.BusinessownerState != nil {
		return
	}
	listenerName := joiner.DisplayName

	cooldownMin := w.Settings.BusinessownerGreetCooldownMinutes
	if cooldownMin <= 0 {
		cooldownMin = sim.DefaultBusinessownerGreetCooldownMinutes
	}

	// Find eligible keepers among the OtherMembers (prior huddle members).
	// The keeper must be in this same huddle (so they share the
	// conversational moment) AND at their own work structure.
	for _, peerID := range joined.OtherMembers {
		if peerID == joined.ActorID {
			continue
		}
		keeper, ok := w.Actors[peerID]
		if !ok {
			continue
		}
		if keeper.BusinessownerState == nil {
			continue
		}
		// At-post check: keeper must be at their own work structure. The
		// event's StructureID is the huddle's structure; the keeper's
		// WorkStructureID must match. A roving keeper outside their post
		// doesn't trigger the hospitality voice.
		if keeper.WorkStructureID == "" || keeper.WorkStructureID != joined.StructureID {
			continue
		}
		if keeper.State == sim.StateSleeping || keeper.State == sim.StateResting {
			continue
		}
		// Build the Spoke recipient set: every OtherMember EXCEPT this
		// keeper, plus the joining actor. The joining actor is now in
		// the huddle (HuddleJoined fires after membership update); the
		// recipient set carries the speech's listener-side warrants.
		recipients := buildBusinessownerRecipients(joined.OtherMembers, joined.ActorID, peerID)

		args := sim.BusinessownerSpeechArgs{
			SpeakerID:       peerID,
			SpeakerName:     keeper.DisplayName,
			ListenerID:      joined.ActorID,
			ListenerName:    listenerName,
			Trigger:         sim.BusinessownerTriggerGreet,
			HuddleID:        joined.HuddleID,
			RecipientIDs:    recipients,
			CooldownMinutes: cooldownMin,
			Rand:            r,
			Now:             joined.At,
		}
		if _, err := sim.EmitBusinessownerSpeech(args).Fn(w); err != nil {
			log.Printf("cascade/businessowner: greet (keeper %q customer %q event %d): %v",
				peerID, joined.ActorID, joined.EventID(), err)
		}
	}
}

// handleOrderDeliveredBusinessowner fires handover lines when the seller
// of a delivered order has BusinessownerState != nil. No cooldown by
// design — every transaction deserves a verbal handover. The seller's
// CurrentHuddleID at delivery time scopes the Spoke event.
//
// Note: handover doesn't gate sleeping/resting (an order delivered by a
// keeper who somehow transitioned to sleeping post-acceptance is a
// degenerate state — the deliver_order tool call already required them
// active). If the keeper is sleeping at delivery time, the Spoke emit
// still works; the keeper's own reactor wouldn't tick anyway thanks to
// actorCanReactNow's StateSleeping gate.
func handleOrderDeliveredBusinessowner(w *sim.World, evt sim.Event, r *rand.Rand) {
	delivered, ok := evt.(*sim.OrderDelivered)
	if !ok {
		return
	}
	seller, ok := w.Actors[delivered.SellerID]
	if !ok {
		return
	}
	if seller.BusinessownerState == nil {
		return
	}
	buyer, ok := w.Actors[delivered.BuyerID]
	if !ok {
		// Buyer disappeared between accept and deliver. Skip the verbal
		// handover (the goods transfer already landed via OrderDelivered's
		// emitter); no listener to address.
		return
	}
	// Build recipient set from the seller's current huddle peers
	// (excluding the seller themselves). The buyer should be in the
	// seller's huddle by the deliver_order same-huddle gate, but we
	// defensively include them by ID rather than relying on huddle
	// membership. The "" gate matches buildBusinessownerRecipients —
	// RecipientIDs should not carry "" under any circumstances even
	// when an upstream invariant is malformed.
	recipients := huddlePeers(w, seller.CurrentHuddleID, delivered.SellerID)
	if delivered.BuyerID != "" && !containsActorID(recipients, delivered.BuyerID) {
		recipients = append(recipients, delivered.BuyerID)
	}

	args := sim.BusinessownerSpeechArgs{
		SpeakerID:       delivered.SellerID,
		SpeakerName:     seller.DisplayName,
		ListenerID:      delivered.BuyerID,
		ListenerName:    buyer.DisplayName,
		Trigger:         sim.BusinessownerTriggerHandover,
		HuddleID:        seller.CurrentHuddleID,
		RecipientIDs:    recipients,
		CooldownMinutes: 0, // no cooldown on handover
		Rand:            r,
		Now:             delivered.At,
	}
	if _, err := sim.EmitBusinessownerSpeech(args).Fn(w); err != nil {
		log.Printf("cascade/businessowner: handover (seller %q buyer %q event %d): %v",
			delivered.SellerID, delivered.BuyerID, delivered.EventID(), err)
	}
}

// handleHuddleLeftBusinessowner fires farewell lines when a non-keeper
// leaves a huddle a keeper remains in. Symmetric to greet — same gate
// stack against the RemainingMembers slice.
//
// Late-attribution note: by the time HuddleLeft fires, the leaver has
// been removed from the huddle's member set. The Spoke recipient set
// excludes them (no listener warrant on the leaver's reactor) — the
// leaver may or may not see the farewell client-side, but the line
// lands in ActionLog either way and the keeper "said goodbye" is the
// load-bearing semantic.
func handleHuddleLeftBusinessowner(w *sim.World, evt sim.Event, r *rand.Rand) {
	left, ok := evt.(*sim.HuddleLeft)
	if !ok {
		return
	}
	leaver, ok := w.Actors[left.ActorID]
	if !ok {
		return
	}
	// Gate 1: leaver is not a keeper themselves.
	if leaver.BusinessownerState != nil {
		return
	}
	listenerName := leaver.DisplayName

	cooldownMin := w.Settings.BusinessownerFarewellCooldownMinutes
	if cooldownMin <= 0 {
		cooldownMin = sim.DefaultBusinessownerFarewellCooldownMinutes
	}

	for _, peerID := range left.RemainingMembers {
		if peerID == left.ActorID {
			continue
		}
		keeper, ok := w.Actors[peerID]
		if !ok {
			continue
		}
		if keeper.BusinessownerState == nil {
			continue
		}
		if keeper.WorkStructureID == "" || keeper.WorkStructureID != left.StructureID {
			continue
		}
		if keeper.State == sim.StateSleeping || keeper.State == sim.StateResting {
			continue
		}
		// Recipient set: RemainingMembers minus this keeper. Leaver is
		// NOT included — they've already left the huddle.
		recipients := buildBusinessownerRecipients(left.RemainingMembers, "", peerID)

		args := sim.BusinessownerSpeechArgs{
			SpeakerID:       peerID,
			SpeakerName:     keeper.DisplayName,
			ListenerID:      left.ActorID,
			ListenerName:    listenerName,
			Trigger:         sim.BusinessownerTriggerFarewell,
			HuddleID:        left.HuddleID,
			RecipientIDs:    recipients,
			CooldownMinutes: cooldownMin,
			Rand:            r,
			Now:             left.At,
		}
		if _, err := sim.EmitBusinessownerSpeech(args).Fn(w); err != nil {
			log.Printf("cascade/businessowner: farewell (keeper %q leaver %q event %d): %v",
				peerID, left.ActorID, left.EventID(), err)
		}
	}
}

// buildBusinessownerRecipients returns the Spoke recipient set for an
// engine hospitality line. members is the candidate pool (HuddleJoined.
// OtherMembers or HuddleLeft.RemainingMembers); extra is an actor ID to
// add when it's not already in members ("" to skip — used by farewell
// where the leaver is intentionally excluded). exclude is the keeper
// themselves (the speaker).
//
// Returned slice contains no duplicates and excludes both the speaker
// and any zero-valued ActorID. Order matches input order (callers don't
// rely on sort).
func buildBusinessownerRecipients(members []sim.ActorID, extra, exclude sim.ActorID) []sim.ActorID {
	out := make([]sim.ActorID, 0, len(members)+1)
	seen := make(map[sim.ActorID]struct{}, len(members)+1)
	for _, id := range members {
		if id == "" || id == exclude {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if extra != "" && extra != exclude {
		if _, dup := seen[extra]; !dup {
			out = append(out, extra)
		}
	}
	return out
}

// huddlePeers returns the actor IDs in huddleID excluding speakerID. Used
// by the handover path to scope the Spoke recipient set without an
// OtherMembers slice on the OrderDelivered event. Returns nil for empty
// huddleID — the handover Spoke still emits with the buyer added later.
//
// MUST be called from inside a Command.Fn or subscriber dispatch (reads
// w.Actors / huddle membership indirectly).
func huddlePeers(w *sim.World, huddleID sim.HuddleID, speakerID sim.ActorID) []sim.ActorID {
	if huddleID == "" {
		return nil
	}
	// Walk actors looking for huddle members. v2's actorsByHuddle index
	// is unexported in the sim package; an exported accessor isn't wired
	// today. For the handover path's scale (one huddle's members) the
	// linear scan is microseconds at village scale.
	out := make([]sim.ActorID, 0, 4)
	for id, a := range w.Actors {
		if a == nil {
			continue
		}
		if id == "" || id == speakerID {
			continue
		}
		if a.CurrentHuddleID == huddleID {
			out = append(out, id)
		}
	}
	return out
}

// containsActorID is a small helper for slice membership. Linear scan;
// the slices we check are huddle-sized (single digits).
func containsActorID(ids []sim.ActorID, want sim.ActorID) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
