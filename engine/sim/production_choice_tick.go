package sim

import (
	"context"
	"log"
	"time"
)

// production_choice_tick.go — tick-driver producer (LLM-116): the production-
// choice producer. A multi-output crafter (the smith: skillet + nail) no longer
// auto-produces every good in parallel; produce_tick fills only its chosen
// ProductionFocus. That makes production depend on the crafter ticking to PICK —
// but once it is already at its post, no other producer wakes it (shift-duty only
// drives an actor TO work, not while there; needs/restock/social fire only on
// their own stimulus). Without this producer an idle smith — needs met, no
// customers — would sit unfocused and forge nothing, a regression from the old
// silent auto-produce. This level-triggered producer wakes such a crafter so it
// sees the "## Time to craft" cue and chooses via the craft tool.
//
// LLM-DECIDED, like the restock/shift producers: it does NOT pick for the actor.
// It stamps a WarrantKindProductionChoice warrant (high-information, so it bypasses
// the noop-skip gate and forces the tick); the forge cue carries the options and
// the model calls craft. LEVEL-TRIGGERED: each scan re-checks the standing
// condition, so the warrant keeps re-firing until the crafter actually picks a
// productive focus (or every good is at cap, at which point there's nothing to
// make and it stops).

// ProductionChoiceWarrantReason is stamped when a multi-output crafter is idle at
// its forge with a choice to make (unfocused, or its focus has hit cap) and at
// least one good still below cap. Zero-sourced (a standing condition is not an
// event), so DedupDiscriminator returns 0 and the per-actor WarrantedSince gate in
// the producer prevents double-stamp. Mirrors RestockWarrantReason /
// ShiftDutyWarrantReason — the other condition-driven, zero-sourced reasons.
type ProductionChoiceWarrantReason struct{}

func (ProductionChoiceWarrantReason) isWarrantReason()           {}
func (ProductionChoiceWarrantReason) Kind() WarrantKind          { return WarrantKindProductionChoice }
func (ProductionChoiceWarrantReason) DedupDiscriminator() uint64 { return 0 }

// shouldChooseProduction reports whether a multi-output crafter is standing at its
// forge with a production choice worth a tick: it has more than one recipe-backed
// produce entry, is physically inside its work structure, at least one good is
// still below cap (something IS makeable), and either it has no focus or its
// current focus is no longer productive (the focused good is at cap). When every
// good is at cap there is nothing to make, so it is left alone. Pure read.
func shouldChooseProduction(a *Actor, w *World) bool {
	if a.RestockPolicy == nil {
		return false
	}
	if a.WorkStructureID == "" || a.InsideStructureID != a.WorkStructureID {
		return false // only at the forge
	}
	produce := a.RestockPolicy.ProduceEntries()
	if makeableProduceCount(w, produce) <= 1 {
		return false // 0-or-1 makeable goods — no choice to make (matches produce_tick)
	}
	anyBelowCap := false
	focusProductive := false
	for _, e := range produce {
		if !makeableRecipe(w, e.Item) {
			continue // not makeable — skip
		}
		cap := e.Cap()
		belowCap := cap <= 0 || a.Inventory[e.Item] < cap
		if !belowCap {
			continue
		}
		anyBelowCap = true
		if a.ProductionFocus == e.Item {
			focusProductive = true
		}
	}
	if !anyBelowCap {
		return false // everything maxed — nothing to forge, don't wake
	}
	// Wake when unfocused, or when the current focus can no longer be made (at
	// cap / no recipe) so the crafter must pick something else.
	return a.ProductionFocus == "" || !focusProductive
}

// EvaluateProductionChoice returns a Command that applies one pass: stamp a
// production-choice warrant on every eligible crafter that should choose. Runs on
// the world goroutine, so the tryStampWarrant calls are serialized. Reuses
// restockEligible (the same agent-backed / not-resting / not-walking / not-already-
// ticking gate the restock producer uses).
func EvaluateProductionChoice(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			stamped := 0
			for _, a := range w.Actors {
				if !restockEligible(a, now) {
					continue
				}
				if !shouldChooseProduction(a, w) {
					continue
				}
				tryStampWarrant(w, a, WarrantMeta{
					TriggerActorID: a.ID,
					Reason:         ProductionChoiceWarrantReason{},
				}, now)
				stamped++
			}
			return stamped, nil
		},
	}
}

// ProductionChoiceTickerInterval — once a minute, matching RunProduceTicker /
// RunRestockTicker / RunShiftTicker.
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
