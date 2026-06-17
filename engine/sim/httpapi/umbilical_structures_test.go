package httpapi

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

func TestUmbilicalStructuresFromSnapshot(t *testing.T) {
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	noon := 12 * 60 // 720 — the snapshot's village clock for on_shift
	future := now.Add(48 * time.Hour)

	snap := &sim.Snapshot{
		PublishedAt:      now,
		LocalMinuteOfDay: intPtr(noon),
		Structures: map[sim.StructureID]*sim.Structure{
			"inn": {
				ID:          "inn",
				DisplayName: "The Inn",
				Tags:        []string{"lodging", "tavern"},
				Rooms: []*sim.Room{
					{ID: 1, StructureID: "inn", Kind: sim.RoomKindCommon},
					{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate},
					{ID: 3, StructureID: "inn", Kind: sim.RoomKindPrivate},
					{ID: 4, StructureID: "inn", Kind: sim.RoomKindStaff},
				},
			},
			"well": {
				ID:          "well",
				DisplayName: "The Well",
				Tags:        []string{"water"},
			},
		},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			// On-shift keeper of the inn (09:00–17:00 covers noon).
			"hannah": {
				DisplayName:      "Hannah Boggs",
				LLMAgent:         "zbbs-hannah-boggs",
				State:            sim.StateWorking,
				WorkStructureID:  "inn",
				ScheduleStartMin: intPtr(9 * 60),
				ScheduleEndMin:   intPtr(17 * 60),
			},
			// Off-shift keeper of the inn (16:00–03:00 wrap shift excludes noon).
			"bram": {
				DisplayName:      "Bram Tosh",
				LLMAgent:         "zbbs-bram-tosh",
				State:            sim.StateSleeping,
				WorkStructureID:  "inn",
				ScheduleStartMin: intPtr(16 * 60),
				ScheduleEndMin:   intPtr(3 * 60),
			},
			// A lodger holding an active ledger grant on inn private room 2.
			"traveler": {
				DisplayName: "A Traveler",
				State:       sim.StateIdle,
				RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
					{RoomID: 2, Source: sim.AccessSourceLedger}: {
						RoomID:    2,
						Source:    sim.AccessSourceLedger,
						Active:    true,
						ExpiresAt: &future,
					},
				},
			},
			// A non-keeper PC (no WorkStructureID) — must not appear as a keeper.
			"pc1": {DisplayName: "Player One", State: sim.StateIdle},
		},
	}

	// scope=keepered: only the inn (the well has no keeper).
	kept := umbilicalStructuresFromSnapshot(snap, structuresScopeKeepered)
	if kept.Total != 1 || len(kept.Structures) != 1 {
		t.Fatalf("keepered total = %d, want 1 (only the inn)", kept.Total)
	}
	inn := kept.Structures[0]
	if inn.ID != "inn" {
		t.Fatalf("keepered[0] = %q, want inn", inn.ID)
	}
	if *kept.LocalMinuteOfDay != noon {
		t.Errorf("local_minute_of_day = %v, want %d", kept.LocalMinuteOfDay, noon)
	}

	// Two keepers, sorted by actor id (bram before hannah).
	if len(inn.Keepers) != 2 || inn.Keepers[0].ActorID != "bram" || inn.Keepers[1].ActorID != "hannah" {
		t.Fatalf("inn keepers = %+v, want [bram, hannah]", inn.Keepers)
	}
	// hannah on shift at noon, bram (wrap shift) off.
	hannah := inn.Keepers[1]
	bram := inn.Keepers[0]
	if !hannah.OnShift {
		t.Errorf("hannah should be on shift at noon (09:00–17:00)")
	}
	if bram.OnShift {
		t.Errorf("bram should be off shift at noon (16:00–03:00 wrap)")
	}
	if hannah.LLMAgent != "zbbs-hannah-boggs" || hannah.State != "working" {
		t.Errorf("hannah keeper fields wrong: %+v", hannah)
	}

	// Room tally: 1 common, 2 private (1 occupied by the traveler's ledger grant),
	// 1 staff.
	wantRooms := UmbilicalStructureRoomsDTO{Common: 1, Private: 2, Staff: 1, PrivateOccupied: 1}
	if inn.Rooms != wantRooms {
		t.Errorf("inn rooms = %+v, want %+v", inn.Rooms, wantRooms)
	}

	// scope=all: both structures, keepered (inn) sorted before keeperless (well).
	all := umbilicalStructuresFromSnapshot(snap, structuresScopeAll)
	if all.Total != 2 || len(all.Structures) != 2 {
		t.Fatalf("all total = %d, want 2", all.Total)
	}
	if all.Structures[0].ID != "inn" || all.Structures[1].ID != "well" {
		t.Errorf("all order = [%q, %q], want [inn, well] (keepered first)", all.Structures[0].ID, all.Structures[1].ID)
	}
	if len(all.Structures[1].Keepers) != 0 {
		t.Errorf("well should have no keepers, got %+v", all.Structures[1].Keepers)
	}

	// nil snapshot → empty roster, no panic.
	empty := umbilicalStructuresFromSnapshot(nil, structuresScopeKeepered)
	if empty.Total != 0 || len(empty.Structures) != 0 {
		t.Errorf("nil snapshot should yield empty roster, got %+v", empty)
	}

	// No village clock → on_shift falls back to false even for a scheduled keeper.
	snapNoClock := &sim.Snapshot{
		PublishedAt: now,
		Structures:  map[sim.StructureID]*sim.Structure{"inn": {ID: "inn", DisplayName: "The Inn"}},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"hannah": {DisplayName: "Hannah Boggs", WorkStructureID: "inn", ScheduleStartMin: intPtr(9 * 60), ScheduleEndMin: intPtr(17 * 60)},
		},
	}
	nc := umbilicalStructuresFromSnapshot(snapNoClock, structuresScopeKeepered)
	if nc.LocalMinuteOfDay != nil {
		t.Errorf("local_minute_of_day should be nil with no clock, got %v", *nc.LocalMinuteOfDay)
	}
	if len(nc.Structures) != 1 || nc.Structures[0].Keepers[0].OnShift {
		t.Errorf("on_shift should be false with no clock, got %+v", nc.Structures)
	}
}
