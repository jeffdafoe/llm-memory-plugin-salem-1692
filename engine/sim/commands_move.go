package sim

import (
	"fmt"
	"time"
)

// commands_move.go — PR 4 locomotion commands.
//
// MoveActor accepts (or rejects) a movement request: it validates the
// destination and that a path exists, optionally leaves the actor's
// current huddle, supersedes any in-flight intent, and stamps a fresh
// MoveIntent. It does NOT move the actor — the locomotion ticker
// (locomotion_ticker.go) advances the actor tile by tile against the
// MoveIntent this command stamps.
//
// resolvePathTarget — the destination-kind-aware "where is the goal
// tile" resolver — also lives here because MoveActor is its first
// consumer; the ticker re-uses it every tick.

// MoveActorResult is the payload returned by an accepted MoveActor
// command. Rejections come back as a non-nil error instead (per the
// CommandResult contract — command-level state validation failures are
// errors).
type MoveActorResult struct {
	// MovementAttemptID is the fresh per-actor generation stamped on the
	// new MoveIntent.
	MovementAttemptID MovementAttemptID

	// SupersededAttemptID is the attempt ID of the prior in-flight
	// MoveIntent this command replaced, or 0 when the actor had no
	// in-flight movement. The superseded attempt dies WITHOUT an
	// ActorMoveStopped event — the new MoveActor is the observable
	// transition (see ActorMoveStopped's doc-comment).
	SupersededAttemptID MovementAttemptID

	// LeftHuddleID is the huddle the actor left because LeaveHuddleFirst
	// was set, or "" when the actor was not in an active huddle.
	LeftHuddleID HuddleID
}

// MoveActor returns a Command that requests actorID walk to dest.
//
// Transaction sequence (all-or-nothing — any rejection leaves world
// state untouched):
//
//  1. Actor must exist.
//  2. Destination must be valid for its kind: StructureEnter / Visit
//     require an existing structure; StructureEnter additionally
//     requires an entry policy that is not "closed" (wells, fountains
//     and decoratives have no interior). Position requires a non-nil
//     coordinate.
//  3. If the actor is in an ACTIVE huddle, LeaveHuddleFirst must be set
//     or the command is rejected. This gate is checked BEFORE path
//     validation so it does not depend on the destination being
//     reachable — but the actual huddle leave is deferred to step 5.
//  4. A path must exist from the actor's current tile to the
//     destination's resolved target tile (door tile / visitor slot /
//     exact tile). No path → reject; no MoveIntent is stamped.
//  5. The huddle transition decided in step 3 is applied: an active
//     huddle is left (emitting HuddleLeft, and HuddleConcluded if the
//     leaver was the last member); a stale back-ref (huddle missing or
//     already concluded) is just cleared. Everything from here down
//     cannot fail.
//  6. Any existing MoveIntent is silently superseded.
//  7. A fresh per-actor MovementAttemptID is generated and a new
//     MoveIntent stamped.
//  8. An ActorMoveStarted is emitted carrying the resolved goal tile, so
//     the client read surface can begin animating the walk.
//
// MoveActor does not MOVE the actor — that is the locomotion ticker's job
// (it advances tile by tile against the stamped MoveIntent). The events it
// emits are the huddle-leave events from step 5 and the ActorMoveStarted
// from step 8.
func MoveActor(actorID ActorID, dest MoveDestination, leaveHuddleFirst bool, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return MoveActorResult{}, fmt.Errorf("actor %q not found", actorID)
			}

			// Step 2 — destination validity. The explicit checks here
			// produce specific error messages; resolvePathTarget below is
			// the canonical resolver and re-checks the same invariants.
			switch dest.Kind {
			case MoveDestinationStructureEnter:
				if dest.StructureID == nil {
					return MoveActorResult{}, fmt.Errorf("structure_enter destination missing StructureID")
				}
				if _, ok := w.Structures[*dest.StructureID]; !ok {
					return MoveActorResult{}, fmt.Errorf("structure %q not found", *dest.StructureID)
				}
				vobj, _, ok := villageObjectForStructure(w, *dest.StructureID)
				if !ok {
					return MoveActorResult{}, fmt.Errorf("structure %q has no placement to enter", *dest.StructureID)
				}
				policy := effectiveEntryPolicy(w, *dest.StructureID, vobj, now)
				if policy == EntryPolicyClosed {
					return MoveActorResult{}, fmt.Errorf("structure %q entry policy is closed", *dest.StructureID)
				}
				if policy == EntryPolicyOwner &&
					!structureMembershipAllows(w, actor, *dest.StructureID, now) {
					return MoveActorResult{}, fmt.Errorf(
						"structure %q is members-only; actor %q is not a member", *dest.StructureID, actorID)
				}
				// A non-closed structure still needs a door tile to be
				// enterable — structureEntryTile resolves the asset's door
				// offset, and StructureEnter has no meaning without one.
				// Catch it here so the step-2 contract ("destination is
				// valid for its kind") actually holds; otherwise
				// resolvePathTarget rejects later with a generic
				// "cannot be resolved" error that hides the real cause.
				// A doorless structure should be targeted with
				// StructureVisit instead.
				if _, ok := structureEntryTile(w, *dest.StructureID); !ok {
					return MoveActorResult{}, fmt.Errorf("structure %q has no door to enter", *dest.StructureID)
				}
			case MoveDestinationStructureVisit:
				if dest.StructureID == nil {
					return MoveActorResult{}, fmt.Errorf("structure_visit destination missing StructureID")
				}
				if _, ok := w.Structures[*dest.StructureID]; !ok {
					return MoveActorResult{}, fmt.Errorf("structure %q not found", *dest.StructureID)
				}
				// No entry-policy check — standing at a visitor slot is
				// always allowed, even for closed structures (a well).
			case MoveDestinationObjectVisit:
				if dest.ObjectID == nil {
					return MoveActorResult{}, fmt.Errorf("object_visit destination missing ObjectID")
				}
				vobj, ok := w.VillageObjects[*dest.ObjectID]
				if !ok || vobj == nil {
					return MoveActorResult{}, fmt.Errorf("village object %q not found", *dest.ObjectID)
				}
				if _, ok := w.Assets[vobj.AssetID]; !ok {
					return MoveActorResult{}, fmt.Errorf("village object %q has no usable placement", *dest.ObjectID)
				}
				// No entry-policy check — standing at a visitor slot beside an
				// object is always allowed (the slot is outside any footprint).
			case MoveDestinationPosition:
				if dest.Position == nil {
					return MoveActorResult{}, fmt.Errorf("position destination missing Position")
				}
			default:
				return MoveActorResult{}, fmt.Errorf("unknown move destination kind %q", dest.Kind)
			}

			// Step 3 — huddle gate (validation only — the actual leave is
			// deferred to the mutation phase below). An actor in an ACTIVE
			// huddle can only move with LeaveHuddleFirst set, and that
			// check must NOT depend on the destination being reachable, so
			// it runs before path validation. A stale back-ref (huddle
			// missing or already concluded) is not a real active huddle —
			// it needs no LeaveHuddleFirst and is just cleared in step 5.
			activeHuddleID := HuddleID("")
			staleHuddleBackRef := false
			if actor.CurrentHuddleID != "" {
				if h, ok := w.Huddles[actor.CurrentHuddleID]; ok && h.ConcludedAt == nil {
					if !leaveHuddleFirst {
						return MoveActorResult{}, fmt.Errorf(
							"actor %q is in active huddle %q; set LeaveHuddleFirst to move",
							actorID, actor.CurrentHuddleID)
					}
					activeHuddleID = actor.CurrentHuddleID
				} else {
					staleHuddleBackRef = true
				}
			}

			// Step 4 — path existence. resolvePathTarget turns the
			// destination into a concrete goal tile; FindPath confirms the
			// actor can actually reach it from where they stand now.
			grid, err := buildWalkGrid(w)
			if err != nil {
				return MoveActorResult{}, fmt.Errorf("build walk grid: %w", err)
			}
			target, ok := resolvePathTarget(w, actor, dest, grid, now)
			if !ok {
				return MoveActorResult{}, fmt.Errorf("destination cannot be resolved to a reachable tile")
			}
			start := actor.Pos
			path := FindPath(grid, start, GridPoint{X: target.X, Y: target.Y})
			if path == nil {
				return MoveActorResult{}, fmt.Errorf("no path from (%d,%d) to target (%d,%d)",
					actor.Pos.X, actor.Pos.Y, target.X, target.Y)
			}

			// Step 5 — apply the huddle transition decided in step 3.
			// Everything above this point is validation only; from here
			// down nothing can fail.
			var leftHuddleID HuddleID
			if activeHuddleID != "" {
				leftHuddleID = activeHuddleID
				leaveCurrentHuddle(w, actor, now)
			} else if staleHuddleBackRef {
				// Stale back-ref — clear the dangling pointer; nothing to leave.
				actor.CurrentHuddleID = ""
			}

			// Step 5.5 — committed movement implies awake (ZBBS-HOME-435:
			// "you can't be asleep and use move_to"). An agent NPC can reach
			// here bedded when an auto-sleep stamp races its in-flight tick
			// (live 2026-06-11: the sweep bedded Prudence Ward 8s after her
			// tick was admitted; the tick then committed move_to and she
			// walked to the tavern flagged asleep, where the reactor rest
			// gate silently ate every warrant for 12h). Getting up and
			// walking IS waking — clear the rest window exactly like the
			// shift-start wake. After the validation gates so a failed move
			// leaves the sleeper undisturbed. PCs have their own input-wake
			// on the /pc routes; decoratives never receive MoveActor.
			if isAgentNPC(actor) && actor.SleepingUntil != nil {
				wakeNPC(w, actor)
			}

			// LLM-69: land a finished-but-not-yet-swept window FIRST, so a move in
			// the ~1s gap between the window expiring and the completion sweep
			// commits the pick/bite (mint + completion beat) instead of discarding
			// it as an abandon below. completeIfDue applies + clears only an EXPIRED
			// window (re-resolving off the still-current tile — the move hasn't
			// changed Pos yet, it only stamps MoveIntent); a genuinely in-flight one
			// is left for the cancel. Covers the PC path too (PCs aren't reactor-
			// shelved, so they can reach a move in that gap). Uses the command's
			// `now` (not wall clock) so a sim-time / deterministic-test caller lands
			// the same completions the rest of MoveActor keys off.
			completeIfDue(w, actor.ID, actor, now)
			// LLM-54: committing a move ABANDONS any STILL-in-flight eat/drink/
			// harvest at a source — the actor got up and walked off, so the bite/
			// pick never lands (the effect applies only at completion, never mid-
			// window, so there is nothing to roll back). Cleared for PC and NPC
			// alike, here after the validation gates so a rejected move leaves a
			// running activity undisturbed. Emit the abandon so a surfacing
			// consumer (LLM-56 PC HUD) gets a terminal event for a start it saw.
			if actor.SourceActivity != nil {
				cancelled := actor.SourceActivity
				actor.SourceActivity = nil
				w.emit(&SourceActivityCancelled{
					ActorID:  actor.ID,
					ObjectID: cancelled.ObjectID,
					Kind:     cancelled.Kind,
					At:       time.Now().UTC(),
				})
			}

			// Step 6 — supersede any in-flight intent. The old attempt
			// dies silently; no ActorMoveStopped is emitted.
			var superseded MovementAttemptID
			if actor.MoveIntent != nil {
				superseded = actor.MoveIntent.AttemptID
			}

			// Step 7 — stamp the fresh intent. cloneMoveDestination so
			// MoveIntent owns its pointer fields rather than aliasing the
			// caller's MoveDestination value.
			actor.MoveAttemptCounter++
			attemptID := actor.MoveAttemptCounter
			actor.MoveIntent = &MoveIntent{
				Destination:   cloneMoveDestination(dest),
				AttemptID:     attemptID,
				BestRemaining: -1, // unset — first tick stamps the real distance
			}

			// Step 8 - announce the walk to the client read surface. Carries
			// the full cost-weighted tile path (computed as the step-4
			// reachability check, reused here) so the viewer renders the
			// engine's road-preferring, building-avoiding route rather than a
			// locally re-derived one. path[0] is the actor's current tile (the
			// walk start); path[len-1] is the resolved goal. dest.StructureID
			// is non-nil for the enter/visit kinds, dest.ObjectID for
			// object_visit, and both nil for position (empty ids).
			destStructureID := StructureID("")
			if dest.StructureID != nil {
				destStructureID = *dest.StructureID
			}
			destObjectID := VillageObjectID("")
			if dest.ObjectID != nil {
				destObjectID = *dest.ObjectID
			}
			w.emit(&ActorMoveStarted{
				ActorID:           actorID,
				FromPosition:      actor.Pos,
				TargetPosition:    target,
				Path:              path,
				DestinationKind:   dest.Kind,
				StructureID:       destStructureID,
				ObjectID:          destObjectID,
				MovementAttemptID: attemptID,
				At:                now,
			})

			return MoveActorResult{
				MovementAttemptID:   attemptID,
				SupersededAttemptID: superseded,
				LeftHuddleID:        leftHuddleID,
			}, nil
		},
	}
}

// resolvePathTarget resolves a MoveDestination to the concrete tile a
// pathfinder should aim for:
//
//   - StructureEnter → the structure's door tile (structureEntryTile),
//     provided the structure still exists, is not closed, and — for an
//     owner-only structure — the actor is still a member. Re-checked
//     every tick, so an actor who loses membership mid-walk (locked
//     out) has the move invalidated.
//   - StructureVisit → a visitor slot around the loiter pin
//     (pickVisitorSlot — re-resolved each call, so a slot freeing up or
//     filling up mid-walk is picked up on the next tick).
//   - Position       → the exact tile, provided it is traversable.
//
// ok=false means the destination is no longer valid: the structure was
// removed, its entry policy went closed, the actor is no longer a member
// of an owner-only structure, it has no reachable door, or the target
// position is untraversable. Callers treat that as a hard reject
// (MoveActor) or a movement invalidation (the ticker).
//
// now is the expiry clock threaded through to structureMembershipAllows.
//
// MUST be called from inside a Command.Fn (reads world maps + builds on
// the supplied WalkGrid). Unexported by design.
func resolvePathTarget(w *World, actor *Actor, dest MoveDestination, grid *WalkGrid, now time.Time) (Position, bool) {
	switch dest.Kind {
	case MoveDestinationStructureEnter:
		if dest.StructureID == nil {
			return Position{}, false
		}
		if _, ok := w.Structures[*dest.StructureID]; !ok {
			return Position{}, false
		}
		vobj, _, ok := villageObjectForStructure(w, *dest.StructureID)
		if !ok {
			return Position{}, false
		}
		policy := effectiveEntryPolicy(w, *dest.StructureID, vobj, now)
		if policy == EntryPolicyClosed {
			return Position{}, false
		}
		if policy == EntryPolicyOwner &&
			!structureMembershipAllows(w, actor, *dest.StructureID, now) {
			return Position{}, false
		}
		return structureEntryTile(w, *dest.StructureID)
	case MoveDestinationStructureVisit:
		if dest.StructureID == nil {
			return Position{}, false
		}
		if _, ok := w.Structures[*dest.StructureID]; !ok {
			return Position{}, false
		}
		return pickVisitorSlot(w, *dest.StructureID, actor, grid)
	case MoveDestinationObjectVisit:
		if dest.ObjectID == nil {
			return Position{}, false
		}
		// pickObjectVisitorSlot internally validates the object + asset
		// exist; a missing object or all-blocked slot ring returns ok=false
		// and the caller treats it as an invalidated destination (mover
		// hard-stops with MoveStoppedInvalidated, matching the structure
		// path's behavior when its structure is removed mid-walk).
		return pickObjectVisitorSlot(w, *dest.ObjectID, actor, grid)
	case MoveDestinationPosition:
		if dest.Position == nil {
			return Position{}, false
		}
		if !grid.CanWalk(dest.Position.X, dest.Position.Y) {
			return Position{}, false
		}
		return *dest.Position, true
	default:
		return Position{}, false
	}
}

// structureMembershipAllows reports whether actor is a MEMBER of
// structureID — the access check behind EntryPolicyOwner ("owner-only").
// Membership is the union of four sources:
//
//   - Resident — the actor's HomeStructureID is this structure. A whole
//     family shares one HomeStructureID, so a household enters its own
//     home without anyone co-owning it.
//   - Staff    — the actor's WorkStructureID is this structure (the hired
//     employee who works here but neither owns nor lives here).
//   - Owner    — the structure's OwnerActorID is this actor.
//   - Lodger   — the actor holds an active, unexpired ledger RoomAccess
//     grant for a room inside this structure (actorIsLodgerAt).
//   - Hired    — the actor holds a live labor job (EnRoute / Working) whose
//     employer's work structure is this one (workerHiredAt, LLM-229): a worker
//     taken on to help here is staff for the window and may enter to do the
//     job, the same way the permanent Staff leg admits a regular employee. The
//     grant evaporates when the offer settles or the sweep voids it.
//
// now is the expiry clock for the lodger leg — caller-controlled to keep
// the check deterministic in tests, matching canEnterRoom's pattern.
//
// Future: an explicit per-actor grant/deny source can join this union to
// support revocable access (an owner locking a resident out mid-argument).
// The resident leg today is derived from HomeStructureID and is not
// itself per-actor revocable — that mechanic is a later layer.
//
// MUST be called from inside a Command.Fn (reads world maps). Unexported
// by design.
func structureMembershipAllows(w *World, actor *Actor, structureID StructureID, now time.Time) bool {
	if actor == nil || structureID == "" {
		return false
	}
	if actor.HomeStructureID == structureID || actor.WorkStructureID == structureID {
		return true
	}
	if vobj, _, ok := villageObjectForStructure(w, structureID); ok {
		if vobj.OwnerActorID != "" && vobj.OwnerActorID == actor.ID {
			return true
		}
	}
	// Hired-for-the-window (LLM-229): a worker on a live labor job at this
	// establishment enters to do it, even when the shop is owner-only.
	if workerHiredAt(w, actor.ID, structureID) {
		return true
	}
	return actorIsLodgerAt(w, actor, structureID, now)
}

// outdoorEncounterOriginKind is the Scene.OriginKind stamped on the
// area-bound scenes StartOutdoorHuddle mints.
const outdoorEncounterOriginKind = "outdoor_encounter"

// StartOutdoorHuddleResult is the payload returned by an accepted
// StartOutdoorHuddle command.
type StartOutdoorHuddleResult struct {
	SceneID      SceneID
	HuddleID     HuddleID
	Participants []ActorID
}

// StartOutdoorHuddle returns a Command that atomically mints an outdoor
// (area-bound) Scene + Huddle and joins every participant in one
// transaction — the multi-participant analog of the indoor
// CreateScene + per-actor JoinHuddle flow. Doing it as a single command
// is what preserves the atomic-encounter invariant: no participant takes
// another locomotion step between "encounter detected" and "everyone is
// in the huddle" (the two-walking-XPCs case).
//
// radius <= 0 falls back to WorldSettings.DefaultOutdoorSceneRadius, and
// then to DefaultOutdoorSceneRadiusValue for an unconfigured test world.
//
// reason, when non-nil, overrides the default warrant TRIGGER / FORCE /
// REASON applied to every participant; when nil, each participant gets a
// default WarrantKindHuddleJoined warrant triggered by themselves. Source
// lineage fields on the per-participant warrant (SourceEventID,
// RootEventID, SourceActorID, HuddleID, SceneID, OccurredAt) are ALWAYS
// drawn from the participant's HuddleJoined event regardless of reason —
// PR 3a's zero-lineage invariant rules out partial lineage. A caller that
// passes pre-filled source fields in reason has them overwritten here.
//
// Transaction sequence (all-or-nothing — every check runs before any
// mutation, so the mutation phase below cannot fail):
//
//  1. participants is non-empty and free of duplicates.
//  2. Every participant exists, is NOT already in an active huddle, and
//     satisfies the area bound (outdoors AND within radius of anchor).
//  3. Mint the area-bound Scene — captures every actor in the bound into
//     ParticipantStateAtOrigin (a superset of participants) and emits
//     SceneMinted.
//  4. Mint the Huddle (StructureID empty — outdoor) and attach it to the
//     scene (area scenes are 1:1 with their huddle).
//  5. Join every participant: set CurrentHuddleID + the actorsByHuddle
//     index, stamp the warrant, emit HuddleJoined plus the pairwise
//     ActorMet events.
//
// Teardown needs nothing special here: when the last participant later
// leaves, the existing leaveCurrentHuddle path detaches the huddle and
// auto-concludes the now-orphaned area scene (the PR 4a invariant).
func StartOutdoorHuddle(participants []ActorID, anchor Position, radius int, reason *WarrantMeta, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			// ---- validation phase — no world mutation until every check
			//      below has passed ----
			if len(participants) == 0 {
				return StartOutdoorHuddleResult{}, fmt.Errorf("no participants")
			}
			seen := make(map[ActorID]struct{}, len(participants))
			for _, id := range participants {
				if _, dup := seen[id]; dup {
					return StartOutdoorHuddleResult{}, fmt.Errorf("duplicate participant %q", id)
				}
				seen[id] = struct{}{}
			}

			if radius <= 0 {
				radius = w.Settings.DefaultOutdoorSceneRadius
			}
			if radius <= 0 {
				radius = DefaultOutdoorSceneRadiusValue
			}
			bound := NewAreaBound(anchor, radius)

			for _, id := range participants {
				actor, ok := w.Actors[id]
				if !ok {
					return StartOutdoorHuddleResult{}, fmt.Errorf("actor %q not found", id)
				}
				if actorInActiveHuddle(w, actor) {
					return StartOutdoorHuddleResult{}, fmt.Errorf(
						"actor %q is already in an active huddle", id)
				}
				if !bound.Contains(w, actor) {
					return StartOutdoorHuddleResult{}, fmt.Errorf(
						"actor %q is not outdoors within radius %d of the anchor", id, radius)
				}
			}

			// ---- mutation phase — every step below cannot fail given the
			//      validation above ----
			sceneAny, err := CreateScene(outdoorEncounterOriginKind, bound, now).Fn(w)
			if err != nil {
				return StartOutdoorHuddleResult{}, fmt.Errorf("mint outdoor scene: %w", err)
			}
			sceneID := sceneAny.(SceneID)
			scene := w.Scenes[sceneID]

			huddleID := HuddleID(newHuddleID())
			w.Huddles[huddleID] = &Huddle{
				ID:          huddleID,
				Members:     make(map[ActorID]struct{}, len(participants)),
				StructureID: "", // outdoor — no structure
				StartedAt:   now,
				// An outdoor huddle is 1:1 with its area scene and carries nothing
				// forward (writeConversationCarryover no-ops without a structure), so
				// its conversation clock always starts here.
				ConversationSince: now,
				// LLM-159: forming the huddle is a membership change = progress, so
				// the loop sweep doesn't treat a brand-new outdoor huddle as a
				// pre-existing stuck loop. (LastActivityAt stays zero here — the
				// silence sweep falls back to StartedAt, same as before.)
				LastProgressAt: now,
			}
			huddle := w.Huddles[huddleID]
			if err := attachHuddleToScene(scene, huddleID); err != nil {
				// Unreachable: scene was just minted by CreateScene above —
				// it is non-nil, area-bound, and has zero huddles, so the
				// 1:1 attach cannot reject. A failure here is a broken
				// invariant, not a runtime condition. Panicking (rather
				// than returning a command error) is what keeps the
				// mutation phase genuinely all-or-nothing: SceneMinted has
				// already been emitted and the Huddle inserted, so there is
				// no honest error return left from this point — see
				// randomHex for the same panic-on-invariant-violation
				// pattern.
				panic("sim: attachHuddleToScene rejected a freshly-minted area scene: " + err.Error())
			}
			if w.actorsByHuddle[huddleID] == nil {
				w.actorsByHuddle[huddleID] = make(map[ActorID]struct{}, len(participants))
			}

			for _, id := range participants {
				actor := w.Actors[id]
				// Members already in the huddle when this participant joins —
				// drives HuddleJoined.OtherMembers and the pairwise ActorMet.
				others := make([]ActorID, 0, len(huddle.Members))
				for existing := range huddle.Members {
					others = append(others, existing)
				}

				huddle.Members[id] = struct{}{}
				actor.CurrentHuddleID = huddleID
				w.actorsByHuddle[huddleID][id] = struct{}{}

				// HuddleJoined emitted FIRST so the per-participant
				// warrant carries full PR 3a source lineage from this
				// event. Each participant gets their own HuddleJoined
				// EventID — distinct SourceEventIDs across participants
				// keep dedup precision intact.
				joinedEvt := &HuddleJoined{
					ActorID:      id,
					HuddleID:     huddleID,
					SceneID:      sceneID,
					StructureID:  "",
					OtherMembers: others,
					HuddleNew:    len(others) == 0,
					At:           now,
				}
				w.emit(joinedEvt)

				// reason, if supplied, controls trigger / force / reason;
				// source lineage is ALWAYS sourced from the event we just
				// emitted (PR 3a invariant — no partial lineage). A caller
				// passing pre-filled source fields in reason would have
				// them overwritten here, which is intentional.
				meta := WarrantMeta{
					TriggerActorID: id,
					Reason:         BasicWarrantReason{K: WarrantKindHuddleJoined},
				}
				if reason != nil {
					meta.TriggerActorID = reason.TriggerActorID
					meta.Force = reason.Force
					meta.Reason = reason.Reason
				}
				meta.SourceEventID = joinedEvt.EventID()
				meta.RootEventID = joinedEvt.RootEventID()
				meta.SourceActorID = id
				meta.HuddleID = huddleID
				meta.SceneID = sceneID
				meta.OccurredAt = now
				tryStampWarrant(w, actor, meta, now)

				for _, other := range others {
					w.emit(&ActorMet{A: id, B: other, HuddleID: huddleID, At: now})
				}
			}

			return StartOutdoorHuddleResult{
				SceneID:      sceneID,
				HuddleID:     huddleID,
				Participants: append([]ActorID(nil), participants...),
			}, nil
		},
	}
}
