package cascade

import (
	"context"
	"log"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
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
func RegisterNPCRoutes(ctx context.Context, w *sim.World, client llm.Client) {
	if w == nil {
		panic("cascade: RegisterNPCRoutes requires a non-nil world")
	}
	if client == nil {
		// The town-crier arrival branch authors the day's notices via the
		// LLM before reading them (LLM-44), so the client is required —
		// fail fast at wiring time (mirrors RegisterNoticeboard).
		panic("cascade: RegisterNPCRoutes requires a non-nil LLM client")
	}
	w.Subscribe(sim.SubscriberFunc(handlePhaseAppliedLamplighter))
	w.Subscribe(sim.SubscriberFunc(func(world *sim.World, evt sim.Event) {
		handleActorArrivedAdvanceRoute(ctx, world, evt, client)
	}))
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
				return buildNoticeboardCandidates(w, rng)
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
// Town crier branch (LLM-44): on arrival at a board stop she authors
// today's notices, posts them (the board variant is set to match the
// number actually authored), reads those notices aloud, and only then
// advances the route — so she reads exactly what she is posting, not the
// prior cycle's stale notice. The author LLM call runs off-world, so
// beginCrierBoardStop kicks it and the post/read/advance happen later in
// the author callback; the route's own per-stop flip is skipped because
// the crier owns the board's state. A no-news day (the stop's target is
// the empty, zero-capacity variant) posts the empty board and passes by
// silently.
//
// Cold-start: a freshly-loaded world has no NoticeboardContent stamped —
// KickstartNoticeboards seeds it shortly after boot so the boards aren't
// blank before the crier's first tour.
func handleActorArrivedAdvanceRoute(ctx context.Context, w *sim.World, evt sim.Event, client llm.Client) {
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

	if route.Label == sim.AttrTownCrier &&
		route.Phase == sim.RoutePhaseActive &&
		route.StopIdx < len(route.Stops) {
		stop := route.Stops[route.StopIdx]
		actor, ok := w.Actors[arrived.ActorID]
		atExpected := ok && actor.Pos.X == stop.WalkTo.X && actor.Pos.Y == stop.WalkTo.Y
		if atExpected {
			beginCrierBoardStop(ctx, w, client, arrived.ActorID, route.StopIdx, stop)
			return
		}
		// Stale arrival — run the route's re-walk/abandon machinery (the
		// crier never flips via the substrate, so skip-flip).
		if _, err := sim.AdvanceNPCRouteSkipFlip(arrived.ActorID).Fn(w); err != nil {
			log.Printf("cascade/npc_route: crier stale advance (actor %q event %d): %v",
				arrived.ActorID, arrived.EventID(), err)
		}
		return
	}

	// Non-crier routes (lamplighter / washerwoman) flip the stop inline.
	cmd := sim.AdvanceNPCRoute(arrived.ActorID)
	if _, err := cmd.Fn(w); err != nil {
		log.Printf("cascade/npc_route: advance (actor %q event %d): %v",
			arrived.ActorID, arrived.EventID(), err)
	}
}

// crierNoticeBeatDelay spaces the crier's spoken notice lines so each board
// notice lands as its own speech bubble and stays up long enough to read
// before the next replaces it — the client shows a single bubble per speaker
// (a fresh line replaces the prior) and scales its lifetime to text length, so
// firing all notices at once would show only the last. A board is read twice a
// day, so the cadence is deliberately unhurried. A var (not const) only so tests
// can shrink it to drive the deferred-flip timing deterministically; production
// never reassigns it.
var crierNoticeBeatDelay = 8 * time.Second

// voiceCrierNotices voices a (possibly multi-line) board aloud: the first
// notice immediately (inline, on the world goroutine we're already on), each
// subsequent notice one crierNoticeBeatDelay later via time.AfterFunc. The
// delayed beats marshal back onto the world goroutine through SendContext and
// are shutdown-guarded via the world's LifecycleContext — the same AfterFunc
// pattern the silence/pay-ledger sweeps use. EmitTownCrierAnnouncement
// re-checks the speaker each beat, so a crier that despawns or loses its
// town-crier attribute mid-tour simply stops voicing.
//
// Returns the number of notices voiced (0 if empty) so the caller can defer the
// route flip/advance until the spiel finishes (ZBBS-HOME-457).
func voiceCrierNotices(w *sim.World, crierID sim.ActorID, content string, at time.Time) int {
	lines := splitNoticeLines(content)
	if len(lines) == 0 {
		return 0
	}
	if _, err := sim.EmitTownCrierAnnouncement(crierID, lines[0], at).Fn(w); err != nil {
		log.Printf("cascade/npc_route: town_crier announce (actor %q): %v", crierID, err)
	}
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		time.AfterFunc(time.Duration(i)*crierNoticeBeatDelay, func() {
			ctx := w.LifecycleContext()
			if ctx.Err() != nil {
				return // shutdown raced the beat
			}
			if _, err := w.SendContext(ctx, sim.EmitTownCrierAnnouncement(crierID, line, time.Now())); err != nil && ctx.Err() == nil {
				log.Printf("cascade/npc_route: town_crier delayed announce (actor %q): %v", crierID, err)
			}
		})
	}
	return len(lines)
}

// splitNoticeLines splits stored multi-line board content into individual
// trimmed, non-empty notice lines — the inverse of ClampNoticeboardContent's
// newline join.
func splitNoticeLines(content string) []string {
	raw := strings.Split(content, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if t := strings.TrimSpace(line); t != "" {
			out = append(out, t)
		}
	}
	return out
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
// This laundry builder is directional (keyed on DefaultState, not the
// algo); the noticeboard builder honors the algo for its random vs
// sequential pick (see nextNoticeboardState) but likewise never gates
// stop-building on it.
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
// TagRotatable advances to a new rotatable-pool state — the flip is what
// triggers fresh prose authoring for the next visit. The new state is
// picked per the asset's RotationAlgo (see nextNoticeboardState): a random
// algo lands on a varied pool state each cycle, deterministic cycles in
// ID order. Same tour at both window boundaries.
func buildNoticeboardCandidates(w *sim.World, rng *rand.Rand) []sim.RouteCandidate {
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
		next := nextNoticeboardState(asset.RotationAlgo, pool, obj.CurrentState, rng)
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

// nextNoticeboardState picks the crier's next state for a board, honoring
// the asset's RotationAlgo — the setting v1 used to vary boards that the
// pre-446 sequential-only crier path dropped (the bulk rotation in
// world_rotation.go still honors it; boards are carved out of that path, so
// without this the configured algo had no effect on them).
//
// A random algo picks a random pool state OTHER than the current one, so a
// board lands on a varied state each cycle — including, by design, the empty
// (zero-capacity) variant, which is an acceptable "nothing posted today"
// resting state — instead of marching predictably through the pool in ID
// order. Deterministic (and any unset/unrecognized algo, as a never-freeze
// fallback so the crier always has a flip to make) keeps the sequential wrap.
//
// random_per_asset collapses to a per-board random pick here: unlike laundry
// lines, each board authors its own content on flip, so the shared-target
// uniformity the bulk path gives random_per_asset buys nothing — only the
// slip count (capacity) would match across boards.
func nextNoticeboardState(algo string, pool []*sim.AssetState, current string, rng *rand.Rand) string {
	switch algo {
	case sim.RotationAlgoRandomPerObject, sim.RotationAlgoRandomPerAsset:
		return pickPoolStateExcluding(pool, current, rng)
	default:
		return nextPoolState(pool, current)
	}
}

// pickPoolStateExcluding returns a random pool state other than current.
// A single-state pool (no alternative) returns that state — the caller drops
// the resulting no-op flip. Mirrors world_rotation.go's pickRandomExcluding
// (unexported in package sim, so re-stated here): bounded retries guard a
// degenerate all-equal pool before the linear fallback.
func pickPoolStateExcluding(pool []*sim.AssetState, current string, rng *rand.Rand) string {
	if len(pool) == 0 {
		return current
	}
	if len(pool) == 1 {
		return pool[0].State
	}
	for attempt := 0; attempt < len(pool)*4; attempt++ {
		if s := pool[rng.Intn(len(pool))].State; s != current {
			return s
		}
	}
	for _, s := range pool {
		if s.State != current {
			return s.State
		}
	}
	return pool[0].State
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
