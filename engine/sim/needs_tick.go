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
// PER-NEED-PER-ACTOR DWELL-CREDIT SKIP (legacy needs_tick carve-out) is
// STUBBED: actor.DwellCredits is in the model but DwellCredit struct is
// still a placeholder until the dwell subsystem ports. When dwell lands,
// the tick adds a "skip this need for this actor if a fresh dwell credit
// exists for the matching effect category" branch — straightforward
// addition once DwellCredit gets its LastCreditedAt field.

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
// The caller supplies how many capped hours of increment to apply — the
// ticker computes this from the elapsed window. Magnitude per hour comes
// from WorldSettings.NeedsTickAmount.
//
// Returns the count of actors touched.
//
// TODO(rewrite): per-need-per-actor dwell-credit skip when dwell subsystem
// ports. Currently the increment applies regardless of dwell state.
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
				for _, n := range Needs {
					a.Needs[n.Key] = ClampNeed(a.Needs[n.Key] + bump)
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
