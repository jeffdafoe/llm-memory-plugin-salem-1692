package handlers

import (
	"log"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// consume_reactor.go — ZBBS-HOME-302 §A. Subscriber for ItemConsumed that
// stamps the immediate consume self-narration warrant on the consumer, so its
// next reactor tick perceives the felt beat ("you eat the bread, the gnawing
// ebbs"). Sibling to dwell_reactor.go (which carries the sustained per-tick /
// terminal dwell beats); this is the one-shot beat at the moment of consuming.
//
// Gated on a need actually moving (len(Applied) > 0): a no-op consume by an
// already-sated actor still emits ItemConsumed for the audit log but produces
// NO perception beat — matching the ItemConsumed event-doc contract and v1's
// narrateConsumeSelf, which only spoke when a need dropped.
//
// Self only (the consumer), Force:false (atmosphere, not an emergency), dedup
// bypassed — same posture as the dwell reactor.

// handleConsumedNarrationWarrants is the ItemConsumed subscriber. Renders the
// felt line via sim.ConsumeNarration and stamps ConsumedWarrantReason on the
// consumer. Skips when no need moved, when only unhandled needs moved (no felt
// fragment → empty narration), or when the consumer has vanished.
func handleConsumedNarrationWarrants(w *sim.World, evt sim.Event) {
	consumed, ok := evt.(*sim.ItemConsumed)
	if !ok {
		return
	}
	if consumed.ActorID == "" || len(consumed.Applied) == 0 {
		return
	}
	narration := sim.ConsumeNarration(consumed.Kind, consumed.Applied)
	if narration == "" {
		return
	}
	// ZBBS-WORK-391: when the needs-clamp held units back, say so in the
	// same felt beat — the actor should know the surplus is in their pack,
	// not gone.
	if consumed.Kept > 0 {
		narration += " The rest you tuck away for later."
	}
	actor, ok := w.Actors[consumed.ActorID]
	if !ok || actor == nil {
		return
	}
	now := time.Now().UTC()
	meta := sim.WarrantMeta{
		TriggerActorID: consumed.ActorID,
		Force:          false,
		Reason: sim.ConsumedWarrantReason{
			ItemKind:      consumed.Kind,
			NarrationText: narration,
		},
		SourceEventID: consumed.EventID(),
		RootEventID:   consumed.RootEventID(),
		SourceActorID: consumed.ActorID,
		OccurredAt:    consumed.At,
	}
	if _, err := sim.StampWarrant(consumed.ActorID, meta, now).Fn(w); err != nil {
		log.Printf(
			"handlers: consume-reactor StampWarrant for consumer %q on ItemConsumed (event %d): %v",
			consumed.ActorID, consumed.EventID(), err,
		)
	}
}

// RegisterConsumeHandlers wires the ItemConsumed self-narration subscriber into
// the world. Separate per-subsystem register (mirrors RegisterDwellHandlers /
// RegisterPayHandlers) so a build can compose piecewise. Must run on the world
// goroutine — before World.Run or from inside a Command.Fn.
//
// Idempotency: registering twice double-stamps per consume (the reason bypasses
// dedup, DedupDiscriminator=0), leaving two copies on the open-cycle warrant
// list until the next tick — same wedge RegisterDwellHandlers documents.
// Production wires it once at world build.
func RegisterConsumeHandlers(w *sim.World) {
	if w == nil {
		panic("handlers: RegisterConsumeHandlers requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handleConsumedNarrationWarrants))
}
