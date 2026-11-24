package sim

import (
	"fmt"
	"log"
	"time"
)

// farm_upkeep.go — LLM-215. A recurring "wealth tax" on farm-tagged producers.
// Terminator producers (empty-inputs recipes) mint goods — and therefore coin —
// from nothing, so their owners pool wealth the rest of the village never
// recirculates. Each game-day a farm's upkeep shovels wear out, and the owner
// owes one fresh shovel per FarmUpkeepCoinsPerShovel coins held ABOVE
// FarmUpkeepFloor, bought from the smith — draining the windfall back into the
// community (the LLM-83 circulation lever, and the demand half of the shovel loop
// the smith produces, LLM-200).
//
// Stock-based (keyed on coins HELD), so — unlike stall wear (LLM-118) — it carries
// NO per-object accumulator: the obligation is a pure function of the owner's
// current coins, re-derived every assessment and every perception build. The only
// new persistent state is the two tunable knobs on WorldSettings.
//
// Seams: assessFarmUpkeep fires once per game-day from checkAndRotate
// (world_rotation.go) on the durable LastRotationAt boundary; the standing owner
// cue is perception/farm_upkeep.go; the wake warrant is WarrantKindFarmUpkeep.

const (
	// DefaultFarmUpkeepFloor (Y) is the coin balance a farm keeps untaxed — only
	// coins above it are assessed. DefaultFarmUpkeepCoinsPerShovel (X) is the coin
	// band per owed shovel, so the marginal rate above the floor is the shovel
	// retail price / X. A non-positive coins-per-shovel disables the feature (the
	// per-feature off-switch, mirroring StallWearPerCoin==0). Guesstimates, tuned
	// live via the umbilical.
	DefaultFarmUpkeepFloor          = 30
	DefaultFarmUpkeepCoinsPerShovel = 20
)

// TagFarm scopes the upkeep tax to farm instances. An operator opts a structure in
// by tagging its village object (umbilical /object/add-tag; the tag vocabulary was
// opened in LLM-203); a farm is assessed only once it ALSO carries an owner (the
// owner holds the coin and buys the shovels). Tag-scoped rather than
// asset-name-matched — the engine carries no catalog string and an operator can
// add/remove a farm live — mirroring TagMarketStall.
const TagFarm = "farm"

// ShovelItemKind is the upkeep consumable: the smith's shovel (LLM-200 catalog +
// recipe; produced by Ezekiel). Bought from the smith and worn out maintaining the
// farm — the coin conduit farm→smith.
const ShovelItemKind ItemKind = "shovel"

// IsFarmStructure reports whether obj is an owned farm — the scope gate for the
// upkeep assessment and the perception cue. Nil-safe. Mirrors IsWearableStall.
func IsFarmStructure(obj *VillageObject) bool {
	return obj != nil && obj.OwnerActorID != "" && obj.HasTag(TagFarm)
}

// OwnedFarm returns the farm owned by ownerID, or nil when they own none. Takes the
// object map so it serves both the live World (w.VillageObjects) and a perception
// Snapshot (snap.VillageObjects). First match wins (a farmer owns one farm by data
// convention). Mirrors OwnedWearableStall.
func OwnedFarm(objects map[VillageObjectID]*VillageObject, ownerID ActorID) *VillageObject {
	if ownerID == "" {
		return nil
	}
	for _, obj := range objects {
		// nil-safe: also runs over hand-built perception/test maps where a stray
		// nil entry must not panic the world (LLM-417; same guard as OwnedHearth).
		if obj != nil && obj.OwnerActorID == ownerID && IsFarmStructure(obj) {
			return obj
		}
	}
	return nil
}

// FarmUpkeepObligation returns how many upkeep shovels a farm owner owes this cycle:
// one per coinsPerShovel coins held strictly above floor, floored. A non-positive
// coinsPerShovel disables the tax (returns 0 — the off-switch); a balance at or
// below the floor owes nothing. Pure, so the assessment and the perception cue read
// the same obligation off the same inputs.
func FarmUpkeepObligation(coins, floor, coinsPerShovel int) int {
	if coinsPerShovel <= 0 || coins <= floor {
		return 0
	}
	return (coins - floor) / coinsPerShovel
}

// FarmUpkeepWarrantReason wakes a farm owner, once per daily assessment, to buy
// their worn-out upkeep shovels from the smith. FarmID is the assessed farm —
// carried for telemetry/replay and so the cue can name it; the deliberation reads
// the owner's live coins + shovel count from perception. DedupDiscriminator returns
// 0: an upkeep obligation is a state condition (coins above the floor), not an
// event, so it bypasses the substrate's source-key dedup — mirroring
// NeedThresholdWarrantReason. Re-stamped each daily assessment; the standing
// perception cue keeps steering the owner in between.
type FarmUpkeepWarrantReason struct {
	FarmID VillageObjectID
}

func (FarmUpkeepWarrantReason) isWarrantReason()           {}
func (FarmUpkeepWarrantReason) Kind() WarrantKind          { return WarrantKindFarmUpkeep }
func (FarmUpkeepWarrantReason) DedupDiscriminator() uint64 { return 0 }

// farmUpkeepWarrantEligible gates who the daily assessment wakes: an agent-backed
// NPC not already pending or mid-tick. Mirrors restockEligible's archetype/pending
// filter but omits the walk/sleep guards — the assessment fires at most once per
// game-day (not per minute), so it can't thrash an in-flight walk, and a warrant
// stamped while the owner sleeps simply waits for their next tick, where the
// standing upkeep cue re-derives the obligation. Consumption is unconditional and
// handled by the caller.
func farmUpkeepWarrantEligible(a *Actor) bool {
	if a == nil {
		return false
	}
	if a.Kind != KindNPCStateful && a.Kind != KindNPCShared {
		return false
	}
	if a.VisitorState != nil {
		return false
	}
	return a.WarrantedSince == nil && !a.TickInFlight
}

// assessFarmUpkeep runs one daily upkeep pass over every owned farm: it wears out
// (consumes) the farm's held upkeep shovels, then — for an eligible owner still
// holding coin above the floor — wakes them to buy fresh ones from the smith. Coin
// only leaves the farm when the owner actually completes the purchase
// (pay_with_item → commitPayTransfer), so this pass never touches coins. A
// non-positive FarmUpkeepCoinsPerShovel disables the feature entirely (no
// consumption, no warrant). Called on the world goroutine from checkAndRotate, so
// the tryStampWarrant calls are serialized.
func assessFarmUpkeep(w *World, now time.Time) {
	if w == nil || w.Settings.FarmUpkeepCoinsPerShovel <= 0 {
		return
	}
	floor := w.Settings.FarmUpkeepFloor
	perShovel := w.Settings.FarmUpkeepCoinsPerShovel
	// One assessment per OWNER, not per farm object. A farmer owns one farm by data
	// convention (OwnedFarm), but a live-tagged duplicate must not consume/warrant the
	// same owner twice in a pass — the second pass would find the shovels already gone
	// and (were it not for this guard) re-evaluate the same coin balance. Assess-per-
	// owner keeps the levy independent of how many farm objects an owner happens to
	// carry; multiplying the tax by farm count is explicitly NOT the intent.
	assessed := make(map[ActorID]bool)
	for _, obj := range w.VillageObjects {
		if !IsFarmStructure(obj) {
			continue
		}
		if assessed[obj.OwnerActorID] {
			continue
		}
		assessed[obj.OwnerActorID] = true
		owner := w.Actors[obj.OwnerActorID]
		if owner == nil {
			continue
		}
		// The season's work wears out this cycle's shovels — the farm must re-buy to
		// meet next assessment's obligation, which is what makes the tax recurring
		// rather than a one-time coins→shovels swap. Same inventory-decrement as a
		// consumed recipe input (produce_tick.go); shovels have no other use, so
		// clearing the item is the whole upkeep stock.
		worn := owner.Inventory[ShovelItemKind]
		delete(owner.Inventory, ShovelItemKind)
		recordFarmToolsWorn(w, owner, worn, now)
		if !farmUpkeepWarrantEligible(owner) {
			continue
		}
		if FarmUpkeepObligation(owner.Coins, floor, perShovel) <= 0 {
			continue
		}
		tryStampWarrant(w, owner, WarrantMeta{
			TriggerActorID: owner.ID,
			Reason:         FarmUpkeepWarrantReason{FarmID: obj.ID},
		}, now)
	}
}

// FarmToolsWorn is what the last upkeep assessment wore out of a farm owner's
// pack (LLM-1005). The delete is silent by nature — it lands at the daily
// boundary while the owner sleeps, and a warrant stamped on a sleeper is
// cleared long before she wakes — so without this record an owner who bought
// shovels all day woke holding none and blamed the smith for goods she had in
// fact received. The `## Farm upkeep` cue renders the loss until the next
// assessment replaces it. Checkpointed with the inventory it explains
// (actor.farm_tools_worn_qty / _at), so the two cannot disagree after a
// restart, which on this engine happens several times a day.
type FarmToolsWorn struct {
	Qty int       // shovels removed by that assessment (> 0; a zero assessment clears the record)
	At  time.Time // the assessment's boundary instant
}

// copyFarmToolsWorn value-copies the record for a snapshot, so the published
// snapshot never aliases the live actor's pointer.
func copyFarmToolsWorn(fw *FarmToolsWorn) *FarmToolsWorn {
	if fw == nil {
		return nil
	}
	c := *fw
	return &c
}

// FarmToolsWornWindow bounds how long the cue shows a loss. Each assessment
// replaces or clears the record, so this only matters when assessments stop
// (the feature switched off, the farm untagged): the line must not outlive
// the day it explains.
const FarmToolsWornWindow = 24 * time.Hour

// FarmToolsWornNarration is the owner's line for a loss: what went and that it
// is ordinary wear. Shared by the action-log entry, the durable row and the
// perception cue so memory and prompt tell one story. No seller is named — the
// engine does not track who sold which shovel — and no time of day, because
// the boot catch-up can run the assessment at any hour (RunRotationTicker).
func FarmToolsWornNarration(qty int) string {
	if qty == 1 {
		return "The shovel you held was worn out in the farm's upkeep — that is the way of farm tools."
	}
	return fmt.Sprintf("The %d shovels you held were worn out in the farm's upkeep — that is the way of farm tools.", qty)
}

// recordFarmToolsWorn sets (qty > 0) or clears (qty == 0) the owner's
// FarmToolsWorn and, for a real loss, writes the `farm_tools_worn` action-log
// entry (read by the shared-NPC soul pass) and durable row (narrated into a
// stateful NPC's memory by memory-api). Written once, at the change. MUST run
// on the world goroutine.
func recordFarmToolsWorn(w *World, owner *Actor, qty int, at time.Time) {
	if qty <= 0 {
		owner.FarmToolsWorn = nil
		return
	}
	owner.FarmToolsWorn = &FarmToolsWorn{Qty: qty, At: at}
	text := FarmToolsWornNarration(qty)
	if _, err := AppendActionLogEntry(ActionLogEntry{
		ActorID:    owner.ID,
		OccurredAt: at,
		ActionType: ActionTypeFarmToolsWorn,
		Text:       text,
	}).Fn(w); err != nil {
		log.Printf("sim/farm_upkeep: append farm_tools_worn for %s: %v", owner.DisplayName, err)
	}
	w.AppendActionLogDurable(DurableActionLogRow{
		ActorID:    owner.ID,
		OccurredAt: at,
		ActionType: ActionTypeFarmToolsWorn,
		Payload: map[string]any{
			"item": string(ShovelItemKind),
			"qty":  qty,
			"text": text,
		},
		SpeakerName: owner.DisplayName,
		Source:      "engine",
	})
}

// FarmUpkeepReserve is how many of a farm owner's held shovels are spoken for
// by the upkeep obligation (LLM-1005): up to what the current obligation calls
// for, so the wares cue, the carry line and the barter gates leave them in the
// pack. 0 for anyone who owns no farm. Held shovels beyond the obligation stay
// spare. objects is the live or snapshot object map (see OwnedFarm).
func FarmUpkeepReserve(objects map[VillageObjectID]*VillageObject, ownerID ActorID, coins, held, floor, coinsPerShovel int) int {
	if held <= 0 || OwnedFarm(objects, ownerID) == nil {
		return 0
	}
	owed := FarmUpkeepObligation(coins, floor, coinsPerShovel)
	if owed > held {
		return held
	}
	return owed
}

// liveFarmUpkeepReserve is FarmUpkeepReserve over a live actor and the live
// settings — shared by LiveBarterHolder and publishedActorSnapshot so the live
// gates and the published snapshot compute the claim one way.
func liveFarmUpkeepReserve(w *World, a *Actor) int {
	if w == nil || a == nil {
		return 0
	}
	return FarmUpkeepReserve(w.VillageObjects, a.ID, a.Coins, a.Inventory[ShovelItemKind],
		w.Settings.FarmUpkeepFloor, w.Settings.FarmUpkeepCoinsPerShovel)
}

// ApplyFarmUpkeep wraps the daily assessment as a Command so the rotation driver
// can run it on the world goroutine. Mirrors EvaluateRestock / ApplyDailyRotation.
func ApplyFarmUpkeep(now time.Time) Command {
	return Command{Fn: func(w *World) (any, error) {
		assessFarmUpkeep(w, now)
		return nil, nil
	}}
}
