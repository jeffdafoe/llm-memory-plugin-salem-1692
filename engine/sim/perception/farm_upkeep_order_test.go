package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// farm_upkeep_order_test.go — LLM-518. The "## Farm upkeep" cue must not goad a
// purchase that is already on order.
//
// The live failure (Elizabeth Ellis, 2026-07-24): she owed 2 upkeep shovels, held
// 1, and had a Ready order for 2 more from Ezekiel Crane awaiting hand-over. The
// upkeep cue counted only physical inventory (buildFarmUpkeep gated on owed > held),
// so the shortfall never cleared while she waited, and the cue — which rides any
// tick — re-nagged "buy a fresh shovel from the blacksmith" every turn. That pull to
// the smith fought the return-to-post duty steer, and she ping-ponged Ellis Farm ↔
// Blacksmith on a ~2-minute cycle for over half an hour.
//
// The fix nets open incoming shovel orders against the shortfall: any on order and
// the cue states the scene ("with N more on order from the blacksmith") and stops —
// no buy imperative, no vendor/co-present steer. With no errand on the view, the
// return-to-post steer walks her back to the farm to wait.
//
// farmOwnerOwesUpkeepOrderInFlight reproduces the live shape: off her post, owes 2 /
// holds 1, a Ready 2-shovel order from Ezekiel who ALSO exists as a resolvable
// supplier of record — so absent the fix the cue would name his move_to destination.
// The golden pins the facts-only upkeep line (no "Buy", no "(destination:") AND the
// to-work steer still firing.
func farmOwnerOwesUpkeepOrderInFlight() (*sim.Snapshot, sim.ActorID, []sim.WarrantMeta) {
	const (
		elizabethID = sim.ActorID("elizabeth")
		ezekielID   = sim.ActorID("ezekiel")
		farm        = sim.StructureID("ellis_farm")
		forge       = sim.StructureID("blacksmith")
		orderID     = sim.OrderID(2539)
	)
	zero := 0
	start, end := 360, 1080 // 06:00–18:00
	minute := 600           // 10:00 — on shift
	// Fixed clock so the order-expiry clause renders deterministically (RenderedAt =
	// PublishedAt; the harness re-renders and requires byte equality).
	published := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	today := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)

	elizabeth := &sim.ActorSnapshot{
		Kind:             sim.KindNPCShared,
		DisplayName:      "Elizabeth Ellis",
		Role:             "farmer",
		State:            sim.StateIdle,
		Pos:              sim.WorldPos{X: 1000, Y: 1000}.Tile(), // off her farm
		WorkStructureID:  farm,
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		Coins:            72, // floor 30, band 20 → owes floor((72-30)/20) = 2
		Needs:            map[sim.NeedKey]int{},
		Inventory:        map[sim.ItemKind]int{"shovel": 1}, // holds 1 → short 1
	}
	// A supplier of record: far from Elizabeth (not co-present) and PRODUCES shovels,
	// so findItemVendors would resolve him and name a destination — absent the fix,
	// the cue would send her to buy. The order short-circuit must win over that.
	ezekiel := &sim.ActorSnapshot{
		Kind:             sim.KindNPCStateful,
		DisplayName:      "Ezekiel Crane",
		Role:             "blacksmith",
		State:            sim.StateIdle,
		Pos:              sim.WorldPos{X: 2000, Y: 2000}.Tile(),
		ScheduleStartMin: &start,
		ScheduleEndMin:   &end,
		WorkStructureID:  forge,
		Coins:            0,
		Needs:            map[sim.NeedKey]int{},
		Inventory:        map[sim.ItemKind]int{"shovel": 12},
		RestockPolicy:    producePolicy("shovel", 40),
	}

	// The Ready order the smith owes her — 2 shovels, not yet forged, so they sit in
	// his hands until deliver_order. Within its window (ReadyBy today) so it renders
	// under "## Orders you're waiting on", not "## Overdue".
	order := &sim.Order{
		ID: orderID, State: sim.OrderStateReady,
		BuyerID: elizabethID, SellerID: ezekielID,
		Item: "shovel", Qty: 2, Amount: 12,
		ConsumerIDs: []sim.ActorID{elizabethID},
		LedgerID:    sim.LedgerID(2539),
		CreatedAt:   published.Add(-2 * time.Minute),
		ReadyBy:     today,
		ExpiresAt:   published.Add(900 * time.Minute),
	}

	snap := &sim.Snapshot{
		LocalMinuteOfDay:         &minute,
		PublishedAt:              published,
		LocalDateUTC:             today,
		NeedThresholds:           sim.NeedThresholds{},
		Assets:                   emptyAssetSet,
		FarmUpkeepFloor:          30,
		FarmUpkeepCoinsPerShovel: 20,
		Actors:                   map[sim.ActorID]*sim.ActorSnapshot{elizabethID: elizabeth, ezekielID: ezekiel},
		Structures: map[sim.StructureID]*sim.Structure{
			farm:  plainStructure(farm, "Ellis Farm"),
			forge: plainStructure(forge, "Blacksmith"),
		},
		VillageObjects: map[sim.VillageObjectID]*sim.VillageObject{
			"ellis_farm": {
				ID:            "ellis_farm",
				DisplayName:   "Ellis Farm",
				Pos:           sim.WorldPos{X: 100, Y: 100},
				OwnerActorID:  elizabethID,
				Tags:          []string{sim.TagFarm},
				LoiterOffsetX: &zero,
				LoiterOffsetY: &zero,
			},
		},
		ItemKinds: map[sim.ItemKind]*sim.ItemKindDef{
			"shovel": {Name: "shovel", DisplayLabel: "shovel", DisplayLabelSingular: "shovel", DisplayLabelPlural: "shovels", Category: sim.ItemCategory("tool")},
		},
		Orders: map[sim.OrderID]*sim.Order{orderID: order},
	}
	return snap, elizabethID, nil
}

func init() {
	perceptionScenarios = append(perceptionScenarios,
		perceptionScenario{
			name: "farm_owner_owes_upkeep_order_in_flight",
			summary: "LLM-518: Elizabeth Ellis owes 2 upkeep shovels and holds 1, with a Ready 2-shovel order from " +
				"Ezekiel Crane already in flight, while off her post. Ezekiel ALSO exists as a resolvable shovel " +
				"supplier, so absent the fix the '## Farm upkeep' cue would name his move_to destination and pull " +
				"her to the smith to buy a shovel she has already ordered — the farm↔blacksmith oscillation. The " +
				"golden pins the on-order facts-only line ('Upkeep calls for 2 shovels and you carry 1, with 2 " +
				"more on order from the blacksmith.') with NO buy imperative and NO destination, and the " +
				"return-to-post steer still firing. Foil of farm_owner_owes_upkeep_with_shovel_supplier (same " +
				"owing owner + supplier, no order → the buy-from-destination steer correctly stands).",
			build: farmOwnerOwesUpkeepOrderInFlight,
		},
	)
}

// TestBuildFarmUpkeep_OrderInFlight_FactsOnly is the focused counterpart to the
// cross-scenario invariant: it asserts buildFarmUpkeep nets the open shovel order
// against the shortfall (OnOrder set, no vendor/co-present errand on the view) and
// renderFarmUpkeep emits the facts-only line with no buy imperative or destination.
func TestBuildFarmUpkeep_OrderInFlight_FactsOnly(t *testing.T) {
	snap, actorID, _ := farmOwnerOwesUpkeepOrderInFlight()
	v := buildFarmUpkeep(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected a farm-upkeep cue for an owing owner with an order in flight, got nil")
	}
	if v.OnOrder != 2 {
		t.Errorf("OnOrder = %d, want 2 (the Ready order qty)", v.OnOrder)
	}
	if v.ShovelsShort != 1 {
		t.Errorf("ShovelsShort = %d, want 1 (owed 2, held 1)", v.ShovelsShort)
	}
	// The on-order view carries no errand — this is what lets the return-to-post
	// steer fire instead of the upkeep cue re-pulling her to the smith.
	if len(v.ShovelVendors) != 0 {
		t.Errorf("ShovelVendors = %v, want empty (order in flight suppresses the walk-to steer)", v.ShovelVendors)
	}
	if v.CoPresentSeller != "" {
		t.Errorf("CoPresentSeller = %q, want empty", v.CoPresentSeller)
	}
	if v.HasWalkToSupplier() {
		t.Error("HasWalkToSupplier() = true, want false (no walk-to errand when the order is in flight)")
	}

	out := renderUpkeep(v)
	if !strings.Contains(out, "with 2 more on order from the blacksmith") {
		t.Errorf("upkeep cue missing the on-order scene line:\n%s", out)
	}
	for _, bad := range []string{"Buy ", "(destination:"} {
		if strings.Contains(out, bad) {
			t.Errorf("upkeep cue still carries the buy steer %q — it should be facts-only when on order:\n%s", bad, out)
		}
	}
}

// TestFarmUpkeep_OnOrder_FactsOnlyRegardlessOfCoverage pins the LLM-518 policy
// (Jeff's call): ANY open shovel order makes the cue facts-only — no buy imperative,
// no destination — whether or not the order covers the full shortfall. The rendered
// line always shows owed / carried / on-order, so a partly-covered shortfall stays
// legible (the model reads the remainder). This is the deliberate alternative to a
// net-shortfall gate (code_review LLM-518): a purchase can't be usefully advertised
// for a good already on order from its sole producer, and re-advertising it every tick
// is what produced the oscillation. The order count in the line is the true on-order
// quantity, even when it exceeds the shortfall.
func TestFarmUpkeep_OnOrder_FactsOnlyRegardlessOfCoverage(t *testing.T) {
	cases := []struct {
		name        string
		coins, held int
		orderQty    int
		wantOwed    int
		wantLine    string
	}{
		{"full_cover_live_shape", 72, 1, 2, 2, "Upkeep calls for 2 shovels and you carry 1, with 2 more on order from the blacksmith."},
		{"partial_cover_still_short", 95, 1, 1, 3, "Upkeep calls for 3 shovels and you carry 1, with 1 more on order from the blacksmith."},
		{"full_cover_held_none", 95, 0, 3, 3, "Upkeep calls for 3 shovels and you carry none, with 3 on order from the blacksmith."},
		{"partial_cover_held_none", 95, 0, 1, 3, "Upkeep calls for 3 shovels and you carry none, with 1 on order from the blacksmith."},
		{"order_exceeds_shortfall", 72, 1, 5, 2, "Upkeep calls for 2 shovels and you carry 1, with 5 more on order from the blacksmith."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snap, actorID, _ := farmUpkeepSnapshot(c.coins, c.held, 30, 20)
			snap.Orders = map[sim.OrderID]*sim.Order{
				1: {
					ID: 1, State: sim.OrderStateReady,
					BuyerID: actorID, SellerID: sim.ActorID("ezekiel"),
					Item: sim.ShovelItemKind, Qty: c.orderQty,
					ConsumerIDs: []sim.ActorID{actorID},
				},
			}
			v := buildFarmUpkeep(snap, actorID, snap.Actors[actorID])
			if v == nil {
				t.Fatalf("expected a farm-upkeep cue (owed %d, held %d, order %d)", c.wantOwed, c.held, c.orderQty)
			}
			if v.OnOrder != c.orderQty {
				t.Errorf("OnOrder = %d, want %d (the true on-order count, not clamped to the shortfall)", v.OnOrder, c.orderQty)
			}
			if v.ShovelsOwed != c.wantOwed {
				t.Errorf("ShovelsOwed = %d, want %d", v.ShovelsOwed, c.wantOwed)
			}
			out := renderUpkeep(v)
			if !strings.Contains(out, c.wantLine) {
				t.Errorf("cue line mismatch.\n got: %q\nwant substring: %q", out, c.wantLine)
			}
			for _, bad := range []string{"Buy ", "(destination:"} {
				if strings.Contains(out, bad) {
					t.Errorf("on-order cue must be facts-only regardless of coverage, but carries %q:\n%s", bad, out)
				}
			}
		})
	}
}

// TestFarmUpkeep_OrderFromOtherBuyer_DoesNotSuppress guards the BuyerID scoping: an
// order someone ELSE placed (even for shovels) must not suppress this owner's upkeep
// buy cue. openIncomingOrderQty counts only orders where the owner is the buyer.
func TestFarmUpkeep_OrderFromOtherBuyer_DoesNotSuppress(t *testing.T) {
	snap, actorID, _ := farmUpkeepSnapshot(72, 1, 30, 20) // owed 2, held 1, short 1
	snap.Orders = map[sim.OrderID]*sim.Order{
		1: {
			ID: 1, State: sim.OrderStateReady,
			BuyerID: sim.ActorID("someone_else"), SellerID: sim.ActorID("ezekiel"),
			Item: sim.ShovelItemKind, Qty: 5,
			ConsumerIDs: []sim.ActorID{sim.ActorID("someone_else")},
		},
	}
	v := buildFarmUpkeep(snap, actorID, snap.Actors[actorID])
	if v == nil {
		t.Fatal("expected the upkeep cue to still render — the order belongs to another buyer")
	}
	if v.OnOrder != 0 {
		t.Errorf("OnOrder = %d, want 0 (another buyer's order must not count)", v.OnOrder)
	}
	// With no order of her own, the ordinary buy steer must return (generic fallback
	// here — no supplier seeded, so "Buy … from the blacksmith"), NOT the facts-only
	// on-order line.
	out := renderUpkeep(v)
	if !strings.Contains(out, "Buy ") {
		t.Errorf("expected the ordinary buy cue when the owner has no order of her own:\n%s", out)
	}
	if strings.Contains(out, "on order") {
		t.Errorf("another buyer's order must not flip the cue to the on-order line:\n%s", out)
	}
}

// TestFarmUpkeep_IgnoresNonPositiveOrderQty makes the openIncomingOrderQty guard
// explicit (code_review LLM-518): a malformed order with Qty <= 0 is skipped before
// the saturation math, so it can neither reduce the total nor make the MaxInt-o.Qty
// overflow guard itself overflow. Such rows contribute nothing and the cue falls
// through to the ordinary buy steer rather than rendering a phantom "on order".
func TestFarmUpkeep_IgnoresNonPositiveOrderQty(t *testing.T) {
	snap, actorID, _ := farmUpkeepSnapshot(72, 1, 30, 20) // owed 2, held 1
	snap.Orders = map[sim.OrderID]*sim.Order{
		1: {ID: 1, State: sim.OrderStateReady, BuyerID: actorID, SellerID: sim.ActorID("ezekiel"), Item: sim.ShovelItemKind, Qty: 0, ConsumerIDs: []sim.ActorID{actorID}},
		2: {ID: 2, State: sim.OrderStateReady, BuyerID: actorID, SellerID: sim.ActorID("ezekiel"), Item: sim.ShovelItemKind, Qty: -3, ConsumerIDs: []sim.ActorID{actorID}},
	}
	if got := openIncomingOrderQty(snap, actorID, sim.ShovelItemKind); got != 0 {
		t.Errorf("openIncomingOrderQty = %d, want 0 (non-positive quantities must be ignored)", got)
	}
	v := buildFarmUpkeep(snap, actorID, snap.Actors[actorID])
	if v == nil || v.OnOrder != 0 {
		t.Fatalf("expected the ordinary cue with OnOrder 0; got %+v", v)
	}
	if strings.Contains(renderUpkeep(v), "on order") {
		t.Errorf("a non-positive-qty order must not render the on-order line:\n%s", renderUpkeep(v))
	}
}

// TestGoldensFarmUpkeepOnOrderNoBuyGoad is the LLM-518 cross-scenario invariant:
// whenever the "## Farm upkeep" cue renders AND the actor has an open incoming shovel
// order, the section must carry no buy imperative ("Buy ") and no walk-to destination
// ("(destination:") — the goods are already ordered, so the cue states the scene and
// nothing more. Keyed off the same openIncomingOrderQty the production build uses;
// farm_owner_owes_upkeep_order_in_flight is the non-vacuous anchor.
func TestGoldensFarmUpkeepOnOrderNoBuyGoad(t *testing.T) {
	exercised := false
	for _, sc := range perceptionScenarios {
		sc := sc
		snap, actorID, _ := sc.build()
		if openIncomingOrderQty(snap, actorID, sim.ShovelItemKind) == 0 {
			continue // no shovel order in flight — invariant N/A here
		}
		out := renderScenario(sc)
		if !strings.Contains(out, "## Farm upkeep") {
			continue // no upkeep cue in this prompt
		}
		exercised = true
		section := promptSection(out, "## Farm upkeep")
		for _, bad := range []string{"Buy ", "(destination:"} {
			if strings.Contains(section, bad) {
				t.Errorf("scenario %q: the farm-upkeep cue carries %q while a shovel order is already in flight — it must state the scene, not goad a second purchase (LLM-518):\n%s", sc.name, bad, section)
			}
		}
	}
	if !exercised {
		t.Error("matrix must exercise a farm owner who owes upkeep with a shovel order in flight (the '## Farm upkeep' cue beside an open incoming shovel order) so the on-order invariant isn't vacuous (LLM-518)")
	}
}
