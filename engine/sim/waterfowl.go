package sim

import (
	"context"
	"log"
	mathrand "math/rand/v2"
	"time"
)

// waterfowl.go — the engine-driven duck (LLM-579).
//
// A waterfowl is a KindDecorative actor whose sprite carries the
// "waterfowl" behavior slug (npc_sprite.behaviors). The admin authors one
// by dropping a duck sprite into a pond with the ordinary editor NPC
// placement — no attribute grant, no agent link, no configuration. The
// wander ticker below then drives it forever: paddle between points of
// the connected water region it was dropped into, come ashore now and
// then to potter around the bank, go back in. No LLM involvement at any
// point; movement rides the ordinary MoveIntent / locomotion-tick
// machinery, so the client renders it exactly like any walking villager.
//
// THE LAKE DEFINES ITSELF. There is no lake entity: each decision
// re-flood-fills the connected water region from the duck's current tile
// (or the nearest water tile within WaterfowlSearchRadius when it is
// ashore). Recomputing per decision instead of caching means a live
// terrain edit is picked up on the next decision with no invalidation
// hook; the fill is a few-hundred-tile BFS every few seconds per duck,
// which is noise.
//
// All wander state is transient in-memory (per the Postgres-is-durable-
// storage rule): the only thing checkpointed is the actor's position,
// which doubles as the re-seed anchor after a restart.
//
// WATER TRAVERSAL. The shared walk grid keeps water impassable — that is
// how villagers stay out of the lake. Waterfowl path on their own grid
// (buildWaterfowlWalkGrid): water costs 1, everything else keeps the
// standard cost, obstacles/passages/doors stamp identically. Grid
// selection is per-actor via walkGridForActor, used by MoveActor,
// SetActorPosition, and the locomotion tick.

const (
	// WaterfowlTickInterval is the decision-ticker cadence. Movement pacing
	// itself is the locomotion tick; this only bounds how quickly an idle
	// duck picks its next intention, so it can be lazy.
	WaterfowlTickInterval = 3 * time.Second

	// WaterfowlSearchRadius bounds the BFS for "nearest water tile" when a
	// duck is ashore (or was placed on land). Beyond this the duck is
	// considered parked away from any water and left idle.
	WaterfowlSearchRadius = 8

	// WaterfowlSwimRange caps how far (Chebyshev, in tiles) a single swim
	// leg reaches. Keeps each glide local so the duck putters about rather
	// than beelining across a big lake.
	WaterfowlSwimRange = 10

	// WaterfowlShoreChance is the per-decision permille chance an
	// on-water duck starts a shore excursion.
	WaterfowlShoreChance = 200

	// WaterfowlMaxAshoreMoves caps consecutive land potters before the
	// duck is sent back to the water.
	WaterfowlMaxAshoreMoves = 3
)

// waterfowlTickerState carries the coalescing flag for the waterfowl
// ticker's AfterFunc self-rearm chain. Same shape and rules as
// locomotionTickerState — touched exclusively from inside Command.Fn.
type waterfowlTickerState struct {
	scheduled bool
}

// waterfowlState is one duck's transient wander state. Held in
// World.waterfowl (lazily created); never checkpointed, never snapshotted.
type waterfowlState struct {
	// NextDecisionAt is when the idle duck acts next. Zero means "not yet
	// dwelling" — the first tick that observes the duck idle stamps a
	// jittered dwell so it bobs in place between legs instead of moving
	// back-to-back.
	NextDecisionAt time.Time

	// AshoreMoves counts consecutive land legs, so a pottering duck is
	// herded back into the water at WaterfowlMaxAshoreMoves.
	AshoreMoves int

	// noWaterLoggedAt de-spams the "no water near duck" log line: a duck
	// parked away from water hits that branch every decision forever.
	noWaterLoggedAt time.Time
}

// actorIsWaterfowl reports whether a is a live waterfowl: a decorative
// actor whose sprite carries the waterfowl behavior. The Kind gate is
// deliberate — a PC or agent NPC wearing a duck sprite must NOT gain
// water traversal (its movement is player/LLM-directed and the rest of
// the sim assumes land actors), and the wander must never seize an actor
// something else drives.
//
// MUST be called from inside a Command.Fn (reads w.Sprites).
func actorIsWaterfowl(w *World, a *Actor) bool {
	if a == nil || a.Kind != KindDecorative {
		return false
	}
	return w.Sprites[a.SpriteID].HasBehavior(BehaviorWaterfowl)
}

// walkGridForActor returns the walk grid this actor paths on: the
// waterfowl grid for waterfowl, the shared grid for everyone else. The
// two-value shape mirrors buildWalkGrid.
//
// MUST be called from inside a Command.Fn.
func walkGridForActor(w *World, a *Actor) (*WalkGrid, error) {
	if actorIsWaterfowl(w, a) {
		return buildWaterfowlWalkGrid(w)
	}
	return buildWalkGrid(w)
}

// isWaterTile reports whether the terrain byte at (x, y) is water.
// False out of bounds.
func isWaterTile(w *World, x, y int) bool {
	if w.Terrain == nil || x < 0 || x >= MapW || y < 0 || y >= MapH {
		return false
	}
	b := w.Terrain.Data[y*MapW+x]
	return b == TerrainShallowWater || b == TerrainDeepWater
}

// RunWaterfowlTicker owns the waterfowl decision ticker's periodic
// schedule. Caller starts this in a goroutine alongside World.Run;
// returns when ctx is cancelled. Same skeleton as RunLocomotionTicker.
func RunWaterfowlTicker(ctx context.Context, w *World) {
	_, err := w.SendContext(ctx, Command{
		Fn: func(w *World) (any, error) {
			armNextWaterfowlTick(w)
			return nil, nil
		},
	})
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/waterfowl: initial ticker arm failed: %v", err)
	}
	<-ctx.Done()
}

// armNextWaterfowlTick schedules the next decision tick. Coalescing
// no-op when one is already scheduled; the flag clears at the start of
// the scheduled Fn. MUST be called from inside a Command.Fn.
func armNextWaterfowlTick(w *World) {
	if w.waterfowlTick.scheduled {
		return
	}
	w.waterfowlTick.scheduled = true
	time.AfterFunc(WaterfowlTickInterval, func() { fireScheduledWaterfowlTick(w) })
}

// fireScheduledWaterfowlTick is the AfterFunc callback body — same
// shutdown posture as fireScheduledLocomotionTick.
func fireScheduledWaterfowlTick(w *World) {
	ctx := w.LifecycleContext()
	if ctx.Err() != nil {
		return
	}
	w.beatTicker("waterfowl")
	_, err := w.SendContext(ctx, Command{
		Fn: func(w *World) (any, error) {
			w.waterfowlTick.scheduled = false
			evaluateWaterfowl(w, time.Now())
			armNextWaterfowlTick(w)
			return nil, nil
		},
	})
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/waterfowl: scheduled tick failed: %v", err)
	}
}

// EvaluateWaterfowlTick exposes one decision pass as a Command so tests
// drive the wander deterministically through the command channel without
// the AfterFunc chain.
func EvaluateWaterfowlTick(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			evaluateWaterfowl(w, now)
			return nil, nil
		},
	}
}

// evaluateWaterfowl runs one decision pass over every idle waterfowl.
// MUST be called from inside a Command.Fn.
func evaluateWaterfowl(w *World, now time.Time) {
	for id, a := range w.Actors {
		if !actorIsWaterfowl(w, a) {
			continue
		}
		if a.MoveIntent != nil {
			continue // mid-leg; the locomotion tick owns it
		}
		if w.waterfowl == nil {
			w.waterfowl = make(map[ActorID]*waterfowlState)
		}
		st := w.waterfowl[id]
		if st == nil {
			st = &waterfowlState{}
			w.waterfowl[id] = st
		}
		if st.NextDecisionAt.IsZero() {
			// Just went idle — dwell (bob in place) before the next leg.
			st.NextDecisionAt = now.Add(waterfowlDwell())
			continue
		}
		if now.Before(st.NextDecisionAt) {
			continue
		}
		decideWaterfowlMove(w, a, st, now)
	}
	// Drop state for ducks that no longer exist (deleted via the editor).
	for id := range w.waterfowl {
		if _, ok := w.Actors[id]; !ok {
			delete(w.waterfowl, id)
		}
	}
}

// waterfowlDwell is the idle bob between legs.
func waterfowlDwell() time.Duration {
	return 2*time.Second + time.Duration(mathrand.Int64N(int64(10*time.Second)))
}

// decideWaterfowlMove picks and dispatches one leg for an idle duck:
// mostly a local swim, sometimes a shore excursion, always back toward
// water when it has pottered ashore long enough. A dispatch failure (or
// nothing to do) re-arms the dwell rather than erroring — the wander has
// no failure mode a duck can't wait out.
func decideWaterfowlMove(w *World, a *Actor, st *waterfowlState, now time.Time) {
	// However this decision ends, the next one starts from a fresh dwell.
	st.NextDecisionAt = time.Time{}

	region := waterfowlRegion(w, a)
	if len(region) == 0 {
		// Parked away from any water — leave it be, log once an hour.
		if now.Sub(st.noWaterLoggedAt) > time.Hour {
			st.noWaterLoggedAt = now
			log.Printf("sim/waterfowl: %s (%s) has no water within %d tiles — idling",
				a.DisplayName, a.ID, WaterfowlSearchRadius)
		}
		return
	}

	onWater := isWaterTile(w, a.Pos.X, a.Pos.Y)
	var target GridPoint
	var ok bool
	switch {
	case !onWater && st.AshoreMoves >= WaterfowlMaxAshoreMoves:
		// Pottered enough — back in the water.
		target, ok = pickWaterfowlTarget(w, a, region)
		st.AshoreMoves = 0
	case !onWater:
		// Ashore: usually potter along the bank, sometimes head back in.
		if mathrand.Int64N(1000) < 600 {
			target, ok = pickWaterfowlTarget(w, a, waterfowlShore(w, region))
			st.AshoreMoves++
		} else {
			target, ok = pickWaterfowlTarget(w, a, region)
			st.AshoreMoves = 0
		}
	case mathrand.Int64N(1000) < WaterfowlShoreChance:
		// On water: shore excursion.
		target, ok = pickWaterfowlTarget(w, a, waterfowlShore(w, region))
		st.AshoreMoves = 0
	default:
		// On water: local swim.
		target, ok = pickWaterfowlTarget(w, a, region)
		st.AshoreMoves = 0
	}
	if !ok {
		return
	}

	pos := Position{X: target.X, Y: target.Y}
	dest := MoveDestination{Kind: MoveDestinationPosition, Position: &pos}
	if _, err := MoveActor(a.ID, dest, false, now).Fn(w); err != nil {
		// Unreachable pick (transient blocker, occupied tile) — the next
		// decision draws fresh. Not worth logging: this is expected churn.
		return
	}
}

// waterfowlRegion flood-fills the connected water region the duck
// belongs to: from its own tile when swimming, else from the nearest
// water tile within WaterfowlSearchRadius (BFS over 4-neighbors,
// ignoring walkability — a shore bank's water is adjacent by
// definition). Empty when there is no water near the duck.
//
// MUST be called from inside a Command.Fn.
func waterfowlRegion(w *World, a *Actor) []GridPoint {
	seed, ok := nearestWaterTile(w, a.Pos)
	if !ok {
		return nil
	}
	// BFS flood fill over water tiles.
	type qp = GridPoint
	visited := map[GridPoint]bool{seed: true}
	queue := []qp{seed}
	region := []GridPoint{seed}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, d := range [4]GridPoint{{0, -1}, {0, 1}, {-1, 0}, {1, 0}} {
			n := GridPoint{X: cur.X + d.X, Y: cur.Y + d.Y}
			if visited[n] || !isWaterTile(w, n.X, n.Y) {
				continue
			}
			visited[n] = true
			region = append(region, n)
			queue = append(queue, n)
		}
	}
	return region
}

// nearestWaterTile returns the closest water tile to pos within
// WaterfowlSearchRadius (BFS ring order, so genuinely nearest-first).
func nearestWaterTile(w *World, pos Position) (GridPoint, bool) {
	start := GridPoint{X: pos.X, Y: pos.Y}
	if isWaterTile(w, start.X, start.Y) {
		return start, true
	}
	visited := map[GridPoint]bool{start: true}
	queue := []GridPoint{start}
	for depth := 0; depth < WaterfowlSearchRadius && len(queue) > 0; depth++ {
		var next []GridPoint
		for _, cur := range queue {
			for _, d := range [4]GridPoint{{0, -1}, {0, 1}, {-1, 0}, {1, 0}} {
				n := GridPoint{X: cur.X + d.X, Y: cur.Y + d.Y}
				if visited[n] || n.X < 0 || n.X >= MapW || n.Y < 0 || n.Y >= MapH {
					continue
				}
				visited[n] = true
				if isWaterTile(w, n.X, n.Y) {
					return n, true
				}
				next = append(next, n)
			}
		}
		queue = next
	}
	return GridPoint{}, false
}

// waterfowlShore returns the shore band of a water region: non-water
// tiles within two 4-steps of the region that are walkable on the
// waterfowl grid (so a bank tile inside a building footprint is never a
// potter target). This is "adjacent to the lake" — the duck's on-land
// range.
//
// MUST be called from inside a Command.Fn.
func waterfowlShore(w *World, region []GridPoint) []GridPoint {
	grid, err := buildWaterfowlWalkGrid(w)
	if err != nil {
		return nil
	}
	inRegion := make(map[GridPoint]bool, len(region))
	for _, p := range region {
		inRegion[p] = true
	}
	band := map[GridPoint]bool{}
	// Ring 1: land tiles adjacent to water.
	for _, p := range region {
		for _, d := range [4]GridPoint{{0, -1}, {0, 1}, {-1, 0}, {1, 0}} {
			n := GridPoint{X: p.X + d.X, Y: p.Y + d.Y}
			if inRegion[n] || isWaterTile(w, n.X, n.Y) || !grid.CanWalk(n.X, n.Y) {
				continue
			}
			band[n] = true
		}
	}
	// Ring 2: one more step out from ring 1.
	var shore []GridPoint
	for p := range band {
		shore = append(shore, p)
		for _, d := range [4]GridPoint{{0, -1}, {0, 1}, {-1, 0}, {1, 0}} {
			n := GridPoint{X: p.X + d.X, Y: p.Y + d.Y}
			if band[n] || inRegion[n] || isWaterTile(w, n.X, n.Y) || !grid.CanWalk(n.X, n.Y) {
				continue
			}
			shore = append(shore, n)
		}
	}
	return shore
}

// pickWaterfowlTarget draws a random candidate within WaterfowlSwimRange
// of the duck that isn't the tile it is standing on. A handful of draws
// beats filtering the whole slice — the range covers most of a small
// pond anyway, and a miss just means the duck dwells another beat.
func pickWaterfowlTarget(w *World, a *Actor, candidates []GridPoint) (GridPoint, bool) {
	if len(candidates) == 0 {
		return GridPoint{}, false
	}
	for try := 0; try < 8; try++ {
		p := candidates[mathrand.Int64N(int64(len(candidates)))]
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
		if dx > WaterfowlSwimRange || dy > WaterfowlSwimRange {
			continue
		}
		return p, true
	}
	return GridPoint{}, false
}
