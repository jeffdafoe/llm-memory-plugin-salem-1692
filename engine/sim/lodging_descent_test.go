package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// sleepingLodger puts alice (a PC) asleep in private bedroom 2 of the inn with a
// ledger grant expiring at expiresAt/active, SleepingUntil in the past so the
// wake sweep wakes her with reason "auto" deterministically (the cap arm).
func sleepingLodger(t *testing.T, w *sim.World, now, expiresAt time.Time, active bool) {
	t.Helper()
	past := now.Add(-time.Hour)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		a.Kind = sim.KindPC
		a.InsideRoomID = 2
		a.SleepingUntil = &past
		a.RoomAccess = map[sim.RoomAccessKey]*sim.RoomAccess{
			{RoomID: 2, Source: sim.AccessSourceLedger}: {RoomID: 2, Source: sim.AccessSourceLedger, ExpiresAt: &expiresAt, Active: active},
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("setup: %v", err)
	}
}

// captureRelocations registers the morning-descent subscriber plus a recorder
// for PCRelocatedToCommon events. The returned reader drains the recorder on the
// world goroutine (race-free). Both the recorder slice and the descent
// subscriber are wired in one command so they're live before the wake sweep.
func captureRelocations(t *testing.T, w *sim.World) func() []*sim.PCRelocatedToCommon {
	t.Helper()
	var got []*sim.PCRelocatedToCommon
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		sim.RegisterLodgingMorningDescentSubscriber(world)
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			if e, ok := evt.(*sim.PCRelocatedToCommon); ok {
				got = append(got, e)
			}
		}))
		return nil, nil
	}}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return func() []*sim.PCRelocatedToCommon {
		res, err := w.Send(sim.Command{Fn: func(_ *sim.World) (any, error) {
			return append([]*sim.PCRelocatedToCommon(nil), got...), nil
		}})
		if err != nil {
			t.Fatalf("read events: %v", err)
		}
		return res.([]*sim.PCRelocatedToCommon)
	}
}

func aliceRoom(t *testing.T, w *sim.World) sim.RoomID {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		return world.Actors["alice"].InsideRoomID, nil
	}})
	if err != nil {
		t.Fatalf("read room: %v", err)
	}
	return res.(sim.RoomID)
}

// TestLodgingMorningDescent_RelocatesWokenLodger covers the happy path: a PC
// woken with a still-valid grant is relocated to the common room and a
// morning-descent PCRelocatedToCommon fires.
func TestLodgingMorningDescent_RelocatesWokenLodger(t *testing.T) {
	w, cancel := buildRoomTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	sleepingLodger(t, w, now, now.Add(24*time.Hour), true) // future grant, active
	readEvents := captureRelocations(t, w)

	if _, err := w.Send(sim.WakeExpiredPCSleepers(now)); err != nil {
		t.Fatalf("wake: %v", err)
	}

	if got := aliceRoom(t, w); got != 1 {
		t.Errorf("alice InsideRoomID after descent = %d, want 1 (common)", got)
	}
	events := readEvents()
	if len(events) != 1 {
		t.Fatalf("PCRelocatedToCommon emitted %d times, want 1", len(events))
	}
	e := events[0]
	if e.ActorID != "alice" || e.Reason != sim.LodgingReasonMorning {
		t.Errorf("event = %+v, want alice / %q", e, sim.LodgingReasonMorning)
	}
	if e.StructureID != "inn" || e.Text == "" {
		t.Errorf("event StructureID/Text = %q/%q, want inn / non-empty", e.StructureID, e.Text)
	}
}

// TestLodgingMorningDescent_SkipsCheckedOutLodger covers the load-bearing
// discriminator: a PC whose grant has lapsed (checkout) is NOT descended — the
// predicate fails, leaving EvictExpiredOccupants to relocate them. This is what
// guarantees descent and eviction never both relocate the same PC.
func TestLodgingMorningDescent_SkipsCheckedOutLodger(t *testing.T) {
	w, cancel := buildRoomTestWorld(t)
	defer cancel()

	now := time.Now().UTC()
	sleepingLodger(t, w, now, now.Add(-time.Hour), false) // lapsed grant, inactive
	readEvents := captureRelocations(t, w)

	if _, err := w.Send(sim.WakeExpiredPCSleepers(now)); err != nil {
		t.Fatalf("wake: %v", err)
	}

	if got := aliceRoom(t, w); got != 2 {
		t.Errorf("alice InsideRoomID = %d, want 2 (descent must NOT move a checked-out lodger)", got)
	}
	if events := readEvents(); len(events) != 0 {
		t.Errorf("descent fired for a checked-out lodger: %+v", events)
	}
}
