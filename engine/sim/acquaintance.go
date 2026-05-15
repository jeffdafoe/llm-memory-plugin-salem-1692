package sim

import "time"

// Acquaintance subscriber — consumes ActorMet events emitted from
// JoinHuddle / StartOutdoorHuddle (one event per (joining, prior-member)
// pair) and updates both actors' Acquaintances maps with first-met
// semantics (subsequent encounters do NOT bump FirstInteractedAt).
//
// Applies to all NPC populations (KindNPCStateful + KindNPCShared) —
// even stateful NPCs need acquaintance gating so perception swaps in a
// descriptor ("the blacksmith") rather than greeting unknowns by name.
// PCs (KindPC) don't track their own acquaintances per v1's design:
// only NPCs have npc_acquaintance rows; a PC's view of "do I know this
// person" is UI-side, not engine-side.
//
// Symmetric: both A.Acquaintances[B.DisplayName] and
// B.Acquaintances[A.DisplayName] are written (filtered by the
// non-PC-side check). One ActorMet event per pair, so a 3-actor huddle
// join produces 2 events and 4 acquaintance writes (each existing pair
// covered both ways).
//
// First-met semantics: the map insert is gated on `_, already :=
// actor.Acquaintances[name]; !already` — repeated huddle joins between
// the same pair (re-enter after leave, second huddle in the same scene)
// preserve the original FirstInteractedAt. Mirrors v1's
// `INSERT ... ON CONFLICT DO NOTHING`.

// handleAcquaintance is the inline subscriber. Runs on the world
// goroutine during emit, has direct *World access, mutates Actor state
// synchronously. Non-ActorMet events fall through (subscriber is fanned
// out to every event by Subscribe; type-assert is the filter).
func handleAcquaintance(w *World, evt Event) {
	met, ok := evt.(*ActorMet)
	if !ok {
		return
	}
	a, aok := w.Actors[met.A]
	b, bok := w.Actors[met.B]
	if !aok || !bok {
		return
	}
	if a.Kind != KindPC && b.DisplayName != "" {
		recordAcquaintance(a, b.DisplayName, met.At)
	}
	if b.Kind != KindPC && a.DisplayName != "" {
		recordAcquaintance(b, a.DisplayName, met.At)
	}
}

// recordAcquaintance inserts otherName into actor.Acquaintances with
// first-met semantics. Lazily allocates the map.
func recordAcquaintance(actor *Actor, otherName string, at time.Time) {
	if actor.Acquaintances == nil {
		actor.Acquaintances = make(map[string]Acquaintance)
	}
	if _, already := actor.Acquaintances[otherName]; already {
		return
	}
	actor.Acquaintances[otherName] = Acquaintance{FirstInteractedAt: at}
}

// RegisterAcquaintanceSubscriber wires the acquaintance subscriber into
// the world. Must run on the world goroutine (call before World.Run or
// from inside a Command.Fn).
//
// Idempotency: registering twice would dispatch handleAcquaintance
// twice per ActorMet. The second invocation sees the pair already
// recorded and short-circuits via the first-met gate — a redundant
// no-op, not a correctness violation. Worth not doing for telemetry
// hygiene but not a panic-worthy bug.
func RegisterAcquaintanceSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterAcquaintanceSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleAcquaintance))
}
