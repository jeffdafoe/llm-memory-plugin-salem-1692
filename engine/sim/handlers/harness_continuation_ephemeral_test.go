package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_continuation_ephemeral_test.go — LLM-468. A tick is an agentic
// multi-call loop, and the ephemeral body is attached to the current turn only
// (never persisted into history), so every round re-ships it in full — including
// the shared-VA soul prose, which is static per actor and averaged 4.4KB across
// 1,747 measured calls in 24h. Round 0 gets the full body; continuations get the
// same body minus that prose.
//
// Asserted through the per-round Request.EphemeralContext the FakeClient
// records, the same instrument harness_selfstate_refresh_test.go uses.

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

func TestHarness_ContinuationRoundDropsSoulProse(t *testing.T) {
	r := NewRegistry()
	// A non-terminal commit that mutates nothing, so the LLM-88 self-state
	// refresh does NOT fire — this test must prove the round-index swap on its
	// own, not ride a re-render.
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
	if !strings.Contains(reqs[0].EphemeralContext, continuationSoulProse) {
		t.Errorf("round 1 must carry the soul prose — it is the identity framing the model deliberates from, got:\n%s", reqs[0].EphemeralContext)
	}
	if strings.Contains(reqs[1].EphemeralContext, continuationSoulProse) {
		t.Errorf("round 2 (continuation) must NOT re-ship the soul prose, got:\n%s", reqs[1].EphemeralContext)
	}
	// The name line survives so the model can still tell it is being addressed
	// (LLM-432), and the round-2 body must still be a real perception body — not
	// emptied by an off-by-one in the slice.
	if !strings.Contains(reqs[1].EphemeralContext, "You are Alice Smith.") {
		t.Errorf("round 2 must keep the self-name line, got:\n%s", reqs[1].EphemeralContext)
	}
	if !strings.Contains(reqs[1].EphemeralContext, "## You") {
		t.Errorf("round 2 must still carry the self-state block, got:\n%s", reqs[1].EphemeralContext)
	}
	if len(reqs[1].EphemeralContext) >= len(reqs[0].EphemeralContext) {
		t.Errorf("round 2 body (%d bytes) must be shorter than round 1 (%d bytes)",
			len(reqs[1].EphemeralContext), len(reqs[0].EphemeralContext))
	}
}

// The two mechanisms compose: the LLM-88 self-state refresh re-renders mid-tick
// when a commit moves the actor's own coins/needs/inventory, and it must refresh
// BOTH bodies. If it updated only the full one, a refreshed continuation round
// would fall back to a stale body — or worse, re-acquire the soul prose the
// round-index selection had just dropped (code_review).
func TestHarness_SelfStateRefreshKeepsContinuationLean(t *testing.T) {
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
	// ...and it stayed lean: a refreshed continuation must not re-acquire the soul.
	if strings.Contains(reqs[1].EphemeralContext, continuationSoulProse) {
		t.Errorf("a REFRESHED continuation round must still omit the soul prose, got:\n%s", reqs[1].EphemeralContext)
	}
	if !strings.Contains(reqs[1].EphemeralContext, "You are Alice Smith.") {
		t.Errorf("a refreshed continuation round must still keep the self-name line, got:\n%s", reqs[1].EphemeralContext)
	}
	// Round 1 is unaffected by either mechanism.
	if !strings.Contains(reqs[0].EphemeralContext, continuationSoulProse) {
		t.Errorf("round 1 must still carry the soul prose, got:\n%s", reqs[0].EphemeralContext)
	}
}
