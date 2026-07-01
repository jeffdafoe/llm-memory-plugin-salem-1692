package sim

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"
)

// Needs mutations — in-memory port of the legacy hourly tick + consumption
// + movement fatigue paths.
//
// Three command-channel entry points:
//
//   ApplyConsumption(actorID, delta)
//       Decrement (or increment) one actor's needs. Used by pay-side food/
//       drink effects, admin reset, and future well-drink etc. Clamps into
//       [0, NeedMax]. Returns post-clamp values for readback.
//
//   IncrementNeedsTick()
//       Hourly batch increment across all eligible actors. Fired by
//       RunNeedsTicker once per minute (no-op when the hour hasn't rolled).
//       Catch-up cap protects from outage shock; sleeping actors and
//       decoratives are skipped.
//
//   ApplyMovementFatigue(actorID, fromX, fromY, toX, toY)
//       Tiredness bump proportional to walked distance. Short walks floor
//       to zero — popping next door is free, by design.
//
// PER-NEED-PER-ACTOR DWELL-CREDIT SKIP — see hasFreshDwellCreditForAttribute
// below. Ported from v1 ZBBS-HOME-214 (ZBBS-WORK-346): a need increment
// is suppressed for the hour when the actor has a fresh dwell-credit row
// on that need, so dwelling at a Shade Tree doesn't see same-hour needs-
// tick accrual cancel out the per-minute dwell recovery delta.

// NeedDelta is the per-need change vector consumption submits. Negative
// values reduce, positive increase, zero leaves alone. Sparse map: keys
// absent from the delta don't touch the corresponding need.
type NeedDelta map[NeedKey]int

// ConsumptionResult is the post-clamp need set returned through the
// command reply. Callers rendering "before/after" readback use this
// rather than re-reading state.
type ConsumptionResult struct {
	Needs NeedSet
}

// ApplyConsumption returns a Command that applies delta to actor's needs,
// clamps into [0, NeedMax], and returns the post-clamp values.
//
// Returns an error if the actor isn't in the world (returned via the
// CommandResult.Err). Missing need keys on the actor are treated as 0 +
// logged (matches the safety-net behavior of legacy SnapshotNeeds).
func ApplyConsumption(actorID ActorID, delta NeedDelta) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, ok := w.Actors[actorID]
			if !ok {
				return nil, fmt.Errorf("actor %q not found", actorID)
			}
			if a.Needs == nil {
				a.Needs = make(map[NeedKey]int)
			}

			out := make(NeedSet, len(Needs))
			for _, n := range Needs {
				current := a.Needs[n.Key]
				bump := delta[n.Key]
				next := ClampNeed(current + bump)
				a.Needs[n.Key] = next
				out[n.Key] = next
			}
			return ConsumptionResult{Needs: out}, nil
		},
	}
}

// ApplyMovementFatigue returns a Command that adds tiredness proportional
// to the Euclidean distance from→to. World coords are pixels with
// tileSize=32; per-tile bump is WorldSettings.MovementFatiguePerTileX100.
//
// Short walks (sub-tile) floor to zero — by design, no fatigue for
// stepping next door. Settings.MovementFatiguePerTileX100 == 0 disables
// the mechanic entirely (admin off-switch).
func ApplyMovementFatigue(actorID ActorID, fromX, fromY, toX, toY int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, ok := w.Actors[actorID]
			if !ok {
				return nil, fmt.Errorf("actor %q not found", actorID)
			}
			perTileX100 := w.Settings.MovementFatiguePerTileX100
			if perTileX100 <= 0 {
				return 0, nil
			}
			const tileSize = 32.0
			dx := float64(toX - fromX)
			dy := float64(toY - fromY)
			tiles := math.Sqrt(dx*dx+dy*dy) / tileSize
			bump := int(tiles * float64(perTileX100) / 100.0)
			if bump <= 0 {
				return 0, nil
			}
			if a.Needs == nil {
				a.Needs = make(map[NeedKey]int)
			}
			a.Needs["tiredness"] = ClampNeed(a.Needs["tiredness"] + bump)
			return bump, nil
		},
	}
}

// NeedThresholdWarrantReason is the WarrantReason stamped when a need sits at or
// past its red threshold (ZBBS-WORK-277 producer #1; ZBBS-HOME-329 made it
// level-triggered — re-pressured each needs tick while red, not just on the
// upward crossing). Need is the need whose red line triggered the warrant —
// carried for telemetry / admin replay; the deliberation reads the full need
// set from perception, not this field. DedupDiscriminator returns 0: a red-tier
// stat is not event-
// sourced, so it bypasses the substrate's source-key dedup paths (the
// per-actor WarrantedSince gate in the producer is what prevents double-stamp).
// Mirrors IdleBackstopWarrantReason — the other condition-driven, zero-sourced
// reason.
type NeedThresholdWarrantReason struct {
	Need NeedKey
}

func (NeedThresholdWarrantReason) isWarrantReason()           {}
func (NeedThresholdWarrantReason) Kind() WarrantKind          { return WarrantKindNeedThreshold }
func (NeedThresholdWarrantReason) DedupDiscriminator() uint64 { return 0 }

// NeedsTickDwellSkipWindow is the freshness cutoff for the per-attribute
// dwell-credit skip in IncrementNeedsTick — mirrors v1's INTERVAL '1 hour'
// in the legacy needs_tick NOT EXISTS predicate (ZBBS-HOME-214). Matched
// to the hourly tick cadence: if the actor was dwelling at any point
// during this tick's hour, skip the increment for that attribute. Binary
// skip — partial-hour accuracy isn't the goal; the per-minute dwell sweep
// handles real-time recovery.
const NeedsTickDwellSkipWindow = time.Hour

// hasFreshDwellCreditForAttribute reports whether actor has any DwellCredit
// whose Attribute matches the given need key and was credited within the
// freshness window. Source-agnostic: both object-source (resting at a Shade
// Tree) and item-source (still digesting stew) credits suppress the same-
// tick accrual on that attribute. Mirrors v1 ZBBS-HOME-214's
// actor_dwell_credit NOT EXISTS predicate (ZBBS-WORK-346).
func hasFreshDwellCreditForAttribute(a *Actor, attr NeedKey, now time.Time, freshness time.Duration) bool {
	if a == nil || len(a.DwellCredits) == 0 {
		return false
	}
	cutoff := now.Add(-freshness)
	for _, c := range a.DwellCredits {
		if c == nil {
			continue
		}
		if c.Attribute != attr {
			continue
		}
		if c.LastCreditedAt.After(cutoff) {
			return true
		}
	}
	return false
}

// actorActionableRedNeed returns the first need (in canonical Needs order)
// that should drive an LLM deliberation right now: it sits at or past its
// red threshold, has no fresh dwell-credit suppressing it (the actor is
// already recovering it by dwelling), and — for tiredness — the actor is
// on shift (overnight tiredness is the deterministic sleep loop's job, not
// a deliberation). ok=false when nothing qualifies.
//
// A sleeping actor never has an actionable need (LLM-135). Hunger/thirst keep
// climbing while it sleeps (IncrementNeedsTick), but the rising need must not
// wake it — it surfaces on wake, not at 3am. This guard is the single point
// that suppresses the mid-sleep wake on BOTH warrant paths that share this
// predicate (the hourly tick producer and the red-need backstop sweep).
//
// Single source of truth for "what red need presses this actor": shared by
// the hourly needs-tick warrant producer (IncrementNeedsTick) and the
// red-need backstop sweep (ZBBS-HOME-363, EvaluateRedNeedBackstop) so the
// two stay consistent — a change to the threshold / dwell / on-shift logic
// updates both at once.
func actorActionableRedNeed(w *World, a *Actor, now time.Time, nowMinute int) (NeedKey, bool) {
	if a.Needs == nil {
		return "", false
	}
	if a.SleepingUntil != nil && a.SleepingUntil.After(now) {
		return "", false
	}
	// LLM-211: an actor on a break is recovering, the same as a sleeper — no red
	// need (including the tiredness the break is curing) should wake it. Mirrors
	// the SleepingUntil suppression above so the break runs to its window rather
	// than the reactor ending it to service the actor's own red need (the
	// take_break churn: reactor_commands.go un-shelves a rester for a red need and
	// calls endBreak at the emit point). Operator-force and PC-speech still
	// interrupt a break — those don't route through this warrant path.
	if a.BreakUntil != nil && a.BreakUntil.After(now) {
		return "", false
	}
	for _, n := range Needs {
		if hasFreshDwellCreditForAttribute(a, n.Key, now, NeedsTickDwellSkipWindow) {
			continue
		}
		threshold := w.Settings.NeedThresholds.Get(n.Key)
		if a.Needs[n.Key] < threshold {
			continue
		}
		if n.Key == "tiredness" && !actorOnShift(w, a, nowMinute) {
			continue
		}
		return n.Key, true
	}
	return "", false
}

// IncrementNeedsTick returns a Command that applies the hourly needs
// increment across all eligible actors.
//
// Eligibility filter, derived from legacy needTickEligibilityPred — LLM-135
// changes sleeping actors from whole-actor skipped to per-need skipped
// (hunger/thirst accrue, tiredness held):
//   - actor must have either LLMAgent or LoginUsername set
//     (decoratives have neither, so they're skipped)
//   - sleeping actors STILL tick hunger + thirst (LLM-135: they wake hungry to
//     drive breakfast demand) but skip tiredness, which the sleep loop is
//     recovering — incrementing it here would fight that recovery. The rising
//     need stays unsurfaced: actorActionableRedNeed returns nothing for a
//     sleeping actor, so neither the warrant below nor the backstop sweep wakes
//     them; it surfaces on wake.
//   - on-break actors STILL accrue hunger + thirst (a vendor on break is awake
//     and should get hungry) but skip tiredness, which the break's recovery
//     sweep is restoring — same treatment as sleep (LLM-211). Like a sleeper, an
//     on-break actor is not warranted while resting (actorActionableRedNeed
//     returns nothing), so the reactor no longer ends the break to service the
//     actor's own red need — the break runs to its window.
//
// Within an eligible actor, per-attribute the increment is skipped if a
// fresh DwellCredit exists for that need (see hasFreshDwellCreditForAttribute)
// — closes v1's "rest under a tree but stat still climbs" wash.
//
// The caller supplies how many capped hours of increment to apply — the
// ticker computes this from the elapsed window. Magnitude per hour comes
// from WorldSettings.NeedsTickAmount.
//
// Returns the count of actors touched.
func IncrementNeedsTick(cappedHours int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if cappedHours <= 0 {
				return 0, nil
			}
			amount := w.Settings.NeedsTickAmount
			if amount <= 0 {
				return 0, fmt.Errorf("NeedsTickAmount must be > 0, got %d", amount)
			}
			bump := amount * cappedHours
			now := time.Now().UTC()
			nowMinute := localMinuteOfDay(w, now)

			touched := 0
			for _, a := range w.Actors {
				if a.LLMAgent == "" && a.LoginUsername == "" {
					continue // decorative
				}
				if a.Needs == nil {
					a.Needs = make(map[NeedKey]int)
				}
				// LLM-135: a sleeping body still gets hungry and thirsty (those
				// needs accrue below so the actor wakes wanting a meal), but
				// tiredness is excluded — the sleep loop is recovering it, and
				// accruing it here would fight that. The climbing need is not
				// surfaced: actorActionableRedNeed returns nothing while asleep,
				// so the warrant block below is a no-op for a sleeper.
				sleeping := a.SleepingUntil != nil && a.SleepingUntil.After(now)
				// LLM-211: an actor on a break recovers tiredness via the same
				// mode-blind sweep, so hold its tiredness accrual too — otherwise
				// the +1/hr increment fights the break's recovery. Hunger/thirst
				// still accrue (like sleep) so it wakes appropriately hungry; only
				// tiredness is held. actorActionableRedNeed likewise returns nothing
				// while on break, so the warrant block below is a no-op for a rester.
				onBreak := a.BreakUntil != nil && a.BreakUntil.After(now)

				// ZBBS-WORK-277 — #1 need-threshold producer. Only agent-backed
				// NPCs warrant: PCs accrue needs but don't reactor-tick, and
				// transient visitors run their own ExpiresAt lifecycle. The
				// WarrantedSince/TickInFlight gate leaves an actor already pending
				// or mid-tick alone (that tick's perception already sees the need)
				// — decision A, 2026-05-22. One-warrant-per-actor-per-tick comes
				// from the `redFound` flag below: only the first red need in the
				// loop is recorded, and a single stamp happens after all the
				// need increments are applied.
				warrantEligible := (a.Kind == KindNPCStateful || a.Kind == KindNPCShared) &&
					a.VisitorState == nil && a.WarrantedSince == nil && !a.TickInFlight

				for _, n := range Needs {
					if (sleeping || onBreak) && n.Key == "tiredness" {
						continue // recovered by the sleep/break loop, not accrued here
					}
					// ZBBS-WORK-346 — port of v1 ZBBS-HOME-214: skip this need
					// for this actor if a fresh dwell-credit exists on the
					// matching attribute. Closes the "rest under a tree but
					// stat still climbs" wash on slow recoverers. A skipped
					// need also can't cross its red threshold this tick — the
					// v1 SQL UPDATE skipped the row entirely, same effect (and
					// actorActionableRedNeed below applies the same skip, so a
					// dwell-credited red need is not warranted either).
					if hasFreshDwellCreditForAttribute(a, n.Key, now, NeedsTickDwellSkipWindow) {
						continue
					}
					a.Needs[n.Key] = ClampNeed(a.Needs[n.Key] + bump)
				}

				// LEVEL check (ZBBS-HOME-329 #1): warrant whenever a need sits
				// AT OR PAST its red threshold after the increment — not only on
				// the upward crossing. The old edge test fired once on the way
				// up and went silent; a need pegged at max never re-crosses, so
				// a stuck-maxed actor lost all need-driven goal pressure and
				// could never recover organically. Re-stamping each tick while
				// the need stays red restores the standing "go resolve this"
				// goal. The warrantEligible gate (no open cycle, not mid-tick)
				// bounds this to the hourly needs cadence and prevents re-stamp
				// spam; the faster red-need backstop sweep (ZBBS-HOME-363) is
				// what re-engages a red-need actor between hourly ticks. The
				// shared actorActionableRedNeed picks the first red, on-shift
				// (tiredness off-shift is the sleep loop's job), non-dwell need.
				if warrantEligible {
					if redNeed, ok := actorActionableRedNeed(w, a, now, nowMinute); ok {
						// Zero-sourced (a stat at its red line is not a discrete
						// event) — SourceEventID stays 0, like the idle-backstop
						// producer; perception surfaces the full need set, so the
						// deliberation resolves whatever is most pressing.
						tryStampWarrant(w, a, WarrantMeta{
							TriggerActorID: a.ID,
							Reason:         NeedThresholdWarrantReason{Need: redNeed},
						}, now)
					}
				}
				touched++
			}
			w.Environment.LastNeedsTickAt = now.Truncate(time.Hour)
			return touched, nil
		},
	}
}

// NeedsTickerInterval is how often RunNeedsTicker wakes to check for hour
// boundaries. One minute matches legacy cadence — finer than necessary
// (the actual tick is hourly) but cheap and survives clock skew.
const NeedsTickerInterval = time.Minute

// RunNeedsTicker owns the needs-tick goroutine. Wakes every
// NeedsTickerInterval, computes the hour boundary, and submits an
// IncrementNeedsTick command when the boundary has rolled past the last
// processed tick.
//
// Catch-up: if many hours have elapsed since the last tick (downtime,
// fresh process), the increment is capped at MaxNeedsCatchupHours.
//
// Fresh-run no-pulse: if LastNeedsTickAt is zero, the ticker stamps the
// current hour boundary without incrementing — avoids a deploy-time
// pulse where every villager gets +N hunger the instant settings load.
func RunNeedsTicker(ctx context.Context, w *World) {
	t := time.NewTicker(NeedsTickerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.beatTicker("needs")
			runNeedsTickIteration(ctx, w)
		}
	}
}

func runNeedsTickIteration(ctx context.Context, w *World) {
	now := time.Now().UTC()
	hourBoundary := now.Truncate(time.Hour)

	// Snapshot the LastNeedsTickAt via a command so we read it consistent
	// with any concurrent commits.
	lastValue, err := w.SendContext(ctx, Command{
		Fn: func(world *World) (any, error) {
			return world.Environment.LastNeedsTickAt, nil
		},
	})
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("sim/needs_tick: snapshot last: %v", err)
		}
		return
	}
	lastAt := lastValue.(time.Time)

	if lastAt.IsZero() {
		// First run after deploy / process boot. Stamp the current hour
		// boundary without incrementing (matches legacy behavior on
		// NULL last_needs_tick_at).
		_, _ = w.SendContext(ctx, Command{
			Fn: func(world *World) (any, error) {
				world.Environment.LastNeedsTickAt = hourBoundary
				return nil, nil
			},
		})
		return
	}

	lastAt = lastAt.UTC().Truncate(time.Hour)
	hoursElapsed := int(hourBoundary.Sub(lastAt) / time.Hour)
	if hoursElapsed <= 0 {
		return
	}

	cappedHours := hoursElapsed
	if cappedHours > MaxNeedsCatchupHours {
		log.Printf("sim/needs_tick: %d hours since last tick exceeds cap (%d) — applying capped catch-up only",
			hoursElapsed, MaxNeedsCatchupHours)
		cappedHours = MaxNeedsCatchupHours
	}

	res, err := w.SendContext(ctx, IncrementNeedsTick(cappedHours))
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("sim/needs_tick: increment failed: %v", err)
		}
		return
	}
	touched := res.(int)
	log.Printf("sim/needs_tick: %d hour(s) elapsed, applying %d capped, touched %d actors",
		hoursElapsed, cappedHours, touched)
}
