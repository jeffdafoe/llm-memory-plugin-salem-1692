package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_continuation_ephemeral_test.go — LLM-501 (supersedes the LLM-468
// round-index soul trim these tests used to pin). The shared-VA soul rides
// Request.StableContext on EVERY round — the adapter routes it to the
// provider-cached system zone — and the volatile Request.EphemeralContext
// never carries it on any round.
//
// Asserted through the per-round Request the FakeClient records, the same
// instrument harness_selfstate_refresh_test.go uses.

const continuationSoulProse = "I keep the forge and I do not suffer idle talk while the iron is hot."

// seedAliceSoul makes alice a shared-VA actor carrying a synthesized soul — the
// only shape that renders "## Who you are" with prose in it (buildNarrativeState
// gates on KindNPCShared; stateful actors carry identity in their own VA).
func seedAliceSoul(t *testing.T, w *sim.World) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors["alice"]
		a.Kind = sim.KindNPCShared
		a.DisplayName = "Alice Smith"
		a.Narrative = &sim.NarrativeState{AboutMe: continuationSoulProse}
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed soul: %v", err)
	}
}

func TestHarness_SoulRidesStableContextEveryRound(t *testing.T) {
	r := NewRegistry()
	// A non-terminal commit that mutates nothing, so the LLM-88 self-state
	// refresh does NOT fire — this proves the steady-state routing on its own.
	touchFn := func(_ HandlerInput) (sim.Command, error) {
		return sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }}, nil
	}
	if err := r.RegisterCommit("touch", json.RawMessage(`{"type":"object"}`), passthroughDecode, touchFn, false); err != nil {
		t.Fatalf("register touch: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "touch", `{}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "done", `{}`)}}},
	)

	f := newIntegrationFixture(t, r, client)
	defer f.stop()

	seedAliceSoul(t, f.world)
	now := time.Now()
	seedDueWarrant(t, f.world, now)
	if _, err := f.world.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}
	if rec := f.waitForTerminalTelemetry(t); rec.Kind == "failed" || rec.Kind == "stale" {
		t.Fatalf("tick did not complete cleanly: kind=%q", rec.Kind)
	}

	reqs := client.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: got %d, want 2 (touch round, done round)", len(reqs))
	}
	for i, req := range reqs {
		// The soul reaches the model every round — via the cached stable zone.
		if !strings.Contains(req.StableContext, continuationSoulProse) {
			t.Errorf("round %d StableContext must carry the soul prose, got:\n%s", i+1, req.StableContext)
		}
		if !strings.Contains(req.StableContext, "You are Alice Smith.") {
			t.Errorf("round %d StableContext must carry the self-name line (LLM-432), got:\n%s", i+1, req.StableContext)
		}
		// ...and never via the volatile per-tick body, where it would re-bill
		// cold on every call (the LLM-501 point).
		if strings.Contains(req.EphemeralContext, continuationSoulProse) || strings.Contains(req.EphemeralContext, "## Who you are") {
			t.Errorf("round %d EphemeralContext must NOT carry the identity section, got:\n%s", i+1, req.EphemeralContext)
		}
		if !strings.Contains(req.EphemeralContext, "## You") {
			t.Errorf("round %d must still carry the self-state block, got:\n%s", i+1, req.EphemeralContext)
		}
	}
	// No round-index variance: both rounds send the same bodies.
	if reqs[0].EphemeralContext != reqs[1].EphemeralContext {
		t.Errorf("rounds must send identical ephemeral bodies when self-state did not change")
	}
	if reqs[0].StableContext != reqs[1].StableContext {
		t.Errorf("rounds must send identical stable bodies")
	}
}

// The LLM-88 self-state refresh re-renders the ephemeral body mid-tick when a
// commit moves the actor's own coins/needs/inventory. The stable body must
// pass through the refresh untouched — nothing in it is self-state, and a
// refreshed round re-acquiring different stable bytes would break the very
// prefix stability the stream exists for.
func TestHarness_SelfStateRefreshLeavesStableContextAlone(t *testing.T) {
	r := NewRegistry()
	// A non-terminal commit that eats one bread on the world goroutine — the
	// own-state change the LLM-88 refresh keys on.
	eatFn := func(in HandlerInput) (sim.Command, error) {
		return sim.Command{Fn: func(world *sim.World) (any, error) {
			a := world.Actors[in.ActorID]
			if a.Inventory["bread"] > 0 {
				a.Inventory["bread"]--
			}
			return sim.ConsumeResult{Kind: "bread", Requested: 1, Consumed: 1}, nil
		}}, nil
	}
	if err := r.RegisterCommit("eat", json.RawMessage(`{"type":"object"}`), passthroughDecode, eatFn, false); err != nil {
		t.Fatalf("register eat: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "eat", `{}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "done", `{}`)}}},
	)

	f := newIntegrationFixture(t, r, client)
	defer f.stop()

	seedAliceSoul(t, f.world)
	seedAliceInventory(t, f.world, map[sim.ItemKind]int{"bread": 3})
	now := time.Now()
	seedDueWarrant(t, f.world, now)
	if _, err := f.world.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}
	if rec := f.waitForTerminalTelemetry(t); rec.Kind == "failed" || rec.Kind == "stale" {
		t.Fatalf("tick did not complete cleanly: kind=%q", rec.Kind)
	}

	reqs := client.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: got %d, want 2 (eat round, done round)", len(reqs))
	}
	// The refresh happened: round 2 carries the decremented stock, not the
	// tick-open figure.
	if !strings.Contains(reqs[1].EphemeralContext, "bread (x2)") {
		t.Errorf("round 2 must carry the refreshed stock 'bread (x2)', got:\n%s", reqs[1].EphemeralContext)
	}
	if strings.Contains(reqs[1].EphemeralContext, "bread (x3)") {
		t.Errorf("round 2 must not carry the stale 'bread (x3)', got:\n%s", reqs[1].EphemeralContext)
	}
	// The identity never leaks into the refreshed ephemeral body...
	if strings.Contains(reqs[1].EphemeralContext, continuationSoulProse) {
		t.Errorf("a refreshed round must not carry the soul in its ephemeral body, got:\n%s", reqs[1].EphemeralContext)
	}
	// ...and the stable body is byte-identical across the refresh.
	if reqs[0].StableContext != reqs[1].StableContext {
		t.Errorf("StableContext must be byte-identical across a self-state refresh\nround1:\n%s\nround2:\n%s",
			reqs[0].StableContext, reqs[1].StableContext)
	}
	if !strings.Contains(reqs[1].StableContext, continuationSoulProse) {
		t.Errorf("round 2 StableContext must still carry the soul, got:\n%s", reqs[1].StableContext)
	}
}
