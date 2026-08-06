package sim_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildGatherTestWorld seeds a fixture for the gather verb (ZBBS-WORK-328):
//   - well: thirst refresh, INFINITE supply, GatherItem="water" (unbounded,
//     gather always succeeds — the v1 well model)
//   - bush: hunger refresh, FINITE supply (available=2 of max=4), continuous
//     regen, GatherItem="berries" (bounded — depletes and refills)
//   - dry_bush: hunger refresh, FINITE depleted (available=0), GatherItem="berries"
//   - sell_bush: YIELD-ONLY (amount=0) FINITE (available=2 of max=4),
//     GatherItem="berries" — the forage-to-sell row (LLM-24): harvestable with
//     no consume-in-place need
//   - oak: hunger refresh, infinite, NO GatherItem (consume-in-place only —
//     proves a refresh row without GatherItem isn't gatherable)
//   - bench: no refreshes (decorative — proves resolve-then-check)
//
// Item catalog seeds water + berries so resolveItemKind succeeds.
func buildGatherTestWorld(t *testing.T) (*sim.World, context.CancelFunc) {
	t.Helper()
	repo, handles := mem.NewRepository()

	handles.ItemKinds.Seed(map[sim.ItemKind]*sim.ItemKindDef{
		"water": {
			Name:     "water",
			Category: sim.ItemCategoryDrink,
			Satisfies: []sim.ItemSatisfaction{
				{Attribute: "thirst", Immediate: 8},
			},
		},
		"berries": {
			Name:     "berries",
			Category: sim.ItemCategoryFood,
			Satisfies: []sim.ItemSatisfaction{
				{Attribute: "hunger", Immediate: 4},
			},
		},
	})

	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"well-stone":   {ID: "well-stone", Name: "Old Well"},
		"bush-berries": {ID: "bush-berries", Name: "Berry Bush"},
		"tree-oak":     {ID: "tree-oak", Name: "Oak"},
		"bench-wood":   {ID: "bench-wood", Name: "Bench"},
	})
	zero := 0
	ip := func(v int) *int { return &v }
	tp := func(t time.Time) *time.Time { return &t }
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"well": {
			ID: "well", DisplayName: "Old Well", AssetID: "well-stone", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 100, Y: 100},
			Refreshes: []*sim.ObjectRefresh{
				{Attribute: "thirst", Amount: -12, GatherItem: "water"}, // infinite
			},
		},
		"bush": {
			ID: "bush", DisplayName: "Berry Bush", AssetID: "bush-berries", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 500, Y: 500},
			Refreshes: []*sim.ObjectRefresh{
				{
					Attribute:          "hunger",
					Amount:             -8,
					AvailableQuantity:  ip(2),
					MaxQuantity:        ip(4),
					RefreshMode:        sim.RefreshModeContinuous,
					RefreshPeriodHours: ip(6),
					LastRefreshAt:      tp(time.Now().UTC()),
					GatherItem:         "berries",
				},
			},
		},
		"dry_bush": {
			ID: "dry_bush", DisplayName: "Picked Bush", AssetID: "bush-berries", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 1000, Y: 1000},
			Refreshes: []*sim.ObjectRefresh{
				{
					Attribute:          "hunger",
					Amount:             -8,
					AvailableQuantity:  ip(0), // depleted
					MaxQuantity:        ip(4),
					RefreshMode:        sim.RefreshModePeriodic,
					RefreshPeriodHours: ip(8),
					GatherItem:         "berries",
				},
			},
		},
		"sell_bush": {
			ID: "sell_bush", DisplayName: "Berry Patch", AssetID: "bush-berries", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 1250, Y: 1250},
			Refreshes: []*sim.ObjectRefresh{
				{
					Attribute:          "hunger",
					Amount:             0, // yield-only: forage-to-sell, no need drop
					AvailableQuantity:  ip(2),
					MaxQuantity:        ip(4),
					RefreshMode:        sim.RefreshModeContinuous,
					RefreshPeriodHours: ip(6),
					LastRefreshAt:      tp(time.Now().UTC()),
					GatherItem:         "berries",
				},
			},
		},
		"oak": {
			ID: "oak", DisplayName: "Oak", AssetID: "tree-oak", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 1500, Y: 1500},
			Refreshes: []*sim.ObjectRefresh{
				{Attribute: "hunger", Amount: -4}, // no GatherItem
			},
		},
		// prod_well models the SHIPPED well shape (LLM-254): an infinite drink row
		// carrying no GatherItem, plus a SEPARATE finite yield-only carry row. The
		// older "well" fixture above folds both into one Amount<0 row, which reads as
		// pick-and-eat and so cannot exercise the LLM-610 gate at all.
		"prod_well": {
			ID: "prod_well", DisplayName: "Village Well", AssetID: "well-stone", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 2500, Y: 2500},
			Refreshes: []*sim.ObjectRefresh{
				{Attribute: "thirst", Amount: -8},                                                // drink: infinite, ungathered, open to all
				{Amount: 0, GatherItem: "water", AvailableQuantity: ip(20), MaxQuantity: ip(20)}, // carry: yield-only
			},
		},
		"bench": {
			ID: "bench", DisplayName: "Bench", AssetID: "bench-wood", CurrentState: "default",
			LoiterOffsetX: &zero, LoiterOffsetY: &zero,
			Pos: sim.WorldPos{X: 2000, Y: 2000},
		},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"hannah": {ID: "hannah", LLMAgent: "hannah-innkeeper"},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	return w, cancel
}

// placeAt moves the actor onto objID's loiter pin (zero offset = anchor tile).
func placeAt(t *testing.T, w *sim.World, actorID sim.ActorID, objID sim.VillageObjectID) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		obj := world.VillageObjects[objID]
		if obj == nil {
			return nil, fmt.Errorf("placeAt: no object %q", objID)
		}
		actor := world.Actors[actorID]
		if actor == nil {
			return nil, fmt.Errorf("placeAt: no actor %q", actorID)
		}
		actor.Pos = obj.Pos.Tile()
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("placeAt(%s): %v", objID, err)
	}
}

// grantForageEntry gives actorID a `forage` restock entry for kind — the trade
// LLM-610 requires before an actor may take from a YIELD-ONLY (forage-to-sell)
// source it does not own. Pick-and-eat rows are open to all and need no grant, so
// only the sell_bush cases call this.
func grantForageEntry(t *testing.T, w *sim.World, actorID sim.ActorID, kind sim.ItemKind) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		actor := world.Actors[actorID]
		if actor == nil {
			return nil, fmt.Errorf("grantForageEntry: no actor %q", actorID)
		}
		if actor.RestockPolicy == nil {
			actor.RestockPolicy = &sim.RestockPolicy{}
		}
		actor.RestockPolicy.Restock = append(actor.RestockPolicy.Restock, sim.RestockEntry{
			Item:   kind,
			Source: sim.RestockSourceForage,
		})
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("grantForageEntry(%s, %s): %v", actorID, kind, err)
	}
}

// inventoryOf reads actorID's quantity of kind off the live world.
func inventoryOf(t *testing.T, w *sim.World, actorID sim.ActorID, kind sim.ItemKind) int {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		actor := world.Actors[actorID]
		if actor == nil {
			return 0, fmt.Errorf("inventoryOf: no actor %q", actorID)
		}
		return actor.Inventory[kind], nil
	}})
	if err != nil {
		t.Fatalf("inventoryOf: %v", err)
	}
	return res.(int)
}

// setObjectOwner stamps objID's OwnerActorID on the live world. The owner-gate
// under test (VillageObject.OwnedByOther) compares ids, so the owner needn't be
// a seeded actor — this bypasses SetVillageObjectOwner's actor-existence check.
func setObjectOwner(t *testing.T, w *sim.World, objID sim.VillageObjectID, owner sim.ActorID) {
	t.Helper()
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		obj := world.VillageObjects[objID]
		if obj == nil {
			return nil, fmt.Errorf("setObjectOwner: no object %q", objID)
		}
		obj.OwnerActorID = owner
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("setObjectOwner(%s): %v", objID, err)
	}
}

// TestGather_OwnedByOther_Rejects — LLM-50 D2: an owned gatherable is owner-only.
// A non-owner standing at it is rejected with ErrNotYourSource (the source
// exists, so it is NOT ErrNoGatherSource), and no stock is drawn down.
func TestGather_OwnedByOther_Rejects(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setObjectOwner(t, w, "bush", "prudence") // owned by someone other than hannah
	placeAt(t, w, "hannah", "bush")

	_, err := w.Send(sim.Gather("hannah", 1, time.Now().UTC()))
	if !errors.Is(err, sim.ErrNotYourSource) {
		t.Errorf("err=%v, want ErrNotYourSource", err)
	}
	if got := inventoryOf(t, w, "hannah", "berries"); got != 0 {
		t.Errorf("inventory berries=%d, want 0 (rejected before harvest)", got)
	}
}

// TestGather_OwnedBySelf_Succeeds — the owner harvests their own source.
func TestGather_OwnedBySelf_Succeeds(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	setObjectOwner(t, w, "bush", "hannah")
	placeAt(t, w, "hannah", "bush")

	res, err := w.Send(sim.Gather("hannah", 1, time.Now().UTC()))
	if err != nil {
		t.Fatalf("owner gather: %v", err)
	}
	if gr := res.(sim.GatherResult); gr.Item != "berries" || gr.Qty != 1 {
		t.Errorf("got Item=%q Qty=%d, want berries/1", gr.Item, gr.Qty)
	}
}

// TestGather_Unowned_IsCommons — an unowned ("" owner) gatherable stays commons:
// anyone may harvest it (pre-LLM-50 behavior preserved).
func TestGather_Unowned_IsCommons(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "bush") // bush seeded with no owner
	if _, err := w.Send(sim.Gather("hannah", 1, time.Now().UTC())); err != nil {
		t.Fatalf("commons gather: %v", err)
	}
}

// TestGather_NearestOwned_RejectsDespiteFartherCommons — the command-side half of
// the cue/command parity (pairs with the perception-side
// TestBuild_GatherableCue_NearestOwnedSuppresses_NoFallthrough). With an OWNED
// bush on the actor's tile (nearest) and an UNOWNED bush one tile away (farther,
// still in range), Gather resolves the SINGLE nearest object (findRefreshObjectNear
// does not skip past it) and rejects with ErrNotYourSource — it does not fall
// through to the farther commons. So suppressing the cue entirely is correct.
func TestGather_NearestOwned_RejectsDespiteFartherCommons(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	ip := func(v int) *int { return &v }
	_, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		base := sim.WorldPos{X: 3000, Y: 3000}
		world.Actors["hannah"].Pos = base.Tile()
		zero, east := 0, 1
		world.VillageObjects["owned_bush"] = &sim.VillageObject{
			ID: "owned_bush", DisplayName: "Prudence's Bush", AssetID: "bush-berries", CurrentState: "default",
			Pos: base, LoiterOffsetX: &zero, LoiterOffsetY: &zero, // cheb 0 from the actor
			OwnerActorID: "prudence",
			Refreshes: []*sim.ObjectRefresh{{
				Attribute: "hunger", Amount: 0, AvailableQuantity: ip(5), MaxQuantity: ip(5),
				RefreshMode: sim.RefreshModeContinuous, RefreshPeriodHours: ip(6), GatherItem: "berries",
			}},
		}
		world.VillageObjects["commons_bush"] = &sim.VillageObject{
			ID: "commons_bush", DisplayName: "Wild Bush", AssetID: "bush-berries", CurrentState: "default",
			Pos: base, LoiterOffsetX: &east, LoiterOffsetY: &zero, // cheb 1 → farther, still in range
			Refreshes: []*sim.ObjectRefresh{{
				Attribute: "hunger", Amount: 0, AvailableQuantity: ip(5), MaxQuantity: ip(5),
				RefreshMode: sim.RefreshModeContinuous, RefreshPeriodHours: ip(6), GatherItem: "berries",
			}},
		}
		return nil, nil
	}})
	if err != nil {
		t.Fatalf("inject: %v", err)
	}

	_, err = w.Send(sim.Gather("hannah", 1, time.Now().UTC()))
	if !errors.Is(err, sim.ErrNotYourSource) {
		t.Errorf("err=%v, want ErrNotYourSource (nearest owned bush, not the farther commons)", err)
	}
}

func TestGather_InfiniteWell_AlwaysSucceeds(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "well")

	res, err := w.Send(sim.Gather("hannah", 3, time.Now().UTC()))
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	gr := res.(sim.GatherResult)
	if gr.Item != "water" || gr.Qty != 3 {
		t.Errorf("got Item=%q Qty=%d, want water/3", gr.Item, gr.Qty)
	}
	if gr.SourceName != "Old Well" {
		t.Errorf("SourceName=%q, want Old Well", gr.SourceName)
	}
	if got := inventoryOf(t, w, "hannah", "water"); got != 3 {
		t.Errorf("inventory water=%d, want 3", got)
	}
}

func TestGather_FiniteBush_DecrementsAndClamps(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "bush")

	// bush has 2 available; ask for 5 → clamps to 2.
	res, err := w.Send(sim.Gather("hannah", 5, time.Now().UTC()))
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	gr := res.(sim.GatherResult)
	if gr.Qty != 2 {
		t.Errorf("Qty=%d, want 2 (clamped to available)", gr.Qty)
	}
	if got := inventoryOf(t, w, "hannah", "berries"); got != 2 {
		t.Errorf("inventory berries=%d, want 2", got)
	}

	// Now empty — a second gather rejects as depleted.
	_, err = w.Send(sim.Gather("hannah", 1, time.Now().UTC()))
	if !errors.Is(err, sim.ErrGatherableDepleted) {
		t.Errorf("second gather err=%v, want ErrGatherableDepleted", err)
	}
}

func TestGather_DepletedBush_Rejects(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "dry_bush")

	_, err := w.Send(sim.Gather("hannah", 1, time.Now().UTC()))
	if !errors.Is(err, sim.ErrGatherableDepleted) {
		t.Errorf("err=%v, want ErrGatherableDepleted", err)
	}
}

// TestGather_YieldOnlyBush_HarvestsAndDecrements covers the forage-to-sell row
// (LLM-24): a yield-only (amount=0) finite gatherable harvests into inventory
// and draws its supply down exactly like an eat+pick bush — the decoupling
// only removes the consume-in-place need, not the harvest.
func TestGather_YieldOnlyBush_HarvestsAndDecrements(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "sell_bush")
	grantForageEntry(t, w, "hannah", "berries") // LLM-610: forage-to-sell needs the trade

	res, err := w.Send(sim.Gather("hannah", 2, time.Now().UTC()))
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	gr := res.(sim.GatherResult)
	if gr.Item != "berries" || gr.Qty != 2 {
		t.Errorf("got Item=%q Qty=%d, want berries/2", gr.Item, gr.Qty)
	}
	if got := inventoryOf(t, w, "hannah", "berries"); got != 2 {
		t.Errorf("inventory berries=%d, want 2", got)
	}

	// Supply drawn down from 2 to 0; a follow-up gather rejects as depleted.
	_, err = w.Send(sim.Gather("hannah", 1, time.Now().UTC()))
	if !errors.Is(err, sim.ErrGatherableDepleted) {
		t.Errorf("second gather err=%v, want ErrGatherableDepleted", err)
	}
}

func TestGather_NonGatherableRefreshObject_Rejects(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "oak") // has a refresh row but no GatherItem

	_, err := w.Send(sim.Gather("hannah", 1, time.Now().UTC()))
	if !errors.Is(err, sim.ErrNoGatherSource) {
		t.Errorf("err=%v, want ErrNoGatherSource", err)
	}
}

func TestGather_NoSourceHere_Rejects(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "bench") // decorative, no refreshes

	_, err := w.Send(sim.Gather("hannah", 1, time.Now().UTC()))
	if !errors.Is(err, sim.ErrNoGatherSource) {
		t.Errorf("err=%v, want ErrNoGatherSource", err)
	}
}

func TestGather_DefaultsQtyToOne(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "well")

	res, err := w.Send(sim.Gather("hannah", 0, time.Now().UTC())) // qty<1 → 1
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if gr := res.(sim.GatherResult); gr.Qty != 1 {
		t.Errorf("Qty=%d, want 1 (default)", gr.Qty)
	}
}

func TestGather_UnknownActor_Errors(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()

	_, err := w.Send(sim.Gather("ghost", 1, time.Now().UTC()))
	if err == nil {
		t.Fatal("want error for unknown actor, got nil")
	}
}

// TestGather_ProductionWell_RefusesWithoutTheTrade is the LLM-610 regression, run
// through the real Gather command against the SHIPPED two-row well.
//
// The live case it reproduces: Ezekiel Crane, a smith with no forage water entry,
// walked to the village well on 2026-08-06, drank, and — before this gate — could
// have carried off the whole 20-pail yield the mill needs to supply the village.
// Fourteen such actors had drawn 58 pails between them.
func TestGather_ProductionWell_RefusesWithoutTheTrade(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "prod_well") // no forage entries at all

	_, err := w.Send(sim.Gather("hannah", 1, time.Now().UTC()))
	if !errors.Is(err, sim.ErrNoGatherSource) {
		t.Fatalf("gather at a commons well without the trade: err=%v, want ErrNoGatherSource", err)
	}
	if got := inventoryOf(t, w, "hannah", "water"); got != 0 {
		t.Errorf("water=%d, want 0 — the pail must not have been drawn", got)
	}
}

// TestGather_ProductionWell_AllowsTheWaterDrawer is the other half: the actor the
// role belongs to still draws. Without this the gate could pass by refusing
// everyone, which would starve the village instead of protecting it.
func TestGather_ProductionWell_AllowsTheWaterDrawer(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "prod_well")
	grantForageEntry(t, w, "hannah", "water")

	res, err := w.Send(sim.Gather("hannah", 2, time.Now().UTC()))
	if err != nil {
		t.Fatalf("gather with the trade: %v", err)
	}
	if gr := res.(sim.GatherResult); gr.Item != "water" || gr.Qty != 2 {
		t.Errorf("got Item=%q Qty=%d, want water/2", gr.Item, gr.Qty)
	}
	if got := inventoryOf(t, w, "hannah", "water"); got != 2 {
		t.Errorf("water=%d, want 2", got)
	}
}

// TestGather_PickAndEatBushStaysACommons pins the other side of the rule: the
// unowned wild bushes are food anyone may take, and the gate must not touch them.
// Regression against over-reaching — an earlier draft of this ticket wrongly
// counted 157 legitimate berry picks as violations.
func TestGather_PickAndEatBushStaysACommons(t *testing.T) {
	w, cancel := buildGatherTestWorld(t)
	defer cancel()
	placeAt(t, w, "hannah", "bush") // Amount<0 + GatherItem = pick-and-eat, unowned

	res, err := w.Send(sim.Gather("hannah", 1, time.Now().UTC()))
	if err != nil {
		t.Fatalf("a wild bush must stay open to everyone: %v", err)
	}
	if gr := res.(sim.GatherResult); gr.Item != "berries" {
		t.Errorf("got Item=%q, want berries", gr.Item)
	}
}
