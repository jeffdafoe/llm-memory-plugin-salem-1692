package sim

import (
	"testing"
	"time"
)

// npc_keeper_sleep_test.go — LLM-29. The keeper arm of the NPC auto-sleep machine:
// an off-shift home==work keeper (innkeeper / tavernkeeper, who sleeps on-premises
// by design) beds into its own staff quarters off the common floor rather than at
// the storefront counter, so it vacates the common room for huddle/scene scoping.
// The companion to the lodger arm (npc_lodger_sleep_test.go); both resolve the
// bed-down room through npcSleepRoomAt.

// keeperTavernWorld builds a tavern with a common floor + one private bedroom for
// lodgers, optionally a staff room (the keeper's quarters), plus the actors. The
// staff room is id 1 — lower than the common (2) and private (3) ids — so a wrong
// "first room in the slice" pick would be caught.
func keeperTavernWorld(withStaffRoom bool, actors ...*Actor) *World {
	w := sleepTestWorld(actors...)
	w.Settings.DawnTime = "07:00"
	w.Settings.LodgingBedtimeHour = 22
	rooms := []*Room{
		{ID: 2, StructureID: "tavern", Kind: RoomKindCommon, Name: "common"},
		{ID: 3, StructureID: "tavern", Kind: RoomKindPrivate, Name: "bedroom_1"},
	}
	if withStaffRoom {
		rooms = append(rooms, &Room{ID: 1, StructureID: "tavern", Kind: RoomKindStaff, Name: "keeper_quarters"})
	}
	w.Structures = map[StructureID]*Structure{
		"tavern": {ID: "tavern", DisplayName: "Tavern", Rooms: rooms},
	}
	return w
}

// liveInKeeper is a home==work tavernkeeper: home, work, and currently-inside are
// all the tavern, off-shift (unscheduled = always off). No lodging grant — it is
// the keeper, not a boarder.
func liveInKeeper(id ActorID) *Actor {
	return &Actor{
		ID:                id,
		Kind:              KindNPCStateful,
		HomeStructureID:   "tavern",
		WorkStructureID:   "tavern",
		InsideStructureID: "tavern",
		Needs:             map[NeedKey]int{"tiredness": 20},
	}
}

func TestKeeperStaffRoomAt(t *testing.T) {
	t.Run("home==work keeper with a staff room -> its quarters", func(t *testing.T) {
		k := liveInKeeper("john")
		w := keeperTavernWorld(true, k)
		room, ok := keeperStaffRoomAt(w, k, "tavern")
		if !ok || room != 1 {
			t.Fatalf("keeperStaffRoomAt = (%d, %v), want (1, true) — the staff quarters", room, ok)
		}
	})
	t.Run("no staff room in the structure -> none", func(t *testing.T) {
		k := liveInKeeper("john")
		w := keeperTavernWorld(false, k) // common + private only
		if room, ok := keeperStaffRoomAt(w, k, "tavern"); ok {
			t.Errorf("keeperStaffRoomAt = (%d, true), want false — no staff room to bed in", room)
		}
	})
	t.Run("actor does not work here -> none (no staff access)", func(t *testing.T) {
		visitor := liveInKeeper("john")
		visitor.WorkStructureID = "smithy" // works elsewhere; merely standing in the tavern
		w := keeperTavernWorld(true, visitor)
		if room, ok := keeperStaffRoomAt(w, visitor, "tavern"); ok {
			t.Errorf("keeperStaffRoomAt = (%d, true), want false — a non-keeper has no staff access", room)
		}
	})
}

// TestExecuteNPCSleep_Keeper_BedsIntoStaffQuarters is the headline LLM-29 fix: a
// home==work keeper bedding down vacates the common floor for its staff quarters,
// and waking clears the room scope back to common.
func TestExecuteNPCSleep_Keeper_BedsIntoStaffQuarters(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC) // small hours — the tavernkeeper's off-shift sleep
	k := liveInKeeper("john")
	w := keeperTavernWorld(true, k)

	if !executeNPCSleep(w, k, now) {
		t.Fatal("executeNPCSleep should bed the off-shift keeper")
	}
	if k.InsideRoomID != 1 {
		t.Errorf("InsideRoomID = %d, want 1 (bedded into the staff quarters, off the common floor)", k.InsideRoomID)
	}

	wakeNPC(w, k)
	if k.InsideRoomID != 0 {
		t.Errorf("InsideRoomID = %d after wake, want 0 (keeper wakes back onto the common floor)", k.InsideRoomID)
	}
}

// TestExecuteNPCSleep_Keeper_NoStaffRoom_SleepsInPlace: a homed NPC whose
// structure has no staff room (a plain cottage, or a keeper structure not yet
// given quarters) beds in place — InsideRoomID stays 0, the pre-LLM-29 behavior.
func TestExecuteNPCSleep_Keeper_NoStaffRoom_SleepsInPlace(t *testing.T) {
	now := time.Date(2026, 6, 24, 4, 0, 0, 0, time.UTC)
	k := liveInKeeper("john")
	w := keeperTavernWorld(false, k) // no staff room

	if !executeNPCSleep(w, k, now) {
		t.Fatal("executeNPCSleep should still bed the keeper (it just sleeps in place)")
	}
	if k.InsideRoomID != 0 {
		t.Errorf("InsideRoomID = %d, want 0 (no staff room — sleeps on the common floor, unchanged)", k.InsideRoomID)
	}
}

// TestNpcSleepRoomAt_LodgerPrefersPrivate: a boarder in an inn that also has a
// staff room beds into its granted PRIVATE room, never the staff quarters (it is
// not the keeper). Guards the lodger arm against the LLM-29 keeper arm — the two
// resolve through npcSleepRoomAt in lodger-first order.
func TestNpcSleepRoomAt_LodgerPrefersPrivate(t *testing.T) {
	now := time.Date(2026, 6, 24, 22, 0, 0, 0, time.UTC)
	future := now.Add(72 * time.Hour)
	l := &Actor{
		ID:                "ezekiel",
		Kind:              KindNPCStateful,
		InsideStructureID: "tavern",
		WorkStructureID:   "smithy", // works elsewhere — a boarder, not the keeper
		RoomAccess: map[RoomAccessKey]*RoomAccess{
			{RoomID: 3, Source: AccessSourceLedger}: {RoomID: 3, Source: AccessSourceLedger, Active: true, ExpiresAt: &future},
		},
	}
	w := keeperTavernWorld(true, l) // tavern has staff room id 1 + private bedroom id 3
	room, ok := npcSleepRoomAt(w, l, "tavern", now)
	if !ok || room != 3 {
		t.Fatalf("npcSleepRoomAt = (%d, %v), want (3, true) — the lodger's private bedroom, not the staff room", room, ok)
	}
}
