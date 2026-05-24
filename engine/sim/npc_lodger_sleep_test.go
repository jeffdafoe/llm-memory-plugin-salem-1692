package sim

import (
	"testing"
	"time"
)

// npc_lodger_sleep_test.go — ZBBS-HOME-296 2c. The lodger arm of the NPC
// auto-sleep machine: a boarder (no HomeStructureID, active ledger RoomAccess)
// beds at the inn it rents, but only at bedtime (off the dawn/dusk-fallback
// window) so a midday visit to the inn's tavern floor doesn't sleep-dart it,
// and wakes on the morning boundary. Homed NPCs are unchanged.

// lodgerSleepWorld extends sleepTestWorld with the inn structure (one private
// bedroom) and a 07:00–19:00 day window — what the bedtime gate reads.
func lodgerSleepWorld(actors ...*Actor) *World {
	w := sleepTestWorld(actors...)
	w.Settings.DawnTime = "07:00"
	w.Settings.DuskTime = "19:00"
	w.Structures = map[StructureID]*Structure{
		"inn": {ID: "inn", DisplayName: "Inn", Rooms: []*Room{
			{ID: 1, StructureID: "inn", Kind: RoomKindPrivate, Name: "bedroom_1"},
		}},
	}
	return w
}

// lodgerNPC is a boarder: no home, standing in the inn it rents, holding an
// active ledger grant for the inn's private bedroom that expires in the future.
func lodgerNPC(id ActorID, expires time.Time) *Actor {
	return &Actor{
		ID:                id,
		Kind:              KindNPCStateful,
		HomeStructureID:   "",
		InsideStructureID: "inn",
		Needs:             map[NeedKey]int{"tiredness": 20},
		RoomAccess: map[RoomAccessKey]*RoomAccess{
			{RoomID: 1, Source: AccessSourceLedger}: {
				RoomID: 1, Source: AccessSourceLedger, Active: true, ExpiresAt: &expires,
			},
		},
	}
}

// TestNpcSleepHere is the unified sleep-target resolution table: home vs lodger,
// and the bedtime window that is the whole point of the lodger arm.
func TestNpcSleepHere(t *testing.T) {
	night := time.Date(2026, 5, 22, 22, 0, 0, 0, time.UTC)  // minute 1320 — off the day window
	midday := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC) // minute 720 — inside the day window
	future := night.Add(72 * time.Hour)

	cases := []struct {
		name  string
		build func() (*World, *Actor)
		at    time.Time
		want  bool
	}{
		{
			name:  "homed unscheduled, night -> beds (unchanged)",
			build: func() (*World, *Actor) { a := npc("h", KindNPCStateful); return lodgerSleepWorld(a), a },
			at:    night,
			want:  true,
		},
		{
			// Homed NPCs keep the always-off-when-unscheduled rule: the window
			// change is lodger-only, so a homed NPC still beds on any off-shift
			// arrival, including midday.
			name:  "homed unscheduled, midday -> still beds (unchanged)",
			build: func() (*World, *Actor) { a := npc("h", KindNPCStateful); return lodgerSleepWorld(a), a },
			at:    midday,
			want:  true,
		},
		{
			name:  "lodger at its inn, bedtime -> beds",
			build: func() (*World, *Actor) { a := lodgerNPC("l", future); return lodgerSleepWorld(a), a },
			at:    night,
			want:  true,
		},
		{
			// The headline case: the inn doubles as the tavern, so a lodger
			// arriving at midday for a meal must NOT be bedded.
			name:  "lodger at its inn, midday tavern visit -> NOT bedded",
			build: func() (*World, *Actor) { a := lodgerNPC("l", future); return lodgerSleepWorld(a), a },
			at:    midday,
			want:  false,
		},
		{
			name: "lodger standing in a structure it does not rent -> not bedded",
			build: func() (*World, *Actor) {
				a := lodgerNPC("l", future)
				a.InsideStructureID = "market" // grant is for the inn
				return lodgerSleepWorld(a), a
			},
			at:   night,
			want: false,
		},
		{
			name: "homeless with no grant -> not bedded",
			build: func() (*World, *Actor) {
				a := npc("x", KindNPCStateful)
				a.HomeStructureID = ""
				a.InsideStructureID = "inn"
				return lodgerSleepWorld(a), a
			},
			at:   night,
			want: false,
		},
		{
			name: "expired lodger grant -> not bedded",
			build: func() (*World, *Actor) {
				a := lodgerNPC("l", night.Add(-time.Hour)) // already expired
				return lodgerSleepWorld(a), a
			},
			at:   night,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, a := tc.build()
			if got := npcSleepHere(w, a, tc.at); got != tc.want {
				t.Errorf("npcSleepHere = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLodgerAutoSleepOnArrival exercises the lodger branch through the public
// arrival subscriber: bedtime arrival beds, midday arrival does not.
func TestLodgerAutoSleepOnArrival(t *testing.T) {
	future := time.Date(2026, 5, 25, 22, 0, 0, 0, time.UTC)
	night := time.Date(2026, 5, 22, 22, 0, 0, 0, time.UTC)
	midday := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)

	t.Run("arrives at its inn at bedtime -> bedded", func(t *testing.T) {
		a := lodgerNPC("l", future)
		w := lodgerSleepWorld(a)
		handleAutoSleepOnArrival(w, &ActorArrived{ActorID: "l", FinalStructureID: "inn", At: night})
		if a.SleepingUntil == nil {
			t.Error("lodger arriving at its inn at bedtime should be bedded")
		}
	})
	t.Run("arrives at its inn at midday -> not bedded", func(t *testing.T) {
		a := lodgerNPC("l", future)
		w := lodgerSleepWorld(a)
		handleAutoSleepOnArrival(w, &ActorArrived{ActorID: "l", FinalStructureID: "inn", At: midday})
		if a.SleepingUntil != nil {
			t.Error("lodger arriving at its inn at midday (tavern visit) should NOT be bedded")
		}
	})
}

// TestLodgerWakeAtDawn verifies a bedded lodger wakes on the dawn/dusk morning
// boundary (not just the 12h cap), while a homed unscheduled NPC's wake is
// unchanged (cap-only).
func TestLodgerWakeAtDawn(t *testing.T) {
	dawn := time.Date(2026, 5, 23, 8, 0, 0, 0, time.UTC)   // minute 480 — inside the day window
	night := time.Date(2026, 5, 23, 23, 0, 0, 0, time.UTC) // minute 1380 — off the day window

	t.Run("lodger wakes at the morning boundary", func(t *testing.T) {
		a := lodgerNPC("l", dawn.Add(72*time.Hour))
		capAt := dawn.Add(6 * time.Hour) // cap not yet reached
		a.SleepingUntil = &capAt
		w := lodgerSleepWorld(a)
		WakeExpiredNPCSleepers(dawn).Fn(w)
		if a.SleepingUntil != nil {
			t.Error("lodger should wake at the dawn boundary")
		}
	})
	t.Run("lodger stays asleep at night before the cap", func(t *testing.T) {
		a := lodgerNPC("l", night.Add(72*time.Hour))
		capAt := night.Add(6 * time.Hour)
		a.SleepingUntil = &capAt
		w := lodgerSleepWorld(a)
		WakeExpiredNPCSleepers(night).Fn(w)
		if a.SleepingUntil == nil {
			t.Error("lodger should stay asleep at night (off the day window) until the cap")
		}
	})
	t.Run("homed unscheduled NPC does NOT wake on the day window (unchanged)", func(t *testing.T) {
		a := npc("h", KindNPCStateful) // homed, unscheduled
		capAt := dawn.Add(6 * time.Hour)
		a.SleepingUntil = &capAt
		w := lodgerSleepWorld(a)
		WakeExpiredNPCSleepers(dawn).Fn(w)
		if a.SleepingUntil == nil {
			t.Error("homed unscheduled NPC should wake only on the 12h cap, not the day window")
		}
	})
	t.Run("homeless non-lodger sleeper does NOT wake on the day window", func(t *testing.T) {
		// No home and NO active grant — the wake-side lodger predicate must be
		// false, so this sleeper keeps the cap-only wake. Guards against the
		// wake condition outrunning the bed condition (code_review, HOME-296 2c).
		a := npc("x", KindNPCStateful)
		a.HomeStructureID = ""
		a.InsideStructureID = "inn"
		a.RoomAccess = nil // not a lodger
		capAt := dawn.Add(6 * time.Hour)
		a.SleepingUntil = &capAt
		w := lodgerSleepWorld(a)
		WakeExpiredNPCSleepers(dawn).Fn(w)
		if a.SleepingUntil == nil {
			t.Error("homeless non-lodger sleeper should stay asleep until the cap, not wake on the day window")
		}
	})
}
