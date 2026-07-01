package sim

import "time"

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
		if obj.OwnerActorID == ownerID && IsFarmStructure(obj) {
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
		delete(owner.Inventory, ShovelItemKind)
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

// ApplyFarmUpkeep wraps the daily assessment as a Command so the rotation driver
// can run it on the world goroutine. Mirrors EvaluateRestock / ApplyDailyRotation.
func ApplyFarmUpkeep(now time.Time) Command {
	return Command{Fn: func(w *World) (any, error) {
		assessFarmUpkeep(w, now)
		return nil, nil
	}}
}
