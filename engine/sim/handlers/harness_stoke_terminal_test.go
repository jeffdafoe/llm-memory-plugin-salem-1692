package handlers

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_stoke_terminal_test.go — LLM-443. Stoke (like repair) is a timed source-
// activity start: a started stoke opens a window, so a SECOND stoke the same tick
// bounces "already busy — finish what you're doing before tending the fire" and a
// weak model stormed on that reject, burning the whole turn (live john-ellis stoke
// loop). Flipping stoke to terminal-on-success means the first successful stoke ends
// the tick, so the re-fire storm cannot recur.
//
// Like harness_speak_terminal_test.go, this registers a REAL stoke commit terminal-
// on-success (mirroring the production RegisterStoke) with a benign no-op command —
// the harness ClassCommit terminal branch, not StartStoke's domain logic, is what's
// under test — and drives it through the integration fixture (a real committing tick
// needs a valid causal root, which only the ReactorTickDue path supplies).

// newStokeTerminalRegistry registers a REAL stoke commit terminal-on-success (exactly
// like the production RegisterStoke) whose command returns a benign result, plus the
// terminal done.
func newStokeTerminalRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	stokeFn := func(HandlerInput) (sim.Command, error) {
		return sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }}, nil
	}
	if err := r.RegisterCommit("stoke", stokeSchema, DecodeStokeArgs, stokeFn, true); err != nil {
		t.Fatalf("register stoke: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	return r
}

// The first successful stoke ends the tick: the second stoke (which live would bounce
// "already busy") and the trailing done() are never reached — one LLM round, terminal
// status "success". This is the whole point of LLM-443: the within-tick re-fire storm
// is structurally impossible once a started stoke ends the tick.
func TestHarness_StokeSuccess_EndsTickNoRestoke(t *testing.T) {
	client := llm.NewFakeClient(
		callTurn("s1", "stoke", `{}`),
		callTurn("s2", "stoke", `{}`), // must NOT be reached — the first stoke ended the tick
		doneTurn("d1"),                // nor this
	)
	f := newIntegrationFixture(t, newStokeTerminalRegistry(t), client)
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
	if got := rec.Detail["terminal_status"]; got != "success" {
		t.Errorf("terminal_status: got %q, want \"success\" — a started stoke ends the tick on its own success", got)
	}
	if n := len(client.Requests()); n != 1 {
		t.Errorf("LLM rounds: got %d, want 1 — the first stoke ends the tick, so the re-fire and done() rounds never run", n)
	}
}
