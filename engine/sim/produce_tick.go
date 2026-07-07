package sim

import (
	"context"
	"fmt"
	"log"
	"time"
)

// Produce tick (ZBBS-HOME-241, redesigned in LLM-319) — the production-cycle
// progress and landing resolver.
//
// Production is opt-in per batch: nothing is made unless the actor calls the
// `produce` tool, which validates, consumes the recipe inputs, and opens the
// actor's single ProductionActivity window (StartProductionCycle). This tick
// advances that window and lands the batch:
//
//   1. Anchor: every tick advances LastProgressAt, whether or not progress
//      accrues — so time spent away from the post is discarded, not credited
//      on return. A zero anchor (fresh start, or a restart reload) stamps
//      without crediting, the legacy first-observation posture.
//   2. Gate: progress accrues only while the actor is inside its work
//      structure AND awake (produceTickGate), and not degraded-shut
//      (LLM-304). A failed gate pauses the batch; it never cancels.
//   3. Credit: elapsed seconds scaled by the labor boost (LLM-224),
//      re-sampled per tick so a helper hired mid-batch speeds the remainder.
//   4. Landing: at RemainingSeconds <= 0 the batch mints (BatchQty plus any
//      booster bonus, LLM-248), the window clears, and
//      ProductionCycleCompleted fires — the completion beat that wakes the
//      actor to decide whether to make more.
//
// The old continuous auto-fill (per-item ProduceState anchors, units-owed
// regen math) is retired: it silently converted a keeper's coins into stock
// with no decision — the Hannah Boggs broke-keeper death spiral LLM-319
// exists to stop.

// ProductionActivity is an actor's single in-flight production cycle. Opened
// by StartProductionCycle (inputs already consumed), advanced and landed by
// ApplyProduceTick. Value struct (no nested pointers) so CloneActor copies it
// shallowly.
//
// Item/BatchQty/RemainingSeconds are CHECKPOINTED (a cycle runs tens of
// minutes and its inputs are already spent — losing it on restart would eat
// the inputs). LastProgressAt is deliberately NOT: a reload leaves it zero, so
// the first post-restart tick stamps the anchor without crediting and the
// downtime never counts as work.
type ProductionActivity struct {
	Item             ItemKind
	BatchQty         int   // units the cycle mints at landing (recipe OutputQty captured at start)
	RemainingSeconds int64 // work left at BASE rate; the labor boost shrinks it faster than wall time
	LastProgressAt   time.Time
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

// ProduceEvent records one LANDED production batch for the recent-production
// readout the trade cue shows a producer (LLM-116). Restart-lossy — a
// transient decision-support signal, never checkpointed (same posture as the
// price book).
type ProduceEvent struct {
	Item ItemKind
	Qty  int
	At   time.Time
}

// RecentProduceCapacity bounds the per-actor recent-production ring. 32 covers a
// busy smith's recent run; the windowed count the cue shows (restockSalesWindow)
// reads only what fits, like the price book's 20-deep ring.
const RecentProduceCapacity = 32

// recordRecentProduce appends one landed batch to the actor's RecentProduce
// ring, dropping the oldest over capacity.
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
// ApplyProduceTick — one landed batch. The Hub layer (when ported) translates
// these to actor_inventory_changed broadcasts; today they surface via the
// command result.
type ProduceTickInventoryChange struct {
	ActorID       ActorID
	Item          ItemKind
	QuantityAdded int
	NewQuantity   int
}

// ProduceTickResult is the command-reply payload — number of batches landed
// plus the per-item changes.
type ProduceTickResult struct {
	Executions int
	Changes    []ProduceTickInventoryChange
}

// makeableRecipe reports whether item has a recipe that can actually produce —
// it exists with a positive rate. The single definition of "makeable" shared by
// StartProductionCycle (the produce gate), shouldChooseProduction (the wake
// gate), and the trade cue, so all three agree on the choice set.
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
// input to run one production cycle of recipe. A recipe with no inputs (an
// origin producer like nail or water) is trivially satisfied. Exported so the
// perception trade cue applies the SAME inputs test StartProductionCycle's
// start-time consumption uses, keeping the cue and the tool in lockstep on
// "can this be made right now" — the inputs axis makeableRecipe deliberately
// omits (LLM-257).
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

// craftableNow reports whether the actor could actually start a cycle of
// entry.Item right now: it is makeableRecipe (recipe with positive rate), still
// below its carry cap, AND the actor holds the inputs for one cycle. The
// shared choice-worthiness gate that keeps the production-choice warrant
// (shouldChooseProduction) and the produce tool (StartProductionCycle) from
// steering a keeper onto a good it cannot presently make, and that the trade
// cue mirrors via HasProduceInputs (LLM-257).
func craftableNow(w *World, a *Actor, entry RestockEntry) bool {
	if !makeableRecipe(w, entry.Item) {
		return false
	}
	if cap := entry.Cap(); cap > 0 && a.Inventory[entry.Item] >= cap {
		return false
	}
	return HasProduceInputs(w.Recipes[entry.Item], a.Inventory)
}

// CycleDurationSeconds is the base-rate work one production cycle of recipe
// takes: OutputQty units at secondsPerUnit (rate_per_hours×3600 / rate_qty).
// Porridge (10 per cycle, 8/h) → 10 × 450s = 4500s ≈ 75 min. Returns 0 for a
// nil or rate-less recipe (not makeable — the caller gates on makeableRecipe).
func CycleDurationSeconds(recipe *ItemRecipe) int64 {
	if recipe == nil || recipe.RateQty <= 0 || recipe.RatePerHours <= 0 {
		return 0
	}
	secondsPerUnit := int64(recipe.RatePerHours) * 3600 / int64(recipe.RateQty)
	if secondsPerUnit <= 0 {
		return 0
	}
	units := int64(1)
	if recipe.OutputQty > 1 {
		units = int64(recipe.OutputQty)
	}
	return units * secondsPerUnit
}

// ProductionCycleStarted / ProductionCycleCompleted are the lifecycle seams of
// a production cycle (LLM-319), mirroring SourceActivityStarted/Completed.
// Completed drives the NPC completion-beat warrant (production_cycle_reactor
// on the handlers side) and is the PC-HUD surfacing seam.
type ProductionCycleStarted struct {
	EventBase
	ActorID         ActorID
	Item            ItemKind
	BatchQty        int
	DurationSeconds int64
	At              time.Time
}

func (ProductionCycleStarted) isSimEvent() {}

// ProductionCycleCompleted carries the landed yield (batch plus booster
// bonus), self-contained so a subscriber can narrate without re-reading world
// state — the SourceActivityCompleted posture.
type ProductionCycleCompleted struct {
	EventBase
	ActorID ActorID
	Item    ItemKind
	Qty     int
	At      time.Time
}

func (ProductionCycleCompleted) isSimEvent() {}

// ProductionDoneWarrantReason captures the NPC completion beat for a landed
// production cycle (LLM-319): the batch is in the stores and the actor should
// get a tick to see it — and, via the idle trade cue now visible, decide
// whether to start another. Minted by the handlers-side ProductionCycleCompleted
// subscriber with the narration pre-rendered (the SourceActivityCompleted
// posture). DedupDiscriminator 0 — each completion is 1:1 with its landing
// sweep, nothing to dedup.
type ProductionDoneWarrantReason struct {
	Item          ItemKind
	Qty           int
	NarrationText string
}

func (ProductionDoneWarrantReason) isWarrantReason()           {}
func (ProductionDoneWarrantReason) Kind() WarrantKind          { return WarrantKindProductionDone }
func (ProductionDoneWarrantReason) DedupDiscriminator() uint64 { return 0 }

// ProductionCompletionNarration is the felt-language self-perception line for
// a landed batch — the production sibling of SourceActivityCompletionNarration.
// noun is the catalog plural display phrase (producePluralNoun).
func ProductionCompletionNarration(noun string, qty int) string {
	if qty <= 0 || noun == "" {
		return ""
	}
	return fmt.Sprintf("You finish the batch — %d %s ready in your stores.", qty, noun)
}

// ApplyProduceTick advances every in-flight production cycle and lands the due
// ones. Two passes: progress (pure per-actor field writes, safe while ranging
// w.Actors) then landing (landProductionCycle emits ProductionCycleCompleted,
// whose subscribers run inline and may touch w.Actors — the
// completeDueSourceActivities posture).
func ApplyProduceTick(now time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			res := ProduceTickResult{}
			var due []ActorID
			for actorID, actor := range w.Actors {
				act := actor.ProductionActivity
				if act == nil {
					continue
				}
				// Advance the anchor unconditionally: elapsed is credited only
				// when the gate passes NOW, so time away from the post (or a
				// restart's zero anchor) is discarded, never banked. The 1-min
				// cadence bounds how much within-window absence can misread.
				elapsed := int64(now.Sub(act.LastProgressAt).Seconds())
				first := act.LastProgressAt.IsZero()
				act.LastProgressAt = now
				if first || elapsed <= 0 {
					continue
				}
				if !produceTickGate(actor, now) {
					continue
				}
				// LLM-304: a degraded business is shut for production — the batch
				// pauses until the owner mends it. Same skip as off-post/sleeping.
				if ownerStallDegraded(w, actorID) {
					continue
				}
				// LLM-224: hired help speeds the batch — each worker laboring at
				// the establishment scales this tick's credit. Re-sampled per tick
				// so a helper hired mid-batch speeds the remainder.
				credit := elapsed
				if scale := produceRateScalePct(w, actorID, actor); scale > 100 {
					credit = elapsed * int64(scale) / 100
				}
				act.RemainingSeconds -= credit
				if act.RemainingSeconds <= 0 {
					due = append(due, actorID)
				}
			}
			for _, id := range due {
				actor := w.Actors[id]
				if actor == nil || actor.ProductionActivity == nil {
					continue
				}
				change := landProductionCycle(w, id, actor, actor.ProductionActivity, now)
				res.Executions++
				res.Changes = append(res.Changes, ProduceTickInventoryChange{
					ActorID:       id,
					Item:          change.Item,
					QuantityAdded: change.Added,
					NewQuantity:   change.NewQty,
				})
			}
			return res, nil
		},
	}
}

// produceTickGate is the progress gate: actor must be inside their work
// structure AND not currently sleeping. Matches the legacy auto-produce gate,
// now meaning "the batch advances" rather than "goods mint silently".
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

// landProductionCycle mints a finished batch: BatchQty units (captured at
// start, so a mid-flight recipe change can't rewrite what the consumed inputs
// bought) plus any booster bonus, then clears the window and emits the
// completion. ProductionNagAt is stamped so the production-choice scan doesn't
// re-nag on top of the completion beat — the completion warrant IS the wake to
// decide about the next batch.
//
// Boosters (LLM-248) are evaluated here, at landing — the same instant the old
// continuous tick consumed them (mint time). Per booster: holding Qty consumes
// it and adds BonusQty, clamped to remaining cap headroom; a fully clamped
// bonus skips consumption, a partially clamped one still consumes in full (the
// clamp trims yield, not cost — the herb went into the batch either way).
func landProductionCycle(w *World, actorID ActorID, actor *Actor, act *ProductionActivity, now time.Time) produceChange {
	if actor.Inventory == nil {
		actor.Inventory = make(map[ItemKind]int)
	}
	total := act.BatchQty
	if recipe := w.Recipes[act.Item]; recipe != nil {
		cap := 0
		if entry, ok := produceEntry(actor, act.Item); ok {
			cap = entry.Cap()
		}
		for _, bi := range recipe.BoostInputs {
			if bi.Qty <= 0 || bi.BonusQty <= 0 {
				continue
			}
			if actor.Inventory[bi.Item] < bi.Qty {
				continue
			}
			bonus := bi.BonusQty
			if cap > 0 {
				if room := cap - (actor.Inventory[act.Item] + total); bonus > room {
					bonus = room
				}
			}
			if bonus <= 0 {
				continue
			}
			actor.Inventory[bi.Item] -= bi.Qty
			if actor.Inventory[bi.Item] <= 0 {
				delete(actor.Inventory, bi.Item)
			}
			total += bonus
		}
	}
	actor.Inventory[act.Item] += total
	actor.ProductionActivity = nil
	actor.ProductionNagAt = now
	recordRecentProduce(actor, act.Item, total, now)
	w.emit(&ProductionCycleCompleted{
		ActorID: actorID,
		Item:    act.Item,
		Qty:     total,
		At:      now,
	})
	return produceChange{
		Item:   act.Item,
		Added:  total,
		NewQty: actor.Inventory[act.Item],
	}
}

// ProduceTickerInterval is how often RunProduceTicker wakes. The 1-min cadence
// is fine-grained against cycle durations measured in tens of minutes.
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
