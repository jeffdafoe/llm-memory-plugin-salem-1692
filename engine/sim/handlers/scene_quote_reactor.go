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
//   - TargetBuyer is non-empty (public quotes do NOT stamp warrants;
//     they surface via the pull-based perception render path).
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
		return // public quote — no warrant stamp; perception render handles it
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
	meta := sim.WarrantMeta{
		TriggerActorID: created.SellerID,
		Force:          false,
		Reason: sim.SceneQuoteTargetedWarrantReason{
			QuoteID:    created.QuoteID,
			SellerID:   created.SellerID,
			ItemKind:   created.ItemKind,
			Qty:        created.Qty,
			Amount:     created.Amount,
			ConsumeNow: created.ConsumeNow,
			ExpiresAt:  created.ExpiresAt,
		},
		SourceEventID: created.EventID(),
		RootEventID:   created.RootEventID(),
		SourceActorID: created.SellerID,
		SceneID:       created.SceneID,
		// HuddleID intentionally empty — see doc above.
		OccurredAt: created.At,
	}
	if _, err := sim.StampWarrant(created.TargetBuyer, meta, now).Fn(w); err != nil {
		log.Printf(
			"handlers: scene-quote-reactor StampWarrant for target %q (quote %d, event %d): %v",
			created.TargetBuyer, created.QuoteID, created.EventID(), err,
		)
	}
}

// RegisterSceneQuoteHandlers wires the SceneQuoteCreated subscriber
// into the world. Separate from RegisterSpeechHandlers /
// RegisterPayHandlers / RegisterEncounterHandlers for the same
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
