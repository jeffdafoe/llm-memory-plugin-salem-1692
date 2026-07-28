package sim

import (
	"fmt"
	"log"
	"time"
)

// npc_route.go — substrate for scheduled NPC routes. Lamplighter walks
// the lamps at dawn/dusk; washerwoman and town_crier walk their domains
// (laundry tiles / notice boards) at their own schedule-window
// boundaries — once at window start, once at window end (ZBBS-HOME-446;
// previously both hung off the daily rotation boundary).
//
// All three share the same skeleton: a list of RouteStop entries (each
// an object to visit with a pre-decided NewState), a phase ("active"
// while visiting stops, "returning" on the home leg), and a StopIdx
// cursor. Per-behavior logic (which candidates to visit, what target
// state to land in) lives in the cascade driver — it builds the
// candidate list and calls StartNPCRoute.
//
// The driver wires the lifecycle:
//
//   - PhaseApplied (lamplighter) / a schedule-window boundary observed
//     by the cascade route-schedule ticker (washerwoman / town_crier,
//     via RouteBoundaryDue below) → start route via StartNPCRoute
//   - ActorArrived for an actor with an entry in World.ActiveRoutes →
//     advance route via AdvanceNPCRoute (flip current stop's state,
//     dispatch next walk OR transition to returning OR clear)
//
// The substrate owns: route state shape, the StartNPCRoute Command
// (which builds the ordered stop list via nearest-neighbor pathfinding
// and dispatches the first MoveActor), the AdvanceNPCRoute Command
// (which flips state at the current stop and dispatches the next walk).
//
// On-stop side-effects beyond the village_object state flip (e.g. the
// town_crier reading an announcement) are deferred to Slice 3. Today
// AdvanceNPCRoute is purely the route-walking machinery; per-NPC
// flavor lives in the cascade driver.

// hasActorWithAttribute reports whether any actor in the world carries
// the given attribute slug. Used by ApplyPhaseTransition to decide
// whether to carve out the lamplighter-target tag from the bulk flip
// (only when an actor will actually consume the carve-out).
//
// MUST be called from inside a Command.Fn (reads w.Actors).
func hasActorWithAttribute(w *World, slug string) bool {
	for _, a := range w.Actors {
		if a == nil {
			continue
		}
		if _, ok := a.Attributes[slug]; ok {
			return true
		}
	}
	return false
}

// Attribute slugs that carry route behavior. The cascade dispatcher
// scans Actor.Attributes for one of these to find the eligible actor.
// Empty values are fine — these are marker attributes, not parameterised.
const (
	// AttrLamplighter — actor walks the lamplighter-target objects at
	// each day/night phase boundary, flipping them to the target tag's
	// state. At most one actor per world should carry this attribute;
	// the dispatcher picks deterministically by ActorID when multiple
	// carriers exist.
	AttrLamplighter = "lamplighter"

	// AttrWasherwoman — actor walks the laundry-tagged objects at her
	// schedule-window boundaries: hangs laundry out (default state →
	// variant) at window start, brings it in (variant → default state)
	// at window end.
	AttrWasherwoman = "washerwoman"

	// AttrTownCrier — actor walks the notice-board-tagged objects at
	// her schedule-window boundaries (same walk at both): reads each
	// board's authored prose aloud on arrival, then flips the board so
	// fresh prose is authored for the next visit.
	AttrTownCrier = "town_crier"

	// AttrConstable — a stateful watch-keeper (Gideon Marsh) who owes a circuit
	// of every business in the village on a fixed INTERVAL rather than a
	// schedule-window boundary (LLM-514). Unlike the three above he is not
	// WALKED: his is a beat (RoutePhaseBeat, LLM-548), so the engine holds which
	// places he still owes and he takes himself to them, entering the open ones
	// and standing at the door of the shut. He returns to his post (the Meeting
	// House) rather than home, under the ordinary shift-duty steer once the
	// circuit is covered. A marker attribute; the live actor gets it added
	// out-of-band, the code only recognizes it. The interval is a w.Settings
	// tunable and carries a per-carrier deterministic phase offset
	// (ConstableRoundsDue) so two carriers never fire on the same tick.
	AttrConstable = "constable"
)

// Tag slugs the route dispatcher narrows candidates by.
const (
	TagLaundry     = "laundry"
	TagNoticeBoard = "notice-board"
)

// RoutePhase discriminates a route's legs. Active walks candidate stops in
// order; Returning is the home leg after the last stop. AdvanceNPCRoute's
// behavior depends on phase: an arrival in Active flips the current stop's
// state and dispatches the next walk; an arrival in Returning clears the route.
// Beat is the volition-carrier phase and dispatches nothing at all.
type RoutePhase string

const (
	RoutePhaseActive    RoutePhase = "active"
	RoutePhaseReturning RoutePhase = "returning"

	// RoutePhaseBeat — the route is a RECORD of a circuit the carrier owes, not
	// an itinerary the engine walks him through (LLM-548). The engine dispatches
	// no walk, ever: not the first, not between stops, not the leg home. It holds
	// which places are still owed, the perception cue names them, and the carrier
	// takes himself there. Advancing is crediting where he actually turned up.
	//
	// This is the phase for a carrier whose own model issues move_to. The engine
	// cannot walk such an actor anywhere he has not chosen to go — every attempt
	// became a tug-of-war with his next turn — and it does not need to: live he
	// walked five of eight stops himself, in his own order, and came back to the
	// one he had broken off at. Dispatching was the part that failed, not the part
	// that worked (the round died when a walk was refused because he was still
	// standing in a conversation that had gone quiet).
	//
	// A beat route is never "in flight" — see NPCRoute.InFlight. Nothing about
	// owing a circuit makes him unavailable, so shift duty, the idle backstop,
	// arrival encounters and the rounds-due gate all behave exactly as they would
	// for a man with no route. That is what lets an ordinary encounter form when
	// he turns up somewhere, which is how the conversations at his stops happen.
	RoutePhaseBeat RoutePhase = "beat"
)

// maxStaleRouteRetries bounds how many times a single stop is re-walked after a
// stale arrival (the actor finished a move to somewhere other than the stop's
// WalkTo — an external MoveActor superseded the route's walk) before the route
// gives up on the stop and abandons. Without a re-walk a single bump stranded
// the stop forever; without a bound, a producer that persistently bumps the
// actor would re-walk it indefinitely. Per-stop budget, reset on each clean
// visit (see advanceActiveRoute).
const maxStaleRouteRetries = 3

// routeIsBeat reports whether a route belongs to a VOLITION carrier — one whose
// own LLM reactor issues move_to — rather than a decorative carrier (lamplighter
// / washerwoman / town_crier) that never self-moves and must be walked.
//
// The distinction decides which advancer runs, and they are opposites: a
// decorative route dispatches every leg and treats an off-stop arrival as an
// external bump to undo; a beat route dispatches nothing and treats any arrival
// on the circuit as the carrier doing his job (LLM-548). The constable is the
// only volition carrier today. The traveller is the obvious next one — he already
// walks himself and records where he went, and lacks only the circuit.
func routeIsBeat(route *NPCRoute) bool {
	return route != nil && labelIsBeat(route.Label)
}

// labelIsBeat is routeIsBeat by carrier label, for StartNPCRoute — which must pick
// the phase before there is a route to ask about. Single source of the carrier set.
func labelIsBeat(label string) bool {
	return label == AttrConstable
}

// RouteStop is one object the route visits with a pre-decided target
// state. WalkTo is the grid-tile destination the actor moves to —
// typically the adjacent walkable tile next to the object's anchor.
// NewState is the AssetState.State the object's CurrentState flips to
// on arrival.
//
// EnterStructureID (LLM-514) opts the stop into ENTERING a structure
// rather than standing at a loiter tile: when set, the actor walks into
// that structure's interior (a StructureEnter move) and arrival is keyed
// on Actor.InsideStructureID == EnterStructureID instead of Pos == WalkTo.
// It is the reusable "enter vs. loiter" primitive — ANY route may set it
// per stop. Empty (the default) keeps the original tile-based behavior, so
// the lamplighter / washerwoman / town_crier routes are unchanged (their
// candidates are doorless objects, never structure-backed businesses).
// WalkTo is still populated for an enter stop — the door tile — so the
// nearest-neighbor layout and cursor bookkeeping in buildRouteStops stay
// tile-based; only arrival detection and dispatch branch on this field.
type RouteStop struct {
	ObjectID         VillageObjectID
	WalkTo           Position
	NewState         string
	EnterStructureID StructureID
}

// RouteStopArrived reports whether the ROUTE'S OWN dispatched walk to stop has
// completed: the actor stands inside the target structure for an ENTER stop, or on
// the WalkTo tile for a loiter/tile stop. The two signals differ because a
// StructureEnter move finishes when the locomotion ticker flips InsideStructureID,
// whereas a Position move finishes on the exact tile it aimed at.
//
// This is a DISPATCH-COMPLETION question, not a location question. WalkTo is where
// the route sent him — `buildRouteStops` picks a *walkable* tile near the object,
// which is not the object's own pin whenever the pin's tile cannot be stood on. So
// "he is on WalkTo" means "the walk I issued finished", and nothing more.
func RouteStopArrived(a *Actor, stop RouteStop) bool {
	if a == nil {
		return false
	}
	if stop.EnterStructureID != "" {
		return a.InsideStructureID == stop.EnterStructureID
	}
	return a.Pos.X == stop.WalkTo.X && a.Pos.Y == stop.WalkTo.Y
}

// ActorAtRouteStopPlace reports whether the actor is AT the stop's place, however he
// came to be there — inside the structure for an enter stop, at the object's own
// loiter pin for a loiter stop. The location question, asked of the shared
// object-keyed predicate (LLM-550).
//
// The stop itself declares which posture counts, rather than this re-deriving it
// from the asset. `resolveRouteStop` already made that judgement at build time via
// moveToCanEnter: an open, enterable business became an ENTER stop, and a closed or
// locked one was downgraded to a loiter stop precisely so the carrier stands at its
// door instead. Re-deciding here could contradict the stop he was actually sent to.
func ActorAtRouteStopPlace(objects map[VillageObjectID]*VillageObject, assets map[AssetID]*Asset, a *Actor, stop RouteStop) bool {
	if a == nil {
		return false
	}
	if stop.EnterStructureID != "" {
		return a.InsideStructureID == stop.EnterStructureID
	}
	return ActorAtObjectPin(objects, assets, a.Pos, stop.ObjectID)
}

// RouteStopReached is the single "is this carrier at this stop?" test, for every
// site that asks the question. Two independent sufficient conditions, and which
// apply depends on what could have put the carrier where he stands:
//
//   - A DECORATIVE carrier never self-moves. The route's own walk is the only thing
//     that can have brought it anywhere, so dispatch completion IS the answer — and
//     a location test would mask the external bump the stale-arrival re-walk exists
//     to undo. Unchanged, byte for byte.
//   - A BEAT carrier is asked the location question, and ONLY that.
//
// **A beat must not accept the dispatch arm, even as a fallback** (code_review). It
// is tempting to keep it as a superset "so nothing is lost", but a beat dispatches no
// walk at any point, so there is no dispatched arrival to lose — that safety argument
// belongs to the decorative case and does not carry over. What it would add instead
// is a way to credit a stop the carrier is not at: WalkTo is an ordinary walkable
// tile beside a business, and nothing here can tell "the route put him there" (it
// never does) from a teleport, an admin force-move, a competing movement producer, or
// simply wandering across it. Where WalkTo lies inside the pin's ring the arm is
// redundant; where it lies outside, it credits a tile that is NOT the place — the
// very error this function was rewritten to stop making.
//
// A nil world means a beat cannot answer its question, so it answers no. Crediting on
// the dispatch arm instead would be that unsafe inference, taken at exactly the
// moment there is least information.
//
// **Why the location arm is not more tile arithmetic.** It used to be: a tolerance
// of LoiterAttributionTiles around `stop.WalkTo`. That reads as the pin's own
// footprint but is not — WalkTo is the route's *pathing goal*, displaced from the
// object's anchor whenever the anchor is unwalkable, while the carrier's own
// move_to parks him via pickVisitorSlot around the ANCHOR. Live on 2026-07-28 those
// were 2 tiles apart at the PW Apothecary: the constable called there three times,
// held a full conversation each visit, was credited none of them, and walked the
// village without stopping for half an hour because the round could never finish.
// Every other stop on that circuit had a pin within a tile of its anchor and
// credited fine, which is exactly why it took a live case to surface.
//
// The fix is to ask the place, not the tile. ActorAtObjectPin measures from the
// object's OWN loiter pin, so no caller's cached tile can drift from what the place
// actually is.
func RouteStopReached(w *World, route *NPCRoute, a *Actor, stop RouteStop) bool {
	if route == nil || a == nil {
		return false
	}
	if routeIsBeat(route) {
		return w != nil && ActorAtRouteStopPlace(w.VillageObjects, w.Assets, a, stop)
	}
	return RouteStopArrived(a, stop)
}

// routeStopDestination builds the MoveDestination that dispatches a walk to
// stop: a StructureEnter into EnterStructureID for an enter stop, else a
// Position walk to WalkTo. Shared by StartNPCRoute's first walk and
// advanceActiveRoute's next-stop / stale re-walk dispatch so every dispatch
// site agrees on enter-vs-tile.
func routeStopDestination(stop RouteStop) MoveDestination {
	if stop.EnterStructureID != "" {
		return NewStructureEnterDestination(stop.EnterStructureID)
	}
	return NewPositionDestination(stop.WalkTo)
}

// routeStopEntersStructure is the reusable enter-vs-loiter RULE (LLM-514): a route
// visits objID by ENTERING it when the placement is structure-backed (its id also
// names a Structure under the shared-identity bridge) AND actor can actually enter
// it right now. Doorless / open placements (market stalls, farms, the mill), bare
// props (lamps, boards, laundry lines), and structures the actor may NOT enter at
// `now` all fall through — the caller stands at the loiter pin instead. Returns the
// StructureID to enter and ok=true only for the enter case.
//
// The "can enter now" check reuses moveToCanEnter — the SAME gate MoveActor's
// StructureEnter validation and the PC move-to derivation use (effectiveEntryPolicy
// + membership + door). This is what keeps the constable OUT of a closed or locked
// business: a lodge whose keeper is abed reads owner-only (lodgeLocked), and a
// non-member constable resolves to a loiter stop at the door rather than an enter
// (LLM-514 fix #8 — John Ellis's tavern before he opens at 3pm). Never reimplement
// the rule here, or the rounds gate drifts from the movement gate.
//
// MUST be called from inside a Command.Fn (reads world maps).
func routeStopEntersStructure(w *World, actor *Actor, objID VillageObjectID, now time.Time) (StructureID, bool) {
	sid := StructureID(objID)
	if _, isStructure := w.Structures[sid]; !isStructure {
		return "", false
	}
	if !moveToCanEnter(w, actor, sid, now) {
		return "", false
	}
	return sid, true
}

// NPCRoute is the in-flight per-NPC route state. Stored in
// World.ActiveRoutes keyed by ActorID. Owned by the world goroutine
// (mutated only from inside Command.Fn).
//
// Label is the route's caller-supplied tag ("lamplighter", "washerwoman",
// "town_crier") — kept for log lines and future per-label side-effects
// (Slice 3 will branch on it for the town_crier on-stop reading).
//
// HomeDestination is the MoveDestination the actor walks to after the
// last stop. Typically a MoveDestinationStructureEnter on the actor's
// HomeStructureID so the locomotion ticker handles door-tile
// resolution + InsideStructureID re-entry automatically. Actors with
// no home structure get a MoveDestinationPosition at their start tile
// (route is effectively a one-way: visit all stops, stand at the
// last reachable tile).
type NPCRoute struct {
	NPCID           ActorID
	Label           string
	Stops           []RouteStop
	StopIdx         int
	Phase           RoutePhase
	HomeDestination MoveDestination
	// StaleRetries counts consecutive stale arrivals at the current stop (Stops
	// [StopIdx]) — re-walk attempts since the last clean visit. Reset to 0 each
	// time a stop is cleanly visited and the cursor advances. Once it reaches
	// maxStaleRouteRetries the route abandons. In-memory only; routes are
	// transient (re-triggered at the next phase/rotation boundary), so this is
	// never persisted.
	StaleRetries int
	// Authoring is set true while the town crier has an off-world noticeboard
	// author call in flight for the current stop, so a duplicate ActorArrived
	// for the same stop doesn't start a second author/read (LLM-44). Cleared
	// when the author callback completes. Set/read/cleared only on the world
	// goroutine; in-memory only, never persisted.
	Authoring bool
	// Visited marks, per stop index, whether he has actually CALLED AT that place on
	// this round — however he got there. Parallel to Stops.
	//
	// A cursor alone models a circuit as a sequence, but a constable walking his own
	// rounds does set coverage: he picks his own order, and the engine's job is to
	// know what is left (LLM-543). Live, he walked the General Store, the Blacksmith
	// and the Inn under his own steam inside twenty minutes and the round recorded
	// none of it, so the cue went on telling him seven places lay ahead while he stood
	// in the seventh. StopIdx names the next place he still OWES a visit, and this is
	// the record of what he no longer does. For a beat route it is the ONLY progress
	// state that means anything — nothing dispatches him, so the cursor is a hint about
	// what to name next rather than a position in a walk (LLM-548).
	//
	// In-memory only, like StaleRetries: routes are transient and re-triggered at the
	// next interval beat, so a restart simply starts a fresh round.
	Visited []bool
	// Gen is a monotonically-increasing identity token stamped at StartNPCRoute
	// install (from World.routeInstallSeq). It distinguishes two routes of the SAME
	// actor — the case StopIdx alone cannot, since a fresh round starts at the same
	// cursor the last one did. Reported by /umbilical/npc-routes, where comparing it
	// across two reads is how an operator tells "still the same round" from "he has
	// been re-dispatched since". World-goroutine-only; never persisted.
	Gen uint64
}

// InFlight reports whether the route is actively carrying the actor — i.e. the
// engine has a walk out for it and the actor's ordinary behaviour should stay
// suppressed. False for a nil route and for a BEAT one (LLM-548): a beat route
// dispatches no walk at any point, so its carrier is never being carried. He owes
// a circuit, which is not the same as being busy — shift duty, the idle backstop
// and arrival encounters must all behave exactly as if he had no route at all.
//
// That last one is load-bearing rather than incidental: outdoorEncounterExcludesActor
// keys on this, so a beat carrier turning up at a shop can be drawn into an ordinary
// encounter. Under the old dispatched round he was excluded for the whole tour and
// the engine had to manufacture the same effect with a timed dwell.
//
// Every "is this actor on a route?" test outside the route machinery itself goes
// through this (or RouteInFlight), so adding a phase can't silently strand an actor
// in a state that some caller still reads as "busy".
func (r *NPCRoute) InFlight() bool {
	return r != nil && r.Phase != RoutePhaseBeat
}

// RouteInFlight reports whether actorID has an in-flight (dispatched) route.
// The world-level form of NPCRoute.InFlight, for callers holding only the world.
// MUST be called from inside a Command.Fn (reads world maps).
func RouteInFlight(w *World, actorID ActorID) bool {
	return w.ActiveRoutes[actorID].InFlight()
}

// markVisited records that the carrier called at stop idx. Tolerant of a nil/short
// Visited slice so a route built by an older path (or a test fixture) degrades to
// the pure-cursor behaviour rather than panicking.
func (r *NPCRoute) markVisited(idx int) {
	if r == nil || idx < 0 || idx >= len(r.Visited) {
		return
	}
	r.Visited[idx] = true
}

// hasVisited reports whether stop idx has already been called at.
func (r *NPCRoute) hasVisited(idx int) bool {
	return r != nil && idx >= 0 && idx < len(r.Visited) && r.Visited[idx]
}

// tracksVisits reports whether the route carries a usable per-stop visit record.
// StartNPCRoute allocates one for every route it installs; a route assembled by
// hand (a test fixture, or some future call site) may not have.
//
// Every helper below degrades to the plain cursor walk when this is false, rather
// than treating "no record" as "nothing visited". That distinction is load-bearing:
// nextUnvisitedFrom's wrap terminates only because each advance marks a stop, so
// with no record to mark, a wrapping search would hand back the stop it started
// from and walk the carrier into it forever. A hand-built one-stop crier route did
// exactly that.
func (r *NPCRoute) tracksVisits() bool {
	return r != nil && len(r.Visited) == len(r.Stops)
}

// nextUnvisitedFrom returns the next stop the carrier still owes a visit, searching
// forward from idx and then WRAPPING to the start. ok=false means the circuit is
// walked out and the route is done.
//
// The wrap is load-bearing, not tidiness. A stop can be left behind the cursor
// unvisited: the LLM-530 adopt moves the cursor onward when he walks to the next
// business himself, and the place he turned away from stays unwalked. Searching only
// forward would leave it unreachable — the count would sit at one for the rest of the
// day and the round could never finish, which is the same "never finishes" complaint
// one layer along. Forward-first keeps the nearest-neighbour ordering the route was
// laid out in; the wrap only picks up what he skipped.
//
// Terminates: every advance marks a stop visited, so at most len(Stops) of them.
func (r *NPCRoute) nextUnvisitedFrom(idx int) (int, bool) {
	if r == nil || len(r.Stops) == 0 {
		return 0, false
	}
	if !r.tracksVisits() {
		return idx + 1, idx+1 < len(r.Stops)
	}
	for offset := 1; offset <= len(r.Stops); offset++ {
		candidate := (idx + offset) % len(r.Stops)
		if !r.hasVisited(candidate) {
			return candidate, true
		}
	}
	return 0, false
}

// unvisitedExcluding counts the stops he still owes a visit, NOT counting the one at
// idx — the place he is standing at. That is what the cue means by "more places on
// your round still lie ahead of you": the stop he is at is the subject of its own
// sentence, so counting it again would say one more place than there is.
func (r *NPCRoute) unvisitedExcluding(idx int) int {
	if r == nil {
		return 0
	}
	if !r.tracksVisits() {
		if n := len(r.Stops) - idx - 1; n > 0 {
			return n
		}
		return 0
	}
	n := 0
	for i := range r.Stops {
		if i != idx && !r.hasVisited(i) {
			n++
		}
	}
	return n
}

// reachedStopIndex returns the index of the circuit stop the actor is standing at,
// or ok=false when he is at none of them. This is how a beat credits a place he
// called at of his own accord — the engine sent him nowhere, but he was demonstrably
// there (it runs from an ActorArrived, so a completed move ended here; it is never a
// walk-past).
//
// Resolution order matters when two stops' tolerant regions overlap (two businesses
// close enough to share a doorstep). Plain first-match would let a neighbouring pin
// answer for the one he actually walked to.
//
//  1. The cursor wins, PROVIDED it is still unvisited. It is the stop the cue named
//     as next, so it is the one he is most likely to have set out for, and an
//     ambiguous position should resolve to the place he was told about rather than
//     to a neighbour.
//  2. Otherwise the first UNVISITED match. A stop already recorded has nothing to
//     add, and preferring it would let a visited neighbour keep answering for a stop
//     he still owes — which would never then be recorded.
//
// Matching only visited stops reports none: the visit is already on the books.
//
// The unvisited condition on the cursor is a GUARD, not a live case. advanceBeatRoute
// only ever moves the cursor via nextUnvisitedFrom, and StartNPCRoute installs it at
// 0 with nothing visited, so a beat's cursor is unvisited by construction. But that
// invariant lives in a different function from this one, and if it ever broke, an
// unguarded cursor preference would answer with an already-recorded stop while an
// unvisited neighbour sharing its ground went on being owed: the credit would no-op,
// the cursor would step to the neighbour, and a second arrival at the SAME position
// would then credit it without the actor having moved at all. With enough overlap a
// circuit could complete on stops he never walked. Making the guard explicit costs a
// bounds-checked bool and removes the need to hold the invariant in mind here
// (code_review, LLM-548).
func (r *NPCRoute) reachedStopIndex(w *World, a *Actor) (int, bool) {
	if r == nil || a == nil {
		return 0, false
	}
	if r.StopIdx >= 0 && r.StopIdx < len(r.Stops) &&
		!r.hasVisited(r.StopIdx) &&
		RouteStopReached(w, r, a, r.Stops[r.StopIdx]) {
		return r.StopIdx, true
	}
	for i, stop := range r.Stops {
		if !r.hasVisited(i) && RouteStopReached(w, r, a, stop) {
			return i, true
		}
	}
	return 0, false
}

// stopIndexAt returns the index of any circuit stop the actor is standing at,
// visited or not. This is the DISPLAY question — "can the cue say he stands before
// somewhere" — and it is deliberately not reachedStopIndex, which answers the
// CREDITING question and reports nothing once a stop is on the books.
//
// The two must differ because a beat credits a stop the instant he arrives. From
// that moment he is standing at a stop that is already recorded, which is precisely
// when the cue wants to name it; asking the crediting predicate would leave "you
// stand before the smithy" unsayable for the whole of every visit.
//
// First match wins. Two stops whose tolerant regions overlap are near enough to
// share a doorstep, so either name describes where he is standing.
func (r *NPCRoute) stopIndexAt(w *World, a *Actor) (int, bool) {
	if r == nil || a == nil {
		return 0, false
	}
	for i, stop := range r.Stops {
		if RouteStopReached(w, r, a, stop) {
			return i, true
		}
	}
	return 0, false
}

// unvisitedCount returns how many circuit stops are still owed a visit. The count
// the cue speaks and the beat logs, so the prompt and the journal can never tell an
// operator two different stories about how much of the round is left.
func (r *NPCRoute) unvisitedCount() int {
	return r.unvisitedExcluding(-1)
}

// RouteUnvisitedCount and RouteStopVisited expose the visited record to the
// umbilical read route, which is outside this package and must not touch the
// unexported helpers. Both are nil-safe reads; neither mutates.
func RouteUnvisitedCount(r *NPCRoute) int { return r.unvisitedCount() }

func RouteStopVisited(r *NPCRoute, idx int) bool { return r.hasVisited(idx) }

// clearActiveRoute removes an actor's route. Use this everywhere in place of a bare
// delete(w.ActiveRoutes, id) so every route-ending path stays one call.
// MUST be called from inside a Command.Fn.
func clearActiveRoute(w *World, actorID ActorID) {
	delete(w.ActiveRoutes, actorID)
}

// ClearActiveRoute is the exported form of clearActiveRoute, for the cascade
// abandon path (handleActorMoveStoppedAdvanceRoute) which must also stop the dwell
// timer, not just delete the map entry. MUST be called from inside a Command.Fn.
func ClearActiveRoute(w *World, actorID ActorID) {
	clearActiveRoute(w, actorID)
}

// RouteCandidate is one input to StartNPCRoute's route builder: an
// object to visit with a pre-decided target state. The substrate orders
// candidates into a nearest-neighbor walk via FindPathToAdjacent;
// callers don't pre-order (the cascade may scan w.VillageObjects in
// map-iteration order, which Go randomizes — the substrate is the
// right place to canonicalize).
//
// WorldX / WorldY are the village_object's pixel-coord anchor —
// converted to tile coords internally via WorldToTile (PadX/PadY
// offsets included).
//
// Enter (LLM-514) opts THIS candidate into entering its backing structure when it
// is a door-backed structure (routeStopEntersStructure). It is OPT-IN, defaulting
// false: the lamplighter / washerwoman / town_crier builders never set it, so their
// candidates keep the tile/loiter behavior byte-for-byte even if a candidate object
// happens to be structure-backed. Only buildConstableRoundsCandidates sets it true.
type RouteCandidate struct {
	ObjectID VillageObjectID
	NewState string
	WorldX   float64
	WorldY   float64
	Enter    bool
}

// StartNPCRouteResult is the typed reply from StartNPCRoute. Carries
// the count of stops the route was laid out with — callers (cascade
// subscribers) log it; tests assert on it.
type StartNPCRouteResult struct {
	NPCID    ActorID
	Label    string
	Stops    int
	Replaced bool // true when an in-flight route was superseded
}

// StartNPCRoute returns a Command that installs a new NPCRoute for the
// given actor and dispatches the first MoveActor. Cancels any prior
// in-flight route on the same actor (the new route is the observable
// transition; the prior one dies silently — same shape as MoveActor's
// supersede contract).
//
// The candidate list is laid out into a nearest-neighbor walk:
//
//  1. Compute the actor's current tile.
//  2. Build the walk grid via buildWalkGrid.
//  3. Repeatedly pick the candidate whose adjacent walkable tile is
//     shortest from the current position; advance the cursor to that
//     neighbor; remove the candidate from the remaining set. O(n²)
//     A* calls in the worst case — fine for the dozen-or-so candidates
//     a village-scale route carries.
//
// Empty candidate list (or all unreachable) returns Stops=0 and no
// MoveActor dispatch — the caller's cascade subscriber treats it as a
// no-op. Replaced still reports whether a prior route was superseded:
// the supersede semantic applies independent of new-route content, so
// a re-trigger with no candidates still clears any in-flight route.
//
// InsideStructureID is NOT mutated here — the locomotion ticker's
// per-tile updateInsideStructureIDFromTileOwnership reconciles it as
// the actor steps off the home footprint, and the home-walk's
// MoveDestinationStructureEnter does the same on arrival back inside.
//
// MUST be invoked on the world goroutine. Cascade subscribers call it
// inline via `cmd.Fn(w)` from inside their dispatch (subscribers
// already run on the world goroutine via emit). External callers go
// through `w.SendContext(ctx, StartNPCRoute(args))`.
func StartNPCRoute(actorID ActorID, label string, homeDest MoveDestination, candidates []RouteCandidate, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return StartNPCRouteResult{}, fmt.Errorf("actor %q not found", actorID)
			}

			grid, err := buildWalkGrid(w)
			if err != nil {
				return StartNPCRouteResult{}, fmt.Errorf("build walk grid: %w", err)
			}
			stops := buildRouteStops(w, grid, actor, candidates, now)

			// Whether or not we have any stops, an in-flight prior route
			// is superseded. The supersede signal is the route start
			// itself; the prior route's pending stops evaporate.
			replaced := false
			if w.ActiveRoutes != nil {
				if _, exists := w.ActiveRoutes[actorID]; exists {
					replaced = true
				}
			}

			if len(stops) == 0 {
				// Nothing reachable. Clear any prior route (the new
				// trigger supersedes it) but don't install an empty
				// route or dispatch a MoveActor.
				if replaced {
					clearActiveRoute(w, actorID)
				}
				return StartNPCRouteResult{
					NPCID:    actorID,
					Label:    label,
					Stops:    0,
					Replaced: replaced,
				}, nil
			}

			phase := RoutePhaseActive
			if labelIsBeat(label) {
				phase = RoutePhaseBeat
			}

			w.routeInstallSeq++
			route := &NPCRoute{
				NPCID:           actorID,
				Label:           label,
				Stops:           stops,
				StopIdx:         0,
				Visited:         make([]bool, len(stops)),
				Phase:           phase,
				HomeDestination: cloneMoveDestination(homeDest),
				Gen:             w.routeInstallSeq,
			}
			if w.ActiveRoutes == nil {
				w.ActiveRoutes = map[ActorID]*NPCRoute{}
			}
			w.ActiveRoutes[actorID] = route

			if phase == RoutePhaseBeat {
				// A beat dispatches NOTHING, including the first leg (LLM-548).
				// He is told what he owes and goes; the engine does not start him
				// off. Dispatching just the first walk would be the one place the
				// engine still moves him, and it is the worst place for it — the
				// round begins at his post, where a walk with LeaveHuddleFirst
				// would drag him out of whatever conversation he was having to
				// send him on his rounds.
				return StartNPCRouteResult{
					NPCID:    actorID,
					Label:    label,
					Stops:    len(stops),
					Replaced: replaced,
				}, nil
			}

			// Dispatch the first walk. Inline call to MoveActor's Fn so
			// the whole start-route sequence is a single atomic
			// world-goroutine transaction — no SendContext round-trip.
			// LeaveHuddleFirst: true so a route-starting NPC who
			// happens to be huddling somewhere cleanly leaves the
			// huddle (HuddleLeft fires as a side-effect). routeStopDestination
			// picks StructureEnter vs Position per the stop's kind (LLM-514).
			first := stops[0]
			moveCmd := MoveActor(actorID, routeStopDestination(first), true, now)
			if _, err := moveCmd.Fn(w); err != nil {
				// Movement rejected (no path to first stop). Clear the
				// route — better to report 0 stops than leave the
				// world with a dangling route that arrival can't
				// advance. Return the populated result so callers
				// observe Replaced=true on a supersede-then-fail (the
				// prior route IS gone; reporting Replaced=false would
				// be wrong).
				clearActiveRoute(w, actorID)
				return StartNPCRouteResult{
					NPCID:    actorID,
					Label:    label,
					Stops:    0,
					Replaced: replaced,
				}, fmt.Errorf("dispatch first walk: %w", err)
			}

			log.Printf("sim/npc_route: %s %q started route with %d stops (replaced=%v)",
				label, actorID, len(stops), replaced)

			return StartNPCRouteResult{
				NPCID:    actorID,
				Label:    label,
				Stops:    len(stops),
				Replaced: replaced,
			}, nil
		},
	}
}

// AdvanceNPCRouteResult is the typed reply from AdvanceNPCRoute. Reason
// describes the route state the call observed; tests + cascade logging
// use it to discriminate happy advance vs final-stop-handled vs
// returned-home vs no-route-found.
type AdvanceNPCRouteResult struct {
	NPCID  ActorID
	Reason string // "stop_advanced" | "returning_home" | "arrived_home" | "no_route" | "stale_stop" | "stale_retry" | "stale_abandoned" | "yielded_to_volition"
}

// AdvanceNPCRoute returns a Command that advances the named actor's
// route by one step in response to an ActorArrived event. The expected
// caller is the cascade ActorArrived subscriber — it dispatches one of
// these per arrival, and the Command no-ops for actors with no entry
// in World.ActiveRoutes.
//
// Behavior by phase:
//
//   - Active and the actor's tile matches the current stop's WalkTo:
//     flip the current stop's village_object state to NewState; advance
//     StopIdx; dispatch next MoveActor (next stop OR home if last).
//     Returns "stop_advanced".
//
//   - Active and the actor's tile DOES NOT match the current stop's
//     WalkTo: stale arrival (the actor was force-moved or arrived via
//     an out-of-band MoveActor). Skip the flip; the next legitimate
//     arrival will resync. Returns "stale_stop".
//
//   - Returning: clear the route. The locomotion ticker reconciled
//     InsideStructureID via updateInsideStructureIDFromTileOwnership
//     as the actor stepped onto the home structure's door tile (for
//     StructureEnter destinations) or position. Returns "arrived_home".
//
// The per-stop flip uses the plain unguarded SetVillageObjectState
// (not ApplyScheduledFlip). The route is not gen-tied: a phase or
// rotation transition that happens mid-walk doesn't kill the route,
// and the per-stop flip is meant to land regardless of how the flip
// generations have advanced since route start.
// SetVillageObjectState's "already_at_target" reason absorbs the
// converged case (object already at NewState — happens when a fresher
// bulk pass overwrote the same object).
func AdvanceNPCRoute(actorID ActorID) Command {
	return advanceNPCRoute(actorID, true)
}

// AdvanceNPCRouteSkipFlip is AdvanceNPCRoute without the active-phase
// per-stop village_object state flip. The town crier owns its board's
// state mutation itself — it sets the board variant to match the number
// of notices it actually authored (LLM-44) — so its route advance must
// NOT also flip the stop to the route candidate's pre-decided NewState.
// Every other behavior (stale-arrival re-walk + abandon, returning-home
// transition, route clearing) is identical to AdvanceNPCRoute.
func AdvanceNPCRouteSkipFlip(actorID ActorID) Command {
	return advanceNPCRoute(actorID, false)
}

// advanceNPCRoute is the shared body of AdvanceNPCRoute (flip=true) and
// AdvanceNPCRouteSkipFlip (flip=false). flip gates only the active-phase
// per-stop SetVillageObjectState; the walk machinery is identical.
func advanceNPCRoute(actorID ActorID, flip bool) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			route, ok := w.ActiveRoutes[actorID]
			if !ok || route == nil {
				return AdvanceNPCRouteResult{NPCID: actorID, Reason: "no_route"}, nil
			}

			switch route.Phase {
			case RoutePhaseActive:
				return advanceActiveRoute(w, route, flip)
			case RoutePhaseReturning:
				return advanceReturningRoute(w, route)
			case RoutePhaseBeat:
				return advanceBeatRoute(w, route)
			default:
				log.Printf("sim/npc_route: %q route in unknown phase %q — clearing",
					actorID, route.Phase)
				clearActiveRoute(w, actorID)
				return AdvanceNPCRouteResult{NPCID: actorID, Reason: "no_route"}, nil
			}
		},
	}
}

// advanceActiveRoute is AdvanceNPCRoute's active-phase body. Flips the
// current stop's state, advances StopIdx, dispatches next walk OR
// transitions to returning + dispatches home walk.
//
// Stale-arrival handling: the cascade subscriber dispatches us on every
// ActorArrived for an actor with an active route. If something other
// than the route's MoveActor brought the actor to this tile (admin
// force-move, an externally-issued MoveActor between supersede and
// arrival, a still-in-flight prior cascade emit), the actor's tile
// won't match the route's expected WalkTo. We don't flip a stop the
// actor isn't at; instead we re-walk to the current stop (bounded by
// maxStaleRouteRetries) so a single bump no longer strands the stop, and
// abandon the route once the budget is spent. See the stale branch below.
func advanceActiveRoute(w *World, route *NPCRoute, flip bool) (AdvanceNPCRouteResult, error) {
	if route.StopIdx >= len(route.Stops) {
		// Defensive — StopIdx should never exceed len(Stops) in active
		// phase. Clear and return.
		log.Printf("sim/npc_route: %q active route StopIdx=%d >= len(Stops)=%d — clearing",
			route.NPCID, route.StopIdx, len(route.Stops))
		clearActiveRoute(w, route.NPCID)
		return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "stale_stop"}, nil
	}

	stop := route.Stops[route.StopIdx]

	actor, ok := w.Actors[route.NPCID]
	if !ok {
		// Actor gone — clear the route.
		clearActiveRoute(w, route.NPCID)
		return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "stale_stop"}, nil
	}
	// Locomotion contract dependency: this guard assumes
	// actor.Pos / actor.InsideStructureID already reflect the arrival
	// state when ActorArrived's subscribers run. The locomotion ticker's
	// finishArrival commits actor.Pos in advanceActorLocomotion
	// (one tile per tick) and reconciles InsideStructureID BEFORE emitting
	// ActorArrived. Reversing that ordering would make valid arrivals look
	// stale. RouteStopArrived branches on stop kind: Pos == WalkTo for a
	// loiter/tile stop (byte-for-byte the prior check), InsideStructureID ==
	// EnterStructureID for an enter stop (LLM-514).
	// A decorative carrier never self-moves, so the only thing that can have put it
	// anywhere is the route's own walk: exact tile equality is the right test, and an
	// off-stop arrival is always an external bump to be undone. (A volition carrier
	// never reaches here — his arrivals go to advanceBeatRoute, which has no notion
	// of being in the wrong place.)
	atStop := RouteStopReached(w, route, actor, stop)
	if !atStop {
		// Stale arrival: this ActorArrived was for some other destination — an
		// external MoveActor (admin force-move, a competing producer's nudge)
		// superseded the route's walk between dispatch and arrival, so the actor
		// is not standing at the stop we expected. Don't flip a stop the actor
		// isn't at.
		//
		// The old behavior returned here with no pending move, parking the route
		// — one bump stranded the stop forever (the never-lit far lamp). Worse
		// now that an in-flight route suppresses the shift-duty producer: a parked
		// route would never clear, leaving the actor home-exempt indefinitely.
		// Instead re-walk to the current stop so the route self-heals, bounded so
		// a producer that keeps bumping the actor can't loop us. On exhaustion,
		// abandon the route (clearing frees the actor; the next phase boundary
		// re-triggers a fresh route over whatever is still un-flipped).
		if route.StaleRetries >= maxStaleRouteRetries {
			log.Printf("sim/npc_route: %q stale arrival at (%d,%d), expected stop %d at (%d,%d) — abandoning route after %d retries",
				route.NPCID, actor.Pos.X, actor.Pos.Y, route.StopIdx, stop.WalkTo.X, stop.WalkTo.Y, maxStaleRouteRetries)
			clearActiveRoute(w, route.NPCID)
			return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "stale_abandoned"}, nil
		}
		// Re-walk to the current stop. Setting a fresh MoveIntent here is safe
		// because this path runs from the ActorArrived emit, and finishArrival
		// clears MoveIntent BEFORE emitting ActorArrived — so nothing nils the
		// intent out from under us afterward. (A failed walk is the opposite: the
		// ticker clears MoveIntent AFTER emitting ActorMoveStopped, so that case
		// is handled by abandoning the route in the cascade rather than re-walking
		// here — see handleActorMoveStoppedAdvanceRoute.)
		route.StaleRetries++
		reWalk := MoveActor(route.NPCID, routeStopDestination(stop), false, time.Now())
		if _, err := reWalk.Fn(w); err != nil {
			log.Printf("sim/npc_route: %q stale arrival; re-walk dispatch to stop %d failed: %v — clearing route",
				route.NPCID, route.StopIdx, err)
			clearActiveRoute(w, route.NPCID)
			return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "stale_stop"}, nil
		}
		log.Printf("sim/npc_route: %q stale arrival at (%d,%d), expected stop %d at (%d,%d) — re-walking to stop (retry %d/%d)",
			route.NPCID, actor.Pos.X, actor.Pos.Y, route.StopIdx, stop.WalkTo.X, stop.WalkTo.Y, route.StaleRetries, maxStaleRouteRetries)
		return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "stale_retry"}, nil
	}

	// Per-stop flip. guardGen=0 disables the gen check — a fresher
	// rotation/transition that overwrote the same object since route
	// start would just bounce off SetVillageObjectState's
	// "already_at_target" path (no-op). Skipped for the town crier
	// (flip=false): she sets the board variant herself to match the
	// notices she authored (LLM-44), so a route flip here would clobber
	// that with the candidate's pre-decided NewState.
	if flip {
		flipCmd := SetVillageObjectState(stop.ObjectID, stop.NewState)
		if _, err := flipCmd.Fn(w); err != nil {
			log.Printf("sim/npc_route: %q stop %d (%q -> %q): flip failed: %v",
				route.NPCID, route.StopIdx, stop.ObjectID, stop.NewState, err)
			// Fall through — a flip failure shouldn't abort the route, the
			// next walk should still dispatch.
		}
	}

	// Clean visit — clear the per-stop stale budget so the next stop starts
	// fresh, and record that this place is walked.
	route.StaleRetries = 0
	route.markVisited(route.StopIdx)

	if nextIdx, ok := route.nextUnvisitedFrom(route.StopIdx); ok {
		// More places still owed a visit — dispatch next walk (StructureEnter or
		// Position per the stop's kind, LLM-514). The cursor moves to the next
		// UNVISITED stop rather than simply incrementing, so a shop he already called
		// at himself is not walked twice while another is never walked at all
		// (LLM-543).
		route.StopIdx = nextIdx
		next := route.Stops[nextIdx]
		moveCmd := MoveActor(route.NPCID, routeStopDestination(next), false, time.Now())
		if _, err := moveCmd.Fn(w); err != nil {
			log.Printf("sim/npc_route: %q dispatch next walk failed: %v — clearing route",
				route.NPCID, err)
			clearActiveRoute(w, route.NPCID)
			return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "stale_stop"}, nil
		}
		return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "stop_advanced"}, nil
	}

	// All stops done — transition to returning, dispatch home walk.
	route.Phase = RoutePhaseReturning
	moveCmd := MoveActor(route.NPCID, route.HomeDestination, false, time.Now())
	if _, err := moveCmd.Fn(w); err != nil {
		// Home walk rejected. Clear the route — the actor stays at the
		// last stop; next phase / rotation boundary re-triggers.
		log.Printf("sim/npc_route: %q dispatch home walk failed: %v — clearing route",
			route.NPCID, err)
		clearActiveRoute(w, route.NPCID)
		return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "stale_stop"}, nil
	}
	return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "returning_home"}, nil
}

// advanceBeatRoute is AdvanceNPCRoute's beat-phase body (LLM-548): the whole of
// what the engine does for a volition carrier's round. He walks himself; this
// records where he turned up, keeps the cursor pointed at something he still owes,
// and ends the round once the circuit is covered. It dispatches nothing and can
// fail at nothing — there is no walk to be refused and no wrong place to be in.
//
// Arriving at a circuit stop CREDITS it, whichever stop it is and however he came
// to be there. That is the whole model: a round is coverage, not a sequence, and he
// picks his own order. Live he called at stops 1, 5, 6, 3 and 4 in that order inside
// twenty minutes.
//
// Arriving anywhere else does nothing at all — no log, no state change, no nagging.
// He has gone to see to something of his own, which is his to do; the round simply
// waits, and the cue keeps naming what is left whenever he next reads it.
//
// The cursor is advisory here, unlike in advanceActiveRoute where it is a position
// in a walk. It names the place the cue will offer next, so it must always point at
// something still owed — hence moving it whenever the place it points at gets
// credited. Nothing walks him there, so pointing it at a place he has already been
// costs no wrong movement, only a wrong sentence in his prompt, which is worse: the
// cue offering a shop he called at ten minutes ago is what sent him back to it.
func advanceBeatRoute(w *World, route *NPCRoute) (AdvanceNPCRouteResult, error) {
	actor, ok := w.Actors[route.NPCID]
	if !ok {
		clearActiveRoute(w, route.NPCID)
		return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "no_route"}, nil
	}
	reachedIdx, atSomeStop := route.reachedStopIndex(w, actor)
	if !atSomeStop {
		return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "beat_elsewhere"}, nil
	}
	route.markVisited(reachedIdx)

	// Keep the cursor on something he still owes. nextUnvisitedFrom searches forward
	// and then WRAPS, which is what lets him call at places out of order without
	// stranding the ones he stepped over — a forward-only search would leave an
	// early stop behind the cursor forever and the round could never finish.
	if nextIdx, hasNext := route.nextUnvisitedFrom(reachedIdx); hasNext {
		route.StopIdx = nextIdx
		// Counted the same way the cue counts, so the log and the prompt never tell an
		// operator two different stories about how much of the round is left.
		log.Printf("sim/npc_route: %q called at stop %d of its beat (%d place(s) still owed, next is stop %d)",
			route.NPCID, reachedIdx, route.unvisitedCount(), nextIdx)
		return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "beat_stop_called"}, nil
	}

	// Everywhere called at. The round is DONE, and done means gone: clearing it is
	// what un-suppresses the shift-duty steer, and that is what tells him to head
	// back to his post. No home walk is dispatched — the same duty machinery that
	// governs every other on-shift NPC takes him there, and it did exactly that live
	// when this round's predecessor died two stops short.
	log.Printf("sim/npc_route: %q called at stop %d — beat complete, all %d place(s) walked",
		route.NPCID, reachedIdx, len(route.Stops))
	clearActiveRoute(w, route.NPCID)
	return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "beat_complete"}, nil
}

// advanceReturningRoute is AdvanceNPCRoute's returning-phase body. The
// locomotion ticker already reconciled InsideStructureID via
// updateInsideStructureIDFromTileOwnership as the actor stepped onto
// the home structure's door tile (for StructureEnter destinations) or
// the home position (for Position destinations); we just clear the
// route.
func advanceReturningRoute(w *World, route *NPCRoute) (AdvanceNPCRouteResult, error) {
	clearActiveRoute(w, route.NPCID)
	return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "arrived_home"}, nil
}

// RouteBoundaryDue reports whether the actor's schedule window has an
// unprocessed boundary at-or-before now — the route-schedule trigger's
// pure decision (ZBBS-HOME-446). The window comes from
// effectiveShiftWindow (the actor's schedule_start/end_minute pair, or
// the world's dawn/dusk day window when unscheduled), the boundary from
// mostRecentWindowBoundary (wrap-midnight safe), and the re-fire guard
// from World.RouteBoundaryStamps[attrSlug].
//
// isStart=true means the boundary is the window START (washerwoman
// hangs laundry out); false means window END (brings it in). The town
// crier ignores the direction — both boundaries trigger the same tour.
//
// A nil/missing stamp fires the most recent boundary immediately: that
// is the boot catch-up (stamps are in-memory and restart-lossy on
// purpose — see the World.RouteBoundaryStamps doc).
//
// MUST be called from inside a Command.Fn (reads world state).
func RouteBoundaryDue(w *World, a *Actor, attrSlug string, now time.Time) (boundary time.Time, isStart bool, due bool) {
	start, end, ok := effectiveShiftWindow(w, a)
	if !ok {
		return time.Time{}, false, false
	}
	boundary, isStart, ok = mostRecentWindowBoundary(w, start, end, now)
	if !ok {
		return time.Time{}, false, false
	}
	if last, has := w.RouteBoundaryStamps[attrSlug]; has && !last.Before(boundary) {
		return time.Time{}, false, false
	}
	return boundary, isStart, true
}

// StampRouteBoundary records boundary as processed for attrSlug, so
// RouteBoundaryDue stops re-firing it for the rest of the window.
// Callers stamp after a successful StartNPCRoute dispatch — including
// the zero-candidate no-op — but NOT after a dispatch error, so a
// transient failure retries on the next tick (same posture as the
// social scheduler's no-stamp-on-failed-walk).
//
// MUST be called from inside a Command.Fn (mutates world state).
func StampRouteBoundary(w *World, attrSlug string, boundary time.Time) {
	if w.RouteBoundaryStamps == nil {
		w.RouteBoundaryStamps = map[string]time.Time{}
	}
	w.RouteBoundaryStamps[attrSlug] = boundary
}

// buildRouteStops lays out an ordered nearest-neighbor walk over the
// candidates from (startX, startY). At each step:
//
//   - For every remaining candidate, resolve how the route visits it
//     (resolveRouteStop — enter the structure vs. stand at its loiter tile)
//     and the path length to that goal tile from the cursor.
//   - Pick the candidate with the shortest path; append its resolved
//     RouteStop; advance the cursor to that goal tile.
//   - Unreachable candidates are skipped (no path → that candidate
//     can't be visited this cycle).
//
// The stand tile prefers the object's loiter pin — the deliberate
// standing tile the editor's green marker designates (e.g. the open tile
// two south of a noticeboard) — over whatever walkable tile merely abuts
// the footprint. Routing to the pin keeps a route off the cramped
// one-lane tile jammed against an object's base, where a single passer-by
// (the PC) could wedge the actor indefinitely (ZBBS-HOME-458). See
// routeStopWalkTarget for the fallback.
//
// O(n²) FindPath calls in the worst case (n candidates, each iteration
// scans the remainder). Fine for the dozen-or-so stops a village-scale
// route carries; would need optimization at 100+ stops (e.g. a TSP-ish
// 2-opt over a coarse seed ordering, or a precomputed all-pairs
// shortest-path table).
//
// Stable ordering: when two candidates tie on path length, the earlier
// (lower index in the input) wins. Callers pre-sort candidates by
// ObjectID before calling if they want deterministic tie-breaking
// across runs.
//
// actor + now are threaded through to resolveRouteStop / routeStopEntersStructure
// so an ENTER stop is chosen only for a structure this actor could enter at `now`
// (the live entry gate, LLM-514 fix #8); they are inert for the tile-based routes,
// whose candidates never opt into entering.
//
// MUST be called from inside a Command.Fn — routeStopWalkTarget reads
// w.VillageObjects / w.Assets to resolve each candidate's loiter pin.
func buildRouteStops(w *World, grid *WalkGrid, actor *Actor, candidates []RouteCandidate, now time.Time) []RouteStop {
	if actor == nil || len(candidates) == 0 {
		return nil
	}
	remaining := make([]RouteCandidate, len(candidates))
	copy(remaining, candidates)

	cursor := GridPoint{X: actor.Pos.X, Y: actor.Pos.Y}
	stops := make([]RouteStop, 0, len(remaining))
	for len(remaining) > 0 {
		bestIdx := -1
		var bestStop RouteStop
		bestGoal := GridPoint{}
		bestLen := -1
		for i, c := range remaining {
			stop, goal, path := resolveRouteStop(w, grid, cursor, c, actor, now)
			if path == nil {
				continue
			}
			if bestLen < 0 || len(path) < bestLen {
				bestIdx = i
				bestStop = stop
				bestGoal = goal
				bestLen = len(path)
			}
		}
		if bestIdx < 0 {
			break // nothing else reachable
		}
		stops = append(stops, bestStop)
		cursor = bestGoal
		remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)
	}
	return stops
}

// resolveRouteStop builds the RouteStop for candidate c from cursor, applying
// the reusable enter-vs-loiter rule (LLM-514):
//
//   - c OPTS IN (c.Enter) AND is a door-backed structure (routeStopEntersStructure)
//     whose door tile is reachable → an ENTER stop: EnterStructureID set, goal =
//     the door tile (the interior tile a StructureEnter finishes on). WalkTo
//     carries the door tile too, for cursor/layout bookkeeping.
//   - otherwise (candidate didn't opt in, doorless/open placement, bare prop, or
//     an unreachable door) → a loiter/tile stop via routeStopWalkTarget: WalkTo =
//     the loiter pin or an adjacent walkable tile, exactly the prior behavior.
//
// The c.Enter gate makes entering OPT-IN: a candidate from the tile-based routes
// (lamplighter / washerwoman / town_crier) that happens to be structure-backed
// still yields a loiter stop, keeping those routes byte-for-byte unchanged.
//
// Returns the RouteStop, the goal tile (path costing + cursor advance), and the
// path from cursor (nil = unreachable this cycle — the caller skips it). MUST be
// called from inside a Command.Fn (reads world state via its resolvers).
func resolveRouteStop(w *World, grid *WalkGrid, cursor GridPoint, c RouteCandidate, actor *Actor, now time.Time) (RouteStop, GridPoint, []GridPoint) {
	// Gate on c.Enter FIRST (fix B): the enter classifier (routeStopEntersStructure →
	// moveToCanEnter) is evaluated ONLY for a candidate that opts in, so the
	// tile-based routes (lamplighter / washerwoman / town_crier) never touch the
	// entry gate at all — truly inert for them, not just filtered after the fact.
	if c.Enter {
		if sid, enters := routeStopEntersStructure(w, actor, c.ObjectID, now); enters {
			if door, ok := structureEntryTile(w, sid); ok {
				doorPt := GridPoint{X: door.X, Y: door.Y}
				// The door tile is the sole walkable footprint tile (buildWalkGrid
				// carves a corridor to it); a StructureEnter finishes there. Only take
				// the enter branch when it's actually reachable from the cursor —
				// otherwise fall through to the loiter fallback so the stop is still
				// visited (stand outside) rather than dropped.
				if grid.CanWalk(doorPt.X, doorPt.Y) {
					if path := FindPath(grid, cursor, doorPt); path != nil {
						return RouteStop{
							ObjectID:         c.ObjectID,
							WalkTo:           Position{X: doorPt.X, Y: doorPt.Y},
							NewState:         c.NewState,
							EnterStructureID: sid,
						}, doorPt, path
					}
				}
			}
		}
	}
	walkTo, path := routeStopWalkTarget(w, grid, cursor, c)
	return RouteStop{
		ObjectID: c.ObjectID,
		WalkTo:   Position{X: walkTo.X, Y: walkTo.Y},
		NewState: c.NewState,
	}, walkTo, path
}

// routeStopWalkTarget resolves the tile an actor stands on to visit a
// route candidate, and the path to it from cursor (a nil path means the
// candidate is unreachable this cycle — skip it). It prefers the object's
// loiter pin — the tile visitors are meant to occupy
// (effectiveObjectLoiterTile, the same pin the editor draws and
// structure/object visitors ring) — and falls back to any walkable tile
// adjacent to the object's footprint (FindPathToAdjacent, the prior
// behaviour) when the object has no resolvable pin, the pin centre is
// itself unwalkable (e.g. a well's centre), or the pin is unreachable
// from the cursor this cycle.
func routeStopWalkTarget(w *World, grid *WalkGrid, cursor GridPoint, c RouteCandidate) (GridPoint, []GridPoint) {
	if pin, ok := effectiveObjectLoiterTile(w, c.ObjectID); ok {
		pinPt := GridPoint{X: pin.X, Y: pin.Y}
		if grid.CanWalk(pinPt.X, pinPt.Y) {
			if path := FindPath(grid, cursor, pinPt); path != nil {
				return pinPt, path
			}
		}
	}
	objTile := WorldToTile(c.WorldX, c.WorldY)
	path, neighbor := FindPathToAdjacent(grid, cursor, objTile)
	return neighbor, path
}
