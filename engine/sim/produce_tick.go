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
				for _, entry := range actor.RestockPolicy.ProduceEntries() {
					recipe, ok := w.Recipes[entry.Item]
					if !ok {
						continue
					}
					change, executed := applyProduceEntry(actor, entry, recipe, now)
					if executed {
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
// First-observation case stamps the anchor to now without filling
// (mirrors legacy "first observation: stamp anchor, no fill").
func applyProduceEntry(actor *Actor, entry RestockEntry, recipe *ItemRecipe, now time.Time) (produceChange, bool) {
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
			_, err := w.Send(ApplyProduceTick(time.Now().UTC()))
			if err != nil {
				log.Printf("sim/produce_ticker: %v", err)
			}
		}
	}
}
