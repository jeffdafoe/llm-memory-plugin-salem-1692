package sim

import (
	"context"
	"log"
	"sort"
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
// LODGER PATH (ZBBS-HOME-296 2c, LLM-14): an NPC boarder has no HomeStructureID
// but holds an active ledger RoomAccess at the inn it rents (Ezekiel). It
// auto-beds there the same way a homed NPC beds at home — see npcSleepHere —
// with one difference: the inn is a public venue (the tavern also serves food +
// drink), so a lodger is bedded only at its night bedtime (inside the
// [LodgingBedtimeHour, DawnTime) lodger night window — see lodgerNightWindow),
// not the moment it walks in for a midday meal. That window is a civil night
// hour decoupled from any work shift: a SCHEDULED lodger (Ezekiel the blacksmith)
// must NOT bed when its forge closes at shift-end — only at night. A SCHEDULED
// homed NPC now shares that night window too (LLM-148, gated off-shift AND
// in-window); an UNSCHEDULED homed NPC keeps the classic always-off rule.

// DefaultNPCSleepMaxDurationHours caps an auto-bedded NPC's sleep when no
// shift-start wakes them sooner. Matches v1's npc_sleep_max_duration_hours.
const DefaultNPCSleepMaxDurationHours = 12

// DefaultLodgingBedtimeHour is the wall-clock hour a lodger retires for the
// night. 22:00 — later than the village's default dusk (19:00) because a guest
// at an inn keeps later hours, and decoupled from any work shift (LLM-14). The
// environment loaders default a MISSING lodging_bedtime_hour to this; an explicit
// 0 means midnight (honored), and lodgerNightWindow only falls back here for an
// out-of-range value (<0 or >23). A literal-built World leaves the field at its
// zero value (midnight), so tests that bed lodgers set it.
const DefaultLodgingBedtimeHour = 22

// lodgerRetireGraceMinutes is how long past bedtime AutoBedAtHomeNPCs holds off
// engine-bedding a still-conversing lodger, to give the model time to voice a
// deliberate goodnight and turn in itself (LLM-36). Past this margin the
// backstop beds the lodger regardless (executeNPCSleep's engine retire line then
// fires as the deterministic fallback), so an endlessly-chatty lodger can never
// re-create the never-sleep / constant-tired equilibrium the sleep lifecycle
// exists to break. In-world minutes, measured from the night window's open.
const lodgerRetireGraceMinutes = 45

// lodgerNightWindow returns the [start, end) minute-of-day window during which a
// lodger is bedded at the inn it rents: [LodgingBedtimeHour, DawnTime). It wraps
// past midnight (bedtime > dawn), which minuteInShiftWindow handles. A lodger
// beds while inside this window and wakes when it closes at dawn — one window
// drives both gates, so they can never thrash at the boundary.
//
// Deliberately NOT effectiveShiftWindow: a lodger's bedtime is a civil night
// hour, not its work-shift end. effectiveShiftWindow returns a scheduled actor's
// WORK schedule, which force-slept a scheduled lodger the moment its day-job
// ended (Ezekiel at 16:00 forge-close) — the LLM-14 root. An out-of-range
// LodgingBedtimeHour falls back to DefaultLodgingBedtimeHour. ok=false only when
// DawnTime fails to parse (logged at load by the phase system).
func lodgerNightWindow(w *World) (start, end int, ok bool) {
	dawnH, dawnM, err := ParseHM(w.Settings.DawnTime)
	if err != nil {
		return 0, 0, false
	}
	return lodgerBedtimeMinute(w), dawnH*60 + dawnM, true
}

// lodgerBedtimeMinute returns the lodger bedtime as minute-of-day in the world
// timezone, applying the DefaultLodgingBedtimeHour fallback for an out-of-range
// setting. Shared by lodgerNightWindow (the sim-side bed gate) and the snapshot
// publish, so the perception retire cue (LLM-36) bounds the SAME night window
// the bed/wake gates use rather than re-deriving it from a raw setting.
func lodgerBedtimeMinute(w *World) int {
	bedtimeHour := w.Settings.LodgingBedtimeHour
	if bedtimeHour < 0 || bedtimeHour > 23 {
		bedtimeHour = DefaultLodgingBedtimeHour
	}
	return bedtimeHour * 60
}

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

// actorOnShift is the worker-aware shift check (LLM-137). It matches
// isActorOnShift except that an unscheduled WORKER (AttrWorker) falls back to
// the world dawn/dusk day window — the same "decision B / day-active" notion
// shift_duty already uses via effectiveShiftWindow — instead of
// always-off-when-unscheduled. So an activated worker with no explicit schedule
// is day-active: out earning during the day and bedded only at night, rather
// than sleep-darted at home the moment it arrives back mid-afternoon. An
// explicit schedule still governs; every non-worker unscheduled actor stays
// always-off (home is its default resting state, HOME-204). Needs w for the
// world dawn/dusk settings (every call site passes a live world).
func actorOnShift(w *World, a *Actor, nowMinute int) bool {
	if a.ScheduleStartMin != nil && a.ScheduleEndMin != nil {
		return isActorOnShift(a, nowMinute)
	}
	if actorIsWorker(a) {
		if start, end, ok := effectiveShiftWindow(w, a); ok {
			return minuteInShiftWindow(start, end, nowMinute)
		}
	}
	return false
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

// npcSleepHere reports whether agent NPC a may auto-bed in the structure it is
// currently inside, at now — the unified sleep-target gate for the home and
// lodger resting relationships (ZBBS-HOME-296 2c). Both require off-shift; the
// difference is which shift notion governs:
//
//   - Home: a is inside its HomeStructureID. A SCHEDULED homed agent beds only
//     when off-shift AND inside the civil-night window [LodgingBedtimeHour,
//     DawnTime) — so it gets an evening rather than bedding the moment its shift
//     ends (LLM-148, the same window the lodger arm uses). The off-shift half is
//     load-bearing: the window alone would bed a night-shift home==work keeper
//     (a tavernkeeper 16:00–03:00) mid-shift at 22:00. An UNSCHEDULED homed NPC
//     keeps the classic always-off rule — home is the default resting state
//     (HOME-204), so it beds on any off-shift arrival — EXCEPT an unscheduled
//     worker (AttrWorker), which actorOnShift makes day-active on the dawn/dusk
//     window, so it beds only at night (LLM-137).
//   - Lodger: a is not home-matched but holds an active ledger RoomAccess for
//     its current structure (actorIsLodgerAt). Bedded only INSIDE the lodger
//     night window ([LodgingBedtimeHour, DawnTime) — see lodgerNightWindow), NOT
//     the moment it steps into the inn for a midday meal (the inn doubles as the
//     tavern). The night window is a civil night hour decoupled from the work
//     shift, so a SCHEDULED lodger no longer beds at its shift-end (the LLM-14
//     force-sleep root); both scheduled and unscheduled lodgers share it.
//
// Caller still enforces "not already sleeping" and "not on break". MUST be
// called from inside a Command.Fn (actorIsLodgerAt reads w.Structures).
func npcSleepHere(w *World, a *Actor, now time.Time) bool {
	if a.InsideStructureID == "" {
		return false
	}
	nowMinute := localMinuteOfDay(w, now)
	if a.HomeStructureID != "" && a.InsideStructureID == a.HomeStructureID {
		// A fully-scheduled homed agent, OR a fully-UNSCHEDULED worker, gets an evening
		// — bed it at the civil-night hour, not the moment its shift ends. The gate is
		// off-shift AND inside the night window: off-shift alone bedded it at shift-end
		// (the old behavior), which for an unscheduled worker (day-active on the
		// dawn/dusk window, LLM-137) meant bedded at DUSK with no evening (LLM-352); the
		// window alone would bed a night-shift home==work keeper (a tavernkeeper
		// 16:00–03:00) mid-shift at 22:00. actorOnShift already supplies the worker's
		// dawn/dusk fallback, so scheduled and unscheduled-worker agents share one gate.
		// Reuses lodgerNightWindow so homed bedtime tracks the same lodging_bedtime_hour
		// knob as lodgers. Wake stays shift-driven (WakeExpiredNPCSleepers): off-shift
		// bed vs on-shift wake can never thrash.
		scheduled := a.ScheduleStartMin != nil && a.ScheduleEndMin != nil
		unscheduledWorker := a.ScheduleStartMin == nil && a.ScheduleEndMin == nil && actorIsWorker(a)
		if scheduled || unscheduledWorker {
			if actorOnShift(w, a, nowMinute) {
				return false
			}
			start, end, ok := lodgerNightWindow(w)
			if !ok {
				return false
			}
			return minuteInShiftWindow(start, end, nowMinute)
		}
		// An unscheduled NON-worker — or a malformed partial schedule (exactly one bound
		// set) — keeps the classic always-off rule: home is its default resting state
		// (HOME-204), so it beds on any off-shift arrival, unchanged from before. The
		// non-worker is deliberately NOT given the civil-night evening — that is the
		// HOME-204 tension noted on LLM-352: whether jobless residents keep evening
		// hours is forward policy for an actor type that does not exist yet.
		return !actorOnShift(w, a, nowMinute)
	}
	if actorIsLodgerAt(w, a, a.InsideStructureID, now) {
		start, end, ok := lodgerNightWindow(w)
		if !ok {
			return false
		}
		return minuteInShiftWindow(start, end, nowMinute)
	}
	return false
}

// npcSleepRoomAt resolves the private room an auto-sleeping NPC beds into when it
// sleeps inside structureID, or (0, false) to sleep in place (the common floor).
// Two mutually-exclusive resting relationships, checked in order:
//
//   - lodger: the private room it holds an active ledger grant on (lodgerRoomAt),
//     LLM-14.
//   - keeper: a staff room of its own workplace, occupied as the establishment's
//     keeper (keeperStaffRoomAt) — a home==work keeper vacating the storefront for
//     its quarters, LLM-29. A homed keeper holds no lodging grant and a lodger is
//     not its own structure's keeper, so the two never both match.
//
// executeNPCSleep stamps Actor.InsideRoomID to this at the actual bed-down (not at
// check-in/arrival) and wakeNPC clears it, so a private InsideRoomID always means
// "asleep in it" — the invariant audienceRoomScope assumes. A homed NPC whose
// structure has no staff room (a plain cottage) resolves to (0, false) and sleeps
// in place, unchanged. MUST be called from inside a Command.Fn (reads w.Structures).
func npcSleepRoomAt(w *World, a *Actor, structureID StructureID, now time.Time) (RoomID, bool) {
	if room, ok := lodgerRoomAt(w, a, structureID, now); ok {
		return room, true
	}
	return keeperStaffRoomAt(w, a, structureID)
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
	// Bedding down supersedes any open take_break window. An actor must never hold
	// both a break and a sleep window at once — the two auto-bed callers used to
	// guard against an on-break actor to preserve that. Clearing it here makes the
	// invariant hold centrally at the one sleep entry point, so an off-shift
	// exhausted actor on a self-renewing break is bedded for the overnight reset
	// instead of evading it (the LLM-62 triple-suppressor). The sleep stamps below
	// re-derive State / LastTirednessRecoveryAt / occupancy, so no further break
	// teardown (endBreak) is needed.
	a.BreakUntil = nil
	// Excuse out of any active huddle BEFORE bedding down: speak a
	// deterministic retire line (so the huddle partners perceive the farewell)
	// then leave the huddle. Gated on an ACTIVE huddle — an NPC bedding alone
	// has no one to excuse itself to and beds silently. The arrival auto-bed
	// path already dropped its huddle during the walk, so this is a no-op
	// there; it matters for the stationary AutoBedAtHomeNPCs path (a lodger
	// bedding mid-conversation in the inn's common room).
	if actorInActiveHuddle(w, a) {
		speakRetireFarewell(w, a, now)
	}
	// LLM-129: a keeper turning in at its own establishment closes the house for
	// the night — announce to any non-tenant still inside and arm the grace timer
	// that turns out whoever hasn't left. Runs while the keeper is still awake (the
	// announcement reads as its closing call) and is a fast no-op for a non-keeper
	// bed-down (a lodger, a homed villager) and for an empty house.
	maybeBeginEstablishmentCloseup(w, a, now)
	// Bed the sleeper into a private room — stamp InsideRoomID at the actual
	// bed-down (not at check-in/arrival) so audience-scoping treats it as public
	// while awake on the floor and private only while asleep (audienceRoomScope).
	// A lodger beds into its granted private room (LLM-14); a home==work keeper
	// beds into its own staff quarters off the storefront (LLM-29). A homed NPC
	// with no staff room (a plain cottage) sleeps in place — InsideRoomID stays 0.
	if room, ok := npcSleepRoomAt(w, a, a.InsideStructureID, now); ok {
		a.InsideRoomID = room
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

// retireLines is the deterministic vocab pool for the auto-sleep farewell —
// the engine-authored "I'm turning in" beat an NPC speaks when bedtime ends
// its presence in a huddle. Period-appropriate, no LLM cognition (same class
// as the businessowner hospitality phrase pools).
var retireLines = []string{
	"I'm for bed — goodnight to you.",
	"I'll turn in now. Rest well.",
	"The hour's late; I'm off to my bed.",
	"Goodnight — I can keep my eyes open no longer.",
	"I'll bid you goodnight and find my rest.",
}

// renderRetireLine picks a retire line deterministically from the pool. Hashed
// on the actor plus the bed-down minute so the same NPC doesn't repeat one line
// every night yet a given (actor, time) is stable for tests — the same
// no-rand-threaded approach pickVisitorSlot uses for slot selection. The pool
// comes from the world's narration registry (ZBBS-WORK-399: the retireLines
// seed plus any LLM-expanded extras), so the draw is counted toward the pool's
// expansion threshold; world-goroutine-only like every caller in this file.
func (w *World) renderRetireLine(actorID ActorID, now time.Time) string {
	pool := w.narrationDraw(NarrationKeyNPCRetire)
	if len(pool) == 0 {
		return ""
	}
	minute := uint32(now.Unix() / 60)
	idx := (hashActorID(actorID) + minute) % uint32(len(pool))
	return pool[idx]
}

// speakRetireFarewell emits a deterministic farewell Spoke to the bedding
// actor's huddle partners, then leaves the huddle. Mirrors the businessowner
// Spoke path: emit directly (so the standard speech subscribers fan it out and
// stamp recipient warrants) and deliberately skip RecordInteraction — an
// engine-authored goodnight shouldn't pollute salient-fact trails. Caller has
// already confirmed an active huddle.
//
// Speak BEFORE leaving so the Spoke carries the live HuddleID and the partners
// are still members when it fires; LeaveHuddle then emits HuddleLeft (and
// HuddleConcluded if the bedding actor was the last one).
func speakRetireFarewell(w *World, a *Actor, now time.Time) {
	huddle, ok := w.Huddles[a.CurrentHuddleID]
	if !ok || huddle.ConcludedAt != nil {
		return
	}
	recipients := make([]ActorID, 0, len(huddle.Members))
	for id := range huddle.Members {
		if id != a.ID {
			recipients = append(recipients, id)
		}
	}
	sort.Slice(recipients, func(i, j int) bool { return recipients[i] < recipients[j] })
	// Only speak when there's actually a partner to excuse oneself to. A huddle
	// can transiently hold just this actor (everyone else left; conclusion only
	// fires at zero members), so an active-huddle gate alone isn't enough to
	// guarantee an audience — don't emit a farewell to an empty room. Leave the
	// huddle regardless (LeaveHuddle then concludes the now-empty huddle).
	// Empty text (a literal-built World with no narration registry) skips the
	// emit the same way an empty room does. Recipients are checked BEFORE the
	// draw so a silent bed-down doesn't count against the pool's expansion
	// threshold.
	if len(recipients) > 0 {
		if text := w.renderRetireLine(a.ID, now); text != "" {
			w.emit(&Spoke{
				SpeakerID:    a.ID,
				HuddleID:     a.CurrentHuddleID,
				RecipientIDs: recipients,
				Text:         text,
				At:           now,
			})
		}
	}
	LeaveHuddle(a.ID, now).Fn(w)
}

// wakeNPC clears an NPC's sleep, drops the recovery cursor (window closed),
// clears the bed-down room scope (InsideRoomID — a lodger or live-in keeper wakes
// into the common area; LLM-14, LLM-29), resets the macro-state to idle (no
// prior-state restore — the next thing the NPC does re-sets it), and refreshes
// occupancy (a darkened home==work tavern re-lights when its keeper wakes).
func wakeNPC(w *World, a *Actor) {
	a.SleepingUntil = nil
	a.LastTirednessRecoveryAt = nil
	a.InsideRoomID = 0
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
	// An on-break actor is intentionally NOT skipped here (it was, pre-LLM-62).
	// npcSleepHere below still requires off-shift, so an on-shift mid-shift stop
	// home is never sleep-darted; an OFF-shift actor on a self-renewing break is
	// now bedded for the night rather than letting the break evade the overnight
	// reset. executeNPCSleep clears BreakUntil, so the "never both rest windows"
	// invariant still holds. (Auto-bed arm of the LLM-62 triple-suppressor fix.)
	// Event-freshness: only act if the actor's current structure still matches
	// the arrival event (a later move could have superseded it).
	if a.InsideStructureID != arr.FinalStructureID {
		return
	}
	// ZBBS-HOME-435: don't bed an actor mid-deliberation — its in-flight tick
	// can still commit actions (a move_to would carry a just-bedded actor away
	// as a walking sleeper). The sweep beds them once the tick settles.
	if a.TickInFlight {
		return
	}
	// At home, or a lodger arriving at the inn it rents (ZBBS-HOME-296 2c).
	// npcSleepHere applies the right off-shift / bedtime-window gate per
	// relationship.
	if !npcSleepHere(w, a, arr.At) {
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
// every agent NPC that npcSleepHere clears (at home OR a lodger at its rented
// inn), off-shift/off-window, awake, and not on break. The arrival subscriber
// handles the normal walk-home case; this catches the stationary ones.
//
// For lodgers this backstop is load-bearing, not just defense-in-depth: a
// lodger who walks into the inn DURING the day is not bedded on arrival (it's
// the bedtime window that gates them), and no fresh arrival event fires at dusk
// — so this sweep is what beds a lodger once its bedtime window opens.
//
// An on-break actor is NOT skipped (it was, pre-LLM-62): npcSleepHere still gates
// the bed-down to off-shift / in-window, so an on-shift break is never bedded,
// while an off-shift actor on a self-renewing break is bedded for the night
// instead of letting the break evade the overnight reset. executeNPCSleep clears
// BreakUntil so the "never both rest windows" invariant holds. (The stationary
// auto-bed arm of the LLM-62 triple-suppressor fix; the arrival subscriber is the
// other.)
func AutoBedAtHomeNPCs(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			bedded := 0
			for _, a := range w.Actors {
				if !isAgentNPC(a) || a.SleepingUntil != nil {
					continue
				}
				// ZBBS-HOME-435: never bed an actor mid-deliberation or
				// mid-walk. A bed-stamp racing an in-flight tick produced the
				// walking sleeper (the tick's committed move_to carried a
				// just-bedded actor away); a mid-walk actor whose path crosses
				// its home footprint is passing through, not resting. Both
				// re-qualify on the next sweep once settled.
				if a.TickInFlight || a.MoveIntent != nil {
					continue
				}
				if !npcSleepHere(w, a, now) {
					continue
				}
				// LLM-36: hold the backstop briefly for a lodger still mid-
				// conversation at bedtime, so the model can voice a deliberate
				// goodnight and turn in itself rather than being silently
				// engine-bedded. Idle lodgers (and all homed NPCs) bed now.
				if lodgerAwaitingDeliberateRetire(w, a, now) {
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

// lodgerAwaitingDeliberateRetire reports whether AutoBedAtHomeNPCs should hold
// off bedding actor a this sweep, to let the model voice a deliberate goodnight
// and turn in itself (LLM-36). The caller has already confirmed npcSleepHere, so
// a is beddable right now; this only DEFERS that for a lodger that is:
//
//   - in the lodger flow, not the home flow — a homed NPC inside its home is
//     governed by the home branch of npcSleepHere and is never held; the
//     deliberate-retire flow is lodger-only.
//   - mid-conversation with a companion — an active huddle holding at least one
//     OTHER member is an audience to bid goodnight to. A lodger that is idle (or
//     in a degenerate sole-member huddle) has no goodnight to voice, so it beds
//     now. This MATCHES the buildRetireCue audience gate (a co-present huddle
//     peer), so the "## Turn in for the night" cue is shown exactly when the
//     backstop holds — cue and gate never disagree.
//   - within lodgerRetireGraceMinutes of the night window's open — past that
//     margin (or once it leaves the huddle) the normal backstop beds it, so a
//     chatty or distracted lodger never never-sleeps.
//
// MUST be called from inside a Command.Fn (actorIsLodgerAt / lodgerNightWindow
// read world state).
func lodgerAwaitingDeliberateRetire(w *World, a *Actor, now time.Time) bool {
	if a.HomeStructureID != "" && a.InsideStructureID == a.HomeStructureID {
		return false // home flow — not a lodger retire
	}
	if !actorIsLodgerAt(w, a, a.InsideStructureID, now) {
		return false
	}
	if !huddleWithCompanion(w, a) {
		return false // idle / no companion to bid goodnight to — bed it now
	}
	start, _, ok := lodgerNightWindow(w)
	if !ok {
		return false
	}
	// Minutes since the window opened, wrap-aware for a window that crosses
	// midnight (bedtime 22:00 → dawn 06:00). npcSleepHere already confirmed we
	// are inside the window, so this is in [0, window length).
	sinceBedtime := (localMinuteOfDay(w, now) - start + 1440) % 1440
	return sinceBedtime < lodgerRetireGraceMinutes
}

// huddleWithCompanion reports whether a is in an active (un-concluded) huddle
// that holds at least one OTHER member — someone co-present to bid goodnight to.
// A bare active huddle isn't enough: huddles conclude only at zero members, so
// one can transiently hold just a (the same edge speakRetireFarewell guards),
// and bedding a lodger that has no one to address shouldn't wait on a deliberate
// goodnight it can't voice. The snapshot mirror of this gate is a non-empty
// Surroundings.HuddleMembers (which already excludes the subject), so the retire
// cue and this backstop hold agree.
func huddleWithCompanion(w *World, a *Actor) bool {
	if a.CurrentHuddleID == "" {
		return false
	}
	h, ok := w.Huddles[a.CurrentHuddleID]
	if !ok || h.ConcludedAt != nil {
		return false
	}
	for id := range h.Members {
		if id != a.ID {
			return true
		}
	}
	return false
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
				if !a.SleepingUntil.After(now) {
					// Safety cap reached — wake regardless of relationship.
					wakeNPC(w, a)
					woken++
					continue
				}
				// Morning wake mirrors the bedding precedence in npcSleepHere so the
				// two never thrash. An actor sleeping AT its home wakes via
				// actorOnShift — so a homed worker wakes at dawn (its day-shift
				// start), symmetric with the dawn/dusk bedding gate, instead of
				// being stranded to the 12h cap (LLM-137). An actor sleeping
				// somewhere that is NOT its home but where it holds an active ledger
				// grant (actorIsLodgerAt) wakes when the lodger night window closes
				// at dawn — the same window it was bedded by. The discriminator is
				// the RELATIONSHIP (not-at-home + lodger), not "no HomeStructureID":
				// a homed NPC lodging elsewhere is bedded by the lodger rule, so it
				// must wake by it too. Any other sleeper (debug tooling, a future
				// HOME-300 shade-tree rester, a backfill) keeps the default cap-only
				// wake, so the wake condition never outruns the bed condition.
				wake := actorOnShift(w, a, nowMinute)
				if a.HomeStructureID != a.InsideStructureID && actorIsLodgerAt(w, a, a.InsideStructureID, now) {
					start, end, ok := lodgerNightWindow(w)
					wake = ok && !minuteInShiftWindow(start, end, nowMinute)
				}
				if !wake {
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
			w.beatTicker("sleep")
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
	// Expire ended breaks before the auto-bed pass (ZBBS-HOME-284 #4): a break
	// that just ended frees the actor to be auto-bedded the same tick if they
	// are home and off-shift, and re-lights a shop the keeper had closed.
	if _, err := w.SendContext(ctx, ExpireEndedBreaks(now)); err != nil {
		if ctx.Err() == nil {
			log.Printf("sim/npc_sleep: break-expiry sweep failed: %v", err)
		}
		return
	}
	// Heal any actor stranded in a rest macro-state with no live window
	// (ZBBS-HOME-410): the wake + break-expiry passes above clear only a window
	// that is still SET, so a StateResting / StateSleeping enum left behind
	// WITHOUT a window (a set-needs tiredness reset, or a checkpoint reload of a
	// stale state) survives them and would otherwise pin the actor —
	// reactor-shelved — forever. Runs before the auto-bed pass so a freshly-healed
	// off-shift-at-home NPC can be re-bedded with a proper window the same tick.
	if _, err := w.SendContext(ctx, HealOrphanedRestStates()); err != nil {
		if ctx.Err() == nil {
			log.Printf("sim/npc_sleep: rest-state heal sweep failed: %v", err)
		}
		return
	}
	if _, err := w.SendContext(ctx, AutoBedAtHomeNPCs(now)); err != nil {
		if ctx.Err() == nil {
			log.Printf("sim/npc_sleep: auto-bed sweep failed: %v", err)
		}
		return
	}
	// Homeless rest floor (ZBBS-HOME-300): after the homed/lodger arms have run,
	// walk any still-exhausted, bed-less, off-shift NPC to a free rest object so
	// it can dwell and recover. Last because auto-bed and lodging take priority.
	if _, err := w.SendContext(ctx, RouteHomelessToRest(now)); err != nil {
		if ctx.Err() == nil {
			log.Printf("sim/npc_sleep: homeless rest-route sweep failed: %v", err)
		}
	}
	// PC sleep arm (ZBBS-WORK-324): wake rested/capped PCs, then auto-bed idle
	// lodger PCs. The player-driven counterpart to the NPC arms above — same
	// per-minute cadence, own wake/bed semantics (rest-until-restored, no shift).
	runPCSleepTick(ctx, w, now)
}
