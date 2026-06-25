package handlers

import (
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// scene_quote_reactor.go — SceneQuoteCreated event subscriber.
// Phase 3 PR S3.
//
// Mints one SceneQuoteTargetedWarrantReason on the TargetBuyer when:
//   - TargetBuyer is non-empty. A PUBLIC quote (TargetBuyer == "")
//     posted while the seller is in an active huddle instead fans the
//     same warrant out to the seller's NPC huddle peers (ZBBS-HOME-431,
//     see fanOutPublicQuoteWarrants) — speak parity: the client renders
//     a posted quote as the seller's speech, so the people standing in
//     the conversation must "hear" it too. Pre-431 a public quote
//     stamped nothing and an idle NPC buyer never learned of the offer
//     (the 2026-06-11 Ezekiel bread stall: quote posted seconds after
//     his tick ended, no warrant, deal frozen until an operator nudge).
//     A public quote from a seller in no active huddle still stamps
//     nothing — the pull-based perception render covers passers-by.
//   - TargetBuyer is an NPC (KindNPCStateful or KindNPCShared). PCs
//     don't reactor-tick, so a warrant on a PC would be inert —
//     PC perception of targeted quotes comes through the client's
//     Snapshot.Quotes + Snapshot.Scenes[sceneID].QuoteIDs path.
//   - TargetBuyer != SellerID (defensive; sim.SceneQuoteCreate's
//     gate-5 resolution already rejects targeting yourself).
//
// Warrant policy choices (locked at scene-quote-design § 7):
//
//   - SourceEventID = SceneQuoteCreated.EventID(). Same one-ID-flows-
//     through-everything pattern as Spoke/Paid. Pre-§8 dedup scheme —
//     ledger-substrate § 8's DedupDiscriminator interface lands at
//     PR S5 alongside pay-offer warrants. Quote warrants are
//     restart-noncritical (a missed targeted warrant just means the
//     buyer re-discovers the quote via perception on their next tick),
//     so they ride the existing (Kind, SourceEventID) scheme rather
//     than the restart-stable scheme the design ports later.
//
//   - Force: false. Matches every other v2 warrant kind except Admin.
//
//   - SceneID = quote.SceneID. Load-bearing for the perception layer's
//     scene resolution (step 1 of resolvePrimaryScene).
//
//   - HuddleID intentionally empty. Quotes are anchored to scenes,
//     not huddles; the targeted buyer may be in any of the scene's
//     huddles. The buyer's perception build can re-derive their
//     current huddle from their own CurrentHuddleID.
func handleSceneQuoteWarrants(w *sim.World, evt sim.Event) {
	created, ok := evt.(*sim.SceneQuoteCreated)
	if !ok {
		return
	}
	if created.TargetBuyer == "" {
		// Public quote: warrant the seller's huddle peers instead
		// (ZBBS-HOME-431) — they heard the offer.
		fanOutPublicQuoteWarrants(w, created)
		return
	}
	if created.TargetBuyer == created.SellerID {
		// Defensive — Command Fn gate-5 rejects self-targeting,
		// so a Created event with target==seller shouldn't reach
		// here. Skip rather than stamp on the seller.
		return
	}
	target, ok := w.Actors[created.TargetBuyer]
	if !ok {
		return
	}
	if target.Kind != sim.KindNPCStateful && target.Kind != sim.KindNPCShared {
		// PC or decorative TargetBuyer. PCs see the quote via
		// client-side perception against Snapshot.Quotes;
		// decorative actors don't react to anything.
		return
	}

	now := time.Now().UTC()
	if _, err := sim.StampWarrant(created.TargetBuyer, sceneQuoteWarrantMeta(created, false), now).Fn(w); err != nil {
		log.Printf(
			"handlers: scene-quote-reactor StampWarrant for target %q (quote %d, event %d): %v",
			created.TargetBuyer, created.QuoteID, created.EventID(), err,
		)
	}
}

// sceneQuoteWarrantMeta builds the warrant meta for a SceneQuoteCreated event
// — shared by the targeted stamp and the public huddle fan-out so both paths
// render the same quote warrant line (seller, item, amount, the quote_id
// fast-path take instruction). overheard distinguishes the fan-out path: the
// render says "offers" rather than "offers you" for a peer who merely heard a
// public quote announced to the conversation.
func sceneQuoteWarrantMeta(created *sim.SceneQuoteCreated, overheard bool) sim.WarrantMeta {
	return sim.WarrantMeta{
		TriggerActorID: created.SellerID,
		Force:          false,
		Reason: sim.SceneQuoteTargetedWarrantReason{
			QuoteID:    created.QuoteID,
			SellerID:   created.SellerID,
			Lines:      created.Lines,
			Amount:     created.Amount,
			ConsumeNow: created.ConsumeNow,
			ExpiresAt:  created.ExpiresAt,
			Overheard:  overheard,
		},
		SourceEventID: created.EventID(),
		RootEventID:   created.RootEventID(),
		SourceActorID: created.SellerID,
		SceneID:       created.SceneID,
		// HuddleID intentionally empty — see doc above.
		OccurredAt: created.At,
	}
}

// fanOutPublicQuoteWarrants stamps the quote warrant on the seller's NPC
// huddle peers when a PUBLIC quote (no TargetBuyer) is posted from inside an
// active huddle (ZBBS-HOME-431). Without it an idle NPC buyer standing in the
// conversation only learns the offer exists on its next perception tick, so
// the deal stalls until something else happens to warrant them — the stamp
// makes them react to the quote the way they react to a Spoke event.
//
// Peer gates, mirroring the speech reactor (handleSpokeWarrants):
//   - NPC kinds only (stateful / shared) — PCs see quotes client-side,
//     decoratives don't react.
//   - mid-walk peers are skipped (ZBBS-HOME-330 rationale: a walking actor's
//     tick can only command-fail; it re-discovers the quote via perception
//     when it arrives).
//   - the seller is never stamped.
//
// A seller in no huddle (or a concluded one) stamps nothing — the pull-based
// perception render still surfaces fresh scene quotes to whoever ticks.
func fanOutPublicQuoteWarrants(w *sim.World, created *sim.SceneQuoteCreated) {
	seller, ok := w.Actors[created.SellerID]
	if !ok || seller.CurrentHuddleID == "" {
		return
	}
	huddle, ok := w.Huddles[seller.CurrentHuddleID]
	if !ok || huddle.ConcludedAt != nil {
		return
	}
	now := time.Now().UTC()
	for peerID := range huddle.Members {
		if peerID == created.SellerID {
			continue
		}
		peer, ok := w.Actors[peerID]
		if !ok {
			continue
		}
		if peer.Kind != sim.KindNPCStateful && peer.Kind != sim.KindNPCShared {
			continue
		}
		if peer.MoveIntent != nil {
			continue
		}
		if _, err := sim.StampWarrant(peerID, sceneQuoteWarrantMeta(created, true), now).Fn(w); err != nil {
			log.Printf(
				"handlers: scene-quote-reactor StampWarrant for huddle peer %q (public quote %d, event %d): %v",
				peerID, created.QuoteID, created.EventID(), err,
			)
		}
	}
}

// RegisterSceneQuoteHandlers wires the SceneQuoteCreated subscriber
// into the world. Separate from RegisterSpeechHandlers /
// RegisterPayHandlers / cascade.RegisterEncounter for the same
// opt-in-piecewise reason: a build that wants speech but not the
// quote reactor (or vice versa) can compose. Must run on the world
// goroutine — call before World.Run or from inside a Command.Fn.
//
// Idempotency: registering twice would invoke the subscriber twice
// per SceneQuoteCreated event, but tryStampWarrant's source-aware
// dedup catches the duplicate ((WarrantKindSceneQuoteTargeted,
// SceneQuoteCreated.EventID) collides with itself) and drops the
// second stamp. Same guarantee speech/pay reactors inherit.
func RegisterSceneQuoteHandlers(w *sim.World) {
	if w == nil {
		panic("handlers: RegisterSceneQuoteHandlers requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handleSceneQuoteWarrants))
}
