package sim_test

import (
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// visitorSpawnNight is 02:00 in New York (06:00 UTC, July) — far outside the
// afternoon spawn window, where the cascade never brings anyone in.
var visitorSpawnNight = time.Date(2026, 7, 15, 6, 0, 0, 0, time.UTC)

// TestForceVisitorSpawn — the operator's arrival (LLM-790): a visitor walks in
// at night, past a full concurrent cap and with every spawn chance at 0, on
// the cascade's own arrival path (state stamped, walk to the tavern). A
// seller with no distributor to sell to arrives a passer-through; an unknown
// kind is refused.
func TestForceVisitorSpawn(t *testing.T) {
	vw := newVisitorWorld()
	tavernID := vw.seedTavern(t)
	w, cancel := vw.load(t)
	defer cancel()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Settings.VisitorMerchantTrickleChancePermille = 0
		world.Settings.VisitorMerchantCorrectionChancePermille = 0
		world.Settings.VisitorPasserSpawnChancePermille = 0
		world.Settings.VisitorMaxConcurrent = 1
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	// The cascade itself brings no one in at this hour.
	res, err := w.Send(sim.TickVisitorCascade(sim.VisitorTickInputs{Now: visitorSpawnNight, Rand: rand.New(rand.NewSource(1))}))
	if err != nil {
		t.Fatalf("TickVisitorCascade: %v", err)
	}
	if tm := res.(sim.VisitorCascadeTelemetry); tm.Spawned != 0 {
		t.Fatalf("the cascade spawned %d at night", tm.Spawned)
	}

	if _, err := w.Send(sim.ForceVisitorSpawn("gate", visitorSpawnNight, rand.New(rand.NewSource(1)))); !errors.Is(err, sim.ErrUnknownVisitorKind) {
		t.Errorf("kind gate: err = %v, want ErrUnknownVisitorKind", err)
	}
	spawn := func(kind string, seed int64) sim.ForcedVisitor {
		t.Helper()
		res, err := w.Send(sim.ForceVisitorSpawn(kind, visitorSpawnNight, rand.New(rand.NewSource(seed))))
		if err != nil {
			t.Fatalf("ForceVisitorSpawn(%q): %v", kind, err)
		}
		return res.(sim.ForcedVisitor)
	}
	first := spawn("", 42)
	if first.ID == "" || first.DisplayName == "" || first.Archetype == "" || first.Merchant {
		t.Errorf("passer = %+v, want a named passer-through", first)
	}
	// The cap is 1 and he fills it: the next still arrives.
	second := spawn(sim.ForceVisitorSeller, 7)
	if second.ID == first.ID || second.Merchant {
		t.Errorf("seller with no distributor = %+v, want a second visitor, a passer-through", second)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		for _, id := range []sim.ActorID{first.ID, second.ID} {
			a := world.Actors[id]
			if a == nil || a.VisitorState == nil {
				t.Errorf("%s is not a visitor in the world", id)
				continue
			}
			if a.VisitorState.ExpiresAt.IsZero() {
				t.Errorf("%s has no stay stamped", id)
			}
			if a.MoveIntent == nil || a.MoveIntent.Destination.StructureID == nil || *a.MoveIntent.Destination.StructureID != tavernID {
				t.Errorf("%s is not walking in to the tavern: %+v", id, a.MoveIntent)
			}
		}
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
}

// TestForceVisitorSpawnSeller — with a distributor to sell to, a forced seller
// arrives a merchant on a SELL errand to it, at night and with the trickle
// off: the forced roll's class and direction reach the errand binding.
func TestForceVisitorSpawnSeller(t *testing.T) {
	vw := newVisitorWorld()
	vw.seedTavern(t)
	distID := vw.seedDistributor(t)
	w, cancel := vw.load(t)
	defer cancel()
	res, err := w.Send(sim.ForceVisitorSpawn(sim.ForceVisitorSeller, visitorSpawnNight, rand.New(rand.NewSource(7))))
	if err != nil {
		t.Fatalf("ForceVisitorSpawn: %v", err)
	}
	got := res.(sim.ForcedVisitor)
	if !got.Merchant {
		t.Fatalf("seller = %+v, want a merchant", got)
	}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		trade := world.Actors[got.ID].VisitorState.Trade
		if trade == nil || trade.Direction != sim.TradeDirectionSell || trade.Counterparty != distID {
			t.Errorf("trade = %+v, want a sell errand to %s", trade, distID)
		}
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
}
