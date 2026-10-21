package sim

import (
	"testing"
	"time"
)

// closed_early_internal_test.go — LLM-817: the closed-early mark's set rule
// (markClosedEarly), its clears (reopening at the post, the lapse sweep) and the
// engine readers (shiftDutyTarget, ShutHoursPhrase). The move_to commit path and
// the door line are covered end to end in closed_early_test.go.

// closedEarlyKeeper is an agent keeper of "shop" (09:00–21:00) who lives at
// "home" and stands outdoors.
func closedEarlyKeeper() *Actor {
	a := shiftNPC("josiah", KindNPCShared, "shop", "home", "")
	a.ScheduleStartMin = intptr(540)
	a.ScheduleEndMin = intptr(1260)
	return a
}

func eveningAt(hour, minute int) time.Time {
	return time.Date(2026, 10, 19, hour, minute, 30, 0, time.UTC)
}

func TestMarkClosedEarly(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(w *World, a *Actor)
		target    StructureID
		now       time.Time
		wantUntil *time.Time
	}{
		{
			name:      "home late in the shift closes until the shift end",
			target:    "home",
			now:       eveningAt(19, 25),
			wantUntil: ptrTime(time.Date(2026, 10, 19, 21, 0, 0, 0, time.UTC)),
		},
		{
			name:      "six minutes before close",
			target:    "home",
			now:       eveningAt(20, 54),
			wantUntil: ptrTime(time.Date(2026, 10, 19, 21, 0, 0, 0, time.UTC)),
		},
		{
			name:   "home at midday is not a close (outside the default window)",
			target: "home",
			now:    eveningAt(12, 8),
		},
		{
			name:      "a window of a whole day counts any time in the shift",
			setup:     func(w *World, a *Actor) { w.Settings.CloseEarlyWindowMinutes = 1440 },
			target:    "home",
			now:       eveningAt(12, 8),
			wantUntil: ptrTime(time.Date(2026, 10, 19, 21, 0, 0, 0, time.UTC)),
		},
		{
			name:   "somewhere other than home is an errand, not a close",
			target: "tavern",
			now:    eveningAt(19, 25),
		},
		{
			name:   "off shift",
			target: "home",
			now:    eveningAt(21, 30),
		},
		{
			name:   "home that is also the post (the innkeeper)",
			setup:  func(w *World, a *Actor) { a.HomeStructureID = "shop" },
			target: "shop",
			now:    eveningAt(19, 25),
		},
		{
			name:   "a decorative never closes",
			setup:  func(w *World, a *Actor) { a.Kind = KindDecorative },
			target: "home",
			now:    eveningAt(19, 25),
		},
		{
			name: "a live mark is not re-stamped",
			setup: func(w *World, a *Actor) {
				earlier := time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC)
				a.ClosedUntil = &earlier
			},
			target:    "home",
			now:       eveningAt(19, 25),
			wantUntil: ptrTime(time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC)),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := closedEarlyKeeper()
			w := sleepTestWorld(a)
			if c.setup != nil {
				c.setup(w, a)
			}
			markClosedEarly(w, a, c.target, c.now)
			switch {
			case c.wantUntil == nil && a.ClosedUntil != nil:
				t.Errorf("ClosedUntil = %v, want unset", *a.ClosedUntil)
			case c.wantUntil != nil && (a.ClosedUntil == nil || !a.ClosedUntil.Equal(*c.wantUntil)):
				t.Errorf("ClosedUntil = %v, want %v", a.ClosedUntil, *c.wantUntil)
			}
		})
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestShiftDutyTarget_ClosedEarlySuppressesToWork(t *testing.T) {
	a := closedEarlyKeeper()
	a.InsideStructureID = "home"
	w := sleepTestWorld(a)
	now := eveningAt(19, 30)

	until := time.Date(2026, 10, 19, 21, 0, 0, 0, time.UTC)
	a.ClosedUntil = &until
	if target, toWork, ok := shiftDutyTarget(w, a, 19*60+30, now); ok {
		t.Errorf("closed early: got duty (%q,%v), want none", target, toWork)
	}

	lapsed := now.Add(-time.Minute)
	a.ClosedUntil = &lapsed
	if target, toWork, ok := shiftDutyTarget(w, a, 19*60+30, now); !ok || target != "shop" || !toWork {
		t.Errorf("lapsed mark: got (%q,%v,%v), want (shop,true,true)", target, toWork, ok)
	}
}

func TestSetActorInsideStructure_ReopeningClearsClosedEarly(t *testing.T) {
	a := closedEarlyKeeper()
	w := sleepTestWorld(a)
	w.actorsByStructure = map[StructureID]map[ActorID]struct{}{}
	w.outdoorActors = map[ActorID]struct{}{a.ID: {}}
	until := time.Date(2026, 10, 19, 21, 0, 0, 0, time.UTC)

	a.ClosedUntil = &until
	setActorInsideStructure(w, a, "tavern")
	if a.ClosedUntil == nil {
		t.Fatal("entering another structure cleared the mark")
	}
	setActorInsideStructure(w, a, "shop")
	if a.ClosedUntil != nil {
		t.Errorf("walking back into the post left ClosedUntil = %v", *a.ClosedUntil)
	}
}

func TestExpireEndedBreaks_ClearsLapsedClosedEarly(t *testing.T) {
	live := closedEarlyKeeper()
	lapsed := shiftNPC("elizabeth", KindNPCShared, "farm", "house", "house")
	w := sleepTestWorld(live, lapsed)
	now := eveningAt(19, 30)
	future, past := now.Add(time.Hour), now.Add(-time.Minute)
	live.ClosedUntil, lapsed.ClosedUntil = &future, &past

	if _, err := ExpireEndedBreaks(now).Fn(w); err != nil {
		t.Fatalf("ExpireEndedBreaks: %v", err)
	}
	if live.ClosedUntil == nil {
		t.Error("a live mark was cleared")
	}
	if lapsed.ClosedUntil != nil {
		t.Errorf("a lapsed mark survived: %v", *lapsed.ClosedUntil)
	}
}

func TestShutHoursPhrase_ClosedToday(t *testing.T) {
	closed := []ShiftWindow{{Start: 540, End: 1260, ClosedToday: true}}
	if got, want := ShutHoursPhrase(closed, 19*60+30), "for the day — it opens at 9 in the morning"; got != want {
		t.Errorf("closed early, inside the window: %q, want %q", got, want)
	}
	// A second worker still in hours keeps the plain line: the place should be open.
	mixed := append(closed, ShiftWindow{Start: 540, End: 1260})
	if got := ShutHoursPhrase(mixed, 19*60+30); got != "" {
		t.Errorf("one worker closed, one in hours: %q, want \"\"", got)
	}
	// An overnight keeper who closed early after midnight opens again that afternoon.
	overnight := []ShiftWindow{{Start: 900, End: 180, ClosedToday: true}}
	if got, want := ShutHoursPhrase(overnight, 60), "until 3 in the afternoon"; got != want {
		t.Errorf("overnight, closed at 1am: %q, want %q", got, want)
	}
}

func TestKeeperShiftWindows_MarksClosedEarly(t *testing.T) {
	a := closedEarlyKeeper()
	w := sleepTestWorld(a)
	now := eveningAt(19, 30)
	until := now.Add(time.Hour)
	a.ClosedUntil = &until

	got := keeperShiftWindows(w, "shop", now)
	if len(got) != 1 || !got[0].ClosedToday {
		t.Fatalf("live mark: %+v, want one ClosedToday window", got)
	}
	// The trail passes a zero instant and must not read the mark back onto a walk.
	if got := keeperShiftWindows(w, "shop", time.Time{}); len(got) != 1 || got[0].ClosedToday {
		t.Errorf("zero instant: %+v, want the plain window", got)
	}
}

// TestShiftEndInstant_DaylightSaving pins the close to the local wall-clock
// hour across a clock change (code_review on LLM-817): adding the remaining
// minutes as elapsed time would land an hour late on the spring-forward night.
func TestShiftEndInstant_DaylightSaving(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	w := sleepTestWorld()
	w.Settings.Location = ny
	cases := []struct {
		name string
		now  time.Time
		end  int
		want time.Time
	}{
		{"spring forward, overnight shift closes at 3", time.Date(2026, 3, 8, 1, 30, 0, 0, ny), 180, time.Date(2026, 3, 8, 3, 0, 0, 0, ny)},
		{"fall back, evening close", time.Date(2026, 11, 1, 19, 25, 0, 0, ny), 1260, time.Date(2026, 11, 1, 21, 0, 0, 0, ny)},
		{"before midnight, close after midnight", time.Date(2026, 10, 19, 23, 30, 0, 0, ny), 180, time.Date(2026, 10, 20, 3, 0, 0, 0, ny)},
	}
	for _, c := range cases {
		if got := shiftEndInstant(w, c.now, c.end); !got.Equal(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got.In(ny), c.want)
		}
	}
}

// TestAdminChangesClearClosedEarly: the mark belongs to the post and the shift
// it closed, so reassigning either drops it (code_review on LLM-817).
func TestAdminChangesClearClosedEarly(t *testing.T) {
	a := closedEarlyKeeper()
	w := sleepTestWorld(a)
	w.Structures = map[StructureID]*Structure{"shop": {ID: "shop"}, "mill": {ID: "mill"}}
	until := eveningAt(21, 0)

	a.ClosedUntil = &until
	start, end := 600, 1200
	if _, err := SetActorSchedule(a.ID, &start, &end).Fn(w); err != nil {
		t.Fatalf("SetActorSchedule: %v", err)
	}
	if a.ClosedUntil != nil {
		t.Error("a schedule change left the mark")
	}

	a.ClosedUntil = &until
	if _, err := SetActorWorkStructure(a.ID, "mill").Fn(w); err != nil {
		t.Fatalf("SetActorWorkStructure: %v", err)
	}
	if a.ClosedUntil != nil {
		t.Error("a new workplace left the mark")
	}
}

// TestAdminSameValuesKeepClosedEarly: re-sending the current post or schedule is
// not a reassignment, so the mark survives (code_review round 2 on LLM-817).
func TestAdminSameValuesKeepClosedEarly(t *testing.T) {
	a := closedEarlyKeeper()
	w := sleepTestWorld(a)
	w.Structures = map[StructureID]*Structure{"shop": {ID: "shop"}}
	until := eveningAt(21, 0)
	a.ClosedUntil = &until

	start, end := *a.ScheduleStartMin, *a.ScheduleEndMin
	if _, err := SetActorSchedule(a.ID, &start, &end).Fn(w); err != nil {
		t.Fatalf("SetActorSchedule: %v", err)
	}
	if _, err := SetActorWorkStructure(a.ID, "shop").Fn(w); err != nil {
		t.Fatalf("SetActorWorkStructure: %v", err)
	}
	if a.ClosedUntil == nil {
		t.Error("an unchanged post or schedule cleared the mark")
	}
}
