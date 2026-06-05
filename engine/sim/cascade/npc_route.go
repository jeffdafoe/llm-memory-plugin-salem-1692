package cascade

import (
	"context"
	"log"
	"sort"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// npc_route.go — Phase 3 Group A scheduled-route cascade driver.
//
// Three event subscribers wire the lamplighter / washerwoman /
// town_crier behaviors onto their respective triggers; one shared
// subscriber on ActorArrived advances any active route by one stop, and
// one on ActorMoveStopped abandons a route whose walk failed.
//
//   - PhaseApplied     → start lamplighter route over carved-out lamps
//   - RotationApplied   → start washerwoman route over laundry tiles
//   - RotationApplied   → start town_crier route over notice boards
//   - ActorArrived     → advance the arrived actor's route (no-op if none)
//   - ActorMoveStopped → abandon the actor's route if its walk failed
//                        (no-op if none) — otherwise a stopped walk leaves
//                        the route parked forever, which now also strands
//                        the actor's shift-duty (see handler doc)
//
// Per-behavior eligibility comes from Actor.Attributes membership
// (AttrLamplighter / AttrWasherwoman / AttrTownCrier). At-most-one
// carrier per attribute per world is the v1 norm; multiple carriers
// are tolerated but only the first (sorted by ActorID) runs the
// triggered cycle.
//
// Carve-out coupling. For lamplighter the substrate already wires
// excludeTag=TagLamplighterTarget on ApplyPhaseTransition — every
// PhaseApplied event implies the carve-out happened, and the
// lamplighter subscriber unconditionally tries to dispatch the route.
// For washerwoman / town_crier the carve-out is dynamic
// (RotationApplied.ExcludedTags) — the cutover layer chooses what to
// carve out by wiring RunRotationTicker with the right scope. The
// subscribers check whether THEIR tag is in ExcludedTags; only then do
// they build a route, because outside the carve-out the bulk rotation
// already flipped the candidates and a route would be redundant /
// would re-flip newly-rotated state.
//
// Town_crier walks silently in Slice 2. The on-stop "say something"
// hook lives in npc_route.go's AdvanceNPCRoute and is currently empty
// — Slice 3 wires the LLM-authored saying broadcast there.

// RegisterNPCRoutes wires the subscribers needed for the Slice 2
// scheduled-route slice. Must run on the world goroutine — call before
// World.Run, or from inside a Command.Fn.
//
// Idempotency: registering twice would dispatch the route twice per
// triggering event. The substrate's StartNPCRoute supersedes any prior
// route on the same actor, so the duplicate would just re-walk; still,
// double-registration is a wiring bug. Wiring guards live at the
// registration site — don't register twice.
//
// Panics on nil w (wiring guard, mirrors RegisterVisitor /
// RegisterBusinessowner).
//
// The ctx parameter is kept for signature symmetry with other cascade
// Register* helpers; today it's unused (this slice is purely
// event-driven with no goroutine).
func RegisterNPCRoutes(_ context.Context, w *sim.World) {
	if w == nil {
		panic("cascade: RegisterNPCRoutes requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handlePhaseAppliedLamplighter))
	w.Subscribe(sim.SubscriberFunc(handleRotationAppliedWasherwoman))
	w.Subscribe(sim.SubscriberFunc(handleRotationAppliedTownCrier))
	w.Subscribe(sim.SubscriberFunc(handleActorArrivedAdvanceRoute))
	w.Subscribe(sim.SubscriberFunc(handleActorMoveStoppedAdvanceRoute))
}

// handlePhaseAppliedLamplighter starts the lamplighter route on each
// PhaseApplied event. The route visits every village_object whose
// AssetState carries both TagLamplighterTarget and the new phase's
// active tag (day-active or night-active) and whose CurrentState
// differs from that target.
func handlePhaseAppliedLamplighter(w *sim.World, evt sim.Event) {
	applied, ok := evt.(*sim.PhaseApplied)
	if !ok {
		return
	}
	var targetTag string
	switch applied.To {
	case sim.PhaseDay:
		targetTag = sim.TagDayActive
	case sim.PhaseNight:
		targetTag = sim.TagNightActive
	default:
		return
	}

	actor := findActorWithAttribute(w, sim.AttrLamplighter)
	if actor == nil {
		return
	}
	candidates := buildLamplighterCandidates(w, targetTag)
	if len(candidates) == 0 {
		return
	}
	cmd := sim.StartNPCRoute(actor.ID, sim.AttrLamplighter, homeDestinationFor(actor), candidates, applied.At)
	if _, err := cmd.Fn(w); err != nil {
		log.Printf("cascade/npc_route: lamplighter dispatch (actor %q event %d): %v",
			actor.ID, applied.EventID(), err)
	}
}

// handleRotationAppliedWasherwoman starts the washerwoman route on
// RotationApplied when TagLaundry is in the event's ExcludedTags slice
// (= the bulk pass carved out laundry for the washerwoman). When
// TagLaundry isn't excluded the bulk pass already rotated the laundry
// objects; a route would just re-flip the same state.
func handleRotationAppliedWasherwoman(w *sim.World, evt sim.Event) {
	applied, ok := evt.(*sim.RotationApplied)
	if !ok {
		return
	}
	if !excludedTagsContain(applied.ExcludedTags, sim.TagLaundry) {
		return
	}
	dispatchRotationRoute(w, applied, sim.AttrWasherwoman, sim.TagLaundry)
}

// handleRotationAppliedTownCrier is washerwoman's twin for the
// notice-board tag. Walks silently in Slice 2 — the on-stop LLM-
// authored saying hook lands in Slice 3 inside AdvanceNPCRoute.
func handleRotationAppliedTownCrier(w *sim.World, evt sim.Event) {
	applied, ok := evt.(*sim.RotationApplied)
	if !ok {
		return
	}
	if !excludedTagsContain(applied.ExcludedTags, sim.TagNoticeBoard) {
		return
	}
	dispatchRotationRoute(w, applied, sim.AttrTownCrier, sim.TagNoticeBoard)
}

// handleActorArrivedAdvanceRoute is the cascade-wide arrival hook for
// NPC routes. Most arrivals match no entry in World.ActiveRoutes and
// the AdvanceNPCRoute command no-ops cheaply.
//
// Town crier branch: BEFORE dispatching AdvanceNPCRoute (which flips
// the noticeboard state), read this-cycle's NoticeboardContent for
// the current stop's object and emit a Spoke via
// EmitTownCrierAnnouncement. The crier "reads what's currently
// posted" — which was authored by the previous rotation cycle's
// flip-triggered authoring. After the read, AdvanceNPCRoute flips
// the state; that flip emits VillageObjectStateChanged which the
// noticeboard cascade subscribes to, spawning fresh authoring for
// the NEW state (consumed next time the board is read).
//
// Cold-start: a freshly-loaded world has no NoticeboardContent
// stamped. The first crier cycle reads nothing (silent stops); the
// flip-triggered authoring lands content for the next cycle. From
// the second cycle on, crier reads + boards rotate normally.
func handleActorArrivedAdvanceRoute(w *sim.World, evt sim.Event) {
	arrived, ok := evt.(*sim.ActorArrived)
	if !ok {
		return
	}
	if w.ActiveRoutes == nil {
		return
	}
	route, has := w.ActiveRoutes[arrived.ActorID]
	if !has || route == nil {
		return
	}

	// Town crier branch: emit existing board content before the
	// flip. Two guards:
	//
	//  - Active-phase stale-arrival (actor must be at expected
	//    WalkTo; mirrors AdvanceNPCRoute's check).
	//
	//  - AtState parity (content.AtState must match the board's
	//    current CurrentState). The save path's stale-guard
	//    rejects writes whose captured state no longer matches,
	//    but content can still sit on the board across an
	//    out-of-band state change (admin direct flip, future
	//    code mutating CurrentState without clearing content).
	//    The crier must not voice content authored for a
	//    different state.
	if route.Label == sim.AttrTownCrier &&
		route.Phase == sim.RoutePhaseActive &&
		route.StopIdx < len(route.Stops) {
		stop := route.Stops[route.StopIdx]
		actor, ok := w.Actors[arrived.ActorID]
		atExpected := ok && actor.Pos.X == stop.WalkTo.X && actor.Pos.Y == stop.WalkTo.Y
		if atExpected && w.NoticeboardContent != nil {
			content, present := w.NoticeboardContent[stop.ObjectID]
			obj, hasObj := w.VillageObjects[stop.ObjectID]
			if present && content != nil && content.Text != "" && hasObj && obj != nil && content.AtState == obj.CurrentState {
				emitCmd := sim.EmitTownCrierAnnouncement(arrived.ActorID, content.Text, arrived.At)
				if _, err := emitCmd.Fn(w); err != nil {
					log.Printf("cascade/npc_route: town_crier announce (actor %q event %d): %v",
						arrived.ActorID, arrived.EventID(), err)
				}
			}
		}
	}

	cmd := sim.AdvanceNPCRoute(arrived.ActorID)
	if _, err := cmd.Fn(w); err != nil {
		log.Printf("cascade/npc_route: advance (actor %q event %d): %v",
			arrived.ActorID, arrived.EventID(), err)
	}
}

// handleActorMoveStoppedAdvanceRoute abandons a route when the routed actor's
// current move fails to complete. The locomotion ticker emits ActorMoveStopped
// (blocked / unreachable / deadlocked / invalidated / cancelled) instead of
// ActorArrived for an accepted move that can't reach its destination, then
// clears MoveIntent. The route's only advance hook is ActorArrived, so without
// this the route sits in ActiveRoutes forever — and since an in-flight route now
// suppresses the shift-duty producer (shiftDutyTarget), that strands the actor's
// shift-duty too. Clearing frees the actor; the next phase / rotation boundary
// rebuilds a fresh route from real object state.
//
// We abandon on ANY ActorMoveStopped for an actor that holds an active route,
// not only the route's own walk. That breadth is deliberate and is the safe
// choice for the "a routed actor's route must always eventually clear" invariant:
//
//   - If the stopped move IS the route's walk, abandoning is plainly right.
//   - If a competing move superseded the route's walk (supersede is SILENT — the
//     dead route walk emits nothing) and that competing move then stops, the
//     actor has no pending move and the route would never receive another
//     ActorArrived / ActorMoveStopped — permanently stuck. The competing move's
//     stop is the only signal that reaches us, so we must act on it. (A competing
//     move that SUCCEEDS instead emits ActorArrived, and the stale-arrival path
//     re-walks the route — recovery, not abandon.)
//
// Correlating on MovementAttemptID to ignore non-route stops would REINTRODUCE
// the stuck-route gap for exactly that supersede-then-fail case, so we don't.
// Note there is only ever one MoveIntent per actor, so a non-route stop can't
// coexist with a still-pending route walk — abandoning never discards a route
// that could otherwise have kept progressing.
//
// MAINTAINER NOTE: this broad abandon is load-bearing on the engine-wide
// single-MoveIntent invariant. Do NOT narrow it to MovementAttemptID correlation
// unless route dispatch also gains an independent expiry/watchdog — otherwise the
// supersede-then-fail case strands the route (and, via shiftDutyTarget, the
// actor's shift-duty) again.
//
// We abandon rather than re-walk because emitMoveStopped clears MoveIntent AFTER
// the emit (finishArrival clears it BEFORE emitting ActorArrived), so a MoveActor
// dispatched from this synchronously-run subscriber would be nil'd by the ticker
// the instant we return. A map delete touches only ActiveRoutes, which the ticker
// never reads — safe.
func handleActorMoveStoppedAdvanceRoute(w *sim.World, evt sim.Event) {
	stopped, ok := evt.(*sim.ActorMoveStopped)
	if !ok {
		return
	}
	if w.ActiveRoutes == nil {
		return
	}
	if route, has := w.ActiveRoutes[stopped.ActorID]; !has || route == nil {
		return
	}
	delete(w.ActiveRoutes, stopped.ActorID)
	log.Printf("cascade/npc_route: %q route walk stopped (%s) — abandoning route",
		stopped.ActorID, stopped.Reason)
}

// dispatchRotationRoute is the shared body for washerwoman / town_crier
// route dispatch. domainTag narrows candidates to AssetStates carrying
// that tag (TagLaundry or TagNoticeBoard); label is the route label
// (= the attribute slug).
func dispatchRotationRoute(w *sim.World, applied *sim.RotationApplied, attrSlug, domainTag string) {
	actor := findActorWithAttribute(w, attrSlug)
	if actor == nil {
		return
	}
	candidates := buildRotationCandidates(w, domainTag)
	if len(candidates) == 0 {
		return
	}
	cmd := sim.StartNPCRoute(actor.ID, attrSlug, homeDestinationFor(actor), candidates, applied.At)
	if _, err := cmd.Fn(w); err != nil {
		log.Printf("cascade/npc_route: %s dispatch (actor %q event %d): %v",
			attrSlug, actor.ID, applied.EventID(), err)
	}
}

// findActorWithAttribute returns the deterministic-first actor carrying
// the given attribute slug (sorted by ActorID), or nil. The sort is
// load-bearing — w.Actors map iteration is non-deterministic, and we
// don't want the cycle to pick a different lamplighter run-to-run when
// multiple carriers exist (a misconfiguration we tolerate but don't
// reward with variable behavior).
func findActorWithAttribute(w *sim.World, slug string) *sim.Actor {
	var matches []sim.ActorID
	for id, a := range w.Actors {
		if a == nil {
			continue
		}
		if _, ok := a.Attributes[slug]; ok {
			matches = append(matches, id)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i] < matches[j]
	})
	return w.Actors[matches[0]]
}

// buildLamplighterCandidates collects the village_objects this route
// will visit. Predicate per object:
//
//   - Its asset has a state carrying TagLamplighterTarget AND targetTag
//     (day-active or night-active depending on the new phase).
//   - The object's CurrentState differs from that target state.
//
// Multiple asset states may carry both tags; we pick the one with the
// lowest AssetStateID (matches Asset.StateForTag's tie-break) as the
// canonical target.
func buildLamplighterCandidates(w *sim.World, targetTag string) []sim.RouteCandidate {
	var out []sim.RouteCandidate
	for id, obj := range w.VillageObjects {
		if obj == nil {
			continue
		}
		asset, ok := w.Assets[obj.AssetID]
		if !ok {
			continue
		}
		target := assetStateForBothTags(asset, sim.TagLamplighterTarget, targetTag)
		if target == nil {
			continue
		}
		if obj.CurrentState == target.State {
			continue
		}
		out = append(out, sim.RouteCandidate{
			ObjectID: id,
			NewState: target.State,
			WorldX:   obj.Pos.X, WorldY: obj.Pos.Y,
		})
	}
	return sortCandidatesByID(out)
}

// buildRotationCandidates collects the village_objects in domainTag
// that need rotation. Predicate per object:
//
//   - Its asset has RotationAlgo == RotationAlgoDeterministic.
//   - Its CurrentState carries TagRotatable AND domainTag.
//   - The asset's rotatable pool has at least one non-current state to
//     flip to.
//
// Non-deterministic algos (random_per_object / random_per_asset) are
// skipped: the route's per-stop pick happens outside the bulk
// rotation pass, so we'd need to reproduce the bulk's rand source to
// stay consistent with what the bulk would have done — and the route
// has no shared rand. Domain assets in production today (laundry,
// noticeboards) are deterministic; if a random algo is ever wanted on
// a route-domain asset, factor the bulk picker into a shared helper +
// thread rand through this path. Today: just skip and log.
//
// The deterministic pick uses nextPoolState — same semantic as the
// substrate's pickDeterministicNext (next pool entry wrapping).
func buildRotationCandidates(w *sim.World, domainTag string) []sim.RouteCandidate {
	var out []sim.RouteCandidate
	for id, obj := range w.VillageObjects {
		if obj == nil {
			continue
		}
		asset, ok := w.Assets[obj.AssetID]
		if !ok {
			continue
		}
		if asset.RotationAlgo != sim.RotationAlgoDeterministic {
			if asset.RotationAlgo != "" {
				log.Printf("cascade/npc_route: skipping route candidate %q — asset %q uses non-deterministic RotationAlgo %q",
					id, obj.AssetID, asset.RotationAlgo)
			}
			continue
		}
		current := asset.FindState(obj.CurrentState)
		if current == nil || !current.HasTag(sim.TagRotatable) {
			continue
		}
		if !current.HasTag(domainTag) {
			continue
		}
		pool := asset.RotatablePool()
		if len(pool) == 0 {
			continue
		}
		next := nextPoolState(pool, obj.CurrentState)
		if next == "" || next == obj.CurrentState {
			continue
		}
		out = append(out, sim.RouteCandidate{
			ObjectID: id,
			NewState: next,
			WorldX:   obj.Pos.X, WorldY: obj.Pos.Y,
		})
	}
	return sortCandidatesByID(out)
}

// nextPoolState returns the pool entry after current, wrapping past the
// last. If current isn't in the pool (shouldn't happen — caller already
// filtered to rotatable), returns the first pool entry.
func nextPoolState(pool []*sim.AssetState, current string) string {
	if len(pool) == 0 {
		return ""
	}
	for i, s := range pool {
		if s.State == current {
			return pool[(i+1)%len(pool)].State
		}
	}
	return pool[0].State
}

// assetStateForBothTags returns the first AssetState (lowest ID) on
// asset carrying both tagA and tagB, or nil. Mirrors v1's lamplighter
// SQL: "states tagged day-active|night-active AND lamplighter-target".
func assetStateForBothTags(asset *sim.Asset, tagA, tagB string) *sim.AssetState {
	var best *sim.AssetState
	for i := range asset.States {
		s := &asset.States[i]
		if !s.HasTag(tagA) || !s.HasTag(tagB) {
			continue
		}
		if best == nil || s.ID < best.ID {
			best = s
		}
	}
	return best
}

// excludedTagsContain reports whether tag is in tags. Linear scan;
// ExcludedTags is at most a single-digit slice in production.
func excludedTagsContain(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// sortCandidatesByID sorts in-place by ObjectID. Stable iteration of
// w.VillageObjects isn't guaranteed by Go; without this, buildRouteStops'
// "earlier wins on tie" stable-ordering rule would still produce
// run-to-run different routes for identical worlds. The sort makes the
// candidate list itself deterministic. Returns the same slice for
// chained-call style.
func sortCandidatesByID(cs []sim.RouteCandidate) []sim.RouteCandidate {
	sort.Slice(cs, func(i, j int) bool {
		return cs[i].ObjectID < cs[j].ObjectID
	})
	return cs
}

// homeDestinationFor returns the MoveDestination the route's return
// leg targets. Actors with a HomeStructureID get a
// MoveDestinationStructureEnter — MoveActor resolves the door tile
// and the locomotion ticker handles InsideStructureID re-entry on
// arrival. Actors without a home structure get a Position destination
// at their current tile (route is effectively a one-way: stand at the
// last reachable stop).
//
// HomeStructureID validity (structure exists, has a placement) is
// checked downstream by MoveActor; we don't pre-flight here. A bad
// HomeStructureID means StartNPCRoute's first walk dispatches fine
// but the home leg later fails — at which point AdvanceNPCRoute
// clears the route gracefully.
func homeDestinationFor(actor *sim.Actor) sim.MoveDestination {
	if actor.HomeStructureID == "" {
		return sim.NewPositionDestination(actor.Pos)
	}
	return sim.NewStructureEnterDestination(actor.HomeStructureID)
}
