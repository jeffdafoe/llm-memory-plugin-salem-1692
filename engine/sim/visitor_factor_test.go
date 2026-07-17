package sim_test

import (
	"math/rand"
	"testing"

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
		world.Settings.VisitorSpawnChancePermille = 1000
		world.Settings.VisitorMaxConcurrent = 2
		world.Settings.VisitorSellWeightPermille = 1000
		world.Settings.VisitorPasserThroughChancePermille = 0
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
	if got.Inventory["coat"] == 0 && got.Inventory["cloak"] == 0 && got.Inventory["gown"] == 0 {
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
