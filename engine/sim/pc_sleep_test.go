package sim

import (
	"errors"
	"testing"
	"time"
)

// pc_sleep_test.go — ZBBS-WORK-324. The player-driven sleep mechanic: the
// paid-bedroom gate, the manual /pc/sleep + /pc/wake commands, the input cursor
// (touchPCInput → input-wake), the recovery/cap wake sweep, and the idle
// auto-bed sweep that is the primary way a lodger PC falls asleep.

// pcSleepWorld builds a test world with an inn holding two private bedrooms
// (rooms 1, 3) and a common tavern floor (room 2) — pcCanSleepHere resolves the
// PC's lodging GRANT against these (LLM-14: grant-based, not InsideRoomID-based).
func pcSleepWorld(actors ...*Actor) *World {
	w := sleepTestWorld(actors...)
	w.Structures = map[StructureID]*Structure{
		"inn": {ID: "inn", DisplayName: "Inn", Rooms: []*Room{
			{ID: 1, StructureID: "inn", Kind: RoomKindPrivate, Name: "bedroom_1"},
			{ID: 2, StructureID: "inn", Kind: RoomKindCommon, Name: "tavern"},
			{ID: 3, StructureID: "inn", Kind: RoomKindPrivate, Name: "bedroom_2"},
		}},
	}
	return w
}

// lodgerPC is a player present in the inn (common area — InsideRoomID 0, awake
// and public-scoped) holding an active ledger grant for private bedroom 1. The
// can-sleep-here baseline: the standing grant, not a check-in-stamped room, is
// what beds them (LLM-14). tiredness defaults to 20 (above the idle-auto-bed
// floor of 10).
func lodgerPC(id ActorID, expires time.Time) *Actor {
	return &Actor{
		ID:                id,
		Kind:              KindPC,
		LoginUsername:     string(id),
		InsideStructureID: "inn",
		Needs:             map[NeedKey]int{"tiredness": 20},
		RoomAccess: map[RoomAccessKey]*RoomAccess{
			{RoomID: 1, Source: AccessSourceLedger}: {
				RoomID: 1, Source: AccessSourceLedger, Active: true, ExpiresAt: &expires,
			},
		},
	}
}

// pcEventRecorder captures emitted events for assertions.
type pcEventRecorder struct{ events []Event }

func (r *pcEventRecorder) handle(_ *World, e Event) { r.events = append(r.events, e) }

func TestPCCanSleepHere(t *testing.T) {
	now := time.Date(2026, 5, 25, 22, 0, 0, 0, time.UTC)
	future := now.Add(72 * time.Hour)

	cases := []struct {
		name     string
		build    func() (*World, *Actor)
		wantRoom RoomID
		wantOK   bool
	}{
		{
			name:     "active grant for a private bedroom here -> can sleep (room 1)",
			build:    func() (*World, *Actor) { a := lodgerPC("p", future); return pcSleepWorld(a), a },
			wantRoom: 1,
			wantOK:   true,
		},
		{
			// LLM-14 headline: a lodger awake at the bar (common floor) still
			// resolves its granted bedroom — the grant, not where it stands, beds it.
			name: "awake in the common area, grant for the bedroom -> can sleep (room 1)",
			build: func() (*World, *Actor) {
				a := lodgerPC("p", future)
				a.InsideRoomID = 2 // standing on the tavern floor, not the bedroom
				return pcSleepWorld(a), a
			},
			wantRoom: 1,
			wantOK:   true,
		},
		{
			name: "outdoors (no structure) -> cannot sleep",
			build: func() (*World, *Actor) {
				a := lodgerPC("p", future)
				a.InsideStructureID = ""
				return pcSleepWorld(a), a
			},
			wantOK: false,
		},
		{
			name: "no ledger grant -> cannot sleep",
			build: func() (*World, *Actor) {
				a := lodgerPC("p", future)
				a.RoomAccess = nil
				return pcSleepWorld(a), a
			},
			wantOK: false,
		},
		{
			name:   "grant expired -> cannot sleep",
			build:  func() (*World, *Actor) { a := lodgerPC("p", now.Add(-time.Hour)); return pcSleepWorld(a), a },
			wantOK: false,
		},
		{
			// The grant's room is in the inn, but the PC stands in a different
			// structure — lodgerRoomAt requires the granted room to belong to the
			// PC's CURRENT structure.
			name: "grant is for a room in another structure -> cannot sleep",
			build: func() (*World, *Actor) {
				a := lodgerPC("p", future)
				a.InsideStructureID = "market"
				return pcSleepWorld(a), a
			},
			wantOK: false,
		},
		{
			// A grant on the common (non-private) room never beds — only private
			// bedrooms qualify.
			name: "grant is for a common (non-private) room -> cannot sleep",
			build: func() (*World, *Actor) {
				a := lodgerPC("p", future)
				expires := future
				a.RoomAccess = map[RoomAccessKey]*RoomAccess{
					{RoomID: 2, Source: AccessSourceLedger}: {
						RoomID: 2, Source: AccessSourceLedger, Active: true, ExpiresAt: &expires,
					},
				}
				return pcSleepWorld(a), a
			},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, a := tc.build()
			gotRoom, gotOK := pcCanSleepHere(w, a, now)
			if gotOK != tc.wantOK || gotRoom != tc.wantRoom {
				t.Errorf("pcCanSleepHere = (%d, %v), want (%d, %v)", gotRoom, gotOK, tc.wantRoom, tc.wantOK)
			}
		})
	}
}

func TestExecutePCSleep(t *testing.T) {
	now := time.Date(2026, 5, 25, 22, 0, 0, 0, time.UTC)
	a := lodgerPC("p", now.Add(72*time.Hour))
	w := pcSleepWorld(a)
	rec := &pcEventRecorder{}
	w.Subscribe(SubscriberFunc(rec.handle))

	if !executePCSleep(w, a, 1, now) {
		t.Fatal("executePCSleep should bed an awake PC")
	}
	if a.InsideRoomID != 1 {
		t.Errorf("InsideRoomID = %d, want 1 (bed-down stamps the granted bedroom, LLM-14)", a.InsideRoomID)
	}
	wantWake := now.Add(DefaultPCSleepMaxDurationHours * time.Hour)
	if a.SleepingUntil == nil || !a.SleepingUntil.Equal(wantWake) {
		t.Errorf("SleepingUntil = %v, want %v", a.SleepingUntil, wantWake)
	}
	if a.LastTirednessRecoveryAt == nil || !a.LastTirednessRecoveryAt.Equal(now) {
		t.Errorf("recovery cursor = %v, want %v", a.LastTirednessRecoveryAt, now)
	}
	if a.State != StateSleeping {
		t.Errorf("State = %q, want %q", a.State, StateSleeping)
	}
	if len(rec.events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(rec.events))
	}
	started, ok := rec.events[0].(*PCSleepStarted)
	if !ok {
		t.Fatalf("event 0 is %T, want *PCSleepStarted", rec.events[0])
	}
	if started.ActorID != "p" || !started.WakeAt.Equal(wantWake) {
		t.Errorf("PCSleepStarted = %+v, want ActorID=p WakeAt=%v", started, wantWake)
	}

	// Idempotent: a second call on a sleeping PC is a no-op and emits nothing.
	if executePCSleep(w, a, 1, now) {
		t.Error("executePCSleep on an already-sleeping PC should return false")
	}
	if len(rec.events) != 1 {
		t.Errorf("re-bedding emitted extra events: %d total", len(rec.events))
	}
}

func TestSleepPCCommand(t *testing.T) {
	now := time.Date(2026, 5, 25, 22, 0, 0, 0, time.UTC)
	future := now.Add(72 * time.Hour)

	t.Run("gate pass -> bedded with wake_at", func(t *testing.T) {
		a := lodgerPC("p", future)
		w := pcSleepWorld(a)
		res, err := SleepPC("p", now).Fn(w)
		if err != nil {
			t.Fatalf("SleepPC error: %v", err)
		}
		out := res.(PCSleepResult)
		if !out.Bedded || !out.WakeAt.Equal(now.Add(DefaultPCSleepMaxDurationHours*time.Hour)) {
			t.Errorf("PCSleepResult = %+v, want Bedded=true with the cap wake", out)
		}
	})

	t.Run("no paid bedroom grant -> ErrPCCannotSleepHere", func(t *testing.T) {
		a := lodgerPC("p", future)
		a.RoomAccess = nil // no standing grant -> nothing to bed into
		w := pcSleepWorld(a)
		_, err := SleepPC("p", now).Fn(w)
		if !errors.Is(err, ErrPCCannotSleepHere) {
			t.Errorf("err = %v, want ErrPCCannotSleepHere", err)
		}
	})

	t.Run("already sleeping -> no-op, no error", func(t *testing.T) {
		a := lodgerPC("p", future)
		w := pcSleepWorld(a)
		if _, err := SleepPC("p", now).Fn(w); err != nil {
			t.Fatalf("first SleepPC error: %v", err)
		}
		res, err := SleepPC("p", now).Fn(w)
		if err != nil {
			t.Fatalf("second SleepPC error: %v", err)
		}
		if res.(PCSleepResult).Bedded {
			t.Error("second SleepPC should report Bedded=false")
		}
	})

	t.Run("already sleeping stays a no-op even if the gate would now fail", func(t *testing.T) {
		// Idempotency must not depend on the location/payment gate: a PC bedded
		// down whose grant later expired (or who moved) must still no-op on a
		// second /pc/sleep, not get ErrPCCannotSleepHere. (code_review #3.)
		a := lodgerPC("p", future)
		w := pcSleepWorld(a)
		if _, err := SleepPC("p", now).Fn(w); err != nil {
			t.Fatalf("first SleepPC error: %v", err)
		}
		// Pull the bedroom out from under the sleeping PC.
		a.RoomAccess = nil
		a.InsideRoomID = 0
		res, err := SleepPC("p", now).Fn(w)
		if err != nil {
			t.Fatalf("second SleepPC should be a no-op, got error: %v", err)
		}
		if res.(PCSleepResult).Bedded {
			t.Error("second SleepPC on an already-sleeping PC should report Bedded=false")
		}
	})
}

func TestWakePCCommand(t *testing.T) {
	now := time.Date(2026, 5, 25, 22, 0, 0, 0, time.UTC)

	t.Run("sleeping PC -> woken with manual reason", func(t *testing.T) {
		a := lodgerPC("p", now.Add(72*time.Hour))
		w := pcSleepWorld(a)
		executePCSleep(w, a, 1, now)
		rec := &pcEventRecorder{}
		w.Subscribe(SubscriberFunc(rec.handle))

		res, err := WakePC("p", now).Fn(w)
		if err != nil || res.(bool) != true {
			t.Fatalf("WakePC = %v, %v; want true, nil", res, err)
		}
		if a.SleepingUntil != nil {
			t.Error("WakePC should clear SleepingUntil")
		}
		if a.InsideRoomID != 0 {
			t.Errorf("WakePC should clear InsideRoomID (player-driven wake drops the room scope, LLM-14); got %d", a.InsideRoomID)
		}
		if len(rec.events) != 1 {
			t.Fatalf("emitted %d events, want 1", len(rec.events))
		}
		ended := rec.events[0].(*PCSleepEnded)
		if ended.Reason != "manual" || ended.ActorID != "p" {
			t.Errorf("PCSleepEnded = %+v, want manual/p", ended)
		}
	})

	t.Run("awake PC -> no-op, no event", func(t *testing.T) {
		a := lodgerPC("p", now.Add(72*time.Hour))
		w := pcSleepWorld(a)
		rec := &pcEventRecorder{}
		w.Subscribe(SubscriberFunc(rec.handle))
		res, _ := WakePC("p", now).Fn(w)
		if res.(bool) {
			t.Error("WakePC on an awake PC should return false")
		}
		if len(rec.events) != 0 {
			t.Errorf("awake WakePC emitted %d events, want 0", len(rec.events))
		}
	})
}

func TestTouchPCInput(t *testing.T) {
	now := time.Date(2026, 5, 25, 22, 0, 0, 0, time.UTC)

	t.Run("stamps cursor on an awake PC, no wake event", func(t *testing.T) {
		a := lodgerPC("p", now.Add(72*time.Hour))
		w := pcSleepWorld(a)
		rec := &pcEventRecorder{}
		w.Subscribe(SubscriberFunc(rec.handle))
		TouchPCInput(w, "p", now)
		if a.LastPCInputAt == nil || !a.LastPCInputAt.Equal(now) {
			t.Errorf("LastPCInputAt = %v, want %v", a.LastPCInputAt, now)
		}
		if len(rec.events) != 0 {
			t.Errorf("awake touch emitted %d events, want 0", len(rec.events))
		}
	})

	t.Run("input-wakes a sleeping PC", func(t *testing.T) {
		a := lodgerPC("p", now.Add(72*time.Hour))
		w := pcSleepWorld(a)
		executePCSleep(w, a, 1, now)
		rec := &pcEventRecorder{}
		w.Subscribe(SubscriberFunc(rec.handle))

		later := now.Add(time.Minute)
		TouchPCInput(w, "p", later)
		if a.SleepingUntil != nil {
			t.Error("acting while asleep should clear SleepingUntil")
		}
		if a.InsideRoomID != 0 {
			t.Errorf("input-wake should clear InsideRoomID (LLM-14); got %d", a.InsideRoomID)
		}
		if a.LastPCInputAt == nil || !a.LastPCInputAt.Equal(later) {
			t.Errorf("LastPCInputAt = %v, want %v", a.LastPCInputAt, later)
		}
		if len(rec.events) != 1 {
			t.Fatalf("emitted %d events, want 1", len(rec.events))
		}
		ended := rec.events[0].(*PCSleepEnded)
		if ended.Reason != "input" {
			t.Errorf("reason = %q, want input", ended.Reason)
		}
	})

	t.Run("non-PC actor is a no-op", func(t *testing.T) {
		n := npc("n", KindNPCStateful)
		now2 := now
		n.SleepingUntil = &now2
		w := sleepTestWorld(n)
		rec := &pcEventRecorder{}
		w.Subscribe(SubscriberFunc(rec.handle))
		TouchPCInput(w, "n", now)
		if n.LastPCInputAt != nil {
			t.Error("TouchPCInput must not stamp a non-PC actor")
		}
		if len(rec.events) != 0 {
			t.Errorf("NPC touch emitted %d events, want 0", len(rec.events))
		}
	})
}

func TestWakeExpiredPCSleepers(t *testing.T) {
	now := time.Date(2026, 5, 25, 22, 0, 0, 0, time.UTC)

	t.Run("fully rested PC wakes (auto)", func(t *testing.T) {
		a := lodgerPC("p", now.Add(72*time.Hour))
		w := pcSleepWorld(a)
		executePCSleep(w, a, 1, now)
		a.Needs["tiredness"] = 0 // recovery has brought them to rested
		rec := &pcEventRecorder{}
		w.Subscribe(SubscriberFunc(rec.handle))

		WakeExpiredPCSleepers(now.Add(time.Hour)).Fn(w)
		if a.SleepingUntil != nil {
			t.Error("a rested PC should wake")
		}
		if len(rec.events) != 1 || rec.events[0].(*PCSleepEnded).Reason != "auto" {
			t.Errorf("want one PCSleepEnded(auto), got %v", rec.events)
		}
	})

	t.Run("safety cap fires", func(t *testing.T) {
		a := lodgerPC("p", now.Add(72*time.Hour))
		w := pcSleepWorld(a)
		executePCSleep(w, a, 1, now)
		// Still tired, but the cap instant has passed.
		past := now.Add(DefaultPCSleepMaxDurationHours*time.Hour + time.Minute)
		WakeExpiredPCSleepers(past).Fn(w)
		if a.SleepingUntil != nil {
			t.Error("the safety cap should wake a still-tired PC")
		}
	})

	t.Run("still tired and within cap -> stays asleep", func(t *testing.T) {
		a := lodgerPC("p", now.Add(72*time.Hour))
		w := pcSleepWorld(a)
		executePCSleep(w, a, 1, now)
		WakeExpiredPCSleepers(now.Add(time.Hour)).Fn(w)
		if a.SleepingUntil == nil {
			t.Error("a tired PC within the cap should stay asleep")
		}
	})

	t.Run("checkout: grant lapsed while sleeping -> woken (auto), room kept for eviction", func(t *testing.T) {
		a := lodgerPC("p", now.Add(72*time.Hour))
		w := pcSleepWorld(a)
		executePCSleep(w, a, 1, now)
		// Night's up: the ledger grant for the room the PC sleeps in lapses.
		a.RoomAccess[RoomAccessKey{RoomID: 1, Source: AccessSourceLedger}].Active = false
		rec := &pcEventRecorder{}
		w.Subscribe(SubscriberFunc(rec.handle))

		WakeExpiredPCSleepers(now.Add(time.Hour)).Fn(w) // not rested, within cap
		if a.SleepingUntil != nil {
			t.Error("a checked-out (grant lapsed) sleeping PC should wake")
		}
		if len(rec.events) != 1 || rec.events[0].(*PCSleepEnded).Reason != "auto" {
			t.Errorf("want one PCSleepEnded(auto), got %v", rec.events)
		}
		// LLM-14: the auto/checkout wake does NOT clear InsideRoomID — the separate
		// EvictExpiredOccupants sweep relocates the PC bedroom->common and narrates
		// the checkout off this still-set room scope.
		if a.InsideRoomID != 1 {
			t.Errorf("InsideRoomID = %d, want 1 kept after the checkout wake (eviction relocates it)", a.InsideRoomID)
		}
	})

	t.Run("ignores a sleeping NPC", func(t *testing.T) {
		n := npc("n", KindNPCStateful)
		capAt := now.Add(-time.Hour) // would wake if PC-eligible
		n.SleepingUntil = &capAt
		n.Needs["tiredness"] = 0
		w := sleepTestWorld(n)
		WakeExpiredPCSleepers(now).Fn(w)
		if n.SleepingUntil == nil {
			t.Error("WakeExpiredPCSleepers must not touch NPC sleepers")
		}
	})
}

func TestAutoBedIdleLodgerPCs(t *testing.T) {
	now := time.Date(2026, 5, 25, 22, 0, 0, 0, time.UTC)
	future := now.Add(72 * time.Hour)
	idleEnough := now.Add(-(DefaultPCIdleSleepMinutes + 1) * time.Minute)

	t.Run("idle tired lodger is bedded into its granted room", func(t *testing.T) {
		a := lodgerPC("p", future)
		a.LastPCInputAt = &idleEnough
		w := pcSleepWorld(a)
		rec := &pcEventRecorder{}
		w.Subscribe(SubscriberFunc(rec.handle))

		res, _ := AutoBedIdleLodgerPCs(now).Fn(w)
		if res.(int) != 1 || a.SleepingUntil == nil {
			t.Fatalf("idle lodger should be bedded; bedded=%v sleeping=%v", res, a.SleepingUntil)
		}
		if a.InsideRoomID != 1 {
			t.Errorf("auto-bed should stamp the granted bedroom; InsideRoomID = %d, want 1", a.InsideRoomID)
		}
		if len(rec.events) != 1 || rec.events[0].(*PCSleepStarted).ActorID != "p" {
			t.Errorf("want one PCSleepStarted for p, got %v", rec.events)
		}
	})

	t.Run("at exactly the idle cutoff is not bedded (strict, v1 < parity)", func(t *testing.T) {
		a := lodgerPC("p", future)
		exact := now.Add(-DefaultPCIdleSleepMinutes * time.Minute) // == idleCutoff
		a.LastPCInputAt = &exact
		w := pcSleepWorld(a)
		AutoBedIdleLodgerPCs(now).Fn(w)
		if a.SleepingUntil != nil {
			t.Error("a PC idle exactly at the cutoff should NOT bed until the next tick (strict older-than)")
		}
	})

	t.Run("recently active PC is not bedded", func(t *testing.T) {
		a := lodgerPC("p", future)
		recent := now.Add(-time.Minute)
		a.LastPCInputAt = &recent
		w := pcSleepWorld(a)
		AutoBedIdleLodgerPCs(now).Fn(w)
		if a.SleepingUntil != nil {
			t.Error("a recently-active PC should not be auto-bedded")
		}
	})

	t.Run("PC that never acted (nil cursor) is not bedded", func(t *testing.T) {
		a := lodgerPC("p", future) // LastPCInputAt nil
		w := pcSleepWorld(a)
		AutoBedIdleLodgerPCs(now).Fn(w)
		if a.SleepingUntil != nil {
			t.Error("a PC with no stamped input should not be auto-bedded")
		}
	})

	t.Run("not tired enough is not bedded", func(t *testing.T) {
		a := lodgerPC("p", future)
		a.LastPCInputAt = &idleEnough
		a.Needs["tiredness"] = DefaultPCIdleSleepMinTiredness - 1
		w := pcSleepWorld(a)
		AutoBedIdleLodgerPCs(now).Fn(w)
		if a.SleepingUntil != nil {
			t.Error("a PC below the tiredness floor should not be auto-bedded")
		}
	})

	t.Run("idle tired lodger awake at the bar is bedded into its granted room (LLM-14)", func(t *testing.T) {
		a := lodgerPC("p", future)
		a.InsideRoomID = 2 // on the tavern floor, not yet in the bedroom
		a.LastPCInputAt = &idleEnough
		w := pcSleepWorld(a)
		AutoBedIdleLodgerPCs(now).Fn(w)
		if a.SleepingUntil == nil {
			t.Error("an idle tired lodger with a standing grant should be auto-bedded off the grant")
		}
		if a.InsideRoomID != 1 {
			t.Errorf("auto-bed should stamp the granted bedroom; InsideRoomID = %d, want 1", a.InsideRoomID)
		}
	})
}
