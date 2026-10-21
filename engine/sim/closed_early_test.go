package sim_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// closed_early_test.go — LLM-817 end to end: a move_to home late in the shift
// closes the keeper for the day, and a customer at the door reads that the
// place is shut for the day rather than that no one is tending it.

func closedUntilOf(t *testing.T, w *sim.World, id sim.ActorID) *time.Time {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		if a.ClosedUntil == nil {
			return (*time.Time)(nil), nil
		}
		t := *a.ClosedUntil
		return &t, nil
	}})
	if err != nil {
		t.Fatalf("read ClosedUntil: %v", err)
	}
	return res.(*time.Time)
}

// seedWalkerKeeper makes buildMoveTestWorld's walker an agent who keeps the
// gazebo 09:00–21:00 and lives at the inn.
func seedWalkerKeeper(t *testing.T, w *sim.World) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["walker"]
		a.Kind = sim.KindNPCShared
		a.HomeStructureID = "inn"
		a.WorkStructureID = "gazebo"
		start, end := 540, 1260
		a.ScheduleStartMin, a.ScheduleEndMin = &start, &end
		world.Settings.Location = time.UTC
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestMoveToHome_LateInShiftClosesForTheDay(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	seedWalkerKeeper(t, w)

	// "home" is the keyword the model uses; anchorKeywordTarget resolves it to the
	// walker's HomeStructureID ("inn") before the move commits.
	now := time.Date(2026, 10, 19, 19, 25, 29, 0, time.UTC)
	if _, err := w.Send(sim.MoveToStructure("walker", "home", now)); err != nil {
		t.Fatalf("move_to home: %v", err)
	}
	got := closedUntilOf(t, w, "walker")
	want := time.Date(2026, 10, 19, 21, 0, 0, 0, time.UTC)
	if got == nil || !got.Equal(want) {
		t.Errorf("ClosedUntil = %v, want %v", got, want)
	}
}

func TestMoveToHome_MiddayDoesNotClose(t *testing.T) {
	w, cancel, _ := buildMoveTestWorld(t)
	defer cancel()
	seedWalkerKeeper(t, w)

	now := time.Date(2026, 10, 19, 12, 8, 0, 0, time.UTC)
	if _, err := w.Send(sim.MoveToStructure("walker", "inn", now)); err != nil {
		t.Fatalf("move_to inn: %v", err)
	}
	if got := closedUntilOf(t, w, "walker"); got != nil {
		t.Errorf("ClosedUntil = %v, want unset (outside the close window)", *got)
	}
}

func TestMoveToDestination_ShutForTheDayWhenKeeperClosedEarly(t *testing.T) {
	w, cancel := buildShutTavernWorld(t)
	defer cancel()

	now := time.Date(2026, 10, 14, 16, 0, 0, 0, time.UTC)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.Location = time.UTC
		world.Actors["hannah"].InsideStructureID = "tavern"
		start, end := 900, 180
		world.Actors["john"].ScheduleStartMin = &start
		world.Actors["john"].ScheduleEndMin = &end
		until := now.Add(11 * time.Hour)
		world.Actors["john"].ClosedUntil = &until
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := w.Send(sim.MoveToDestination("hannah", "the Tavern", nil, sim.RememberedPlaces{}, now))
	want := "shut for the day — it opens at 3 in the afternoon"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("no-op = %v, want it to contain %q", err, want)
	}
}
