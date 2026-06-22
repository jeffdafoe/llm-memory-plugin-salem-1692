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
			// At-own-business precondition (LLM-66): stay_open is a keeper-only
			// commitment, coherent only for the owner of a business while
			// physically inside it — presence is the open/closed signal, so an
			// OpenUntil stamped by a non-keeper, or by a keeper who is away, would
			// suppress that actor's go-home wind-down (shiftDutyTarget) while
			// nothing of theirs is actually open. Observed live as a blacksmith
			// calling stay_open from under a Shade Tree. The advertising gate
			// (handlers/tool_gating.go) keeps the tool off the menu unless the
			// off-shift cue offers it; this is the dispatch-side backstop, since
			// that gating is advertising-only and the command stays directly
			// dispatchable. Mirrors the cue's own AtOwnBusiness predicate
			// (perception/build.go) and the canonical keeper-at-post identity used
			// across the cascade (BusinessownerState != nil AND inside the work
			// structure): WorkStructureID alone marks an assigned workplace, not
			// ownership, so a non-owner worker (staff/apprentice) is excluded too.
			// Unlike accept_pay (legitimately dispatchable unadvertised), an
			// off-business stay_open is never valid, so it is rejected, not merely
			// un-advertised.
			if a.BusinessownerState == nil {
				return nil, fmt.Errorf(
					"stay_open is for a keeper of their own business — you do not keep one, so there is nothing for you to keep open; pick a different action this turn",
				)
			}
			if a.WorkStructureID == "" || a.InsideStructureID != a.WorkStructureID {
				return nil, fmt.Errorf(
					"you can only keep your business open while you are there — you are away from it right now. Return to your business to stay open, or simply close on schedule.",
				)
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
			// No-op reject (LLM-40): a stay_open that does not actually reach past
			// the keeper's regular close is the diligence-reflex misfire — committing
			// to "open until 9" when you already close at 9, with no customer to
			// serve. Only meaningful while still BEFORE the active close: once the
			// scheduled close has passed, any future window resolveOpenUntil produced
			// is a genuine extension. Scoped to keepers with an explicit schedule (a
			// dawn/dusk-window keeper has no precise close to compare against).
			if a.ScheduleEndMin != nil {
				endMin := *a.ScheduleEndMin
				nowLocal := at.In(loc)
				y, mo, d := nowLocal.Date()
				closeAt := time.Date(y, mo, d, endMin/60, endMin%60, 0, 0, loc)
				// Overnight schedule (close hour before start hour, e.g. 18:00–03:00):
				// while on the pre-midnight side of the shift the active close is
				// TOMORROW's end, not today's already-past one — otherwise the reject
				// is skipped and the keeper can no-op stay_open until its own close
				// (code_review). Schedule bounds are both-set-or-both-nil (repo
				// validation), so the nil guard is defensive.
				if a.ScheduleStartMin != nil {
					startMin := *a.ScheduleStartMin
					nowMin := nowLocal.Hour()*60 + nowLocal.Minute()
					if endMin < startMin && nowMin >= startMin {
						closeAt = closeAt.AddDate(0, 0, 1)
					}
				}
				if nowLocal.Before(closeAt) && !openUntil.After(closeAt) {
					return nil, fmt.Errorf(
						"you are already open until %s — stay_open is for keeping shop PAST your usual close, not until it. Name a later hour if you mean to stay late, or simply close on schedule.",
						ClockHourProse(endMin),
					)
				}
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
