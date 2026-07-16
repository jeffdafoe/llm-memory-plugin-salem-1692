package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_source_activity_terminal_test.go — LLM-443. Stoke and repair are timed
// source-activity starts: a started stoke/repair opens a window, so a SECOND start of
// the same kind that tick bounces "already busy ..." and a weak model stormed on that
// reject, burning the whole turn (live john-ellis stoke loop). Flipping both to
// terminal-on-success means the first success ends the tick, so the re-fire is
// structurally impossible.
//
// SCOPE: like harness_speak_terminal_test.go, these drive the harness ClassCommit
// TERMINAL-DISPATCH branch with a stand-in command (a benign no-op), NOT the production
// sim.StartStoke / sim.StartRepair domain logic — the gate validation, fuel spend, and
// window open are covered in the sim package. What is proven here is the wiring: a
// successful stoke/repair commit ends the tick, so the scripted re-fire + done() are
// never requested. The terminal flag itself is pinned by
// register_stoke_repair_terminal_test.go.

// newSourceActivityTerminalRegistry registers a REAL commit `name` terminal-on-success
// (mirroring the production RegisterStoke/RegisterRepair flag) whose command returns a
// benign result, plus the terminal done.
func newSourceActivityTerminalRegistry(t *testing.T, name string, schema json.RawMessage, decode func(json.RawMessage) (any, error)) *Registry {
	t.Helper()
	r := NewRegistry()
	fn := func(HandlerInput) (sim.Command, error) {
		return sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }}, nil
	}
	if err := r.RegisterCommit(name, schema, decode, fn, true); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	return r
}

// A started stoke/repair ends the tick on first success: the scripted second start
// (which live would bounce "already busy") and the trailing done() are never reached —
// one LLM round, terminal status "success". This is the whole point of LLM-443, proven
// for BOTH timed starts.
func TestHarness_SourceActivityStart_EndsTickNoRefire(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema json.RawMessage
		decode func(json.RawMessage) (any, error)
	}{
		{"stoke", stokeSchema, DecodeStokeArgs},
		{"repair", repairSchema, DecodeRepairArgs},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			client := llm.NewFakeClient(
				callTurn("a1", tc.name, `{}`),
				callTurn("a2", tc.name, `{}`), // must NOT be reached — the first start ended the tick
				doneTurn("d1"),                // nor this
			)
			f := newIntegrationFixture(t, newSourceActivityTerminalRegistry(t, tc.name, tc.schema, tc.decode), client)
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
				t.Errorf("%s: terminal_status = %q, want \"success\" — a started %s ends the tick on its own success", tc.name, got, tc.name)
			}
			if n := len(client.Requests()); n != 1 {
				t.Errorf("%s: LLM rounds = %d, want 1 — the first %s ends the tick, so the re-fire and done() rounds never run", tc.name, n, tc.name)
			}
		})
	}
}
