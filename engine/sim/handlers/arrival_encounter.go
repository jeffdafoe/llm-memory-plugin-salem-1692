package handlers

import (
	"log"
	"sort"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// arrival_encounter.go — PR 3e arrival-encounter detection.
//
// Subscribes to sim.ActorArrived. When an outdoor actor arrives, scans
// for other outdoor non-huddled actors within the world's default
// outdoor scene radius, and atomically forms a huddle including all of
// them via StartOutdoorHuddle (PR 4 — already mints its own area-bound
// scene atomically).
//
// Trigger is ActorArrived only. The "two NPCs walking past each other
// mid-route" case (LOS encounters off ActorMoved) is a documented
// semantic hole, deferred to the cascade-controller PR: per-tile
// scanning is expensive, an LOS filter needs design beyond a cheap
// nearby-actor check (terrain occlusion, sight lines), and bilateral-
// pause-on-huddle already limits the "ships passing" window to one
// tile — so the missed encounter only matters if neither actor arrives
// near the other.
//
// PR 3 explicitly does NOT own general scene origination (that's the
// later engine/sim/cascade/ controller PR). This subscriber is the one
// narrow slice PR 3 owns: arrival-driven outdoor huddle formation.

// handleArrivalEncounter is the ActorArrived subscriber that detects
// outdoor encounters and forms huddles. Registered via
// RegisterEncounterHandlers; runs synchronously on the world goroutine
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
	if arriver.CurrentX != arrived.FinalPosition.X ||
		arriver.CurrentY != arrived.FinalPosition.Y ||
		arriver.InsideStructureID != arrived.FinalStructureID {
		log.Printf(
			"sim/handlers: arrival-encounter skipped — arriver %q state (%d,%d,%q) != event final (%d,%d,%q)",
			arriver.ID,
			arriver.CurrentX, arriver.CurrentY, arriver.InsideStructureID,
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

	// O(actors) scan. At v2's actor counts (< 100 in dev) this is fine;
	// a spatial index on outdoor actors is the natural optimization if
	// the hot path grows. The pre-filter uses the same SceneBound.Contains
	// helper StartOutdoorHuddle uses internally, so eligibility here
	// matches the command's own validation rule exactly.
	var nearby []sim.ActorID
	for id, a := range w.Actors {
		if id == arriver.ID {
			continue
		}
		if a.CurrentHuddleID != "" {
			continue
		}
		if !bound.Contains(w, a) {
			continue
		}
		nearby = append(nearby, id)
	}
	if len(nearby) == 0 {
		return
	}
	sort.Slice(nearby, func(i, j int) bool { return nearby[i] < nearby[j] })

	participants := make([]sim.ActorID, 0, len(nearby)+1)
	participants = append(participants, arriver.ID)
	participants = append(participants, nearby...)

	if _, err := sim.StartOutdoorHuddle(participants, arrived.FinalPosition, radius, nil, arrived.At).Fn(w); err != nil {
		log.Printf(
			"sim/handlers: arrival-encounter StartOutdoorHuddle rejected for %v: %v",
			participants, err,
		)
	}
}

// RegisterEncounterHandlers wires the arrival-encounter subscriber into
// the world. Separate from RegisterTickHandlers because the two
// registrations are independent — a world that wants encounter
// detection without the agent-tick pipeline (or vice versa) can opt in
// piecewise — and because conflating them under one name would hide
// what is actually being registered.
//
// Must run on the world goroutine (call before World.Run or from inside
// a Command.Fn).
//
// Idempotency: registering twice would dispatch the subscriber twice
// per ActorArrived. The second invocation always observes the arriver
// already in the huddle the first invocation just minted, so its
// pre-filter (`CurrentHuddleID != "" → return`) short-circuits before
// any StartOutdoorHuddle call — a no-op, not an error. Tests pin this
// in TestArrivalEncounter_DoubleRegistrationProducesOneHuddle.
func RegisterEncounterHandlers(w *sim.World) {
	if w == nil {
		panic("handlers: RegisterEncounterHandlers requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handleArrivalEncounter))
}
