package sim

// blockers.go — PR 4 tile-blocker classification for the locomotion
// ticker. One helper, so the hard/soft decision has exactly one
// definition and is testable in isolation.

// classifyTileBlocker classifies the next tile on a mover's path:
//
//   - hard — the tile is not walkable per the current WalkGrid: a wall,
//     an obstacle footprint, water, a tile that became non-traversable.
//     The mover stops; the ticker emits ActorMoveStopped{blocked} and
//     clears the MoveIntent.
//   - soft — the tile is walkable terrain but another actor is standing
//     on it right now. Transient: the ticker preserves the MoveIntent
//     and retries next tick (next tick's re-plan may also route around).
//   - neither — the tile is clear; the mover advances onto it.
//
// hard takes precedence: a tile that is both non-walkable and occupied
// reports hard (who is standing there is irrelevant if nobody can).
//
// Actor occupancy is checked here rather than by FindPath because the
// WalkGrid does not encode actor positions — buildWalkGrid stamps
// terrain + village objects only.
//
// MUST be called from inside a Command.Fn (tileOccupiedByOtherActor
// reads w.Actors). Unexported by design.
func classifyTileBlocker(grid *WalkGrid, w *World, tile Position, mover ActorID) (hard, soft bool) {
	if !grid.CanWalk(tile.X, tile.Y) {
		return true, false
	}
	if tileOccupiedByOtherActor(w, tile, mover) {
		return false, true
	}
	return false, false
}
