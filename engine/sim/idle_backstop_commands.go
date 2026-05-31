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
// Scope: KindNPCStateful AND KindNPCShared, excluding transient visitors
// (VisitorState != nil). PCs (KindPC) don't tick via the reactor (player-
// driven); idle backstop is meaningless for them. Visitors fire on
// encounter via the existing speech / huddle subscribers but don't need
// engine-injected liveness — ExpiresAt drives their lifecycle. See
// shared/notes/codebase/salem-engine-v2/visitor.
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
//   - Off-stage / deceased actors. Belongs in actorCanReactNow alongside
//     the sleeping/resting check that already lives there. Subsystems
//     for these states haven't ported yet.
//
//   - Noop-tick prevention (actor has no needs / no peers / nothing
//     to act on). Belongs in tick-handler preflight, which has full
//     perception in hand. Applies to all warrant kinds, not just idle.
//
// Sleeping/resting are filtered at the reactor evaluator gate, not
// here — the cascade slice still stamps the warrant on a sleeping
// actor, and `actorCanReactNow` shelves it (eligible=false, stale=false)
// when the evaluator picks it up. When the actor transitions out of
// sleeping/resting, the warrant fires on the next scan. This is the
// "engine evaluator will clear an idle warrant on a sleeping actor for
// free" pattern the original cascade design pass anchored on.

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
				// Transient-visitor skip: visitors fire on encounter
				// (nearby PC speaks → speech reactor stamps a warrant via
				// the existing huddle subscribers) but don't need engine-
				// injected liveness — they have ExpiresAt to drive their
				// lifecycle. An idle-backstop tick on a visitor who has
				// nothing scheduled to do would burn tokens for no
				// observable behavior. See
				// shared/notes/codebase/salem-engine-v2/visitor.
				if a.VisitorState != nil {
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
				// Compute quiet once and clamp at zero — a wall-clock
				// jump backward or a test-supplied `now` before the
				// effective anchor would otherwise produce a negative
				// duration. (R1 fix.)
				quiet := time.Duration(0)
				if !effective.IsZero() {
					quiet = now.Sub(effective)
					if quiet < 0 {
						quiet = 0
					}
				}
				// Boundary: "older than threshold" is strict — at exactly
				// the threshold (quiet == threshold), the actor is "at
				// threshold," not "past it." Stamp only when quiet >
				// threshold. (R1 fix.)
				if effective.IsZero() || quiet <= threshold {
					t.SkippedRecentlyTicked++
					continue
				}
				// tryStampWarrant is guaranteed to stamp (return true)
				// given the pre-conditions enforced above: non-nil actor,
				// non-nil Reason, no open WarrantedSince (open-cycle path
				// checks SourceEventID != 0 and bails on zero-source like
				// ours), no in-flight markers, no recently-consumed dedup
				// (same SourceEventID == 0 bypass). MaxWarrantsPerActor cap
				// caps the list size via drop-oldest, which is still a
				// stamp. So t.Stamped++ is accurate; the return is ignored
				// here (the red-need backstop is the consumer that checks
				// it — ZBBS-HOME-363).
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
