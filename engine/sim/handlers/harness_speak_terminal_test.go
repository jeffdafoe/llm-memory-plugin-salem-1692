package handlers

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_speak_terminal_test.go — LLM-321: a successful speak is terminal-on-
// success. The turn prompt used to tell the model to call done() after speaking,
// costing a second full ~6.7k-input LLM round whose only job was to emit done()
// (~22% of all reactor-tick calls). Ending the tick on the speak commit itself
// kills that round.
//
// Like harness_craft_terminal_test.go, these register a REAL commit `speak`
// (terminal-on-success, mirroring the production RegisterSpeak) whose command
// returns a benign result, so the dispatch's ClassCommit terminal branch — the
// code under test — actually runs. They go through the integration fixture (not
// a bare RunTick) because a real committing tick needs a valid causal root,
// which only the ReactorTickDue path supplies. The LLM round count
// (client.Requests) is the metric that matters: terminal means the done() round
// never runs.

// newSpeakTerminalRegistry builds a registry with a REAL speak commit registered
// terminal-on-success (exactly like the production RegisterSpeak), plus the
// terminal done. The command returns nil — commitResultContent reads the speak
// echo off the decoded args, not the command result, so no domain result is
// needed to exercise the terminal branch.
func newSpeakTerminalRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	speakFn := func(HandlerInput) (sim.Command, error) {
		return sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }}, nil
	}
	if err := r.RegisterCommit("speak", speakSchema, DecodeSpeakArgs, speakFn, true); err != nil {
		t.Fatalf("register speak: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	return r
}

// A speak-only tick ends in ONE round: the speak commit ends the tick on its own
// success, so the scripted done() round is never requested. This is the whole
// point of LLM-321 — the trailing done() LLM call is gone.
func TestHarness_SpeakSuccess_EndsTick(t *testing.T) {
	client := llm.NewFakeClient(
		callTurn("c1", "speak", `{"text":"Good evening to you."}`),
		doneTurn("d1"), // must NOT be reached — the speak already ended the tick
	)
	f := newIntegrationFixture(t, newSpeakTerminalRegistry(t), client)
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
		t.Errorf("terminal_status: got %q, want \"success\" — the speak ends the tick on its own success, not the model's done()", got)
	}
	if n := len(client.Requests()); n != 1 {
		t.Errorf("LLM rounds: got %d, want 1 — a successful speak ends the tick, so the trailing done() round never runs", n)
	}
}

// A FAILED speak does NOT end the tick: terminal-on-SUCCESS, not terminal-on-
// attempt. Round 1's speak command bounces (a ModelFacingError, e.g. nobody to
// address), the tick continues, and round 2's done() ends it — two LLM rounds,
// terminal_status "done". If a bounced speak wrongly ended the tick, the done
// round would never be requested (this is the retry contract the harness
// preserves for every failed commit).
func TestHarness_SpeakBounce_StaysNonTerminal(t *testing.T) {
	r := NewRegistry()
	speakFn := func(HandlerInput) (sim.Command, error) {
		return sim.Command{Fn: func(*sim.World) (any, error) {
			return nil, sim.ModelFacingError{Msg: "there is no one here to hear you"}
		}}, nil
	}
	if err := r.RegisterCommit("speak", speakSchema, DecodeSpeakArgs, speakFn, true); err != nil {
		t.Fatalf("register speak: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}

	client := llm.NewFakeClient(
		callTurn("c1", "speak", `{"text":"Good evening to you."}`), // bounces -> non-terminal
		doneTurn("d1"), // round 2 runs: the model ends the tick
	)
	f := newIntegrationFixture(t, r, client)
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
		t.Errorf("terminal_status: got %q, want \"done\" — a bounced speak is non-terminal, so the model's done() ends the tick", got)
	}
	if n := len(client.Requests()); n != 2 {
		t.Errorf("LLM rounds: got %d, want 2 — a failed speak must not end the tick (terminal on success, not on attempt)", n)
	}
}

// A single response that emits [speak, done] commits the speak and ends the tick
// on it; the done in the same batch is skipped as post_terminal. Still one round,
// and the tick ends as "success" (the speak ended it), not "done". This pins the
// "a speak that would previously be followed by done() no longer emits a second
// call" contract even when the model bundles both into one response.
func TestHarness_SpeakThenDoneSameBatch_SpeakEndsTick(t *testing.T) {
	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{
			newToolCall("c1", 0, "speak", `{"text":"A room is four coins."}`),
			newToolCall("d1", 1, "done", `{}`),
		}}},
	)
	f := newIntegrationFixture(t, newSpeakTerminalRegistry(t), client)
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
		t.Errorf("terminal_status: got %q, want \"success\" — the speak ends the tick; the same-batch done() is skipped as post_terminal", got)
	}
	if n := len(client.Requests()); n != 1 {
		t.Errorf("LLM rounds: got %d, want 1 — the speak ends the tick within the single response", n)
	}
}
