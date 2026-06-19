package sim

import (
	"fmt"
	"time"
)

// maxStayOpenWindow bounds a stay_open commitment: a keeper may extend at most
// this far past "now". stay_open is for keeping a business open a few hours past
// close — including across midnight — not for a full-day commitment. Without the
// bound a model that names the CURRENT or a just-past hour rolls forward a full
// day (LLM-39: until_hour=20 named at 20:01 resolved to 20:00 the NEXT day, an
// ~24h all-nighter). A genuine cross-midnight commit ("until 1am" at 9pm = +4h)
// sits well inside this window.
const maxStayOpenWindow = 8 * time.Hour

// stay_open.go — the stay_open tool's substrate half (ZBBS-WORK-387).
//
// What stay_open is: the overnight wind-down-side twin of take_break. A keeper
// calls it (the model-facing tool surface is handlers/stay_open.go) to commit to
// keeping its business open PAST the end of its shift, until a chosen hour —
// instead of closing up and heading off for the night. It stamps an OpenUntil
// window which SUPPRESSES the routine off-shift wind-down (the go-home / to-inn
// shift duty in shiftDutyTarget, and the renderDutySteer perception cue) so the
// level-triggered shift producer stops re-ticking the keeper home every cycle.
//
// Inverse semantics to take_break: take_break says "I'm resting, don't expect
// service"; stay_open says "I'm working late, don't send me to rest". They share
// the *time.Time-window shape but nothing else — stay_open sets NO macro-state
// (the keeper stays awake and present), does NO occupancy refresh (presence
// already keeps the shop open), and stamps NO recovery cursor (it is not rest).
//
// The commitment is NOT absolute: it yields to exhaustion. shiftDutyTarget gates
// the OpenUntil suppression on `!atPeakTiredness`, so at peak the needs floor
// (the homed MarchHome in classifyAgentDuty; the lodger's inn nudge; the
// homeless RouteHomelessToRest sweep) wins and the keeper closes early. The
// resolved window is also capped at 24h ahead. (Design: shared
// tasks/.../zbbs-133-keeper-closing-decision/design, Decisions log 2026-06-09.)
//
// TRANSIENT — OpenUntil is deliberately not checkpointed (see Actor.OpenUntil):
// restart-loss reverts the keeper to the safe default close-on-schedule.

// resolveOpenUntil computes the stay-open window end from a target hour.
//
//   - untilHour in [0, 23]: resolve to the NEXT occurrence of that hour in the
//     world timezone — that hour today if it is still future, else the same hour
//     tomorrow. This is the inverse of take_break's resolveBreakUntil, which
//     REJECTS a past hour: staying open overnight is exactly the cross-midnight
//     case ("open until 1am" committed at 9pm) take_break forbids, so here a
//     past hour rolls forward a day rather than erroring.
//   - untilHour < 0 or > 23: rejected. StayOpen is exported and callable directly
//     (tests, future in-engine paths), so an out-of-range hour must fail loudly.
//
// The resolved window is bounded to maxStayOpenWindow past `at`: a commitment
// that lands further out — because the model named the current or a just-past
// hour, which rolls a full day forward — is REJECTED so the keeper falls back to
// closing on schedule rather than committing to an all-nighter (LLM-39). `loc`
// anchors the wall-clock hour to the timezone the perception header advertised;
// nil falls back to UTC (matches localMinuteOfDay). `at` is the commit instant
// (UTC).
func resolveOpenUntil(loc *time.Location, untilHour int, at time.Time) (time.Time, error) {
	if untilHour < 0 || untilHour > 23 {
		return time.Time{}, fmt.Errorf(
			"until_hour=%d is out of range — use 0..23 for the hour you will close (e.g. 23 for 11pm, 1 for 1am the next morning)",
			untilHour,
		)
	}
	if loc == nil {
		loc = time.UTC
	}
	now := at.In(loc)
	y, mo, d := now.Date()
	candidate := time.Date(y, mo, d, untilHour, 0, 0, 0, loc)
	// Next occurrence: a target hour not strictly in the future today is read as
	// the same wall-clock hour TOMORROW (the keeper is committing past midnight).
	// Roll the DATE (not a +24h add) so the local close hour stays exact across a
	// DST transition, where "tomorrow" can be 23h or 25h of elapsed time.
	if !candidate.After(now) {
		candidate = time.Date(y, mo, d+1, untilHour, 0, 0, 0, loc)
	}
	// Bound the commitment to maxStayOpenWindow. A model that names the CURRENT or
	// a just-past hour (until_hour=20 at 20:01) rolled forward a full ~24h above;
	// reject it so the keeper closes on schedule instead of standing at the post
	// all night (LLM-39). A real cross-midnight commit is well inside the window.
	if candidate.Sub(now) > maxStayOpenWindow {
		// Name the resolved DAY (weekday) and the elapsed hours so the message
		// reveals the all-nighter — "resolves to Fri 20:00, about 23 hours from
		// now" — rather than a bare "20:00" that hides the next-day rollover.
		return time.Time{}, fmt.Errorf(
			"until_hour=%d resolves to %s, about %d hours from now — staying open is for extending a few hours past your close, not a full day. Choose an hour within the next %d hours, or simply close up for the night.",
			untilHour, candidate.Format("Mon 15:04"), int(candidate.Sub(now).Hours()), int(maxStayOpenWindow.Hours()),
		)
	}
	// Normalize to UTC for storage consistency with BreakUntil/SleepingUntil. The
	// instant is unchanged — .After/.Equal are zone-agnostic — but it keeps a
	// future direct .Format() on OpenUntil from surprising someone with a
	// world-tz string.
	return candidate.UTC(), nil
}

// executeStayOpen stamps the OpenUntil window on an actor. Assumes the caller has
// already rejected an actor already committed. Runs on the world goroutine.
//
// Deliberately minimal vs executeTakeBreak: NO StateResting (the keeper stays
// awake and working), NO occupancy refresh (the keeper is present, so the shop
// is already open — presence is the open/closed signal), NO recovery cursor
// (staying open is not rest). OpenUntil is the entire stamp.
func executeStayOpen(a *Actor, openUntil time.Time) {
	ou := openUntil
	a.OpenUntil = &ou
}

// StayOpen returns a Command that commits actorID to staying open until the
// resolved window end. Runs on the world goroutine.
//
// Rejections (surfaced to the model as tool errors so it can retry):
//   - actor not in world.
//   - already committed (OpenUntil still ahead of now) — accepting a second
//     stay_open would silently move the window; mirror take_break's already-on-
//     break gate.
//   - until_hour out of range (see resolveOpenUntil).
//
// On success it emits StayingOpen so the action log records the commitment (with
// its reason) like every other committing tool.
func StayOpen(actorID ActorID, reason string, untilHour int, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, ok := w.Actors[actorID]
			if !ok {
				return nil, fmt.Errorf("StayOpen: actor %q not in world", actorID)
			}
			loc := w.Settings.Location
			if loc == nil {
				loc = time.UTC
			}
			if a.OpenUntil != nil && a.OpenUntil.After(at) {
				return nil, fmt.Errorf(
					"you have already committed to staying open until %s — no need to call stay_open again; pick a different action this turn",
					a.OpenUntil.In(loc).Format("15:04"),
				)
			}
			openUntil, err := resolveOpenUntil(loc, untilHour, at)
			if err != nil {
				return nil, err
			}
			executeStayOpen(a, openUntil)
			w.emit(&StayingOpen{
				ActorID:   actorID,
				Reason:    reason,
				OpenUntil: openUntil,
				At:        at,
			})
			return nil, nil
		},
	}
}
