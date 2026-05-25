package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_persist_test.go — tests for the post-terminal persist path:
// the harness's defer of llm.ToolResultPersister.PersistToolResults to
// close the v1 orphan-tool_use bug. See harness.go's
// persistTickToolResults doc for the rule (Done / Success /
// BudgetForced statuses with a non-empty trailing tool batch).

// newHarnessWorldWithAgent seeds alice with the given LLMAgent so the
// harness reads a non-empty model into req.Model + PersistRequest.Model.
// Existing newHarnessWorld leaves LLMAgent empty (no VA), which would
// gate the persist path off.
func newHarnessWorldWithAgent(t *testing.T, attemptID sim.TickAttemptID, agent string) (*sim.World, context.CancelFunc) {
	t.Helper()
	w, _, cancel := newTestWorld(t, 0)
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["alice"].LLMAgent = agent
		return nil, nil
	}}); err != nil {
		cancel()
		t.Fatalf("set LLMAgent: %v", err)
	}
	setInFlight(t, w, attemptID)
	return w, cancel
}

// --- happy path: terminal end persists last batch ------------------------

func TestHarness_Persist_OnTerminalDone(t *testing.T) {
	w, cancel := newHarnessWorldWithAgent(t, "attempt-A", "zbbs-josiah")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{newToolCall("c-term", 0, "done", `{}`)},
	}})
	h, _ := newTestHarness(t, client, 0, 0)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))
	if result.TerminalStatus != sim.TickStatusDone {
		t.Fatalf("status = %v, want Done", result.TerminalStatus)
	}

	persists := client.PersistRequests()
	if len(persists) != 1 {
		t.Fatalf("PersistRequests len = %d, want 1", len(persists))
	}
	pr := persists[0]
	if pr.Model != "zbbs-josiah" {
		t.Errorf("PersistRequest.Model = %q, want zbbs-josiah", pr.Model)
	}
	if pr.SceneID == "" {
		t.Errorf("PersistRequest.SceneID empty — harness must mint and thread one")
	}
	if len(pr.Results) != 1 {
		t.Fatalf("Results len = %d, want 1", len(pr.Results))
	}
	if pr.Results[0].ID != "c-term" {
		t.Errorf("Results[0].ID = %q, want c-term", pr.Results[0].ID)
	}
	if pr.Results[0].Content != "[done]" {
		t.Errorf("Results[0].Content = %q, want [done]", pr.Results[0].Content)
	}
}

// --- happy path: TerminalOnSuccess commit triggers persist --------------

// TestHarness_PersistTickToolResults_OnSuccess exercises the
// TickStatusSuccess persist path directly. Driving TickStatusSuccess
// through RunTick requires a real eventSeq (sim.RunTickToolCommand
// rejects rootEventID > w.eventSeq, which is 0 in unit-test worlds).
// Calling persistTickToolResults directly with a hand-built transcript
// covers the gate behavior without that plumbing.
func TestHarness_PersistTickToolResults_OnSuccess(t *testing.T) {
	client := llm.NewFakeClient()
	h, _ := newTestHarness(t, client, 0, 0)
	transcript := []llm.Message{
		{Role: llm.RoleUser, Content: "perception"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.RawToolCall{{ID: "c-move", Name: "move_to"}}},
		{Role: llm.RoleTool, ToolCallID: "c-move", Content: "[ok]"},
	}
	h.persistTickToolResults(context.Background(), "zbbs-josiah", "scene-x",
		transcript, sim.TickStatusSuccess)

	persists := client.PersistRequests()
	if len(persists) != 1 {
		t.Fatalf("PersistRequests len = %d, want 1", len(persists))
	}
	if persists[0].Model != "zbbs-josiah" {
		t.Errorf("Model = %q", persists[0].Model)
	}
	if persists[0].SceneID != "scene-x" {
		t.Errorf("SceneID = %q", persists[0].SceneID)
	}
	if len(persists[0].Results) != 1 || persists[0].Results[0].ID != "c-move" {
		t.Errorf("Results = %+v", persists[0].Results)
	}
}

// TestHarness_PersistTickToolResults_GateMatrix covers the status-gate
// table directly: each TerminalStatus value is checked for whether it
// triggers persist. Pairs with TestHarness_PersistTickToolResults_OnSuccess
// above so the full gate matrix has direct coverage.
func TestHarness_PersistTickToolResults_GateMatrix(t *testing.T) {
	transcript := []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.RawToolCall{{ID: "x"}}},
		{Role: llm.RoleTool, ToolCallID: "x", Content: "[ok]"},
	}
	cases := []struct {
		name        string
		status      sim.TickTerminalStatus
		wantPersist bool
	}{
		{"Done", sim.TickStatusDone, true},
		{"Success", sim.TickStatusSuccess, true},
		{"BudgetForced", sim.TickStatusBudgetForced, true},
		{"Skipped", sim.TickStatusSkipped, false},
		{"Shutdown", sim.TickStatusShutdown, false},
		{"Stale", sim.TickStatusStale, false},
		{"FailedBeforeRender", sim.TickStatusFailedBeforeRender, false},
		{"FailedAfterRender", sim.TickStatusFailedAfterRender, false},
		{"Unknown", sim.TickStatusUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := llm.NewFakeClient()
			h, _ := newTestHarness(t, client, 0, 0)
			h.persistTickToolResults(context.Background(), "zbbs-josiah", "scene-x",
				transcript, tc.status)
			got := len(client.PersistRequests())
			want := 0
			if tc.wantPersist {
				want = 1
			}
			if got != want {
				t.Errorf("status=%v: PersistRequests len = %d, want %d",
					tc.status, got, want)
			}
		})
	}
}

// --- happy path: budget-forced persists last batch -----------------------

func TestHarness_Persist_OnBudgetForced(t *testing.T) {
	w, cancel := newHarnessWorldWithAgent(t, "attempt-A", "zbbs-josiah")
	defer cancel()

	// recall is a non-terminal OBSERVATION, so recall-only rounds don't
	// consume the action budget (ZBBS-WORK-321) — the loop exhausts at the
	// hard per-tick ceiling (IterationBudget + MaxObservationRounds) instead.
	// At exhaustion the LAST round's recall tool-result trails in the
	// transcript and must be persisted.
	const iterBudget = 2
	rounds := iterBudget + DefaultMaxObservationRounds
	turns := make([]llm.ScriptedTurn, rounds)
	for i := range turns {
		turns[i] = llm.ScriptedTurn{Response: llm.Response{
			ToolCalls: []llm.RawToolCall{newToolCall("c"+string(rune('a'+i)), 0, "recall", `{}`)},
		}}
	}
	client := llm.NewFakeClient(turns...)
	h, _ := newTestHarness(t, client, iterBudget, 0)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))
	if result.TerminalStatus != sim.TickStatusBudgetForced {
		t.Fatalf("status = %v, want BudgetForced", result.TerminalStatus)
	}

	persists := client.PersistRequests()
	if len(persists) != 1 {
		t.Fatalf("PersistRequests len = %d, want 1", len(persists))
	}
	wantID := "c" + string(rune('a'+rounds-1))
	if persists[0].Results[0].ID != wantID {
		t.Errorf("Results[0].ID = %q, want %q (last round's call)", persists[0].Results[0].ID, wantID)
	}
}

// --- parallel tool calls in terminal batch -------------------------------

func TestHarness_Persist_ParallelToolCallsInTerminalBatch(t *testing.T) {
	w, cancel := newHarnessWorldWithAgent(t, "attempt-A", "zbbs-josiah")
	defer cancel()

	// Three-call batch: [recall, recall, done]. After done, the trailing
	// (none here) are skipped. Persist should carry all 3 tool results
	// (the two recalls + the done).
	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{
			newToolCall("c1", 0, "recall", `{}`),
			newToolCall("c2", 1, "recall", `{}`),
			newToolCall("c3", 2, "done", `{}`),
		},
	}})
	h, _ := newTestHarness(t, client, 0, 0)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))
	if result.TerminalStatus != sim.TickStatusDone {
		t.Fatalf("status = %v, want Done", result.TerminalStatus)
	}

	persists := client.PersistRequests()
	if len(persists) != 1 {
		t.Fatalf("PersistRequests len = %d, want 1", len(persists))
	}
	results := persists[0].Results
	if len(results) != 3 {
		t.Fatalf("Results len = %d, want 3", len(results))
	}
	wantIDs := []string{"c1", "c2", "c3"}
	for i, r := range results {
		if r.ID != wantIDs[i] {
			t.Errorf("Results[%d].ID = %q, want %q", i, r.ID, wantIDs[i])
		}
	}
}

// --- negative cases: no persist on stale/skipped/cancel ------------------

func TestHarness_Persist_SkippedOnStale(t *testing.T) {
	w, cancel := newHarnessWorldWithAgent(t, "attempt-A", "zbbs-josiah")
	defer cancel()

	// alice in-flight under attempt-A; job carries attempt-B. Preflight
	// stale-check returns Stale before any Complete.
	client := llm.NewFakeClient()
	h, _ := newTestHarness(t, client, 0, 0)

	warrants := []sim.WarrantMeta{{TriggerActorID: "bob", Reason: sim.BasicWarrantReason{K: sim.WarrantKindNPCSpoke}}}
	result := h.RunTick(context.Background(), w, newTestJob("attempt-B", warrants))
	if result.TerminalStatus != sim.TickStatusStale {
		t.Fatalf("status = %v, want Stale", result.TerminalStatus)
	}
	if got := len(client.PersistRequests()); got != 0 {
		t.Errorf("PersistRequests len = %d, want 0 (stale → no persist)", got)
	}
}

func TestHarness_Persist_SkippedOnContextCancel(t *testing.T) {
	w, cancel := newHarnessWorldWithAgent(t, "attempt-A", "zbbs-josiah")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{newToolCall("c-term", 0, "done", `{}`)},
	}})
	h, _ := newTestHarness(t, client, 0, 0)

	ctx, cancelTick := context.WithCancel(context.Background())
	cancelTick()
	result := h.RunTick(ctx, w, newTestJob("attempt-A", nil))
	if result.TerminalStatus != sim.TickStatusShutdown {
		t.Fatalf("status = %v, want Shutdown", result.TerminalStatus)
	}
	if got := len(client.PersistRequests()); got != 0 {
		t.Errorf("PersistRequests len = %d, want 0 (shutdown → no persist)", got)
	}
}

func TestHarness_Persist_SkippedOnNoopGate(t *testing.T) {
	w, cancel := newHarnessWorldWithAgent(t, "attempt-A", "zbbs-josiah")
	defer cancel()

	client := llm.NewFakeClient()
	h, _ := newTestHarness(t, client, 0, 0)

	// Low-info-only warrant batch → noop-skip gate trips (no LLM call).
	// IdleBackstop is the canonical low-info kind.
	warrants := []sim.WarrantMeta{{
		TriggerActorID: "alice",
		Reason:         sim.IdleBackstopWarrantReason{},
	}}
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", warrants))
	if result.TerminalStatus != sim.TickStatusSkipped {
		t.Fatalf("status = %v, want Skipped", result.TerminalStatus)
	}
	if got := len(client.PersistRequests()); got != 0 {
		t.Errorf("PersistRequests len = %d, want 0 (Skipped → no persist)", got)
	}
}

// --- gate: empty LLMAgent skips persist (no VA to route to) --------------

func TestHarness_Persist_SkippedWhenLLMAgentEmpty(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A") // no LLMAgent set
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{newToolCall("c-term", 0, "done", `{}`)},
	}})
	h, _ := newTestHarness(t, client, 0, 0)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))
	if result.TerminalStatus != sim.TickStatusDone {
		t.Fatalf("status = %v, want Done", result.TerminalStatus)
	}
	if got := len(client.PersistRequests()); got != 0 {
		t.Errorf("PersistRequests len = %d, want 0 (no LLMAgent → no persist)", got)
	}
}

// --- persist-error propagation: harness logs, tick still succeeds --------

func TestHarness_Persist_ErrorDoesNotAffectResult(t *testing.T) {
	w, cancel := newHarnessWorldWithAgent(t, "attempt-A", "zbbs-josiah")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{newToolCall("c-term", 0, "done", `{}`)},
	}})
	client.SetPersistError(errors.New("simulated persist failure"))
	h, _ := newTestHarness(t, client, 0, 0)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))
	// Persist failure must NOT change TerminalStatus. The tick's
	// authoritative outcome is Done; the persist error is purely
	// informational (logged in the harness).
	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status = %v, want Done (persist error must not affect status)", result.TerminalStatus)
	}
}

// --- model + scene plumbing on Complete calls ----------------------------

func TestHarness_CompleteCarriesModelAndScene(t *testing.T) {
	w, cancel := newHarnessWorldWithAgent(t, "attempt-A", "zbbs-josiah")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{newToolCall("c-term", 0, "done", `{}`)},
	}})
	h, _ := newTestHarness(t, client, 0, 0)

	_ = h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))
	reqs := client.Requests()
	if len(reqs) != 1 {
		t.Fatalf("Complete calls = %d, want 1", len(reqs))
	}
	if reqs[0].Model != "zbbs-josiah" {
		t.Errorf("Request.Model = %q, want zbbs-josiah", reqs[0].Model)
	}
	if reqs[0].SceneID == "" {
		t.Errorf("Request.SceneID empty — harness must mint and pass one")
	}
}

func TestHarness_CompleteIterationsShareSceneID(t *testing.T) {
	w, cancel := newHarnessWorldWithAgent(t, "attempt-A", "zbbs-josiah")
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
		t.Fatalf("Complete calls = %d, want 2", len(reqs))
	}
	if reqs[0].SceneID == "" || reqs[0].SceneID != reqs[1].SceneID {
		t.Errorf("scene IDs should match across iterations: %q vs %q",
			reqs[0].SceneID, reqs[1].SceneID)
	}
	// Persist should use the same scene as the tick.
	persists := client.PersistRequests()
	if len(persists) != 1 || persists[0].SceneID != reqs[0].SceneID {
		t.Errorf("Persist scene = %q, want %q (same as tick)",
			persists[0].SceneID, reqs[0].SceneID)
	}
}

// --- unit test: extractTrailingToolResults --------------------------------

func TestExtractTrailingToolResults(t *testing.T) {
	cases := []struct {
		name       string
		transcript []llm.Message
		wantIDs    []string
	}{
		{
			name: "empty",
		},
		{
			name: "no tool trailing",
			transcript: []llm.Message{
				{Role: llm.RoleUser, Content: "p"},
				{Role: llm.RoleAssistant, Content: "hi"},
			},
		},
		{
			name: "single trailing tool",
			transcript: []llm.Message{
				{Role: llm.RoleUser, Content: "p"},
				{Role: llm.RoleAssistant, ToolCalls: []llm.RawToolCall{{ID: "a"}}},
				{Role: llm.RoleTool, ToolCallID: "a", Content: "[ok]"},
			},
			wantIDs: []string{"a"},
		},
		{
			name: "parallel trailing tools",
			transcript: []llm.Message{
				{Role: llm.RoleUser, Content: "p"},
				{Role: llm.RoleAssistant, ToolCalls: []llm.RawToolCall{{ID: "a"}, {ID: "b"}}},
				{Role: llm.RoleTool, ToolCallID: "a", Content: "ra"},
				{Role: llm.RoleTool, ToolCallID: "b", Content: "rb"},
			},
			wantIDs: []string{"a", "b"},
		},
		{
			name: "skips earlier batches",
			transcript: []llm.Message{
				{Role: llm.RoleUser, Content: "p"},
				{Role: llm.RoleAssistant, ToolCalls: []llm.RawToolCall{{ID: "old"}}},
				{Role: llm.RoleTool, ToolCallID: "old", Content: "old-result"},
				{Role: llm.RoleAssistant, ToolCalls: []llm.RawToolCall{{ID: "new"}}},
				{Role: llm.RoleTool, ToolCallID: "new", Content: "new-result"},
			},
			wantIDs: []string{"new"},
		},
		{
			name: "skips tool without ID",
			transcript: []llm.Message{
				{Role: llm.RoleAssistant, ToolCalls: []llm.RawToolCall{{ID: "a"}, {ID: "b"}}},
				{Role: llm.RoleTool, Content: "no-id"}, // defensive — should skip
				{Role: llm.RoleTool, ToolCallID: "b", Content: "rb"},
			},
			wantIDs: []string{"b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractTrailingToolResults(tc.transcript)
			if len(got) != len(tc.wantIDs) {
				t.Fatalf("results len = %d, want %d", len(got), len(tc.wantIDs))
			}
			for i, r := range got {
				if r.ID != tc.wantIDs[i] {
					t.Errorf("[%d].ID = %q, want %q", i, r.ID, tc.wantIDs[i])
				}
			}
		})
	}
}
