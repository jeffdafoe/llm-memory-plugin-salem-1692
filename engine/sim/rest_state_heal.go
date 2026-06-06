package sim

// rest_state_heal.go — ZBBS-HOME-410. Keeps an actor's rest MACRO-STATE
// (StateResting / StateSleeping) coupled to a live rest WINDOW (BreakUntil /
// SleepingUntil), and recovers actors where the two have drifted apart.
//
// THE INVARIANT: an agent NPC is in StateResting iff it holds a live BreakUntil,
// and in StateSleeping iff it holds a live SleepingUntil. executeTakeBreak /
// endBreak and executeNPCSleep / wakeNPC set and clear each pair together, so
// normal operation upholds it.
//
// HOW IT BREAKS: two paths nil a window without touching the macro-state —
//   1. the set-needs tiredness reset (umbilical setActorNeeds) used to nil
//      BreakUntil / SleepingUntil directly to un-park an actor; and
//   2. a checkpoint reload that restores a stale StateResting / StateSleeping
//      enum whose window did not survive.
// Either leaves State==StateResting with BreakUntil==nil (or the sleep
// equivalent). That orphan is INVISIBLE to ExpireEndedBreaks /
// WakeExpiredNPCSleepers — both key on a non-nil window — so nothing ever resets
// the enum. The reactor warrant gate (reactor.go) keys on the State==StateResting
// enum, so the orphan is shelved against every warrant except a red need, an
// operator nudge, or PC speech: the actor sits motionless in its
// (occupancy-closed) structure indefinitely. This was the live Ezekiel Crane /
// Prudence Ward "stuck in a closed stall all day" case (2026-06-06): a
// `set-needs all tiredness:0` nilled their break windows mid-break and left the
// StateResting enum behind.
//
// THE FIX, two halves:
//   - ClearRestForReset: the set-needs reset routes through here so it ends a
//     rest via endBreak / wakeNPC (State→idle + occupancy refresh) instead of
//     nil-ing the window and stranding the enum (path 1, at the source).
//   - HealOrphanedRestStates: a sweep in the sleep ticker that resets any agent
//     NPC already stranded in a windowless rest state (path 2, plus a backstop
//     for anything else that ever decouples them).

// endRestState ends whichever rest macro-state an agent NPC is in — break or
// sleep — by routing through the canonical endBreak / wakeNPC so State resets to
// idle, the recovery cursor drops, and structure occupancy refreshes (a closed
// shop re-opens). It fires on the enum OR a live window, so it also resets a
// stranded enum whose window is already nil. No-op when the actor is in neither
// rest state. Runs on the world goroutine.
//
// Dual-window safety (break and sleep are mutually exclusive in normal operation,
// but this defends the overlap): endBreak clears BreakUntil UNCONDITIONALLY (its
// SleepingUntil guard only wraps the State/cursor reset), and wakeNPC — running
// SECOND — clears SleepingUntil and sets State=idle unconditionally. So an actor
// that somehow held both windows lands fully idle with neither window. The order
// is load-bearing: wakeNPC must run last so its State=idle wins over endBreak's
// SleepingUntil-guarded (skipped) reset. TestEndRestState_DualWindow locks this.
func endRestState(w *World, a *Actor) {
	if a.BreakUntil != nil || a.State == StateResting {
		endBreak(w, a)
	}
	if a.SleepingUntil != nil || a.State == StateSleeping {
		wakeNPC(w, a)
	}
}

// ClearRestForReset un-parks an actor whose tiredness was just zeroed (the
// set-needs lever): at 0 tiredness there is no reason to stay resting. For an
// agent NPC it ends the rest PROPERLY (endRestState — State→idle + occupancy
// refresh); leaving only the windows nil would strand the macro-state and the
// reactor would never re-warrant the actor (see file header). For a non-agent
// actor (PC, decorative) it only nils any rest windows — their macro-state is
// not reactor-driven (a decorative's sleeping sprite is set-dressing, not a
// stuck agent), matching the pre-HOME-410 behavior. Runs on the world goroutine.
func ClearRestForReset(w *World, a *Actor) {
	if isAgentNPC(a) {
		endRestState(w, a)
		return
	}
	a.BreakUntil = nil
	a.SleepingUntil = nil
}

// HealOrphanedRestStates resets every agent NPC stranded in a rest macro-state
// with no live window — State==StateResting && BreakUntil==nil, or
// State==StateSleeping && SleepingUntil==nil. See the file header for how the
// invariant breaks and why the orphan is otherwise unrecoverable (the reactor
// gates on the enum; the window-keyed expiry sweeps skip a nil window). Returns
// the count healed. Scheduled in runSleepTickIteration AFTER the wake +
// break-expiry passes, so an expired-but-still-set window has already been
// cleared by those and only true (windowless) orphans remain for this sweep.
func HealOrphanedRestStates() Command {
	return Command{
		Fn: func(w *World) (any, error) {
			healed := 0
			for _, a := range w.Actors {
				if !isAgentNPC(a) {
					continue
				}
				orphanResting := a.State == StateResting && a.BreakUntil == nil
				orphanSleeping := a.State == StateSleeping && a.SleepingUntil == nil
				if orphanResting || orphanSleeping {
					endRestState(w, a)
					healed++
				}
			}
			return healed, nil
		},
	}
}
