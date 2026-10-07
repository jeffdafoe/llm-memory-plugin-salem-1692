package perception

import (
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// hearth_cooking_golden_test.go — LLM-474 fixtures. Hannah Boggs at her post
// with porridge on the books, across the four fire states the cue distinguishes:
// burning well, ebbing, dead, and no fireplace at all.
//
// The porridge recipe carries the hearth_lit BoostState in every fixture except
// the hearthless one, so the discriminator under test is the FIRE (and the
// presence of a hearth object), never the recipe.

// cookHearthClock is the fixed fire clock these fixtures measure burn time
// against — the hearth goldens' convention, restated here so the two files can
// drift apart without silently coupling.
var cookHearthClock = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

// boostedPorridgeRecipes is the catalog every cooking fixture shares: porridge
// with a live-fire bonus. OutputQty/prices mirror the live catalog closely
// enough to read true without pinning the golden to production tuning.
func boostedPorridgeRecipes() map[sim.ItemKind]*sim.ItemRecipe {
	return map[sim.ItemKind]*sim.ItemRecipe{
		"porridge": {
			OutputItem: "porridge", OutputQty: 10, RateQty: 10, RatePerHours: 1,
			Inputs:         []sim.RecipeInput{{Item: "flour", Qty: 2}, {Item: "water", Qty: 5}},
			BoostState:     []sim.BoostState{{State: sim.BoostStateHearthLit, BonusQty: 3}},
			WholesalePrice: 2, RetailPrice: 3,
		},
	}
}

// cookAtHearth is the shared fixture body. litUntil sets the fire state relative
// to cookHearthClock; hearthTagged decides whether the Inn has a fireplace at
// all; wood is the firewood Hannah carries; batchOn puts a porridge batch on the
// fire, which is what lets the stoke cue speak under a calm sky.
func cookAtHearth(litUntil time.Duration, hearthTagged bool, wood int, batchOn bool) (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	now := 720
	start, end := 360, 1080
	inventory := map[sim.ItemKind]int{}
	if wood > 0 {
		inventory[sim.FirewoodItemKind] = wood
	}
	hannah := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Hannah Boggs",
		Role:              "innkeeper",
		State:             sim.StateIdle,
		Pos:               sim.WorldPos{X: 100, Y: 100}.Tile(),
		InsideStructureID: "inn",
		WorkStructureID:   "inn",
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             25,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         inventory,
		RestockPolicy:     producePolicy("porridge", 20),
	}
	if batchOn {
		hannah.ProductionItem = "porridge"
		hannah.ProductionBatchQty = 10
		hannah.ProductionRemainingSeconds = 2400
	}
	tags := []string{sim.TagBusiness}
	if hearthTagged {
		tags = append(tags, sim.TagHearth)
	}
	inn := &sim.VillageObject{
		ID:           "inn",
		DisplayName:  "Inn",
		Pos:          sim.WorldPos{X: 100, Y: 100},
		OwnerActorID: "hannah",
		Tags:         tags,
	}
	if hearthTagged {
		inn.HearthLitUntil = cookHearthClock.Add(litUntil)
	}
	snap := &sim.Snapshot{
		PublishedAt:       cookHearthClock,
		LocalMinuteOfDay:  &now,
		NeedThresholds:    sim.NeedThresholds{},
		Assets:            emptyAssetSet,
		HearthLowMinutes:  60,
		StokeWoodPerStoke: 1,
		Environment:       sim.WorldEnvironment{Weather: sim.WeatherClear},
		Actors:            map[sim.ActorID]*sim.ActorSnapshot{"hannah": hannah},
		Structures: map[sim.StructureID]*sim.Structure{
			"inn": plainStructure("inn", "Inn"),
		},
		VillageObjects:    map[sim.VillageObjectID]*sim.VillageObject{"inn": inn},
		Recipes:           boostedPorridgeRecipes(),
		RestockReorderPct: 25,
	}
	return snap, "hannah", nil
}

// cookAtBurningHearth: the fire is well in (four hours of burn left, far above
// the 60-min low line). The cooking stake renders its warm tier and the stoke
// cue is ABSENT — the case that forced this to be a separate view from
// HearthView, which gates the stoke tool and is nil while a fire burns well.
func cookAtBurningHearth() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return cookAtHearth(4*time.Hour, true, 2, false)
}

// cookAtEbbingHearth: embers — under the low line but still lit — with a
// porridge batch on. Both sections render: "## Your hearth" carries the remedy
// (wood in hand, stoke now) and the cooking line carries the stake. Neither
// repeats the other.
func cookAtEbbingHearth() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return cookAtHearth(30*time.Minute, true, 2, true)
}

// cookAtEbbingHearthNothingOn: the same embers and wood in hand, calm sky, but
// no batch on and nobody cold. The stoke cue stays silent — a low fire costs
// nothing here yet — while the cooking line still states the stake for the next
// batch. The live case: John Ellis kept the Tavern fire in all day for a bread
// bonus, some thirty sticks a week.
func cookAtEbbingHearthNothingOn() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return cookAtHearth(30*time.Minute, true, 2, false)
}

// cookAtDeadHearthNoWood: the fire is out, she carries none, and a porridge
// batch is on. The cooking line takes its coldest tier; the wood steer stays
// where it belongs, in the hearth section.
func cookAtDeadHearthNoWood() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return cookAtHearth(-time.Hour, true, 0, true)
}

// cookAtHearthlessKitchen: the same cook and the same boosted recipe in a
// kitchen with NO hearth object. Nothing renders — this is the fixture that pins
// the promise that every non-hearth kitchen in the village is untouched.
func cookAtHearthlessKitchen() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	return cookAtHearth(0, false, 0, false)
}
