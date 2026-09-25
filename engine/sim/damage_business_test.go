package sim_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// damage_business_test.go — LLM-675: business damage on the public-works loop.

// buildBusinessDamageWorld is buildDamageWorld plus Joseph's shop (an owned
// wearable business with an open/closed asset, currently "open"), the Debris
// asset, and business terms of 25 coins for two hours.
func buildBusinessDamageWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	w, cancel := buildDamageWorld(t)
	mustSend(t, w, func(world *sim.World) {
		world.Assets["shop-asset"] = &sim.Asset{ID: "shop-asset", Name: "Shop", States: []sim.AssetState{
			{ID: 20, State: "closed"},
			{ID: 21, State: "open"},
		}}
		world.Assets[sim.DebrisAssetID] = &sim.Asset{ID: sim.DebrisAssetID, Name: "Debris", DefaultState: sim.DebrisStateWorn, States: []sim.AssetState{
			{ID: 30, State: sim.DebrisStateWorn},
			{ID: 31, State: sim.DebrisStateStorm},
		}}
		zero := 0
		world.VillageObjects["shop"] = &sim.VillageObject{
			ID: "shop", DisplayName: "General Store", AssetID: "shop-asset", CurrentState: "open",
			OwnerActorID: "joseph", Tags: []string{sim.TagBusiness}, Wear: 90,
			LoiterOffsetX: &zero, LoiterOffsetY: &zero, Pos: sim.WorldPos{X: 3000, Y: 3000},
		}
		world.Settings.PublicWorksBusinessBounty = 25
		world.Settings.PublicWorksBusinessRepairSeconds = 7200
		world.Settings.BusinessDamageWearReference = 180
		world.Settings.BusinessDamageMinGapHours = 24
		world.Settings.StallWearRepairThreshold = 180
		world.Settings.StallWearDegradeThreshold = 270
	})
	return w, cancel
}

// debrisOn returns the Debris overlay attached to businessID, or nil.
func debrisOn(world *sim.World, businessID sim.VillageObjectID) *sim.VillageObject {
	for _, o := range world.VillageObjects {
		if o.AttachedTo == businessID && o.HasTag(sim.TagDebris) {
			return o
		}
	}
	return nil
}

// TestBusinessDamagePlacesDebrisAndShutsTrade — a storm break damages the shop,
// hangs unnamed storm debris on it, and puts it out of trade; the repair takes
// the debris away and leaves the keeper's wear and the shop's open state alone.
func TestBusinessDamagePlacesDebrisAndShutsTrade(t *testing.T) {
	w, cancel := buildBusinessDamageWorld(t)
	defer cancel()
	if _, err := w.Send(sim.SetObjectDamage("shop", "storm")); err != nil {
		t.Fatalf("storm damage: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		shop := world.VillageObjects["shop"]
		if !shop.Damaged() {
			t.Fatal("shop not damaged")
		}
		d := debrisOn(world, "shop")
		if d == nil {
			t.Fatal("no debris on the damaged shop")
		}
		if d.CurrentState != sim.DebrisStateStorm {
			t.Errorf("debris state = %q, want storm", d.CurrentState)
		}
		if d.DisplayName != "" {
			t.Errorf("debris is named %q — the loiter resolvers would attribute people to it", d.DisplayName)
		}
		if !sim.BusinessOutOfTrade(shop, world.Settings.StallWearDegradeThreshold) {
			t.Error("a damaged shop is still in trade")
		}
		if sim.StallRepairable(shop, world.Settings.StallWearRepairThreshold, world.Settings.StallWearDegradeThreshold) {
			t.Error("damage made the keeper's own nail-mend available")
		}
		if got := sim.DamageFact(world.VillageObjects, world.Structures, world.Assets, shop); got != "The storm has torn at the General Store" {
			t.Errorf("fact = %q", got)
		}
	})
	if _, err := w.Send(sim.SetObjectDamage("shop", "repair")); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) {
		shop := world.VillageObjects["shop"]
		if shop.Damaged() || debrisOn(world, "shop") != nil {
			t.Errorf("after repair: damaged=%v debris=%v", shop.Damaged(), debrisOn(world, "shop"))
		}
		if shop.Wear != 90 {
			t.Errorf("wear = %d, want the keeper's 90 untouched", shop.Wear)
		}
		if shop.CurrentState != "open" {
			t.Errorf("state = %q, want open (a business has no damaged art to undo)", shop.CurrentState)
		}
	})
}

// TestBusinessDamageRollGuards — the business roll scales with wear, damages at
// most one business, and keeps its own min gap: a well repair does not hold off
// a shop break.
func TestBusinessDamageRollGuards(t *testing.T) {
	w, cancel := buildBusinessDamageWorld(t)
	defer cancel()
	now := time.Now().UTC()
	roll := func(vals ...float64) []*sim.VillageObject {
		t.Helper()
		var got []*sim.VillageObject
		mustSend(t, w, func(world *sim.World) {
			world.Settings.WellDamageStormChancePermille = 0
			world.Settings.BusinessDamageStormChancePermille = 100
			got = sim.RollStormDamage(world, &seqRoller{vals: vals}, now)
		})
		return got
	}
	// Wear 90 / 180 = factor 0.5: p = 0.05. 0.06 misses; 0.04 hits.
	if got := roll(0.06); len(got) != 0 {
		t.Fatalf("0.06 broke %v, want nothing at p=0.05", got)
	}
	mustSend(t, w, func(world *sim.World) { world.Environment.LastWellRepairAt = now })
	if got := roll(0.04); len(got) != 1 || got[0].ID != "shop" {
		t.Fatalf("roll = %v, want the shop (a recent well repair must not hold it off)", got)
	}
	if got := roll(0); len(got) != 0 {
		t.Errorf("a second break while the shop is damaged: %v", got)
	}
	if _, err := w.Send(sim.SetObjectDamage("shop", "repair")); err != nil {
		t.Fatal(err)
	}
	if got := roll(0); len(got) != 0 {
		t.Errorf("a break inside the business min gap: %v", got)
	}
}

// TestPublicWorksRepairAtDamagedBusiness — a hand inside the damaged shop takes
// the town's work on the business terms; on completion the chest pays 25, the
// debris goes, and the keeper's wear is untouched.
func TestPublicWorksRepairAtDamagedBusiness(t *testing.T) {
	w, cancel := buildBusinessDamageWorld(t)
	defer cancel()
	if _, err := w.Send(sim.SetObjectDamage("shop", "damage")); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) {
		world.Actors["anne"].Pos = sim.WorldPos{X: 3000, Y: 3000}.Tile()
		world.Actors["anne"].InsideStructureID = "shop"
	})
	if _, err := w.Send(sim.StartRepair("anne")); err != nil {
		t.Fatalf("StartRepair in the damaged shop: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		act := world.Actors["anne"].SourceActivity
		if act == nil || !act.PublicWorks || act.Bounty != 25 || act.ObjectID != "shop" {
			t.Fatalf("activity = %+v, want the town's repair of the shop at 25", act)
		}
		if got := act.Until.Sub(act.StartedAt); got != 2*time.Hour {
			t.Errorf("window = %v, want 2h", got)
		}
		act.Until = time.Now().UTC().Add(-time.Second)
	})
	mustSend(t, w, func(world *sim.World) { sim.CompleteDueSourceActivities(world, time.Now().UTC()) })
	mustSend(t, w, func(world *sim.World) {
		shop := world.VillageObjects["shop"]
		if shop.Damaged() || debrisOn(world, "shop") != nil {
			t.Error("shop still damaged after the town's repair landed")
		}
		if got := world.Actors["anne"].Coins; got != 25 {
			t.Errorf("anne coins = %d, want 25", got)
		}
		if got := world.Environment.TownChest; got != 75 {
			t.Errorf("chest = %d, want 75", got)
		}
		if shop.Wear != 90 {
			t.Errorf("wear = %d, want the keeper's 90 untouched", shop.Wear)
		}
	})
}

// TestKeeperNailMendLeavesTheTownsDamage — the keeper of a damaged shop cannot
// mend the damage; when his shop is also worn, his own mend clears the wear,
// the damage stays, and the town pays nothing.
func TestKeeperNailMendLeavesTheTownsDamage(t *testing.T) {
	w, cancel := buildBusinessDamageWorld(t)
	defer cancel()
	if _, err := w.Send(sim.SetObjectDamage("shop", "damage")); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) {
		world.Actors["joseph"].InsideStructureID = "shop"
		world.Actors["joseph"].Pos = sim.WorldPos{X: 3000, Y: 3000}.Tile()
	})
	if _, err := w.Send(sim.StartRepair("joseph")); err == nil || !strings.Contains(err.Error(), "town's to mend") {
		t.Fatalf("keeper StartRepair on town damage: err = %v, want the town's-work refusal", err)
	}

	mustSend(t, w, func(world *sim.World) {
		world.VillageObjects["shop"].Wear = 200
		world.Settings.StallNailsPerRepair = 1
		world.Actors["joseph"].Inventory = map[sim.ItemKind]int{sim.NailItemKind: 1}
	})
	if _, err := w.Send(sim.StartRepair("joseph")); err != nil {
		t.Fatalf("keeper nail-mend of a worn, damaged shop: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		act := world.Actors["joseph"].SourceActivity
		if act == nil || act.PublicWorks {
			t.Fatalf("activity = %+v, want the keeper's own mend", act)
		}
		act.Until = time.Now().UTC().Add(-time.Second)
	})
	mustSend(t, w, func(world *sim.World) { sim.CompleteDueSourceActivities(world, time.Now().UTC()) })
	mustSend(t, w, func(world *sim.World) {
		shop := world.VillageObjects["shop"]
		if shop.Wear != 0 {
			t.Errorf("wear = %d, want 0 after the keeper's mend", shop.Wear)
		}
		if !shop.Damaged() {
			t.Error("the keeper's nail-mend cleared the town's damage")
		}
		if world.Environment.TownChest != 100 || world.Actors["joseph"].Coins != 0 {
			t.Errorf("chest %d, joseph %d — the town paid for the keeper's mend", world.Environment.TownChest, world.Actors["joseph"].Coins)
		}
	})
}

// TestBusinessDamageNoticesAndTicker — a damaged shop pins its two lines to the
// board and a ticker line, with the business bounty.
func TestBusinessDamageNoticesAndTicker(t *testing.T) {
	w, cancel := buildBusinessDamageWorld(t)
	defer cancel()
	if _, err := w.Send(sim.SetObjectDamage("shop", "damage")); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) {
		c := world.NoticeboardContent["board"]
		if c == nil || c.Pinned != 2 {
			t.Fatalf("board = %+v, want 2 pinned lines", c)
		}
		if !strings.Contains(c.Text, "Boards have split and given way at the General Store") || !strings.Contains(c.Text, "25 coins") {
			t.Errorf("board text = %q", c.Text)
		}
	})
	snap := w.Published()
	lines := sim.DamageTickerLines(snap)
	if len(lines) != 1 || lines[0].ObjectID != "shop" || !strings.Contains(lines[0].Text, "25 coins") {
		t.Errorf("ticker = %+v", lines)
	}
}

// TestStalePublicWorksRepairIsSilent — the operator mends the shop while a
// hand's town repair is in flight: when the window lands there is nothing left
// to mend, so nothing is paid and no completion beat is emitted.
func TestStalePublicWorksRepairIsSilent(t *testing.T) {
	w, cancel := buildBusinessDamageWorld(t)
	defer cancel()
	if _, err := w.Send(sim.SetObjectDamage("shop", "damage")); err != nil {
		t.Fatal(err)
	}
	var completions []*sim.SourceActivityCompleted
	mustSend(t, w, func(world *sim.World) {
		world.Subscribe(sim.SubscriberFunc(func(_ *sim.World, evt sim.Event) {
			if c, ok := evt.(*sim.SourceActivityCompleted); ok {
				completions = append(completions, c)
			}
		}))
		world.Actors["anne"].Pos = sim.WorldPos{X: 3000, Y: 3000}.Tile()
		world.Actors["anne"].InsideStructureID = "shop"
	})
	if _, err := w.Send(sim.StartRepair("anne")); err != nil {
		t.Fatalf("StartRepair: %v", err)
	}
	if _, err := w.Send(sim.SetObjectDamage("shop", "repair")); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) {
		world.Actors["anne"].SourceActivity.Until = time.Now().UTC().Add(-time.Second)
	})
	mustSend(t, w, func(world *sim.World) { sim.CompleteDueSourceActivities(world, time.Now().UTC()) })
	mustSend(t, w, func(world *sim.World) {
		if got := world.Actors["anne"].Coins; got != 0 {
			t.Errorf("anne paid %d for a repair someone else landed", got)
		}
		if got := world.Environment.TownChest; got != 100 {
			t.Errorf("chest = %d, want the untouched 100", got)
		}
		if world.Actors["anne"].SourceActivity != nil {
			t.Error("the stale window was not cleared")
		}
		if len(completions) != 0 {
			t.Errorf("emitted %d completion(s) for a repair that never landed: %+v", len(completions), completions[0])
		}
	})
}

// TestRepairRemovesEveryDebrisOverlay — a stray second Debris overlay on the
// shop goes with the first when it is mended.
func TestRepairRemovesEveryDebrisOverlay(t *testing.T) {
	w, cancel := buildBusinessDamageWorld(t)
	defer cancel()
	if _, err := w.Send(sim.SetObjectDamage("shop", "damage")); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) {
		world.VillageObjects["stray-debris"] = &sim.VillageObject{ID: "stray-debris", AssetID: sim.DebrisAssetID,
			CurrentState: sim.DebrisStateWorn, AttachedTo: "shop", Tags: []string{sim.TagDebris}}
	})
	if _, err := w.Send(sim.SetObjectDamage("shop", "repair")); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) {
		if d := debrisOn(world, "shop"); d != nil {
			t.Errorf("debris %s still on the mended shop", d.ID)
		}
	})
}

// TestSetObjectDamageUnknownAction — an action the control does not know is
// refused, for a business as for a well.
func TestSetObjectDamageUnknownAction(t *testing.T) {
	w, cancel := buildBusinessDamageWorld(t)
	defer cancel()
	if _, err := w.Send(sim.SetObjectDamage("shop", "flood")); !errors.Is(err, sim.ErrUnknownDamageAction) {
		t.Errorf("err = %v, want ErrUnknownDamageAction", err)
	}
}

// TestWellUseCountsUnitsDrawn — a pail of five counts five toward the well's
// hazard, a drink one.
func TestWellUseCountsUnitsDrawn(t *testing.T) {
	w, cancel := buildDamageWorld(t)
	defer cancel()
	placeAt(t, w, "joseph", "well-a")
	grantForageEntry(t, w, "joseph", "water")
	if _, err := w.Send(sim.Gather("joseph", 5, time.Now())); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	mustSend(t, w, func(world *sim.World) {
		if got := world.VillageObjects["well-a"].UseSinceRepair; got != 5 {
			t.Errorf("use after a 5-unit pail = %d, want 5", got)
		}
		world.Actors["joseph"].Needs = map[sim.NeedKey]int{"thirst": 50}
	})
	if _, err := w.Send(sim.ApplyObjectRefreshAtArrival("joseph")); err != nil {
		t.Fatal(err)
	}
	mustSend(t, w, func(world *sim.World) {
		if got := world.VillageObjects["well-a"].UseSinceRepair; got != 6 {
			t.Errorf("use after a drink = %d, want 6", got)
		}
	})
}
