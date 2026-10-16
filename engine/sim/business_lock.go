package sim

import "time"

// businessLocked reports whether structureID is a walk-in business whose door is
// shut because its keeper is not there (LLM-791, Jeff's rule: "people should be
// able to go into the mill — or any other non-stall business — when the
// proprietor is there"). It keeps new non-members from wandering into an empty
// shop; the members of the place (structureMembershipAllows — the keeper,
// household, staff, hired hand, lodger, let-in guest) still walk in, so the
// keeper can always come back and open up.
//
// Scope is a business (TagBusiness) that is not a market stall (TagMarketStall):
// live, the Inn, the Tavern, the Mill and Lewis's Workshop. A stall is dealt with
// from in front and stays on its own static policy; the Meeting House carries no
// business tag and stays open (Jeff, 10-16).
//
// "There" is keeperPresentAt: a worker of the place, awake, inside it or at its
// loiter pin. A keeper on a break still counts (the place is open, just quiet);
// a keeper asleep — an innkeeper abed in the staff room, John Ellis sleeping
// until the Tavern opens at 3 PM — does not, so this also covers the night lock
// lodges had before (LLM-130). A hired hand alone does NOT keep the place open:
// keeperPresentAt answers the authority question (someone who can sell to you),
// not businessTendedAt's presence question. The keeper is whoever works the
// place (WorkStructureID), deliberately not narrowed to BusinessownerState:
// Lewis Walker keeps his Workshop with the worker role and no businessowner
// marker, so the narrower test would lock it for good.
//
// Lock only: nobody already inside is turned out when the keeper steps away. The
// bedtime turn-out is the LLM-129 close-up, unchanged.
//
// MUST be called from inside a Command.Fn (reads world maps).
func businessLocked(w *World, structureID StructureID, vobj *VillageObject) bool {
	if !vobj.HasTag(TagBusiness) || vobj.HasTag(TagMarketStall) {
		return false
	}
	return !keeperPresentAt(w, structureID)
}

// effectiveEntryPolicy returns the entry policy in force for structureID right
// now: normally the static vobj.EntryPolicy, but owner-only while the structure
// is a business with no keeper there (businessLocked). A walk-in business's
// static policy is "open", so the lock tightens it to members-only while the
// keeper is away. A "closed" or already "owner-only" structure is returned
// unchanged — the lock only ever tightens an otherwise-enterable policy, never
// loosens one.
//
// The entry-decision sites (EnterOrKnock, KnockAtDoor, LetIn, moveToCanEnter,
// MoveActor's StructureEnter validation, resolvePathTarget) consult this instead
// of reading vobj.EntryPolicy directly, so a locked business presents one
// consistent members-only face to the PC knock path, the NPC move-to derivation,
// and the per-tick enforcement — they cannot drift out of agreement. For any
// structure that is not a business this returns the static policy verbatim.
//
// MUST be called from inside a Command.Fn.
func effectiveEntryPolicy(w *World, structureID StructureID, vobj *VillageObject, _ time.Time) EntryPolicy {
	p := vobj.EntryPolicy
	if p != EntryPolicyClosed && p != EntryPolicyOwner && businessLocked(w, structureID, vobj) {
		return EntryPolicyOwner
	}
	return p
}
