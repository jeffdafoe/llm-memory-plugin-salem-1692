package sim

import (
	"context"
	"errors"
	"log"
	"time"
)

// PC sleep — the player-driven counterpart to the NPC auto-sleep machine in
// npc_sleep.go. This is the v2 in-memory port of v1 engine/sleep.go's PC half
// (ZBBS-132 + ZBBS-150). Where an NPC auto-beds on off-shift arrival home and
// wakes at shift-start, a human player's sleep is a PASSIVE lodging mechanic
// keyed off a last-input cursor:
//
//	pay for a night → granted a private bedroom (AssignBedroomForLodger)
//	  → stop acting → the idle-auto-bed sweep beds them once idle + tired
//	  → RecoverTiredness (no Kind gate) restores tiredness while they sleep
//	  → WakeExpiredPCSleepers wakes them when fully rested (or the cap fires)
//	  → any action (touchPCInput) or the Wake button (/pc/wake) wakes early
//
// Wake model (ZBBS-150 — the SHIPPED v1 behavior, NOT the pre-150 dawn anchor):
// bedding sets SleepingUntil = now + a safety cap, but the EXPECTED wake is
// recovery-driven. RecoverTiredness credits a sleeping PC every minute with no
// Kind gate, so the PC wakes the moment tiredness hits 0 (~4h wall-clock at the
// default rate); the cap is only a backstop against a wedged recovery sweep. PC
// and NPC sleep thus share one model ("rest until restored"); the boundary
// differs (NPC: shift-start; PC: fully rested) because a PC has no shift.
//
// SCOPE (ZBBS-WORK-324). The checkout WAKE (wake a sleeping PC when its lodging
// grant lapses) IS ported — see WakeExpiredPCSleepers's third arm — because
// leaving it out stranded the client sleep overlay when home's eviction sweep
// relocated a still-sleeping PC. Two v1 lodger-lifecycle arms remain unported,
// both on home's eviction/lodging surface, NOT this file's: (1) the eviction
// RELOCATION's client broadcast (home's RunRoomSweep relocates a checked-out PC
// to the common room but doesn't yet surface it over WS); (2) morning-descent —
// walking a naturally-woken lodger down to the common room. The widened auto-bed
// gate that WALKED a lodger up from the common room is also dropped: v2 has no
// voluntary room-move, and a lodger holds a standing private-room grant, so the
// grant-based pcCanSleepHere gate (LLM-14) beds them off that grant — it no
// longer needs the lodger to already be "in" the bedroom.

const (
	// DefaultPCSleepMaxDurationHours caps a bedded PC's sleep when recovery
	// doesn't wake them sooner. Mirrors v1's pc_sleep_max_duration_hours
	// (default 12). A const, not a WorldSettings knob, for the MVP — the cap is
	// only a backstop (the recovery wake is the expected path), so live-tuning
	// it buys little; promote to a setting if a need to diverge from 12 appears.
	DefaultPCSleepMaxDurationHours = 12

	// DefaultPCIdleSleepMinutes is how long a lodger PC must go without a
	// stamped action (LastPCInputAt) before the idle-auto-bed sweep beds them.
	// Mirrors v1's pc_idle_sleep_minutes (default 15 — "I forgot I was AFK"
	// pacing, not "I stepped away for a moment").
	DefaultPCIdleSleepMinutes = 15

	// DefaultPCIdleSleepMinTiredness is the tiredness floor for idle auto-bed —
	// a PC who isn't genuinely tired isn't knocked out. Set at the tiredness
	// red/"weary" line (DefaultTirednessRedThreshold): mild daytime tiredness
	// plateaus at ~10-11 on the 0-24 scale (see DefaultTirednessAwarenessFloor),
	// so v1's floor of 10 was always satisfied and idle lodgers were bedded at
	// any hour of the day (LLM-331). At weary, the ~1/hr climb reaches the floor
	// only late in a long day — a natural bedtime with no clock window, so
	// late-night players still bed whenever they're actually weary.
	DefaultPCIdleSleepMinTiredness = 16
)

// ErrPCCannotSleepHere is returned by SleepPC when the PC holds no active
// private-room grant in its current structure — the explicit /pc/sleep route
// maps it to a rejection. The idle-auto-bed sweep applies the same gate
// (pcCanSleepHere) but silently skips rather than erroring. LLM-14: the gate is
// grant-based, so the message says a paid bedroom "here", not "be in" one — a
// lodger can bed from the common floor as long as it holds a grant.
var ErrPCCannotSleepHere = errors.New("you need a paid bedroom here to sleep")

// pcCanSleepHere reports whether PC may bed down where it currently stands and,
// if so, the private bedroom it beds into. It must be inside a structure where
// it holds an active ledger RoomAccess for a PRIVATE room of that structure (the
// paid-bedroom proof). v2 port of v1 handlePCSleep's gate — rejects sleeping
// from the common room/bar or without a paid night.
//
// LLM-14: keyed off the GRANT, not the live InsideRoomID. Check-in no longer
// stamps InsideRoomID (an awake checked-in lodger stays public-scoped), so the
// gate can't require InsideRoomID to already be the bedroom — and a PC that
// manual-wakes (InsideRoomID cleared) must still be able to re-bed off its
// standing grant. The resolved room is what executePCSleep stamps as
// InsideRoomID at bed-down. MUST be called from inside a Command.Fn (reads
// w.Structures via findRoom).
func pcCanSleepHere(w *World, pc *Actor, now time.Time) (RoomID, bool) {
	if pc.InsideStructureID == "" {
		return 0, false
	}
	return lodgerRoomAt(w, pc, pc.InsideStructureID, now)
}

// executePCSleep beds a PC at now into roomID (the private bedroom resolved by
// pcCanSleepHere): stamps InsideRoomID = roomID — the bed-down moment is where a
// lodger's physical room scope is set (LLM-14), not check-in — sets
// SleepingUntil = now + the safety cap, stamps the tiredness-recovery cursor at
// bed-down (so RecoverTiredness credits from this moment rather than its next
// lazy-init pass), soft-sets State to StateSleeping, and emits PCSleepStarted
// carrying the wake-cap instant. Idempotent — a no-op (returns false, emits
// nothing) if already sleeping.
//
// InsideRoomID is cleared on wake to keep the invariant "a private InsideRoomID
// means asleep in it": the manual/input wakes and the normal morning/cap wake
// clear it immediately (so an awake PC is never bedroom-scoped), and the
// morning-descent subscriber then relocates the PC to the common room off the
// wake event's FromRoomID. The checkout wake (lapsed grant) is the one exception
// — it keeps InsideRoomID set so the EvictExpiredOccupants sweep relocates +
// narrates the checkout off live state.
//
// Unlike executeNPCSleep this does NOT refresh structure occupancy (a sleeping
// player doesn't close a shop) and does not excuse the PC from a huddle (a
// player's social state is theirs to manage). Runs on the world goroutine.
func executePCSleep(w *World, pc *Actor, roomID RoomID, now time.Time) bool {
	if pc.SleepingUntil != nil {
		return false
	}
	pc.InsideRoomID = roomID
	wakeAt := now.Add(DefaultPCSleepMaxDurationHours * time.Hour)
	pc.SleepingUntil = &wakeAt
	stamp := now
	pc.LastTirednessRecoveryAt = &stamp
	pc.State = StateSleeping
	w.emit(&PCSleepStarted{ActorID: pc.ID, WakeAt: wakeAt, At: now})
	return true
}

// wakePCActor clears a PC's sleep and resets the recovery cursor. Does NOT emit
// — the caller emits PCSleepEnded with the right reason. Mirrors wakeNPC minus
// the occupancy refresh. It does NOT touch InsideRoomID — each caller manages the
// bedroom scope per wake reason (LLM-14): the manual/input wakes and the normal
// morning/cap wake clear it, while the checkout wake keeps it for the eviction
// sweep. State only resets to
// Idle when it is actually
// StateSleeping: an action that input-wakes a PC runs its own command right
// after this and re-sets State (Walking/Conversing/…), so guarding here avoids
// clobbering a macro-state another command set, and leaves a non-sleeping State
// untouched if it ever diverged.
func wakePCActor(pc *Actor) {
	pc.SleepingUntil = nil
	pc.LastTirednessRecoveryAt = nil
	if pc.State == StateSleeping {
		pc.State = StateIdle
	}
}

// PCSleepResult carries a SleepPC outcome to the httpapi handler. Bedded is
// false on the idempotent already-sleeping no-op (the handler then omits
// wake_at to signal "no fresh transition", matching v1). WakeAt is the
// safety-cap instant for the client countdown on a fresh bed-down.
type PCSleepResult struct {
	Bedded bool
	WakeAt time.Time
}

// SleepPC is the explicit /pc/sleep route command: gate the caller's PC via
// pcCanSleepHere, then bed them. Returns ErrPCCannotSleepHere when the PC isn't
// in a paid private bedroom. An already-sleeping PC is the idempotent no-op
// (Bedded=false, nil error) — checked BEFORE the location/payment gate so a
// second /pc/sleep stays a no-op even if the grant has since expired or the PC
// moved (the gate would otherwise wrongly reject an already-bedded PC). The
// httpapi wrapper resolves actorID and has already confirmed it's a real PC; the
// defensive guard here just avoids a nil-deref.
func SleepPC(actorID ActorID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			pc := w.Actors[actorID]
			if pc == nil || pc.Kind != KindPC {
				return PCSleepResult{}, ErrPCCannotSleepHere
			}
			if pc.SleepingUntil != nil {
				return PCSleepResult{Bedded: false}, nil
			}
			room, ok := pcCanSleepHere(w, pc, now)
			if !ok {
				return PCSleepResult{}, ErrPCCannotSleepHere
			}
			executePCSleep(w, pc, room, now)
			return PCSleepResult{Bedded: true, WakeAt: *pc.SleepingUntil}, nil
		},
	}
}

// WakePC is the manual /pc/wake route command: clear the caller's PC's sleep
// and emit PCSleepEnded reason "manual". Idempotent — a no-op (emits nothing)
// when the PC isn't sleeping, returning false. The wrapper has resolved a real
// PC; the guard is defensive.
func WakePC(actorID ActorID, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			pc := w.Actors[actorID]
			if pc == nil || pc.Kind != KindPC || pc.SleepingUntil == nil {
				return false, nil
			}
			fromRoom := pc.InsideRoomID
			wakePCActor(pc)
			// LLM-14: a player-driven wake drops the private-room scope itself
			// (no morning-descent runs for "manual"/"input"), so an awake PC in
			// the inn is public-scoped rather than stuck addressing its empty
			// bedroom.
			pc.InsideRoomID = 0
			w.emit(&PCSleepEnded{ActorID: actorID, Reason: "manual", FromRoomID: fromRoom, At: now})
			return true, nil
		},
	}
}

// TouchPCInput records a PC's deliberate action: stamps LastPCInputAt = now
// (feeding the idle-auto-bed timer) and, if the PC was sleeping, wakes them and
// emits PCSleepEnded reason "input" — so acting while asleep both wakes the PC
// and lets the action proceed. v2 port of v1 touchPCInput, called from the PC
// write-command wrappers (move / speak / pay) BEFORE delegating to the sim
// command. PC-only: an NPC id (or vanished actor) is a no-op, so a stray caller
// can't wake an NPC or emit a PC event for it. MUST run on the world goroutine
// (mutates the actor + emits).
func TouchPCInput(w *World, actorID ActorID, now time.Time) {
	pc := w.Actors[actorID]
	if pc == nil || pc.Kind != KindPC {
		return
	}
	stamp := now
	pc.LastPCInputAt = &stamp
	if pc.SleepingUntil != nil {
		fromRoom := pc.InsideRoomID
		wakePCActor(pc)
		pc.InsideRoomID = 0 // LLM-14: player-driven wake drops the private-room scope (see WakePC)
		w.emit(&PCSleepEnded{ActorID: actorID, Reason: "input", FromRoomID: fromRoom, At: now})
	}
}

// WakeExpiredPCSleepers wakes any sleeping PC whose wake condition has fired,
// emitting PCSleepEnded reason "auto" for each. Four ORed conditions (ZBBS-150,
// LLM-14):
//
//   - tiredness <= 0: fully rested — the EXPECTED wake. RecoverTiredness
//     decrements the PC's tiredness every minute while SleepingUntil is set,
//     so a max-tiredness PC wakes in ~4h wall-clock at the default rate.
//   - SleepingUntil <= now: the safety cap — a backstop against a wedged
//     recovery sweep, not the normal path.
//   - checkout: the PC's ledger grant for a private room here has lapsed
//     (pcCanSleepHere returns ok=false). Without this a PC asleep at checkout
//     keeps SleepingUntil set (client sleep overlay stuck). This wake KEEPS
//     InsideRoomID so the separate EvictExpiredOccupants sweep relocates +
//     narrates the checkout off live state — order-independent, end state awake +
//     in common.
//   - movedOut: a defensive repair — a sleeping PC's InsideRoomID must be its
//     granted bedroom (stamped at bed-down). If something moved it out while it
//     slept (it shouldn't, under the LLM-14 invariant), wake to surface it rather
//     than leave a sleeper inconsistent with its grant.
//
// On a non-checkout wake (rested / cap / movedOut, all with an active grant) the
// wake clears InsideRoomID immediately so an awake PC is never bedroom-scoped
// (the LLM-14 invariant), and the morning-descent subscriber relocates it to the
// common room off the event's FromRoomID.
//
// PC-only (Kind == KindPC); NPC sleepers are handled by WakeExpiredNPCSleepers,
// which has the shift-boundary semantics PCs lack. Run from the sleep ticker.
func WakeExpiredPCSleepers(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			woken := 0
			for id, a := range w.Actors {
				if a.Kind != KindPC || a.SleepingUntil == nil {
					continue
				}
				room, canSleepHere := pcCanSleepHere(w, a, now)
				rested := a.Needs["tiredness"] <= 0
				capped := !a.SleepingUntil.After(now)
				checkedOut := !canSleepHere
				// Defensive: a sleeping PC's InsideRoomID is its granted bedroom.
				movedOut := canSleepHere && a.InsideRoomID != room
				if !rested && !capped && !checkedOut && !movedOut {
					continue
				}
				fromRoom := a.InsideRoomID
				wakePCActor(a)
				// Non-checkout wakes clear the room scope now (invariant: a private
				// InsideRoomID means asleep); descent relocates to common off
				// FromRoomID. Checkout keeps it for the eviction sweep.
				if !checkedOut {
					a.InsideRoomID = 0
				}
				w.emit(&PCSleepEnded{ActorID: id, Reason: "auto", FromRoomID: fromRoom, At: now})
				woken++
			}
			return woken, nil
		},
	}
}

// AutoBedIdleLodgerPCs beds any lodger PC that has gone idle in its paid
// bedroom — the passive sleep entry that is the primary way a player sleeps
// (v1 autoBedIdleLodgers). A PC qualifies when it is: awake, has a stamped
// LastPCInputAt older than DefaultPCIdleSleepMinutes, has tiredness at or above
// DefaultPCIdleSleepMinTiredness (a fresh PC isn't knocked out), and passes
// pcCanSleepHere (in a private room of its current structure with an active
// ledger grant). executePCSleep emits PCSleepStarted, which drives the client's
// sleep overlay + Wake button.
//
// Grant-based gate (LLM-14): a lodger holds a private-room grant but is bedded
// into the room only at this sweep (or /pc/sleep), so pcCanSleepHere keys off
// the standing grant in the current structure, not a check-in-stamped
// InsideRoomID; the bedroom it resolves is stamped as InsideRoomID at bed-down.
// Run from the sleep ticker, after the wake pass.
func AutoBedIdleLodgerPCs(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			idleCutoff := now.Add(-DefaultPCIdleSleepMinutes * time.Minute)
			bedded := 0
			for _, a := range w.Actors {
				if a.Kind != KindPC || a.SleepingUntil != nil {
					continue
				}
				if a.LastPCInputAt == nil || !a.LastPCInputAt.Before(idleCutoff) {
					continue // never acted, or acted too recently to be idle
				}
				if a.Needs["tiredness"] < DefaultPCIdleSleepMinTiredness {
					continue // not tired enough to be knocked out
				}
				room, ok := pcCanSleepHere(w, a, now)
				if !ok {
					continue // no active grant for a private bedroom here
				}
				if executePCSleep(w, a, room, now) {
					bedded++
				}
			}
			return bedded, nil
		},
	}
}

// runPCSleepTick runs the PC sleep sweep for one tick: wake first (surface PCs
// who are rested or capped), then bed (catch idle lodgers now eligible).
// Wake-before-bed mirrors the NPC arm and avoids a wake/bed thrash on a PC at
// the boundary. Called from runSleepTickIteration alongside the NPC arms.
func runPCSleepTick(ctx context.Context, w *World, now time.Time) {
	if _, err := w.SendContext(ctx, WakeExpiredPCSleepers(now)); err != nil {
		if ctx.Err() == nil {
			log.Printf("sim/pc_sleep: PC wake sweep failed: %v", err)
		}
		return
	}
	if _, err := w.SendContext(ctx, AutoBedIdleLodgerPCs(now)); err != nil {
		if ctx.Err() == nil {
			log.Printf("sim/pc_sleep: PC auto-bed sweep failed: %v", err)
		}
	}
}
