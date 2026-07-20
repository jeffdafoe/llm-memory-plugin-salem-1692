package httpapi

import (
	"encoding/json"
	"net/http"
	"slices"
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

// TestUmbilicalStructuresTagsFromVillageObject pins LLM-478: the roster renders
// the structure's VILLAGE OBJECT tags, not Structure.Tags. The two sets are
// seeded to DISAGREE — that disagreement is the live defect (the Mill's object
// carried "wholesaler" while its structure row said "mill", so the operator
// surface reported a tag set no gate reads).
func TestUmbilicalStructuresTagsFromVillageObject(t *testing.T) {
	snap := &sim.Snapshot{
		PublishedAt: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC),
		Structures: map[sim.StructureID]*sim.Structure{
			// Object tags win over the structure's own (stale) set.
			"mill": {ID: "mill", DisplayName: "The Mill", Tags: []string{"business", "mill"}},
			// No village object placed → [] rather than the structure's tags.
			"ghost": {ID: "ghost", DisplayName: "Unplaced", Tags: []string{"business", "shop"}},
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"mill": {ID: "mill", Tags: []string{"business", "wholesaler"}},
		},
	}

	got := umbilicalStructuresFromSnapshot(snap, structuresScopeAll)
	tags := map[string][]string{}
	for _, s := range got.Structures {
		tags[s.ID] = s.Tags
	}

	if want := []string{"business", "wholesaler"}; !slices.Equal(tags["mill"], want) {
		t.Errorf("mill tags = %v, want %v (VillageObject.Tags, not Structure.Tags)", tags["mill"], want)
	}
	if got := tags["ghost"]; got == nil || len(got) != 0 {
		t.Errorf("unplaced structure tags = %v, want [] (non-nil, empty)", got)
	}
}

// TestUmbilicalStructures_AddTagRoundTrip is the end-to-end half of LLM-478: a
// tag an operator adds via POST /umbilical/object/add-tag must be visible on the
// next GET /umbilical/structures. This is the loop that was broken in the field —
// tagging the Mill "wholesaler" changed engine behavior but never showed up on
// the roster — so it is asserted through the real handlers and world goroutine
// rather than the pure snapshot mapper.
func TestUmbilicalStructures_AddTagRoundTrip(t *testing.T) {
	srv, h := controlServer(t, operatorPerms)

	// The seeded world has no structure; add one with its placement (structure and
	// village object share an id — the bridge the roster resolves tags through).
	if _, err := srv.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Structures["mill"] = &sim.Structure{
			ID: "mill", DisplayName: "The Mill", Tags: []string{"business", "mill"},
		}
		world.VillageObjects["mill"] = &sim.VillageObject{
			ID: "mill", AssetID: "asset-x", DisplayName: "The Mill", Tags: []string{"business"},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed mill: %v", err)
	}

	if rec := postReq(t, h, "/api/village/umbilical/object/add-tag", "tok",
		`{"object_id":"mill","tag":"wholesaler"}`); rec.Code != http.StatusOK {
		t.Fatalf("add-tag = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	rec := req(t, h, "/api/village/umbilical/structures?scope=all", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("structures = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var out UmbilicalStructuresDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode structures: %v", err)
	}

	var mill *UmbilicalStructureRowDTO
	for i := range out.Structures {
		if out.Structures[i].ID == "mill" {
			mill = &out.Structures[i]
		}
	}
	if mill == nil {
		t.Fatalf("mill missing from roster: %+v", out.Structures)
	}
	if want := []string{"business", "wholesaler"}; !slices.Equal(mill.Tags, want) {
		t.Errorf("mill tags after add-tag = %v, want %v", mill.Tags, want)
	}
}
