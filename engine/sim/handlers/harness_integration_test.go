package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// End-to-end integration tests exercising the full PR 3d pipeline:
//
//   EvaluateReactors → ReactorTickDue emit → subscriber → pool channel →
//   worker → Harness.RunTick → perception/LLM/dispatch → world goroutine
//   commit via RunTickToolCommand → CompleteReactorTick.
//
// These tests cover the paths that harness_test.go can't reach in isolation
// because RunTickToolCommand needs a valid causal root (rootEventID > 0
// AND <= eventSeq). The real subscriber populates rootEventID from the
// ReactorTickDue event's own EventID, which is what bumps eventSeq.

// integrationFixture wires a running world + pool + harness for an
// integration test. Caller is responsible for seeding warrants and calling
// EvaluateReactors to drive a tick.
type integrationFixture struct {
	world *sim.World
	tel   *recordingTelemetry
	pool  *TickWorkerPool
	stop  func()
}

func newIntegrationFixture(t *testing.T, r *Registry, client llm.Client) *integrationFixture {
	t.Helper()
	w, tel, cancelWorld := newTestWorld(t, 0)

	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		cancelWorld()
		t.Fatalf("NewHarness: %v", err)
	}

	pool := NewTickWorkerPoolWithHarness(w, tel, h)
	// RegisterTickHandlers runs on the world goroutine; safe to call before
	// Start (it just registers the admission controller + subscriber).
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		RegisterTickHandlers(world, pool)
		return nil, nil
	}}); err != nil {
		cancelWorld()
		t.Fatalf("RegisterTickHandlers: %v", err)
	}

	poolCtx, poolCancel := context.WithCancel(context.Background())
	pool.Start(poolCtx)

	stop := func() {
		pool.Stop()
		poolCancel()
		pool.Wait()
		cancelWorld()
	}

	return &integrationFixture{world: w, tel: tel, pool: pool, stop: stop}
}

// waitForTerminalTelemetry blocks until the recording sink sees a
// completed/failed/stale record for any tick (the test does one tick per
// fixture). Times out via eventually(t).
func (f *integrationFixture) waitForTerminalTelemetry(t *testing.T) sim.TickTelemetryRecord {
	t.Helper()
	var rec sim.TickTelemetryRecord
	eventually(t, "terminal telemetry record", func() bool {
		for _, r := range f.tel.snapshot() {
			switch r.Kind {
			case "completed", "failed", "stale":
				rec = r
				return true
			}
		}
		return false
	})
	return rec
}

// --- happy path: commit dispatched through the worker pool ---------------

func TestHarnessPool_CommitDispatchEndToEnd(t *testing.T) {
	var commitRan atomic.Bool
	r := NewRegistry()
	moveFn := func(_ HandlerInput) (sim.Command, error) {
		return sim.Command{Fn: func(*sim.World) (any, error) {
			commitRan.Store(true)
			return nil, nil
		}}, nil
	}
	if err := r.RegisterCommit("move_to", json.RawMessage(`{"type":"object"}`), passthroughDecode, moveFn, true); err != nil {
		t.Fatalf("register: %v", err)
	}

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "move_to", `{}`)},
	}})

	f := newIntegrationFixture(t, r, client)
	defer f.stop()

	// Seed warrant + trigger evaluator. The evaluator emits ReactorTickDue
	// (bumping eventSeq), the subscriber enqueues a tickJob with the
	// emitted EventID as rootEventID, the worker dequeues and runs the
	// harness. The harness's move_to dispatch then hits a VALID
	// rootEventID at the RunTickToolCommand guard.
	now := time.Now()
	seedDueWarrant(t, f.world, now)
	if _, err := f.world.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}

	rec := f.waitForTerminalTelemetry(t)
	if rec.Kind != "completed" {
		t.Errorf("terminal telemetry kind: got %q, want \"completed\"", rec.Kind)
	}

	if !commitRan.Load() {
		t.Errorf("move_to commit handler should have run; commitRan=false")
	}

	// CompleteReactorTick should have cleared TickInFlight.
	eventually(t, "alice tick cleared", func() bool { return !actorTickInFlight(t, f.world) })
}

// --- stale at tool: supersede mid-batch via an observation handler -------

func TestHarnessPool_StaleAtTool(t *testing.T) {
	// Observation tool that supersedes alice's attempt as a side effect.
	// Runs synchronously on the worker goroutine; the world-goroutine
	// command it sends bumps alice's TickAttemptID, so the subsequent
	// move_to in the same batch hits the stale guard.
	var commitRan atomic.Bool
	var observationRan atomic.Bool
	var fixturePtr atomic.Pointer[integrationFixture]

	r := NewRegistry()
	supersedeFn := func(_ context.Context, _ HandlerInput) (string, error) {
		observationRan.Store(true)
		f := fixturePtr.Load()
		if f == nil {
			return "[error: fixture not set]", nil
		}
		// Bump TickAttemptID off-pattern: writes from the world goroutine,
		// completes synchronously before this handler returns.
		if _, err := f.world.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
			world.Actors["alice"].TickAttemptID = "SUPERSEDED"
			return nil, nil
		}}); err != nil {
			return "[error: supersede send failed]", nil
		}
		return "[supersede done]", nil
	}
	if err := r.RegisterObservation("supersede", json.RawMessage(`{"type":"object"}`), passthroughDecode, supersedeFn); err != nil {
		t.Fatalf("register supersede: %v", err)
	}

	moveFn := func(_ HandlerInput) (sim.Command, error) {
		return sim.Command{Fn: func(*sim.World) (any, error) {
			commitRan.Store(true)
			return nil, nil
		}}, nil
	}
	if err := r.RegisterCommit("move_to", json.RawMessage(`{"type":"object"}`), passthroughDecode, moveFn, true); err != nil {
		t.Fatalf("register move_to: %v", err)
	}

	// Multi-call response: [supersede, move_to]. The supersede observation
	// runs first (bumping the attempt), then move_to hits the stale guard
	// at RunTickToolCommand and ends the batch as TickStatusStale +
	// StaleStageAtTool.
	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{
			newToolCall("c1", 0, "supersede", `{}`),
			newToolCall("c2", 1, "move_to", `{}`),
		},
	}})

	f := newIntegrationFixture(t, r, client)
	defer f.stop()
	fixturePtr.Store(f)

	now := time.Now()
	seedDueWarrant(t, f.world, now)
	if _, err := f.world.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}

	rec := f.waitForTerminalTelemetry(t)
	// CompleteReactorTick saw the superseded attempt and returned Stale=true
	// — telemetry kind is "stale" (worker.go runs that branch before the
	// harness-set terminal_status detail can land).
	if rec.Kind != "stale" {
		t.Errorf("terminal telemetry kind: got %q, want \"stale\"", rec.Kind)
	}

	if !observationRan.Load() {
		t.Errorf("supersede observation should have run; observationRan=false")
	}
	if commitRan.Load() {
		t.Errorf("move_to commit should NOT have run after supersede; commitRan=true")
	}

	// At-tool stale records the rich TickResult diagnostics in the
	// completed/failed branch (not the stale branch — worker.go's stale
	// kind is recorded BEFORE applying harness diagnostics). Find the
	// preceding completed/failed record for this attempt — the harness
	// returned TickStatusStale + StaleStageAtTool with a populated
	// Tools{Requested,FailedRejected} set, and the harness's diagnostics
	// fed harnessResultDetail. The "stale" kind from CompleteReactorTick
	// is a separate, terser record.
	//
	// Per R2 invariant: ToolsFailedRejected MUST be a subset of
	// ToolsRequested, the stale call itself MUST be in FailedRejected,
	// and every in-budget call after the stale one MUST be in BOTH.
	var harnessRec *sim.TickTelemetryRecord
	for i, r := range f.tel.snapshot() {
		if r.Detail["stale_stage"] == sim.StaleStageAtTool.String() {
			rec := f.tel.snapshot()[i]
			harnessRec = &rec
			break
		}
	}
	if harnessRec == nil {
		t.Fatalf("expected a telemetry record carrying stale_stage=at_tool, got %+v", f.tel.snapshot())
	}
	requested := strings.Split(harnessRec.Detail["tools_requested"], ",")
	failed := strings.Split(harnessRec.Detail["tools_failed_rejected"], ",")

	requestedSet := make(map[string]bool, len(requested))
	for _, n := range requested {
		requestedSet[n] = true
	}
	for _, n := range failed {
		if !requestedSet[n] {
			t.Errorf("R2 invariant violation: tool %q is in tools_failed_rejected but NOT in tools_requested", n)
		}
	}

	// The stale call (move_to) must be in BOTH sets.
	if !requestedSet["move_to"] {
		t.Errorf("move_to should be in tools_requested; got %v", requested)
	}
	failedSet := make(map[string]bool, len(failed))
	for _, n := range failed {
		failedSet[n] = true
	}
	if !failedSet["move_to"] {
		t.Errorf("move_to should be in tools_failed_rejected (the stale-at-tool call itself); got %v", failed)
	}
}

// --- harness LLM error surfaces via the worker pipeline ------------------

func TestHarnessPool_LLMErrorPath(t *testing.T) {
	r := NewRegistry()
	// Register a dummy observation so AdvertisedSpecs has something to
	// hand the FakeClient (which never gets called anyway because of the
	// scripted error).
	obsFn := func(_ context.Context, _ HandlerInput) (string, error) { return "", nil }
	if err := r.RegisterObservation("noop", json.RawMessage(`{"type":"object"}`), passthroughDecode, obsFn); err != nil {
		t.Fatalf("register: %v", err)
	}

	client := llm.NewFakeClient(llm.ScriptedTurn{Err: &llm.Error{Class: llm.ErrorTransport, Message: "boom"}})

	f := newIntegrationFixture(t, r, client)
	defer f.stop()

	now := time.Now()
	seedDueWarrant(t, f.world, now)
	if _, err := f.world.Send(sim.EvaluateReactors(now)); err != nil {
		t.Fatalf("EvaluateReactors: %v", err)
	}

	rec := f.waitForTerminalTelemetry(t)
	if rec.Kind != "failed" {
		t.Errorf("terminal telemetry kind: got %q, want \"failed\"", rec.Kind)
	}
	if rec.Detail["terminal_status"] != "failed_before_render" {
		t.Errorf("terminal_status detail: got %q, want \"failed_before_render\"", rec.Detail["terminal_status"])
	}

	// CompleteReactorTick still landed and cleared TickInFlight (the worker
	// pipeline ferries the failure result the same way as success).
	eventually(t, "alice tick cleared", func() bool { return !actorTickInFlight(t, f.world) })
}

// --- LLM-209: a TerminalNoOpError ends the tick (no budget_forced storm) --

// A commit whose command returns a sim.TerminalNoOpError — the shape of a no-op
// rest verb (move_to to the structure the actor is already in, take_break while
// already on break) — must END the tick in ONE round, even though the tool is
// registered NON-terminal. The terminal outcome must come from the
// TerminalNoOpError path, not the registration (registered false on purpose).
// Without the fix the weak model re-fires the identical no-op every round to the
// iteration budget (terminal_status budget_forced) — the observed move_to×6 /
// take_break×6 storm. client.Requests() (the number of LLM rounds) is the metric.
func TestHarnessPool_TerminalNoOpEndsTickNotBudgetForced(t *testing.T) {
	var commitRuns atomic.Int32
	r := NewRegistry()
	noopFn := func(_ HandlerInput) (sim.Command, error) {
		return sim.Command{Fn: func(*sim.World) (any, error) {
			commitRuns.Add(1)
			return nil, sim.TerminalNoOpError{Msg: `you are already at "home" — you're right where you meant to be; nothing more to do here.`}
		}}, nil
	}
	// terminalOnSuccess=false: the tick must end via the no-op error path, not the
	// tool's own terminal policy.
	if err := r.RegisterCommit("move_to", json.RawMessage(`{"type":"object"}`), passthroughDecode, noopFn, false); err != nil {
		t.Fatalf("register move_to: %v", err)
	}

	// Script the identical no-op move far more times than the iteration budget: if
	// the no-op did NOT end the tick, the harness would consume these round after
	// round to budget_forced. With the fix, only the first is ever requested.
	client := llm.NewFakeClient(
		callTurn("c1", "move_to", `{}`),
		callTurn("c2", "move_to", `{}`),
		callTurn("c3", "move_to", `{}`),
		callTurn("c4", "move_to", `{}`),
		callTurn("c5", "move_to", `{}`),
		callTurn("c6", "move_to", `{}`),
		callTurn("c7", "move_to", `{}`),
		callTurn("c8", "move_to", `{}`),
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
	if got := rec.Detail["terminal_status"]; got != "success" {
		t.Errorf("terminal_status: got %q, want \"success\" — a TerminalNoOpError ends the tick as a terminal no-op, not budget_forced", got)
	}
	if n := len(client.Requests()); n != 1 {
		t.Errorf("LLM rounds: got %d, want 1 — the no-op ends the tick on round 1 instead of storming to the iteration budget", n)
	}
	if got := commitRuns.Load(); got != 1 {
		t.Errorf("commit runs: got %d, want 1 — the no-op command runs once then ends the tick", got)
	}
}
