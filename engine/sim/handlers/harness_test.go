package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// --- test fixtures --------------------------------------------------------

// newHarnessWorld builds a running world with alice in-flight under the
// given attemptID. Returns the world + a cancel; caller defers cancel.
func newHarnessWorld(t *testing.T, attemptID sim.TickAttemptID) (*sim.World, context.CancelFunc) {
	t.Helper()
	w, _, cancel := newTestWorld(t, 0)
	setInFlight(t, w, attemptID)
	return w, cancel
}

// newTestJob builds a tickJob for alice with the given attempt ID and
// warrants. Used by every harness test.
func newTestJob(attemptID sim.TickAttemptID, warrants []sim.WarrantMeta) tickJob {
	return tickJob{
		actorID:        "alice",
		attemptID:      attemptID,
		rootEventID:    42,
		warrants:       warrants,
		warrantedSince: time.Unix(1_700_000_000, 0),
		dueAt:          time.Unix(1_700_000_001, 0),
		emittedAt:      time.Unix(1_700_000_002, 0),
	}
}

// newTestRegistry builds a registry with a representative mix of tools:
// observation `recall`, commit-terminal `move_to`, commit-non-terminal
// `note`, disabled commit `speak`, terminal `done`. The handlers are
// minimal — they record being called via closures the test supplies.
type testRegistry struct {
	r              *Registry
	observationLog *[]string
	commitLog      *[]string
}

func newTestRegistry(t *testing.T) *testRegistry {
	t.Helper()
	r := NewRegistry()
	var obsLog, comLog []string
	obsFn := func(_ context.Context, in HandlerInput) (string, error) {
		obsLog = append(obsLog, "recall")
		return "[recall: ok]", nil
	}
	if err := r.RegisterObservation("recall", json.RawMessage(`{"type":"object"}`), passthroughDecode, obsFn); err != nil {
		t.Fatalf("register recall: %v", err)
	}
	moveFn := func(in HandlerInput) (sim.Command, error) {
		comLog = append(comLog, "move_to")
		// A no-op command — returns nil tuple. The harness wraps it via
		// RunTickToolCommand; the wrapper checks attempt-staleness on the
		// world goroutine and then runs this command.
		return sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }}, nil
	}
	if err := r.RegisterCommit("move_to", json.RawMessage(`{"type":"object"}`), passthroughDecode, moveFn, true); err != nil {
		t.Fatalf("register move_to: %v", err)
	}
	noteFn := func(in HandlerInput) (sim.Command, error) {
		comLog = append(comLog, "note")
		return sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }}, nil
	}
	if err := r.RegisterCommit("note", json.RawMessage(`{"type":"object"}`), passthroughDecode, noteFn, false); err != nil {
		t.Fatalf("register note: %v", err)
	}
	speakFn := func(_ HandlerInput) (sim.Command, error) {
		// Should never be called — speak is Disabled, validator rejects.
		t.Errorf("speak handler should never run (disabled)")
		return sim.Command{}, nil
	}
	if err := r.RegisterCommit("speak", json.RawMessage(`{"type":"object"}`), passthroughDecode, speakFn, true, WithAvailability(AvailabilityDisabled)); err != nil {
		t.Fatalf("register speak: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	return &testRegistry{r: r, observationLog: &obsLog, commitLog: &comLog}
}

func passthroughDecode(raw json.RawMessage) (any, error) {
	return string(raw), nil
}

// newToolCall is a typed helper for building llm.RawToolCall in tests.
func newToolCall(id string, idx int, name, args string) llm.RawToolCall {
	return llm.RawToolCall{ID: id, Index: idx, Name: name, Arguments: json.RawMessage(args)}
}

// newHarness builds a Harness with the given client + a fresh registry +
// the given budgets. Budgets <= 0 use the defaults.
func newTestHarness(t *testing.T, client llm.Client, iterBudget, callsCap int) (*Harness, *testRegistry) {
	t.Helper()
	tr := newTestRegistry(t)
	cfg := HarnessConfig{
		Client:                  client,
		Registry:                tr.r,
		IterationBudget:         iterBudget,
		MaxToolCallsPerResponse: callsCap,
	}
	h, err := NewHarness(cfg)
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	return h, tr
}

// --- NewHarness validation ----------------------------------------------

func TestHarness_NewHarness_RequiresClient(t *testing.T) {
	_, err := NewHarness(HarnessConfig{Registry: NewRegistry()})
	if err == nil || !strings.Contains(err.Error(), "Client") {
		t.Errorf("expected Client-required error, got %v", err)
	}
}

func TestHarness_NewHarness_RequiresRegistry(t *testing.T) {
	_, err := NewHarness(HarnessConfig{Client: llm.NewFakeClient()})
	if err == nil || !strings.Contains(err.Error(), "Registry") {
		t.Errorf("expected Registry-required error, got %v", err)
	}
}

func TestHarness_NewHarness_AppliesDefaults(t *testing.T) {
	h, err := NewHarness(HarnessConfig{Client: llm.NewFakeClient(), Registry: NewRegistry()})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	if h.iterationBudget != DefaultIterationBudget {
		t.Errorf("iterationBudget default: got %d, want %d", h.iterationBudget, DefaultIterationBudget)
	}
	if h.maxToolCallsPerResponse != DefaultMaxToolCallsPerResponse {
		t.Errorf("maxToolCallsPerResponse default: got %d, want %d", h.maxToolCallsPerResponse, DefaultMaxToolCallsPerResponse)
	}
	if h.validator == nil {
		t.Errorf("validator default: got nil")
	}
	if h.clock == nil {
		t.Errorf("clock default: got nil")
	}
}

// --- preflight stale check ----------------------------------------------

func TestHarness_Preflight_ActorNotInSnapshot(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	h, _ := newTestHarness(t, llm.NewFakeClient(), 0, 0)
	job := tickJob{actorID: "ghost", attemptID: "attempt-A", rootEventID: 1}

	result := h.RunTick(context.Background(), w, job)
	if result.TerminalStatus != sim.TickStatusFailedBeforeRender {
		t.Errorf("missing actor: got %v, want FailedBeforeRender", result.TerminalStatus)
	}
	if result.IterationCount != 0 {
		t.Errorf("IterationCount: got %d, want 0 (preflight short-circuit)", result.IterationCount)
	}
}

func TestHarness_Preflight_NotInFlight(t *testing.T) {
	w, _, cancel := newTestWorld(t, 0) // alice exists but NOT in-flight
	defer cancel()

	h, _ := newTestHarness(t, llm.NewFakeClient(), 0, 0)
	warrants := []sim.WarrantMeta{{TriggerActorID: "bob", Reason: sim.BasicWarrantReason{K: sim.WarrantKindNPCSpoke}}}
	job := newTestJob("attempt-A", warrants)

	result := h.RunTick(context.Background(), w, job)
	if result.TerminalStatus != sim.TickStatusStale {
		t.Errorf("not in-flight: got %v, want Stale", result.TerminalStatus)
	}
	if result.StaleStage != sim.StaleStageBeforeRender {
		t.Errorf("StaleStage: got %v, want before_render", result.StaleStage)
	}
	if len(result.UnaddressedWarrants) != 1 {
		t.Errorf("UnaddressedWarrants: got %d, want 1 (full carry-forward)", len(result.UnaddressedWarrants))
	}
}

func TestHarness_Preflight_AttemptIDMismatch(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	h, _ := newTestHarness(t, llm.NewFakeClient(), 0, 0)
	// Job carries attempt-B; world has alice in-flight under attempt-A.
	warrants := []sim.WarrantMeta{{TriggerActorID: "bob", Reason: sim.BasicWarrantReason{K: sim.WarrantKindNPCSpoke}}}
	job := newTestJob("attempt-B", warrants)

	result := h.RunTick(context.Background(), w, job)
	if result.TerminalStatus != sim.TickStatusStale {
		t.Errorf("attempt mismatch: got %v, want Stale", result.TerminalStatus)
	}
	if result.StaleStage != sim.StaleStageBeforeRender {
		t.Errorf("StaleStage: got %v, want before_render", result.StaleStage)
	}
}

// newTestHarnessWithWaitMax builds a harness with an explicit preflight
// freshness-wait ceiling — used by the snapshot-lag tests to bound the wait
// tightly so they don't spend the default 75ms sleeping.
func newTestHarnessWithWaitMax(t *testing.T, client llm.Client, waitMax time.Duration) *Harness {
	t.Helper()
	tr := newTestRegistry(t)
	h, err := NewHarness(HarnessConfig{Client: client, Registry: tr.r, PreflightSnapshotWaitMax: waitMax})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	return h
}

// A published snapshot that never catches up to the job's dispatch (a wedged /
// saturated world goroutine) must NOT be read as a supersession: it predates
// the dispatch and cannot witness one. RunTick returns a snapshot-lag retry
// (TickStatusStale + StaleStageSnapshotLag) with the full batch carried
// forward and never calls the LLM — distinct from a genuine before-render
// stale. dispatchTick is max uint64, so the published AtTick can never exceed
// it and the freshness wait must time out (also proving the wait is bounded:
// an unbounded wait would hang and trip the test timeout). Regression guard
// for LLM-275.
func TestHarness_Preflight_SnapshotLag_LiveAttempt(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A") // alice genuinely in-flight under attempt-A
	defer cancel()

	// No scripted turns: if the harness reached the LLM this fake would error,
	// but the snapshot-lag return happens before render, so it is never called.
	h := newTestHarnessWithWaitMax(t, llm.NewFakeClient(), 2*time.Millisecond)

	warrants := []sim.WarrantMeta{{TriggerActorID: "bob", Reason: sim.BasicWarrantReason{K: sim.WarrantKindNPCSpoke}}}
	job := tickJob{actorID: "alice", attemptID: "attempt-A", rootEventID: 42, warrants: warrants, dispatchTick: ^uint64(0)}

	result := h.RunTick(context.Background(), w, job)
	if result.TerminalStatus != sim.TickStatusStale {
		t.Errorf("terminal status: got %v, want Stale", result.TerminalStatus)
	}
	if result.StaleStage != sim.StaleStageSnapshotLag {
		t.Errorf("StaleStage: got %v, want snapshot_lag", result.StaleStage)
	}
	if result.IterationCount != 0 {
		t.Errorf("IterationCount: got %d, want 0 (LLM must not be called on snapshot lag)", result.IterationCount)
	}
	if len(result.UnaddressedWarrants) != 1 {
		t.Errorf("UnaddressedWarrants: got %d, want 1 (full carry-forward)", len(result.UnaddressedWarrants))
	}
	if result.PreflightWait <= 0 {
		t.Errorf("PreflightWait: got %v, want > 0 (the wait was measured)", result.PreflightWait)
	}
}

// Actor absence is only authoritative from a FRESH snapshot. When the snapshot
// still predates the dispatch, a missing actor is snapshot lag, not a deleted
// actor — RunTick must retry (StaleStageSnapshotLag), NOT fail-before-render.
// Guards that the freshness gate runs BEFORE the actor-presence check (LLM-275).
func TestHarness_Preflight_SnapshotLag_ActorMissingIsNotGone(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	h := newTestHarnessWithWaitMax(t, llm.NewFakeClient(), 2*time.Millisecond)
	job := tickJob{actorID: "ghost", attemptID: "attempt-A", rootEventID: 1, dispatchTick: ^uint64(0)}

	result := h.RunTick(context.Background(), w, job)
	if result.TerminalStatus != sim.TickStatusStale || result.StaleStage != sim.StaleStageSnapshotLag {
		t.Errorf("missing actor on a stale snapshot: got %v/%v, want Stale/snapshot_lag",
			result.TerminalStatus, result.StaleStage)
	}
}

// A cancelled worker context (pool shutdown) must interrupt the freshness wait
// promptly rather than sit out the ceiling, and classify as Shutdown, not a
// snapshot-lag retry. Uses a 1h ceiling + a never-fresh snapshot so the wait
// would block ~forever if it ignored ctx; the pre-cancelled ctx must make
// RunTick return immediately. Guards the worker-starvation-on-shutdown concern.
func TestHarness_Preflight_ContextCanceledDuringWaitIsShutdown(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	h := newTestHarnessWithWaitMax(t, llm.NewFakeClient(), time.Hour)

	ctx, cancelCtx := context.WithCancel(context.Background())
	cancelCtx() // cancelled before RunTick even starts

	job := tickJob{actorID: "alice", attemptID: "attempt-A", rootEventID: 42, dispatchTick: ^uint64(0)}

	done := make(chan sim.TickResult, 1)
	go func() { done <- h.RunTick(ctx, w, job) }()
	select {
	case result := <-done:
		if result.TerminalStatus != sim.TickStatusShutdown {
			t.Errorf("cancelled preflight: got %v, want Shutdown", result.TerminalStatus)
		}
		if result.StaleStage == sim.StaleStageSnapshotLag {
			t.Errorf("cancelled preflight must not be classified snapshot_lag")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunTick did not return promptly on a cancelled ctx — the freshness wait ignored cancellation")
	}
}

// --- successful tick paths ----------------------------------------------

func TestHarness_Done_TerminatesAsDone(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "done", `{}`)},
	}})
	h, _ := newTestHarness(t, client, 0, 0)
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("terminal status: got %v, want Done", result.TerminalStatus)
	}
	if result.IterationCount != 1 {
		t.Errorf("IterationCount: got %d, want 1", result.IterationCount)
	}
	if got := result.ToolsRequested; len(got) != 1 || got[0] != "done" {
		t.Errorf("ToolsRequested: got %v, want [done]", got)
	}
}

// capturingPromptSink records every prompt the harness writes (ZBBS-HOME-360).
// RunTick is called synchronously in these tests, so no locking is needed.
type capturingPromptSink struct{ recs []sim.PromptRecord }

func (c *capturingPromptSink) WritePrompt(r sim.PromptRecord) { c.recs = append(c.recs, r) }

// A rendered tick captures its prompt to the PromptSink with the actor/attempt
// ids and the non-empty rendered text — the umbilical debug surface's feed.
func TestHarness_CapturesRenderedPrompt(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "done", `{}`)},
	}})
	tr := newTestRegistry(t)
	sink := &capturingPromptSink{}
	h, err := NewHarness(HarnessConfig{Client: client, Registry: tr.r, PromptSink: sink})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	job := newTestJob("attempt-A", nil)
	h.RunTick(context.Background(), w, job)

	if len(sink.recs) != 1 {
		t.Fatalf("want exactly 1 captured prompt, got %d", len(sink.recs))
	}
	rec := sink.recs[0]
	if rec.ActorID != job.actorID {
		t.Errorf("captured ActorID = %q, want %q", rec.ActorID, job.actorID)
	}
	if rec.AttemptID != job.attemptID {
		t.Errorf("captured AttemptID = %q, want %q", rec.AttemptID, job.attemptID)
	}
	if rec.Prompt == "" {
		t.Error("captured prompt text is empty; want the rendered deliberation prompt")
	}
}

// A nil PromptSink (umbilical disabled) is the default and must not panic or
// capture.
func TestHarness_NilPromptSink_NoCapture(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "done", `{}`)},
	}})
	h, _ := newTestHarness(t, client, 0, 0) // no PromptSink
	if h.promptSink != nil {
		t.Fatal("expected nil promptSink by default")
	}
	// Must run cleanly with no sink wired.
	if res := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil)); res.TerminalStatus != sim.TickStatusDone {
		t.Errorf("tick status = %v, want Done", res.TerminalStatus)
	}
}

func TestHarness_Observation_RunsInlineThenLoops(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	// Iter 1: recall (observation, continues). Iter 2: done.
	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{
			ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "recall", `{}`)},
		}},
		llm.ScriptedTurn{Response: llm.Response{
			ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "done", `{}`)},
		}},
	)
	h, tr := newTestHarness(t, client, 0, 0)
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if result.IterationCount != 2 {
		t.Errorf("IterationCount: got %d, want 2", result.IterationCount)
	}
	if got := *tr.observationLog; len(got) != 1 || got[0] != "recall" {
		t.Errorf("observation handler: got %v, want [recall]", got)
	}
	// Confirm transcript continuation — iter-2 input includes prior
	// assistant + tool (iter-2's own append happens AFTER the call).
	reqs := client.Requests()
	if len(reqs) != 2 {
		t.Fatalf("client calls: got %d, want 2", len(reqs))
	}
	if got := len(reqs[1].Messages); got != 3 {
		t.Errorf("iter-2 messages: got %d, want 3 (user/assistant/tool)", got)
	}
}

// TestHarness_StampsSimActorIdentity pins LLM-236: every deliberation turn a
// tick emits carries the acting in-world actor's id + display name on the
// llm.Request, so a shared-VA (salem-vendor) turn logged by memory-api can be
// attributed to the character rather than only the switchboard agent. The test
// world's actor is "alice" (DisplayName == id).
func TestHarness_StampsSimActorIdentity(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "done", `{}`)},
	}})
	h, _ := newTestHarness(t, client, 0, 0)

	if res := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil)); res.TerminalStatus != sim.TickStatusDone {
		t.Fatalf("tick status = %v, want Done", res.TerminalStatus)
	}

	reqs := client.Requests()
	if len(reqs) < 1 {
		t.Fatalf("client calls: got %d, want >=1", len(reqs))
	}
	if reqs[0].SimActorID != "alice" {
		t.Errorf("SimActorID = %q, want alice", reqs[0].SimActorID)
	}
	if reqs[0].SimActorName != "alice" {
		t.Errorf("SimActorName = %q, want alice", reqs[0].SimActorName)
	}
}

// Commit-dispatch end-to-end tests (move_to, note) require a valid
// rootEventID — sim.RunTickToolCommand wraps via newRootedCommand which
// rejects root > w.eventSeq. The test world's eventSeq starts at 0 and
// nothing in the unit-test setup emits an event. The commit path is
// short and reviewed inline; the end-to-end exercise lands in slice 4
// (pool integration test), where a real ReactorTickDue emit provides
// the legitimate rootEventID for the worker.

// --- multi-call semantics -----------------------------------------------

func TestHarness_MultiCall_PostTerminalSkipped(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	// One response with [recall, done, recall]. After done, the trailing
	// recall MUST be skipped + logged.
	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{
			newToolCall("c1", 0, "recall", `{}`),
			newToolCall("c2", 1, "done", `{}`),
			newToolCall("c3", 2, "recall", `{}`),
		},
	}})
	h, tr := newTestHarness(t, client, 0, 0)
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	// recall pre-terminal ran; trailing recall did NOT.
	if got := *tr.observationLog; len(got) != 1 {
		t.Errorf("observation calls: got %v, want exactly 1", got)
	}
	// The trailing call shows up in ToolsRequested AND in ToolsFailedRejected
	// (rejection reason = post_terminal).
	if !contains(result.ToolsRequested, "recall") {
		t.Errorf("ToolsRequested should include skipped recall")
	}
}

func TestHarness_MultiCall_ValidationFailureNonTerminal(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	// One response with [unknown_tool, done]. The unknown_tool call is a
	// validation failure (non-terminal); done should still execute.
	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{
			newToolCall("c1", 0, "ghost_tool", `{}`),
			newToolCall("c2", 1, "done", `{}`),
		},
	}})
	h, _ := newTestHarness(t, client, 0, 0)
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if !contains(result.ToolsFailedRejected, "ghost_tool") {
		t.Errorf("ToolsFailedRejected should include ghost_tool, got %v", result.ToolsFailedRejected)
	}
}

func TestHarness_MultiCall_DisabledToolRejected(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{
			ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "speak", `{}`)},
		}},
		llm.ScriptedTurn{Response: llm.Response{
			ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "done", `{}`)},
		}},
	)
	h, _ := newTestHarness(t, client, 0, 0)
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if !contains(result.ToolsFailedRejected, "speak") {
		t.Errorf("speak (disabled) should be rejected, got %v", result.ToolsFailedRejected)
	}
	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("disabled rejection should be non-terminal — got %v", result.TerminalStatus)
	}
}

func TestHarness_MultiCallCap_TruncatedSurfacedAsError(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	// Cap at 2. Response has 4 recall calls + nothing terminal.
	calls := []llm.RawToolCall{
		newToolCall("c1", 0, "recall", `{}`),
		newToolCall("c2", 1, "recall", `{}`),
		newToolCall("c3", 2, "recall", `{}`),
		newToolCall("c4", 3, "recall", `{}`),
	}
	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: calls}},
		llm.ScriptedTurn{Response: llm.Response{
			ToolCalls: []llm.RawToolCall{newToolCall("c5", 0, "done", `{}`)},
		}},
	)
	h, tr := newTestHarness(t, client, 0, 2)
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	// Only 2 recalls actually executed (the in-budget ones).
	if got := len(*tr.observationLog); got != 2 {
		t.Errorf("observation calls: got %d, want 2 (cap=2)", got)
	}
	// All 4 calls show up in ToolsRequested.
	recallCount := 0
	for _, n := range result.ToolsRequested {
		if n == "recall" {
			recallCount++
		}
	}
	if recallCount != 4 {
		t.Errorf("ToolsRequested recall count: got %d, want 4", recallCount)
	}
	// 2 of the recalls (the truncated ones) end up rejected.
	rejectedCount := 0
	for _, n := range result.ToolsFailedRejected {
		if n == "recall" {
			rejectedCount++
		}
	}
	if rejectedCount != 2 {
		t.Errorf("ToolsFailedRejected recall count: got %d, want 2 (the truncated calls)", rejectedCount)
	}
}

// --- LLM error classification -------------------------------------------

func TestHarness_LLMError_MapsToStatus(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus sim.TickTerminalStatus
		wantClass  string
	}{
		{"transport", &llm.Error{Class: llm.ErrorTransport}, sim.TickStatusFailedBeforeRender, "transport"},
		{"malformed", &llm.Error{Class: llm.ErrorMalformed}, sim.TickStatusFailedBeforeRender, "malformed"},
		{"too_large", &llm.Error{Class: llm.ErrorTooLarge}, sim.TickStatusFailedBeforeRender, "too_large"},
		{"refusal", &llm.Error{Class: llm.ErrorProviderRefusal}, sim.TickStatusFailedBeforeRender, "provider_refusal"},
		{"ctx", &llm.Error{Class: llm.ErrorContextCancelled, Cause: context.Canceled}, sim.TickStatusShutdown, "context_cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, cancel := newHarnessWorld(t, "attempt-A")
			defer cancel()
			client := llm.NewFakeClient(llm.ScriptedTurn{Err: tc.err})
			h, _ := newTestHarness(t, client, 0, 0)
			result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))
			if result.TerminalStatus != tc.wantStatus {
				t.Errorf("TerminalStatus: got %v, want %v", result.TerminalStatus, tc.wantStatus)
			}
			if result.LLMErrorClass != tc.wantClass {
				t.Errorf("LLMErrorClass: got %q, want %q", result.LLMErrorClass, tc.wantClass)
			}
		})
	}
}

func TestHarness_LLMError_AfterFirstIter_IsFailedAfterRender(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()
	// Iter 1: recall (success). Iter 2: transport failure.
	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{
			ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "recall", `{}`)},
		}},
		llm.ScriptedTurn{Err: &llm.Error{Class: llm.ErrorTransport, Message: "boom"}},
	)
	h, _ := newTestHarness(t, client, 0, 0)
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusFailedAfterRender {
		t.Errorf("post-first-iter LLM error: got %v, want FailedAfterRender", result.TerminalStatus)
	}
	if result.LLMErrorClass != "transport" {
		t.Errorf("LLMErrorClass: got %q, want transport", result.LLMErrorClass)
	}
}

// --- budget exhaustion --------------------------------------------------

// TestHarness_BudgetExhausted_CommitRounds — a model that keeps issuing
// NON-terminal commits exhausts the ACTION budget at IterationBudget. Commit
// rounds count (unlike observation-only rounds; ZBBS-WORK-321).
func TestHarness_BudgetExhausted_CommitRounds(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	// "note" is a non-terminal commit in the test registry — each round is an
	// action round, so budget=3 forces after 3 rounds.
	turns := make([]llm.ScriptedTurn, 5)
	for i := range turns {
		turns[i] = llm.ScriptedTurn{Response: llm.Response{
			ToolCalls: []llm.RawToolCall{newToolCall("c"+string(rune('a'+i)), 0, "note", `{}`)},
		}}
	}
	client := llm.NewFakeClient(turns...)
	h, _ := newTestHarness(t, client, 3, 0)
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusBudgetForced {
		t.Errorf("status: got %v, want BudgetForced", result.TerminalStatus)
	}
	if !result.BudgetHit {
		t.Errorf("BudgetHit: got false, want true")
	}
	if result.IterationCount != 3 {
		t.Errorf("IterationCount: got %d, want 3 (action budget)", result.IterationCount)
	}
}

// TestHarness_ObservationRoundsBoundedByCeiling — a model that ONLY ever
// recalls (observation) never consumes the action budget, but is still
// bounded: it forces at the hard per-tick ceiling = IterationBudget +
// MaxObservationRounds (ZBBS-WORK-321). With budget=3 + the default 3
// observation rounds, that's 6 LLM rounds.
func TestHarness_ObservationRoundsBoundedByCeiling(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	wantRounds := 3 + DefaultMaxObservationRounds
	turns := make([]llm.ScriptedTurn, wantRounds+2) // a couple extra so the ceiling, not the script, terminates
	for i := range turns {
		turns[i] = llm.ScriptedTurn{Response: llm.Response{
			ToolCalls: []llm.RawToolCall{newToolCall("c"+string(rune('a'+i)), 0, "recall", `{}`)},
		}}
	}
	client := llm.NewFakeClient(turns...)
	h, _ := newTestHarness(t, client, 3, 0)
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusBudgetForced {
		t.Errorf("status: got %v, want BudgetForced", result.TerminalStatus)
	}
	if !result.BudgetHit {
		t.Errorf("BudgetHit: got false, want true")
	}
	if result.IterationCount != wantRounds {
		t.Errorf("IterationCount: got %d, want %d (IterationBudget + MaxObservationRounds)", result.IterationCount, wantRounds)
	}
}

// TestHarness_ObservationRoundsDoNotConsumeActionBudget — the actual feature:
// the model spends several rounds recalling (thinking), THEN commits — and
// the commit lands even though total rounds already exceeded IterationBudget.
// Under the old "every round counts" rule the budget would have forced before
// the commit ever ran.
func TestHarness_ObservationRoundsDoNotConsumeActionBudget(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	// budget=2: 3 recall rounds (free thinking) then a terminal `done`. 4
	// total rounds > budget 2 — old behavior would have budget-forced at
	// round 2, never reaching the terminator. (`done` is ClassTerminal — it
	// ends the tick without a world command, so it's reliable in the bare
	// test world where commit dispatches can't resolve a root event.)
	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "recall", `{}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "recall", `{}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "recall", `{}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c4", 0, "done", `{}`)}}},
	)
	h, _ := newTestHarness(t, client, 2, 0)
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus == sim.TickStatusBudgetForced {
		t.Errorf("status: got BudgetForced — thinking rounds wrongly consumed the action budget")
	}
	if result.BudgetHit {
		t.Errorf("BudgetHit: got true, want false (the tick ended cleanly)")
	}
	if result.IterationCount != 4 {
		t.Errorf("IterationCount: got %d, want 4 (3 recall + 1 done)", result.IterationCount)
	}
}

// --- context cancellation -----------------------------------------------

func TestHarness_ContextCancelled_BeforeFirstIter_Shutdown(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "done", `{}`)},
	}})
	h, _ := newTestHarness(t, client, 0, 0)

	ctx, ctxCancel := context.WithCancel(context.Background())
	ctxCancel()
	result := h.RunTick(ctx, w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusShutdown {
		t.Errorf("status: got %v, want Shutdown", result.TerminalStatus)
	}
	if result.LLMErrorClass != "context_cancelled" {
		t.Errorf("LLMErrorClass: got %q, want context_cancelled", result.LLMErrorClass)
	}
}

// --- content-only response (no tool calls) ------------------------------

// LLM-378: a content-only response with spoken substance is NOT end-of-thought
// — words reach the scene only through speak(), so a bare-prose reply is heard
// by no one. The harness reprompts once; here the model then ends via done(),
// so the tick takes two iterations and ends TickStatusDone. (The RunTick-level
// pin of the reprompt; the speak-completes-it and one-shot-cap arms live in
// harness_bare_content_speak_test.go through the integration fixture. The empty
// content-only case — genuine end-of-thought, no reprompt — is covered there
// and by the noop-skip tests.)
func TestHarness_ContentOnlyReply_RepromptedThenEnds(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{Content: "I'll just sit and think", StopReason: "end_turn"}},
		doneTurn("d1"), // after the steer, the model ends its turn
	)
	h, _ := newTestHarness(t, client, 0, 0)
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done (reprompted content-only ends via done())", result.TerminalStatus)
	}
	if result.IterationCount != 2 {
		t.Errorf("IterationCount: got %d, want 2 (one reprompt round after the bare-content response)", result.IterationCount)
	}
}

// At-tool stale (TickStatusStale + StaleStageAtTool) requires the
// snapshot to still say in-flight @ attempt-A at preflight time, but
// the LIVE world to say attempt-B by the time RunTickToolCommand's
// guard runs on the world goroutine. That race is impossible to set
// up deterministically from a unit test without instrumentation
// hooks. The harness logic for the at-tool stale path is short and
// reviewed inline; end-to-end exercise lands in slice 4 (pool
// integration), where the supersede-mid-tick can be sequenced
// through the real ReactorTickDue / EvaluateReactors flow.

// --- LLM error chain unwrapping -----------------------------------------

func TestHarness_LLMError_UnwrapsContextChain(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	// A raw context.Canceled (not wrapped in *llm.Error) must still
	// classify as context_cancelled — Classify walks errors.Is.
	client := llm.NewFakeClient(llm.ScriptedTurn{Err: context.Canceled})
	h, _ := newTestHarness(t, client, 0, 0)
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusShutdown {
		t.Errorf("raw context.Canceled: got status %v, want Shutdown", result.TerminalStatus)
	}
}

// --- transcript shape ---------------------------------------------------

func TestHarness_TranscriptContinuation(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{
			ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "recall", `{}`)},
		}},
		llm.ScriptedTurn{Response: llm.Response{
			ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "done", `{}`)},
		}},
	)
	h, _ := newTestHarness(t, client, 0, 0)
	_ = h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	reqs := client.Requests()
	if len(reqs) != 2 {
		t.Fatalf("client calls: got %d, want 2", len(reqs))
	}
	// Iter 1: [user(rendered perception)]
	if len(reqs[0].Messages) != 1 || reqs[0].Messages[0].Role != llm.RoleUser {
		t.Errorf("iter-1 messages: got %v, want one user message", reqs[0].Messages)
	}
	// Iter 2: [user, assistant(tool_calls), tool(result)] — continuation
	if got := len(reqs[1].Messages); got != 3 {
		t.Errorf("iter-2 messages: got %d, want 3 (user + assistant + tool)", got)
	}
	if reqs[1].Messages[1].Role != llm.RoleAssistant {
		t.Errorf("iter-2 message[1]: got role %q, want assistant", reqs[1].Messages[1].Role)
	}
	if reqs[1].Messages[2].Role != llm.RoleTool {
		t.Errorf("iter-2 message[2]: got role %q, want tool", reqs[1].Messages[2].Role)
	}
	if reqs[1].Messages[2].ToolCallID != "c1" {
		t.Errorf("iter-2 tool ToolCallID: got %q, want c1", reqs[1].Messages[2].ToolCallID)
	}
}

// --- safety: panic on unknown class is impossible (typed constructors) --

func TestHarness_DispatchUnknownClass_TypeSystemEnforced(t *testing.T) {
	// The typed registration constructors (RegisterObservation /
	// RegisterCommit / RegisterTerminal) are the only way to add entries
	// and always set Class to one of the known values. The default branch
	// in dispatch() exists for type-system completeness, not as a reachable
	// runtime path — verified here by confirming registry construction
	// produces only valid classes.
	r := NewRegistry()
	_ = r.RegisterObservation("a", json.RawMessage(`{}`), passthroughDecode, func(_ context.Context, _ HandlerInput) (string, error) { return "", nil })
	_ = r.RegisterCommit("b", json.RawMessage(`{}`), passthroughDecode, func(_ HandlerInput) (sim.Command, error) { return sim.Command{}, nil }, true)
	_ = r.RegisterTerminal("c")
	for _, name := range []string{"a", "b", "c"} {
		entry, _ := r.Lookup(name)
		switch entry.Class {
		case ClassObservation, ClassCommit, ClassTerminal:
			// expected
		default:
			t.Errorf("entry %s: got class %v, want one of {Observation, Commit, Terminal}", name, entry.Class)
		}
	}
}

func TestCopyWarrants_EmptyAndPopulated(t *testing.T) {
	if got := copyWarrants(nil); got != nil {
		t.Errorf("copyWarrants(nil): got %v, want nil", got)
	}
	in := []sim.WarrantMeta{
		{TriggerActorID: "a", Reason: sim.BasicWarrantReason{K: sim.WarrantKindNPCSpoke}},
		{TriggerActorID: "b", Reason: sim.BasicWarrantReason{K: sim.WarrantKindNPCSpoke}},
	}
	out := copyWarrants(in)
	if len(out) != 2 {
		t.Fatalf("copyWarrants len: got %d, want 2", len(out))
	}
	// Mutate input — output should not change.
	in[0].TriggerActorID = "MUTATED"
	if out[0].TriggerActorID != "a" {
		t.Errorf("copyWarrants should be defensive against mutation; got %q", out[0].TriggerActorID)
	}
}

// R2 regression: Duration must be populated in the returned TickResult.
// The defer that stamps Duration mutates the named return slot — without
// the named return, callers see Duration == 0.
func TestHarness_R2_DurationPopulated(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	// Inject a clock that advances ~1ms between calls so Duration is non-zero.
	var ticks int64
	advancingClock := func() time.Time {
		ticks++
		return time.Unix(0, ticks*int64(time.Millisecond))
	}
	tr := newTestRegistry(t)
	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "done", `{}`)},
	}})
	h, err := NewHarness(HarnessConfig{Client: client, Registry: tr.r, Clock: advancingClock})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))
	if result.Duration <= 0 {
		t.Errorf("Duration: got %v, want > 0 (defer should mutate named return)", result.Duration)
	}
}

// R2 regression: when a stale-at-tool fires mid-batch, the stale call
// itself must be in ToolsFailedRejected (not just ToolsRequested), and
// subsequent in-budget calls must be in BOTH ToolsRequested and
// ToolsFailedRejected — the TickResult contract is that FailedRejected
// is a subset of Requested. (Cannot reproduce stale-at-tool in a unit
// test — see harness_integration_test.go for the end-to-end version of
// this invariant; this test exercises the equivalent diagnostic-shape
// invariant via a synthetic registry whose commit handler returns an
// error that ALSO uses a fake-stale outcome shape isn't trivial, so we
// verify the invariant via the at-tool integration test instead.)
//
// Kept as a placeholder to flag that the integration test in
// harness_integration_test.go is the authoritative check for this.

// R2 regression: handler errors must NOT leak err.Error() into the
// transcript — the model sees a stable public label only. Implementation
// details get logged separately.
func TestHarness_R2_HandlerErrorNotLeakedToTranscript(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	r := NewRegistry()
	// Observation handler that returns an internal-looking error.
	fn := func(_ context.Context, _ HandlerInput) (string, error) {
		return "", errors.New("PII-like-secret-token-abc123 from /etc/secrets/foo at line 42")
	}
	if err := r.RegisterObservation("recall", json.RawMessage(`{"type":"object"}`), passthroughDecode, fn); err != nil {
		t.Fatalf("register: %v", err)
	}

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{
			ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "recall", `{}`)},
		}},
		llm.ScriptedTurn{Response: llm.Response{
			ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "done", `{}`)},
		}},
	)
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	_ = h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	// Inspect the transcript handed to iter-2: it should contain the
	// stable label but NOT the leaky internal error message.
	reqs := client.Requests()
	if len(reqs) < 2 {
		t.Fatalf("client calls: got %d, want >= 2", len(reqs))
	}
	transcript := reqs[1].Messages
	var toolContent string
	for _, m := range transcript {
		if m.Role == llm.RoleTool && m.ToolCallID == "c1" {
			toolContent = m.Content
			break
		}
	}
	if !strings.Contains(toolContent, "[error: handler_failed]") {
		t.Errorf("tool content should carry the stable label; got %q", toolContent)
	}
	if strings.Contains(toolContent, "PII-like-secret-token-abc123") {
		t.Errorf("tool content LEAKED internal error details: %q", toolContent)
	}
	if strings.Contains(toolContent, "/etc/secrets/foo") {
		t.Errorf("tool content LEAKED file path: %q", toolContent)
	}
}

// Smoke: NewHarness with explicit Validator uses it rather than building default.
func TestHarness_NewHarness_UsesExplicitValidator(t *testing.T) {
	r := NewRegistry()
	v := NewValidator(r)
	h, err := NewHarness(HarnessConfig{Client: llm.NewFakeClient(), Registry: r, Validator: v})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	if h.validator != v {
		t.Errorf("explicit validator: harness using a different validator")
	}
}

// --- unused suppression --
var _ = errors.New
