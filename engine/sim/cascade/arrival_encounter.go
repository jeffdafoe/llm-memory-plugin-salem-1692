package cascade

import (
	"log"
	"sort"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// outdoorEncounterExcludesActor reports whether an actor must be left out of an
// outdoor encounter — as the INITIATOR or as a nearby participant. Centralizing
// the rule keeps the two checks from drifting (the exact divergence code_review
// caught: the participant scan skipped stale PCs but the initiator path didn't).
//
// Rules:
//   - A stale (ghost) PC — a closed-tab player whose presence stamp has gone
//     stale (WS dropped; LLM-342) — must neither greet nor be greeted, so
//     co-located NPCs don't burn ticks on an absent player (ZBBS-WORK-326).
//   - A moving actor (one holding a MoveIntent) is mid-task and not available
//     to be pulled into a conversation (ZBBS-HOME-340). Encounters form only
//     among actors who are actually standing somewhere: the arriver has just
//     stopped (its MoveIntent was cleared on arrival), and any bystander still
//     walking is left alone rather than warranted into a greeting it can't act
//     on without abandoning its walk. This is what lets villagers walk past a
//     stationary NPC in silence instead of being yelled at, and is the
//     replacement for the removed locomotion "bilateral pause" — a mover is
//     never joined to a huddle in the first place, so a walk is never frozen.
//   - A sleeping or resting actor is bedded down or has stepped away
//     (take_break / dwelling at a rest object) and is not available to be
//     pulled into a conversation. Mirrors the keeper-side gate in
//     businessowner.go, which already skips a StateSleeping/StateResting
//     keeper — without the same gate here, a villager arriving near an NPC
//     resting under a shade tree formed a huddle that greeted a sleeper, who
//     read the silence as rudeness (ZBBS-WORK-425, residual of HOME-436).
//     StateSleeping is bedded-down indoors, so outdoors this is reached by
//     StateResting in practice; both are excluded for parity with the keeper
//     gate and so a future outdoor sleep state is covered too.
//   - An actor with an active scheduled route (ZBBS-HOME-452) is mid-task in
//     exactly the HOME-340 sense, but invisibly to the MoveIntent check:
//     finishArrival clears the intent BEFORE emitting ActorArrived, so at an
//     intermediate route stop the actor looks "stopped" while it is actually
//     between stop N and stop N+1. Without this exclusion, arriving at a stop
//     within radius of a standing actor formed a huddle on the SAME emit whose
//     route-advance subscriber then had its next-walk dispatch rejected by
//     that huddle — clearing the route (remaining stops never flipped) and,
//     for a decorative NPC + silent PC pair, parking the actor at the stop for
//     the full 2h huddle-silence-sweep window (the 2026-06-12 lamplighter
//     wedge). Applies to both directions: a route actor must not initiate an
//     encounter at its own stop, and must not be grabbed as a bystander while
//     standing at one. The returning-home leg is excluded too — the actor is
//     on duty until the route is actually done (advanceReturningRoute deletes
//     it on home arrival).
//
// w must be non-nil by contract: the only callers are handleArrivalEncounter's
// initiator and participant checks, which run as event subscribers on the
// world goroutine and always hold the real World. A nil-tolerant guard here
// would silently skip route-awareness on a caller bug instead of surfacing it.
func outdoorEncounterExcludesActor(w *sim.World, a *sim.Actor, now time.Time, staleAfter time.Duration) bool {
	if a.MoveIntent != nil {
		return true
	}
	if a.State == sim.StateSleeping || a.State == sim.StateResting {
		return true
	}
	if _, onRoute := w.ActiveRoutes[a.ID]; onRoute {
		return true
	}
	// LLM-375: an actor at an OPEN stall's loiter pin belongs to the keeper's
	// structure conversation across the threshold, not an open-ground encounter.
	// Excluding it here — for both the arriver and each nearby candidate — stops a
	// second customer's arrival from grabbing the two co-loiterers into a peer
	// huddle that shadows the stall, after which neither could resolve the keeper
	// for a pay / quote / greet. Their own speak or transaction forms/joins the
	// keeper's structure huddle instead (EnsureColocatedHuddle). A SHUT stall
	// scopes to "" (LLM-359), so loiterers there still meet on open ground.
	if sim.InOpenLoiterStallScope(w, a) {
		return true
	}
	return a.Kind == sim.KindPC && sim.PCPresenceStale(a.LastPCSeenAt, now, staleAfter)
}

// arrival_encounter.go — arrival-encounter detection.
//
// Subscribes to sim.ActorArrived. When an outdoor actor arrives, scans
// for other outdoor non-huddled actors within the world's default
// outdoor scene radius, and atomically forms a huddle including all of
// them via StartOutdoorHuddle (PR 4 — already mints its own area-bound
// scene atomically).
//
// Arrival is the ONLY outdoor-encounter trigger: a huddle forms when an
// actor stops somewhere, never while walking past (the mid-route
// "moved encounter" was removed in ZBBS-HOME-340 — it pulled stationary
// villagers into greetings with every passerby and froze the walker via
// the locomotion bilateral pause). LOS-proper (terrain occlusion, sight
// lines) is still future work; the radius scan is bounded.

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
//     internally) + not in any active huddle + not the arriver + not
//     mid-move. Since ZBBS-HOME-340 a moving actor IS excluded
//     (outdoorEncounterExcludesActor returns true for MoveIntent != nil):
//     the old "bilateral pause" that froze a walker after pulling it into
//     a huddle is gone, so a mover is simply never joined in the first
//     place — it walks past a stationary actor in silence rather than
//     being grabbed into a greeting it cannot act on.
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
	// A knock arrival belongs to the knock service-huddle bootstrap
	// (ZBBS-HOME-445, business_arrival.go) — the knocker's attention is on
	// the door, not on passersby. Explicit skip rather than relying on
	// subscriber registration order: if the encounter fired first it would
	// grab the knocker into an outdoor huddle and the knock bootstrap's
	// already-huddled gate would then drop the knock on the floor.
	if arrived.Knocked {
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

	staleAfter := sim.PCPresenceStaleAfter(w)
	// A stale (ghost) PC must not INITIATE an outdoor encounter either, not just
	// be skipped as a nearby participant — otherwise a ghost arriving (e.g. an
	// in-flight move that completes after the tab closed) would still pull nearby
	// NPCs into a greeting huddle (ZBBS-WORK-326, code_review R1).
	if outdoorEncounterExcludesActor(w, arriver, arrived.At, staleAfter) {
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
		if outdoorEncounterExcludesActor(w, a, arrived.At, staleAfter) {
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

// RegisterEncounter wires the arrival-encounter subscriber into the
// world. Separate from the tick-handler registrations because the two
// groups are independent — a world that wants encounter detection
// without the agent-tick pipeline (or vice versa) can opt in piecewise.
//
// Must run on the world goroutine (call before World.Run or from inside
// a Command.Fn).
//
// Idempotency: registering twice would dispatch the subscriber twice
// per event. The second invocation always observes the trigger actor
// already in the huddle the first invocation just minted, so its
// pre-filter (`CurrentHuddleID != "" → return`) short-circuits before
// any StartOutdoorHuddle call — a no-op, not an error. Tests pin this
// in TestArrivalEncounter_DoubleRegistrationProducesOneHuddle.
func RegisterEncounter(w *sim.World) {
	if w == nil {
		panic("cascade: RegisterEncounter requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handleArrivalEncounter))
}
