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
//	pay for a night → placed in a private bedroom (AssignBedroomForLodger)
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
// voluntary room-move, and AssignBedroomForLodger already places a paying lodger
// IN their private room, so the narrow "already in your bedroom" gate suffices.

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
	// a PC who sat down fresh in their room isn't knocked out. Mirrors v1's
	// pc_idle_sleep_min_tiredness (default 10).
	DefaultPCIdleSleepMinTiredness = 10
)

// ErrPCCannotSleepHere is returned by SleepPC when the PC isn't in a paid
// private bedroom — the explicit /pc/sleep route maps it to a rejection. The
// idle-auto-bed sweep applies the same gate (pcCanSleepHere) but silently skips
// rather than erroring.
var ErrPCCannotSleepHere = errors.New("you need to be in a paid bedroom to sleep")

// pcCanSleepHere reports whether PC may bed down where it currently stands: it
// must be inside a structure, in a PRIVATE room that belongs to that structure,
// and hold an active ledger RoomAccess for THAT specific room (the paid-bedroom
// proof). v2 port of v1 handlePCSleep's gate — rejects sleeping from the common
// room/bar (no private room) or without a paid night (no active ledger grant).
// Tying it to the exact InsideRoomID (not just any private room in the
// structure) matches v1's room_access join on inside_room_id. MUST be called
// from inside a Command.Fn (reads w.Structures via findRoom).
func pcCanSleepHere(w *World, pc *Actor, now time.Time) bool {
	if pc.InsideStructureID == "" || pc.InsideRoomID == 0 {
		return false
	}
	room := findRoom(w, pc.InsideRoomID)
	if room == nil || room.Kind != RoomKindPrivate || room.StructureID != pc.InsideStructureID {
		return false
	}
	for key, ra := range pc.RoomAccess {
		if key.RoomID == pc.InsideRoomID && IsActiveLedgerGrant(ra, now) {
			return true
		}
	}
	return false
}

// executePCSleep beds a PC at now: sets SleepingUntil = now + the safety cap,
// stamps the tiredness-recovery cursor at bed-down (so RecoverTiredness credits
// from this moment rather than its next lazy-init pass), soft-sets State to
// StateSleeping, and emits PCSleepStarted carrying the wake-cap instant.
// Idempotent — a no-op (returns false, emits nothing) if already sleeping.
//
// Unlike executeNPCSleep this does NOT refresh structure occupancy (a sleeping
// player doesn't close a shop) and does not excuse the PC from a huddle (a
// player's social state is theirs to manage). Runs on the world goroutine.
func executePCSleep(w *World, pc *Actor, now time.Time) bool {
	if pc.SleepingUntil != nil {
		return false
	}
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
// the occupancy refresh. State only resets to Idle when it is actually
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
			if !pcCanSleepHere(w, pc, now) {
				return PCSleepResult{}, ErrPCCannotSleepHere
			}
			executePCSleep(w, pc, now)
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
			wakePCActor(pc)
			w.emit(&PCSleepEnded{ActorID: actorID, Reason: "manual", At: now})
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
		wakePCActor(pc)
		w.emit(&PCSleepEnded{ActorID: actorID, Reason: "input", At: now})
	}
}

// WakeExpiredPCSleepers wakes any sleeping PC whose wake condition has fired,
// emitting PCSleepEnded reason "auto" for each. Three ORed conditions (ZBBS-150):
//
//   - tiredness <= 0: fully rested — the EXPECTED wake. RecoverTiredness
//     decrements the PC's tiredness every minute while SleepingUntil is set,
//     so a max-tiredness PC wakes in ~4h wall-clock at the default rate.
//   - SleepingUntil <= now: the safety cap — a backstop against a wedged
//     recovery sweep, not the normal path.
//   - checkout: the PC no longer passes pcCanSleepHere — its ledger grant for
//     the room lapsed, or home's EvictExpiredOccupants already relocated it to
//     the common room. A PC can't change room/grant while asleep EXCEPT via
//     expiry/eviction, so "sleeping but no longer can-sleep-here" uniquely means
//     checked out. Without this a PC asleep at checkout keeps SleepingUntil set
//     (client sleep overlay stuck) after being moved out. v1 woke-then-evicted;
//     v2's eviction sweep is a separate ticker, so we wake here and it relocates
//     — order-independent, end state awake + in common.
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
				rested := a.Needs["tiredness"] <= 0
				capped := !a.SleepingUntil.After(now)
				checkedOut := !pcCanSleepHere(w, a, now)
				if !rested && !capped && !checkedOut {
					continue
				}
				wakePCActor(a)
				w.emit(&PCSleepEnded{ActorID: id, Reason: "auto", At: now})
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
// Narrow gate (already in the private room): v2's AssignBedroomForLodger places
// a paying lodger IN their bedroom, and v2 has no voluntary room-move to walk a
// PC up from the common room, so the widened v1 gate (walk-from-common) is not
// ported. Run from the sleep ticker, after the wake pass.
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
				if !pcCanSleepHere(w, a, now) {
					continue // not in a paid private bedroom
				}
				if executePCSleep(w, a, now) {
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
