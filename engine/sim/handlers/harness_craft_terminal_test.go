package handlers

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_craft_terminal_test.go — LLM-201: produce (craftToolName) is terminal-on-
// success when the call does NOT change the actor's active production item, and
// stays non-terminal when it does. A no-switch produce is a "tend your post" no-op;
// ending the tick on it kills the wasted second agentic round (and the malformed
// "tool spooge" that rides it). A genuine switch keeps the tick going so the actor
// can speak/act/done() after choosing.
//
// Unlike the dedup tests (which register an OBSERVATION shim to exercise the pre-
// dispatch guards), these register a REAL commit `produce` whose command returns a
// sim.ProductionFocusResult with a fixed Switched, so the dispatch's ClassCommit
// terminal branch — the code under test — actually runs. They go through the
// integration fixture (not a bare RunTick) because a real committing tick needs a
// valid causal root, which only the ReactorTickDue path supplies. The number of LLM
// rounds (client.Requests) is the metric that matters: LLM-201 is precisely about
// the wasted second round not happening.

// newCraftTerminalRegistry builds a registry with a REAL produce commit whose
// command returns a ProductionFocusResult with the given Switched flag, plus the
// terminal done. Registered non-terminal, exactly like the production RegisterCraft —
// the no-switch terminality is the dispatch's job, not the registry's.
func newCraftTerminalRegistry(t *testing.T, switched bool) *Registry {
	t.Helper()
	r := NewRegistry()
	produceFn := func(in HandlerInput) (sim.Command, error) {
		return sim.Command{Fn: func(*sim.World) (any, error) {
			return sim.ProductionFocusResult{ID: in.ActorID, Focus: "nail", Switched: switched}, nil
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

// A no-switch produce ends the tick in ONE round: only the produce round runs, the
// scripted done() is never reached, and the tick completes cleanly.
func TestHarness_ProduceNoSwitch_EndsTick(t *testing.T) {
	client := llm.NewFakeClient(
		callTurn("c1", "produce", `{"item":"nail"}`),
		doneTurn("d1"), // must NOT be reached — the no-switch produce already ended the tick
	)
	f := newIntegrationFixture(t, newCraftTerminalRegistry(t, false), client)
	defer f.stop()

	now := time.Now()
	seedDueWarrant(t, f.world, now)
	if _, err := f.world.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}
	if rec := f.waitForTerminalTelemetry(t); rec.Kind != "completed" {
		t.Fatalf("tick did not complete cleanly: kind=%q", rec.Kind)
	}
	if n := len(client.Requests()); n != 1 {
		t.Errorf("LLM rounds: got %d, want 1 — a no-switch produce ends the tick, so the wasted second round never runs", n)
	}
}

// A produce that SWITCHES the active item stays non-terminal: the tick continues to
// a second round where the model calls done().
func TestHarness_ProduceSwitch_StaysNonTerminal(t *testing.T) {
	client := llm.NewFakeClient(
		callTurn("c1", "produce", `{"item":"skillet"}`), // switch -> non-terminal
		doneTurn("d1"), // round 2 runs: the model ends the tick
	)
	f := newIntegrationFixture(t, newCraftTerminalRegistry(t, true), client)
	defer f.stop()

	now := time.Now()
	seedDueWarrant(t, f.world, now)
	if _, err := f.world.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}
	if rec := f.waitForTerminalTelemetry(t); rec.Kind != "completed" {
		t.Fatalf("tick did not complete cleanly: kind=%q", rec.Kind)
	}
	if n := len(client.Requests()); n != 2 {
		t.Errorf("LLM rounds: got %d, want 2 — a switching produce is non-terminal, so round 2 (done) runs", n)
	}
}
