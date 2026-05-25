package sim_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildRoomTestWorld seeds an Inn with three rooms (common +
// two private bedrooms) and one PC at the common room.
func buildRoomTestWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"inn": {
			ID:          "inn",
			DisplayName: "The Greenleaf Inn",
			Rooms: []*sim.Room{
				{ID: 1, StructureID: "inn", Kind: sim.RoomKindCommon, Name: "common"},
				{ID: 2, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_1"},
				{ID: 3, StructureID: "inn", Kind: sim.RoomKindPrivate, Name: "bedroom_2"},
				{ID: 4, StructureID: "inn", Kind: sim.RoomKindStaff, Name: "back_office"},
			},
		},
		"empty_house": { // structure with no private rooms — tests ErrNoPrivateRooms
			ID:          "empty_house",
			DisplayName: "Empty House",
			Rooms: []*sim.Room{
				{ID: 10, StructureID: "empty_house", Kind: sim.RoomKindCommon, Name: "common"},
			},
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"alice": {
			ID:                "alice",
			LoginUsername:     "alice",
			InsideStructureID: "inn",
			InsideRoomID:      1,
			DisplayName:       "Alice",
		},
		"hannah": {
			ID:                "hannah",
			LLMAgent:          "hannah-innkeeper",
			InsideStructureID: "inn",
			InsideRoomID:      1,
			WorkStructureID:   "inn",
			DisplayName:       "Hannah",
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// TestCommonRoomForStructure covers the lookup helper.
func TestCommonRoomForStructure(t *testing.T) {
	w, cancel := buildRoomTestWorld(t)
	defer cancel()

	got, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.CommonRoomForStructure(world, "inn"), nil
		},
	})
	if got.(sim.RoomID) != 1 {
		t.Errorf("CommonRoomForStructure(inn) = %d, want 1", got.(sim.RoomID))
	}

	// Missing structure → 0.
	got, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.CommonRoomForStructure(world, "ghost"), nil
		},
	})
	if got.(sim.RoomID) != 0 {
		t.Errorf("CommonRoomForStructure(ghost) = %d, want 0", got.(sim.RoomID))
	}
}

// TestCanEnterRoomCommon covers the always-allow path.
func TestCanEnterRoomCommon(t *testing.T) {
	w, cancel := buildRoomTestWorld(t)
	defer cancel()

	got, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.CanEnterRoom(world, world.Actors["alice"], 1, time.Now().UTC()), nil
		},
	})
	if !got.(bool) {
		t.Error("CanEnterRoom(common) = false, want true")
	}
}

// TestCanEnterRoomPrivateWithoutAccess covers the deny path for a PC
// with no RoomAccess row.
func TestCanEnterRoomPrivateWithoutAccess(t *testing.T) {
	w, cancel := buildRoomTestWorld(t)
	defer cancel()

	got, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.CanEnterRoom(world, world.Actors["alice"], 2, time.Now().UTC()), nil
		},
	})
	if got.(bool) {
		t.Error("CanEnterRoom(private, no access) = true, want false")
	}
}

// TestCanEnterRoomPrivateWithActiveAccess covers the allow path.
func TestCanEnterRoomPrivateWithActiveAccess(t *testing.T) {
	w, cancel := buildRoomTestWorld(t)
	defer cancel()

	future := time.Now().UTC().Add(24 * time.Hour)
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].RoomAccess = map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: 2, Source: sim.AccessSourceLedger}: {
					RoomID: 2, Source: sim.AccessSourceLedger,
					ExpiresAt: &future, Active: true,
				},
			}
			return nil, nil
		},
	})

	got, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.CanEnterRoom(world, world.Actors["alice"], 2, time.Now().UTC()), nil
		},
	})
	if !got.(bool) {
		t.Error("CanEnterRoom(private, active access) = false, want true")
	}
}

// TestCanEnterRoomPrivateInactiveAccess covers the deny-on-inactive path.
func TestCanEnterRoomPrivateInactiveAccess(t *testing.T) {
	w, cancel := buildRoomTestWorld(t)
	defer cancel()

	past := time.Now().UTC().Add(-1 * time.Hour)
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].RoomAccess = map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: 2, Source: sim.AccessSourceLedger}: {
					RoomID: 2, Source: sim.AccessSourceLedger,
					ExpiresAt: &past, Active: false, // already flipped by ExpireRoomAccess
				},
			}
			return nil, nil
		},
	})

	got, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.CanEnterRoom(world, world.Actors["alice"], 2, time.Now().UTC()), nil
		},
	})
	if got.(bool) {
		t.Error("CanEnterRoom(private, inactive access) = true, want false")
	}
}

// TestCanEnterRoomPrivateExpiryGate covers the window where Active is
// still true but ExpiresAt has passed (a request arriving between the
// expiry instant and the next ExpireRoomAccess sweep). canEnterRoom must
// fail closed in that window.
func TestCanEnterRoomPrivateExpiryGate(t *testing.T) {
	w, cancel := buildRoomTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	expired := now.Add(-1 * time.Minute) // expiry passed a minute ago

	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].RoomAccess = map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: 2, Source: sim.AccessSourceLedger}: {
					RoomID:    2,
					Source:    sim.AccessSourceLedger,
					ExpiresAt: &expired,
					Active:    true, // sweep hasn't run yet
				},
			}
			return nil, nil
		},
	})

	got, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.CanEnterRoom(world, world.Actors["alice"], 2, now), nil
		},
	})
	if got.(bool) {
		t.Error("CanEnterRoom(private, expired-but-not-swept) = true, want false")
	}

	// Sanity: with an ExpiresAt in the future, same row passes.
	future := now.Add(1 * time.Hour)
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].RoomAccess[sim.RoomAccessKey{RoomID: 2, Source: sim.AccessSourceLedger}].ExpiresAt = &future
			return nil, nil
		},
	})
	got, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.CanEnterRoom(world, world.Actors["alice"], 2, now), nil
		},
	})
	if !got.(bool) {
		t.Error("CanEnterRoom(private, future expiry) = false, want true")
	}
}

// TestCanEnterRoomStaff covers the staff allow/deny paths.
func TestCanEnterRoomStaff(t *testing.T) {
	w, cancel := buildRoomTestWorld(t)
	defer cancel()

	// Hannah works at inn → can enter staff room.
	got, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.CanEnterRoom(world, world.Actors["hannah"], 4, time.Now().UTC()), nil
		},
	})
	if !got.(bool) {
		t.Error("CanEnterRoom(staff, work matches) = false, want true")
	}

	// Alice doesn't work there → denied.
	got, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return sim.CanEnterRoom(world, world.Actors["alice"], 4, time.Now().UTC()), nil
		},
	})
	if got.(bool) {
		t.Error("CanEnterRoom(staff, no work match) = true, want false")
	}
}

// TestComputeLodgerUntil covers the lodger-until time math.
func TestComputeLodgerUntil(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	readyBy := time.Date(2026, 5, 12, 15, 0, 0, 0, loc) // 3pm check-in
	got := sim.ComputeLodgerUntil(readyBy, 2, 11, loc)
	want := time.Date(2026, 5, 14, 11, 0, 0, 0, loc) // 2 nights → 14th at 11am
	if !got.Equal(want) {
		t.Errorf("ComputeLodgerUntil = %v, want %v", got, want)
	}
}

// TestComputeEarliestCheckIn covers the earliest-check-in time math.
func TestComputeEarliestCheckIn(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	readyBy := time.Date(2026, 5, 12, 10, 0, 0, 0, loc)
	got := sim.ComputeEarliestCheckIn(readyBy, 15, loc)
	want := time.Date(2026, 5, 12, 15, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("ComputeEarliestCheckIn = %v, want %v", got, want)
	}
}

// TestAssignBedroomForLodgerHappy covers the typical assignment.
func TestAssignBedroomForLodgerHappy(t *testing.T) {
	w, cancel := buildRoomTestWorld(t)
	defer cancel()

	expires := time.Now().UTC().Add(24 * time.Hour)
	res, err := w.Send(sim.AssignBedroomForLodger("inn", "alice", 123, expires))
	if err != nil {
		t.Fatalf("AssignBedroom: %v", err)
	}
	r := res.(sim.AssignBedroomResult)
	if r.RoomID != 2 { // first private room by Name ASC
		t.Errorf("RoomID = %d, want 2 (bedroom_1)", r.RoomID)
	}
	if r.WasReassigned {
		t.Error("WasReassigned = true on fresh assignment")
	}

	// Actor InsideRoomID updated + RoomAccess stamped.
	state, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return struct {
				InsideRoom sim.RoomID
				HasAccess  bool
			}{
				world.Actors["alice"].InsideRoomID,
				world.Actors["alice"].RoomAccess[sim.RoomAccessKey{RoomID: 2, Source: sim.AccessSourceLedger}] != nil,
			}, nil
		},
	})
	s := state.(struct {
		InsideRoom sim.RoomID
		HasAccess  bool
	})
	if s.InsideRoom != 2 {
		t.Errorf("InsideRoomID = %d, want 2", s.InsideRoom)
	}
	if !s.HasAccess {
		t.Error("RoomAccess row not stamped")
	}
}

// TestAssignBedroomForLodgerNoPrivateRooms covers the
// ErrNoPrivateRooms branch — distinguishes data error from contention.
func TestAssignBedroomForLodgerNoPrivateRooms(t *testing.T) {
	w, cancel := buildRoomTestWorld(t)
	defer cancel()

	expires := time.Now().UTC().Add(24 * time.Hour)
	_, err := w.Send(sim.AssignBedroomForLodger("empty_house", "alice", 1, expires))
	if !errors.Is(err, sim.ErrNoPrivateRooms) {
		t.Errorf("err = %v, want ErrNoPrivateRooms", err)
	}
}

// TestAssignBedroomForLodgerAllOccupied covers the contention case.
func TestAssignBedroomForLodgerAllOccupied(t *testing.T) {
	w, cancel := buildRoomTestWorld(t)
	defer cancel()

	future := time.Now().UTC().Add(24 * time.Hour)
	// Hannah and Alice each grab one of the two private rooms.
	_, _ = w.Send(sim.AssignBedroomForLodger("inn", "hannah", 1, future))
	_, _ = w.Send(sim.AssignBedroomForLodger("inn", "alice", 2, future))

	// Seed a third PC and try to assign — should return RoomID=0
	// (contention, no rooms available).
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["bob"] = &sim.Actor{ID: "bob", LoginUsername: "bob"}
			return nil, nil
		},
	})
	res, err := w.Send(sim.AssignBedroomForLodger("inn", "bob", 3, future))
	if err != nil {
		t.Fatalf("third assignment: %v", err)
	}
	if res.(sim.AssignBedroomResult).RoomID != 0 {
		t.Errorf("RoomID = %d on contention, want 0", res.(sim.AssignBedroomResult).RoomID)
	}
}

// TestAssignBedroomForLodgerExtendsExisting covers the re-pay extension
// path — same buyer paying again should hit ON CONFLICT and extend
// expires_at on the SAME room, not hop.
func TestAssignBedroomForLodgerExtendsExisting(t *testing.T) {
	w, cancel := buildRoomTestWorld(t)
	defer cancel()

	first := time.Now().UTC().Add(24 * time.Hour)
	res1, _ := w.Send(sim.AssignBedroomForLodger("inn", "alice", 1, first))
	roomID := res1.(sim.AssignBedroomResult).RoomID

	second := time.Now().UTC().Add(48 * time.Hour)
	res2, _ := w.Send(sim.AssignBedroomForLodger("inn", "alice", 2, second))
	r := res2.(sim.AssignBedroomResult)
	if r.RoomID != roomID {
		t.Errorf("extension room-hopped: got %d, want %d", r.RoomID, roomID)
	}
	if !r.WasReassigned {
		t.Error("WasReassigned = false on re-pay extension")
	}

	// ExpiresAt updated to the new value.
	exp, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			ra := world.Actors["alice"].RoomAccess[sim.RoomAccessKey{RoomID: roomID, Source: sim.AccessSourceLedger}]
			return *ra.ExpiresAt, nil
		},
	})
	if !exp.(time.Time).Equal(second) {
		t.Errorf("ExpiresAt = %v, want %v", exp, second)
	}
}

// TestExpireRoomAccess covers the sweep that flips Active=false on
// rows past their ExpiresAt.
func TestExpireRoomAccess(t *testing.T) {
	w, cancel := buildRoomTestWorld(t)
	defer cancel()

	past := time.Now().UTC().Add(-1 * time.Hour)
	future := time.Now().UTC().Add(24 * time.Hour)
	_, _ = w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].RoomAccess = map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: 2, Source: sim.AccessSourceLedger}: {
					RoomID: 2, ExpiresAt: &past, Active: true,
				},
				{RoomID: 3, Source: sim.AccessSourceLedger}: {
					RoomID: 3, ExpiresAt: &future, Active: true,
				},
			}
			return nil, nil
		},
	})

	res, _ := w.Send(sim.ExpireRoomAccess(time.Now().UTC()))
	if res.(sim.ExpireRoomAccessResult).Expired != 1 {
		t.Errorf("Expired = %d, want 1", res.(sim.ExpireRoomAccessResult).Expired)
	}
	// Verify the right one got flipped.
	check, _ := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors["alice"]
			return struct {
				PastActive   bool
				FutureActive bool
			}{
				a.RoomAccess[sim.RoomAccessKey{RoomID: 2, Source: sim.AccessSourceLedger}].Active,
				a.RoomAccess[sim.RoomAccessKey{RoomID: 3, Source: sim.AccessSourceLedger}].Active,
			}, nil
		},
	})
	c := check.(struct {
		PastActive   bool
		FutureActive bool
	})
	if c.PastActive {
		t.Error("past-expired access still Active")
	}
	if !c.FutureActive {
		t.Error("future-expired access flipped wrongly")
	}
}

// TestEvictExpiredOccupants covers the teleport-back-to-common sweep.
func TestEvictExpiredOccupants(t *testing.T) {
	w, cancel := buildRoomTestWorld(t)
	defer cancel()

	// Set up: Alice in bedroom_1 (room 2), her access is Active=false.
	now := time.Now().UTC()
	past := now.Add(-1 * time.Hour)
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			a := world.Actors["alice"]
			a.InsideRoomID = 2
			a.RoomAccess = map[sim.RoomAccessKey]*sim.RoomAccess{
				{RoomID: 2, Source: sim.AccessSourceLedger}: {
					RoomID: 2, ExpiresAt: &past, Active: false,
				},
			}
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	res, err := w.Send(sim.EvictExpiredOccupants(now))
	if err != nil {
		t.Fatalf("evict: %v", err)
	}
	r := res.(sim.EvictExpiredOccupantsResult)
	if len(r.Evicted) != 1 {
		t.Fatalf("Evicted count = %d, want 1", len(r.Evicted))
	}
	e := r.Evicted[0]
	if e.ActorID != "alice" {
		t.Errorf("Evicted ActorID = %q, want alice", e.ActorID)
	}
	if e.FromRoomID != 2 || e.ToRoomID != 1 {
		t.Errorf("Evicted From=%d To=%d, want From=2 To=1", e.FromRoomID, e.ToRoomID)
	}
	foundText := false
	for _, p := range sim.LodgingNarrationPool(sim.LodgingReasonCheckout) {
		if e.Text == p {
			foundText = true
			break
		}
	}
	if !foundText {
		t.Errorf("Text = %q, want a lodging-checkout pool phrase", e.Text)
	}

	// Actor moved.
	got, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			return world.Actors["alice"].InsideRoomID, nil
		},
	})
	if err != nil {
		t.Fatalf("read room: %v", err)
	}
	if got.(sim.RoomID) != 1 {
		t.Errorf("Alice InsideRoomID after evict = %d, want 1 (common)", got.(sim.RoomID))
	}
}

// TestEvictExpiredOccupantsSpareNPCs covers the PC-only filter.
func TestEvictExpiredOccupantsSpareNPCs(t *testing.T) {
	w, cancel := buildRoomTestWorld(t)
	defer cancel()

	// Hannah (NPC) is in a private room with no access — should NOT
	// be evicted (NPCs use different access mechanisms).
	if _, err := w.Send(sim.Command{
		Fn: func(world *sim.World) (any, error) {
			world.Actors["hannah"].InsideRoomID = 2
			world.Actors["hannah"].RoomAccess = nil
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	res, err := w.Send(sim.EvictExpiredOccupants(time.Now().UTC()))
	if err != nil {
		t.Fatalf("evict: %v", err)
	}
	if len(res.(sim.EvictExpiredOccupantsResult).Evicted) != 0 {
		t.Errorf("NPC was evicted: %+v", res.(sim.EvictExpiredOccupantsResult).Evicted)
	}
}

// TestEvictExpiredOccupantsEmitsRelocation covers the WS-surfacing half: an
// eviction emits a PCRelocatedToCommon event (which the hub translates to the
// private room_event the client renders), carrying the checkout reason and a
// pooled narration line.
func TestEvictExpiredOccupantsEmitsRelocation(t *testing.T) {
	w, cancel := buildRoomTestWorld(t)
	defer cancel()

	// got is mutated by the subscriber and read by the reader command — both
	// run on the world goroutine, so this is race-free without a mutex.
	var got []*sim.PCRelocatedToCommon
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			if e, ok := evt.(*sim.PCRelocatedToCommon); ok {
				got = append(got, e)
			}
		}))
		return nil, nil
	}}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	now := time.Now().UTC()
	past := now.Add(-1 * time.Hour)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		a.InsideRoomID = 2
		a.RoomAccess = map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 2, Source: sim.AccessSourceLedger}: {RoomID: 2, ExpiresAt: &past, Active: false},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := w.Send(sim.EvictExpiredOccupants(now)); err != nil {
		t.Fatalf("evict: %v", err)
	}

	read, err := w.Send(sim.Command{Fn: func(_ *sim.World) (any, error) {
		return append([]*sim.PCRelocatedToCommon(nil), got...), nil
	}})
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	events := read.([]*sim.PCRelocatedToCommon)
	if len(events) != 1 {
		t.Fatalf("PCRelocatedToCommon emitted %d times, want 1", len(events))
	}
	e := events[0]
	if e.ActorID != "alice" {
		t.Errorf("event ActorID = %q, want alice", e.ActorID)
	}
	if e.Reason != sim.LodgingReasonCheckout {
		t.Errorf("event Reason = %q, want %q", e.Reason, sim.LodgingReasonCheckout)
	}
	if e.StructureID == "" || e.Text == "" {
		t.Errorf("event StructureID/Text empty: %+v", e)
	}
}
