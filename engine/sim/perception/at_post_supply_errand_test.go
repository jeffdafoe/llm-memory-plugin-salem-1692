package perception

import (
	"regexp"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// walkToBulletRE matches exactly the destination-bearing buy bullet
// renderWalkToVendors emits — "  - buy from <place> (destination: <id>)". It is
// the off-scene walk-to marker every supply cue shares, and the ONLY thing that
// contradicts the at-post stabilizer. Anchored on BOTH "- buy from " and
// "(destination: " on one line so it can't match a co-present "Buy it now" prose
// line (no bullet), a blocked-supplier "<place> sells <item>, but…" line (no
// destination), or the duty/anchors destination tokens (no "buy from" bullet) —
// the false positives a bare "- buy from " check would admit (code_review).
var walkToBulletRE = regexp.MustCompile(`(?m)^\s*- buy from .*\(destination: `)

// at_post_supply_errand_test.go — LLM-491. The at-post duty stabilizer
// (renderDutySteer) has two forms: the default "stay and look after your work"
// and, when a supply cue in the SAME prompt sends the subject off to buy
// something, a reconciled "going to fetch it and coming back is part of minding
// your trade" step-out line. Before LLM-491 only the forage cue (own bushes) got a
// reconciled form; the four BUY-side supply cues — "## Restocking", the
// buy-nails-at-post repair, the season's shovels, and a cold owner's hearth —
// rendered their off-scene "(destination: <id>)" bullets while the stabilizer
// still said "stay". The live trigger was Josiah Thorne, pinned to the General
// Store and handed James Farm's destination id for the wheat he was low on, in one
// turn (huddle hud-eb0280e0bc4342584a012eb60cee10e0, 2026-07-20).
//
// The golden scenario below pins the restock arm end-to-end: a keeper at his post,
// resale stock low, the only supplier off-scene → the reconciled step-out line, not
// the bare stay form. The cross-scenario invariant then holds the whole class: over
// every scenario in the matrix, an at-post subject whose prompt names an off-scene
// buy supplier must never also carry the bare stay steer.

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "vendor_at_post_restock_supplier_offscene",
			summary: "LLM-491: a keeper (John Ellis) on shift INSIDE his own tavern (at post) is out of a bought-in " +
				"resale good (ale) with the only supplier — the distributor's General Store — off-scene. The at-post " +
				"stabilizer must render its reconciled step-out form ('you are at your post … going to fetch it and " +
				"coming back is part of minding your trade'), NOT the bare 'stay and look after your work', because the " +
				"'## Restocking' section in the same prompt hands him a '(destination: general_store)' walk-to bullet. " +
				"Isolates the reconciliation from the salt-booster framing of cook_out_of_salt_vendor_stocked. " +
				"Cross-scenario guard: TestGoldensAtPostSteerNeverContradictsSupplyErrand.",
			build: vendorAtPostRestockSupplierOffscene,
		},
	)
}

// vendorAtPostRestockSupplierOffscene builds the LLM-491 restock-arm fixture: John
// Ellis on shift standing inside his own Ellis Tavern (WorkStructureID ==
// InsideStructureID, so the duty steer resolves to AtPost), out of ale he buys to
// resell (a `buy` restock entry at 0 on hand, below the reorder threshold), with
// the distributor-tagged General Store — Josiah's post, off-scene — holding ale.
// The distributor resolves as the walk-to supplier (isRestockSupplierOf admits a
// distributor-tagged structure without it producing the good), so "## Restocking"
// renders a "(destination: general_store)" bullet — exactly the off-scene errand
// the at-post stabilizer must reconcile with rather than contradict.
func vendorAtPostRestockSupplierOffscene() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		keeperID = sim.ActorID("john")
		josiahID = sim.ActorID("josiah")
		tavern   = sim.StructureID("ellis_tavern")
	)
	start, end := 360, 1080 // 06:00–18:00
	now := 600              // 10:00 — on shift
	keeper := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "John Ellis",
		Role:              "tavernkeeper",
		State:             sim.StateIdle,
		WorkStructureID:   tavern,
		InsideStructureID: tavern,
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             30,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"ale": 0},
		RestockPolicy: &sim.RestockPolicy{Restock: []sim.RestockEntry{
			{Item: "ale", Source: sim.RestockSourceBuy, Max: 6},
		}},
	}
	josiah := distributorKeeper(sim.TilePos{X: 41, Y: 40}, "")
	josiah.Inventory = map[sim.ItemKind]int{"ale": 6}
	vobjs, structs := distributorObjects()
	vobjs[sim.VillageObjectID(tavern)] = &sim.VillageObject{ID: sim.VillageObjectID(tavern), Pos: sim.WorldPos{X: 640, Y: 640}}
	structs[tavern] = plainStructure(tavern, "Ellis Tavern")
	snap := &sim.Snapshot{
		LocalMinuteOfDay: &now,
		NeedThresholds:   sim.NeedThresholds{},
		Actors:           map[sim.ActorID]*sim.ActorSnapshot{keeperID: keeper, josiahID: josiah},
		Structures:       structs,
		VillageObjects:   vobjs,
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"ale": {Name: "ale", DisplayLabel: "ale", DisplayLabelSingular: "mug of ale", DisplayLabelPlural: "ale", Category: sim.ItemCategoryFood, Capabilities: []string{"portable"}},
		},
		Recipes: map[sim.ItemKind]*sim.ItemRecipe{
			"ale": {OutputItem: "ale", OutputQty: 1, RateQty: 1, RatePerHours: 1, WholesalePrice: 2, RetailPrice: 3},
		},
		RestockReorderPct: 25,
	}
	return snap, keeperID, nil
}

// TestGoldensAtPostSteerNeverContradictsSupplyErrand is the LLM-491 cross-scenario
// invariant. Over every scenario in the matrix: whenever the subject is standing at
// its own post (the AtPost duty stabilizer) AND the rendered prompt names an
// off-scene buy supplier — the "  - buy from <place> (destination: <id>)" bullet
// renderWalkToVendors is the sole producer of, shared by all four supply cues —
// the prompt must NOT also carry the bare "stay and look after your work" steer.
// That pairing is the exact contradiction the ticket exists to remove: one section
// pins the keeper to his post while another hands him a place to walk to.
//
// Keyed on the RENDERED buy bullet, not on the SupplyErrand flag, on purpose: a
// future fifth at-post walk-to cue that renders a buy bullet but isn't threaded
// into hasAtPostSupplyErrand would leave the bare stay form standing beside it, and
// this fails loudly rather than shipping the contradiction. vendor_at_post_restock_
// supplier_offscene keeps it non-vacuous (an at-post subject with a buy bullet in
// the fixture).
func TestGoldensAtPostSteerNeverContradictsSupplyErrand(t *testing.T) {
	const bareStay = "stay and look after your work"
	for _, sc := range perceptionScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			snap, actorID, warrants := sc.build()
			p := Build(snap, actorID, warrants)
			if p.DutySteer == nil || !p.DutySteer.AtPost {
				return // invariant N/A — subject isn't standing at its post
			}
			out := combinedPrompt(Render(p, DefaultRenderConfig()))
			if !walkToBulletRE.MatchString(out) {
				return // no off-scene buy supplier named — nothing to contradict
			}
			if strings.Contains(out, bareStay) {
				t.Errorf("scenario %q: the at-post steer renders its bare 'stay and look after your work' form while an off-scene buy supplier is named in the same prompt — the LLM-491 contradiction:\n%s", sc.name, out)
			}
		})
	}
}

// TestHasWalkToSupplierMatchesRenderedBullet is the LLM-491 parity guard the
// cross-scenario invariant alone can't give: it pins each of the four
// HasWalkToSupplier() helpers against what its OWN renderer actually prints. The
// invariant only fires on a helper that returns false while a bullet renders (the
// bare stay form leaks); the OTHER drift — a helper returning true while NO bullet
// renders — merely flips the stabilizer to the permissive step-out form with
// nothing to step out for, and slips past the invariant silently. Each helper
// mirrors a different renderer's branch tree by hand, so each branch is a place the
// two can diverge. This table walks every branch and asserts helper == rendered.
func TestHasWalkToSupplierMatchesRenderedBullet(t *testing.T) {
	vendor := RestockVendor{StructureLabel: "The General Store", StructureID: "general_store"}

	t.Run("restocking", func(t *testing.T) {
		cases := []struct {
			name string
			view *RestockingView
		}{
			{"empty", &RestockingView{}},
			{"conserve", &RestockingView{BuyerCoins: 1, Conserve: true, Items: []RestockItemView{{ItemLabel: "ale", Cap: 6, Vendors: []RestockVendor{vendor}}}}},
			{"all_blocked", &RestockingView{Items: []RestockItemView{{ItemLabel: "ale", Blocked: []RestockBlockedSupplier{{StructureLabel: "The General Store", Reason: restockBlockShut}}}}}},
			{"pending_offer", &RestockingView{Items: []RestockItemView{{ItemLabel: "ale", Cap: 6, CoPresentSeller: "Josiah Thorne", PendingOfferToCoPresentSeller: true, Vendors: []RestockVendor{vendor}}}}},
			{"blocked_item", &RestockingView{Items: []RestockItemView{{ItemLabel: "ale", Blocked: []RestockBlockedSupplier{{StructureLabel: "The General Store", Reason: restockBlockNoMeans}}}}}},
			{"copresent_seller", &RestockingView{Items: []RestockItemView{{ItemLabel: "ale", Cap: 6, CoPresentSeller: "Josiah Thorne", SellerStock: 6}}}},
			{"walk_to_vendor", &RestockingView{Items: []RestockItemView{{ItemLabel: "ale", Cap: 6, Vendors: []RestockVendor{vendor}}}}},
			{"item_no_vendor_no_block", &RestockingView{Items: []RestockItemView{{ItemLabel: "ale", Cap: 6}}}},
			{"mixed_copresent_and_walk_to", &RestockingView{Items: []RestockItemView{
				{ItemLabel: "milk", Cap: 6, CoPresentSeller: "Josiah Thorne", SellerStock: 6},
				{ItemLabel: "ale", Cap: 6, Vendors: []RestockVendor{vendor}},
			}}},
		}
		for _, c := range cases {
			assertParity(t, c.name, c.view.HasWalkToSupplier(), func(b *strings.Builder) { renderRestocking(b, c.view) })
		}
	})

	t.Run("stall_repair", func(t *testing.T) {
		cases := []struct {
			name string
			view *StallRepairView
		}{
			{"hired", &StallRepairView{Hired: true, NailsNeeded: 5, NailVendors: []RestockVendor{vendor}}},
			{"enough_nails", &StallRepairView{NailsNeeded: 5, NailsHeld: 5, HasEnoughNails: true, NailVendors: []RestockVendor{vendor}}},
			{"makes_nails", &StallRepairView{NailsNeeded: 5, MakesNails: true, NailVendors: []RestockVendor{vendor}}},
			{"conserve", &StallRepairView{NailsNeeded: 5, Conserve: true, NailVendors: []RestockVendor{vendor}}},
			{"walk_to_vendor", &StallRepairView{NailsNeeded: 5, NailVendors: []RestockVendor{vendor}}},
			{"no_vendor", &StallRepairView{NailsNeeded: 5}},
		}
		for _, c := range cases {
			assertParity(t, c.name, c.view.HasWalkToSupplier(), func(b *strings.Builder) { renderStallRepair(b, c.view) })
		}
	})

	t.Run("farm_upkeep", func(t *testing.T) {
		cases := []struct {
			name string
			view *FarmUpkeepView
		}{
			{"copresent_seller", &FarmUpkeepView{ShovelsOwed: 2, ShovelsShort: 2, CoPresentSeller: "Ezekiel Crane", SellerStock: 2, ShovelVendors: []RestockVendor{vendor}}},
			{"walk_to_vendor", &FarmUpkeepView{ShovelsOwed: 2, ShovelsShort: 2, ShovelVendors: []RestockVendor{vendor}}},
			{"no_vendor", &FarmUpkeepView{ShovelsOwed: 2, ShovelsShort: 2}},
		}
		for _, c := range cases {
			assertParity(t, c.name, c.view.HasWalkToSupplier(), func(b *strings.Builder) { renderFarmUpkeep(b, c.view) })
		}
	})

	t.Run("hearth", func(t *testing.T) {
		cases := []struct {
			name string
			view *HearthView
		}{
			{"hired", &HearthView{Hired: true, WoodNeeded: 2, WoodVendors: []RestockVendor{vendor}}},
			{"enough_wood", &HearthView{WoodNeeded: 2, WoodHeld: 2, HasEnoughWood: true, WoodVendors: []RestockVendor{vendor}}},
			{"conserve", &HearthView{WoodNeeded: 2, Conserve: true, WoodVendors: []RestockVendor{vendor}}},
			{"walk_to_vendor", &HearthView{WoodNeeded: 2, WoodVendors: []RestockVendor{vendor}}},
			{"no_vendor", &HearthView{WoodNeeded: 2}},
		}
		for _, c := range cases {
			assertParity(t, c.name, c.view.HasWalkToSupplier(), func(b *strings.Builder) { renderHearth(b, c.view) })
		}
	})
}

// assertParity renders one view and requires HasWalkToSupplier()'s answer to equal
// whether a destination-bearing walk-to bullet actually appears in that render.
func assertParity(t *testing.T, name string, helper bool, render func(*strings.Builder)) {
	t.Helper()
	var b strings.Builder
	render(&b)
	rendered := walkToBulletRE.MatchString(b.String())
	if helper != rendered {
		t.Errorf("%s: HasWalkToSupplier()=%v but a walk-to bullet %s in the render:\n%s",
			name, helper, map[bool]string{true: "IS present", false: "is NOT present"}[rendered], b.String())
	}
}

// TestBuildStallRepairBuyNilAtPost pins the premise that lets hasAtPostSupplyErrand
// safely EXCLUDE StallRepairBuy (LLM-491): the off-post repair-buy section
// (buildStallRepairBuy) returns nil the moment the owner stands at its own worn
// business — sim.AtBusiness short-circuits it (stall_repair.go), because the at-post
// "## Your business" cue owns the buy there. AtPost in the duty steer likewise
// requires standing inside the workplace, so the two states are mutually exclusive
// and no StallRepairBuy walk-to bullet can ever accompany the at-post stabilizer. If
// this gate ever loosens, StallRepairBuy would need adding to hasAtPostSupplyErrand.
func TestBuildStallRepairBuyNilAtPost(t *testing.T) {
	start, end := 360, 1080
	now := 600
	zero := 0
	owner := &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Elizabeth Ellis",
		State:             sim.StateIdle,
		Pos:               sim.WorldPos{X: 100, Y: 100}.Tile(),
		InsideStructureID: "ellis_farm",
		WorkStructureID:   "ellis_farm",
		ScheduleStartMin:  &start,
		ScheduleEndMin:    &end,
		Coins:             39,
		Needs:             map[sim.NeedKey]int{},
		Inventory:         map[sim.ItemKind]int{"nail": 0},
	}
	smith := &sim.ActorSnapshot{
		Kind:            sim.KindNPCStateful,
		DisplayName:     "Ezekiel Crane",
		State:           sim.StateIdle,
		Pos:             sim.WorldPos{X: 2000, Y: 2000}.Tile(),
		WorkStructureID: "blacksmith",
		Inventory:       map[sim.ItemKind]int{"nail": 21},
		RestockPolicy:   producePolicy("nail", 40),
	}
	snap := &sim.Snapshot{
		LocalMinuteOfDay:          &now,
		NeedThresholds:            sim.NeedThresholds{},
		Assets:                    emptyAssetSet,
		StallWearRepairThreshold:  400,
		StallWearDegradeThreshold: 600,
		StallNailsPerRepair:       5,
		Actors:                    map[sim.ActorID]*sim.ActorSnapshot{"elizabeth": owner, "ezekiel": smith},
		Structures:                map[sim.StructureID]*sim.Structure{"ellis_farm": plainStructure("ellis_farm", "Ellis Farm"), "blacksmith": plainStructure("blacksmith", "Blacksmith")},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"ellis_farm": {ID: "ellis_farm", DisplayName: "Ellis Farm", Pos: sim.WorldPos{X: 100, Y: 100}, OwnerActorID: "elizabeth", Tags: []string{sim.TagBusiness}, Wear: 450, LoiterOffsetX: &zero, LoiterOffsetY: &zero},
		},
	}
	if v := buildStallRepairBuy(snap, "elizabeth", owner); v != nil {
		t.Fatalf("buildStallRepairBuy must be nil for an owner standing at its own worn business (the off-post section can't co-occur with AtPost), got %+v", v)
	}
}
