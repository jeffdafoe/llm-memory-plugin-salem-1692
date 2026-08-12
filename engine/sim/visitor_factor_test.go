package sim_test

import (
	"math/rand"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// visitor_factor_test.go — the wholesale factor spawn (end-to-end), now the SELL instance of a
// merchant errand (LLM-455, generalizing LLM-410). Forces the coin-valve to sell (sell weight
// 1000) and disables passers-through (chance 0), drives one cascade tick, then asserts the
// factor-specific spawn wiring: a sell TradeErrand to the distributor, the Boston origin, the
// clothing/charm pack + heavy purse, and the distributor-targeted arrival walk (not the tavern).

// seedDistributor adds a distributor-tagged structure to the visitor world so a factor has a
// target to walk to. Placed interior on the all-dirt terrain so the edge-tile picker connects.
func (vw *visitorWorld) seedDistributor(t *testing.T) sim.StructureID {
	t.Helper()
	const id = "general_store"
	vw.handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"store-asset": {ID: "store-asset", Category: "structure", DoorOffsetX: intpV(1), DoorOffsetY: intpV(2)},
	})
	vw.handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		id: {
			ID:          id,
			AssetID:     "store-asset",
			Pos:         sim.WorldPos{X: 160, Y: 160},
			EntryPolicy: sim.EntryPolicyOpen,
			Tags:        []string{sim.TagDistributor},
		},
	})
	vw.handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		id: {ID: id, DisplayName: "The General Store"},
	})
	return id
}

func TestTickVisitorCascade_FactorSpawn(t *testing.T) {
	vw := newVisitorWorld()
	vw.seedTavern(t) // the ordinary anchor — the factor should NOT target this
	distID := vw.seedDistributor(t)
	w, cancel := vw.load(t)
	defer cancel()

	// Force the coin-valve to a SELLER and disable passers-through so the spawn is
	// deterministically a factor (LLM-455). A distributor is placed so bindSellErrand succeeds.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.VisitorMerchantTrickleChancePermille = 1000
		world.Settings.VisitorMaxConcurrent = 2
		world.Settings.VisitorSellWeightPermille = 1000
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	r := rand.New(rand.NewSource(7))
	res, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: visitorSpawnDaytime, Rand: r}))
	if err != nil {
		t.Fatalf("TickVisitorCascade: %v", err)
	}
	if tm := res.(sim.VisitorCascadeTelemetry); tm.Spawned != 1 {
		t.Fatalf("spawned = %d, want 1 (factor forced to land)", tm.Spawned)
	}

	snap := w.Published()
	var got *sim.ActorSnapshot
	for _, a := range snap.Actors {
		if a.VisitorState != nil {
			got = a
			break
		}
	}
	if got == nil {
		t.Fatal("no visitor in snapshot after factor spawn")
	}
	if got.VisitorState.Trade == nil {
		t.Fatal("factor spawned without a Trade errand")
	}
	if got.VisitorState.Trade.Direction != sim.TradeDirectionSell {
		t.Errorf("errand direction = %q, want sell", got.VisitorState.Trade.Direction)
	}
	if got.VisitorState.Trade.Counterparty != distID {
		t.Errorf("errand counterparty = %q, want distributor %q", got.VisitorState.Trade.Counterparty, distID)
	}
	if got.VisitorState.Archetype != "factor" {
		t.Errorf("archetype = %q, want factor", got.VisitorState.Archetype)
	}
	if got.VisitorState.Origin != "Boston" {
		t.Errorf("origin = %q, want Boston (forced for a factor)", got.VisitorState.Origin)
	}
	// Factor pack: at least one clothing/charm ware kind, and the heavier purse (>= min 120).
	if got.Inventory["coat"] == 0 && got.Inventory["cloak"] == 0 && got.Inventory["homespun"] == 0 {
		t.Errorf("factor pack carries no garments: %v", got.Inventory)
	}
	if got.Coins < sim.DefaultVisitorFactorPurseMin {
		t.Errorf("factor purse = %d, want >= %d (heavier than an ordinary traveler)", got.Coins, sim.DefaultVisitorFactorPurseMin)
	}

	// Distributor-targeted arrival: the walk goes to the distributor, not the tavern.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		for _, a := range world.Actors {
			if a.VisitorState == nil {
				continue
			}
			if a.MoveIntent == nil {
				t.Error("spawned factor has no MoveIntent")
				return nil, nil
			}
			if a.MoveIntent.Destination.StructureID == nil || *a.MoveIntent.Destination.StructureID != distID {
				t.Errorf("factor MoveIntent dest = %+v, want distributor StructureID=%q", a.MoveIntent.Destination, distID)
			}
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("MoveIntent check: %v", err)
	}
}

// TestTickVisitorCascade_CorrectionForcesFactor — the LLM-626 correction roll end to end:
// resident coin above the high band spawns a FORCED seller off the correction chance, with the
// trickle roll off and the sell weight at 0 — proving the direction came from the band, not the
// weighted random (which at 0 would have produced a buyer had the trickle path been taken).
func TestTickVisitorCascade_CorrectionForcesFactor(t *testing.T) {
	vw := newVisitorWorld()
	vw.seedTavern(t)
	distID := vw.seedDistributor(t)
	w, cancel := vw.load(t)
	defer cancel()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["rich_resident"] = &sim.Actor{ID: "rich_resident", Kind: sim.KindNPCShared, DisplayName: "Rich Resident", Coins: 500}
		world.Settings.VisitorCoinBandLow = 10
		world.Settings.VisitorCoinBandHigh = 100
		world.Settings.VisitorMerchantCorrectionChancePermille = 1000
		world.Settings.VisitorMerchantTrickleChancePermille = 0
		world.Settings.VisitorSellWeightPermille = 0
		world.Settings.VisitorMaxConcurrent = 2
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	r := rand.New(rand.NewSource(7))
	res, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: visitorSpawnDaytime, Rand: r}))
	if err != nil {
		t.Fatalf("TickVisitorCascade: %v", err)
	}
	if tm := res.(sim.VisitorCascadeTelemetry); tm.Spawned != 1 {
		t.Fatalf("spawned = %d, want 1 (correction roll at 1000 must land)", tm.Spawned)
	}
	snap := w.Published()
	for _, a := range snap.Actors {
		if a.VisitorState == nil {
			continue
		}
		if a.VisitorState.Trade == nil || a.VisitorState.Trade.Direction != sim.TradeDirectionSell {
			t.Fatalf("corrective spawn errand = %+v, want a forced SELL to %q", a.VisitorState.Trade, distID)
		}
		return
	}
	t.Fatal("no visitor in snapshot after corrective spawn")
}

// TestTickVisitorCascade_ReturnerDoesNotPreemptCorrection — a due returner comes back as a
// passer-through, so letting it ride a CORRECTIVE spawn would silently swallow the band's
// correction (LLM-626). The corrective spawn must produce the errand-bound merchant; the
// returner stays due for the next trickle or flavor slot.
func TestTickVisitorCascade_ReturnerDoesNotPreemptCorrection(t *testing.T) {
	vw := newVisitorWorld()
	vw.seedTavern(t)
	vw.seedDistributor(t)
	w, cancel := vw.load(t)
	defer cancel()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["rich_resident"] = &sim.Actor{ID: "rich_resident", Kind: sim.KindNPCShared, DisplayName: "Rich Resident", Coins: 500}
		world.RecurringVisitors = map[sim.RecurringVisitorID]*sim.RecurringVisitor{
			"rvis-0000dddd": {ID: "rvis-0000dddd", Name: "Obadiah Pratt", Archetype: "circuit preacher",
				NextReturnAt: visitorSpawnDaytime.Add(-time.Hour)},
		}
		world.Settings.VisitorCoinBandLow = 10
		world.Settings.VisitorCoinBandHigh = 100
		world.Settings.VisitorMerchantCorrectionChancePermille = 1000
		world.Settings.VisitorSellWeightPermille = 0
		world.Settings.VisitorMaxConcurrent = 2
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	r := rand.New(rand.NewSource(7))
	if _, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: visitorSpawnDaytime, Rand: r})); err != nil {
		t.Fatalf("TickVisitorCascade: %v", err)
	}
	snap := w.Published()
	for _, a := range snap.Actors {
		if a.VisitorState == nil {
			continue
		}
		if a.VisitorState.RecurringID != "" || a.VisitorState.Trade == nil {
			t.Fatalf("corrective spawn = returner %q / trade %+v — the returner must not preempt a correction",
				a.VisitorState.RecurringID, a.VisitorState.Trade)
		}
		return
	}
	t.Fatal("no visitor in snapshot after corrective spawn")
}

// TestTickVisitorCascade_PasserFlowIndependent — the LLM-626 flavor roll spawns a passer-through
// with BOTH merchant chances at 0: the flavor flow no longer needs the merchant pipeline to fire.
func TestTickVisitorCascade_PasserFlowIndependent(t *testing.T) {
	vw := newVisitorWorld()
	vw.seedTavern(t)
	w, cancel := vw.load(t)
	defer cancel()

	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.VisitorPasserSpawnChancePermille = 1000
		world.Settings.VisitorMaxConcurrent = 2
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	r := rand.New(rand.NewSource(7))
	res, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: visitorSpawnDaytime, Rand: r}))
	if err != nil {
		t.Fatalf("TickVisitorCascade: %v", err)
	}
	if tm := res.(sim.VisitorCascadeTelemetry); tm.Spawned != 1 {
		t.Fatalf("spawned = %d, want 1 (passer roll at 1000 must land)", tm.Spawned)
	}
	snap := w.Published()
	for _, a := range snap.Actors {
		if a.VisitorState == nil {
			continue
		}
		if a.VisitorState.Trade != nil {
			t.Fatalf("passer spawn carries errand %+v, want none", a.VisitorState.Trade)
		}
		return
	}
	t.Fatal("no visitor in snapshot after passer spawn")
}
