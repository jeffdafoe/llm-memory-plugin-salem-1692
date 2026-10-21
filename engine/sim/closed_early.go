package sim

import "time"

// closed_early.go — LLM-817. A keeper who leaves its post for its own night
// place late in its shift has closed for the day. Actor.ClosedUntil records it,
// so the to-work duty, the return-to-post steer and the door lines stop
// treating the keeper as missing from a shop that should be open.
//
// WHY. Before this, going home on shift read as "on shift, away from post"
// until the close: the shift-duty warrant woke the keeper about once a minute
// and every turn rendered "make your way to <post> now". Each wake was another
// chance to change its mind, so keepers went home, came back, and left again
// (live 10-19/10-20: Josiah home 19:25, back 20:04, home 20:05; Elizabeth home
// 18:24, back 37 s later). Closing early is fine; flip-flopping is not.
//
// Only a move the keeper chose counts: markClosedEarly runs on the move_to
// commit, not inside MoveActor, so an engine-driven walk (the red-tiredness
// march home, a route) never closes a business.

// DefaultCloseEarlyWindowMinutes is the close window when
// close_early_window_minutes is unset or 0: a move home counts as closing only
// in the last three hours of the shift. The live case it keeps out: the
// constable home at 12:08 after his rounds, out again on his own at 13:20, on a
// shift that closes at 18:00.
const DefaultCloseEarlyWindowMinutes = 180

// ClosedEarly is emitted by markClosedEarly after ClosedUntil is stamped. At is
// the commit instant (UTC); ClosedUntil is the end of the shift the keeper
// closed; WorkStructureID is the post it left.
type ClosedEarly struct {
	EventBase
	ActorID         ActorID
	WorkStructureID StructureID
	ClosedUntil     time.Time
	At              time.Time
}

func (ClosedEarly) isSimEvent() {}

// closeEarlyWindowMinutes resolves the close-window setting.
func closeEarlyWindowMinutes(w *World) int {
	if w.Settings.CloseEarlyWindowMinutes <= 0 {
		return DefaultCloseEarlyWindowMinutes
	}
	return w.Settings.CloseEarlyWindowMinutes
}

// ClosedForDay reports whether a's closed-early mark is live at now.
func ClosedForDay(a *Actor, now time.Time) bool {
	return a != nil && a.ClosedUntil != nil && a.ClosedUntil.After(now)
}

// shiftEndInstant is the next occurrence of the wall-clock minute endMinute after
// now, in the world timezone, as a UTC instant. It steps by calendar date, not by
// 24h or by elapsed minutes, so the close lands on the right local hour across a
// daylight-saving change (an overnight 15:00–03:00 shift closed at 01:30 on the
// spring-forward night ends at 03:00, not 04:00). An end minute inside the
// skipped or repeated hour itself (01:00–02:59 on a change night) resolves by
// time.Date's normalization; no village schedule ends there.
func shiftEndInstant(w *World, now time.Time, endMinute int) time.Time {
	loc := w.Settings.Location
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	end := time.Date(local.Year(), local.Month(), local.Day(), endMinute/60, endMinute%60, 0, 0, loc)
	if !end.After(now) {
		end = time.Date(local.Year(), local.Month(), local.Day()+1, endMinute/60, endMinute%60, 0, 0, loc)
	}
	return end.UTC()
}

// markClosedEarly stamps ClosedUntil when an agent keeper, on shift and inside
// the close window, has just committed a move to its own night place: its home,
// or the inn it rents a room at. A home that is also the post (the innkeeper)
// is not a close. A live mark is left as it is. MUST be called from inside a
// Command.Fn, after the move committed.
func markClosedEarly(w *World, a *Actor, target StructureID, now time.Time) {
	if a == nil || target == "" {
		return
	}
	if a.Kind != KindNPCStateful && a.Kind != KindNPCShared {
		return
	}
	if a.VisitorState != nil || a.WorkStructureID == "" || target == a.WorkStructureID {
		return
	}
	if ClosedForDay(a, now) {
		return
	}
	nightPlace := a.HomeStructureID != "" && target == a.HomeStructureID
	if !nightPlace && a.HomeStructureID == "" {
		if inn, ok := lodgingStructureFor(w, a, now); ok && inn == target {
			nightPlace = true
		}
	}
	if !nightPlace {
		return
	}
	start, end, ok := effectiveShiftWindow(w, a)
	if !ok {
		return
	}
	nowMinute := localMinuteOfDay(w, now)
	if !minuteInShiftWindow(start, end, nowMinute) {
		return
	}
	remaining := (end - nowMinute + 1440) % 1440
	if remaining > closeEarlyWindowMinutes(w) {
		return
	}
	until := shiftEndInstant(w, now, end)
	a.ClosedUntil = &until
	w.emit(&ClosedEarly{
		ActorID:         a.ID,
		WorkStructureID: a.WorkStructureID,
		ClosedUntil:     until,
		At:              now,
	})
}
