package sim

import "time"

// checkHuddleDriftAfterPositionMutation runs after any command that
// mutates Actor.Position (CurrentX/CurrentY) or Actor.InsideStructureID
// to enforce the scene-bound physical-presence invariant: an actor in
// an active huddle whose scene's bound no longer contains them is
// automatically removed from that huddle.
//
// The drift case the helper handles:
//
//   - Two actors join an outdoor SceneBoundArea huddle anchored at
//     position P with radius R.
//   - One of them walks outside the radius (locomotion port, future
//     command). Their new tile is no longer in Bound.Contains.
//   - This helper observes the drift and runs LeaveHuddle on them.
//
// Mirror case for indoor scenes: an actor in a SceneBoundStructure
// huddle who leaves the structure (an admin teleport, a future scripted
// move, etc.) is auto-removed from the huddle. JoinHuddle's physical-
// presence invariant requires the actor to be inside the structure;
// drift out of the structure violates the invariant.
//
// Event ordering matters and differs from the explicit-leave-first case:
//
//   - Explicit MoveActor{LeaveHuddleFirst}: HuddleLeft emits BEFORE the
//     subsequent locomotion events (the leave is the cause of the move
//     being possible).
//   - Drift auto-leave: the locomotion event emits FIRST, THEN this
//     helper runs and emits HuddleLeft (the leave is caused by the new
//     position).
//
// PR 4 locomotion command handlers call this helper after every
// successful position mutation, before returning from the command Fn,
// so the auto-leave is part of the same transaction as the mutation.
// PR 4a defines the helper for PR 4 to wire in; no PR 4a command
// currently mutates position, so the helper has no in-tree callsites
// in PR 4a but is unit-tested directly.
//
// Returns the ID of the huddle the actor was auto-removed from, or an
// empty slice when no drift was detected. The helper removes the actor
// from at most one huddle per call (an actor is in at most one huddle
// by invariant), so the slice has length 0 or 1.
func checkHuddleDriftAfterPositionMutation(w *World, actorID ActorID, now time.Time) []HuddleID {
	if w == nil {
		return nil
	}
	actor, ok := w.Actors[actorID]
	if !ok {
		return nil
	}
	if actor.CurrentHuddleID == "" {
		return nil
	}

	huddle, ok := w.Huddles[actor.CurrentHuddleID]
	if !ok || huddle.ConcludedAt != nil {
		// Stale back-ref — opportunistic repair: clear the actor's
		// pointer and the matching actorsByHuddle index entry so
		// subsequent commands see consistent state. No drift to
		// report (there was no live huddle to drift out of).
		staleHuddleID := actor.CurrentHuddleID
		actor.CurrentHuddleID = ""
		if members, ok := w.actorsByHuddle[staleHuddleID]; ok {
			delete(members, actorID)
			if len(members) == 0 {
				delete(w.actorsByHuddle, staleHuddleID)
			}
		}
		return nil
	}

	// Walk the scenes observing this huddle directly off w.Scenes —
	// no intermediate map allocation. Area huddles are observed by
	// their paired area scene (1:1 invariant). Structure huddles may
	// be observed by multiple structure scenes minted at the same
	// structure over time. If ANY observing scene's bound rejects the
	// actor, auto-leave applies.
	var rejecting bool
	for _, scene := range w.Scenes {
		if scene == nil {
			continue
		}
		if _, observed := scene.Huddles[actor.CurrentHuddleID]; !observed {
			continue
		}
		if !scene.Bound.Contains(w, actor) {
			rejecting = true
			break
		}
	}
	if !rejecting {
		return nil
	}

	huddleID := actor.CurrentHuddleID
	leaveCurrentHuddle(w, actor, now)
	return []HuddleID{huddleID}
}
