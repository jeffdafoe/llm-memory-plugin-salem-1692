package mem_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestRoundTrip_ActorClonesBreakAliasing verifies that going through
// Seed → LoadAll → mutate → SaveSnapshot → LoadAll preserves values but
// produces fresh entities. The aliasing check is the load-bearing assertion
// — without per-aggregate clone helpers, mem aliased pointers and the
// pg-impl serialization boundary would surface shape bugs only at cutover.
func TestRoundTrip_ActorClonesBreakAliasing(t *testing.T) {
	ctx := context.Background()
	_, h := mem.NewRepository()

	now := time.Now().UTC()
	rem := 3
	expires := now.Add(2 * time.Hour)

	seed := map[sim.ActorID]*sim.Actor{
		"elizabeth": {
			ID:               "elizabeth",
			DisplayName:      "Elizabeth Ellis",
			Kind:             sim.KindNPCStateful,
			State:            sim.StateWalking,
			StateEnteredAt:   now,
			Needs:            map[sim.NeedKey]int{"hunger": 5, "tiredness": 3},
			Inventory:        map[sim.ItemKind]int{"bread": 2},
			Coins:            42,
			LastTickedAt:     &now,
			RecentActions:    sim.NewRingBuffer[sim.Action](4),
			RecentStateTrans: sim.NewRingBuffer[sim.StateTransition](4),
			DwellCredits: map[sim.DwellCreditKey]*sim.DwellCredit{
				{ObjectID: "oak-1", Attribute: "tiredness", Source: sim.DwellSourceObject}: {
					ObjectID:           "oak-1",
					Attribute:          "tiredness",
					Source:             sim.DwellSourceObject,
					LastCreditedAt:     now,
					DwellDelta:         -2,
					DwellPeriodMinutes: 15,
				},
				{ObjectID: "bread-1", Attribute: "hunger", Source: sim.DwellSourceItem}: {
					ObjectID:           "bread-1",
					Attribute:          "hunger",
					Source:             sim.DwellSourceItem,
					LastCreditedAt:     now,
					RemainingTicks:     &rem,
					DwellDelta:         -1,
					DwellPeriodMinutes: 5,
				},
			},
			RoomAccess: map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: 2, Source: sim.AccessSourceLedger}: {
					RoomID:    2,
					Source:    sim.AccessSourceLedger,
					LedgerID:  100,
					ExpiresAt: &expires,
					Active:    true,
					CreatedAt: now,
				},
			},
		},
	}
	h.Actors.Seed(seed)

	// Mutating the seed map after Seed must NOT bleed through — Seed clones.
	seed["elizabeth"].Needs["hunger"] = 999
	seed["elizabeth"].DwellCredits[sim.DwellCreditKey{ObjectID: "oak-1", Attribute: "tiredness", Source: sim.DwellSourceObject}].DwellDelta = 999

	loaded1, err := h.Actors.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if got := loaded1["elizabeth"].Needs["hunger"]; got != 5 {
		t.Fatalf("Seed didn't clone: post-Seed mutation of caller's map leaked to repo (Needs.hunger=%d, want 5)", got)
	}
	if got := loaded1["elizabeth"].DwellCredits[sim.DwellCreditKey{ObjectID: "oak-1", Attribute: "tiredness", Source: sim.DwellSourceObject}].DwellDelta; got != -2 {
		t.Fatalf("Seed didn't clone DwellCredit: got DwellDelta=%d, want -2", got)
	}

	// Mutate the loaded entity, save, reload — value should be preserved.
	loaded1["elizabeth"].Needs["hunger"] = 7
	loaded1["elizabeth"].Inventory["ale"] = 1
	loaded1["elizabeth"].Coins = 50
	loaded1["elizabeth"].DwellCredits[sim.DwellCreditKey{ObjectID: "oak-1", Attribute: "tiredness", Source: sim.DwellSourceObject}].DwellDelta = -5

	if err := h.Actors.SaveSnapshot(ctx, nil, loaded1); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// After save, mutating the source again should not leak.
	loaded1["elizabeth"].Needs["hunger"] = 123

	loaded2, err := h.Actors.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll #2: %v", err)
	}

	// Values reflect the saved mutation.
	if got := loaded2["elizabeth"].Needs["hunger"]; got != 7 {
		t.Errorf("Needs.hunger after save+reload = %d, want 7", got)
	}
	if got := loaded2["elizabeth"].Inventory["ale"]; got != 1 {
		t.Errorf("Inventory.ale after save+reload = %d, want 1", got)
	}
	if got := loaded2["elizabeth"].Coins; got != 50 {
		t.Errorf("Coins after save+reload = %d, want 50", got)
	}
	if got := loaded2["elizabeth"].DwellCredits[sim.DwellCreditKey{ObjectID: "oak-1", Attribute: "tiredness", Source: sim.DwellSourceObject}].DwellDelta; got != -5 {
		t.Errorf("DwellCredit DwellDelta after save+reload = %d, want -5", got)
	}

	// Pointer identity is broken across reloads — proves the clone is real
	// and not just a shallow copy of the outer struct.
	if loaded1["elizabeth"] == loaded2["elizabeth"] {
		t.Error("Actor pointer aliased between LoadAll calls")
	}
	k := sim.DwellCreditKey{ObjectID: "oak-1", Attribute: "tiredness", Source: sim.DwellSourceObject}
	if loaded1["elizabeth"].DwellCredits[k] == loaded2["elizabeth"].DwellCredits[k] {
		t.Error("DwellCredit pointer aliased between LoadAll calls")
	}
	rk := sim.RoomAccessKey{RoomID: 2, Source: sim.AccessSourceLedger}
	if loaded1["elizabeth"].RoomAccess[rk] == loaded2["elizabeth"].RoomAccess[rk] {
		t.Error("RoomAccess pointer aliased between LoadAll calls")
	}
	if loaded1["elizabeth"].RoomAccess[rk].ExpiresAt == loaded2["elizabeth"].RoomAccess[rk].ExpiresAt {
		t.Error("RoomAccess.ExpiresAt *time.Time aliased between LoadAll calls")
	}
}

// TestRoundTrip_VillageObjectClonesBreakAliasing verifies the same
// invariants for VillageObject — Tags slice, Refreshes slice, and each
// ObjectRefresh pointer must be fresh across the repo boundary.
func TestRoundTrip_VillageObjectClonesBreakAliasing(t *testing.T) {
	ctx := context.Background()
	_, h := mem.NewRepository()

	avail := 5
	max := 10
	hours := 6

	seed := map[sim.VillageObjectID]*sim.VillageObject{
		"well-1": {
			ID:           "well-1",
			AssetID:      "well",
			CurrentState: "default",
			Tags:         []string{"refresh", "public"},
			Refreshes: []*sim.ObjectRefresh{
				{
					Attribute:          "thirst",
					Amount:             -3,
					AvailableQuantity:  &avail,
					MaxQuantity:        &max,
					RefreshMode:        sim.RefreshModeContinuous,
					RefreshPeriodHours: &hours,
				},
			},
		},
	}
	h.VillageObjects.Seed(seed)

	// Post-Seed mutation must not leak.
	seed["well-1"].Tags[0] = "MUTATED"
	seed["well-1"].Refreshes[0] = nil

	loaded1, err := h.VillageObjects.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if got := loaded1["well-1"].Tags[0]; got != "refresh" {
		t.Fatalf("Seed didn't clone Tags: got %q, want refresh", got)
	}
	if loaded1["well-1"].Refreshes[0] == nil {
		t.Fatal("Seed didn't clone Refreshes slice element")
	}

	// Mutate + save + reload.
	loaded1["well-1"].CurrentState = "lit"
	next := 8
	loaded1["well-1"].Refreshes[0].AvailableQuantity = &next

	if err := h.VillageObjects.SaveSnapshot(ctx, nil, loaded1); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	loaded2, err := h.VillageObjects.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll #2: %v", err)
	}

	if got := loaded2["well-1"].CurrentState; got != "lit" {
		t.Errorf("CurrentState after save+reload = %q, want lit", got)
	}
	if got := *loaded2["well-1"].Refreshes[0].AvailableQuantity; got != 8 {
		t.Errorf("AvailableQuantity after save+reload = %d, want 8", got)
	}

	if loaded1["well-1"] == loaded2["well-1"] {
		t.Error("VillageObject pointer aliased between LoadAll calls")
	}
	if loaded1["well-1"].Refreshes[0] == loaded2["well-1"].Refreshes[0] {
		t.Error("ObjectRefresh pointer aliased between LoadAll calls")
	}
}

// TestRoundTrip_StructureClonesBreakAliasing verifies Structure (and its
// Rooms slice) round-trips with fresh pointers.
func TestRoundTrip_StructureClonesBreakAliasing(t *testing.T) {
	ctx := context.Background()
	_, h := mem.NewRepository()

	seed := map[sim.StructureID]*sim.Structure{
		"tavern": {
			ID:          "tavern",
			DisplayName: "The Crow's Foot",
			Tags:        []string{"tavern", "lodging"},
			Rooms: []*sim.Room{
				{ID: 1, StructureID: "tavern", Kind: sim.RoomKindCommon, Name: "common"},
				{ID: 2, StructureID: "tavern", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
			},
		},
	}
	h.Structures.Seed(seed)

	seed["tavern"].Tags[0] = "MUTATED"
	seed["tavern"].Rooms[0].Name = "MUTATED"

	loaded1, err := h.Structures.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if got := loaded1["tavern"].Tags[0]; got != "tavern" {
		t.Fatalf("Seed didn't clone Tags: got %q", got)
	}
	if got := loaded1["tavern"].Rooms[0].Name; got != "common" {
		t.Fatalf("Seed didn't clone Rooms: got %q", got)
	}

	loaded1["tavern"].DisplayName = "Renamed"
	if err := h.Structures.SaveSnapshot(ctx, nil, loaded1); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	loaded2, err := h.Structures.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll #2: %v", err)
	}

	if got := loaded2["tavern"].DisplayName; got != "Renamed" {
		t.Errorf("DisplayName after save+reload = %q, want Renamed", got)
	}
	if loaded1["tavern"] == loaded2["tavern"] {
		t.Error("Structure pointer aliased between LoadAll calls")
	}
	if loaded1["tavern"].Rooms[0] == loaded2["tavern"].Rooms[0] {
		t.Error("Room pointer aliased between LoadAll calls")
	}
}

// TestRoundTrip_HuddleClonesBreakAliasing covers Huddle with its Members
// map.
func TestRoundTrip_HuddleClonesBreakAliasing(t *testing.T) {
	ctx := context.Background()
	_, h := mem.NewRepository()

	seed := map[sim.HuddleID]*sim.Huddle{
		"h1": {
			ID:        "h1",
			Members:   map[sim.ActorID]struct{}{"alice": {}, "bob": {}},
			StartedAt: time.Now().UTC(),
		},
	}
	h.Huddles.Seed(seed)

	// Mutate seed after — must not leak into repo.
	delete(seed["h1"].Members, "alice")

	loaded1, err := h.Huddles.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := loaded1["h1"].Members["alice"]; !ok {
		t.Fatal("Seed didn't clone Members: post-Seed delete leaked")
	}

	delete(loaded1["h1"].Members, "alice")
	if err := h.Huddles.SaveSnapshot(ctx, nil, loaded1); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	loaded2, err := h.Huddles.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll #2: %v", err)
	}
	if _, ok := loaded2["h1"].Members["alice"]; ok {
		t.Error("alice should be gone after save+reload")
	}
	if loaded1["h1"] == loaded2["h1"] {
		t.Error("Huddle pointer aliased between LoadAll calls")
	}
}

// TestRoundTrip_SceneClonesBreakAliasing covers Scene including the
// nested ParticipantStateAtOrigin map of *ActorSnapshot. The participant-
// snapshot capture is the seam Phase 2 PR 3 perception build will read for
// diff-against-scene-start, so the round-trip clone has to deep-copy each
// snapshot AND each snapshot's Needs map — otherwise a checkpoint+reload
// would produce ghost-aliased state observable through subsequent
// mutations.
func TestRoundTrip_SceneClonesBreakAliasing(t *testing.T) {
	ctx := context.Background()
	_, h := mem.NewRepository()

	now := time.Now().UTC()
	seed := map[sim.SceneID]*sim.Scene{
		"sc1": {
			ID:         "sc1",
			OriginAt:   now,
			OriginKind: "pc_speak",
			Bound:      sim.NewStructureBound("tavern"),
			Huddles: map[sim.HuddleID]struct{}{
				"h1": {},
			},
			ParticipantStateAtOrigin: map[sim.ActorID]*sim.ActorSnapshot{
				"alice": {
					AtTick:            42,
					State:             sim.StateConversing,
					InsideStructureID: "tavern",
					CurrentHuddleID:   "h1",
					Needs:             map[sim.NeedKey]int{"hunger": 4, "thirst": 1},
					Coins:             7,
				},
			},
		},
	}
	h.Scenes.Seed(seed)

	// Mutate seed AFTER Seed — must not leak into the repo. Tests both
	// the Huddles set and the inner Needs map of the captured snapshot.
	delete(seed["sc1"].Huddles, "h1")
	seed["sc1"].ParticipantStateAtOrigin["alice"].Needs["hunger"] = 999

	loaded1, err := h.Scenes.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := loaded1["sc1"].Huddles["h1"]; !ok {
		t.Fatal("Seed didn't clone Huddles set: post-Seed delete leaked")
	}
	if got := loaded1["sc1"].ParticipantStateAtOrigin["alice"].Needs["hunger"]; got != 4 {
		t.Fatalf("Seed didn't deep-clone ActorSnapshot.Needs: got hunger=%d, want 4", got)
	}

	// Mutate the loaded scene, save, reload — values preserved.
	loaded1["sc1"].Huddles["h2"] = struct{}{}
	loaded1["sc1"].ParticipantStateAtOrigin["alice"].Needs["thirst"] = 5
	loaded1["sc1"].ParticipantStateAtOrigin["bob"] = &sim.ActorSnapshot{
		AtTick:          42,
		State:           sim.StateIdle,
		CurrentHuddleID: "h1",
		Needs:           map[sim.NeedKey]int{"hunger": 0},
		Coins:           3,
	}

	if err := h.Scenes.SaveSnapshot(ctx, nil, loaded1); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	loaded2, err := h.Scenes.LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll #2: %v", err)
	}
	if _, ok := loaded2["sc1"].Huddles["h2"]; !ok {
		t.Error("Huddles set lost h2 after save+reload")
	}
	if got := loaded2["sc1"].ParticipantStateAtOrigin["alice"].Needs["thirst"]; got != 5 {
		t.Errorf("alice thirst after save+reload = %d, want 5", got)
	}
	if got := loaded2["sc1"].ParticipantStateAtOrigin["bob"].Coins; got != 3 {
		t.Errorf("bob coins after save+reload = %d, want 3", got)
	}

	if loaded1["sc1"] == loaded2["sc1"] {
		t.Error("Scene pointer aliased between LoadAll calls")
	}
	if loaded1["sc1"].ParticipantStateAtOrigin["alice"] == loaded2["sc1"].ParticipantStateAtOrigin["alice"] {
		t.Error("ActorSnapshot pointer aliased between LoadAll calls")
	}
}
