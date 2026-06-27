package sim

import "time"

// schedule_window.go — minute-of-day window-boundary math for the engine's
// edge-triggered, two-boundary schedulers. The route-schedule trigger
// (RouteBoundaryDue in npc_route.go) is the sole caller. These helpers were
// extracted here when the decorative social scheduler — their original home —
// was removed (LLM-150); they are general window math, not social-specific.

// windowBoundaryAt returns the wall-clock instant of minuteOfDay on the local
// day containing `day`, in the world timezone. Built with time.Date (not
// Duration arithmetic) so it stays correct across DST transitions — minute-of-
// day is a wall-clock concept, not an elapsed-duration one. minuteOfDay may
// exceed 59; time.Date normalizes it into the hour.
func windowBoundaryAt(w *World, day time.Time, minuteOfDay int) time.Time {
	loc := w.Settings.Location
	if loc == nil {
		loc = time.UTC
	}
	d := day.In(loc)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, minuteOfDay, 0, 0, loc)
}

// mostRecentWindowBoundary returns the most recent window boundary at-or-before
// `now` for the [startMin, endMin) window, and whether that boundary is the
// ENTER (start) one. It considers today's and yesterday's enter/leave instants
// so a window straddling midnight (e.g. 22:00–02:00) resolves correctly — the
// most recent boundary before a 00:30 `now` is yesterday's 22:00 enter. Used by
// the route-schedule trigger (RouteBoundaryDue in npc_route.go), an
// edge-triggered two-boundary mover over a minute-of-day window.
//
// ok=false when startMin == endMin: a zero-width window has no distinct
// enter/leave boundaries, so there's nothing to drive (same "equal endpoints =
// empty, not all-day" convention the shift window uses).
func mostRecentWindowBoundary(w *World, startMin, endMin int, now time.Time) (boundary time.Time, isEnter, ok bool) {
	if startMin == endMin {
		return time.Time{}, false, false
	}
	yesterday := now.AddDate(0, 0, -1)
	type cand struct {
		t       time.Time
		isEnter bool
	}
	cands := [...]cand{
		{windowBoundaryAt(w, now, startMin), true},
		{windowBoundaryAt(w, now, endMin), false},
		{windowBoundaryAt(w, yesterday, startMin), true},
		{windowBoundaryAt(w, yesterday, endMin), false},
	}
	var best cand
	found := false
	for _, c := range cands {
		if c.t.After(now) {
			continue
		}
		if !found || c.t.After(best.t) {
			best = c
			found = true
		}
	}
	if !found {
		return time.Time{}, false, false
	}
	return best.t, best.isEnter, true
}
