package sim

import (
	"fmt"
	mathrand "math/rand/v2"
	"time"
)

// StampWarrantResult is what StampWarrant returns. Stamped is true when
// the call started a fresh warrant cycle (actor wasn't warranted before);
// false when it appended to an existing cycle.
type StampWarrantResult struct {
	Stamped bool // true on fresh cycle, false on append-to-existing
}

// StampWarrant returns a Command that funnels a warrant stamp through
// tryStampWarrant. Public command form for callers outside the package
// (admin endpoints, test setup); internal callsites in command handlers
// call tryStampWarrant directly (they already hold the world goroutine).
//
// Rejects with error when actorID isn't a known actor. meta.Reason must
// be non-nil — that's the load-bearing carrier for the warrant kind.
func StampWarrant(actorID ActorID, meta WarrantMeta, now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if meta.Reason == nil {
				return StampWarrantResult{}, fmt.Errorf("warrant meta requires a non-nil Reason")
			}
			actor, ok := w.Actors[actorID]
			if !ok {
				return StampWarrantResult{}, fmt.Errorf("actor %q not found", actorID)
			}
			fresh := actor.WarrantedSince == nil
			tryStampWarrant(w, actor, meta, now)
			return StampWarrantResult{Stamped: fresh}, nil
		},
	}
}

// CompleteReactorTickResult is what CompleteReactorTick returns. Stale is
// true when the completion's AttemptID didn't match the actor's current
// TickAttemptID — the completion is for an attempt the world has already
// moved past (typed out, superseded). Stale completions are a no-op.
type CompleteReactorTickResult struct {
	Stale bool
}

// TickResult is the slim outcome of an LLM tick. PR 2 ships the type as a
// placeholder so the completion command signature is stable; PR 3 fills
// in what the tick handler returns (tool calls applied, speech emitted,
// etc.).
type TickResult struct {
	// Reserved for PR 3. Empty in PR 2.
}

// CompleteReactorTick returns a Command that records the completion of an
// in-flight reactor tick. The command:
//
//   - Returns Stale=true with no mutation if the AttemptID doesn't match
//     the actor's current TickAttemptID. This catches the case where a
//     timed-out attempt-1 worker returns AFTER attempt-2 has started —
//     without the check, attempt-1's completion would clear attempt-2's
//     in-flight flag and the world would think no tick was running.
//
//   - On match: clears TickInFlight and TickAttemptID. Does NOT clear
//     WarrantedSince / WarrantDueAt / Warrants — a fresh warrant cycle
//     may have started while the LLM call was pending and must survive
//     the completion to fire on the next evaluator pass.
//
// Result handling (applying tool calls, mutating state per the LLM's
// returned actions) is PR 3's responsibility — this command just signals
// "the LLM round-trip finished for attempt X."
func CompleteReactorTick(actorID ActorID, attemptID string, _ TickResult) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			actor, ok := w.Actors[actorID]
			if !ok {
				return CompleteReactorTickResult{}, fmt.Errorf("actor %q not found", actorID)
			}
			if actor.TickAttemptID != attemptID {
				return CompleteReactorTickResult{Stale: true}, nil
			}
			actor.TickInFlight = false
			actor.TickAttemptID = ""
			return CompleteReactorTickResult{Stale: false}, nil
		},
	}
}

// EvaluateReactors returns a Command that scans every actor for due
// warrants and emits ReactorTickDue events for those that pass the
// eligibility and rate gates. Called periodically by the evaluator
// AfterFunc chain (see reactor_evaluator.go); exported here so tests can
// drive the body synchronously without timing dependencies.
//
// For each due actor:
//
//  1. actorCanReactNow filters out asleep/concluded-huddle/etc. Stale
//     warrants (returns stale=true) are cleared inline; the warrant cycle
//     is dropped — the conversational context no longer applies.
//
//  2. checkRateGate enforces MaxReactorTicksPerActorPerMinute. Capped
//     actors get their WarrantDueAt pushed to the next allowed time
//     rather than dropped — the warrant survives, just delayed.
//
//  3. Warrant is consumed at EMIT time (clearWarrant) — see reactor.go.
//     TickInFlight + TickAttemptID set. RecentReactorTicks ring appended
//     for the rate-gate window count.
//
//  4. ReactorTickDue emitted with the consumed Warrants list.
//
// After the scan, the next AfterFunc evaluation is re-armed via
// armNextEvaluation. Idempotent re-arming: if a re-arm already happened
// during this command (shouldn't, since we're in the world goroutine
// throughout), the second one is a no-op.
func EvaluateReactors(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			rateCap := w.Settings.MaxReactorTicksPerActorPerMinute
			window := defaultRateWindow

			for _, actor := range w.Actors {
				if !actorReactorDue(actor, now) {
					continue
				}

				eligible, stale := actorCanReactNow(w, actor)
				if stale {
					clearWarrant(actor)
					continue
				}
				if !eligible {
					// Temporarily unavailable (asleep, etc. — none of these
					// land in PR 2 but the hook exists). Push WarrantDueAt
					// out by a backoff so we don't reconsider this actor on
					// every 250ms scan. The actor's caller-of-record (whatever
					// subsystem set the unavailability) is responsible for
					// clearing the warrant if it stays unavailable indefinitely.
					next := now.Add(unavailableBackoff)
					actor.WarrantDueAt = &next
					continue
				}

				// Rate-gate check. Capped actors get their fire delayed to
				// the next-allowed boundary (the cap'th-oldest entry in
				// the window expires at that time). The cap is a
				// settings-driven gross gate — no $ math.
				//
				// Force-bypass: any pending warrant with Force=true skips
				// the rate gate. Used by admin overrides and emergency
				// reasons that must fire even when an actor is loud.
				if rateCap > 0 && !hasForcedWarrant(actor.Warrants) &&
					!checkRateGate(actor, now, rateCap, window) {
					next := nextRateAllowedAt(actor, now, rateCap, window)
					actor.WarrantDueAt = &next
					continue
				}

				// Snapshot the warrant cycle metadata BEFORE clearing.
				warrantsCopy := append([]WarrantMeta(nil), actor.Warrants...)
				warrantedSince := *actor.WarrantedSince
				dueAt := *actor.WarrantDueAt

				clearWarrant(actor)
				actor.TickInFlight = true
				actor.TickAttemptID = newTickAttemptID()
				recordReactorTick(actor, now, rateCap)

				w.emit(ReactorTickDue{
					ActorID:        actor.ID,
					AttemptID:      actor.TickAttemptID,
					Warrants:       warrantsCopy,
					WarrantedSince: warrantedSince,
					DueAt:          dueAt,
					EmittedAt:      now,
				})
			}

			armNextEvaluation(w)
			return nil, nil
		},
	}
}

// nextRateAllowedAt computes the earliest time at which the actor will
// be below the per-minute cap. The expiring entry that matters is the
// (len(inWindow) - cap)th — the one that, when it drops out of the
// window, brings the in-window count down to cap-1. Adds a small jitter
// so co-capped actors don't all clear simultaneously.
//
// When len(inWindow) < cap the cap isn't actually breached (caller
// shouldn't have invoked this) — returns now to avoid pushing the fire
// out of the present.
func nextRateAllowedAt(a *Actor, now time.Time, cap int, window time.Duration) time.Time {
	if a.RecentReactorTicks == nil {
		return now
	}
	ticks := a.RecentReactorTicks.Snapshot()
	// Drop ticks already outside the window — they don't count toward the cap.
	cutoff := now.Add(-window)
	inWindow := ticks[:0]
	for _, t := range ticks {
		if t.After(cutoff) {
			inWindow = append(inWindow, t)
		}
	}
	if cap <= 0 || len(inWindow) < cap {
		return now
	}
	idx := len(inWindow) - cap
	return inWindow[idx].Add(window).Add(rateBackoffJitter())
}

// hasForcedWarrant returns true if any meta in the list has Force=true.
// Linear scan; the list is bounded by Settings.MaxWarrantsPerActor
// (default 16) so this is cheap.
func hasForcedWarrant(list []WarrantMeta) bool {
	for _, m := range list {
		if m.Force {
			return true
		}
	}
	return false
}

// rateBackoffJitter returns a small randomized offset (50-250ms) used to
// stagger rate-cap clearings so several co-capped actors don't all re-
// fire on the same scan cycle.
func rateBackoffJitter() time.Duration {
	return 50*time.Millisecond + time.Duration(mathrand.Int64N(int64(200*time.Millisecond)))
}

// unavailableBackoff is the delay applied when actorCanReactNow returns
// eligible=false (but not stale). A short backoff lets the evaluator
// recheck soon without burning every 250ms scan cycle on the same actor.
const unavailableBackoff = 2 * time.Second
