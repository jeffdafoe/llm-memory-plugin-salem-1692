package handlers

import (
	"context"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_unfinished_intent_test.go — LLM-414. A batch that queues COMMIT
// calls after a terminal one drops them (invariant 3) — but the drop is the
// actor's own declared, unfinished intent, so RunTick must surface the
// commit-class names in result.SkippedIntentTools for CompleteReactorTick to
// stamp a prompt re-tick. Exclusions under test: speak (by name — re-saying
// is the LLM-184 storm), non-commit classes (done / observations), unknown
// tools, and the one-retry guard (a tick itself triggered by an
// unfinished_intent warrant collects nothing).
//
// done stands in for the terminal call — unit-test commit dispatch has no
// valid rootEventID (see the note above TestHarness_MultiCall_PostTerminalSkipped);
// the post-terminal skip loop runs identically for any ended batch.

// TestHarness_PostTerminalSkip_CollectsCommitIntent: batch =
// [done (terminal), note (commit), recall (observation), speak (excluded by
// name), ghost (unknown), move_to (commit)]. done ends the batch; the
// unfinished commit intent is exactly [note, move_to].
func TestHarness_PostTerminalSkip_CollectsCommitIntent(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{
			newToolCall("c1", 0, "done", `{}`),
			newToolCall("c2", 1, "note", `{}`),
			newToolCall("c3", 2, "recall", `{}`),
			newToolCall("c4", 3, "speak", `{}`),
			newToolCall("c5", 4, "ghost_tool", `{}`),
			newToolCall("c6", 5, "move_to", `{}`),
		},
	}})
	h, _ := newTestHarness(t, client, 0, 0)
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Fatalf("status: got %v, want Done", result.TerminalStatus)
	}
	want := []string{"note", "move_to"}
	if len(result.SkippedIntentTools) != len(want) ||
		result.SkippedIntentTools[0] != want[0] || result.SkippedIntentTools[1] != want[1] {
		t.Errorf("SkippedIntentTools = %v, want %v", result.SkippedIntentTools, want)
	}
}

// TestHarness_PostTerminalSkip_NoIntentWithoutSkippedCommits: a clean
// terminal batch (nothing queued after) declares no unfinished intent.
func TestHarness_PostTerminalSkip_NoIntentWithoutSkippedCommits(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "done", `{}`)},
	}})
	h, _ := newTestHarness(t, client, 0, 0)
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if len(result.SkippedIntentTools) != 0 {
		t.Errorf("SkippedIntentTools = %v, want empty for a clean terminal batch", result.SkippedIntentTools)
	}
}

// TestHarness_PostTerminalSkip_OneRetryOnly: a tick triggered by an
// unfinished_intent warrant never collects intent again — one retry per
// split, never a self-sustaining re-tick chain.
func TestHarness_PostTerminalSkip_OneRetryOnly(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{
		ToolCalls: []llm.RawToolCall{
			newToolCall("c1", 0, "done", `{}`),
			newToolCall("c2", 1, "note", `{}`),
		},
	}})
	h, _ := newTestHarness(t, client, 0, 0)
	warrants := []sim.WarrantMeta{{
		TriggerActorID: "alice",
		Reason:         sim.UnfinishedIntentWarrantReason{Tools: "note"},
	}}
	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", warrants))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Fatalf("status: got %v, want Done", result.TerminalStatus)
	}
	if len(result.SkippedIntentTools) != 0 {
		t.Errorf("SkippedIntentTools = %v on an unfinished_intent-triggered tick — the one-retry guard failed (re-tick storm)", result.SkippedIntentTools)
	}
}
