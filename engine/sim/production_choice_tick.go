package sim

import (
	"context"
	"log"
	"time"
)

// production_choice_tick.go — the production-choice producer (LLM-116,
// generalized to ALL producers in LLM-319). Production is opt-in per batch:
// nothing is made unless the actor calls `produce`, so an idle producer at its
// post — nothing in the works, something craftable — must occasionally be
// GIVEN the decision or the economy starves on model forgetfulness. This
// level-triggered producer wakes such an actor so it sees the "## Your trade"
// scene and decides.
//
// LLM-DECIDED, like the restock/shift producers: it does NOT pick for the
// actor, and it does not even urge production — the warrant line is "your
// thoughts turn to your trade"; the trade cue carries the situation and the
// model draws the conclusion. Declining to produce is a legitimate outcome
// (the whole point of LLM-319's agency), which is why the wake is PACED:
// re-granting the same decision every scan would be the old auto-produce
// wearing a decision costume — a weak model nagged every minute eventually
// complies, every time. ProductionNagAt (stamped here on wake, and by
// landProductionCycle on batch landing, whose completion warrant is itself the
// going-idle wake) holds the re-nag off for ProductionRenagInterval.

// ProductionChoiceWarrantReason is stamped when a producer is idle at its post
// (no ProductionActivity) with at least one good craftable right now and its
// re-nag interval elapsed. Zero-sourced (a standing condition is not an
// event), so DedupDiscriminator returns 0 and the per-actor WarrantedSince gate
// in the producer prevents double-stamp. Mirrors RestockWarrantReason /
// ShiftDutyWarrantReason — the other condition-driven, zero-sourced reasons.
type ProductionChoiceWarrantReason struct{}

func (ProductionChoiceWarrantReason) isWarrantReason()           {}
func (ProductionChoiceWarrantReason) Kind() WarrantKind          { return WarrantKindProductionChoice }
func (ProductionChoiceWarrantReason) DedupDiscriminator() uint64 { return 0 }

// ProductionRenagInterval is how long a production-choice wake (or a landed
// batch, whose completion beat is the same decision moment) holds off the next
// one. The pacing that makes "no" a decision that sticks instead of one
// re-litigated every scan. Guesstimate against cycle durations measured in
// tens of minutes; tune live if producers over- or under-supply.
const ProductionRenagInterval = 30 * time.Minute

// shouldChooseProduction reports whether a producer is standing at its post
// with a production decision worth a tick: it has at least one recipe-backed
// produce entry, is physically inside its work structure, has NOTHING in the
// works (no ProductionActivity), and at least one good is craftable right now
// (makeable, below cap, inputs on hand — LLM-257). When nothing is craftable
// there is nothing to decide, so it is left alone. Pure read; pacing is the
// caller's (EvaluateProductionChoice reads ProductionNagAt).
//
// Since LLM-319 this fires for single- and multi-output producers alike — a
// one-good keeper's "choice" is the go/no-go on another batch, and that
// go/no-go is exactly the agency the redesign grants.
func shouldChooseProduction(a *Actor, w *World) bool {
	if a.RestockPolicy == nil {
		return false
	}
	if a.WorkStructureID == "" || a.InsideStructureID != a.WorkStructureID {
		return false // only at the post
	}
	if a.ProductionActivity != nil {
		return false // a batch is in the works — nothing to decide until it lands
	}
	if ownerStallDegraded(w, a.ID) {
		return false // degraded = shut for refill (LLM-304) — the repair warrant owns this wake
	}
	produce := a.RestockPolicy.ProduceEntries()
	if makeableProduceCount(w, produce) < 1 {
		return false // not a producer
	}
	for _, e := range produce {
		if craftableNow(w, a, e) {
			return true
		}
	}
	return false // nothing makeable right now — don't wake to an impossible choice
}

// EvaluateProductionChoice returns a Command that applies one pass: stamp a
// production-choice warrant on every eligible producer that should choose and
// whose re-nag interval has elapsed. Runs on the world goroutine, so the
// tryStampWarrant calls are serialized. Reuses restockEligible (the same
// agent-backed / not-resting / not-walking / not-already-ticking gate the
// restock producer uses).
func EvaluateProductionChoice(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			stamped := 0
			for _, a := range w.Actors {
				if !restockEligible(a, now) {
					continue
				}
				if !a.ProductionNagAt.IsZero() && now.Sub(a.ProductionNagAt) < ProductionRenagInterval {
					continue // decision recently granted (or a batch just landed) — let it stand
				}
				if !shouldChooseProduction(a, w) {
					continue
				}
				tryStampWarrant(w, a, WarrantMeta{
					TriggerActorID: a.ID,
					Reason:         ProductionChoiceWarrantReason{},
				}, now)
				a.ProductionNagAt = now
				stamped++
			}
			return stamped, nil
		},
	}
}

// ProductionChoiceTickerInterval — once a minute, matching RunProduceTicker /
// RunRestockTicker / RunShiftTicker. The scan is cheap; ProductionRenagInterval
// is what paces the actual wakes.
const ProductionChoiceTickerInterval = time.Minute

// RunProductionChoiceTicker owns the production-choice producer goroutine: once a
// minute, submit an EvaluateProductionChoice. Same time.NewTicker idiom as
// RunRestockTicker. Returns when ctx is cancelled.
func RunProductionChoiceTicker(ctx context.Context, w *World) {
	t := time.NewTicker(ProductionChoiceTickerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.beatTicker("production_choice")
			if _, err := w.SendContext(ctx, EvaluateProductionChoice(time.Now().UTC())); err != nil {
				if ctx.Err() == nil {
					log.Printf("sim/production_choice: tick failed: %v", err)
				}
			}
		}
	}
}
