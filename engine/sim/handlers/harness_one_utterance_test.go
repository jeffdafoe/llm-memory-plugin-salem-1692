package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_one_utterance_test.go — ZBBS-HOME-381: the one-utterance-per-tick
// cap. A non-terminal `speak` left the model free to loop back every round and,
// with no new input, re-pitch the same line until the iteration budget
// force-ended the tick (the live speak×6 ramble). The cap (a) ends the tick
// after the round a OneUtterancePerTick tool commits in and (b) drops a second
// such call in the same batch — while still letting it pair with a following
// terminal in one response.
//
// The cap keys on RegistryEntry.OneUtterancePerTick plus dispatch success; it
// is independent of tool class (success is set identically for observation and
// commit dispatch — harness.go). These tests therefore use an OBSERVATION tool
// `say` carrying the production marker, which exercises the exact harness
// branches without the causal-root scaffolding a commit dispatch needs (see the
// rootEventID note in harness_test.go / harness_integration_test.go). The real
// commit-class `speak` registration is covered by register_speak_test.go.

// newCappedSayHarness builds a harness whose registry has an OBSERVATION tool
// `say` marked WithOneUtterancePerTick, plus the terminal `done`. The `say`
// handler appends to the returned log so a test can assert how many utterances
// actually ran.
func newCappedSayHarness(t *testing.T, client llm.Client) (*Harness, *[]string) {
	t.Helper()
	r := NewRegistry()
	var sayLog []string
	sayFn := func(_ context.Context, _ HandlerInput) (string, error) {
		sayLog = append(sayLog, "say")
		return "[say: ok]", nil
	}
	if err := r.RegisterObservation("say", json.RawMessage(`{"type":"object"}`), passthroughDecode, sayFn, WithOneUtterancePerTick()); err != nil {
		t.Fatalf("register say: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	return h, &sayLog
}

// A single utterance ends the tick — the model is NOT re-prompted for another
// round. This is the core fix: one perception → at most one utterance.
func TestHarness_OneUtterancePerTick_EndsTickAfterSingleUtterance(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	// Turn 1: [say]. Turn 2 is another [say] that must NEVER be consumed — if
	// the cap is broken, the harness re-prompts and pulls this second turn.
	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "say", `{}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "say", `{}`)}}},
	)
	h, sayLog := newCappedSayHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusSuccess {
		t.Errorf("status: got %v, want Success", result.TerminalStatus)
	}
	if result.IterationCount != 1 {
		t.Errorf("IterationCount: got %d, want 1 (no re-prompt after the utterance)", result.IterationCount)
	}
	if n := len(client.Requests()); n != 1 {
		t.Errorf("LLM calls: got %d, want 1 (model must not be re-prompted after speaking)", n)
	}
	if got := *sayLog; len(got) != 1 {
		t.Errorf("utterances run: got %d, want exactly 1", len(got))
	}
	if got := result.ToolsSucceeded; len(got) != 1 || got[0] != "say" {
		t.Errorf("ToolsSucceeded: got %v, want [say]", got)
	}
}

// A response that emits [say, say] runs only the first; the second is rejected
// as already-spoken, and the tick still ends after the round.
func TestHarness_OneUtterancePerTick_DropsSecondUtteranceInSameBatch(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{
		newToolCall("c1", 0, "say", `{}`),
		newToolCall("c2", 1, "say", `{}`),
	}}})
	h, sayLog := newCappedSayHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusSuccess {
		t.Errorf("status: got %v, want Success", result.TerminalStatus)
	}
	if got := *sayLog; len(got) != 1 {
		t.Errorf("utterances run: got %d, want exactly 1 (second dropped, not dispatched)", len(got))
	}
	if got := result.ToolsSucceeded; len(got) != 1 || got[0] != "say" {
		t.Errorf("ToolsSucceeded: got %v, want [say]", got)
	}
	if !contains(result.ToolsFailedRejected, "say") {
		t.Errorf("ToolsFailedRejected should include the dropped second utterance, got %v", result.ToolsFailedRejected)
	}
	if result.IterationCount != 1 {
		t.Errorf("IterationCount: got %d, want 1", result.IterationCount)
	}
}

// A bounced (failed-dispatch) utterance does NOT burn the cap: utteredThisTick
// is only set on success, so the model gets another round to correct itself,
// and the tick ends only once an utterance actually lands. Models the
// production case of a speak rejected by world-state (success=false) via an
// observation handler that errors on its first call and succeeds on its second.
func TestHarness_OneUtterancePerTick_RejectedUtteranceDoesNotBurnCap(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	r := NewRegistry()
	calls := 0
	sayFn := func(_ context.Context, _ HandlerInput) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("bounced: nobody to address")
		}
		return "[say: ok]", nil
	}
	if err := r.RegisterObservation("say", json.RawMessage(`{"type":"object"}`), passthroughDecode, sayFn, WithOneUtterancePerTick()); err != nil {
		t.Fatalf("register say: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	// Round 1: [say] bounces. Round 2: [say] lands and ends the tick. If a
	// bounced speak wrongly burned the cap, round 1 would end the tick and this
	// second turn would never be consumed.
	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "say", `{}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "say", `{}`)}}},
	)
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusSuccess {
		t.Errorf("status: got %v, want Success", result.TerminalStatus)
	}
	if result.IterationCount != 2 {
		t.Errorf("IterationCount: got %d, want 2 (bounced speak must not end the tick)", result.IterationCount)
	}
	if calls != 2 {
		t.Errorf("say handler calls: got %d, want 2 (model retried after the bounce)", calls)
	}
	if n := len(client.Requests()); n != 2 {
		t.Errorf("LLM calls: got %d, want 2 (model re-prompted after the bounced utterance)", n)
	}
	if !contains(result.ToolsFailedRejected, "say") {
		t.Errorf("ToolsFailedRejected should include the bounced say, got %v", result.ToolsFailedRejected)
	}
	if !contains(result.ToolsSucceeded, "say") {
		t.Errorf("ToolsSucceeded should include the landed say, got %v", result.ToolsSucceeded)
	}
}

// A capped utterance alongside a terminal in ONE response runs both and ends on
// the terminal — the cap only kills the cross-round re-speak, it does not
// pre-empt a same-batch act (the production "say a line then move_to" pattern,
// modeled here with `done` since both end the batch before the cap is reached).
func TestHarness_OneUtterancePerTick_TerminalInSameBatchEndsTick(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{
		newToolCall("c1", 0, "say", `{}`),
		newToolCall("c2", 1, "done", `{}`),
	}}})
	h, sayLog := newCappedSayHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done (ended on the terminal, not the utterance cap)", result.TerminalStatus)
	}
	if got := *sayLog; len(got) != 1 {
		t.Errorf("utterances run: got %d, want 1 (say ran before the terminal)", len(got))
	}
	if result.IterationCount != 1 {
		t.Errorf("IterationCount: got %d, want 1", result.IterationCount)
	}
}
