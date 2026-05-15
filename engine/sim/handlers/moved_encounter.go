package handlers

import (
	"log"
	"sort"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// moved_encounter.go — mid-route encounter detection.
//
// Subscribes to sim.ActorMoved. When an outdoor actor advances a tile,
// scans nearby outdoor non-huddled actors and atomically forms a huddle
// via StartOutdoorHuddle if any qualify. Mirrors handleArrivalEncounter
// — same encounter rules, but driven by mid-route ActorMoved instead
// of ActorArrived.
//
// Closes the "two NPCs walking past each other within encounter radius
// but neither arriving" semantic hole that PR 3e's arrival_encounter.go
// explicitly documented as future cascade-controller work. LOS-proper
// (terrain occlusion, sight lines) is still future work; bounded radius
// matches handleArrivalEncounter and is the scope here.
//
// SUBSCRIBER-LIGHTNESS BUDGET. ActorMoved fires once per tile per moving
// actor (~5 Hz per walker per locomotion ticker cadence). Synchronous
// emit dispatch puts the subscriber on the world goroutine for every
// step of every walk. Hot-path optimization: scan via
// World.ForEachOutdoorActor — iterates the outdoorActors secondary
// index rather than w.Actors, bounded by outdoor population. At 200+
// actor scale with most actors indoor at any moment, scan cost is
// dominated by outdoor density (typically a small fraction of total).
// Pre-filter is arranged cheapest-first so the common case (mover
// indoor / already in huddle / stale event) returns before any scan.
//
// SAME-TICK RACE WITH handleArrivalEncounter. On a final-tile arrival,
// locomotion_ticker.go emits ActorMoved first, then (still same tick)
// ActorArrived via finishArrival. handleMovedEncounter sees ActorMoved
// first and may form a huddle — the mover's CurrentHuddleID is set
// inside StartOutdoorHuddle's Fn. Then ActorArrived emits and
// handleArrivalEncounter runs, but its pre-filter
// ("arriver.CurrentHuddleID != "" → return") short-circuits. The
// huddle is minted exactly once; the race is benign by the existing
// pre-filter pattern, no new coordination needed.

// handleMovedEncounter is the ActorMoved subscriber. Registered by
// RegisterEncounterHandlers; runs synchronously on the world goroutine
// from inside sim.World.emit.
//
// Calling StartOutdoorHuddle(...).Fn(w) directly here is safe and
// correct: subscribers dispatch inline from emit, so we are already on
// the world goroutine. A SendContext would deadlock the goroutine. The
// resulting HuddleJoined / SceneMinted / ActorMet emits nest under the
// original ActorMoved's withRoot scope, so every consequent event
// carries the move's EventID as its RootEventID (PR 3a).
//
// Pre-filter (every condition must hold or the subscriber returns):
//
//   - Mover still exists in w.Actors.
//   - Mover's current position AND structure attribution match the
//     event's ToPosition / ToStructureID. Same event-freshness
//     invariant pattern as handleArrivalEncounter — a stale event whose
//     state disagrees with current actor state would anchor a huddle at
//     the wrong tile or (worse) mint an outdoor huddle for an actor now
//     indoor.
//   - Mover is outdoors (InsideStructureID == "") and not in an active
//     huddle. An indoor move (structure-entry tile transition) is the
//     arrival subscriber's domain; a moving actor already in a huddle
//     has bilateral-pause active.
//   - Eligible nearby actors exist. Same rule as handleArrival-
//     Encounter: outdoor + not in huddle + within radius
//     (SceneBound.Contains against an area bound centered on
//     ToPosition) + not the mover.
//
// Participant ordering: [mover, ...nearby sorted by ActorID]. Mover is
// the causal trigger and goes first so StartOutdoorHuddle's
// HuddleJoined / ActorMet sequence reflects "the mover caused the
// encounter"; sorting the rest gives deterministic event ordering
// across runs. Mirrors handleArrivalEncounter exactly.
//
// On StartOutdoorHuddle rejection: log and continue. Pre-filter
// already excluded ineligible actors; rejection here indicates an
// invariant violation (state drift between pre-filter and the
// command's own validation), not an expected race. No retry with a
// smaller participant set — the atomic command is the right level for
// "everyone or no one."
func handleMovedEncounter(w *sim.World, evt sim.Event) {
	moved, ok := evt.(*sim.ActorMoved)
	if !ok {
		return
	}
	mover, ok := w.Actors[moved.ActorID]
	if !ok {
		return
	}
	// Event freshness invariant: mover's current position AND structure
	// attribution must still match the event's destination tile. A
	// position-only check would accept a stale event whose ToStructureID
	// disagrees with current state — e.g. a "moved to (X,Y) outdoors"
	// event against an actor now standing on the same tile but indoor
	// because a structure footprint was reconciled in between, which
	// would otherwise mint an outdoor huddle from an indoor-now event.
	if mover.CurrentX != moved.ToPosition.X ||
		mover.CurrentY != moved.ToPosition.Y ||
		mover.InsideStructureID != moved.ToStructureID {
		log.Printf(
			"sim/handlers: moved-encounter skipped — mover %q state (%d,%d,%q) != event to (%d,%d,%q)",
			mover.ID,
			mover.CurrentX, mover.CurrentY, mover.InsideStructureID,
			moved.ToPosition.X, moved.ToPosition.Y, moved.ToStructureID,
		)
		return
	}
	if mover.InsideStructureID != "" || mover.CurrentHuddleID != "" {
		return
	}

	radius := w.Settings.DefaultOutdoorSceneRadius
	if radius <= 0 {
		radius = sim.DefaultOutdoorSceneRadiusValue
	}
	bound := sim.NewAreaBound(moved.ToPosition, radius)

	var nearby []sim.ActorID
	w.ForEachOutdoorActor(func(a *sim.Actor) bool {
		if a.ID == mover.ID {
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
	participants = append(participants, mover.ID)
	participants = append(participants, nearby...)

	if _, err := sim.StartOutdoorHuddle(participants, moved.ToPosition, radius, nil, moved.At).Fn(w); err != nil {
		log.Printf(
			"sim/handlers: moved-encounter StartOutdoorHuddle rejected for %v: %v",
			participants, err,
		)
	}
}
