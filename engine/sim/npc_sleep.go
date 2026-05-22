package sim

import (
	"context"
	"log"
	"time"
)

// NPC sleep state machine — in-memory port of v1 engine/sleep.go's NPC half
// (ZBBS-175 + ZBBS-HOME-204/262/281/282), ZBBS-HOME-284 #2.
//
// The deterministic night-rest loop, no LLM:
//
//	work's #2 duty producer nudges an off-shift NPC home
//	  → maybeNPCAutoSleep beds them on arrival (ActorArrived subscriber)
//	  → autoBedAtHomeNPCs backstop catches home==work vendors who never "arrive"
//	  → the tiredness recovery sweep (#1) restores tiredness while they sleep
//	  → wakeExpiredNPCSleepers wakes them at shift-start (or the 12h cap)
//
// SEAM E (settled with work, mail 9cf4bcf0): there is NO agent_override_until
// in v2. SleepingUntil / BreakUntil ARE the universal "this actor is resting,
// leave it alone" suppressor — work's producers gate on them, and so does this
// machine. "Mid-deliberation" is the reactor's concern (WarrantedSince /
// admission), not a rest field.
//
// LODGER PATH (#3) is deferred: an NPC boarder rests at a granted room_access
// bedroom (Ezekiel at the inn), which has no HomeStructureID. This slice beds
// only at HomeStructureID; the lodger branch lands with ZBBS-WORK-204.

// DefaultNPCSleepMaxDurationHours caps an auto-bedded NPC's sleep when no
// shift-start wakes them sooner. Matches v1's npc_sleep_max_duration_hours.
const DefaultNPCSleepMaxDurationHours = 12

// actorIsResting reports whether the actor is currently asleep or on break —
// its rest window is still ahead of now. Uses .After(now) (not just non-nil) so
// a lingering expired window between cap-expiry and the next wake sweep doesn't
// wrongly count as resting. Consumed by occupancy (drop "not open for business"
// keepers from active-presence headcounts) and the reactor rest gate.
func actorIsResting(a *Actor, now time.Time) bool {
	if a.SleepingUntil != nil && a.SleepingUntil.After(now) {
		return true
	}
	if a.BreakUntil != nil && a.BreakUntil.After(now) {
		return true
	}
	return false
}

// isAgentNPC reports whether the actor is an agent-backed NPC (stateful or
// shared-VA) — the populations the sleep machine drives. PCs and decoratives
// are excluded; transient visitors (KindNPCShared) fall out of the auto-sleep
// paths via the home-structure gate (they have no HomeStructureID).
func isAgentNPC(a *Actor) bool {
	return a.Kind == KindNPCStateful || a.Kind == KindNPCShared
}

// isActorOnShift reports whether nowMinute (local minute-of-day, 0–1439) falls
// in the actor's shift window. Unscheduled actors (nil schedule) are treated
// as always off-shift. Handles wrap-midnight shifts (e.g. tavernkeeper
// 16:00–03:00 → start=960, end=180).
func isActorOnShift(a *Actor, nowMinute int) bool {
	if a.ScheduleStartMin == nil || a.ScheduleEndMin == nil {
		return false
	}
	start, end := *a.ScheduleStartMin, *a.ScheduleEndMin
	// Half-open window [start, end): start inclusive, end exclusive. start==end
	// is an empty (always-off) shift, NOT a 24h shift — matches v1's CASE,
	// which never encoded "always on" as equal endpoints.
	if start <= end {
		return nowMinute >= start && nowMinute < end
	}
	return nowMinute >= start || nowMinute < end
}

// localMinuteOfDay converts an instant to minute-of-day in the world timezone.
// Falls back to UTC when settings haven't loaded a Location yet.
func localMinuteOfDay(w *World, at time.Time) int {
	loc := w.Settings.Location
	if loc == nil {
		loc = time.UTC
	}
	local := at.In(loc)
	return local.Hour()*60 + local.Minute()
}

// executeNPCSleep beds an NPC: sets SleepingUntil = now + the configured cap,
// stamps the tiredness-recovery cursor at the window's open so the recovery
// sweep (#1) counts from bed-down rather than its next lazy-init pass, soft-sets
// the State enum to StateSleeping (so the macro-state stops lying — the
// timestamp stays authoritative for eligibility), and refreshes occupancy on
// the structure (a home==work tavern darkens when its keeper sleeps; option (b),
// non-night-only only). Idempotent — a no-op (returns false) if already sleeping.
//
// Runs on the world goroutine (called inline from a subscriber or a Command).
func executeNPCSleep(w *World, a *Actor, now time.Time) bool {
	if a.SleepingUntil != nil {
		return false
	}
	maxHours := w.Settings.NPCSleepMaxDurationHours
	if maxHours <= 0 || maxHours > 24 {
		maxHours = DefaultNPCSleepMaxDurationHours
	}
	wakeAt := now.Add(time.Duration(maxHours) * time.Hour)
	a.SleepingUntil = &wakeAt
	stamp := now
	a.LastTirednessRecoveryAt = &stamp
	a.State = StateSleeping
	if a.InsideStructureID != "" {
		refreshStructureOccupancyState(w, a.InsideStructureID)
	}
	return true
}

// wakeNPC clears an NPC's sleep, drops the recovery cursor (window closed),
// resets the macro-state to idle (no prior-state restore — the next thing the
// NPC does re-sets it), and refreshes occupancy (a darkened home==work tavern
// re-lights when its keeper wakes).
func wakeNPC(w *World, a *Actor) {
	a.SleepingUntil = nil
	a.LastTirednessRecoveryAt = nil
	a.State = StateIdle
	if a.InsideStructureID != "" {
		refreshStructureOccupancyState(w, a.InsideStructureID)
	}
}

// handleAutoSleepOnArrival beds an NPC that arrives at its home off-shift. The
// ActorArrived subscriber — the v2 equivalent of v1's maybeNPCAutoSleep call
// from applyArrivalSideEffects. Fires once per arrival, no cost while walking.
//
// Auto-sleep is unconditional of tiredness (HOME-204): off-shift + at home
// beds the NPC, full-stop — the body rests at home by default. The on-shift
// guard is what stops a vendor's quick stop home mid-shift from getting
// sleep-darted.
func handleAutoSleepOnArrival(w *World, evt Event) {
	arr, ok := evt.(*ActorArrived)
	if !ok {
		return
	}
	a := w.Actors[arr.ActorID]
	if a == nil || !isAgentNPC(a) || a.SleepingUntil != nil {
		return
	}
	// Event-freshness: only act if the actor's current structure still matches
	// the arrival event (a later move could have superseded it).
	if a.InsideStructureID != arr.FinalStructureID {
		return
	}
	// At home. (Lodger-at-inn path is #3, deferred to ZBBS-WORK-204.)
	if a.HomeStructureID == "" || a.InsideStructureID != a.HomeStructureID {
		return
	}
	if isActorOnShift(a, localMinuteOfDay(w, arr.At)) {
		return
	}
	executeNPCSleep(w, a, arr.At)
}

// RegisterSleepSubscriber wires the auto-sleep-on-arrival subscriber. Call
// before World.Run or from inside a Command (world-goroutine-safe). Idempotent
// in effect: executeNPCSleep no-ops an already-sleeping actor, so a duplicate
// registration just dispatches a redundant no-op.
func RegisterSleepSubscriber(w *World) {
	if w == nil {
		panic("sim: RegisterSleepSubscriber requires a non-nil world")
	}
	w.Subscribe(SubscriberFunc(handleAutoSleepOnArrival))
}

// AutoBedAtHomeNPCs is the periodic backstop for NPCs that never fire an
// arrival event — home==work vendors (the farmers, a future live-in
// tavernkeeper) who are already standing at home and so never "arrive." Beds
// every agent NPC that is at home, off-shift, awake, and not on break. The
// arrival subscriber handles the normal walk-home case; this catches the
// stationary ones.
//
// On-break actors are skipped (BreakUntil > now) — a vendor on break is awake
// off-shift by choice and recovers via the tiredness sweep without being
// bedded. This is the v2 replacement for v1's agent_override_until exclusion.
func AutoBedAtHomeNPCs(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			nowMinute := localMinuteOfDay(w, now)
			bedded := 0
			for _, a := range w.Actors {
				if !isAgentNPC(a) || a.SleepingUntil != nil {
					continue
				}
				if a.BreakUntil != nil && a.BreakUntil.After(now) {
					continue // on break — awake by choice
				}
				if a.HomeStructureID == "" || a.InsideStructureID != a.HomeStructureID {
					continue
				}
				if isActorOnShift(a, nowMinute) {
					continue
				}
				if executeNPCSleep(w, a, now) {
					bedded++
				}
			}
			return bedded, nil
		},
	}
}

// WakeExpiredNPCSleepers clears SleepingUntil on any NPC whose wake condition
// has fired. Two ORed conditions:
//   - SleepingUntil <= now: the safety cap.
//   - on-shift now (ZBBS-HOME-262): executeNPCSleep sets a flat 12h cap
//     regardless of how near shift-start is, so the cap alone could leave an
//     NPC asleep into their shift; waking at shift-start surfaces them on time.
//
// ZBBS-HOME-282: NO tiredness=0 wake for NPCs. They sleep through the night
// like villagers and wake on shift-start, not the moment recovery completes —
// otherwise a promptly-bedded NPC pops awake at 3am with nothing to do and
// drifts back to "tired" before their shift, the village-wide constant-tired
// equilibrium this whole lifecycle exists to break.
func WakeExpiredNPCSleepers(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			nowMinute := localMinuteOfDay(w, now)
			woken := 0
			for _, a := range w.Actors {
				if !isAgentNPC(a) || a.SleepingUntil == nil {
					continue
				}
				capReached := !a.SleepingUntil.After(now)
				if !capReached && !isActorOnShift(a, nowMinute) {
					continue
				}
				wakeNPC(w, a)
				woken++
			}
			return woken, nil
		},
	}
}

// SleepTickerInterval is how often RunSleepTicker wakes. One minute matches the
// other sim tickers and v1's sweep cadence.
const SleepTickerInterval = time.Minute

// RunSleepTicker owns the sleep-sweep goroutine: wake first (surface NPCs whose
// shift started or whose cap fired), then bed (catch stationary home==work
// vendors now off-shift). Wake-before-bed mirrors v1 and avoids a wake/bed
// thrash on an NPC right at a boundary. Caller starts it in a goroutine
// alongside World.Run; returns when ctx is cancelled.
func RunSleepTicker(ctx context.Context, w *World) {
	t := time.NewTicker(SleepTickerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runSleepTickIteration(ctx, w)
		}
	}
}

func runSleepTickIteration(ctx context.Context, w *World) {
	now := time.Now().UTC()
	if _, err := w.SendContext(ctx, WakeExpiredNPCSleepers(now)); err != nil {
		if ctx.Err() == nil {
			log.Printf("sim/npc_sleep: wake sweep failed: %v", err)
		}
		return
	}
	if _, err := w.SendContext(ctx, AutoBedAtHomeNPCs(now)); err != nil {
		if ctx.Err() == nil {
			log.Printf("sim/npc_sleep: auto-bed sweep failed: %v", err)
		}
	}
}
