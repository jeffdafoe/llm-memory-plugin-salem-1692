package sim

import (
	"fmt"
	"sort"
	"time"
)

// EnterOrKnockResult is the outcome of an EnterOrKnock command. It embeds the
// MoveActorResult for the walk that was started — to the door tile when the
// actor may enter, or to a loiter slot when they may not — and adds the
// knock-specific signals the PC client renders (it reads knocked /
// knock_narration off the /pc/move response).
type EnterOrKnockResult struct {
	MoveActorResult
	// Knocked is true when the actor was turned away from an owner-only
	// structure they are not a member of and routed to the loiter slot rather
	// than through the door — the v1 ZBBS-101 knock. The service huddle forms
	// on ARRIVAL at the slot, not now (ZBBS-HOME-445) — the client polls its
	// huddle state to open the talk panel once the door is answered.
	Knocked bool
	// KnockNarration is a short line rendered in the talk panel when the
	// knock looks like it will go unanswered (no receiver inside at click
	// time). Empty when the actor entered normally or a receiver is in —
	// there the arrival-time hospitality greet is the feedback.
	KnockNarration string
}

// EnterOrKnock resolves a deliberate "go to this structure" request the way v1's
// handlePCMove did (ZBBS-101): it is the PC-facing wrapper over MoveActor that
// turns an owner-only rejection into a knock instead of a hard error.
//
//   - The actor MAY enter (open policy, or owner-only and a member) and the
//     structure has a door → walk to the door tile (StructureEnter); the inside
//     flip happens on arrival exactly as a bare StructureEnter would.
//
//   - Otherwise → walk to a loiter slot (StructureVisit, which has no membership
//     gate). When the rejection was specifically the owner-only membership gate
//     this is a KNOCK: the visit destination is stamped Knock=true, and ON
//     ARRIVAL at the slot the knock-arrival subscriber (cascade/business_arrival
//     → EnsureKnockServiceHuddle) pulls the knocker and any associated receiver
//     inside into the structure's huddle, so the talk panel opens and pay/speak
//     work across the doorway. The knocker stays physically outside (no inside
//     flip) — the huddle is conversational scope, not presence.
//
//     ZBBS-HOME-445: the join used to happen HERE, at click time, "so the talk
//     panel opens immediately." That membership never survived: the locomotion
//     ticker's mover-leave rule (ZBBS-HOME-340) evicted the walking knocker on
//     the next tick, and the businessowner farewell cascade read the eviction
//     as a customer departure — the keeper said "Until next time" to a customer
//     still walking IN, and the knocker arrived huddle-less with the keeper
//     stranded in a one-member huddle. Joining on arrival removes the mid-walk
//     membership entirely; the speak gate was already pacing speech to arrival,
//     so click-time membership bought nothing real.
//
// leaveHuddleFirst is threaded to the underlying move; PC click-moves pass true
// so a deliberate navigation ends any current conversation (v1's service-huddle
// cleanup) and a PC already in a huddle can move at all.
//
// MUST be called from inside a Command.Fn. It composes MoveActor by invoking
// its Fn inline against the same world — every emitted event stays under the
// caller command's causal root.
func EnterOrKnock(actorID ActorID, structureID StructureID, leaveHuddleFirst bool, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return EnterOrKnockResult{}, fmt.Errorf("actor %q not found", actorID)
			}
			if _, ok := w.Structures[structureID]; !ok {
				return EnterOrKnockResult{}, fmt.Errorf("structure %q not found", structureID)
			}
			vobj, _, ok := villageObjectForStructure(w, structureID)
			if !ok {
				return EnterOrKnockResult{}, fmt.Errorf("structure %q has no placement", structureID)
			}

			// Whether this actor may walk through the door, mirroring
			// MoveActor's StructureEnter validation: closed structures have no
			// interior; owner-only structures admit only members; and a door
			// tile must resolve. Anything short of that routes to a loiter slot.
			member := structureMembershipAllows(w, actor, structureID, now)
			policyAllowsEnter := vobj.EntryPolicy != EntryPolicyClosed &&
				(vobj.EntryPolicy != EntryPolicyOwner || member)
			_, hasDoor := structureEntryTile(w, structureID)
			canEnter := policyAllowsEnter && hasDoor

			// A knock is specifically the owner-only-non-member case where a door
			// exists: a door the actor can't walk through. A closed or doorless
			// structure also routes to the loiter slot, but that is a plain visit
			// (stand beside a well), not a knock — no service huddle, no "the door
			// is shut" narration. hasDoor is required so a doorless owner-only
			// structure is a visit, matching the comment above and v1.
			knocked := vobj.EntryPolicy == EntryPolicyOwner && !member && hasDoor

			var dest MoveDestination
			if canEnter {
				dest = NewStructureEnterDestination(structureID)
			} else {
				dest = NewStructureVisitDestination(structureID)
				// The arrival forms the service huddle, not this command —
				// see the doc comment (ZBBS-HOME-445).
				dest.Knock = knocked
			}

			raw, err := MoveActor(actorID, dest, leaveHuddleFirst, now).Fn(w)
			if err != nil {
				return EnterOrKnockResult{}, err
			}
			moveRes, ok := raw.(MoveActorResult)
			if !ok {
				return EnterOrKnockResult{}, fmt.Errorf("EnterOrKnock: unexpected move result type %T", raw)
			}

			out := EnterOrKnockResult{MoveActorResult: moveRes, Knocked: knocked}
			if !knocked {
				return out, nil
			}

			// Click-time narration covers only the unanswered-looking case: no
			// receptive receiver inside right now, so the player learns why
			// the click will probably go nowhere. Predictive by nature — a
			// receiver who returns home (or wakes) mid-walk still answers on
			// arrival (EnsureKnockServiceHuddle re-checks live state). When
			// someone IS in, say nothing here: the arrival-time hospitality
			// greet is the "door answered" feedback, and a click-time "you are
			// bid to wait" would be the same premature speech this ticket
			// removed.
			if len(receptiveKnockReceivers(w, structureID)) == 0 {
				out.KnockNarration = "You knock, but the door is shut fast and no one answers."
			}
			return out, nil
		},
	}
}

// insideAssociatedActors returns the ids of actors physically inside structureID
// who are also associated with it as resident or staff (their home or work
// anchor is this structure) — the keeper(s) a knocker is trying to reach.
// Sorted for a deterministic huddle-join order and reproducible tests.
func insideAssociatedActors(w *World, structureID StructureID) []ActorID {
	var ids []ActorID
	for id, a := range w.Actors {
		if a.InsideStructureID == structureID &&
			(a.HomeStructureID == structureID || a.WorkStructureID == structureID) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
