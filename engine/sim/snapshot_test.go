package sim_test

import (
	"context"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// TestSnapshotIsImmutable_PointerIdentity proves the published Snapshot
// is a genuine deep copy: successive publishes return distinct
// VillageObject / Structure / Huddle / Order pointers, and mutating a
// snapshot's per-aggregate values does not bleed back into world state.
//
// Locks in the contract behind atomic.Pointer[Snapshot] so refactors that
// re-introduce pointer aliasing get caught here, not in a production race.
func TestSnapshotIsImmutable_PointerIdentity(t *testing.T) {
	repo, handles := mem.NewRepository()

	avail := 5
	max := 10
	hours := 6
	delta := -3
	period := 15

	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"oak-1": {
			ID:           "oak-1",
			AssetID:      "tree-oak",
			CurrentState: "default",
			Tags:         []string{"shade", "harvest"},
			Refreshes: []*sim.ObjectRefresh{
				{
					Attribute:          "tiredness",
					Amount:             -2,
					AvailableQuantity:  &avail,
					MaxQuantity:        &max,
					RefreshMode:        sim.RefreshModeContinuous,
					RefreshPeriodHours: &hours,
					DwellDelta:         &delta,
					DwellPeriodMinutes: &period,
				},
			},
		},
	})

	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"tavern": {
			ID:          "tavern",
			DisplayName: "The Crow's Foot",
			Tags:        []string{"tavern", "lodging"},
			Rooms: []*sim.Room{
				{ID: 1, StructureID: "tavern", Kind: sim.RoomKindCommon, Name: "common"},
				{ID: 2, StructureID: "tavern", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
			},
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	s1 := w.Published()

	// Force another republish via a no-op command.
	if _, err := w.Send(sim.Command{Fn: func(_ *sim.World) (any, error) { return nil, nil }}); err != nil {
		t.Fatalf("no-op send: %v", err)
	}
	s2 := w.Published()

	if s1 == s2 {
		t.Fatal("expected successive Published() to return distinct Snapshot pointers")
	}

	// Per-aggregate pointer identity: each publish must mint fresh entities
	// so a reader holding s1 cannot observe (and cannot mutate) what s2's
	// reader observes — or what the world goroutine subsequently mutates.
	if s1.VillageObjects["oak-1"] == s2.VillageObjects["oak-1"] {
		t.Error("VillageObject pointer aliased across snapshots")
	}
	if s1.Structures["tavern"] == s2.Structures["tavern"] {
		t.Error("Structure pointer aliased across snapshots")
	}

	// Deep clone: mutating the snapshot's nested slice/map must not bleed
	// into world state observable from the next publish.
	s1.VillageObjects["oak-1"].Tags[0] = "MUTATED"
	if len(s1.VillageObjects["oak-1"].Refreshes) > 0 {
		s1.VillageObjects["oak-1"].Refreshes[0] = nil
	}
	s1.Structures["tavern"].Rooms[0].Name = "MUTATED"

	if _, err := w.Send(sim.Command{Fn: func(_ *sim.World) (any, error) { return nil, nil }}); err != nil {
		t.Fatalf("second no-op send: %v", err)
	}
	s3 := w.Published()

	if got := s3.VillageObjects["oak-1"].Tags[0]; got != "shade" {
		t.Errorf("Tags leaked through snapshot mutation: got %q, want %q", got, "shade")
	}
	if r := s3.VillageObjects["oak-1"].Refreshes; len(r) == 0 || r[0] == nil {
		t.Error("Refreshes leaked through snapshot mutation")
	}
	if got := s3.Structures["tavern"].Rooms[0].Name; got != "common" {
		t.Errorf("Rooms leaked through snapshot mutation: got %q, want %q", got, "common")
	}
}

// TestSnapshotObjectRefreshInnerPointersIsolated verifies the round-2
// fix: mutating *AvailableQuantity (or any of the other scalar pointer
// fields on ObjectRefresh) through a published Snapshot must not affect
// the next publish. Code_review caught the original CloneVillageObject
// aliasing these pointers; this test locks the deep-copy invariant.
func TestSnapshotObjectRefreshInnerPointersIsolated(t *testing.T) {
	repo, handles := mem.NewRepository()

	avail := 5
	max := 10
	hours := 6
	delta := -3
	period := 15
	last := time.Now().UTC()
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"well-1": {
			ID:           "well-1",
			AssetID:      "well",
			CurrentState: "default",
			Refreshes: []*sim.ObjectRefresh{
				{
					Attribute:          "thirst",
					Amount:             -3,
					AvailableQuantity:  &avail,
					MaxQuantity:        &max,
					RefreshMode:        sim.RefreshModeContinuous,
					RefreshPeriodHours: &hours,
					LastRefreshAt:      &last,
					DwellDelta:         &delta,
					DwellPeriodMinutes: &period,
				},
			},
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	s1 := w.Published()

	// Mutate every scalar pointer on the snapshot's refresh row. If clone
	// is shallow, these writes corrupt world state.
	r := s1.VillageObjects["well-1"].Refreshes[0]
	*r.AvailableQuantity = 999
	*r.MaxQuantity = 999
	*r.RefreshPeriodHours = 999
	*r.DwellDelta = 999
	*r.DwellPeriodMinutes = 999
	*r.LastRefreshAt = time.Time{}

	// Trigger a fresh publish.
	if _, err := w.Send(sim.Command{Fn: func(_ *sim.World) (any, error) { return nil, nil }}); err != nil {
		t.Fatalf("no-op send: %v", err)
	}
	s2 := w.Published()
	r2 := s2.VillageObjects["well-1"].Refreshes[0]

	if *r2.AvailableQuantity != 5 {
		t.Errorf("AvailableQuantity leaked: got %d, want 5", *r2.AvailableQuantity)
	}
	if *r2.MaxQuantity != 10 {
		t.Errorf("MaxQuantity leaked: got %d, want 10", *r2.MaxQuantity)
	}
	if *r2.RefreshPeriodHours != 6 {
		t.Errorf("RefreshPeriodHours leaked: got %d, want 6", *r2.RefreshPeriodHours)
	}
	if *r2.DwellDelta != -3 {
		t.Errorf("DwellDelta leaked: got %d, want -3", *r2.DwellDelta)
	}
	if *r2.DwellPeriodMinutes != 15 {
		t.Errorf("DwellPeriodMinutes leaked: got %d, want 15", *r2.DwellPeriodMinutes)
	}
	if r2.LastRefreshAt.IsZero() {
		t.Error("LastRefreshAt leaked: zeroed in snapshot bled into world")
	}
}

// TestSnapshotHuddleScene_PointerIdentity covers the Huddle and Scene
// helpers explicitly via direct world-state seeding (LoadWorld doesn't
// load these aggregates today).
func TestSnapshotHuddleScene_PointerIdentity(t *testing.T) {
	repo, _ := mem.NewRepository()
	w := sim.NewWorld(repo)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Seed via a command so the world goroutine owns the write.
	_, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Huddles["h1"] = &sim.Huddle{
				ID:        "h1",
				Members:   map[sim.ActorID]struct{}{"alice": {}, "bob": {}},
				StartedAt: time.Now(),
			}
			world.Scenes["sc1"] = &sim.Scene{
				ID:       "sc1",
				OriginAt: time.Now(),
				Huddles:  map[sim.HuddleID]struct{}{"h1": {}},
			}
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("seed send: %v", err)
	}

	s1 := w.Published()
	_, _ = w.Send(sim.Command{Fn: func(_ *sim.World) (any, error) { return nil, nil }})
	s2 := w.Published()

	if s1.Huddles["h1"] == s2.Huddles["h1"] {
		t.Error("Huddle pointer aliased across snapshots")
	}
	if s1.Scenes["sc1"] == s2.Scenes["sc1"] {
		t.Error("Scene pointer aliased across snapshots")
	}

	// Mutating s1's Members map should not affect s2 or the world.
	delete(s1.Huddles["h1"].Members, "alice")
	if _, ok := s2.Huddles["h1"].Members["alice"]; !ok {
		t.Error("Huddle.Members leaked across snapshots (alice gone from s2)")
	}
}

// TestCloneTags_NeverNil locks in the "cloned Tags is always non-nil"
// invariant. Regression for ZBBS-HOME-315: CloneStructure / CloneVillageObject
// previously used append([]string(nil), src...), which returns nil for an
// empty source. The checkpoint clones every structure/object (checkpoint.go),
// so an empty-tags entry produced a nil slice that pgx encoded as SQL NULL —
// rejected by tags TEXT[] NOT NULL, aborting the entire SaveWorld transaction
// and breaking all checkpointing. Both empty and nil sources must clone to a
// non-nil empty slice.
func TestCloneTags_NeverNil(t *testing.T) {
	t.Run("structure empty tags", func(t *testing.T) {
		got := sim.CloneStructure(&sim.Structure{ID: "s1", DisplayName: "X", Tags: []string{}})
		if got.Tags == nil {
			t.Fatal("CloneStructure nilled an empty Tags slice (would write SQL NULL)")
		}
	})
	t.Run("structure nil tags", func(t *testing.T) {
		got := sim.CloneStructure(&sim.Structure{ID: "s1", DisplayName: "X", Tags: nil})
		if got.Tags == nil {
			t.Fatal("CloneStructure left Tags nil (would write SQL NULL)")
		}
	})
	t.Run("structure populated tags copied and isolated", func(t *testing.T) {
		src := &sim.Structure{ID: "s1", DisplayName: "X", Tags: []string{"tavern", "lodging"}}
		got := sim.CloneStructure(src)
		if len(got.Tags) != 2 || got.Tags[0] != "tavern" || got.Tags[1] != "lodging" {
			t.Fatalf("Tags not copied: %v", got.Tags)
		}
		got.Tags[0] = "MUTATED"
		if src.Tags[0] != "tavern" {
			t.Error("clone Tags aliased the source slice")
		}
	})
	t.Run("village object empty tags", func(t *testing.T) {
		got := sim.CloneVillageObject(&sim.VillageObject{ID: "o1", Tags: []string{}})
		if got.Tags == nil {
			t.Fatal("CloneVillageObject nilled an empty Tags slice (would write SQL NULL)")
		}
	})
	t.Run("village object nil tags", func(t *testing.T) {
		got := sim.CloneVillageObject(&sim.VillageObject{ID: "o1", Tags: nil})
		if got.Tags == nil {
			t.Fatal("CloneVillageObject left Tags nil (would write SQL NULL)")
		}
	})
	t.Run("village object populated tags copied and isolated", func(t *testing.T) {
		src := &sim.VillageObject{ID: "o1", Tags: []string{"shade", "harvest"}}
		got := sim.CloneVillageObject(src)
		if len(got.Tags) != 2 || got.Tags[0] != "shade" || got.Tags[1] != "harvest" {
			t.Fatalf("Tags not copied: %v", got.Tags)
		}
		got.Tags[0] = "MUTATED"
		if src.Tags[0] != "shade" {
			t.Error("clone Tags aliased the source slice")
		}
	})
}
