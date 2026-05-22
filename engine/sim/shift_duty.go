package sim

import (
	"context"
	"log"
	"time"
)

// shift_duty.go — tick-driver producer #2 (ZBBS-WORK-278): the shift/duty
// producer. A once-a-minute, level-triggered check that drives NPCs to and from
// their workplace on their shift boundaries.
//
// LEVEL-TRIGGERED (not edge-triggered): each scan re-evaluates the standing
// condition, so one mechanism gives BOTH the opening nudge and the keep-trying
// retry — the v2 form of v1 npc_scheduler's shouldNudgeReturnToWork /
// shouldNudgeReturnHome.
//
// AGENT/DECORATIVE SPLIT (ported from v1 npc_scheduler.go, which walks
// decoratives mechanically but only NUDGES agent NPCs and lets the LLM decide
// how to walk):
//   - Decorative NPCs are MECHANICALLY walked (MoveActor) — no warrant, no LLM.
//   - Agent NPCs get a WarrantKindShiftDuty warrant; they deliberate and
//     self-walk via the move_to tool (home). move_to is not yet wired into the
//     agent tool surface, so the agent half is INERT until it lands — but the
//     warrant is correct to stamp now (mirrors #1 shipping before take_break's
//     warrant path was exercised).
//
// UNSCHEDULED NPCs fall back to the world's dawn/dusk day window (decision B,
// 2026-05-22) so they're active by day and home/asleep at night rather than
// perpetually parked.
//
// REST SUPPRESSION: sleeping / on-break NPCs are skipped (SleepingUntil /
// BreakUntil) — the same rest suppressor the reactor and #1 producer use.
// NEED SUPPRESSION: an agent with a mild-or-worse need is NOT nudged to WORK
// (let it resolve the need first — v1 WORK-234), but IS still sent home (going
// home is how it rests). Decoratives carry no real needs (the needs tick skips
// them; their need rows are inert seed values), so they are never need-suppressed.
//
// DEFERRED from this slice:
//   - Per-NPC lateness offset that staggers arrivals (v1 lateness_window_minutes)
//     — a feel refinement; without it all due NPCs head out on the same minute.
//   - The "your shift started / ended; head to your workplace" PERCEPTION CUE
//     that tells a warranted agent why it ticked and what to do — slice 2b,
//     converges with home's move_to (cue + tool together make the agent half work).

// ShiftDutyWarrantReason is the WarrantReason stamped when an agent NPC is on
// the wrong side of its shift boundary (on-shift away from work, or off-shift
// still at work). ToWork distinguishes the two so the 2b perception cue can
// render the right line. Zero-sourced (a wall-clock boundary is not an event);
// the per-actor WarrantedSince gate prevents double-stamp. Mirrors
// IdleBackstopWarrantReason / NeedThresholdWarrantReason.
type ShiftDutyWarrantReason struct {
	ToWork bool
}

func (ShiftDutyWarrantReason) isWarrantReason()           {}
func (ShiftDutyWarrantReason) Kind() WarrantKind          { return WarrantKindShiftDuty }
func (ShiftDutyWarrantReason) DedupDiscriminator() uint64 { return 0 }

// ShiftTickerInterval — once a minute, matching RunNeedsTicker / RunSleepTicker
// / RunPhaseTicker. ~60s duty-retry granularity, easily tuned later.
const ShiftTickerInterval = time.Minute

// minuteInShiftWindow reports whether nowMinute (0..1439) falls in [start, end),
// handling wrap-midnight windows (start > end, e.g. a 16:00–03:00 tavern shift).
// Mirrors the wrap logic in npc_sleep.go's isActorOnShift — and shares the same
// schedule fields with it — so start == end is an EMPTY window (never on shift),
// NOT all-day; kept consistent with the sleep machine on purpose. An all-day
// window is a full [start, end) span (e.g. 0..1440). Takes an explicit window so
// it serves both per-NPC schedules and the dawn/dusk fallback.
func minuteInShiftWindow(start, end, nowMinute int) bool {
	if start <= end {
		return nowMinute >= start && nowMinute < end
	}
	return nowMinute >= start || nowMinute < end
}

// effectiveShiftWindow returns the actor's [start, end) minute-of-day shift
// window: its own schedule when both bounds are set, else the world's dawn/dusk
// day window (decision B — unscheduled NPCs are day-active). ok=false only when
// dawn/dusk fail to parse (the phase system logs that at load).
func effectiveShiftWindow(w *World, a *Actor) (start, end int, ok bool) {
	if a.ScheduleStartMin != nil && a.ScheduleEndMin != nil {
		return *a.ScheduleStartMin, *a.ScheduleEndMin, true
	}
	dawnH, dawnM, err := ParseHM(w.Settings.DawnTime)
	if err != nil {
		return 0, 0, false
	}
	duskH, duskM, err := ParseHM(w.Settings.DuskTime)
	if err != nil {
		return 0, 0, false
	}
	return dawnH*60 + dawnM, duskH*60 + duskM, true
}

// anyNeedMildOrWorse reports whether the actor has any need at mild tier or
// above (value >= needSilentFloor). Suppresses the return-to-WORK nudge so a
// hungry/tired NPC deals with its need first (v1 WORK-234).
func anyNeedMildOrWorse(w *World, a *Actor) bool {
	for _, n := range Needs {
		value, ok := a.Needs[n.Key]
		if !ok {
			continue // a missing need is not an unmet need — don't suppress on it
		}
		threshold := w.Settings.NeedThresholds.Get(n.Key)
		if NeedLabelTier(value, threshold) >= NeedMild {
			return true
		}
	}
	return false
}

// shiftDutyTarget computes the actor's standing shift duty as of nowMinute:
// where it should be and isn't. Returns ok=false when there's no duty (already
// where it belongs, out of scope, resting, or a to-work nudge suppressed by an
// unmet need). Pure read of world + actor state — the dispatch (walk vs warrant)
// is the caller's, keyed on Kind.
func shiftDutyTarget(w *World, a *Actor, nowMinute int, now time.Time) (target StructureID, toWork, ok bool) {
	// Scope: NPCs only. PCs are player-driven; transient visitors run their own
	// ExpiresAt lifecycle.
	isAgent := a.Kind == KindNPCStateful || a.Kind == KindNPCShared
	if !isAgent && a.Kind != KindDecorative {
		return "", false, false
	}
	// Transient visitors run their own ExpiresAt lifecycle. Visitors are
	// KindNPCShared today, so this is agent-only in practice, but the guard is
	// unconditional for robustness if a decorative-visitor ever exists.
	if a.VisitorState != nil {
		return "", false, false
	}
	// Resting NPCs are left alone (same suppressor as the reactor / #1 / sleep).
	if a.SleepingUntil != nil && a.SleepingUntil.After(now) {
		return "", false, false
	}
	if a.BreakUntil != nil && a.BreakUntil.After(now) {
		return "", false, false
	}

	start, end, winOK := effectiveShiftWindow(w, a)
	if !winOK {
		return "", false, false
	}
	onShift := minuteInShiftWindow(start, end, nowMinute)

	atWork := a.WorkStructureID != "" && a.InsideStructureID == a.WorkStructureID
	atHome := a.HomeStructureID != "" && a.InsideStructureID == a.HomeStructureID

	switch {
	case onShift && a.WorkStructureID != "" && !atWork:
		// Heading to work — suppressed for an agent with an unmet need (resolve
		// it first; decoratives have no real needs, so they always go).
		if isAgent && anyNeedMildOrWorse(w, a) {
			return "", false, false
		}
		return a.WorkStructureID, true, true
	case !onShift && a.HomeStructureID != "" && !atHome:
		// Heading home — NOT need-suppressed; going home is how an NPC rests.
		return a.HomeStructureID, false, true
	default:
		return "", false, false
	}
}

// alreadyEnRouteTo reports whether the actor is already walking into the target
// structure, so the decorative walk dispatch stays idempotent across the
// once-a-minute re-evaluation (don't re-issue a walk every tick).
func alreadyEnRouteTo(a *Actor, target StructureID) bool {
	mi := a.MoveIntent
	return mi != nil &&
		mi.Destination.Kind == MoveDestinationStructureEnter &&
		mi.Destination.StructureID != nil &&
		*mi.Destination.StructureID == target
}

// ShiftTick returns a Command that applies one pass of the shift/duty producer.
// Decoratives are mechanically walked; agents get a duty warrant. Runs on the
// world goroutine, so the MoveActor / tryStampWarrant calls are serialized.
func ShiftTick(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			nowMinute := localMinuteOfDay(w, now)
			for _, a := range w.Actors {
				target, toWork, ok := shiftDutyTarget(w, a, nowMinute, now)
				if !ok {
					continue
				}
				if a.Kind == KindDecorative {
					if alreadyEnRouteTo(a, target) {
						continue
					}
					if _, err := MoveActor(a.ID, NewStructureEnterDestination(target), false, now).Fn(w); err != nil {
						log.Printf("sim/shift_duty: walk %s -> %s: %v", a.ID, target, err)
					}
					continue
				}
				// Agent NPC: stamp a duty warrant (gated like #1 — leave an
				// already-pending / mid-tick actor alone; the level check
				// re-fires next minute if the duty still stands). The NPC
				// self-walks via move_to once that tool lands.
				if a.WarrantedSince != nil || a.TickInFlight {
					continue
				}
				tryStampWarrant(w, a, WarrantMeta{
					TriggerActorID: a.ID,
					Reason:         ShiftDutyWarrantReason{ToWork: toWork},
				}, now)
			}
			return nil, nil
		},
	}
}

// RunShiftTicker owns the shift/duty goroutine: once a minute, submit a
// ShiftTick. Same time.NewTicker idiom as RunNeedsTicker / RunSleepTicker.
// Returns when ctx is cancelled.
func RunShiftTicker(ctx context.Context, w *World) {
	t := time.NewTicker(ShiftTickerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := w.SendContext(ctx, ShiftTick(time.Now().UTC())); err != nil {
				if ctx.Err() == nil {
					log.Printf("sim/shift_duty: tick failed: %v", err)
				}
			}
		}
	}
}
