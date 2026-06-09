package sim

import (
	"fmt"
	"time"
)

// take_break.go — the take_break tool's substrate half, ZBBS-HOME-284 #4.
// Ports v1's agent_tick.go take_break dispatcher (ZBBS-133/141/154) into the
// in-memory world model.
//
// What take_break is: a daytime "step away from my post" verb. The VA calls it
// (the model-facing tool surface is handlers/take_break.go) to set a BreakUntil
// window — the actor stays awake but stops counting as open for business and
// recovers tiredness via the #1 sweep, which already credits while BreakUntil
// is open. It is the landing pad for work's on-shift tiredness warrant
// (warrant → deliberate → take_break → BreakUntil → recovery sweep).
//
// Relationship to sleep (#2): break and sleep share the BreakUntil/SleepingUntil
// "resting, leave alone" suppressor and the single mode-blind recovery rate.
// They are mutually exclusive in this machine — executeNPCSleep no-ops a
// sleeper, AutoBedAtHomeNPCs skips on-break actors, and TakeBreak rejects an
// actor already on break. Overnight rest is the sleep cycle's job; take_break
// is for stepping away during the day.
//
// SEAM E (settled with work, mail 9cf4bcf0): there is NO agent_override_until
// in v2. BreakUntil alone is the suppressor; the reactor + occupancy + recovery
// sweep all key on it. v1's agent_override_until stamp is deliberately not
// ported.
//
// DEFERRED from this slice (flagged for follow-up, NOT regressions):
//   - The spoken excuse / "shop closed" client broadcast (v1 spoke an excuse on
//     take_break). Same bucket as #2's deferred npc_sleep_started/ended WS
//     frames — the rest-state broadcast layer isn't wired in v2 yet. The Reason
//     rides on the TookBreak event for whoever surfaces it.
//   - The ZBBS-133 close-shop-with-vendor-inside eviction sequence (ask / assert
//     / eject customers). That depends on the entry-policy + eviction substrate,
//     which hasn't ported to engine/sim.
//   - end_break (manual early end). Natural expiry is handled by
//     ExpireEndedBreaks below; the warrant model means the VA sets a target hour
//     up front, so early-end is a later refinement.

// DefaultTakeBreakHours is the break length when the model omits until_hour.
// Matches v1's "NOW + 4h" default — long enough to be a real breather, short
// enough to reopen the same working day.
const DefaultTakeBreakHours = 4

// resolveBreakUntil computes the break-window end from a target hour.
//
//   - untilHour == 0: no target — default to now + DefaultTakeBreakHours. This
//     is how the handler signals "omitted" (it normalizes a nil pointer to 0).
//   - untilHour in [1, 23]: resolve to that hour TODAY in the world timezone.
//     Reject if it is already past (take_break is "back later today", not an
//     overnight closure — that's the sleep cycle). The error returns the
//     current time so the model has the anchor it needs to retry.
//   - untilHour < 0 or > 23: rejected. TakeBreak is exported and callable
//     directly (tests, future in-engine paths), so an out-of-range hour must
//     fail loudly rather than silently become a default break.
//
// The result is clamped to now + 24h as a defensive invariant. `loc` anchors
// the wall-clock hour comparison to the timezone the perception header
// advertised; nil falls back to UTC (matches localMinuteOfDay). `at` is the
// commit instant (UTC from the handler).
func resolveBreakUntil(loc *time.Location, untilHour int, at time.Time) (time.Time, error) {
	if untilHour < 0 || untilHour > 23 {
		return time.Time{}, fmt.Errorf(
			"until_hour=%d is out of range — use 1..23 for a target hour, or 0 for a %d-hour default break",
			untilHour, DefaultTakeBreakHours,
		)
	}
	if loc == nil {
		loc = time.UTC
	}
	now := at.In(loc)
	var breakUntil time.Time
	if untilHour > 0 {
		y, mo, d := now.Date()
		candidate := time.Date(y, mo, d, untilHour, 0, 0, 0, loc)
		if !candidate.After(now) {
			return time.Time{}, fmt.Errorf(
				"until_hour=%d is already past (the time is now %02d:%02d) — pick a later hour today, or omit until_hour for a %d-hour break. take_break is for stepping away during the day; overnight rest is handled automatically by the sleep cycle",
				untilHour, now.Hour(), now.Minute(), DefaultTakeBreakHours,
			)
		}
		breakUntil = candidate
	} else {
		breakUntil = now.Add(DefaultTakeBreakHours * time.Hour)
	}
	// Defensive cap: a break never exceeds 24h. Strictly redundant for the
	// until_hour path (a same-day future hour is always < 24h out) but it
	// bounds the default and any future caller.
	if maxUntil := now.Add(24 * time.Hour); breakUntil.After(maxUntil) {
		breakUntil = maxUntil
	}
	// Normalize to UTC for storage consistency with SleepingUntil (set via
	// now.Add, UTC) and the recovery cursor. The instant is unchanged —
	// .After/.Equal are zone-agnostic — but it keeps a future direct .Format()
	// on BreakUntil from surprising someone with a world-tz string (both
	// reviewers flagged this, ZBBS-HOME-284 #4).
	return breakUntil.UTC(), nil
}

// executeTakeBreak applies the break to an actor: sets BreakUntil, stamps the
// recovery cursor at the window's open, soft-sets the macro-state to
// StateResting, and refreshes occupancy on the structure the actor is in.
// Assumes the caller has already rejected an actor already on break. Runs on
// the world goroutine.
//
// Mirrors executeNPCSleep (#2) field-for-field — the only differences are the
// window field (BreakUntil vs SleepingUntil) and the state (StateResting vs
// StateSleeping).
func executeTakeBreak(w *World, a *Actor, breakUntil, at time.Time) {
	bu := breakUntil
	a.BreakUntil = &bu
	// Stamp the recovery cursor at window-open so the tiredness sweep counts
	// from the break's start, not from its next lazy-init pass (the #1 review
	// lesson — the sweep skips crediting on the pass where it first observes a
	// window without a cursor). Use the UTC instant `at` to match the sweep's
	// UTC clock; the stored BreakUntil instant is timezone-agnostic for the
	// .After comparisons the sweep / reactor / occupancy do.
	stamp := at
	a.LastTirednessRecoveryAt = &stamp
	// Soft-set the macro-state. Timestamps stay authoritative for reactor
	// eligibility; this just stops the enum lying and gives the StateResting
	// reactor gate (cascade/businessowner.go) defense-in-depth.
	a.State = StateResting
	// A keeper on break stops counting as "open for business": refresh the
	// structure so a non-night-only shop/tavern flips to its closed visual
	// (option (b) occupancy, shipped with #2). Night-only structures still
	// count everyone, so an inn doesn't darken when a guest takes a break.
	if a.InsideStructureID != "" {
		refreshStructureOccupancyState(w, a.InsideStructureID)
	}
}

// TakeBreak returns a Command that puts actorID on break until the resolved
// window end. Runs on the world goroutine.
//
// Rejections (surfaced to the model as tool errors so it can retry):
//   - actor not in world.
//   - already on break (BreakUntil still ahead of now) — accepting a second
//     take_break would silently extend the window; v1 ZBBS-154 closed this gate.
//   - until_hour already past today (see resolveBreakUntil).
//
// On success it emits TookBreak so the action log records the break (with its
// reason) like every other committing tool.
func TakeBreak(actorID ActorID, reason string, untilHour int, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, ok := w.Actors[actorID]
			if !ok {
				return nil, fmt.Errorf("TakeBreak: actor %q not in world", actorID)
			}
			loc := w.Settings.Location
			if loc == nil {
				loc = time.UTC
			}
			if a.BreakUntil != nil && a.BreakUntil.After(at) {
				return nil, fmt.Errorf(
					"you are already on break until %s — no need to call take_break again; pick a different action this turn",
					a.BreakUntil.In(loc).Format("15:04"),
				)
			}
			breakUntil, err := resolveBreakUntil(loc, untilHour, at)
			if err != nil {
				return nil, err
			}
			executeTakeBreak(w, a, breakUntil, at)
			w.emit(&TookBreak{
				ActorID:    actorID,
				Reason:     reason,
				BreakUntil: breakUntil,
				At:         at,
			})
			return nil, nil
		},
	}
}

// endBreak clears an actor's break: nils BreakUntil, drops the recovery cursor,
// resets the macro-state to idle, and refreshes occupancy (a darkened
// home==work shop re-lights when its keeper's break ends). The counterpart to
// wakeNPC (#2) on the break side.
//
// The SleepingUntil guard is defensive — break and sleep are mutually exclusive
// in this machine, so an actor ending a break should not also be asleep, but if
// some future path ever overlapped them this keeps endBreak from stripping a
// live sleep window's recovery cursor or its StateSleeping macro-state.
func endBreak(w *World, a *Actor) {
	a.BreakUntil = nil
	if a.SleepingUntil == nil {
		a.LastTirednessRecoveryAt = nil
		a.State = StateIdle
	}
	if a.InsideStructureID != "" {
		refreshStructureOccupancyState(w, a.InsideStructureID)
	}
}

// ExpireEndedBreaks clears the break on any actor whose BreakUntil has passed.
// Folded into the sleep ticker (runSleepTickIteration) rather than given its
// own goroutine — break and sleep are the same rest machine, and one
// minute-grained sweep covers both. Without this the recovery sweep's
// expired-window clear is the only thing that nils a stale BreakUntil, and the
// shop's occupancy wouldn't re-light until something else moved through it.
//
// It also nils a lapsed stay_open OpenUntil window (ZBBS-WORK-387). OpenUntil is
// inert once past (every reader gates on .After(now)), but clearing it here keeps
// a non-nil OpenUntil meaning a LIVE commitment, so future code can treat
// non-nil as "is committed" without re-checking expiry. No occupancy/state reset
// is needed — unlike a break, OpenUntil stamps nothing else on the actor.
func ExpireEndedBreaks(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			ended := 0
			for _, a := range w.Actors {
				if a.OpenUntil != nil && !a.OpenUntil.After(now) {
					a.OpenUntil = nil
				}
				if a.BreakUntil == nil || a.BreakUntil.After(now) {
					continue
				}
				endBreak(w, a)
				ended++
			}
			return ended, nil
		},
	}
}
