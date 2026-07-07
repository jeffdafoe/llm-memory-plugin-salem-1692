package handlers

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_craft_terminal_test.go — LLM-319: a successful produce (craftToolName)
// is ALWAYS non-terminal. Under one-shot production every successful produce
// STARTS a batch — real work, never a "tend your post" no-op — so the tick
// continues and the actor can speak its social beat before calling done().
//
// This retires the LLM-201 no-switch terminal-flip this file used to pin: with
// the continuous focus gone there is no "produce that changes nothing" success
// path left to end the tick on (a mid-cycle re-produce bounces as a
// ModelFacingError world-side before the terminal decision is ever reached).
//
// Unlike the dedup tests (which register an OBSERVATION shim to exercise the
// pre-dispatch guards), this registers a REAL commit `produce` whose command
// returns a sim.ProductionStartResult, so the dispatch's ClassCommit terminal
// branch — the code under test — actually runs. It goes through the integration
// fixture (not a bare RunTick) because a real committing tick needs a valid
// causal root, which only the ReactorTickDue path supplies. The number of LLM
// rounds (client.Requests) is the metric that matters: non-terminal means the
// done() round runs.

// newCraftTerminalRegistry builds a registry with a REAL produce commit whose
// command returns a successful ProductionStartResult, plus the terminal done.
// Registered non-terminal, exactly like the production RegisterCraft.
func newCraftTerminalRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	produceFn := func(in HandlerInput) (sim.Command, error) {
		return sim.Command{Fn: func(*sim.World) (any, error) {
			return sim.ProductionStartResult{ID: in.ActorID, Item: "nail", Noun: "nails", BatchQty: 10, DurationSeconds: 1200}, nil
		}}, nil
	}
	if err := r.RegisterCommit(craftToolName, craftSchema, DecodeCraftArgs, produceFn, false); err != nil {
		t.Fatalf("register produce: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	return r
}

// A successful produce does NOT end the tick: the tick continues to a second
// round where the model calls done(). If produce wrongly ended the tick, the
// done turn would never be requested (1 round, terminal_status "success").
func TestHarness_ProduceSuccess_StaysNonTerminal(t *testing.T) {
	client := llm.NewFakeClient(
		callTurn("c1", "produce", `{"item":"nail"}`), // starts a batch -> non-terminal
		doneTurn("d1"), // round 2 runs: the model ends the tick
	)
	f := newIntegrationFixture(t, newCraftTerminalRegistry(t), client)
	defer f.stop()

	now := time.Now()
	seedDueWarrant(t, f.world, now)
	if _, err := f.world.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}
	rec := f.waitForTerminalTelemetry(t)
	if rec.Kind != "completed" {
		t.Fatalf("tick did not complete cleanly: kind=%q", rec.Kind)
	}
	if got := rec.Detail["terminal_status"]; got != "done" {
		t.Errorf("terminal_status: got %q, want \"done\" — a successful produce is non-terminal (LLM-319), so the model's done() ends the tick", got)
	}
	if n := len(client.Requests()); n != 2 {
		t.Errorf("LLM rounds: got %d, want 2 — a successful produce does not end the tick, so round 2 (done) runs", n)
	}
}
