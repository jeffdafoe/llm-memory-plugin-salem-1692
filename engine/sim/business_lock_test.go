package sim

import (
	"strings"
	"testing"
	"time"
)

// business_lock_test.go — LLM-791 (was LLM-130's lodge lock). A walk-in business
// presents an owner-only (members-only) face while no keeper is there, so no new
// non-member wanders into an empty shop. Reuses keeperTavernWorld /
// closeupKeeper / placeInside / lodgerActor from the sibling test files (same
// package).

// lodgeWorld is keeperTavernWorld plus the placement businessLocked reads: a
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

// abedInStaffRoom puts the keeper into the LLM-29 bed-down state — sleeping,
// InsideRoomID stamped to its staff room, and present in the actorsByStructure
// index — without running the whole sleep machine. Matches what executeNPCSleep
// stamps for a home==work keeper (verified by the headline case below, which
// drives the real bed-down instead).
func abedInStaffRoom(w *World, k *Actor) {
	placeInside(w, "tavern", k.ID)
	room, _ := keeperStaffRoomAt(w, k, "tavern")
	k.InsideRoomID = room
	k.State = StateSleeping
	// Mirror executeNPCSleep: a future SleepingUntil, not just the State enum.
	// Far-future is fine — every test's `now` precedes it.
	until := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	k.SleepingUntil = &until
}

// awayKeeper is the keeper out of the house — outdoors, nowhere near its door.
func awayKeeper(id ActorID) *Actor {
	k := closeupKeeper(id)
	k.InsideStructureID = ""
	k.Pos = TilePos{X: 1, Y: 1}
	return k
}

// walkInTags is a walk-in business that also lodges — the Tavern and the Inn.
var walkInTags = []string{"business", "lodging"}

func TestBusinessLocked(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)
	locked := func(w *World) bool { return businessLocked(w, "tavern", w.VillageObjects["tavern"]) }

	t.Run("keeper abed in its staff room locks it", func(t *testing.T) {
		k := closeupKeeper("john")
		w := lodgeWorld(EntryPolicyOpen, walkInTags, k)
		placeInside(w, "tavern", "john")
		// Drive the real LLM-29 bed-down: it stamps InsideRoomID = staff room and
		// sets StateSleeping. No non-tenant inside, so no close-up timer is armed.
		if !executeNPCSleep(w, k, now) {
			t.Fatal("executeNPCSleep should bed the off-shift keeper")
		}
		if !locked(w) {
			t.Error("want locked — the keeper is abed")
		}
	})

	t.Run("awake keeper inside -> open", func(t *testing.T) {
		k := closeupKeeper("john")
		w := lodgeWorld(EntryPolicyOpen, walkInTags, k)
		placeInside(w, "tavern", "john")
		if locked(w) {
			t.Error("want open — the keeper is awake and tending the house")
		}
	})

	t.Run("keeper on a break -> open", func(t *testing.T) {
		k := closeupKeeper("john")
		k.State = StateResting
		w := lodgeWorld(EntryPolicyOpen, walkInTags, k)
		placeInside(w, "tavern", "john")
		if locked(w) {
			t.Error("want open — a take_break does not lock the door")
		}
	})

	t.Run("keeper asleep outside the staff room -> locked", func(t *testing.T) {
		k := closeupKeeper("john")
		k.State = StateSleeping
		k.InsideRoomID = 2 // common floor
		w := lodgeWorld(EntryPolicyOpen, walkInTags, k)
		placeInside(w, "tavern", "john")
		if !locked(w) {
			t.Error("want locked — an asleep keeper is not tending the place, wherever he sleeps")
		}
	})

	t.Run("keeper away -> locked", func(t *testing.T) {
		// The Mill case: Joseph is off at the well, nobody minds the place.
		k := awayKeeper("john")
		w := lodgeWorld(EntryPolicyOpen, []string{"business"}, k)
		if !locked(w) {
			t.Error("want locked — the keeper is not there")
		}
	})

	t.Run("keeper at the loiter pin -> open", func(t *testing.T) {
		k := awayKeeper("john")
		w := lodgeWorld(EntryPolicyOpen, walkInTags, k)
		z := 0
		vobj := w.VillageObjects["tavern"]
		vobj.DisplayName = "Tavern" // an unnamed placement never resolves from its pin
		vobj.LoiterOffsetX, vobj.LoiterOffsetY = &z, &z
		k.Pos = vobj.Pos.Tile() // outdoors, standing at the place's pin
		if locked(w) {
			t.Error("want open — the keeper is at the door, minding the place")
		}
	})

	t.Run("a worker with no businessowner marker keeps it open", func(t *testing.T) {
		// Lewis's Workshop: Lewis works there (WorkStructureID) but carries the
		// worker role, not businessowner. He is the place's keeper all the same.
		lewis := liveInKeeper("lewis")
		w := lodgeWorld(EntryPolicyOpen, []string{"business"}, lewis)
		placeInside(w, "tavern", "lewis")
		if locked(w) {
			t.Error("want open — whoever works the place keeps it, marker or not")
		}
	})

	t.Run("a co-keeper awake inside keeps it open", func(t *testing.T) {
		bedded := closeupKeeper("john")
		cokeeper := closeupKeeper("martha") // also works here, still awake
		w := lodgeWorld(EntryPolicyOpen, walkInTags, bedded, cokeeper)
		placeInside(w, "tavern", "john", "martha")
		abedInStaffRoom(w, bedded)
		if locked(w) {
			t.Error("want open — a co-keeper is still awake tending the floor")
		}
	})

	t.Run("a hired hand alone does not keep it open", func(t *testing.T) {
		k := awayKeeper("john")
		hand := &Actor{ID: "abraham", Kind: KindNPCStateful}
		w := lodgeWorld(EntryPolicyOpen, []string{"business"}, k, hand)
		placeInside(w, "tavern", "abraham")
		w.LaborLedger = map[LaborID]*LaborOffer{
			1: {ID: 1, WorkerID: "abraham", EmployerID: "john", State: LaborStateWorking},
		}
		if !businessTendedAt(w, "tavern") {
			t.Fatal("fixture: the hand should make the place tended")
		}
		if !locked(w) {
			t.Error("want locked — a hand cannot sell; only the proprietor opens the door")
		}
	})

	t.Run("a sleeping lodger with the keeper away -> locked", func(t *testing.T) {
		lodger := lodgerActor("ezekiel", now)
		lodger.State = StateSleeping
		lodger.InsideRoomID = 3
		w := lodgeWorld(EntryPolicyOpen, walkInTags, lodger)
		placeInside(w, "tavern", "ezekiel")
		if !locked(w) {
			t.Error("want locked — a boarder is not the keeper")
		}
	})

	t.Run("market stall never locks", func(t *testing.T) {
		k := awayKeeper("john")
		w := lodgeWorld(EntryPolicyOpen, []string{"business", "market_stall"}, k)
		if locked(w) {
			t.Error("want open — a stall stays on its static policy")
		}
	})

	t.Run("not a business never locks", func(t *testing.T) {
		// The Meeting House: a worked place with no business tag.
		k := awayKeeper("john")
		w := lodgeWorld(EntryPolicyOpen, []string{"lodging"}, k)
		if locked(w) {
			t.Error("want open — only business-tagged structures lock")
		}
	})
}

func TestEffectiveEntryPolicy(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)

	// locked builds a locked business (keeper abed) with the given static policy
	// and returns its effective policy.
	locked := func(policy EntryPolicy) EntryPolicy {
		k := closeupKeeper("john")
		w := lodgeWorld(policy, walkInTags, k)
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

	t.Run("open business with an awake keeper -> open", func(t *testing.T) {
		k := closeupKeeper("john")
		w := lodgeWorld(EntryPolicyOpen, walkInTags, k)
		placeInside(w, "tavern", "john")
		if got := effectiveEntryPolicy(w, "tavern", w.VillageObjects["tavern"], now); got != EntryPolicyOpen {
			t.Errorf("effectiveEntryPolicy = %q, want open (keeper is up)", got)
		}
	})
	t.Run("non-business passes the static policy through", func(t *testing.T) {
		k := closeupKeeper("john")
		w := lodgeWorld(EntryPolicyOpen, []string{"tavern"}, k) // no business tag
		abedInStaffRoom(w, k)
		if got := effectiveEntryPolicy(w, "tavern", w.VillageObjects["tavern"], now); got != EntryPolicyOpen {
			t.Errorf("effectiveEntryPolicy = %q, want open (not a business)", got)
		}
	})
}

// entryWorld builds a walk-in business with a non-member (stranger), a resident
// member (child via HomeStructureID), and the keeper.
func entryWorld() *World {
	k := closeupKeeper("john")
	stranger := &Actor{ID: "stranger", Kind: KindNPCStateful}
	resident := &Actor{ID: "child", Kind: KindNPCStateful, HomeStructureID: "tavern"}
	w := lodgeWorld(EntryPolicyOpen, walkInTags, k, stranger, resident)
	placeInside(w, "tavern", "john") // keeper awake inside — open until a case changes it
	return w
}

// TestMoveToCanEnter_LockedBusiness — the NPC move-to derivation site. A locked
// business turns a non-member's enter into a visit (false); a member still
// enters; an open business admits anyone.
func TestMoveToCanEnter_LockedBusiness(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)

	t.Run("non-member turned away when locked", func(t *testing.T) {
		w := entryWorld()
		abedInStaffRoom(w, w.Actors["john"])
		if moveToCanEnter(w, w.Actors["stranger"], "tavern", now) {
			t.Error("want false — a non-member cannot enter a locked business")
		}
	})
	t.Run("non-member turned away while the keeper is out", func(t *testing.T) {
		w := entryWorld()
		john := w.Actors["john"]
		delete(w.actorsByStructure["tavern"], "john")
		john.InsideStructureID = ""
		john.Pos = TilePos{X: 1, Y: 1} // outdoors, nowhere near the door
		if moveToCanEnter(w, w.Actors["stranger"], "tavern", now) {
			t.Error("want false — nobody is minding the place")
		}
		if !moveToCanEnter(w, john, "tavern", now) {
			t.Error("want true — the keeper can always come back and open up")
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
		w := entryWorld()
		if !moveToCanEnter(w, w.Actors["stranger"], "tavern", now) {
			t.Error("want true — an open business admits anyone")
		}
	})
}

// TestMoveActor_StructureEnter_LockedBusiness — the MoveActor enforcement site.
// A non-member handed a StructureEnter for a locked business is rejected with
// the members-only error (step 2, before any pathing).
func TestMoveActor_StructureEnter_LockedBusiness(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)
	w := entryWorld()
	abedInStaffRoom(w, w.Actors["john"])

	_, err := MoveActor("stranger", NewStructureEnterDestination("tavern"), true, now).Fn(w)
	if err == nil || !strings.Contains(err.Error(), "members-only") {
		t.Errorf("err = %v, want a members-only rejection for a locked business", err)
	}
}

// TestResolvePathTarget_LockedBusiness — the per-tick re-check site. A
// non-member's enter target is invalidated when the business is locked (so a
// non-member already walking when the door locks mid-walk is stopped); a
// member's target survives; an open business resolves the door tile for anyone.
// nil grid is safe — the StructureEnter branch never reads it.
func TestResolvePathTarget_LockedBusiness(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)
	dest := NewStructureEnterDestination("tavern")

	t.Run("non-member target invalidated when locked", func(t *testing.T) {
		w := entryWorld()
		abedInStaffRoom(w, w.Actors["john"])
		if _, ok := resolvePathTarget(w, w.Actors["stranger"], dest, nil, now); ok {
			t.Error("want ok=false — a non-member's enter target is invalid at a locked business")
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
		w := entryWorld()
		if _, ok := resolvePathTarget(w, w.Actors["stranger"], dest, nil, now); !ok {
			t.Error("want ok=true — an open business resolves the door tile for anyone")
		}
	})
}
