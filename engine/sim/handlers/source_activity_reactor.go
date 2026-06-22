package handlers

import (
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// source_activity_reactor.go — LLM-69. The NPC completion-perception seam the
// consume-at-source feature (LLM-54..57) left open: SourceActivityCompleted was
// emitted with no subscriber (a PC-HUD surfacing seam only), so a finished timed
// eat/drink/harvest landed its effect — the need drop / item mint — with nothing
// telling the NPC. The yield appeared silently in inventory and the forage-to-
// sell loop (LLM-59) had no closing signal. This subscriber mints a self-
// perception warrant on completion — "you finish gathering; you now have 3
// blueberries in your pack" — exactly mirroring handleDwellEndedWarrants.
//
// Targeting policy, dedup, and force posture all match the dwell reactor: self
// only, DedupDiscriminator=0 (each completion is 1:1 with its sweep, so there's
// nothing to dedup and the (Kind, 0) bypass keeps unrelated completions from
// collapsing), Force=false (a finished pick is atmosphere, not an emergency).

// handleSourceActivityCompletedWarrants is the SourceActivityCompleted
// subscriber. Stamps SourceActivityCompletedWarrantReason on the actor with the
// pre-rendered completion narration. Skips a non-terminal auto-repeat bite
// (Continues — a finite refresh re-arms a fresh window after the emit, so only
// the terminal completion carries the beat; LLM-55) and an empty narration
// (defensive, mirroring the dwell empty-narration skip — an empty-text warrant
// would render the vague "Something happened nearby" fallback).
func handleSourceActivityCompletedWarrants(w *sim.World, evt sim.Event) {
	done, ok := evt.(*sim.SourceActivityCompleted)
	if !ok {
		return
	}
	if done.ActorID == "" || done.Continues {
		return
	}
	actor, ok := w.Actors[done.ActorID]
	if !ok || actor == nil {
		return
	}
	narration := sim.SourceActivityCompletionNarration(done.Kind, done.Item, done.Qty, done.Attribute, done.SourceName)
	if narration == "" {
		// An unhandled kind/attribute combination — keep the event for audit/
		// replay but don't mint a vague-fallback warrant. Breadcrumb so a real
		// narration-coverage gap stays observable (the dwell reactor's posture).
		log.Printf(
			"handlers: source-activity-reactor skipping empty-narration SourceActivityCompleted for actor %q (event %d, kind %q, attr %q) — no narration coverage",
			done.ActorID, done.EventID(), done.Kind, done.Attribute,
		)
		return
	}
	now := time.Now().UTC()
	meta := sim.WarrantMeta{
		TriggerActorID: done.ActorID,
		Force:          false,
		Reason: sim.SourceActivityCompletedWarrantReason{
			ActivityKind:  done.Kind,
			Item:          done.Item,
			Qty:           done.Qty,
			Attribute:     done.Attribute,
			SourceName:    done.SourceName,
			NarrationText: narration,
		},
		SourceEventID: done.EventID(),
		RootEventID:   done.RootEventID(),
		SourceActorID: done.ActorID,
		OccurredAt:    done.At,
	}
	if _, err := sim.StampWarrant(done.ActorID, meta, now).Fn(w); err != nil {
		log.Printf(
			"handlers: source-activity-reactor StampWarrant for actor %q on SourceActivityCompleted (event %d): %v",
			done.ActorID, done.EventID(), err,
		)
	}
}

// RegisterSourceActivityHandlers wires the SourceActivityCompleted subscriber
// into the world. Separate per-subsystem register (mirrors RegisterDwellHandlers
// / RegisterPayHandlers) so a build can compose piecewise. Must run on the world
// goroutine — call before World.Run or from inside a Command.Fn.
//
// Idempotency: registering twice invokes the subscriber twice per event; since
// the warrant bypasses dedup (DedupDiscriminator=0), the second stamp would land
// — same re-registration wedge RegisterDwellHandlers documents. Production wiring
// registers once at world build.
func RegisterSourceActivityHandlers(w *sim.World) {
	if w == nil {
		panic("handlers: RegisterSourceActivityHandlers requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handleSourceActivityCompletedWarrants))
}
