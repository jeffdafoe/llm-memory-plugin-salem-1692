package handlers_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/handlers"
)

// labor_reactor_test.go — LLM-187 coverage of handleLaborOfferReceivedWarrants
// (wired by RegisterLaborHandlers). Driven via the real sim.SolicitWork command
// so LaborOfferReceived emits through the production cascade path, mirroring
// pay_with_item_reactor_test.go. Reuses buildReactorWorld / readWarrants /
// firstByKind from that file (same handlers_test package). The pay handlers
// buildReactorWorld already wired ignore LaborOfferReceived, so the two coexist.

// armLaborWorker grants AttrWorker to the worker and registers the labor
// subscriber, on the world goroutine, BEFORE any solicitation (a subscriber
// registered after the emit would miss the event). registerTimes lets a test
// double-register to exercise the dedup interlock.
func armLaborWorker(t *testing.T, w *sim.World, workerID sim.ActorID, registerTimes int) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		worker := world.Actors[workerID]
		if worker != nil {
			if worker.Attributes == nil {
				worker.Attributes = map[string][]byte{}
			}
			worker.Attributes[sim.AttrWorker] = nil // presence-only marker
		}
		for i := 0; i < registerTimes; i++ {
			handlers.RegisterLaborHandlers(world)
		}
		return nil, nil
	}}); err != nil {
		t.Fatalf("armLaborWorker: %v", err)
	}
}

func solicitWork(t *testing.T, w *sim.World, workerID sim.ActorID, employerName string, reward, durationMin int) {
	t.Helper()
	if _, err := w.Send(sim.SolicitWork(workerID, employerName, reward, durationMin, time.Now().UTC())); err != nil {
		t.Fatalf("SolicitWork: %v", err)
	}
}

func countByKind(warrants []sim.WarrantMeta, kind sim.WarrantKind) int {
	n := 0
	for _, m := range warrants {
		if m.Kind() == kind {
			n++
		}
	}
	return n
}

// ====================================================================
// LaborOfferReceived subscriber
// ====================================================================

// TestSubscriber_LaborOfferReceived_StampsEmployer is the core LLM-187
// assertion: a worker's solicit_work warrants the EMPLOYER so their next
// reactor tick perceives the offer — without anyone speaking to wake them.
func TestSubscriber_LaborOfferReceived_StampsEmployer(t *testing.T) {
	w, stop := buildReactorWorld(t, []reactorActor{
		{id: "anne", displayName: "Anne", kind: sim.KindNPCShared, huddleID: "h1", coins: 20},
		{id: "prudence", displayName: "Prudence", kind: sim.KindNPCStateful, huddleID: "h1", coins: 50},
	})
	defer stop()
	armLaborWorker(t, w, "anne", 1)
	solicitWork(t, w, "anne", "Prudence", 5, 120)

	warrants := readWarrants(t, w, "prudence")
	m, ok := firstByKind(warrants, sim.WarrantKindLaborOffer)
	if !ok {
		t.Fatalf("employer carries no labor-offer warrant; got %d warrants: %+v", len(warrants), warrants)
	}
	reason, ok := m.Reason.(sim.LaborOfferWarrantReason)
	if !ok {
		t.Fatalf("warrant reason is %T, want LaborOfferWarrantReason", m.Reason)
	}
	if reason.Worker != "anne" || reason.Reward != 5 || reason.DurationMin != 120 {
		t.Errorf("reason = %+v, want worker anne / reward 5 / duration 30", reason)
	}
	if reason.LaborID == 0 {
		t.Errorf("reason LaborID is the unset sentinel 0")
	}
	if m.TriggerActorID != "anne" {
		t.Errorf("TriggerActorID = %q, want anne", m.TriggerActorID)
	}
}

// TestSubscriber_LaborOfferReceived_SkipsPCEmployer — a PC employer deliberates
// through the UI, not the reactor, so no warrant is stamped (defensive skip
// mirroring the pay reactor's non-NPC-seller skip). The offer still mints.
func TestSubscriber_LaborOfferReceived_SkipsPCEmployer(t *testing.T) {
	w, stop := buildReactorWorld(t, []reactorActor{
		{id: "anne", displayName: "Anne", kind: sim.KindNPCShared, huddleID: "h1", coins: 20},
		{id: "patron", displayName: "Patron", kind: sim.KindPC, huddleID: "h1", coins: 50},
	})
	defer stop()
	armLaborWorker(t, w, "anne", 1)
	solicitWork(t, w, "anne", "Patron", 5, 120)

	warrants := readWarrants(t, w, "patron")
	if _, ok := firstByKind(warrants, sim.WarrantKindLaborOffer); ok {
		t.Errorf("PC employer should carry no labor-offer warrant; got %+v", warrants)
	}
}

// TestSubscriber_LaborOfferReceived_DedupesDoubleRegister — registering the
// subscriber twice invokes it twice per event, but the DedupDiscriminator
// (uint64(LaborID)) collapses both stamps to one warrant.
func TestSubscriber_LaborOfferReceived_DedupesDoubleRegister(t *testing.T) {
	w, stop := buildReactorWorld(t, []reactorActor{
		{id: "anne", displayName: "Anne", kind: sim.KindNPCShared, huddleID: "h1", coins: 20},
		{id: "prudence", displayName: "Prudence", kind: sim.KindNPCStateful, huddleID: "h1", coins: 50},
	})
	defer stop()
	armLaborWorker(t, w, "anne", 2)
	solicitWork(t, w, "anne", "Prudence", 5, 120)

	warrants := readWarrants(t, w, "prudence")
	if got := countByKind(warrants, sim.WarrantKindLaborOffer); got != 1 {
		t.Errorf("labor-offer warrants = %d, want 1 (double-register must dedupe); warrants: %+v", got, warrants)
	}
}

// TestRegisterLaborHandlers_NilWorldPanics mirrors the pay-handler nil guard.
func TestRegisterLaborHandlers_NilWorldPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("RegisterLaborHandlers(nil) didn't panic")
		}
	}()
	handlers.RegisterLaborHandlers(nil)
}
