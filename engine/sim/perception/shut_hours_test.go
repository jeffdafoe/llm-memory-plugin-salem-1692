package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// shut_hours_test.go — LLM-771. A shut business outside its keepers' hours
// says when it opens, at the door and in the trail. The live trigger: Constable
// Gideon Marsh walked his round to the Tavern at midday (2026-10-14), read only
// "The Tavern is shut — no one is tending it.", asked around, and brought John
// Ellis before the magistrates as missing. John was abed — his shift opens at 3 in
// the afternoon — exactly as the village is built.

// turnedAwayAtTavernWithHours is turnedAwayOutdoorsAtShutTavern (Moses at the shut
// Tavern's door, a just-found-shut trail row, John abed inside) with John on a
// [start, end) shift and the village clock at now.
func turnedAwayAtTavernWithHours(start, end, now int) (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	snap, id, warrants := turnedAwayOutdoorsAtShutTavern()
	john := snap.Actors[sim.ActorID("john")]
	john.ScheduleStartMin = intp(start)
	john.ScheduleEndMin = intp(end)
	snap.LocalMinuteOfDay = intp(now)
	return snap, id, warrants
}

// tavernBeforeOpening is the live case: John's real 15:00–03:00 shift, midday.
func tavernBeforeOpening() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return turnedAwayAtTavernWithHours(900, 180, 720)
}

// tavernAfterClose puts the keeper on a 07:00–16:00 day shift (the smith's hours)
// at 17:00 — shut for the day, opening tomorrow morning.
func tavernAfterClose() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return turnedAwayAtTavernWithHours(420, 960, 1020)
}

// tavernShutInHours is John abed at 16:00, inside his 15:00–03:00 shift. The place
// should be open, so the plain "no one is tending it" line stays: the absence is
// the news.
func tavernShutInHours() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return turnedAwayAtTavernWithHours(900, 180, 960)
}

// TestShutLineNamesHoursOnlyOutsideKeeperHours is the LLM-771 cross-scenario
// invariant. At every shut business in the matrix: when every keeper of the place
// is off shift (checked with sim.OnShiftAtMinute, not the phrase builder), the
// plain "no one is tending it" line must NOT render and the door line must name
// the opening hour; when any keeper is on shift, the plain line stays. The matrix
// must exercise both branches.
func TestShutLineNamesHoursOnlyOutsideKeeperHours(t *testing.T) {
	const plain = "is shut — no one is tending it"
	var sawOutside, sawInside bool
	for _, sc := range perceptionScenarios {
		snap, actorID, warrants := sc.build()
		p := Build(snap, actorID, warrants)
		if p.Surroundings.LocationDeadEnd != DeadEndShutBusiness || snap.LocalMinuteOfDay == nil {
			continue
		}
		stID := snap.Actors[actorID].InsideStructureID
		if stID == "" {
			_, stID = findLoiterStructure(snap, snap.Actors[actorID])
		}
		scheduled, onShift := false, false
		for _, w := range snap.Actors {
			if w == nil || w.WorkStructureID != stID || w.ScheduleStartMin == nil || w.ScheduleEndMin == nil {
				continue
			}
			scheduled = true
			if sim.OnShiftAtMinute(w.ScheduleStartMin, w.ScheduleEndMin, *snap.LocalMinuteOfDay) {
				onShift = true
			}
		}
		if !scheduled {
			continue
		}
		got := combinedPrompt(Render(p, DefaultRenderConfig()))
		if onShift {
			sawInside = true
			if !strings.Contains(got, plain) {
				t.Errorf("scenario %q: a keeper is on shift, want the plain %q line", sc.name, plain)
			}
			continue
		}
		sawOutside = true
		if strings.Contains(got, plain) {
			t.Errorf("scenario %q: every keeper is off shift, but the plain %q line rendered", sc.name, plain)
		}
		if !strings.Contains(got, " is shut until ") && !strings.Contains(got, " is shut for the day — it opens at ") {
			t.Errorf("scenario %q: every keeper is off shift, but the door line names no opening hour", sc.name)
		}
	}
	if !sawOutside || !sawInside {
		t.Errorf("matrix must exercise both branches: sawOutside=%v sawInside=%v", sawOutside, sawInside)
	}
}

// TestShutHoursPhrase pins sim.ShutHoursPhrase: "" inside any keeper's hours,
// "until X" before today's opening, "for the day — it opens at X" after close,
// wrap-midnight windows, the soonest of several keepers, and the empty cases.
func TestShutHoursPhrase(t *testing.T) {
	john := sim.ShiftWindow{Start: 900, End: 180}  // 15:00–03:00
	smith := sim.ShiftWindow{Start: 420, End: 960} // 07:00–16:00
	cases := []struct {
		name    string
		windows []sim.ShiftWindow
		now     int
		want    string
	}{
		{"wrap window, midday → until", []sim.ShiftWindow{john}, 720, "until 3 in the afternoon"},
		{"wrap window, just after close at 3 → until", []sim.ShiftWindow{john}, 180, "until 3 in the afternoon"},
		{"wrap window, after midnight inside hours", []sim.ShiftWindow{john}, 60, ""},
		{"wrap window, at opening", []sim.ShiftWindow{john}, 900, ""},
		{"day window, early morning → until", []sim.ShiftWindow{smith}, 300, "until 7 in the morning"},
		{"day window, evening → for the day", []sim.ShiftWindow{smith}, 1020, "for the day — it opens at 7 in the morning"},
		{"day window, at close → for the day", []sim.ShiftWindow{smith}, 960, "for the day — it opens at 7 in the morning"},
		{"day window, inside hours", []sim.ShiftWindow{smith}, 600, ""},
		{"opens at noon", []sim.ShiftWindow{{Start: 720, End: 1080}}, 600, "until noon"},
		{"half-hour opening", []sim.ShiftWindow{{Start: 450, End: 1170}}, 300, "until 7:30 in the morning"},
		{"one keeper on shift → in hours", []sim.ShiftWindow{john, smith}, 600, ""},
		{"soonest of two keepers", []sim.ShiftWindow{john, smith}, 200, "until 7 in the morning"},
		{"empty window ignored", []sim.ShiftWindow{{Start: 600, End: 600}}, 300, ""},
		{"no windows", nil, 720, ""},
		{"clock out of range", []sim.ShiftWindow{john}, 1440, ""},
		{"invalid window beside a valid one is skipped", []sim.ShiftWindow{{Start: 1500, End: 100}, smith}, 300, "until 7 in the morning"},
		{"negative window beside a valid one is skipped", []sim.ShiftWindow{{Start: -60, End: 600}, smith}, 1020, "for the day — it opens at 7 in the morning"},
		{"all-day (stay_open) window → in hours", []sim.ShiftWindow{smith, sim.ShiftWindowAllDay}, 1020, ""},
	}
	for _, c := range cases {
		if got := sim.ShutHoursPhrase(c.windows, c.now); got != c.want {
			t.Errorf("%s: ShutHoursPhrase = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestSnapshotMinuteAt pins the step-back from the published clock: whole minutes
// count from the minute boundaries of both instants, it wraps past midnight, a
// later instant clamps to now, and an unknown clock is not ok.
func TestSnapshotMinuteAt(t *testing.T) {
	published := time.Date(2026, 10, 14, 4, 10, 30, 0, time.UTC)
	snap := &sim.Snapshot{PublishedAt: published, LocalMinuteOfDay: intp(10)} // 00:10 local
	cases := []struct {
		name string
		at   time.Time
		want int
	}{
		{"same instant", published, 10},
		{"earlier in the same minute", published.Add(-20 * time.Second), 10},
		{"crossing a minute boundary by seconds", published.Add(-31 * time.Second), 9},
		{"one hour ago, across midnight", published.Add(-time.Hour), 1390},
		{"after the publish clamps to now", published.Add(time.Minute), 10},
	}
	for _, c := range cases {
		got, ok := snapshotMinuteAt(snap, c.at)
		if !ok || got != c.want {
			t.Errorf("%s: snapshotMinuteAt = %d, %v; want %d, true", c.name, got, ok, c.want)
		}
	}
	if _, ok := snapshotMinuteAt(&sim.Snapshot{PublishedAt: published}, published); ok {
		t.Errorf("nil LocalMinuteOfDay: want ok=false")
	}
}

// TestShutDoorLine_DawnDuskFallbackAndStayOpen pins the two window sources the
// scheduled goldens don't reach. A keeper with no schedule row is day-active on the
// world dawn/dusk window (shiftWindowBounds), so before dawn the door names dawn as
// the opening. A keeper off shift but holding an unlapsed stay_open has chosen to
// keep the place open, so its absence keeps the plain line.
func TestShutDoorLine_DawnDuskFallbackAndStayOpen(t *testing.T) {
	doorLine := func(snap *sim.Snapshot, id sim.ActorID) string {
		return deadEndClause(Build(snap, id, nil).Surroundings)
	}

	snap, id, _ := turnedAwayOutdoorsAtShutTavern() // John unscheduled
	snap.DawnMinute, snap.DuskMinute, snap.DawnDuskMinuteOK = 420, 1140, true
	snap.LocalMinuteOfDay = intp(300)
	if got, want := doorLine(snap, id), "The Tavern is shut until 7 in the morning."; got != want {
		t.Errorf("dawn/dusk fallback before dawn: door line = %q, want %q", got, want)
	}

	snap, id, _ = tavernBeforeOpening()
	until := snap.PublishedAt.Add(time.Hour)
	snap.Actors[sim.ActorID("john")].OpenUntil = &until
	if got, want := doorLine(snap, id), "The Tavern is shut — no one is tending it."; got != want {
		t.Errorf("keeper on stay_open: door line = %q, want %q", got, want)
	}
}
