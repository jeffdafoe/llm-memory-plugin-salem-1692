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

// TestLaborOffer_UnaffordableEmployerAutoDeclinedNotWoken is the LLM-193
// counterpart to the wake test above: an employer who can't cover the asked
// reward must NOT be woken. Soliciting a broke keeper used to emit
// LaborOfferReceived and wake them for a full LLM tick that ended in "my purse
// is empty" — a tick burned on both sides for no hire. The solicit now
// auto-declines at mint without emitting the received event, so (i) no wake
// warrant, (ii) the offer resolves Declined, (iii) the worker earns the 12h
// seek-work drop memory, and (iv) the employer perceives no actionable offer.
func TestLaborOffer_UnaffordableEmployerAutoDeclinedNotWoken(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "lewis", displayName: "Lewis Walker", huddleID: "h1", worker: true},
		{id: "prudence", displayName: "Prudence Ward", huddleID: "h1", coins: 2, workStruct: "apothecary"},
	})
	defer stop()

	events := captureLaborEvents(t, w)

	// Wire both labor subscribers: the wake subscriber (proves the wake would
	// have fired for an affordable offer) and the declined-work memory subscriber
	// (main.go wires it separately from RegisterLaborHandlers).
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handlers.RegisterLaborHandlers(world)
		sim.RegisterDeclinedWorkSubscriber(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("register labor subscribers: %v", err)
	}

	// Worker asks 5 coins; the employer holds 2 — can't cover it.
	res, err := w.Send(sim.SolicitWork("lewis", "Prudence Ward", 5, 120, time.Now().UTC()))
	if err != nil {
		t.Fatalf("SolicitWork: %v", err)
	}
	if r := res.(sim.LaborSolicitResult); r.State != sim.LaborStateDeclined {
		t.Fatalf("unaffordable solicit State = %v, want Declined", r.State)
	}

	// (i) The employer is NOT woken — no LaborOfferReceived, so no wake warrant.
	if len(events.Received) != 0 {
		t.Errorf("unaffordable solicit emitted %d LaborOfferReceived; want 0 (employer must not be woken)", len(events.Received))
	}
	for _, k := range warrantKindsOf(t, w, "prudence") {
		if k == sim.WarrantKindLaborOffer {
			t.Errorf("employer carries a labor-offer warrant; a broke employer must not be woken")
		}
	}

	// (ii) The offer resolved Declined — exactly one terminal.
	if len(events.Resolved) != 1 || events.Resolved[0].TerminalState != sim.LaborTerminalStateDeclined {
		t.Fatalf("LaborResolved = %+v, want exactly one Declined terminal", events.Resolved)
	}

	// (iii) The worker learned to avoid the shop — the 12h ObservedDeclinedWork
	// memory is stamped on the employer's workplace, so buildSeekWorkPlaces drops it.
	stamped, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		_, ok := world.Actors["lewis"].Observed.At(sim.ObservedStateKey{StructureID: "apothecary", Condition: sim.ObservedDeclinedWork})
		return ok, nil
	}})
	if err != nil {
		t.Fatalf("read worker memory: %v", err)
	}
	if !stamped.(bool) {
		t.Errorf("worker has no ObservedDeclinedWork memory for the employer's workplace; the seek-work drop won't fire")
	}

	// (iv) The employer perceives no actionable offer — nothing to burn a tick on.
	p := perception.Build(w.Published(), "prudence", nil)
	if len(p.LaborOffersForMe) != 0 {
		t.Errorf("employer LaborOffersForMe = %d, want 0 (an auto-declined offer must not surface)", len(p.LaborOffersForMe))
	}
}

// TestLaborOffer_AffordableExactRewardStillWoken pins the boundary: an employer
// holding EXACTLY the asked reward can afford it (buyerCanAfford is coins >=
// amount), so the offer is a normal pending solicit and the employer is woken.
func TestLaborOffer_AffordableExactRewardStillWoken(t *testing.T) {
	w, stop := buildLaborWorld(t, "h1", "sc1", []laborActor{
		{id: "lewis", displayName: "Lewis Walker", huddleID: "h1", worker: true},
		{id: "prudence", displayName: "Prudence Ward", huddleID: "h1", coins: 5, workStruct: "apothecary"},
	})
	defer stop()

	events := captureLaborEvents(t, w)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handlers.RegisterLaborHandlers(world)
		return nil, nil
	}}); err != nil {
		t.Fatalf("RegisterLaborHandlers: %v", err)
	}

	res, err := w.Send(sim.SolicitWork("lewis", "Prudence Ward", 5, 120, time.Now().UTC()))
	if err != nil {
		t.Fatalf("SolicitWork: %v", err)
	}
	if r := res.(sim.LaborSolicitResult); r.State != sim.LaborStatePending {
		t.Fatalf("exact-affordable solicit State = %v, want Pending", r.State)
	}
	if len(events.Received) != 1 {
		t.Errorf("exact-affordable solicit emitted %d LaborOfferReceived; want 1 (employer woken)", len(events.Received))
	}
	woke := false
	for _, k := range warrantKindsOf(t, w, "prudence") {
		if k == sim.WarrantKindLaborOffer {
			woke = true
		}
	}
	if !woke {
		t.Errorf("employer not woken on an offer it can exactly afford")
	}
}
