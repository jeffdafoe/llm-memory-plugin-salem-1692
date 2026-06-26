package sim

import "time"

// lodgeLocked reports whether structureID is a lodge whose door is currently
// shut for the night — its own keeper has bedded down in its staff room. This
// is the door-lock half of the establishment close-up: LLM-129 turns the
// non-tenants already inside OUT when the keeper retires; this keeps new ones
// from wandering IN once the house is closed.
//
// Scope is lodging-tagged structures only (live: the Tavern and the Inn — the
// only home==work keepers that sleep on-premises). Every other establishment is
// either already owner-only or has a keeper who walks home (keeperless reads
// closed), so none of them need a lock.
//
// The signal is the LLM-29 bed-down relocation, not operating hours: at retire
// executeNPCSleep stamps the keeper's InsideRoomID to its staff room
// (keeperStaffRoomAt) and sets it sleeping; wakeNPC clears the room at shift
// start. So the lock engages the moment the keeper turns in and lifts on its own
// at dawn. A brief take_break rests the keeper in place (StateResting, no room
// relocation) and deliberately does NOT lock the door.
//
// A co-keeper still awake on the floor keeps the lodge OPEN: the lock shares the
// close-up's establishmentHasAwakeKeeperPresent "is anyone still tending it"
// predicate (LLM-129), so the lock and the eviction always agree — a lodge that
// the close-up would not have evicted from (still attended) is not locked either.
//
// MUST be called from inside a Command.Fn (reads world maps).
func lodgeLocked(w *World, structureID StructureID, now time.Time) bool {
	vobj, _, ok := villageObjectForStructure(w, structureID)
	if !ok || !vobj.HasTag("lodging") {
		return false
	}
	// A keeper of this lodge must be abed in a staff room of it.
	keeperAbed := false
	for id := range w.actorsByStructure[structureID] {
		a := w.Actors[id]
		if a == nil || !actorIsEstablishmentKeeper(a, structureID) {
			continue // not the keeper of this lodge
		}
		if a.State != StateSleeping {
			continue // present but not abed (awake / on break)
		}
		if staff, ok := keeperStaffRoomAt(w, a, structureID); ok && a.InsideRoomID == staff {
			keeperAbed = true
			break
		}
	}
	if !keeperAbed {
		return false
	}
	// ...and no other keeper is still awake tending the floor (a co-keeper keeps
	// the house open, exactly as it keeps the close-up from firing).
	return !establishmentHasAwakeKeeperPresent(w, structureID, "", now)
}

// effectiveEntryPolicy returns the entry policy in force for structureID right
// now: normally the static vobj.EntryPolicy, but owner-only while the structure
// is a locked lodge (lodgeLocked). A lodge's static policy is "open", so the
// lock tightens it to members-only for the night. A "closed" or already
// "owner-only" structure is returned unchanged — the lock only ever tightens an
// otherwise-enterable policy, never loosens one.
//
// The entry-decision sites (EnterOrKnock, moveToCanEnter, MoveActor's
// StructureEnter validation, resolvePathTarget) consult this instead of reading
// vobj.EntryPolicy directly, so a locked lodge presents one consistent
// members-only face to the PC knock path, the NPC move-to derivation, and the
// per-tick enforcement — they cannot drift out of agreement. For any structure
// that is not a locked lodge this returns the static policy verbatim, so routing
// every structure through it is a no-op for cottages, shops, and the like.
//
// MUST be called from inside a Command.Fn.
func effectiveEntryPolicy(w *World, structureID StructureID, vobj *VillageObject, now time.Time) EntryPolicy {
	p := vobj.EntryPolicy
	if p != EntryPolicyClosed && p != EntryPolicyOwner && lodgeLocked(w, structureID, now) {
		return EntryPolicyOwner
	}
	return p
}
