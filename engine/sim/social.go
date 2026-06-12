package sim

import (
	"context"
	"log"
	"time"
)

// social.go — tick-driver producer #4 (ZBBS-WORK-279 slice 4b): the social
// scheduler. A once-a-minute, EDGE-triggered mover that walks decorative NPCs
// to a social gathering spot at their social-window start and back home at its
// end. The persistence (the four social_* fields) shipped as slice 4a.
//
// NOT a warrant producer. Decorative NPCs (no VA) have no LLM to deliberate, so
// #4 dispatches deterministic walks — no warrant, no reactor, no LLM. It sits
// alongside the warrant producers (#1 need-threshold, #2 shift-duty), sharing
// only the once-a-minute ticker shape.
//
// EDGE-TRIGGERED (unlike #2's level-triggered duty): there are exactly two
// boundaries per day — enter@start, leave@end. SocialLastBoundaryAt records the
// last boundary already acted on; a boundary fires once and is then stamped so
// it can't re-fire (even on a no-op walk) for the rest of the window. The stamp
// is its own field, deliberately separate from any shift stamp, so the social
// and shift schedulers can never collide on precedence (v1's separation, ported
// verbatim).
//
// SUBJECTS: decorative NPCs (Kind == KindDecorative) with the social window
// fully configured AND a HomeStructureID. Agent NPCs are excluded by Kind — an
// operator re-populating social_tag on an agent NPC must not start a social
// walk that competes with the agent's own deliberation (v1 excluded them at the
// query level; v2 excludes by Kind, which is stronger — decoratives are the
// only no-VA movers).
//
// THE TWO ACTIONS (ported from v1 engine/social_scheduler.go):
//   - enter: walk to the NEAREST structure carrying the NPC's per-instance
//     social_tag (each NPC carries its own tag — not a global social center).
//     Skipped if already inside a structure with that tag.
//   - leave: walk back home — but ONLY if currently inside a social_tag
//     structure. This guard is load-bearing: an earlier v1 version walked every
//     NPC home at every leave boundary, yanking workers out of their shops when
//     an admin set social hours mid-day. Leave is the gathering-spot→home
//     transition, not a policy enforced from any location.
//
// The boundary is stamped even when the action is a no-op (already there / no
// tagged structure exists / not inside one to leave), so the scheduler doesn't
// re-evaluate every tick for the rest of the window.

// SocialTickerInterval — once a minute, matching RunShiftTicker / RunNeedsTicker
// / RunSleepTicker / RunPhaseTicker.
const SocialTickerInterval = time.Minute

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
// most recent boundary before a 00:30 `now` is yesterday's 22:00 enter.
// Shared by the social scheduler and the route-schedule trigger
// (RouteBoundaryDue in npc_route.go) — both are edge-triggered two-boundary
// movers over a minute-of-day window.
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

// structureHasTag reports whether structureID names a structure whose
// village-object placement carries `tag` (per-instance VillageObject.Tags,
// resolved through the shared-identity bridge). Empty structureID → false.
func structureHasTag(w *World, structureID StructureID, tag string) bool {
	if structureID == "" {
		return false
	}
	vobj, ok := w.VillageObjects[VillageObjectID(structureID)]
	if !ok {
		return false
	}
	return vobj.HasTag(tag)
}

// findNearestSocialStructure returns the structure nearest to the actor that
// (a) carries `tag` and (b) is a structure the NPC can stand inside (exists in
// w.Structures via the shared-identity bridge — a bare decorative object with
// the tag but no structure row is not a valid destination, since the leave
// guard keys on being INSIDE a tagged structure). ok=false when no tagged
// structure exists. Linear scan over VillageObjects — fine at village scale
// (~50); a spatial index lands with the loiter-pin port if it's ever needed.
func findNearestSocialStructure(w *World, a *Actor, tag string) (StructureID, bool) {
	var best StructureID
	var bestDist2 float64
	found := false
	// Compare in world pixels: the actor's position is a tile index, the
	// object's is world pixels, so project the actor to the tile centre in
	// pixel space before measuring. (Without this the two operands are on
	// different scales — 1 tile = TileSize px — and "nearest" picks the
	// wrong structure once more than one carries the tag.) This is a
	// destination search, not an at-a-pin attribution, so it stays a plain
	// nearest-tagged scan rather than ResolveLoiteringObject (which is
	// bounded by a Chebyshev radius and filters by DisplayName, not tag).
	ac := a.Pos.Center()
	for id, obj := range w.VillageObjects {
		if !obj.HasTag(tag) {
			continue
		}
		sid := StructureID(id)
		if _, isStructure := w.Structures[sid]; !isStructure {
			continue
		}
		dx := obj.Pos.X - ac.X
		dy := obj.Pos.Y - ac.Y
		d2 := dx*dx + dy*dy
		if !found || d2 < bestDist2 {
			bestDist2 = d2
			best = sid
			found = true
		}
	}
	return best, found
}

// socialMove computes the social mover's decision for one actor at `now`: the
// pure read of world + actor state, with no dispatch (the MoveActor call is the
// caller's, keyed on walkTo). Mirrors shiftDutyTarget's pure-decision shape.
//
//   - ok=false  → nothing to do (out of scope, no unprocessed boundary).
//   - ok=true   → an unprocessed boundary fired; the caller MUST stamp
//     SocialLastBoundaryAt = boundary (even when walkTo == "", so a no-op
//     boundary doesn't re-evaluate every tick for the rest of the window).
//   - walkTo != "" → in addition, walk (enter) that structure: the nearest
//     tagged structure on an enter boundary, or HomeStructureID on a leave.
//   - walkTo == "" with ok=true → stamp only (already at the gathering spot on
//     enter / not inside a tagged structure to leave from / no tagged structure
//     exists).
func socialMove(w *World, a *Actor, now time.Time) (walkTo StructureID, boundary time.Time, ok bool) {
	// Decoratives only — agent NPCs deliberate their own movement, PCs are
	// player-driven, visitors run their own ExpiresAt lifecycle.
	if a.Kind != KindDecorative {
		return "", time.Time{}, false
	}
	// Social window must be fully configured (all-or-none, enforced at save)
	// and the NPC needs a home to return to at the leave boundary.
	if a.SocialTag == "" || a.SocialStartMin == nil || a.SocialEndMin == nil {
		return "", time.Time{}, false
	}
	if a.HomeStructureID == "" {
		return "", time.Time{}, false
	}
	// Resting suppressor, for parity with the other producers. Decoratives
	// don't sleep/break today, so this is a cheap defensive no-op.
	if a.SleepingUntil != nil && a.SleepingUntil.After(now) {
		return "", time.Time{}, false
	}
	if a.BreakUntil != nil && a.BreakUntil.After(now) {
		return "", time.Time{}, false
	}

	boundary, isEnter, ok := mostRecentWindowBoundary(w, *a.SocialStartMin, *a.SocialEndMin, now)
	if !ok {
		return "", time.Time{}, false
	}
	// Already acted on this boundary (or a later one). nil stamp means we've
	// never processed a boundary, so the most recent one fires now.
	if a.SocialLastBoundaryAt != nil && !a.SocialLastBoundaryAt.Before(boundary) {
		return "", time.Time{}, false
	}

	if isEnter {
		// Enter: walk to the nearest tagged structure, unless already inside
		// one carrying the tag (then it's a stamp-only no-op).
		if !structureHasTag(w, a.InsideStructureID, a.SocialTag) {
			if target, found := findNearestSocialStructure(w, a, a.SocialTag); found {
				return target, boundary, true
			}
		}
		return "", boundary, true
	}
	// Leave: walk home ONLY if currently inside a tagged structure (the
	// load-bearing guard — don't yank an NPC home from anywhere else).
	if structureHasTag(w, a.InsideStructureID, a.SocialTag) {
		return a.HomeStructureID, boundary, true
	}
	return "", boundary, true
}

// SocialTick returns a Command that applies one pass of the social scheduler
// across all actors: dispatch the walk (if any) and stamp the processed
// boundary. Runs on the world goroutine, so the per-actor MoveActor dispatches
// are serialized.
func SocialTick(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			for _, a := range w.Actors {
				walkTo, boundary, ok := socialMove(w, a, now)
				if !ok {
					continue
				}
				if walkTo != "" {
					if _, err := MoveActor(a.ID, NewStructureEnterDestination(walkTo), false, now).Fn(w); err != nil {
						// Do NOT stamp on a failed walk — SocialLastBoundaryAt is
						// the sole edge re-fire guard, so stamping here would make
						// a transient MoveActor failure (e.g. a momentarily
						// unreachable target) silently skip this boundary for the
						// rest of the window. Leaving the stamp unset re-evaluates
						// the same boundary next tick and retries the walk.
						log.Printf("sim/social: walk %s -> %s: %v", a.ID, walkTo, err)
						continue
					}
				}
				// Stamp the processed boundary — a successful walk or a genuine
				// no-op (walkTo == "": already there / nothing tagged / not
				// inside one to leave). Stamping the no-op stops the boundary
				// re-evaluating every tick for the rest of the window. Persisted
				// (slice 4a) + deep-cloned, so a restart mid-window won't re-walk.
				stamped := boundary
				a.SocialLastBoundaryAt = &stamped
			}
			return nil, nil
		},
	}
}

// RunSocialTicker owns the social-scheduler goroutine: once a minute, submit a
// SocialTick. Same time.NewTicker idiom as RunShiftTicker / RunNeedsTicker.
// Returns when ctx is cancelled.
func RunSocialTicker(ctx context.Context, w *World) {
	t := time.NewTicker(SocialTickerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.beatTicker("social")
			if _, err := w.SendContext(ctx, SocialTick(time.Now().UTC())); err != nil {
				if ctx.Err() == nil {
					log.Printf("sim/social: tick failed: %v", err)
				}
			}
		}
	}
}
