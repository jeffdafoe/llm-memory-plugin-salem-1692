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

// IncrementNeedsTick returns a Command that applies the hourly needs
// increment across all eligible actors.
//
// Eligibility filter (matches legacy needTickEligibilityPred):
//   - actor must have either LLMAgent or LoginUsername set
//     (decoratives have neither, so they're skipped)
//   - actor must NOT be sleeping (SleepingUntil > now suppresses)
//   - on-break actors STILL tick (vendor on break is awake, just off-shift,
//     and should still get hungry per legacy comment)
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
				if a.SleepingUntil != nil && a.SleepingUntil.After(now) {
					continue // sleeping body's clock pauses
				}
				if a.Needs == nil {
					a.Needs = make(map[NeedKey]int)
				}

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
				var redNeed NeedKey
				redFound := false

				for _, n := range Needs {
					// ZBBS-WORK-346 — port of v1 ZBBS-HOME-214: skip this need
					// for this actor if a fresh dwell-credit exists on the
					// matching attribute. Closes the "rest under a tree but
					// stat still climbs" wash on slow recoverers. A skipped
					// need also can't cross its red threshold this tick — the
					// v1 SQL UPDATE skipped the row entirely, same effect.
					if hasFreshDwellCreditForAttribute(a, n.Key, now, NeedsTickDwellSkipWindow) {
						continue
					}

					before := a.Needs[n.Key]
					after := ClampNeed(before + bump)
					a.Needs[n.Key] = after

					// LEVEL check (ZBBS-HOME-329 #1): warrant whenever the need
					// sits AT OR PAST its red threshold — not only on the upward
					// crossing. The old edge test (before < threshold) fired once
					// on the way up and then went silent; a need pegged at max
					// never re-crosses, so a stuck-maxed actor lost all need-
					// driven goal pressure and could never recover organically.
					// Re-stamping each tick while the need stays red restores the
					// standing "go resolve this" goal. The warrantEligible gate
					// above (no open cycle, not mid-tick) bounds this to the
					// hourly needs cadence and prevents re-stamp spam. Tiredness
					// is excluded off-shift — overnight tiredness is the
					// deterministic sleep loop's job, not an LLM deliberation;
					// on-shift tiredness routes the warrant to take_break.
					if !redFound && warrantEligible {
						threshold := w.Settings.NeedThresholds.Get(n.Key)
						if after >= threshold {
							if n.Key != "tiredness" || isActorOnShift(a, nowMinute) {
								redNeed = n.Key
								redFound = true
							}
						}
					}
				}

				// Stamp once per actor per tick. Zero-sourced (a stat sitting at
				// its red line is not a discrete event) — SourceEventID stays 0,
				// like the idle-backstop producer; the warrant's perception
				// surfaces the full need set, so the deliberation resolves
				// whatever is most pressing, not just redNeed.
				if redFound {
					tryStampWarrant(w, a, WarrantMeta{
						TriggerActorID: a.ID,
						Reason:         NeedThresholdWarrantReason{Need: redNeed},
					}, now)
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
