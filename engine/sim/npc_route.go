package sim

import (
	"fmt"
	"log"
	"time"
)

// npc_route.go — substrate for scheduled NPC routes. Lamplighter walks
// the lamps at dawn/dusk; washerwoman walks laundry tiles at the daily
// rotation boundary; town_crier walks notice boards at the same boundary.
//
// All three share the same skeleton: a list of RouteStop entries (each
// an object to visit with a pre-decided NewState), a phase ("active"
// while visiting stops, "returning" on the home leg), and a StopIdx
// cursor. Per-behavior logic (which candidates to visit, what target
// state to land in) lives in the cascade driver — it builds the
// candidate list and calls StartNPCRoute.
//
// The driver wires the lifecycle event-by-event:
//
//   - PhaseApplied (lamplighter) / RotationApplied (washerwoman /
//     town_crier) → start route via StartNPCRoute
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

	// AttrWasherwoman — actor walks the laundry-tagged rotatable
	// objects at the daily rotation boundary, rotating each per the
	// asset's RotationAlgo.
	AttrWasherwoman = "washerwoman"

	// AttrTownCrier — actor walks the notice-board-tagged rotatable
	// objects at the daily rotation boundary. Slice 2 walks silently;
	// Slice 3 will wire an LLM-authored saying broadcast on each stop.
	AttrTownCrier = "town_crier"
)

// Tag slugs the route dispatcher narrows candidates by.
const (
	TagLaundry     = "laundry"
	TagNoticeBoard = "notice-board"
)

// RoutePhase discriminates the two legs of a route. Active walks
// candidate stops in order; Returning is the home leg after the last
// stop. AdvanceNPCRoute's behavior depends on phase: an arrival in
// Active flips the current stop's state and dispatches the next walk;
// an arrival in Returning clears the route.
type RoutePhase string

const (
	RoutePhaseActive    RoutePhase = "active"
	RoutePhaseReturning RoutePhase = "returning"
)

// maxStaleRouteRetries bounds how many times a single stop is re-walked after a
// stale arrival (the actor finished a move to somewhere other than the stop's
// WalkTo — an external MoveActor superseded the route's walk) before the route
// gives up on the stop and abandons. Without a re-walk a single bump stranded
// the stop forever; without a bound, a producer that persistently bumps the
// actor would re-walk it indefinitely. Per-stop budget, reset on each clean
// visit (see advanceActiveRoute).
const maxStaleRouteRetries = 3

// RouteStop is one object the route visits with a pre-decided target
// state. WalkTo is the grid-tile destination the actor moves to —
// typically the adjacent walkable tile next to the object's anchor.
// NewState is the AssetState.State the object's CurrentState flips to
// on arrival.
type RouteStop struct {
	ObjectID VillageObjectID
	WalkTo   Position
	NewState string
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
type RouteCandidate struct {
	ObjectID VillageObjectID
	NewState string
	WorldX   float64
	WorldY   float64
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
			stops := buildRouteStops(grid, actor.Pos.X, actor.Pos.Y, candidates)

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
					delete(w.ActiveRoutes, actorID)
				}
				return StartNPCRouteResult{
					NPCID:    actorID,
					Label:    label,
					Stops:    0,
					Replaced: replaced,
				}, nil
			}

			route := &NPCRoute{
				NPCID:           actorID,
				Label:           label,
				Stops:           stops,
				StopIdx:         0,
				Phase:           RoutePhaseActive,
				HomeDestination: cloneMoveDestination(homeDest),
			}
			if w.ActiveRoutes == nil {
				w.ActiveRoutes = map[ActorID]*NPCRoute{}
			}
			w.ActiveRoutes[actorID] = route

			// Dispatch the first walk. Inline call to MoveActor's Fn so
			// the whole start-route sequence is a single atomic
			// world-goroutine transaction — no SendContext round-trip.
			// LeaveHuddleFirst: true so a route-starting NPC who
			// happens to be huddling somewhere cleanly leaves the
			// huddle (HuddleLeft fires as a side-effect).
			first := stops[0]
			moveCmd := MoveActor(actorID, NewPositionDestination(first.WalkTo), true, now)
			if _, err := moveCmd.Fn(w); err != nil {
				// Movement rejected (no path to first stop). Clear the
				// route — better to report 0 stops than leave the
				// world with a dangling route that arrival can't
				// advance. Return the populated result so callers
				// observe Replaced=true on a supersede-then-fail (the
				// prior route IS gone; reporting Replaced=false would
				// be wrong).
				delete(w.ActiveRoutes, actorID)
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
	Reason string // "stop_advanced" | "returning_home" | "arrived_home" | "no_route" | "stale_stop" | "stale_retry" | "stale_abandoned"
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
// The per-stop flip passes guardGen=0 (no gen check). The route is
// not gen-tied: a phase or rotation transition that happens mid-walk
// doesn't kill the route, and the per-stop flip is meant to land
// regardless of whether WorldEventGen has advanced since route start.
// SetVillageObjectState's "already_at_target" reason absorbs the
// converged case (object already at NewState — happens when a fresher
// bulk pass overwrote the same object).
func AdvanceNPCRoute(actorID ActorID) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			route, ok := w.ActiveRoutes[actorID]
			if !ok || route == nil {
				return AdvanceNPCRouteResult{NPCID: actorID, Reason: "no_route"}, nil
			}

			switch route.Phase {
			case RoutePhaseActive:
				return advanceActiveRoute(w, route)
			case RoutePhaseReturning:
				return advanceReturningRoute(w, route)
			default:
				log.Printf("sim/npc_route: %q route in unknown phase %q — clearing",
					actorID, route.Phase)
				delete(w.ActiveRoutes, actorID)
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
func advanceActiveRoute(w *World, route *NPCRoute) (AdvanceNPCRouteResult, error) {
	if route.StopIdx >= len(route.Stops) {
		// Defensive — StopIdx should never exceed len(Stops) in active
		// phase. Clear and return.
		log.Printf("sim/npc_route: %q active route StopIdx=%d >= len(Stops)=%d — clearing",
			route.NPCID, route.StopIdx, len(route.Stops))
		delete(w.ActiveRoutes, route.NPCID)
		return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "stale_stop"}, nil
	}

	stop := route.Stops[route.StopIdx]

	actor, ok := w.Actors[route.NPCID]
	if !ok {
		// Actor gone — clear the route.
		delete(w.ActiveRoutes, route.NPCID)
		return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "stale_stop"}, nil
	}
	// Locomotion contract dependency: this guard assumes
	// actor.Pos already reflects the arrival tile when
	// ActorArrived's subscribers run. The locomotion ticker's
	// finishArrival commits actor.Pos in advanceActorLocomotion
	// (one tile per tick) BEFORE emitting ActorArrived. Reversing
	// that ordering would make valid arrivals look stale.
	if actor.Pos.X != stop.WalkTo.X || actor.Pos.Y != stop.WalkTo.Y {
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
			delete(w.ActiveRoutes, route.NPCID)
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
		reWalk := MoveActor(route.NPCID, NewPositionDestination(stop.WalkTo), false, time.Now())
		if _, err := reWalk.Fn(w); err != nil {
			log.Printf("sim/npc_route: %q stale arrival; re-walk dispatch to stop %d failed: %v — clearing route",
				route.NPCID, route.StopIdx, err)
			delete(w.ActiveRoutes, route.NPCID)
			return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "stale_stop"}, nil
		}
		log.Printf("sim/npc_route: %q stale arrival at (%d,%d), expected stop %d at (%d,%d) — re-walking to stop (retry %d/%d)",
			route.NPCID, actor.Pos.X, actor.Pos.Y, route.StopIdx, stop.WalkTo.X, stop.WalkTo.Y, route.StaleRetries, maxStaleRouteRetries)
		return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "stale_retry"}, nil
	}

	// Per-stop flip. guardGen=0 disables the gen check — a fresher
	// rotation/transition that overwrote the same object since route
	// start would just bounce off SetVillageObjectState's
	// "already_at_target" path (no-op).
	flipCmd := SetVillageObjectState(stop.ObjectID, stop.NewState, 0)
	if _, err := flipCmd.Fn(w); err != nil {
		log.Printf("sim/npc_route: %q stop %d (%q -> %q): flip failed: %v",
			route.NPCID, route.StopIdx, stop.ObjectID, stop.NewState, err)
		// Fall through — a flip failure shouldn't abort the route, the
		// next walk should still dispatch.
	}

	// Clean visit — clear the per-stop stale budget so the next stop starts
	// fresh.
	route.StaleRetries = 0
	route.StopIdx++

	if route.StopIdx < len(route.Stops) {
		// More stops — dispatch next walk.
		next := route.Stops[route.StopIdx]
		moveCmd := MoveActor(route.NPCID, NewPositionDestination(next.WalkTo), false, time.Now())
		if _, err := moveCmd.Fn(w); err != nil {
			log.Printf("sim/npc_route: %q dispatch next walk failed: %v — clearing route",
				route.NPCID, err)
			delete(w.ActiveRoutes, route.NPCID)
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
		delete(w.ActiveRoutes, route.NPCID)
		return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "stale_stop"}, nil
	}
	return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "returning_home"}, nil
}

// advanceReturningRoute is AdvanceNPCRoute's returning-phase body. The
// locomotion ticker already reconciled InsideStructureID via
// updateInsideStructureIDFromTileOwnership as the actor stepped onto
// the home structure's door tile (for StructureEnter destinations) or
// the home position (for Position destinations); we just clear the
// route.
func advanceReturningRoute(w *World, route *NPCRoute) (AdvanceNPCRouteResult, error) {
	delete(w.ActiveRoutes, route.NPCID)
	return AdvanceNPCRouteResult{NPCID: route.NPCID, Reason: "arrived_home"}, nil
}

// buildRouteStops lays out an ordered nearest-neighbor walk over the
// candidates from (startX, startY). At each step:
//
//   - Try every remaining candidate, using FindPathToAdjacent to find
//     an adjacent walkable tile and the path length from the cursor.
//   - Pick the candidate with the shortest path; record the chosen
//     neighbor tile as the RouteStop's WalkTo; advance the cursor.
//   - Unreachable candidates are skipped (no path → that candidate
//     can't be visited this cycle).
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
func buildRouteStops(grid *WalkGrid, startX, startY int, candidates []RouteCandidate) []RouteStop {
	if len(candidates) == 0 {
		return nil
	}
	remaining := make([]RouteCandidate, len(candidates))
	copy(remaining, candidates)

	cursor := GridPoint{X: startX, Y: startY}
	stops := make([]RouteStop, 0, len(remaining))
	for len(remaining) > 0 {
		bestIdx := -1
		bestNeighbor := GridPoint{}
		bestLen := -1
		for i, c := range remaining {
			objTile := WorldToTile(c.WorldX, c.WorldY)
			path, neighbor := FindPathToAdjacent(grid, cursor, objTile)
			if path == nil {
				continue
			}
			if bestLen < 0 || len(path) < bestLen {
				bestIdx = i
				bestNeighbor = neighbor
				bestLen = len(path)
			}
		}
		if bestIdx < 0 {
			break // nothing else reachable
		}
		chosen := remaining[bestIdx]
		stops = append(stops, RouteStop{
			ObjectID: chosen.ObjectID,
			WalkTo:   Position{X: bestNeighbor.X, Y: bestNeighbor.Y},
			NewState: chosen.NewState,
		})
		cursor = bestNeighbor
		remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)
	}
	return stops
}
