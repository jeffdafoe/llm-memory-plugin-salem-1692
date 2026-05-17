package sim

import "time"

// idle_backstop_commands.go — substrate primitive for the idle-backstop
// cascade slice. The Command lives in sim/ (alongside StampWarrant,
// EvaluateReactors, FindConsolidationCandidates) because it operates on
// substrate state — Actor.RecentReactorTicks, WarrantedSince,
// TickInFlight — and routes through the tryStampWarrant funnel. The
// goroutine driver that pumps it on a cadence lives in
// engine/sim/cascade/idle_backstop.go.
//
// Criterion B (locked design): an actor qualifies for an idle-backstop
// warrant when all three hold:
//
//   1. effectiveLastActivity older than IdleBackstopThreshold, where
//      effectiveLastActivity = max(lastReactorTickAt, World.LoadedAt).
//      The LoadedAt floor is the cold-start anchor: a fresh-loaded
//      actor has no RecentReactorTicks history (lastReactorTickAt
//      returns ok=false), but they aren't idle — they just woke up
//      with the world. Treating them as "active at LoadedAt" prevents
//      a backstop storm on the first post-restart sweep without
//      polluting the lastReactorTickAt semantics other consumers
//      (MinReactorTickGap, rate gate) rely on.
//   2. No open warrant cycle (a.WarrantedSince == nil). An actor already
//      pending a tick doesn't need engine-injected liveness — they have
//      a real reason coming.
//   3. Not mid-tick (!a.TickInFlight). An actor currently inside the
//      LLM call doesn't need a parallel idle warrant queued for the
//      next attempt; existing reactor semantics already handle that.
//
// Scope: KindNPCStateful AND KindNPCShared. PCs (KindPC) don't tick via
// the reactor (player-driven); idle backstop is meaningless for them.
//
// Source-event: idle backstop fires from the absence of activity, not
// a specific stimulus. WarrantMeta is stamped with SourceEventID = 0
// (not event-sourced) by design — the cascade slice's pre-filter
// against already-warranted actors makes the substrate's source-aware
// dedup paths redundant for this kind. See
// IdleBackstopWarrantReason.DedupDiscriminator (returns 0).
//
// Force flag: false. Idle backstop fires on minute scales (default
// 30 min); the 5s MinReactorTickGap pacing floor and per-minute rate
// gate aren't blockers. Force stays reserved for WarrantKindAdmin
// (operator-injected ticks).
//
// What it does NOT check today (deferred):
//
//   - Sleeping / resting actors. Belongs in actorCanReactNow (substrate
//     eligibility primitive) alongside the future asleep / off-stage /
//     deceased checks, not in the cascade slice. The reactor evaluator
//     will clear an idle warrant on a sleeping actor for free once that
//     check lands.
//
//   - Noop-tick prevention (actor has no needs / no peers / nothing
//     to act on). Belongs in tick-handler preflight, which has full
//     perception in hand. Applies to all warrant kinds, not just idle.

// IdleBackstopTelemetry is the return value of EvaluateIdleBackstop.
// Stamped reports how many actors got a fresh idle warrant on this
// sweep; Skipped breaks down why the rest didn't qualify. Useful for
// telemetry / admin dashboards and load-bearing for the unit tests.
type IdleBackstopTelemetry struct {
	Stamped               int
	SkippedScope          int // not KindNPCStateful / KindNPCShared
	SkippedRecentlyTicked int // lastReactorTickAt within threshold
	SkippedWarranted      int // open WarrantedSince cycle
	SkippedTickInFlight   int // mid-tick
}

// EvaluateIdleBackstop returns a Command that scans the world's actors,
// applies criterion B + scope, and stamps a WarrantKindIdleBackstop
// warrant on each qualifying actor.
//
// Runs on the world goroutine via SendContext from the cascade driver
// (cascade.RunIdleBackstop). Single round-trip per sweep: the Fn does
// the entire scan + stamp loop atomically, no inter-step SendContext.
// Calls tryStampWarrant inline (already on the world goroutine).
//
// now is the wall-clock moment the sweep started; passed in so tests
// can drive deterministic time-based scenarios.
func EvaluateIdleBackstop(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			threshold := w.Settings.IdleBackstopThreshold
			if threshold <= 0 {
				threshold = defaultIdleBackstopThreshold
			}
			var t IdleBackstopTelemetry
			for _, a := range w.Actors {
				if a.Kind != KindNPCStateful && a.Kind != KindNPCShared {
					t.SkippedScope++
					continue
				}
				if a.WarrantedSince != nil {
					t.SkippedWarranted++
					continue
				}
				if a.TickInFlight {
					t.SkippedTickInFlight++
					continue
				}
				// effectiveLastActivity: actual tick history if any, else
				// fall back to the world's LoadedAt anchor. max() rather
				// than just-the-tick because the LoadedAt floor must not
				// regress a "ticked since load" actor back to load time.
				effective := w.LoadedAt
				if lastTick, ok := lastReactorTickAt(a); ok && lastTick.After(effective) {
					effective = lastTick
				}
				if !effective.IsZero() && now.Sub(effective) < threshold {
					t.SkippedRecentlyTicked++
					continue
				}
				var quiet time.Duration
				if !effective.IsZero() {
					quiet = now.Sub(effective)
				}
				tryStampWarrant(w, a, WarrantMeta{
					TriggerActorID: a.ID,
					Force:          false,
					Reason:         IdleBackstopWarrantReason{QuietDuration: quiet},
				}, now)
				t.Stamped++
			}
			return t, nil
		},
	}
}
