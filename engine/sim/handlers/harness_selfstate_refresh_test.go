package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_selfstate_refresh_test.go — LLM-88: mid-tick own-state re-perception.
// A non-terminal commit that moves the actor's own material state (a consume
// that eases a need and spends stock, a buy that moves coins/goods) makes the
// tick-open `## You` block + eat/drink/buy affordances stale — they were
// rendered once and re-sent verbatim each round, priming the already-satisfied
// action again (live: Josiah ate, then re-consumed against a still-"you feel
// thirsty / consume to drink" furniture). When the post-commit self-state
// actually changed, the harness re-perceives from it so the next round's
// EphemeralContext reflects reality. Asserted via the per-round
// Request.EphemeralContext the FakeClient records.
//
// These run through newIntegrationFixture (not a bare RunTick) because a real
// committing tick needs a valid causal root — RunTickToolCommand rejects a
// hand-built rootEventID that exceeds eventSeq. The subscriber populates the
// root from the ReactorTickDue event, the same path harness_integration_test.go
// uses.

// seedAliceInventory sets alice's carried goods on the world goroutine before
// the tick runs, so the ## You carrying line has something to render.
func seedAliceInventory(t *testing.T, w *sim.World, inv map[sim.ItemKind]int) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["alice"].Inventory = inv
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
}

// After a commit that decrements the actor's stock, round 2's EphemeralContext
// reflects the new quantity — not the stale tick-open one.
func TestHarness_SelfStateRefresh_ReflectsStockChangeNextRound(t *testing.T) {
	r := NewRegistry()
	// A non-terminal commit that eats one bread on the world goroutine — the
	// own-state change the refresh keys on.
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
	if !strings.Contains(reqs[0].EphemeralContext, "bread (x3)") {
		t.Errorf("round 1 (pre-eat) EphemeralContext must show the tick-open stock 'bread (x3)', got:\n%s", reqs[0].EphemeralContext)
	}
	if !strings.Contains(reqs[1].EphemeralContext, "bread (x2)") {
		t.Errorf("round 2 (post-eat) EphemeralContext must reflect the decremented stock 'bread (x2)' via the LLM-88 refresh, got:\n%s", reqs[1].EphemeralContext)
	}
	if strings.Contains(reqs[1].EphemeralContext, "bread (x3)") {
		t.Errorf("round 2 must NOT still carry the stale 'bread (x3)', got:\n%s", reqs[1].EphemeralContext)
	}
}

// A successful commit that changes no own-state (needs/coins/goods) must NOT
// perturb the EphemeralContext — the change-gate skips the re-render, and in
// particular must not accidentally swap in the lean continuation body.
func TestHarness_SelfStateRefresh_NoChangeLeavesBodyIntact(t *testing.T) {
	r := NewRegistry()
	// A non-terminal commit that mutates nothing.
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
		t.Fatalf("requests: got %d, want 2", len(reqs))
	}
	if reqs[0].EphemeralContext != reqs[1].EphemeralContext {
		t.Errorf("a no-op commit must not change EphemeralContext:\nround 1:\n%s\nround 2:\n%s", reqs[0].EphemeralContext, reqs[1].EphemeralContext)
	}
}

func TestSelfStateChanged(t *testing.T) {
	base := &sim.ActorSnapshot{Coins: 10, InventoryHash: 5, Needs: map[sim.NeedKey]int{"thirst": 12}}
	cases := []struct {
		name string
		post *sim.ActorSnapshot
		want bool
	}{
		{"identical", &sim.ActorSnapshot{Coins: 10, InventoryHash: 5, Needs: map[sim.NeedKey]int{"thirst": 12}}, false},
		{"need eased", &sim.ActorSnapshot{Coins: 10, InventoryHash: 5, Needs: map[sim.NeedKey]int{"thirst": 0}}, true},
		{"coins moved", &sim.ActorSnapshot{Coins: 8, InventoryHash: 5, Needs: map[sim.NeedKey]int{"thirst": 12}}, true},
		{"inventory moved", &sim.ActorSnapshot{Coins: 10, InventoryHash: 4, Needs: map[sim.NeedKey]int{"thirst": 12}}, true},
		{"need key added", &sim.ActorSnapshot{Coins: 10, InventoryHash: 5, Needs: map[sim.NeedKey]int{"thirst": 12, "hunger": 3}}, true},
		{"need key swapped", &sim.ActorSnapshot{Coins: 10, InventoryHash: 5, Needs: map[sim.NeedKey]int{"hunger": 12}}, true},
	}
	for _, c := range cases {
		if got := selfStateChanged(base, c.post); got != c.want {
			t.Errorf("%s: selfStateChanged = %v, want %v", c.name, got, c.want)
		}
	}
	if selfStateChanged(nil, nil) {
		t.Error("nil/nil: want false")
	}
	if !selfStateChanged(nil, base) {
		t.Error("nil pre, non-nil post: want true")
	}
	if selfStateChanged(base, nil) {
		t.Error("non-nil pre, nil post: want false (no post → nothing to refresh from)")
	}
}
