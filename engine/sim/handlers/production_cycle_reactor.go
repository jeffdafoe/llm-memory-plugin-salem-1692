package handlers

import (
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// production_cycle_reactor.go — LLM-319. The NPC completion-perception seam for
// a landed production cycle: the batch minted into the actor's stores with
// nothing telling it, so this subscriber stamps a self-perception warrant —
// "You finish the batch — 10 porridge ready in your stores." — exactly
// mirroring handleSourceActivityCompletedWarrants (LLM-69). The wake matters
// doubly under one-shot production: the same tick that narrates the landing
// shows the now-idle trade cue, which is where the actor decides whether to
// start another batch.
//
// Targeting policy, dedup, and force posture all match the source-activity
// reactor: self only, DedupDiscriminator=0 (each completion is 1:1 with its
// landing sweep), Force=false (a finished batch is business, not an emergency).

// handleProductionCycleCompletedWarrants is the ProductionCycleCompleted
// subscriber. Stamps ProductionDoneWarrantReason on the actor with the
// pre-rendered completion narration.
func handleProductionCycleCompletedWarrants(w *sim.World, evt sim.Event) {
	done, ok := evt.(*sim.ProductionCycleCompleted)
	if !ok {
		return
	}
	if done.ActorID == "" {
		return
	}
	actor, ok := w.Actors[done.ActorID]
	if !ok || actor == nil {
		return
	}
	narration := sim.ProductionCompletionNarration(sim.ProducePluralNoun(w, done.Item), done.Qty)
	if narration == "" {
		// A zero-yield or unnamed completion — keep the event for audit but
		// don't mint a vague-fallback warrant (the dwell reactor's posture).
		log.Printf(
			"handlers: production-cycle-reactor skipping empty-narration ProductionCycleCompleted for actor %q (event %d, item %q, qty %d)",
			done.ActorID, done.EventID(), done.Item, done.Qty,
		)
		return
	}
	now := time.Now().UTC()
	meta := sim.WarrantMeta{
		TriggerActorID: done.ActorID,
		Force:          false,
		Reason: sim.ProductionDoneWarrantReason{
			Item:          done.Item,
			Qty:           done.Qty,
			NarrationText: narration,
		},
		SourceEventID: done.EventID(),
		RootEventID:   done.RootEventID(),
		SourceActorID: done.ActorID,
		OccurredAt:    done.At,
	}
	if _, err := sim.StampWarrant(done.ActorID, meta, now).Fn(w); err != nil {
		log.Printf(
			"handlers: production-cycle-reactor StampWarrant for actor %q on ProductionCycleCompleted (event %d): %v",
			done.ActorID, done.EventID(), err,
		)
	}
}

// RegisterProductionCycleHandlers wires the ProductionCycleCompleted
// subscriber into the world. Separate per-subsystem register (mirrors
// RegisterSourceActivityHandlers) so a build can compose piecewise. Must run on
// the world goroutine — call before World.Run or from inside a Command.Fn.
func RegisterProductionCycleHandlers(w *sim.World) {
	if w == nil {
		panic("handlers: RegisterProductionCycleHandlers requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handleProductionCycleCompletedWarrants))
}
