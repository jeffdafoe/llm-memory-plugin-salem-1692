package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// craft_terminal_say_test.go — LLM-468 conditional terminality. `produce` stays
// registered non-terminal so a silent producer can still act again in the same
// tick, but a produce that carried a spoken `say` HAS uttered — and an utterance
// ends a tick, the same invariant that makes `speak` terminal-on-success.
// Without the flip the model could say its piece on the acting call and a second
// thing through `speak`, which is the double-utterance the terminal-verb rule
// exists to prevent.
//
// SCOPE: like harness_source_activity_terminal_test.go, these drive the harness
// dispatch branch with a stand-in commit returning a real
// sim.ProductionStartResult — NOT the production sim.StartProductionCycle domain
// logic (input spend, cycle open, and the SpeakTo emission are covered in the sim
// package). What is proven here is the wiring: Spoke on the result ends the tick.

// newSpokenProduceFixture registers a non-terminal `produce` whose command
// returns a ProductionStartResult with the given Spoke flag, plus terminal done,
// and scripts a produce round followed by a done round. The returned client lets
// the caller count how many rounds were actually requested.
func newSpokenProduceFixture(t *testing.T, spoke bool) *llm.FakeClient {
	t.Helper()
	r := NewRegistry()
	produceFn := func(_ HandlerInput) (sim.Command, error) {
		return sim.Command{Fn: func(*sim.World) (any, error) {
			return sim.ProductionStartResult{
				ID: "alice", Item: "nail", Noun: "nails", BatchQty: 1, DurationSeconds: 60, Spoke: spoke,
			}, nil
		}}, nil
	}
	if err := r.RegisterCommit("produce", json.RawMessage(`{"type":"object"}`), passthroughDecode, produceFn, false); err != nil {
		t.Fatalf("register produce: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "produce", `{}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "done", `{}`)}}},
	)

	f := newIntegrationFixture(t, r, client)
	defer f.stop()

	now := time.Now()
	seedDueWarrant(t, f.world, now)
	if _, err := f.world.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}
	if rec := f.waitForTerminalTelemetry(t); rec.Kind == "failed" || rec.Kind == "stale" {
		t.Fatalf("tick did not complete cleanly: kind=%q", rec.Kind)
	}
	return client
}

func TestHarness_SpokenProduceEndsTick(t *testing.T) {
	client := newSpokenProduceFixture(t, true)
	if n := len(client.Requests()); n != 1 {
		t.Fatalf("a produce that spoke must end the tick: got %d rounds, want 1", n)
	}
}

func TestHarness_SilentProduceKeepsTickOpen(t *testing.T) {
	// The 83-actions-a-day case: a silent produce must still let the actor sell,
	// pay, consume or move in the same tick. This is why produce is not simply
	// flipped terminal.
	client := newSpokenProduceFixture(t, false)
	if n := len(client.Requests()); n != 2 {
		t.Fatalf("a silent produce must keep the tick open: got %d rounds, want 2", n)
	}
}

func TestProducedWithSpeech(t *testing.T) {
	if !producedWithSpeech(sim.ProductionStartResult{Spoke: true}) {
		t.Errorf("a spoken production start must report speech")
	}
	if producedWithSpeech(sim.ProductionStartResult{Spoke: false}) {
		t.Errorf("a silent production start must not report speech")
	}
	// Every other tool's result flows through the same check, so a type
	// mismatch must read false rather than panic.
	if producedWithSpeech(sim.ConsumeResult{Kind: "bread"}) {
		t.Errorf("a non-production result must not report speech")
	}
	if producedWithSpeech(nil) {
		t.Errorf("a nil result must not report speech")
	}
}
