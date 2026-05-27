package sim

import (
	"context"
	"log"
	"math"
	"time"
)

// Tiredness recovery sweep — in-memory port of legacy
// engine/tiredness_recovery_sweep.go (ZBBS-141), now ZBBS-HOME-284 #1.
//
// THE GAP THIS CLOSES: v2 accrues tiredness (IncrementNeedsTick +
// ApplyMovementFatigue) but nothing decrements it — tiredness could only
// ever climb, so the village exhausts with no recovery path. This sweep is
// the decrement side: while an actor is asleep (SleepingUntil) or on break
// (BreakUntil), tiredness drops toward 0 (rested) at a settings-driven rate.
//
// WHY A MINUTE-GRAINED SWEEP, NOT THE HOURLY NEEDS TICK: realistic breaks
// are 15-30 min. Folding recovery into the hourly IncrementNeedsTick would
// either miss a sub-hour break entirely or credit a flat hour for a 5-min
// nap. A once-a-minute sweep tracks actual elapsed wall-clock per actor.
//
// V2-NATIVE SIMPLIFICATION vs the legacy DB sweep: legacy held a
// `last_tiredness_recovery_at` Postgres column as the carry cursor, a
// FOR UPDATE lock over candidate rows, and a per-actor UPDATE. In the
// single-goroutine in-memory world none of that is needed: the cursor is
// the transient Actor.LastTirednessRecoveryAt pointer, the world goroutine
// already serializes access, and the decrement is a map write. The cursor
// is NOT persisted (in-process cadence state doesn't earn a column); on
// restart it re-inits and at most a sub-unit fraction is lost.
//
// THE CURSOR IS THE CARRY: each pass advances the cursor by exactly the
// wall-clock time represented by the WHOLE recovered units (units / rate
// minutes). Leftover fractional minutes stay between the new cursor and
// now, so they accumulate into the next pass instead of being rounded away.

// recoveryEpsilon nudges floor(elapsed*rate) past exact-boundary values that
// float arithmetic represents as 9.99999998 instead of 10. Smaller than any
// plausible recovery rate so it can't promote a genuinely-fractional accrual
// into a whole unit. Ported verbatim from the legacy sweep.
const recoveryEpsilon = 1e-9

// TirednessRecoveryTickerInterval is how often RunTirednessRecoveryTicker
// wakes. One minute matches legacy cadence and the other sim tickers.
const TirednessRecoveryTickerInterval = time.Minute

// RecoverTiredness returns a Command that credits tiredness recovery to every
// actor with an open rest window (SleepingUntil or BreakUntil ahead of its
// recovery cursor), as of `now`. The per-minute rate is read from
// WorldSettings inside the command so it stays consistent with any concurrent
// settings change. Returns the count of actors whose tiredness was decremented.
//
// An actor that is NOT resting (both windows nil) has its cursor cleared, so a
// later rest window starts counting fresh rather than crediting the whole gap
// since the actor last rested.
func RecoverTiredness(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			rateX100 := w.Settings.TirednessRecoveryPerMinuteX100
			if rateX100 <= 0 {
				// Recovery disabled by setting — leave cursors untouched; if
				// re-enabled the next pass picks up from wherever they sit.
				return 0, nil
			}
			rate := float64(rateX100) / 100.0

			recovered := 0
			for _, a := range w.Actors {
				end := laterTime(a.BreakUntil, a.SleepingUntil)
				if end == nil {
					// No rest window at all. Drop the cursor so a fresh window
					// can't be credited against a stale (possibly hours-old)
					// timestamp.
					a.LastTirednessRecoveryAt = nil
					continue
				}
				if a.Needs == nil {
					// Shouldn't happen — every actor is seeded with the three
					// needs. Init rather than skip so cursor management still
					// runs for a resting actor, matching the nil-map handling
					// in ApplyConsumption / IncrementNeedsTick.
					a.Needs = make(map[NeedKey]int)
				}

				// A window pointer can linger after its end (nothing nils
				// SleepingUntil/BreakUntil until wake/break-expiry runs). Treat
				// "resting" as a window whose end is still ahead of now; an
				// already-ended window credits at most its final unit, then the
				// cursor is cleared below so a later window starts clean rather
				// than crediting the whole gap from a stale cursor.
				expired := !end.After(now)

				// First time we observe this actor resting: start the cursor
				// here and credit nothing this pass (we don't know how long it
				// has been resting; counting begins now). The sleep/break
				// commands (ZBBS-HOME-284 #2/#4) also stamp the cursor at
				// window-open so the very first minute counts — this is the
				// belt-and-suspenders init for any window that appeared without
				// a stamp (e.g. loaded from a checkpoint). A nil cursor against
				// an already-expired window is left nil: nothing to credit.
				if a.LastTirednessRecoveryAt == nil {
					if !expired {
						t := now
						a.LastTirednessRecoveryAt = &t
					}
					continue
				}

				// Credit only up to the window's end — don't pay out minutes
				// the actor hasn't actually slept/rested yet.
				recoveryTo := *end
				if now.Before(recoveryTo) {
					recoveryTo = now
				}
				elapsedMin := recoveryTo.Sub(*a.LastTirednessRecoveryAt).Minutes()
				units := 0
				if elapsedMin > 0 {
					units = int(math.Floor(elapsedMin*rate + recoveryEpsilon))
				}
				if units > 0 {
					before := a.Needs["tiredness"]
					after := ClampNeed(before - units)
					a.Needs["tiredness"] = after

					// Advance the cursor by exactly the time the whole units
					// cover; the remaining fraction stays in the next pass's
					// window. Integer math off rateX100 avoids a binary-float
					// round-trip (units/rate as float64).
					advance := time.Duration(int64(units) * 100 * int64(time.Minute) / int64(rateX100))
					nc := a.LastTirednessRecoveryAt.Add(advance)
					a.LastTirednessRecoveryAt = &nc

					if after < before {
						recovered++ // count only when tiredness actually dropped
					}
				}
				// Sub-unit fraction (units == 0) carries forward via the
				// unchanged cursor — UNLESS the window has ended, in which case
				// drop the cursor (and the sub-unit remainder, v1 parity) so the
				// next window can't be credited from this expired one.
				if expired {
					a.LastTirednessRecoveryAt = nil
				}
			}
			return recovered, nil
		},
	}
}

// laterTime returns a pointer to the later of two optional times, or nil when
// both are nil. A single non-nil argument wins (mirrors Postgres GREATEST
// ignoring NULLs in the legacy query).
func laterTime(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case a.After(*b):
		return a
	default:
		return b
	}
}

// RunTirednessRecoveryTicker owns the recovery-sweep goroutine. Wakes every
// TirednessRecoveryTickerInterval and submits a RecoverTiredness command.
// Caller starts it in a goroutine alongside World.Run; returns when ctx is
// cancelled.
func RunTirednessRecoveryTicker(ctx context.Context, w *World) {
	t := time.NewTicker(TirednessRecoveryTickerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.beatTicker("tiredness_recovery")
			runTirednessRecoveryIteration(ctx, w)
		}
	}
}

func runTirednessRecoveryIteration(ctx context.Context, w *World) {
	res, err := w.SendContext(ctx, RecoverTiredness(time.Now().UTC()))
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("sim/tiredness_recovery: sweep failed: %v", err)
		}
		return
	}
	if n := res.(int); n > 0 {
		log.Printf("sim/tiredness_recovery: credited recovery to %d actor(s)", n)
	}
}
