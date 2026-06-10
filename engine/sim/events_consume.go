package sim

import "time"

// ItemConsumed fires when an actor commits a consume tool call against a
// world that has resolved their inventory and applied the immediate
// satisfaction. Phase 3 PR S2 scope — self-consume only; group-feed
// (consume.consumers in v1) lands in a later PR alongside the buy/serve
// verbs.
//
// Qty is in whole units, post-validation: >= 1, <= MaxConsumeQty
// (math.MaxInt32). Handler-side decode rejects zero/negative/oversized;
// Command Fn re-validates before this event emits.
//
// Applied carries the actual per-need decrement post-clamp. For each
// satisfaction entry the Command computed `ClampNeed(pre - Immediate*Qty)`;
// Applied[attr] is `pre - post` for needs whose post-clamp value actually
// moved. A consume on a not-hungry actor leaves Applied[hunger] absent
// (the entry only appears when the need actually dropped). Subscribers
// rendering "the gnawing ebbs" should fire only when Applied[hunger] > 0
// — that way a no-op consume produces an event for audit/replay but no
// narrative beat.
//
// Subscribers that need the raw catalog effect (without the clamp) can
// read w.ItemKinds[Kind].Satisfies directly; that's the per-unit shape
// before the qty multiplier and the clamp.
//
// No DwellStarted-equivalent on this event. Dwell credits are stamped to
// actor.DwellCredits directly inside Consume when Satisfies entries have
// HasDwell() and the actor is within tolerance of a VillageObject (the
// dwell pin); the per-minute ApplyDwellTick reads those credits, applies
// the dwell delta, and stamps its own per-tick events/completions when
// the dwell narration subsystem lands.
//
// At is wall-clock; matches the LastCreditedAt anchor stamped on any dwell
// credits this consume creates so the engine log and the dwell timer
// align exactly.
type ItemConsumed struct {
	EventBase
	ActorID ActorID
	Kind    ItemKind
	Qty     int
	// Kept counts units the ZBBS-WORK-391 needs-clamp held back from an
	// over-sized consume, and is only ever stamped when those units landed
	// with THIS event's actor: for the consume tool they stay in the actor's
	// inventory; for a consume_now accept the surplus is pocketed into the
	// BUYER's inventory, so Kept is stamped only on the buyer's own consume
	// event and is zero on a non-buyer consumer's (their "you keep the rest"
	// beat would otherwise address the wrong actor). Zero when the request
	// fit the need. Subscribers rendering the consume beat append the
	// "you keep the rest" clause off this field.
	Kept    int
	Applied map[NeedKey]int
	At      time.Time
}

func (ItemConsumed) isSimEvent() {}
