package cascade

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// TestForceRouteCommand_DispatchesCrierTour proves the operator lever behind the
// umbilical POST /route endpoint: ForceRouteCommand builds and dispatches the
// town crier's board tour on demand — no schedule boundary required. This is the
// in-test mirror of the live "reproduce the crier cycle" trigger.
func TestForceRouteCommand_DispatchesCrierTour(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w, sim.AttrTownCrier)
	seedRunnerSchedule(w, 540, 1080) // 9:00–18:00 (irrelevant to the forcer — it ignores the gate)
	RegisterNPCRoutes(context.Background(), w, llm.NewFakeClient())
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	res, err := w.Send(ForceRouteCommand(sim.AttrTownCrier, false))
	if err != nil {
		t.Fatalf("ForceRouteCommand: %v", err)
	}
	out, ok := res.(sim.StartNPCRouteResult)
	if !ok {
		t.Fatalf("result type = %T, want sim.StartNPCRouteResult", res)
	}
	if out.NPCID != "runner" || out.Label != sim.AttrTownCrier {
		t.Errorf("result NPCID=%q Label=%q, want runner / %q", out.NPCID, out.Label, sim.AttrTownCrier)
	}
	if out.Stops < 1 {
		t.Errorf("forced crier route Stops = %d, want >= 1 (the notice board)", out.Stops)
	}
	if states := routeStopStates(t, w); len(states) == 0 {
		t.Error("no active route after ForceRouteCommand; expected the board route installed")
	}
}

// TestForceRouteCommand_NoCarrierErrors confirms forcing a route nobody carries
// surfaces an error (→ 422 at the umbilical handler) rather than silently
// no-op'ing — the runner has no town_crier attribute seeded.
func TestForceRouteCommand_NoCarrierErrors(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	RegisterNPCRoutes(context.Background(), w, llm.NewFakeClient())
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	if _, err := w.Send(ForceRouteCommand(sim.AttrTownCrier, false)); err == nil {
		t.Fatal("ForceRouteCommand with no carrier: want error, got nil")
	}
}

// TestForceRouteCommand_UnknownAttrErrors confirms a non-route attribute is
// rejected (defensive — the umbilical handler validates first, but the command
// must not silently dispatch garbage).
func TestForceRouteCommand_UnknownAttrErrors(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w, sim.AttrLamplighter) // a real attribute, but not schedule-driven
	RegisterNPCRoutes(context.Background(), w, llm.NewFakeClient())
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	if _, err := w.Send(ForceRouteCommand(sim.AttrLamplighter, false)); err == nil {
		t.Fatal("ForceRouteCommand for a non-scheduled attribute: want error, got nil")
	}
}
