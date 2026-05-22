package sim

import (
	"fmt"
	"time"
)

// move_to.go — the move_to agent tool's substrate half, ZBBS-HOME-285.
//
// What move_to is: the agent NPC's self-directed movement verb. The model
// names a structure it can see in its perception (its own home/work, a shop,
// the meeting house) and the engine walks it there. It is the wired-tool
// surface over the EXISTING MoveActor command (commands_move.go) — move_to
// builds nothing new about locomotion; it derives the right arrival kind and
// delegates.
//
// Why it exists: v2 agent NPCs could talk and transact but had no movement
// verb (MoveActor existed but was never in registerTools). This is the agent
// half of work's shift/duty producer seam — a "head to your workplace / go
// home" duty warrant is inert without a tool to act on. Arrival fires
// ActorArrived, which is what closes the auto-sleep loop
// (handleAutoSleepOnArrival, npc_sleep.go): an off-shift NPC that move_to's
// home ENTERS, lands InsideStructureID == HomeStructureID, and is bedded.
//
// Enter-vs-visit is DERIVED here, not passed by the model — see
// moveToDestinationFor. The model-facing tool stays a single structure_id arg;
// the engine picks enter or visit the same way the PC-move client chooses a
// kind, but without making the model reason about it.
//
// Terminal: move_to ends the tick (registered terminalOnSuccess=true). A model
// that wants to announce its exit does speak FIRST ("heading to the smithy
// now", to its current companions, broadcast at the FROM location) then
// move_to to end the turn — the speak-then-move ordering v1 enforced
// (ZBBS-HOME-237), which avoids a post-move speak landing at the room the
// actor just left. Confirmed with work for the duty-warrant seam.

// MoveToStructure returns a Command that walks actorID to structureID, derives
// enter-vs-visit from the structure's entry policy, and dispatches MoveActor.
// Runs on the world goroutine.
//
// Rejections (surfaced to the model as tool errors so it can retry / pick
// another action):
//   - actor not in world.
//   - empty structure_id (defense-in-depth — the handler also checks).
//   - no such structure — the id doesn't name a structure in the world.
//   - already inside the target — a no-op walk (mirrors v1's
//     errMoveAlreadyAtDest); checked before the enter/visit decision because
//     being inside the target makes the distinction moot.
//   - already walking to the SAME structure — re-issuing an in-flight
//     destination is a no-op. Changing to a DIFFERENT destination is allowed
//     (MoveActor silently supersedes). This guard keeps a level-triggered duty
//     warrant that re-deliberates mid-walk from restamping the intent (and
//     re-emitting ActorMoveStarted) every tick.
//   - destination unreachable — surfaced by MoveActor's path check.
func MoveToStructure(actorID ActorID, structureID StructureID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, ok := w.Actors[actorID]
			if !ok {
				return MoveActorResult{}, fmt.Errorf("MoveToStructure: actor %q not in world", actorID)
			}
			if structureID == "" {
				return MoveActorResult{}, fmt.Errorf("move_to: structure_id is required")
			}
			if _, ok := w.Structures[structureID]; !ok {
				return MoveActorResult{}, fmt.Errorf(
					"there is no structure %q to walk to — use a structure_id you can see in your perception", structureID)
			}
			// A structure record with no backing VillageObject placement can't
			// be walked to: moveToCanEnter would silently fall back to a visit
			// and MoveActor's visitor-slot resolve (pickVisitorSlot reads the
			// placement's loiter pin) would then fail with a generic
			// "destination cannot be resolved". Catch the missing placement here
			// so the model gets a crisp, retry-anchored error instead. The
			// shared-identity bridge keeps these in lockstep in practice; this
			// guards partial/desynced world state (code_review, ZBBS-HOME-285).
			if _, _, ok := villageObjectForStructure(w, structureID); !ok {
				return MoveActorResult{}, fmt.Errorf(
					"structure %q has no placement in the village to walk to — pick a different structure_id from your perception", structureID)
			}
			if a.InsideStructureID == structureID {
				return MoveActorResult{}, fmt.Errorf(
					"you are already at %q — no need to move there; pick a different action this turn", structureID)
			}
			if a.MoveIntent != nil && a.MoveIntent.Destination.StructureID != nil &&
				*a.MoveIntent.Destination.StructureID == structureID {
				return MoveActorResult{}, fmt.Errorf(
					"you are already on your way to %q — keep walking; pick a different action this turn", structureID)
			}
			dest := moveToDestinationFor(w, a, structureID, now)
			// leaveHuddleFirst=true: choosing to walk somewhere ends any
			// conversation the actor is in (ZBBS-HOME-285 — matches v1's
			// move=leave, confirmed with work for the duty-warrant seam). The
			// duty warrant is level-triggered, so the model isn't yanked
			// mid-sentence: it can keep talking over ticks and leaves the
			// huddle cleanly only when it chooses to move.
			return MoveActor(actorID, dest, true, now).Fn(w)
		},
	}
}

// moveToDestinationFor derives the MoveDestination for a move_to: a
// StructureEnter when the actor can actually enter structureID, else a
// StructureVisit (walk to a visitor slot and stand outside).
//
// The enter predicate mirrors MoveActor's StructureEnter validation
// (commands_move.go) and v1's agentMoveShouldEnter (engine/agent_tick.go):
//
//   - entry policy "closed"     → never enter (wells, fountains, decoratives).
//   - entry policy "owner-only" → enter only if the actor is a member
//     (resident / staff / owner / lodger — structureMembershipAllows).
//   - no door tile              → never enter (a doorless structure has no
//     interior to stand in); visit its loiter slot instead.
//   - otherwise (open / type-default, door present) → enter.
//
// The auto-sleep loop depends on the enter case: move_to(home) must land
// InsideStructureID == HomeStructureID for handleAutoSleepOnArrival to bed an
// off-shift NPC (npc_sleep.go).
//
// MUST be called from inside a Command.Fn (reads world maps). Unexported.
func moveToDestinationFor(w *World, actor *Actor, structureID StructureID, now time.Time) MoveDestination {
	if moveToCanEnter(w, actor, structureID, now) {
		return NewStructureEnterDestination(structureID)
	}
	return NewStructureVisitDestination(structureID)
}

// moveToCanEnter reports whether actor may enter structureID's interior. The
// checks are the same invariants MoveActor re-validates on the StructureEnter
// path, so a true result here always survives MoveActor's own gate — the
// derivation never produces an enter destination MoveActor would reject for
// policy/door reasons (only reachability, which neither side can know without
// the walk grid, can still fail downstream).
//
// MUST be called from inside a Command.Fn (reads world maps). Unexported.
func moveToCanEnter(w *World, actor *Actor, structureID StructureID, now time.Time) bool {
	vobj, _, ok := villageObjectForStructure(w, structureID)
	if !ok {
		return false
	}
	if vobj.EntryPolicy == EntryPolicyClosed {
		return false
	}
	if vobj.EntryPolicy == EntryPolicyOwner && !structureMembershipAllows(w, actor, structureID, now) {
		return false
	}
	if _, ok := structureEntryTile(w, structureID); !ok {
		return false
	}
	return true
}
