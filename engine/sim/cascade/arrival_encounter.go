package cascade

import (
	"log"
	"sort"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// arrival_encounter.go — arrival-encounter detection.
//
// Subscribes to sim.ActorArrived. When an outdoor actor arrives, scans
// for other outdoor non-huddled actors within the world's default
// outdoor scene radius, and atomically forms a huddle including all of
// them via StartOutdoorHuddle (PR 4 — already mints its own area-bound
// scene atomically).
//
// Pairs with handleMovedEncounter (moved_encounter.go) which covers the
// mid-route case (two NPCs walking past each other within encounter
// radius without either arriving). LOS-proper (terrain occlusion, sight
// lines) is still future work; both subscribers use bounded radius.

// handleArrivalEncounter is the ActorArrived subscriber that detects
// outdoor encounters and forms huddles. Registered via
// RegisterEncounter; runs synchronously on the world goroutine
// from inside sim.World.emit.
//
// Calling StartOutdoorHuddle(...).Fn(w) directly here is safe and
// correct: subscribers dispatch inline from emit, so we are already
// on the world goroutine. A SendContext would deadlock the goroutine.
// The resulting HuddleJoined / SceneMinted / ActorMet emits nest under
// the original ActorArrived's withRoot scope, so every consequent
// event carries the arrival's EventID as its RootEventID (PR 3a).
//
// Pre-filter (every condition must hold or the subscriber returns):
//
//   - Arriver still exists, is outdoors (InsideStructureID == ""), and
//     is not already in an active huddle. A walking actor pulled into
//     a huddle by an earlier same-tick arrival subscriber is skipped
//     here, AND the locomotion ticker's per-iteration huddle re-check
//     would have skipped this finishArrival emit in the first place.
//   - Arriver's current position equals the event's FinalPosition.
//     Defensive invariant — if it does not hold, the event is stale
//     relative to the actor's true position and minting a huddle
//     anchored at the event coordinates would be wrong.
//   - Eligible nearby actors exist. Nearby = outdoor + within radius
//     (the same SceneBound.Contains rule StartOutdoorHuddle uses
//     internally) + not in any active huddle + not the arriver. We
//     do NOT filter on MoveIntent: pulling a walking actor into the
//     huddle is a legitimate interrupt (PR 4 bilateral-pause then
//     freezes their locomotion next tick, preserving MovementAttemptID
//     so they resume after leaving the huddle).
//
// Participant ordering: [arriver, ...nearby sorted by ActorID]. The
// arriver is the causal trigger and goes first so StartOutdoorHuddle's
// HuddleJoined / ActorMet sequence reflects "the arriver caused the
// encounter"; sorting the rest gives deterministic event ordering
// across runs.
//
// On StartOutdoorHuddle rejection: log and continue. The pre-filter
// already excluded ineligible actors, so a rejection here indicates an
// invariant violation (state drift between pre-filter and the command's
// own validation), not an expected race. No retry with a smaller
// participant set — the atomic command is the right level for
// "everyone or no one."
func handleArrivalEncounter(w *sim.World, evt sim.Event) {
	arrived, ok := evt.(*sim.ActorArrived)
	if !ok {
		return
	}
	arriver, ok := w.Actors[arrived.ActorID]
	if !ok {
		return
	}
	// Event freshness invariant: the arriver's current position AND
	// structure attribution must still match the event we're handling.
	// A position-only check would accept a stale event whose
	// FinalStructureID disagrees with current state — e.g. an event
	// stamped "arrived indoors at (X,Y)" against an actor now
	// outdoors at (X,Y), which would otherwise mint an outdoor huddle
	// from an indoor-arrival event. Reject any mismatch.
	if arriver.Pos.X != arrived.FinalPosition.X ||
		arriver.Pos.Y != arrived.FinalPosition.Y ||
		arriver.InsideStructureID != arrived.FinalStructureID {
		log.Printf(
			"sim/cascade: arrival-encounter skipped — arriver %q state (%d,%d,%q) != event final (%d,%d,%q)",
			arriver.ID,
			arriver.Pos.X, arriver.Pos.Y, arriver.InsideStructureID,
			arrived.FinalPosition.X, arrived.FinalPosition.Y, arrived.FinalStructureID,
		)
		return
	}
	if arriver.InsideStructureID != "" || arriver.CurrentHuddleID != "" {
		return
	}

	radius := w.Settings.DefaultOutdoorSceneRadius
	if radius <= 0 {
		radius = sim.DefaultOutdoorSceneRadiusValue
	}
	bound := sim.NewAreaBound(arrived.FinalPosition, radius)

	// Outdoor-set scan via World.ForEachOutdoorActor — iterates only
	// actors with InsideStructureID == "" (the outdoorActors secondary
	// index), bounded by outdoor population rather than total actor
	// count. SceneBound.Contains for an area bound already rejects
	// indoor actors, so this is purely an optimization with identical
	// semantics to the prior w.Actors scan.
	var nearby []sim.ActorID
	w.ForEachOutdoorActor(func(a *sim.Actor) bool {
		if a.ID == arriver.ID {
			return true
		}
		if a.CurrentHuddleID != "" {
			return true
		}
		if !bound.Contains(w, a) {
			return true
		}
		nearby = append(nearby, a.ID)
		return true
	})
	if len(nearby) == 0 {
		return
	}
	sort.Slice(nearby, func(i, j int) bool { return nearby[i] < nearby[j] })

	participants := make([]sim.ActorID, 0, len(nearby)+1)
	participants = append(participants, arriver.ID)
	participants = append(participants, nearby...)

	if _, err := sim.StartOutdoorHuddle(participants, arrived.FinalPosition, radius, nil, arrived.At).Fn(w); err != nil {
		log.Printf(
			"sim/cascade: arrival-encounter StartOutdoorHuddle rejected for %v: %v",
			participants, err,
		)
	}
}

// RegisterEncounter wires both encounter subscribers
// (arrival-driven + mid-route-move-driven) into the world. Separate
// from the tick-handler registrations because the two groups are
// independent — a world that wants encounter detection without the
// agent-tick pipeline (or vice versa) can opt in piecewise.
//
// Must run on the world goroutine (call before World.Run or from inside
// a Command.Fn).
//
// Idempotency: registering twice would dispatch each subscriber twice
// per event. The second invocation always observes the trigger actor
// already in the huddle the first invocation just minted, so its
// pre-filter (`CurrentHuddleID != "" → return`) short-circuits before
// any StartOutdoorHuddle call — a no-op, not an error. Tests pin this
// in TestArrivalEncounter_DoubleRegistrationProducesOneHuddle for the
// arrival subscriber; the moved subscriber inherits the same shape.
func RegisterEncounter(w *sim.World) {
	if w == nil {
		panic("cascade: RegisterEncounter requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handleArrivalEncounter))
	w.Subscribe(sim.SubscriberFunc(handleMovedEncounter))
}
