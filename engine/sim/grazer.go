package sim

import (
	"context"
	"log"
	mathrand "math/rand/v2"
	"time"
)

// grazer.go — engine-driven livestock (LLM-639), the land cousin of the
// waterfowl wander (waterfowl.go — read its header first; every design rule
// there applies here unless this file says otherwise).
//
// A grazer is a KindDecorative actor whose sprite carries the "grazer"
// behavior slug (npc_sprite.behaviors) — the cattle. The admin authors one by
// dropping a cow sprite into a pen with the ordinary editor NPC placement —
// no attribute grant, no agent link, no configuration. The wander below
// drives it forever: long grazing dwells (the sprite's idle rows ARE grazing
// animations), then a short amble to a nearby tile. No LLM involvement;
// movement rides the ordinary MoveIntent / locomotion-tick machinery.
//
// THE PEN DEFINES ITSELF. There is no pen entity: each decision flood-fills
// the tiles the grazer can walk to (on its own grid) within GrazerRoamRange
// of where it stands. Inside a closed fence ring that region IS the pen
// interior; loose, the animal drifts across the village — which is exactly
// the lever fences are for. Recomputing per decision means a live fence edit
// is picked up on the next decision with no invalidation hook.
//
// GATES. A grazer paths on its own grid (buildGrazerWalkGrid): the standard
// grid with the footprint tiles of every FENCE-GATE placement stamped
// impassable. A gate asset is NOT an obstacle — people and villagers walk
// through it freely — it blocks only actors that path on this grid. The gate
// contract is the asset-state tag "fence-gate" (TagFenceGate), the same
// tag-is-the-contract pattern as the fence pieces (fence.go).
//
// All wander state is transient in-memory; the checkpointed actor position
// is the restart anchor.

const (
	// GrazerTickInterval is the decision-ticker cadence — how often idle
	// grazers get a chance to act. Movement pacing is the locomotion tick.
	GrazerTickInterval = 3 * time.Second

	// GrazerRoamRange caps the reachable-region flood fill (Chebyshev tiles
	// from the animal's current tile). Bounds the per-decision BFS to a
	// (2N+1)² box, and is the loose animal's drift rate ceiling per leg —
	// inside a pen the fence is the real boundary.
	GrazerRoamRange = 12

	// GrazerAmbleRange caps a single leg (Chebyshev), so a cow shifts a few
	// tiles at a time rather than beelining across its region.
	GrazerAmbleRange = 5

	// GrazerStepDivisor slows grazer locomotion: a grazer mover advances on
	// every Nth locomotion tick, same mechanism as WaterfowlStepDivisor and
	// the SAME two-sided contract — the client lerps slow walkers at 1/N of
	// walk speed (client/scripts/world.gd npc_walk_speed_factor), keyed off
	// the sprite's behavior slugs. Change the two in lockstep or walking
	// cattle visibly desync from their tiles.
	GrazerStepDivisor = 2
)

// TagFenceGate marks an asset state whose placements are livestock barriers:
// walkable for everyone on the standard grid (the asset must NOT be an
// obstacle or it would block people too), impassable on the grazer grid
// across the placement's whole footprint. Lives with the fence piece tags
// conceptually; declared here because only grazer code reads it.
const TagFenceGate = "fence-gate"

// grazerTickerState carries the coalescing flag for the grazer ticker's
// AfterFunc self-rearm chain — same shape and rules as waterfowlTickerState.
type grazerTickerState struct {
	scheduled bool
}

// grazerState is one animal's transient wander state. Held in World.grazers
// (lazily created); never checkpointed, never snapshotted.
type grazerState struct {
	// NextDecisionAt is when the idle animal acts next. Zero means "not yet
	// dwelling" — the first tick that observes it idle stamps a jittered
	// grazing dwell.
	NextDecisionAt time.Time

	// stepBeat counts locomotion ticks for the GrazerStepDivisor slow walk:
	// the animal advances only when stepBeat wraps to 0.
	stepBeat int
}

// actorIsGrazer reports whether a is a live grazer: a decorative actor whose
// sprite carries the grazer behavior. The Kind gate mirrors actorIsWaterfowl's
// and is just as deliberate — a PC or agent NPC wearing a cow sprite must not
// be gate-blocked or seized by the wander.
//
// MUST be called from inside a Command.Fn (reads w.Sprites).
func actorIsGrazer(w *World, a *Actor) bool {
	if a == nil || a.Kind != KindDecorative {
		return false
	}
	return w.Sprites[a.SpriteID].HasBehavior(BehaviorGrazer)
}

// isFenceGateAsset reports whether the asset's placements are livestock
// barriers — any state carries TagFenceGate.
func isFenceGateAsset(asset *Asset) bool {
	return asset != nil && asset.StateForTag(TagFenceGate) != nil
}

// buildGrazerWalkGrid is the grazer's movement grid: the standard grid with
// every fence-gate placement's footprint stamped impassable. Everything else
// — terrain costs, obstacles, passages, doors — is byte-identical to the
// standard grid, so the two can never disagree about the rest of the world.
//
// MUST be called from inside a Command.Fn.
func buildGrazerWalkGrid(w *World) (*WalkGrid, error) {
	grid, err := buildWalkGrid(w)
	if err != nil {
		return nil, err
	}
	for _, obj := range w.VillageObjects {
		if obj == nil || obj.AttachedTo != "" {
			continue // overlay attachments don't stamp, mirroring the standard pass
		}
		asset := w.Assets[obj.AssetID]
		if !isFenceGateAsset(asset) {
			continue
		}
		anchor := obj.Pos.Tile()
		for ty := anchor.Y - asset.FootprintTop; ty <= anchor.Y+asset.FootprintBottom; ty++ {
			if ty < 0 || ty >= MapH {
				continue
			}
			for tx := anchor.X - asset.FootprintLeft; tx <= anchor.X+asset.FootprintRight; tx++ {
				if tx < 0 || tx >= MapW {
					continue
				}
				grid.cost[ty*MapW+tx] = 0
			}
		}
	}
	return grid, nil
}

// grazerShouldStep implements the GrazerStepDivisor slow walk — the grazer
// half of waterfowlShouldStep, with its own per-animal beat.
//
// MUST be called from inside a Command.Fn.
func grazerShouldStep(w *World, id ActorID) bool {
	st := ensureGrazerState(w, id)
	st.stepBeat = (st.stepBeat + 1) % GrazerStepDivisor
	return st.stepBeat == 0
}

// grazerResetStepBeat re-arms the slow-walk beat so the NEXT locomotion tick
// is a stepping one — same determinism contract as waterfowlResetStepBeat,
// called by MoveActor for every accepted grazer movement.
//
// MUST be called from inside a Command.Fn.
func grazerResetStepBeat(w *World, id ActorID) {
	ensureGrazerState(w, id).stepBeat = GrazerStepDivisor - 1
}

// ensureGrazerState returns (lazily creating) the per-animal wander state.
// MUST be called from inside a Command.Fn.
func ensureGrazerState(w *World, id ActorID) *grazerState {
	if w.grazers == nil {
		w.grazers = make(map[ActorID]*grazerState)
	}
	st := w.grazers[id]
	if st == nil {
		st = &grazerState{}
		w.grazers[id] = st
	}
	return st
}

// RunGrazerTicker owns the grazer decision ticker's periodic schedule.
// Caller starts this in a goroutine alongside World.Run; returns when ctx is
// cancelled. Same skeleton as RunWaterfowlTicker.
func RunGrazerTicker(ctx context.Context, w *World) {
	_, err := w.SendContext(ctx, Command{
		Fn: func(w *World) (any, error) {
			armNextGrazerTick(w)
			return nil, nil
		},
	})
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/grazer: initial ticker arm failed: %v", err)
	}
	<-ctx.Done()
}

// armNextGrazerTick schedules the next decision tick. Coalescing no-op when
// one is already scheduled. MUST be called from inside a Command.Fn.
func armNextGrazerTick(w *World) {
	if w.grazerTick.scheduled {
		return
	}
	w.grazerTick.scheduled = true
	time.AfterFunc(GrazerTickInterval, func() { fireScheduledGrazerTick(w) })
}

// fireScheduledGrazerTick is the AfterFunc callback body — same shutdown
// posture as fireScheduledWaterfowlTick.
func fireScheduledGrazerTick(w *World) {
	ctx := w.LifecycleContext()
	if ctx.Err() != nil {
		return
	}
	w.beatTicker("grazer")
	_, err := w.SendContext(ctx, Command{
		Fn: func(w *World) (any, error) {
			w.grazerTick.scheduled = false
			evaluateGrazers(w, time.Now())
			armNextGrazerTick(w)
			return nil, nil
		},
	})
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/grazer: scheduled tick failed: %v", err)
	}
}

// EvaluateGrazerTick exposes one decision pass as a Command so tests drive
// the wander deterministically without the AfterFunc chain.
func EvaluateGrazerTick(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			evaluateGrazers(w, now)
			return nil, nil
		},
	}
}

// evaluateGrazers runs one decision pass over every idle grazer.
// MUST be called from inside a Command.Fn.
func evaluateGrazers(w *World, now time.Time) {
	for id, a := range w.Actors {
		if !actorIsGrazer(w, a) {
			continue
		}
		if a.MoveIntent != nil {
			continue // mid-leg; the locomotion tick owns it
		}
		st := ensureGrazerState(w, id)
		if st.NextDecisionAt.IsZero() {
			// Just went idle — graze in place before the next amble. The idle
			// animation rows are grazing poses, so the dwell reads as eating.
			st.NextDecisionAt = now.Add(grazerDwell())
			continue
		}
		if now.Before(st.NextDecisionAt) {
			continue
		}
		decideGrazerMove(w, a, st, now)
	}
	// Drop state for animals that no longer exist (deleted via the editor).
	for id := range w.grazers {
		if _, ok := w.Actors[id]; !ok {
			delete(w.grazers, id)
		}
	}
}

// grazerDwell is the grazing pause between ambles. Long and lazy on purpose:
// cattle mostly stand and eat, and the pen reads calmer for it.
func grazerDwell() time.Duration {
	return 8*time.Second + time.Duration(mathrand.Int64N(int64(37*time.Second)))
}

// decideGrazerMove picks and dispatches one amble for an idle grazer. A
// dispatch failure (or nothing to do) re-arms the dwell rather than erroring
// — the wander has no failure mode an animal can't wait out.
func decideGrazerMove(w *World, a *Actor, st *grazerState, now time.Time) {
	// However this decision ends, the next one starts from a fresh dwell.
	st.NextDecisionAt = time.Time{}

	region := grazerRegion(w, a)
	if len(region) == 0 {
		// Boxed in completely (or standing somewhere unwalkable after an
		// admin teleport) — wait; a fence edit frees it on a later decision.
		return
	}
	target, ok := pickGrazerTarget(a, region)
	if !ok {
		return
	}

	pos := Position{X: target.X, Y: target.Y}
	dest := MoveDestination{Kind: MoveDestinationPosition, Position: &pos}
	// leaveHuddleFirst mirrors the waterfowl dispatch (LLM-582): a decorative
	// can never speak, so if any huddle path reaches it, leaving is always the
	// right resolution — otherwise the silent error path below freezes it.
	if _, err := MoveActor(a.ID, dest, true, now).Fn(w); err != nil {
		// Unreachable pick (transient blocker, occupied tile) — the next
		// decision draws fresh. Expected churn, not worth logging.
		return
	}
}

// grazerRegion flood-fills the tiles the animal can reach on ITS grid within
// GrazerRoamRange (Chebyshev) of where it stands: 4-neighbor BFS over
// grazer-walkable tiles, bounded by the range box. Inside a closed pen the
// bound never bites and the region is exactly the pen interior; loose, the
// box keeps the fill (and the drift) local. Excludes the animal's own tile
// as a target by leaving it to pickGrazerTarget.
//
// MUST be called from inside a Command.Fn.
func grazerRegion(w *World, a *Actor) []GridPoint {
	grid, err := buildGrazerWalkGrid(w)
	if err != nil {
		return nil
	}
	start := GridPoint{X: a.Pos.X, Y: a.Pos.Y}
	inRange := func(p GridPoint) bool {
		dx, dy := p.X-start.X, p.Y-start.Y
		if dx < 0 {
			dx = -dx
		}
		if dy < 0 {
			dy = -dy
		}
		return dx <= GrazerRoamRange && dy <= GrazerRoamRange
	}
	visited := map[GridPoint]bool{start: true}
	queue := []GridPoint{start}
	var region []GridPoint
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, d := range [4]GridPoint{{0, -1}, {0, 1}, {-1, 0}, {1, 0}} {
			n := GridPoint{X: cur.X + d.X, Y: cur.Y + d.Y}
			if visited[n] || !inRange(n) || !grid.CanWalk(n.X, n.Y) {
				continue
			}
			visited[n] = true
			region = append(region, n)
			queue = append(queue, n)
		}
	}
	return region
}

// pickGrazerTarget draws a random region tile within GrazerAmbleRange of the
// animal that isn't where it stands — same bounded-draws shape as
// pickWaterfowlTarget, and a miss just means another grazing beat.
func pickGrazerTarget(a *Actor, region []GridPoint) (GridPoint, bool) {
	if len(region) == 0 {
		return GridPoint{}, false
	}
	for try := 0; try < 8; try++ {
		p := region[mathrand.Int64N(int64(len(region)))]
		if p.X == a.Pos.X && p.Y == a.Pos.Y {
			continue
		}
		dx, dy := p.X-a.Pos.X, p.Y-a.Pos.Y
		if dx < 0 {
			dx = -dx
		}
		if dy < 0 {
			dy = -dy
		}
		if dx > GrazerAmbleRange || dy > GrazerAmbleRange {
			continue
		}
		return p, true
	}
	return GridPoint{}, false
}
