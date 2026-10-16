package sim

import (
	"context"
	"log"
	mathrand "math/rand/v2"
	"time"
)

// familiar.go — the duck that follows a duck-pendant wearer (LLM-800).
//
// A familiar is a pond duck with an owner: a KindDecorative actor in a duck
// sprite (so the waterfowl walk grid, the half-speed walk, the ambient
// action-log skip and the no-huddle gate all apply unchanged) whose
// Actor.FamiliarOf names the wearer. The pond wander skips it; this ticker
// drives it instead.
//
//   - Put the pendant on: a duck spawns on a map-edge road tile (the visitor
//     edge picker) and walks to its owner.
//   - Following: when the duck is more than FamiliarFollowRadius tiles from its
//     owner it walks to a free tile beside them. An owner indoors keeps the
//     door tile as Pos, so the duck waits outside the door.
//   - Pottering (LLM-812): while the owner stands still outdoors and awake, the
//     duck walks round the edge of their space — a free tile on the ring
//     FamiliarFollowRadius out — after each short dwell.
//   - Take it off, or lose the last pendant: the duck walks to a map edge and
//     is removed. Put it back on while it is still walking off and it turns
//     around.
//   - Restart: familiars are never checkpointed. The first pass after boot
//     finds each wearer without a duck and puts one beside them.

const (
	// DuckPendantKind is the good whose wearing brings a duck familiar.
	DuckPendantKind ItemKind = "duck_pendant"

	// FamiliarTickInterval is the familiar ticker's cadence.
	FamiliarTickInterval = 2 * time.Second

	// FamiliarFollowRadius is how near (Chebyshev tiles) the duck keeps to its
	// owner. Within it the duck follows no further; while the owner stands still
	// outdoors it potters round the ring this far out (LLM-812).
	FamiliarFollowRadius = 2

	// FamiliarPotterDwell is the least the duck stands between two potters;
	// familiarPotterDwell adds up to FamiliarPotterJitter more.
	FamiliarPotterDwell  = 3 * time.Second
	FamiliarPotterJitter = 6 * time.Second

	// familiarSpriteID is the "Duck (mallard)" sprite (migration LLM-579).
	familiarSpriteID SpriteID = "579d0c01-0000-4000-8000-000000000001"

	familiarActorIDPrefix = "fam-"
)

type familiarTickerState struct {
	scheduled bool
}

// familiarState is one familiar's transient state, keyed by the familiar's
// actor id in World.familiars.
type familiarState struct {
	// Departing is set while the duck walks off to a map edge.
	Departing bool

	// Target is the follow spot last dispatched; HasTarget is false until the
	// first one. The duck keeps walking while Target stays near its owner.
	Target    TilePos
	HasTarget bool

	// NextPotterAt is when the duck next potters round its standing owner; zero
	// until it comes to rest beside them (LLM-812).
	NextPotterAt time.Time
}

// RunFamiliarTicker owns the familiar ticker's periodic schedule. Same
// skeleton as RunWaterfowlTicker.
func RunFamiliarTicker(ctx context.Context, w *World) {
	_, err := w.SendContext(ctx, Command{
		Fn: func(w *World) (any, error) {
			armNextFamiliarTick(w)
			return nil, nil
		},
	})
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/familiar: initial ticker arm failed: %v", err)
	}
	<-ctx.Done()
}

func armNextFamiliarTick(w *World) {
	if w.familiarTick.scheduled {
		return
	}
	w.familiarTick.scheduled = true
	time.AfterFunc(FamiliarTickInterval, func() { fireScheduledFamiliarTick(w) })
}

func fireScheduledFamiliarTick(w *World) {
	ctx := w.LifecycleContext()
	if ctx.Err() != nil {
		return
	}
	w.beatTicker("familiar")
	_, err := w.SendContext(ctx, Command{
		Fn: func(w *World) (any, error) {
			w.familiarTick.scheduled = false
			evaluateFamiliars(w, time.Now())
			armNextFamiliarTick(w)
			return nil, nil
		},
	})
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/familiar: scheduled tick failed: %v", err)
	}
}

// EvaluateFamiliarTick exposes one pass as a Command for tests.
func EvaluateFamiliarTick(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			evaluateFamiliars(w, now)
			return nil, nil
		},
	}
}

// evaluateFamiliars runs one pass: take off pendants no longer held, give each
// wearer without a duck one beside them, and move every duck along.
// MUST be called from inside a Command.Fn.
func evaluateFamiliars(w *World, now time.Time) {
	for _, a := range w.Actors {
		if a.FamiliarOf != "" || len(a.Wearing) == 0 {
			continue
		}
		dropUnheldWearing(a)
		reconcileFamiliar(w, a, now, false)
	}
	for id, d := range w.Actors {
		if d.FamiliarOf == "" {
			continue
		}
		st := ensureFamiliarState(w, id)
		owner := w.Actors[d.FamiliarOf]
		if owner == nil {
			removeFamiliar(w, d, now)
			continue
		}
		if !ownerWantsFamiliar(owner) && !st.Departing {
			beginFamiliarDeparture(w, d, st, now)
			continue
		}
		if st.Departing {
			if d.MoveIntent == nil {
				removeFamiliar(w, d, now)
			}
			continue
		}
		followOwner(w, d, st, owner, now)
	}
	for id := range w.familiars {
		if _, ok := w.Actors[id]; !ok {
			delete(w.familiars, id)
		}
	}
}

func ensureFamiliarState(w *World, id ActorID) *familiarState {
	if w.familiars == nil {
		w.familiars = make(map[ActorID]*familiarState)
	}
	st := w.familiars[id]
	if st == nil {
		st = &familiarState{}
		w.familiars[id] = st
	}
	return st
}

func ownerWantsFamiliar(a *Actor) bool {
	return actorWears(a, DuckPendantKind) && a.Inventory[DuckPendantKind] > 0
}

// familiarFor returns owner's familiar, or nil.
func familiarFor(w *World, ownerID ActorID) *Actor {
	for _, d := range w.Actors {
		if d.FamiliarOf == ownerID {
			return d
		}
	}
	return nil
}

// reconcileFamiliar brings owner's duck in line with what they wear: spawn
// one when they want it and have none (fromEdge: walking in from a map edge,
// else beside them), turn a departing one around, or send it off.
func reconcileFamiliar(w *World, owner *Actor, now time.Time, fromEdge bool) {
	want := ownerWantsFamiliar(owner)
	d := familiarFor(w, owner.ID)
	switch {
	case want && d == nil:
		spawnFamiliar(w, owner, now, fromEdge)
	case want && d != nil:
		st := ensureFamiliarState(w, d.ID)
		if st.Departing {
			st.Departing = false
			st.HasTarget = false
			followOwner(w, d, st, owner, now)
		}
	case !want && d != nil:
		st := ensureFamiliarState(w, d.ID)
		if !st.Departing {
			beginFamiliarDeparture(w, d, st, now)
		}
	}
}

// spawnFamiliar adds owner's duck to the world. fromEdge puts it on a map-edge
// road tile walking in; otherwise (and when no edge tile can be had) it starts
// on a free tile beside the owner. With neither it is not spawned, and the
// ticker's next pass tries again.
func spawnFamiliar(w *World, owner *Actor, now time.Time, fromEdge bool) {
	sprite := w.Sprites[familiarSpriteID]
	if sprite == nil {
		log.Printf("sim/familiar: duck sprite %s missing; %s gets no familiar", familiarSpriteID, owner.DisplayName)
		return
	}
	var pos TilePos
	placed := false
	if fromEdge {
		if grid, err := buildWalkGrid(w); err == nil {
			pos, placed = pickVisitorEdgeTile(w, grid, owner.Pos, inputsRandOrDefault(nil))
		}
	}
	if !placed {
		pos, placed = pickFamiliarSpot(w, "", owner.Pos, owner.Pos)
	}
	if !placed {
		return // no free tile beside them; the next pass tries again
	}

	id := ActorID("")
	for attempt := 0; attempt < 10; attempt++ {
		candidate := ActorID(familiarActorIDPrefix + randomHex(4))
		if _, exists := w.Actors[candidate]; !exists {
			id = candidate
			break
		}
	}
	if id == "" {
		log.Printf("sim/familiar: actor-ID minting exhausted 10 retries; %s gets no familiar this pass", owner.DisplayName)
		return
	}
	name := owner.DisplayName + "'s duck"
	if len([]rune(name)) > MaxActorDisplayNameLen {
		name = "Duck"
	}
	d := &Actor{
		ID:          id,
		DisplayName: name,
		Kind:        KindDecorative,
		SpriteID:    familiarSpriteID,
		Facing:      "south",
		Pos:         pos,
		Needs:       seedVisitorNeeds(),
		Inventory:   map[ItemKind]int{},
		Attributes:  map[string][]byte{},
		State:       StateIdle,
		FamiliarOf:  owner.ID,
	}
	w.Actors[id] = d
	w.outdoorActors[id] = struct{}{}
	st := ensureFamiliarState(w, id)
	w.emit(&NPCCreated{
		ActorID:     id,
		DisplayName: name,
		Kind:        KindDecorative,
		X:           pos.X,
		Y:           pos.Y,
		Facing:      d.Facing,
		Sprite:      sprite,
		FamiliarOf:  owner.ID,
		At:          now.UTC(),
	})
	log.Printf("sim/familiar: %s's duck (id=%s) at (%d,%d), from edge=%v", owner.DisplayName, id, pos.X, pos.Y, fromEdge && placed)
	followOwner(w, d, st, owner, now)
}

// followOwner keeps d within FamiliarFollowRadius of its owner. Near and idle:
// stay. Walking to a spot still near the owner: keep walking. Otherwise walk
// to the free spot beside the owner nearest the duck.
func followOwner(w *World, d *Actor, st *familiarState, owner *Actor, now time.Time) {
	goal := owner.Pos
	if d.MoveIntent == nil && chebyshevDistance(d.Pos, goal) <= FamiliarFollowRadius {
		st.HasTarget = false
		potterRoundOwner(w, d, st, owner, now)
		return
	}
	st.NextPotterAt = time.Time{}
	if d.MoveIntent != nil && st.HasTarget && chebyshevDistance(st.Target, goal) <= FamiliarFollowRadius {
		return
	}
	spot, ok := pickFamiliarSpot(w, d.ID, d.Pos, goal)
	if !ok {
		return
	}
	dest := NewPositionDestination(spot)
	// leaveHuddleFirst as the pond wander (LLM-582): a duck has nothing to say.
	if _, err := MoveActor(d.ID, dest, true, now).Fn(w); err != nil {
		st.HasTarget = false
		return
	}
	st.Target = spot
	st.HasTarget = true
}

// potterRoundOwner walks a resting duck round the edge of its owner's space
// while they stand still outdoors and awake (LLM-812): a short dwell, then a
// free tile on the FamiliarFollowRadius ring, then another dwell. Indoors, asleep
// or on the move, the owner gets no pottering.
func potterRoundOwner(w *World, d *Actor, st *familiarState, owner *Actor, now time.Time) {
	if owner.MoveIntent != nil || owner.InsideStructureID != "" || owner.SleepingUntil != nil {
		st.NextPotterAt = time.Time{}
		return
	}
	if st.NextPotterAt.IsZero() {
		st.NextPotterAt = now.Add(familiarPotterDwell())
		return
	}
	if now.Before(st.NextPotterAt) {
		return
	}
	st.NextPotterAt = time.Time{}
	spot, ok := pickPotterSpot(w, d, owner.Pos)
	if !ok {
		return
	}
	if _, err := MoveActor(d.ID, NewPositionDestination(spot), true, now).Fn(w); err != nil {
		return
	}
	st.Target = spot
	st.HasTarget = true
}

func familiarPotterDwell() time.Duration {
	return FamiliarPotterDwell + time.Duration(mathrand.Int64N(int64(FamiliarPotterJitter)))
}

// pickPotterSpot picks a random free tile on the ring exactly
// FamiliarFollowRadius from goal (Chebyshev) — the edge of the owner's space —
// that the duck can stand on, not its own tile.
func pickPotterSpot(w *World, d *Actor, goal TilePos) (TilePos, bool) {
	grid, err := buildWaterfowlWalkGrid(w)
	if err != nil {
		return TilePos{}, false
	}
	r := FamiliarFollowRadius
	var ring []TilePos
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			p := TilePos{X: goal.X + dx, Y: goal.Y + dy}
			if chebyshevDistance(p, goal) != r || p == d.Pos {
				continue
			}
			if !grid.CanWalk(p.X, p.Y) || tileOccupiedByRestingOtherActor(w, p, d.ID) {
				continue
			}
			ring = append(ring, p)
		}
	}
	if len(ring) == 0 {
		return TilePos{}, false
	}
	return ring[mathrand.IntN(len(ring))], true
}

// pickFamiliarSpot picks the tile within FamiliarFollowRadius of goal (never
// goal itself) that the duck can stand on and nobody rests on, nearest from.
// self is the duck (excluded from the occupancy test; "" before it exists).
func pickFamiliarSpot(w *World, self ActorID, from, goal TilePos) (TilePos, bool) {
	grid, err := buildWaterfowlWalkGrid(w)
	if err != nil {
		return TilePos{}, false
	}
	best := TilePos{}
	bestDist := -1
	for dy := -FamiliarFollowRadius; dy <= FamiliarFollowRadius; dy++ {
		for dx := -FamiliarFollowRadius; dx <= FamiliarFollowRadius; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			p := TilePos{X: goal.X + dx, Y: goal.Y + dy}
			if !grid.CanWalk(p.X, p.Y) || tileOccupiedByRestingOtherActor(w, p, self) {
				continue
			}
			// Nearest the duck first; break ties toward the owner so the duck
			// settles close rather than at the ring's edge.
			dist := chebyshevDistance(p, from)*8 + chebyshevDistance(p, goal)
			if bestDist < 0 || dist < bestDist {
				best, bestDist = p, dist
			}
		}
	}
	return best, bestDist >= 0
}

// beginFamiliarDeparture sends d off to a map edge. With no edge tile or no
// path it is removed at once.
func beginFamiliarDeparture(w *World, d *Actor, st *familiarState, now time.Time) {
	st.Departing = true
	st.HasTarget = false
	grid, err := buildWalkGrid(w)
	if err != nil {
		removeFamiliar(w, d, now)
		return
	}
	edge, ok := pickVisitorEdgeTile(w, grid, d.Pos, inputsRandOrDefault(nil))
	if !ok {
		removeFamiliar(w, d, now)
		return
	}
	if _, err := MoveActor(d.ID, NewPositionDestination(edge), true, now).Fn(w); err != nil {
		removeFamiliar(w, d, now)
	}
}

// removeFamiliar takes d out of the world, as DeleteActor does an editor NPC:
// ActorDeparted first (the hub turns it into npc_deleted), then the indexes.
func removeFamiliar(w *World, d *Actor, now time.Time) {
	w.emit(&ActorDeparted{
		ActorID:               d.ID,
		DisplayName:           d.DisplayName,
		LastInsideStructureID: d.InsideStructureID,
		LastPosition:          d.Pos,
		At:                    now.UTC(),
	})
	if d.CurrentHuddleID != "" {
		if members, ok := w.actorsByHuddle[d.CurrentHuddleID]; ok {
			delete(members, d.ID)
			if len(members) == 0 {
				delete(w.actorsByHuddle, d.CurrentHuddleID)
			}
		}
	}
	setActorInsideStructure(w, d, "")
	delete(w.outdoorActors, d.ID)
	delete(w.Actors, d.ID)
	delete(w.familiars, d.ID)
}
