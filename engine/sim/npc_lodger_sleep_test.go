package sim

import (
	"testing"
	"time"
)

// npc_lodger_sleep_test.go — ZBBS-HOME-296 2c, LLM-14. The lodger arm of the NPC
// auto-sleep machine: a boarder (no HomeStructureID, active ledger RoomAccess)
// beds at the inn it rents, but only at its night bedtime (inside the
// [LodgingBedtimeHour, DawnTime) lodger night window) so a midday visit to the
// inn's tavern floor doesn't sleep-dart it, and wakes when the window closes at
// dawn. The night window is decoupled from the work shift, so a SCHEDULED lodger
// no longer beds at its shift-end (the LLM-14 force-sleep root). Homed NPCs are
// unchanged.

// lodgerSleepWorld extends sleepTestWorld with the inn structure (one private
// bedroom), a 07:00 dawn, and a 22:00 lodger bedtime — so the lodger night
// window the bed/wake gates read is [22:00, 07:00).
func lodgerSleepWorld(actors ...*Actor) *World {
	w := sleepTestWorld(actors...)
	w.Settings.DawnTime = "07:00"
	w.Settings.DuskTime = "19:00"
	w.Settings.LodgingBedtimeHour = 22
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

// scheduledLodgerNPC is a boarder that ALSO works a day shift — the LLM-14 case.
// Ezekiel the blacksmith: shift 07:00–16:00 (minutes 420–960), lodging at the
// tavern. Before LLM-14 the bed gate read his WORK shift and force-slept him at
// 16:00 forge-close; now it reads the lodger night window like any other lodger.
func scheduledLodgerNPC(id ActorID, expires time.Time) *Actor {
	a := lodgerNPC(id, expires)
	start, end := 7*60, 16*60
	a.ScheduleStartMin = &start
	a.ScheduleEndMin = &end
	return a
}

// TestNpcSleepHere is the unified sleep-target resolution table: home vs lodger,
// and the bedtime window that is the whole point of the lodger arm.
func TestNpcSleepHere(t *testing.T) {
	night := time.Date(2026, 5, 22, 22, 0, 0, 0, time.UTC)      // minute 1320 — the lodger bedtime
	midday := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)     // minute 720 — inside the awake day
	forgeClose := time.Date(2026, 5, 22, 16, 0, 0, 0, time.UTC) // minute 960 — a scheduled lodger's shift-end
	evening := time.Date(2026, 5, 22, 20, 0, 0, 0, time.UTC)    // minute 1200 — after dusk, before bedtime
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
			// LLM-14 regression: a scheduled lodger must NOT be bedded when its
			// work shift ends (16:00 forge-close) — that was the force-sleep root.
			name:  "scheduled lodger at its shift-end (16:00) -> NOT bedded (LLM-14)",
			build: func() (*World, *Actor) { a := scheduledLodgerNPC("l", future); return lodgerSleepWorld(a), a },
			at:    forgeClose,
			want:  false,
		},
		{
			name:  "scheduled lodger at bedtime (22:00) -> beds (LLM-14)",
			build: func() (*World, *Actor) { a := scheduledLodgerNPC("l", future); return lodgerSleepWorld(a), a },
			at:    night,
			want:  true,
		},
		{
			// A lodger keeps later hours than the village: bedded at the 22:00
			// night window, not the moment dusk (19:00) passes.
			name:  "lodger in the evening before bedtime (20:00) -> NOT bedded",
			build: func() (*World, *Actor) { a := lodgerNPC("l", future); return lodgerSleepWorld(a), a },
			at:    evening,
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
	t.Run("scheduled lodger wakes at dawn, not its later shift-start (LLM-14)", func(t *testing.T) {
		a := scheduledLodgerNPC("l", dawn.Add(72*time.Hour))
		start, end := 10*60, 18*60 // a 10:00 shift — starts AFTER the 08:00 dawn wake
		a.ScheduleStartMin = &start
		a.ScheduleEndMin = &end
		capAt := dawn.Add(6 * time.Hour)
		a.SleepingUntil = &capAt
		w := lodgerSleepWorld(a)
		WakeExpiredNPCSleepers(dawn).Fn(w) // dawn = 08:00, before the 10:00 shift
		if a.SleepingUntil != nil {
			t.Error("scheduled lodger should wake at dawn (night-window close), not wait for its 10:00 shift-start")
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

// withActiveHuddle attaches an active (un-concluded) huddle to the named actors
// so actorInActiveHuddle reads true — the "mid-conversation" condition the
// deliberate-retire backstop holds for (LLM-36). Populates both Huddles and
// actorsByHuddle (separate maps) so LeaveHuddle on bed-down has its index.
func withActiveHuddle(w *World, id HuddleID, members ...ActorID) {
	mem := make(map[ActorID]struct{}, len(members))
	idx := make(map[ActorID]struct{}, len(members))
	for _, m := range members {
		mem[m] = struct{}{}
		idx[m] = struct{}{}
		if a := w.Actors[m]; a != nil {
			a.CurrentHuddleID = id
		}
	}
	if w.Huddles == nil {
		w.Huddles = map[HuddleID]*Huddle{}
	}
	if w.actorsByHuddle == nil {
		w.actorsByHuddle = map[HuddleID]map[ActorID]struct{}{}
	}
	w.Huddles[id] = &Huddle{ID: id, Members: mem}
	w.actorsByHuddle[id] = idx
}

// TestAutoBedAtHomeNPCs_DeliberateRetire covers the LLM-36 backstop demotion: a
// lodger still conversing at bedtime is given a grace margin to voice a goodnight
// and turn in itself before the engine beds it; an idle lodger beds at once (no
// goodnight to voice), and a lodger still talking PAST the grace margin is bedded
// regardless so it can never never-sleep. The hold is lodger-only — a homed NPC
// in a huddle at bedtime still beds.
func TestAutoBedAtHomeNPCs_DeliberateRetire(t *testing.T) {
	bedtime := time.Date(2026, 5, 22, 22, 0, 0, 0, time.UTC)    // minute 1320 — window open, grace fresh (0 < 45)
	pastGrace := time.Date(2026, 5, 22, 22, 50, 0, 0, time.UTC) // minute 1370 — 50 min in, past the 45 grace
	future := bedtime.Add(72 * time.Hour)

	t.Run("conversing lodger within grace -> held (not bedded)", func(t *testing.T) {
		l := lodgerNPC("l", future)
		player := npc("p", KindPC) // a co-present peer to bid goodnight to; PCs aren't auto-bedded
		w := lodgerSleepWorld(l, player)
		withActiveHuddle(w, "h1", "l", "p")
		res, _ := AutoBedAtHomeNPCs(bedtime).Fn(w)
		if n := res.(int); n != 0 {
			t.Fatalf("bedded = %d, want 0 (held for deliberate retire)", n)
		}
		if l.SleepingUntil != nil {
			t.Error("a conversing lodger within the grace margin should be held, not engine-bedded")
		}
	})

	t.Run("idle lodger at bedtime -> bedded at once", func(t *testing.T) {
		l := lodgerNPC("l", future)
		w := lodgerSleepWorld(l) // no huddle — nothing to wind down
		res, _ := AutoBedAtHomeNPCs(bedtime).Fn(w)
		if n := res.(int); n != 1 {
			t.Fatalf("bedded = %d, want 1 (idle lodger beds at once)", n)
		}
		if l.SleepingUntil == nil {
			t.Error("an idle lodger at bedtime should be bedded by the backstop")
		}
	})

	t.Run("lodger in a companionless huddle -> bedded (no one to bid goodnight)", func(t *testing.T) {
		l := lodgerNPC("l", future)
		w := lodgerSleepWorld(l)
		withActiveHuddle(w, "h1", "l") // sole member — no companion present
		res, _ := AutoBedAtHomeNPCs(bedtime).Fn(w)
		if n := res.(int); n != 1 {
			t.Fatalf("bedded = %d, want 1 (a sole-member huddle has no companion — bed now)", n)
		}
		if l.SleepingUntil == nil {
			t.Error("a lodger with no co-present companion should be bedded, not held")
		}
	})

	t.Run("conversing lodger past the grace margin -> bedded regardless", func(t *testing.T) {
		l := lodgerNPC("l", future)
		player := npc("p", KindPC)
		w := lodgerSleepWorld(l, player)
		withActiveHuddle(w, "h1", "l", "p")
		res, _ := AutoBedAtHomeNPCs(pastGrace).Fn(w)
		if n := res.(int); n != 1 {
			t.Fatalf("bedded = %d, want 1 (past grace — hard backstop)", n)
		}
		if l.SleepingUntil == nil {
			t.Error("a lodger still conversing past the grace margin must be bedded so it never never-sleeps")
		}
	})

	t.Run("homed NPC conversing at bedtime -> bedded (demotion is lodger-only)", func(t *testing.T) {
		h := npc("h", KindNPCStateful) // homed, inside its home, unscheduled
		w := lodgerSleepWorld(h)
		withActiveHuddle(w, "h1", "h")
		res, _ := AutoBedAtHomeNPCs(bedtime).Fn(w)
		if n := res.(int); n != 1 {
			t.Fatalf("bedded = %d, want 1 (homed NPC is not subject to the lodger retire grace)", n)
		}
		if h.SleepingUntil == nil {
			t.Error("a homed NPC should bed at once — the deliberate-retire hold is lodger-only")
		}
	})
}
