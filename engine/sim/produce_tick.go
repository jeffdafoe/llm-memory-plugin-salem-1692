package sim

import (
	"context"
	"log"
	"time"
)

// Produce tick (ZBBS-HOME-241) — in-memory port of engine/produce_tick.go.
//
// Per-minute handler that grows actor inventories per their restock
// policy `produce` entries. Mirrors the continuous-mode regen shape of
// object_refresh_tick.go but the "supply" is the actor's own
// inventory and the rate comes from the recipe.
//
// Algorithm per actor per produce entry:
//
//   1. Gate: actor must be inside their WorkStructureID AND not asleep.
//   2. Get or initialize the per-item ProduceState. First observation
//      stamps the anchor to now without filling (matches the legacy
//      first-pass behavior).
//   3. Compute units owed since the anchor (continuous regen math).
//      Skip when sub-unit time has elapsed.
//   4. If recipe has inputs and the actor lacks ANY at the required
//      quantity, skip the whole entry (skip-if-any-input-short, design
//      decision #7). Otherwise cap executions by min input availability.
//   5. Cap executions by inventory headroom (Cap - current_quantity).
//      If already at cap, advance anchor to now (no back-credit when
//      consumption later opens headroom).
//   6. Consume inputs, mint output, advance anchor by exact
//      unit-second multiples so sub-unit residue carries forward.
//
// HUB BROADCAST STUB. Legacy emits actor_inventory_changed via the
// Hub. Until the Hub port lands, the result carries the per-actor
// inventory-change records so callers/tests can observe.

// ProduceState carries the per-item production anchor on an Actor.
type ProduceState struct {
	Item           ItemKind
	LastProducedAt time.Time
}

// DefaultLaborProduceBoostPct is the per-worker production boost (LLM-224):
// each hired worker laboring AT the keeper's establishment adds this percent
// of the keeper's own base rate to the produce tick, so a wage buys real
// output instead of pure flavor (one helper at 50 → 1.5x, two → 2x). A
// non-positive WorldSettings.LaborProduceBoostPct disables the boost (the
// per-feature off-switch, mirroring FarmUpkeepCoinsPerShovel==0). Guesstimate,
// tuned live via the umbilical (settings/labor-produce-boost).
const DefaultLaborProduceBoostPct = 50

// laboringHelperCount counts the workers currently on an accepted job for
// employerID who are physically at the employer's work post — Working
// LaborLedger offers whose worker is at workStructureID (actorAtWorkpost: inside
// a building, or at the staff pin of a doorless stall). Ledger-authoritative
// like workerHasLiveJob (a Working row counts until the sweep settles it,
// regardless of its clock), and location-gated because the boost models
// hands-on help: a deal struck elsewhere speeds nothing until the worker is at
// the establishment. An EnRoute worker (relocating, not yet working) is skipped
// by the state check and starts counting only once the arrival subscriber flips
// them to Working at the post (LLM-229, LLM-224).
func laboringHelperCount(w *World, employerID ActorID, workStructureID StructureID) int {
	if workStructureID == "" {
		return 0
	}
	n := 0
	for _, o := range w.LaborLedger {
		if o == nil || o.State != LaborStateWorking || o.EmployerID != employerID {
			continue
		}
		worker := w.Actors[o.WorkerID]
		if worker == nil || !actorAtWorkpost(w, worker, workStructureID) {
			continue
		}
		n++
	}
	return n
}

// produceRateScalePct resolves the keeper's production-rate scale for this
// tick: 100 (base rate) plus LaborProduceBoostPct per laboring helper at the
// establishment. Sampled once per keeper per tick — the 1-min cadence bounds
// how stale a mid-window hire/finish can read.
func produceRateScalePct(w *World, employerID ActorID, employer *Actor) int {
	boostPct := w.Settings.LaborProduceBoostPct
	if boostPct <= 0 {
		return 100
	}
	helpers := laboringHelperCount(w, employerID, employer.WorkStructureID)
	return 100 + helpers*boostPct
}

// ProduceEvent records one ACTUAL production execution (a real mint, NOT an
// at-cap anchor advance) for the recent-production readout the forge-choice cue
// shows a multi-output crafter (LLM-116). Restart-lossy — a transient decision-
// support signal, never checkpointed (same posture as the price book).
type ProduceEvent struct {
	Item ItemKind
	Qty  int
	At   time.Time
}

// RecentProduceCapacity bounds the per-actor recent-production ring. 32 covers a
// busy smith's recent run; the windowed count the cue shows (restockSalesWindow)
// reads only what fits, like the price book's 20-deep ring.
const RecentProduceCapacity = 32

// recordRecentProduce appends one real mint to the actor's RecentProduce ring,
// dropping the oldest over capacity. Called ONLY when produce_tick actually
// minted (qty > 0), so an at-cap anchor advance never pollutes the "recently
// made" signal.
func recordRecentProduce(a *Actor, item ItemKind, qty int, now time.Time) {
	if qty <= 0 {
		return
	}
	a.RecentProduce = append(a.RecentProduce, ProduceEvent{Item: item, Qty: qty, At: now})
	if len(a.RecentProduce) > RecentProduceCapacity {
		a.RecentProduce = a.RecentProduce[len(a.RecentProduce)-RecentProduceCapacity:]
	}
}

// ProduceTickInventoryChange is a per-item inventory delta produced by
// ApplyProduceTick. The Hub layer (when ported) translates these to
// actor_inventory_changed broadcasts; today they surface via the
// command result.
type ProduceTickInventoryChange struct {
	ActorID       ActorID
	Item          ItemKind
	QuantityAdded int
	NewQuantity   int
}

// ProduceTickResult is the command-reply payload — number of executions
// fired plus the per-item changes.
type ProduceTickResult struct {
	Executions int
	Changes    []ProduceTickInventoryChange
}

// makeableRecipe reports whether item has a recipe that can actually produce —
// it exists with a positive rate. The single definition of "makeable" shared by
// produce_tick (the multi-output count), SetProductionFocus (the craft gate), and
// shouldChooseProduction (the wake gate), so all three agree on the choice set.
// (It does NOT require recipe inputs — an origin producer like nail makes its good
// from an empty inputs list and is fully makeable.)
func makeableRecipe(w *World, item ItemKind) bool {
	r, ok := w.Recipes[item]
	return ok && r != nil && r.RateQty > 0 && r.RatePerHours > 0
}

// makeableProduceCount counts how many of the produce entries are makeable
// (recipe-backed with positive rate).
func makeableProduceCount(w *World, entries []RestockEntry) int {
	n := 0
	for _, e := range entries {
		if makeableRecipe(w, e.Item) {
			n++
		}
	}
	return n
}

// HasProduceInputs reports whether inventory holds enough of every required
// input to run at least one execution of recipe. A recipe with no inputs (an
// origin producer like nail or water) is trivially satisfied. Exported so the
// perception forge-choice cue applies the SAME inputs test applyProduceEntry's
// input clamp (below) uses, keeping the cue and the tick in lockstep on "can
// this be made right now" — the inputs axis makeableRecipe deliberately omits
// (LLM-257).
func HasProduceInputs(recipe *ItemRecipe, inventory map[ItemKind]int) bool {
	if recipe == nil {
		return false
	}
	for _, in := range recipe.Inputs {
		if in.Qty <= 0 {
			continue
		}
		if inventory[in.Item] < in.Qty {
			return false
		}
	}
	return true
}

// craftableNow reports whether the actor could actually produce entry.Item on
// the next tick: it is makeableRecipe (recipe with positive rate), still below
// its carry cap, AND the actor holds the inputs for at least one execution. The
// inputs-aware superset of makeableRecipe-plus-below-cap — the shared
// choice-worthiness gate that keeps the production-choice warrant
// (shouldChooseProduction) and the craft tool (SetProductionFocus) from steering
// a keeper onto a good it cannot presently make, and that the forge cue mirrors
// via HasProduceInputs (LLM-257). Time/anchor is NOT considered — that is
// produce_tick's rate pacing, not a "should I pick this" test.
func craftableNow(w *World, a *Actor, entry RestockEntry) bool {
	if !makeableRecipe(w, entry.Item) {
		return false
	}
	if cap := entry.Cap(); cap > 0 && a.Inventory[entry.Item] >= cap {
		return false
	}
	return HasProduceInputs(w.Recipes[entry.Item], a.Inventory)
}

// ApplyProduceTick walks every actor with a produce-source restock
// entry and applies units owed.
//
// Recipes are looked up from World.Recipes (loaded once at startup).
// Missing recipes are silent skips. Errors per-entry log and continue;
// they don't roll back other entries on the same actor (the in-memory
// model doesn't have transactional aggregation across entries — each
// entry is independent state).
func ApplyProduceTick(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			res := ProduceTickResult{}
			for actorID, actor := range w.Actors {
				if !produceTickGate(actor, now) {
					continue
				}
				if actor.RestockPolicy == nil {
					continue
				}
				if actor.ProduceState == nil {
					actor.ProduceState = make(map[ItemKind]*ProduceState)
				}
				if actor.Inventory == nil {
					actor.Inventory = make(map[ItemKind]int)
				}
				produceEntries := actor.RestockPolicy.ProduceEntries()
				// A multi-output crafter (e.g. the smith: skillet + nail) forges
				// only its chosen ProductionFocus, not every entry in parallel
				// (LLM-116). An empty focus → it produces nothing until it picks
				// one at its forge. A single-output producer ignores focus and
				// keeps auto-producing its one good. "Multi-output" counts only
				// MAKEABLE (recipe-backed) entries — matching the forge cue and the
				// production-choice producer — so a no-recipe entry never makes an
				// actor a chooser (which would otherwise stall it on a focus it
				// can't produce).
				multiOutput := makeableProduceCount(w, produceEntries) > 1
				// LLM-224: hired help speeds the keeper's whole produce tick —
				// each worker laboring at the establishment scales the rate.
				rateScalePct := produceRateScalePct(w, actorID, actor)
				for _, entry := range produceEntries {
					if multiOutput && actor.ProductionFocus != entry.Item {
						continue
					}
					recipe, ok := w.Recipes[entry.Item]
					if !ok {
						continue
					}
					change, executed := applyProduceEntry(actor, entry, recipe, now, rateScalePct)
					if executed {
						recordRecentProduce(actor, change.Item, change.Added, now)
						res.Executions++
						res.Changes = append(res.Changes, ProduceTickInventoryChange{
							ActorID:       actorID,
							Item:          change.Item,
							QuantityAdded: change.Added,
							NewQuantity:   change.NewQty,
						})
					}
				}
			}
			return res, nil
		},
	}
}

// produceTickGate is the gate test: actor must be inside their work
// structure AND not currently sleeping. Matches legacy gate exactly.
func produceTickGate(actor *Actor, now time.Time) bool {
	if actor.WorkStructureID == "" {
		return false
	}
	if actor.InsideStructureID != actor.WorkStructureID {
		return false
	}
	if actor.SleepingUntil != nil && now.Before(*actor.SleepingUntil) {
		return false
	}
	return true
}

type produceChange struct {
	Item   ItemKind
	Added  int
	NewQty int
}

// applyProduceEntry runs one entry on one actor. Returns the change
// (when something was produced) and whether an execution fired.
//
// rateScalePct scales the recipe rate (100 = base; 150 = one boost-50 helper,
// LLM-224) by shrinking secondsPerUnit. The boosted value is used for BOTH the
// units-owed division and the anchor advance, so sub-unit residue stays
// consistent within the tick; a scale change between ticks mis-values at most
// one unit's residue (acceptable at the 1-min cadence).
//
// First-observation case stamps the anchor to now without filling
// (mirrors legacy "first observation: stamp anchor, no fill").
func applyProduceEntry(actor *Actor, entry RestockEntry, recipe *ItemRecipe, now time.Time, rateScalePct int) (produceChange, bool) {
	state, ok := actor.ProduceState[entry.Item]
	if !ok {
		actor.ProduceState[entry.Item] = &ProduceState{
			Item:           entry.Item,
			LastProducedAt: now,
		}
		return produceChange{}, false
	}

	if recipe.RateQty <= 0 || recipe.RatePerHours <= 0 {
		return produceChange{}, false
	}
	periodSeconds := int64(recipe.RatePerHours) * 3600
	secondsPerUnit := periodSeconds / int64(recipe.RateQty)
	if secondsPerUnit <= 0 {
		return produceChange{}, false
	}
	if rateScalePct > 100 {
		secondsPerUnit = secondsPerUnit * 100 / int64(rateScalePct)
		if secondsPerUnit < 1 {
			secondsPerUnit = 1
		}
	}
	elapsedSeconds := int64(now.Sub(state.LastProducedAt).Seconds())
	if elapsedSeconds < secondsPerUnit {
		return produceChange{}, false
	}
	unitsOwed := elapsedSeconds / secondsPerUnit
	if unitsOwed <= 0 {
		return produceChange{}, false
	}

	currentQty := actor.Inventory[entry.Item]
	cap := entry.Cap()

	if cap > 0 && currentQty >= cap {
		// Already at cap — advance anchor to now to avoid back-credit
		// when consumption later opens headroom.
		state.LastProducedAt = now
		return produceChange{}, false
	}

	// Convert unitsOwed into executions. One execution mints output_qty
	// units; when output_qty <= 1 the conversion is identity.
	executionsOwedByTime := unitsOwed
	if recipe.OutputQty > 1 {
		executionsOwedByTime = unitsOwed / int64(recipe.OutputQty)
	}
	if executionsOwedByTime <= 0 {
		return produceChange{}, false
	}

	headroom := int64(0)
	if cap > 0 {
		headroom = int64(cap - currentQty)
	} else {
		// Defensive bound — no cap configured, allow at most one
		// execution per tick to avoid runaway accumulation.
		headroom = int64(recipe.OutputQty)
	}
	executionsByCap := headroom
	if recipe.OutputQty > 1 {
		executionsByCap = headroom / int64(recipe.OutputQty)
	}

	executions := executionsOwedByTime
	if executionsByCap < executions {
		executions = executionsByCap
	}
	if executions <= 0 {
		return produceChange{}, false
	}

	// Input check + clamp. Skip-if-any-input-short: if any input is
	// short for one execution, the entry skips without advancing the
	// anchor (next tick re-checks once inputs arrive).
	if len(recipe.Inputs) > 0 {
		for _, in := range recipe.Inputs {
			if in.Qty <= 0 {
				continue
			}
			have := actor.Inventory[in.Item]
			canExecute := int64(have / in.Qty)
			if canExecute < executions {
				executions = canExecute
			}
		}
		if executions <= 0 {
			return produceChange{}, false
		}
		// Consume inputs in-place.
		for _, in := range recipe.Inputs {
			if in.Qty <= 0 {
				continue
			}
			consume := in.Qty * int(executions)
			actor.Inventory[in.Item] -= consume
			if actor.Inventory[in.Item] <= 0 {
				delete(actor.Inventory, in.Item)
			}
		}
	}

	// Mint the output.
	totalProduced := int(executions) * recipe.OutputQty
	actor.Inventory[entry.Item] += totalProduced

	// Optional booster step (LLM-248). Purely post-mint: boosters never gate
	// executions (holding none leaves base production untouched) and never
	// touch the anchor math — the elective mirror of the required-inputs
	// clamp above. Per booster, each execution that can pay Qty consumes it
	// and mints BonusQty extra output, clamped to remaining cap headroom so
	// a boosted batch can't overshoot the entry's carry cap.
	for _, bi := range recipe.BoostInputs {
		if bi.Qty <= 0 || bi.BonusQty <= 0 {
			continue
		}
		have := actor.Inventory[bi.Item]
		boostedExecs := int64(have / bi.Qty)
		if boostedExecs > executions {
			boostedExecs = executions
		}
		if boostedExecs <= 0 {
			continue
		}
		bonus := int(boostedExecs) * bi.BonusQty
		if cap > 0 {
			if room := cap - actor.Inventory[entry.Item]; bonus > room {
				bonus = room
			}
		}
		if bonus <= 0 {
			continue
		}
		// Consume the booster in full for the executions it backed — the
		// bonus clamp above trims yield, not cost (the herb went into the
		// batch either way).
		consume := bi.Qty * int(boostedExecs)
		actor.Inventory[bi.Item] -= consume
		if actor.Inventory[bi.Item] <= 0 {
			delete(actor.Inventory, bi.Item)
		}
		totalProduced += bonus
		actor.Inventory[entry.Item] += bonus
	}

	// Advance anchor by exactly the consumed time so sub-unit residue
	// carries forward to the next tick.
	advanceUnits := executions
	if recipe.OutputQty > 1 {
		advanceUnits = int64(recipe.OutputQty) * executions
	}
	advanceSeconds := advanceUnits * secondsPerUnit
	state.LastProducedAt = state.LastProducedAt.Add(time.Duration(advanceSeconds) * time.Second)

	return produceChange{
		Item:   entry.Item,
		Added:  totalProduced,
		NewQty: actor.Inventory[entry.Item],
	}, true
}

// ProduceTickerInterval is how often RunProduceTicker wakes. Matches
// legacy 1-min cadence.
const ProduceTickerInterval = time.Minute

// RunProduceTicker owns the produce-tick goroutine. Wakes every
// ProduceTickerInterval and submits ApplyProduceTick.
func RunProduceTicker(ctx context.Context, w *World) {
	t := time.NewTicker(ProduceTickerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.beatTicker("produce")
			_, err := w.SendContext(ctx, ApplyProduceTick(time.Now().UTC()))
			if err != nil && ctx.Err() == nil {
				log.Printf("sim/produce_ticker: %v", err)
			}
		}
	}
}
