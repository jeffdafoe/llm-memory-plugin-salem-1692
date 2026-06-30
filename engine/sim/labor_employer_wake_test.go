package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/perception"
)

// labor_employer_wake_test.go — LLM-187. The end-to-end counterpart to the
// handler-level reactor tests: drive the full production path (RegisterLabor
// Handlers -> SolicitWork -> World.Published() -> perception.Build) and assert
// the employer is BOTH woken (carries the labor-offer warrant) AND perceives the
// actionable offer (the ledger-backed "## Work offers awaiting your decision"
// view, with the labor_id the employer echoes into accept_work). Reuses
// buildLaborWorld from labor_commands_test.go.
//
// This is the regression for the live confabulated-hire bug: before the wake
// warrant, a solicitation made into a conversational lull left the employer
// dormant through the 3-min TTL and accept_work never fired.

func warrantKindsOf(t *testing.T, w *sim.World, id sim.ActorID) []sim.WarrantKind {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		if a == nil {
			return []sim.WarrantKind(nil), nil
		}
		ks := make([]sim.WarrantKind, 0, len(a.Warrants))
		for _, m := range a.Warrants {
			ks = append(ks, m.Kind())
		}
		return ks, nil
	}})
	if err != nil {
		t.Fatalf("warrantKindsOf: %v", err)
	}
	return res.([]sim.WarrantKind)
}

func TestLaborOffer_WakesEmployerAndSurfacesOffer(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "anne", displayName: "Anne Walker", huddleID: "h1", worker: true},
		{id: "prudence", displayName: "Prudence Ward", huddleID: "h1", coins: 50},
	})
	defer stop()

	// Wire the LLM-187 subscriber before the solicit emits its event.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handlers.RegisterLaborHandlers(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("RegisterLaborHandlers: %v", err)
	}

	res, err := w.Send(sim.SolicitWork("anne", "Prudence Ward", 5, 120, time.Now().UTC()))
	if err != nil {
		t.Fatalf("SolicitWork: %v", err)
	}
	laborID := res.(sim.LaborSolicitResult).ID

	// (i) The wake — the employer carries a labor-offer warrant, so the reactor
	// will give them a tick WITHOUT anyone needing to speak again.
	kinds := warrantKindsOf(t, w, "prudence")
	woke := false
	for _, k := range kinds {
		if k == sim.WarrantKindLaborOffer {
			woke = true
		}
	}
	if !woke {
		t.Fatalf("employer not woken: no labor-offer warrant; kinds = %v", kinds)
	}

	// (ii) The perceive — on that woken tick the employer's perception surfaces
	// the actionable offer (built live off snap.LaborLedger), carrying the
	// labor_id accept_work needs.
	p := perception.Build(w.Published(), "prudence", nil)
	if len(p.LaborOffersForMe) != 1 {
		t.Fatalf("employer LaborOffersForMe = %d, want 1", len(p.LaborOffersForMe))
	}
	off := p.LaborOffersForMe[0]
	if off.LaborID != laborID {
		t.Errorf("offer view LaborID = %d, want %d (the id accept_work echoes)", off.LaborID, laborID)
	}
	if off.Worker != "anne" || off.Reward != 5 || off.DurationMin != 120 {
		t.Errorf("offer view = %+v, want worker anne / reward 5 / duration 120", off)
	}
}
