package sim

import (
	"strings"
	"testing"
	"time"
)

// lodge_lock_test.go — LLM-130. The door-lock half of the establishment
// close-up: while a lodge's keeper is abed in its staff room (the LLM-29
// bed-down), the lodge presents an owner-only (members-only) face so no new
// non-member wanders in. Reuses keeperTavernWorld / closeupKeeper / placeInside
// / lodgerActor from the sibling test files (same package).

// lodgeWorld is keeperTavernWorld plus the placement lodgeLocked reads: a
// "house" asset with a door (so structureEntryTile resolves for the entry-site
// tests) and a "tavern" VillageObject carrying the given entry policy + tags.
// keeperTavernWorld(true) supplies the staff room (id 1) a keeper beds into.
func lodgeWorld(policy EntryPolicy, tags []string, actors ...*Actor) *World {
	w := keeperTavernWorld(true, actors...)
	dx, dy := 0, 2
	w.Assets = map[AssetID]*Asset{
		"house": {ID: "house", Category: "structure", DoorOffsetX: &dx, DoorOffsetY: &dy},
	}
	w.VillageObjects = map[VillageObjectID]*VillageObject{
		"tavern": {ID: "tavern", AssetID: "house", Pos: WorldPos{X: 320, Y: 320}, Tags: tags, EntryPolicy: policy},
	}
	return w
}

// abedInStaffRoom puts the keeper into the LLM-29 bed-down state lodgeLocked keys
// off — sleeping, InsideRoomID stamped to its staff room, and present in the
// actorsByStructure index — without running the whole sleep machine. Matches what
// executeNPCSleep stamps for a home==work keeper (verified by the headline case
// below, which drives the real bed-down instead).
func abedInStaffRoom(w *World, k *Actor) {
	placeInside(w, "tavern", k.ID)
	room, _ := keeperStaffRoomAt(w, k, "tavern")
	k.InsideRoomID = room
	k.State = StateSleeping
	// Mirror executeNPCSleep: a future SleepingUntil, not just the State enum, so
	// actorIsResting (and thus the establishmentHasAwakeKeeperPresent gate in
	// lodgeLocked) reads the keeper as abed rather than awake on the floor.
	// Far-future is fine — every test's `now` precedes it.
	until := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	k.SleepingUntil = &until
}

func TestLodgeLocked(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)

	t.Run("keeper abed in its staff room locks the lodge", func(t *testing.T) {
		k := closeupKeeper("john")
		w := lodgeWorld(EntryPolicyOpen, []string{"lodging"}, k)
		placeInside(w, "tavern", "john")
		// Drive the real LLM-29 bed-down: it stamps InsideRoomID = staff room and
		// sets StateSleeping. No non-tenant inside, so no close-up timer is armed.
		if !executeNPCSleep(w, k, now) {
			t.Fatal("executeNPCSleep should bed the off-shift keeper")
		}
		if !lodgeLocked(w, "tavern", now) {
			t.Error("want locked — the keeper is abed in its staff room")
		}
	})

	t.Run("awake keeper on the floor -> open", func(t *testing.T) {
		k := closeupKeeper("john") // no sleep stamp
		w := lodgeWorld(EntryPolicyOpen, []string{"lodging"}, k)
		placeInside(w, "tavern", "john")
		if lodgeLocked(w, "tavern", now) {
			t.Error("want open — the keeper is awake and tending the house")
		}
	})

	t.Run("keeper resting in place (take_break) -> open", func(t *testing.T) {
		k := closeupKeeper("john")
		k.State = StateResting // a break rests in place — no staff-room relocation
		w := lodgeWorld(EntryPolicyOpen, []string{"lodging"}, k)
		placeInside(w, "tavern", "john")
		if lodgeLocked(w, "tavern", now) {
			t.Error("want open — a take_break does not lock the door")
		}
	})

	t.Run("keeper asleep but not in a staff room -> open", func(t *testing.T) {
		k := closeupKeeper("john")
		k.State = StateSleeping
		k.InsideRoomID = 2 // common floor, not the staff quarters
		w := lodgeWorld(EntryPolicyOpen, []string{"lodging"}, k)
		placeInside(w, "tavern", "john")
		if lodgeLocked(w, "tavern", now) {
			t.Error("want open — the lock keys off the staff-room bed-down, not bare sleep")
		}
	})

	t.Run("a co-keeper awake on the floor keeps it open", func(t *testing.T) {
		// Mirrors the close-up's co-keeper gate: one keeper abed does not lock the
		// lodge while another keeper is still up tending it.
		bedded := closeupKeeper("john")
		cokeeper := closeupKeeper("martha") // also works the lodge, still awake
		w := lodgeWorld(EntryPolicyOpen, []string{"lodging"}, bedded, cokeeper)
		placeInside(w, "tavern", "john", "martha")
		abedInStaffRoom(w, bedded)
		if lodgeLocked(w, "tavern", now) {
			t.Error("want open — a co-keeper is still awake tending the floor")
		}
	})

	t.Run("not a lodge -> never locks", func(t *testing.T) {
		k := closeupKeeper("john")
		w := lodgeWorld(EntryPolicyOpen, []string{"tavern"}, k) // no "lodging" tag
		abedInStaffRoom(w, k)
		if lodgeLocked(w, "tavern", now) {
			t.Error("want open — only lodging-tagged structures lock")
		}
	})

	t.Run("no keeper present -> open", func(t *testing.T) {
		// A boarder asleep in its private room is not the keeper; the lodge does
		// not lock just because someone is abed in it.
		lodger := lodgerActor("ezekiel", now)
		lodger.State = StateSleeping
		lodger.InsideRoomID = 3
		w := lodgeWorld(EntryPolicyOpen, []string{"lodging"}, lodger)
		placeInside(w, "tavern", "ezekiel")
		if lodgeLocked(w, "tavern", now) {
			t.Error("want open — a sleeping boarder does not lock the door; only the keeper does")
		}
	})
}

func TestEffectiveEntryPolicy(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)

	// locked builds a locked lodge (keeper abed) with the given static policy and
	// returns its effective policy.
	locked := func(policy EntryPolicy) EntryPolicy {
		k := closeupKeeper("john")
		w := lodgeWorld(policy, []string{"lodging"}, k)
		abedInStaffRoom(w, k)
		return effectiveEntryPolicy(w, "tavern", w.VillageObjects["tavern"], now)
	}

	t.Run("locked open -> owner-only", func(t *testing.T) {
		if got := locked(EntryPolicyOpen); got != EntryPolicyOwner {
			t.Errorf("effectiveEntryPolicy = %q, want owner-only", got)
		}
	})
	t.Run("locked type-default -> owner-only", func(t *testing.T) {
		if got := locked(EntryPolicyDefault); got != EntryPolicyOwner {
			t.Errorf("effectiveEntryPolicy = %q, want owner-only", got)
		}
	})
	t.Run("locked closed stays closed", func(t *testing.T) {
		if got := locked(EntryPolicyClosed); got != EntryPolicyClosed {
			t.Errorf("effectiveEntryPolicy = %q, want closed (lock never loosens)", got)
		}
	})
	t.Run("locked owner-only stays owner-only", func(t *testing.T) {
		if got := locked(EntryPolicyOwner); got != EntryPolicyOwner {
			t.Errorf("effectiveEntryPolicy = %q, want owner-only", got)
		}
	})

	t.Run("open lodge with an awake keeper -> open", func(t *testing.T) {
		k := closeupKeeper("john") // awake — not locked
		w := lodgeWorld(EntryPolicyOpen, []string{"lodging"}, k)
		placeInside(w, "tavern", "john")
		if got := effectiveEntryPolicy(w, "tavern", w.VillageObjects["tavern"], now); got != EntryPolicyOpen {
			t.Errorf("effectiveEntryPolicy = %q, want open (keeper is up)", got)
		}
	})
	t.Run("non-lodge passes the static policy through", func(t *testing.T) {
		// A keeper abed in a non-lodging structure does not lock it.
		k := closeupKeeper("john")
		w := lodgeWorld(EntryPolicyOpen, []string{"tavern"}, k) // no lodging tag
		abedInStaffRoom(w, k)
		if got := effectiveEntryPolicy(w, "tavern", w.VillageObjects["tavern"], now); got != EntryPolicyOpen {
			t.Errorf("effectiveEntryPolicy = %q, want open (not a lodge)", got)
		}
	})
}

// entryWorld builds a lodge with a non-member (stranger), a resident member
// (child via HomeStructureID), and the keeper.
func entryWorld() *World {
	k := closeupKeeper("john")
	stranger := &Actor{ID: "stranger", Kind: KindNPCStateful}
	resident := &Actor{ID: "child", Kind: KindNPCStateful, HomeStructureID: "tavern"}
	return lodgeWorld(EntryPolicyOpen, []string{"lodging"}, k, stranger, resident)
}

// TestMoveToCanEnter_LockedLodge — the NPC move-to derivation site. A locked
// lodge turns a non-member's enter into a visit (false); a member still enters;
// an open lodge admits anyone.
func TestMoveToCanEnter_LockedLodge(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)

	t.Run("non-member turned away when locked", func(t *testing.T) {
		w := entryWorld()
		abedInStaffRoom(w, w.Actors["john"])
		if moveToCanEnter(w, w.Actors["stranger"], "tavern", now) {
			t.Error("want false — a non-member cannot enter a locked lodge")
		}
	})
	t.Run("member still enters when locked", func(t *testing.T) {
		w := entryWorld()
		abedInStaffRoom(w, w.Actors["john"])
		if !moveToCanEnter(w, w.Actors["child"], "tavern", now) {
			t.Error("want true — a resident still walks in at night")
		}
	})
	t.Run("non-member enters while open", func(t *testing.T) {
		w := entryWorld() // keeper awake — open
		if !moveToCanEnter(w, w.Actors["stranger"], "tavern", now) {
			t.Error("want true — an open lodge admits anyone")
		}
	})
}

// TestMoveActor_StructureEnter_LockedLodge — the MoveActor enforcement site. A
// non-member handed a StructureEnter for a locked lodge is rejected with the
// members-only error (step 2, before any pathing).
func TestMoveActor_StructureEnter_LockedLodge(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)
	w := entryWorld()
	abedInStaffRoom(w, w.Actors["john"])

	_, err := MoveActor("stranger", NewStructureEnterDestination("tavern"), true, now).Fn(w)
	if err == nil || !strings.Contains(err.Error(), "members-only") {
		t.Errorf("err = %v, want a members-only rejection for a locked lodge", err)
	}
}

// TestResolvePathTarget_LockedLodge — the per-tick re-check site. A non-member's
// enter target is invalidated when the lodge is locked (so a non-member already
// walking when the door locks mid-walk is stopped); a member's target survives;
// an open lodge resolves the door tile for anyone. nil grid is safe — the
// StructureEnter branch never reads it.
func TestResolvePathTarget_LockedLodge(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)
	dest := NewStructureEnterDestination("tavern")

	t.Run("non-member target invalidated when locked", func(t *testing.T) {
		w := entryWorld()
		abedInStaffRoom(w, w.Actors["john"])
		if _, ok := resolvePathTarget(w, w.Actors["stranger"], dest, nil, now); ok {
			t.Error("want ok=false — a non-member's enter target is invalid at a locked lodge")
		}
	})
	t.Run("member target stays valid when locked", func(t *testing.T) {
		w := entryWorld()
		abedInStaffRoom(w, w.Actors["john"])
		if _, ok := resolvePathTarget(w, w.Actors["child"], dest, nil, now); !ok {
			t.Error("want ok=true — a resident's enter target survives the lock")
		}
	})
	t.Run("non-member target valid while open", func(t *testing.T) {
		w := entryWorld() // keeper awake
		if _, ok := resolvePathTarget(w, w.Actors["stranger"], dest, nil, now); !ok {
			t.Error("want ok=true — an open lodge resolves the door tile for anyone")
		}
	})
}
