package sim

import "time"

// red_need_backstop_commands.go — substrate primitive for the red-need
// backstop cascade slice (ZBBS-HOME-363). The Command lives in sim/
// (alongside EvaluateIdleBackstop, IncrementNeedsTick) because it operates
// on substrate state — Actor.Needs, the WarrantedSince/TickInFlight tick
// markers, and the per-actor RedNeed* backoff pacing fields — and routes
// through the tryStampWarrant funnel. The goroutine driver that pumps it on
// a cadence lives in engine/sim/cascade/red_need_backstop.go.
//
// WHY THIS EXISTS. The hourly needs tick re-stamps a NeedThresholdWarrant
// while a need stays red (HOME-329 level-trigger), but it only runs on the
// game-hour boundary. The only other thing that re-engages an idle actor is
// the 30-min idle backstop. So an actor that burns a reactor tick FAILING to
// resolve a red need (a starving NPC who can't transact with an unavailable
// keeper, or who bought food but went idle before eating it) sits frozen for
// up to a game-hour / 30 min before anything re-engages it — the live
// "man alone in a field, starving" symptom. This sweep re-engages such an
// actor promptly.
//
// COST DISCIPLINE — the load-bearing constraint. Every warrant becomes an
// LLM deliberation, so a genuinely-UNRESOLVABLE red need must not re-warrant
// on a tight loop or it burns tokens forever. The per-actor cadence is an
// EXPONENTIAL BACKOFF (RedNeedBackoffLevel): the gap doubles every sweep the
// need makes no progress, base (default 90 s) → … → cap (default 30 min =
// the idle-backstop rate). So a permanently-stuck actor's steady-state cost
// is no worse than the idle backstop. A need that drops between stamps
// (real progress — only consume / dwell / admin lower a need, and dwell is
// excluded upstream) resets the backoff to base so a resolving actor keeps
// getting nudged until it is fed; a brand-new red need also starts at base.
// When the need falls below its threshold the actor is no longer eligible —
// no warrant, no cost — and its backoff state is cleared. tryStampWarrant's
// own MinReactorTickGap + per-minute rate gate are a second cost backstop.
//
// Scope mirrors the idle backstop: KindNPCStateful + KindNPCShared, excluding
// transient visitors (VisitorState != nil; they run their own ExpiresAt
// lifecycle). PCs don't reactor-tick. "Actionable red need" — at/past
// threshold, not dwell-credited, and on-shift for tiredness — is the shared
// actorActionableRedNeed predicate (needs_tick.go), so this sweep and the
// hourly producer can never disagree on what presses an actor.

// RedNeedBackstopTelemetry is the return value of EvaluateRedNeedBackstop.
// Stamped is how many actors got a fresh red-need warrant this sweep; the
// Skipped* breakdown is why the rest didn't, for telemetry + the unit tests.
type RedNeedBackstopTelemetry struct {
	Stamped             int
	SkippedScope        int // not KindNPCStateful / KindNPCShared, or a visitor
	SkippedNoRedNeed    int // no actionable red need (backoff state cleared)
	SkippedWarranted     int // open WarrantedSince cycle
	SkippedTickInFlight  int // mid-tick
	SkippedBackoff       int // stalled need still inside its backoff window
	SkippedStampDeclined int // tryStampWarrant funnel declined (unreachable today)
}

// EvaluateRedNeedBackstop returns a Command that scans the world's actors and
// stamps a WarrantKindNeedThreshold warrant on each in-scope actor with an
// actionable red need whose per-actor backoff window has elapsed (or who has
// made progress since the last stamp). Reuses NeedThresholdWarrantReason —
// the stimulus IS the same standing red need; only the producer (fast,
// backoff-paced) differs from the hourly needs tick — so perception renders
// it identically and hasNeedWarrant (break interrupt) recognizes it.
//
// Runs on the world goroutine via SendContext from the cascade driver. The
// whole scan + stamp + backoff-state update happens inside the single Fn,
// no inter-step SendContext. now is the wall-clock the sweep started, passed
// in so tests can drive deterministic time-based scenarios.
func EvaluateRedNeedBackstop(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			base := w.Settings.RedNeedBackstopBaseDelay
			if base <= 0 {
				base = defaultRedNeedBackstopBaseDelay
			}
			maxDelay := w.Settings.RedNeedBackstopMaxDelay
			if maxDelay <= 0 {
				maxDelay = defaultRedNeedBackstopMaxDelay
			}
			if maxDelay < base {
				maxDelay = base
			}
			nowMinute := localMinuteOfDay(w, now)

			var t RedNeedBackstopTelemetry
			for _, a := range w.Actors {
				if a.Kind != KindNPCStateful && a.Kind != KindNPCShared {
					t.SkippedScope++
					continue
				}
				if a.VisitorState != nil {
					t.SkippedScope++
					continue
				}

				redNeed, ok := actorActionableRedNeed(w, a, now, nowMinute)
				if !ok {
					// Nothing pressing (resolved, or recovering via dwell, or
					// off-shift tiredness). Clear the backoff so the NEXT time
					// this actor goes red it re-engages from base rather than
					// inheriting a stale escalated timer.
					clearRedNeedBackstop(a)
					t.SkippedNoRedNeed++
					continue
				}

				// An actor already pending a tick or mid-LLM-call doesn't need
				// an injected warrant — that tick's perception already sees the
				// need. Don't touch the backoff timer either: let the in-flight
				// result drive progress/stall on the next clean sweep.
				if a.WarrantedSince != nil {
					t.SkippedWarranted++
					continue
				}
				if a.TickInFlight {
					t.SkippedTickInFlight++
					continue
				}

				curVal := a.Needs[redNeed]
				// "paced" = we have already stamped THIS need and are tracking
				// its backoff. progressed = the value fell since that stamp
				// (only consume/dwell/admin lower a need, and dwell is excluded
				// from actorActionableRedNeed, so a drop on a still-red need is
				// genuine resolution work in progress).
				paced := a.RedNeedLastKey == redNeed && a.RedNeedNextWarrantAt != nil
				progressed := paced && curVal < a.RedNeedLastValue

				// A STALLED need that is still inside its backoff window waits —
				// this is the cost guard. Progress, or a brand-new red need
				// (paced == false), bypasses the timer and re-engages at base.
				if paced && !progressed && now.Before(*a.RedNeedNextWarrantAt) {
					t.SkippedBackoff++
					continue
				}

				level := 0
				if paced && !progressed {
					// Due and stalled → escalate. Progress / fresh need stays 0.
					level = a.RedNeedBackoffLevel + 1
				}
				delay := redNeedBackoffDelay(base, maxDelay, level)

				// Only advance the backoff (and count the stamp) if the funnel
				// actually recorded the warrant. The WarrantedSince/TickInFlight
				// pre-checks above make a decline unreachable today (the reason
				// is zero-sourced, so the source-dedup paths don't apply), but
				// gating on the real result keeps the pacing correct-by-
				// construction if a future stamp gate is added — a declined
				// stamp must NOT pace the actor for a deliberation that never
				// happened (that would silently suppress re-engagement).
				if !tryStampWarrant(w, a, WarrantMeta{
					TriggerActorID: a.ID,
					Reason:         NeedThresholdWarrantReason{Need: redNeed},
				}, now) {
					t.SkippedStampDeclined++
					continue
				}

				next := now.Add(delay)
				a.RedNeedNextWarrantAt = &next
				a.RedNeedBackoffLevel = level
				a.RedNeedLastKey = redNeed
				a.RedNeedLastValue = curVal
				t.Stamped++
			}
			return t, nil
		},
	}
}

// redNeedBackoffDelay returns base doubled `level` times, clamped to
// maxDelay. The clamp-BEFORE-doubling guard (return maxDelay once d would
// exceed it) is what makes this overflow-safe: a plain `d *= 2` loop that
// only checks `d < maxDelay` could wrap int64 negative for a large
// base+maxDelay before the post-loop clamp runs, returning a negative
// duration. Callers pass already-positive base/maxDelay (the Command clamps
// to defaults); the guards here keep the helper safe if reused.
func redNeedBackoffDelay(base, maxDelay time.Duration, level int) time.Duration {
	if base <= 0 {
		return 0
	}
	if maxDelay < base {
		return base
	}
	d := base
	for i := 0; i < level; i++ {
		if d > maxDelay/2 {
			return maxDelay
		}
		d *= 2
	}
	if d > maxDelay {
		return maxDelay
	}
	return d
}

// clearRedNeedBackstop resets an actor's red-need backoff pacing. Called
// when the actor has no actionable red need (so the next red need starts at
// base) and on LoadWorld via resetReactorStateOnLoad.
func clearRedNeedBackstop(a *Actor) {
	a.RedNeedNextWarrantAt = nil
	a.RedNeedBackoffLevel = 0
	a.RedNeedLastKey = ""
	a.RedNeedLastValue = 0
}
