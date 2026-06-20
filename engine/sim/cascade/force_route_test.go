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

// TestForceRouteCommand_DoesNotConsumeScheduleBoundary proves the central
// guarantee of the operator lever: forcing a tour dispatches the route but does
// NOT stamp the schedule-window boundary, so the real schedule ticker still
// fires that boundary afterward (the umbilical /route contract: "Does NOT consume
// the real schedule boundary"). Without this, forcing a tour for monitoring would
// silently suppress the day's genuine scheduled tour.
func TestForceRouteCommand_DoesNotConsumeScheduleBoundary(t *testing.T) {
	w, _ := buildRouteCascadeWorld(t)
	seedActorAttribute(w, sim.AttrTownCrier)
	seedRunnerSchedule(w, 540, 1080) // 9:00–18:00
	RegisterNPCRoutes(context.Background(), w, llm.NewFakeClient())
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	if _, err := w.Send(ForceRouteCommand(sim.AttrTownCrier, false)); err != nil {
		t.Fatalf("ForceRouteCommand: %v", err)
	}
	if townCrierBoundaryStamped(t, w) {
		t.Fatal("force stamped the schedule boundary — it must not consume it")
	}

	// The 9:00 start boundary is still unstamped, so a scheduled tick at 10:00
	// fires and stamps it — proving the force left the real boundary intact.
	if _, err := w.Send(RouteScheduleTick(at(10, 0), newDeterministicRand())); err != nil {
		t.Fatalf("scheduled tick: %v", err)
	}
	if !townCrierBoundaryStamped(t, w) {
		t.Error("scheduled boundary did not fire after a force — the force consumed the real boundary")
	}
}

// townCrierBoundaryStamped reads RouteBoundaryStamps on the world goroutine
// (mirrors the inline stamp read in npc_route_test.go's boundary tests).
func townCrierBoundaryStamped(t *testing.T, w *sim.World) bool {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		_, stamped := world.RouteBoundaryStamps[sim.AttrTownCrier]
		return stamped, nil
	}})
	if err != nil {
		t.Fatalf("read town_crier boundary stamp: %v", err)
	}
	return res.(bool)
}
