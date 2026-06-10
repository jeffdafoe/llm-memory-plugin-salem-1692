package sim

import (
	"context"
	"log"
	"sort"
	"time"
)

// huddle_silence_sweep.go — ZBBS-HOME-417. Silence-conclusion sweep for
// huddles.
//
// A structure-bound huddle has no routine conclusion path at a staffed
// location: leaveCurrentHuddle only concludes on the last-member-leave, and the
// keeper is always present, so one huddle accreted every exchange at the
// structure for days (the live Tavern huddle was 8 days old). The durable
// structure scene living forever is intended (commerce context); the huddle
// living forever is the defect. This sweep is the missing lever: a huddle with
// no spoken line, join, or completed transaction for HuddleSilenceTimeout is
// concluded — SILENTLY (concludeHuddleInner with stampWarrants=false), so a
// dormant conversation ending doesn't manufacture a round of LLM ticks. The
// next speak re-forms a fresh huddle (on the same durable scene), which also
// rotates the conversation_id grouping for the admin chat viewer.
//
// Cadence: WorldSettings.HuddleSilenceSweepCadence (default 60s via
// HuddleSilenceSweepCadenceDefault). Coalesced AfterFunc self-rearm chain —
// same shape as the pay-ledger / scene-quote / order sweeps.
//
// Lifecycle:
//
//	RunHuddleSilenceSweep(ctx, w)
//	└─> kickHuddleSilenceSweep             // initial arm via the cmd channel
//	     └─> armNextHuddleSilenceSweep      // schedules the first AfterFunc
//	          └─> [cadence] fireScheduledHuddleSilenceSweep
//	               └─> SendContext(evaluateHuddleSilenceAndRearm(now))
//	                    └─> Fn: clear flag, run scan, re-arm

// HuddleSilenceTimeoutDefault is the default dormancy window before a huddle is
// concluded when WorldSettings.HuddleSilenceTimeout is unset (zero). 2h is the
// conservative end Jeff picked: long enough that a patron who steps out and
// returns resumes the same conversation rather than triggering a fresh one,
// short enough that a structure's day breaks into per-session conversations
// instead of one multi-day blob. Tunable down from data via the
// huddle_silence_timeout_minutes setting.
const HuddleSilenceTimeoutDefault = 2 * time.Hour

// HuddleSilenceSweepCadenceDefault is the default scan cadence when
// WorldSettings.HuddleSilenceSweepCadence is unset (zero). 60s matches the
// pay-ledger / scene-quote / order sweeps so admin tuning sees one mental
// model; the ±60s latency is invisible against a 2h timeout.
const HuddleSilenceSweepCadenceDefault = 60 * time.Second

// effectiveHuddleSilenceTimeout returns the configured dormancy window or the
// default when WorldSettings.HuddleSilenceTimeout is zero/unset.
func effectiveHuddleSilenceTimeout(s WorldSettings) time.Duration {
	if s.HuddleSilenceTimeout > 0 {
		return s.HuddleSilenceTimeout
	}
	return HuddleSilenceTimeoutDefault
}

// effectiveHuddleSilenceSweepCadence returns the configured sweep cadence or
// the default when WorldSettings.HuddleSilenceSweepCadence is zero/unset.
func effectiveHuddleSilenceSweepCadence(s WorldSettings) time.Duration {
	if s.HuddleSilenceSweepCadence > 0 {
		return s.HuddleSilenceSweepCadence
	}
	return HuddleSilenceSweepCadenceDefault
}

// RunHuddleSilenceSweep owns the silence-sweep periodic schedule. Caller starts
// this in a goroutine alongside World.Run (next to RunPayLedgerSweep et al.);
// returns when ctx is cancelled. The first sweep is kicked immediately so a
// huddle already dormant past its window at startup doesn't wait a full cadence
// before being concluded.
func RunHuddleSilenceSweep(ctx context.Context, w *World) {
	_, err := w.SendContext(ctx, kickHuddleSilenceSweep())
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/huddle_silence: initial sweep arm failed: %v", err)
	}
	<-ctx.Done()
}

// kickHuddleSilenceSweep returns a Command whose Fn arms the first sweep on the
// world goroutine — mirrors kickPayLedgerSweep.
func kickHuddleSilenceSweep() Command {
	return Command{
		Fn: func(w *World) (any, error) {
			armNextHuddleSilenceSweep(w)
			return nil, nil
		},
	}
}

// armNextHuddleSilenceSweep schedules the next sweep after one cadence
// interval. MUST be called from inside a Command.Fn — touches
// w.huddleSilenceSweep.scheduled without coordination.
//
// Coalescing: no-op when a sweep is already scheduled. The flag clears at the
// start of the scheduled Fn (evaluateHuddleSilenceAndRearm), so a re-arm during
// that Fn queues the next sweep rather than no-opping.
func armNextHuddleSilenceSweep(w *World) {
	if w.huddleSilenceSweep.scheduled {
		return
	}
	w.huddleSilenceSweep.scheduled = true
	cadence := effectiveHuddleSilenceSweepCadence(w.Settings)
	time.AfterFunc(cadence, func() { fireScheduledHuddleSilenceSweep(w) })
}

// fireScheduledHuddleSilenceSweep is the AfterFunc callback body. Factored out
// so tests can drive the post-shutdown path directly (matches
// fireScheduledPayLedgerSweep). Uses LifecycleContext so a shutdown-while-armed
// unblocks SendContext instead of deadlocking on a send to a dead cmds channel.
func fireScheduledHuddleSilenceSweep(w *World) {
	ctx := w.LifecycleContext()
	if ctx.Err() != nil {
		// Shutdown raced us. Clearing the flag would race with the world
		// goroutine; fresh worlds come from LoadWorld / NewWorld, so a
		// post-shutdown stale flag has no effect.
		return
	}
	w.beatTicker("huddle_silence_sweep")
	_, err := w.SendContext(ctx, evaluateHuddleSilenceAndRearm(time.Now()))
	if err != nil && ctx.Err() == nil {
		log.Printf("sim/huddle_silence: scheduled sweep failed: %v", err)
	}
}

// evaluateHuddleSilenceAndRearm clears the scheduled flag, runs one sweep, and
// re-arms — all in one Fn on the world goroutine. Clearing the flag first means
// the re-arm starts a fresh chain rather than seeing the still-set flag and
// no-opping.
func evaluateHuddleSilenceAndRearm(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			w.huddleSilenceSweep.scheduled = false
			res, err := EvaluateHuddleSilenceSweep(now).Fn(w)
			armNextHuddleSilenceSweep(w)
			return res, err
		},
	}
}

// EvaluateHuddleSilenceSweep returns a Command that concludes every active
// huddle whose last conversational activity is older than the configured
// HuddleSilenceTimeout. Exposed as a Command (not just an internal Fn) so tests
// can drive sweeps deterministically without the AfterFunc timing chain.
//
// Dormancy baseline: LastActivityAt, or StartedAt when LastActivityAt is zero
// (a creation site that didn't stamp — e.g. the outdoor huddle path). Already-
// concluded huddles are skipped. Conclusion is SILENT (no per-member warrant)
// so a quiet conversation ending doesn't wake its members into a re-pitch tick.
//
// Collect-then-conclude: concludeHuddleInner emits HuddleConcluded and a
// subscriber could in principle mutate w.Huddles, so the dormant set is
// gathered before any conclusion runs. Iteration is sorted by HuddleID for a
// stable conclusion order (replay-test + admin-trace readability).
func EvaluateHuddleSilenceSweep(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if len(w.Huddles) == 0 {
				return nil, nil
			}
			timeout := effectiveHuddleSilenceTimeout(w.Settings)
			dormant := make([]HuddleID, 0)
			for id, h := range w.Huddles {
				if h == nil || h.ConcludedAt != nil {
					continue
				}
				lastActive := h.LastActivityAt
				if lastActive.IsZero() {
					lastActive = h.StartedAt
				}
				// IsZero baseline guard: a huddle with neither stamp (shouldn't
				// happen — JoinHuddle sets both) has no measurable dormancy, so
				// leave it for a later sweep once it gains a timestamp.
				if lastActive.IsZero() {
					continue
				}
				if now.Sub(lastActive) >= timeout {
					dormant = append(dormant, id)
				}
			}
			if len(dormant) == 0 {
				return nil, nil
			}
			sort.Slice(dormant, func(i, j int) bool { return dormant[i] < dormant[j] })
			for _, id := range dormant {
				// Re-check: an earlier conclusion's subscriber could have
				// already concluded/removed this one (defensive, matches the
				// pay-ledger sweep posture).
				if h, ok := w.Huddles[id]; !ok || h == nil || h.ConcludedAt != nil {
					continue
				}
				concludeHuddleInner(w, id, now, false)
			}
			return nil, nil
		},
	}
}
