package cascade

import (
	"context"
	"log"
	"math/rand"
	"sort"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// npc_route.go — Phase 3 Group A scheduled-route cascade driver.
//
// The lamplighter is event-triggered; the washerwoman and town_crier
// are schedule-triggered (ZBBS-HOME-446 — previously RotationApplied
// at the daily rotation boundary). One shared subscriber on
// ActorArrived advances any active route by one stop, and one on
// ActorMoveStopped abandons a route whose walk failed.
//
//   - PhaseApplied            → start lamplighter route over carved-out lamps
//   - schedule-window boundary → start washerwoman route (hang laundry out
//     at window start, bring it in at window end) and town_crier route
//     (read + flip the notice boards, same tour at both boundaries) —
//     observed by RunRouteScheduleTicker once a minute, edge-triggered
//     via sim.RouteBoundaryDue / sim.StampRouteBoundary
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
// Window source: the carrier's own schedule_start/end_minute pair via
// the shift machinery's effectiveShiftWindow — an unscheduled carrier
// falls back to the world's dawn/dusk day window (Hope James runs on
// the fallback: laundry out at dawn, in at dusk; Grace Edwards carries
// an explicit 9:00–18:00 window).
//
// Carve-out coupling. For lamplighter the substrate already wires
// excludeTag=TagLamplighterTarget on ApplyPhaseTransition. Laundry and
// notice boards stay in RunRotationTicker's ExcludeTags (cmd/engine
// wiring) so the bulk midnight rotation never touches them — the
// schedule-triggered routes are those domains' sole mutators. Unlike
// the pre-446 shape, the exclusion no longer doubles as the route
// trigger; it only keeps the bulk pass out of the way.

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
	w.Subscribe(sim.SubscriberFunc(handleActorArrivedAdvanceRoute))
	w.Subscribe(sim.SubscriberFunc(handleActorMoveStoppedAdvanceRoute))
}

// RouteScheduleTickerInterval — once a minute, matching the sim
// package's RunShiftTicker / RunSocialTicker cadence. A boundary fires
// at worst ~60s late.
const RouteScheduleTickerInterval = time.Minute

// RunRouteScheduleTicker owns the schedule-route goroutine: once a
// minute, submit a RouteScheduleTick. Started by cmd/engine alongside
// the core sim tickers — deliberately NOT inside RegisterNPCRoutes, so
// the Register* helpers stay pure subscriber wiring and tests that
// install synthetic routes aren't raced by the ticker's catch-up
// dispatch.
//
// Immediate first check at goroutine entry: a freshly-booted world has
// no boundary stamps, so the most recent window boundary fires right
// away — the boot catch-up that re-hangs (or brings in) laundry and
// re-tours the boards for the current time of day instead of waiting
// for the next boundary. Pairs with KickstartNoticeboards: the
// kickstart authors content for the boards' current states, and the
// crier's catch-up tour then reads it aloud.
//
// The *rand.Rand is seeded once at goroutine entry and threaded into
// every pass (same idiom as RunRotationTicker) — it feeds the
// washerwoman's per-object variant pick. It is only ever read while
// this goroutine is parked inside SendContext, so there's no
// concurrent access.
func RunRouteScheduleTicker(ctx context.Context, w *sim.World) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	if _, err := w.SendContext(ctx, RouteScheduleTick(time.Now().UTC(), rng)); err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Printf("cascade/npc_route: schedule tick failed: %v", err)
	}
	t := time.NewTicker(RouteScheduleTickerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := w.SendContext(ctx, RouteScheduleTick(time.Now().UTC(), rng)); err != nil {
				if ctx.Err() == nil {
					log.Printf("cascade/npc_route: schedule tick failed: %v", err)
				}
			}
		}
	}
}

// RouteScheduleTick returns a Command that runs one boundary check for
// each schedule-triggered route behavior. Exported so tests drive it
// deterministically without the ticker goroutine. A nil rng gets a
// now-seeded fallback rather than panicking inside the washerwoman's
// variant pick.
func RouteScheduleTick(now time.Time, rng *rand.Rand) sim.Command {
	if rng == nil {
		rng = rand.New(rand.NewSource(now.UnixNano()))
	}
	return sim.Command{
		Fn: func(w *sim.World) (any, error) {
			runScheduledRoute(w, sim.AttrWasherwoman, now, func(w *sim.World, isStart bool) []sim.RouteCandidate {
				return buildLaundryCandidates(w, isStart, rng)
			})
			runScheduledRoute(w, sim.AttrTownCrier, now, func(w *sim.World, _ bool) []sim.RouteCandidate {
				return buildNoticeboardCandidates(w)
			})
			return nil, nil
		},
	}
}

// runScheduledRoute is the shared per-behavior body of a schedule
// tick: find the attribute carrier, ask the substrate whether a window
// boundary is due, build the (direction-aware) candidate list, start
// the route, and stamp the boundary.
//
// Stamping discipline: a successful StartNPCRoute — including the
// zero-candidate no-op (nothing to hang out / bring in) — stamps the
// boundary so it can't re-fire for the rest of the window. A dispatch
// ERROR leaves the stamp unset so the same boundary retries next tick
// (mirrors the social scheduler's no-stamp-on-failed-walk). A missing
// carrier skips without stamping — the per-tick re-check is two cheap
// map scans.
//
// An in-flight route on the carrier is superseded by the new dispatch —
// that's StartNPCRoute's documented contract (same shape as MoveActor's
// supersede), and the pre-446 rotation triggers had the same property.
// In practice it can only bite an actor carrying two route attributes
// (a tolerated misconfiguration); ActiveRoutes is empty at boot, so the
// catch-up dispatch never clobbers a real in-progress route.
func runScheduledRoute(w *sim.World, attrSlug string, now time.Time, build func(*sim.World, bool) []sim.RouteCandidate) {
	actor := findActorWithAttribute(w, attrSlug)
	if actor == nil {
		return
	}
	boundary, isStart, due := sim.RouteBoundaryDue(w, actor, attrSlug, now)
	if !due {
		return
	}
	candidates := build(w, isStart)
	cmd := sim.StartNPCRoute(actor.ID, attrSlug, homeDestinationFor(actor), candidates, now)
	res, err := cmd.Fn(w)
	if err != nil {
		log.Printf("cascade/npc_route: %s boundary dispatch (actor %q): %v",
			attrSlug, actor.ID, err)
		return
	}
	// Log EVERY boundary fire, including the silent-before-this no-op
	// shapes (0 candidates = nothing to do; candidates>0 with 0 stops =
	// nothing REACHABLE — the distinction matters when diagnosing a
	// route NPC that never walks). StartNPCRoute itself only logs when
	// stops > 0.
	started, ok := res.(sim.StartNPCRouteResult)
	if !ok {
		// Don't collapse a broken result contract into "0 reachable
		// stops" — that's exactly the shape this log exists to diagnose.
		log.Printf("cascade/npc_route: %s boundary fired (start=%v, boundary=%s) — %d candidate(s), unexpected StartNPCRoute result %T",
			attrSlug, isStart, boundary.Format(time.RFC3339), len(candidates), res)
		sim.StampRouteBoundary(w, attrSlug, boundary)
		return
	}
	log.Printf("cascade/npc_route: %s boundary fired (start=%v, boundary=%s) — %d candidate(s), %d stop(s), replaced=%v",
		attrSlug, isStart, boundary.Format(time.RFC3339), len(candidates), started.Stops, started.Replaced)
	sim.StampRouteBoundary(w, attrSlug, boundary)
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

// handleActorArrivedAdvanceRoute is the cascade-wide arrival hook for
// NPC routes. Most arrivals match no entry in World.ActiveRoutes and
// the AdvanceNPCRoute command no-ops cheaply.
//
// Town crier branch: BEFORE dispatching AdvanceNPCRoute (which flips
// the noticeboard state), read the board's NoticeboardContent for
// the current stop's object and emit a Spoke via
// EmitTownCrierAnnouncement. The crier "reads what's currently
// posted" — which was authored by the previous visit's flip-triggered
// authoring (or the boot kickstart). After the read, AdvanceNPCRoute
// flips the state; that flip emits VillageObjectStateChanged which the
// noticeboard cascade subscribes to, spawning fresh authoring for
// the NEW state (consumed next time the board is read).
//
// Cold-start: a freshly-loaded world has no NoticeboardContent
// stamped — KickstartNoticeboards authors it shortly after boot. A
// crier arrival that beats the kickstart's LLM call reads nothing
// (silent stop); the flip-triggered authoring lands content for the
// next visit either way.
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

// buildLaundryCandidates collects the laundry objects the washerwoman
// visits, direction-aware (ZBBS-HOME-446):
//
//   - hangOut=true (window start): objects sitting at their asset's
//     DefaultState (the bare line — "empty" on every production laundry
//     asset) flip to a randomly-picked non-default state from the
//     rotatable pool. Random per object for visual variety, the same
//     spirit as the assets' random_per_object bulk algo.
//   - hangOut=false (window end): objects NOT at DefaultState flip back
//     to it.
//
// Directionality is keyed on Asset.DefaultState rather than a
// hardcoded state name or the RotationAlgo — both legs are idempotent
// (an already-hung line is skipped on hang-out; an already-bare line
// on bring-in), which is what makes the boot catch-up safe to re-fire.
//
// Note the pre-446 builder gated on RotationAlgo == deterministic —
// which silently excluded EVERY production laundry and notice-board
// asset (all random_per_object), so the routes never built a stop.
// The directional/cycling builders don't consult the algo at all.
func buildLaundryCandidates(w *sim.World, hangOut bool, rng *rand.Rand) []sim.RouteCandidate {
	var out []sim.RouteCandidate
	for id, obj := range w.VillageObjects {
		if obj == nil {
			continue
		}
		asset, ok := w.Assets[obj.AssetID]
		if !ok {
			continue
		}
		current := asset.FindState(obj.CurrentState)
		if current == nil || !current.HasTag(sim.TagLaundry) {
			continue
		}
		if hangOut {
			if obj.CurrentState != asset.DefaultState {
				continue // already hung out
			}
			var variants []string
			for _, s := range asset.RotatablePool() {
				if s.State != asset.DefaultState {
					variants = append(variants, s.State)
				}
			}
			if len(variants) == 0 {
				continue
			}
			out = append(out, sim.RouteCandidate{
				ObjectID: id,
				NewState: variants[rng.Intn(len(variants))],
				WorldX:   obj.Pos.X, WorldY: obj.Pos.Y,
			})
			continue
		}
		if obj.CurrentState == asset.DefaultState {
			continue // already brought in
		}
		if asset.FindState(asset.DefaultState) == nil {
			continue // misconfigured asset — no default to return to
		}
		out = append(out, sim.RouteCandidate{
			ObjectID: id,
			NewState: asset.DefaultState,
			WorldX:   obj.Pos.X, WorldY: obj.Pos.Y,
		})
	}
	return sortCandidatesByID(out)
}

// buildNoticeboardCandidates collects the notice boards the town crier
// visits. Every board whose current state carries TagNoticeBoard +
// TagRotatable advances to the next rotatable-pool state (wrapping) —
// the flip is what triggers fresh prose authoring for the next visit,
// so WHICH state it lands on doesn't matter, only that it changes.
// Same tour at both window boundaries.
func buildNoticeboardCandidates(w *sim.World) []sim.RouteCandidate {
	var out []sim.RouteCandidate
	for id, obj := range w.VillageObjects {
		if obj == nil {
			continue
		}
		asset, ok := w.Assets[obj.AssetID]
		if !ok {
			continue
		}
		current := asset.FindState(obj.CurrentState)
		if current == nil || !current.HasTag(sim.TagRotatable) || !current.HasTag(sim.TagNoticeBoard) {
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
