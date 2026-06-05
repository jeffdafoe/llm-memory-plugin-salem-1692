package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_dedup_test.go — ZBBS-WORK-375: the same-tick repetition guard that
// replaced HOME-381's hard one-utterance-per-tick cap. The cap ended the tick
// after the first speak; that stopped the budget_forced storm but also cut a
// legitimate two-beat turn (greet THEN a distinct answer). WORK-375 instead lets
// a speak be non-terminal (the model ends the turn with done()) and rejects only
// a normalized-exact REPEAT of something already said this tick — within the
// same response batch or on a later round. A distinct follow-up still commits.
//
// The guard keys on the speak tool by name + its SpeakArgs text (harness.go
// speakUtteranceKey) plus dispatch success, independent of tool class — the
// dedup check and the spokenThisTick insert run identically for observation and
// commit dispatch. So, like the HOME-381 tests these replace, these tests use an
// OBSERVATION tool named `speak` (decoding real SpeakArgs) to exercise the exact
// harness branches WITHOUT the causal-root scaffolding a commit dispatch needs
// (see the rootEventID note in harness_test.go). The real commit-class `speak`
// registration is covered by register_speak_test.go; the post-speak continuation
// steer in the tool result is covered by commit_result_content_test.go. The
// handler appends to the returned log on success so a test can assert how many
// utterances landed.

// newSpeakDedupHarness builds a harness whose registry has an OBSERVATION tool
// `speak` (decoding real SpeakArgs, logging the spoken text on success) plus the
// terminal `done`.
func newSpeakDedupHarness(t *testing.T, client llm.Client) (*Harness, *[]string) {
	t.Helper()
	r := NewRegistry()
	var spokeLog []string
	speakFn := func(_ context.Context, in HandlerInput) (string, error) {
		args, ok := in.Args.(SpeakArgs)
		if !ok {
			return "", errors.New("speak test handler: unexpected args type")
		}
		spokeLog = append(spokeLog, args.Text)
		return "[spoke: ok]", nil
	}
	if err := r.RegisterObservation("speak", speakSchema, DecodeSpeakArgs, speakFn, WithDescription(speakDescription)); err != nil {
		t.Fatalf("register speak: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	return h, &spokeLog
}

// speakUtteranceKey is correctness-critical and special-cased on BOTH the tool
// name and the decoded-args type, so pin it directly against the PRODUCTION
// decoder (DecodeSpeakArgs). If a future decoder refactor changes the decoded
// shape (e.g. to *SpeakArgs), the dedup guard would silently disable for the
// real speak tool and the storm could return — this test fails loudly instead.
// Also covers normalization and the non-speak / empty-text / nil fall-throughs.
func TestSpeakUtteranceKey_ProductionDecoderShapeAndFallthroughs(t *testing.T) {
	decoded, err := DecodeSpeakArgs(json.RawMessage(`{"text":"  Four   Coins. "}`))
	if err != nil {
		t.Fatalf("DecodeSpeakArgs: %v", err)
	}
	norm, ok := speakUtteranceKey(&ValidatedCall{Name: "speak", DecodedArgs: decoded})
	if !ok {
		t.Fatal("speakUtteranceKey ok=false for a production-decoded speak — dedup would be silently disabled in production")
	}
	if norm != "four coins." {
		t.Errorf("normalized key = %q, want %q", norm, "four coins.")
	}

	if _, ok := speakUtteranceKey(&ValidatedCall{Name: "move_to", DecodedArgs: decoded}); ok {
		t.Error("non-speak tool: want ok=false")
	}
	if _, ok := speakUtteranceKey(&ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: "   "}}); ok {
		t.Error("whitespace-only text: want ok=false")
	}
	if _, ok := speakUtteranceKey(nil); ok {
		t.Error("nil call: want ok=false")
	}
}

// A verbatim repeat on a LATER round is rejected; the model then ends with
// done(). This is the core fix: the storm's repeated line never reaches the
// transcript a second time, but the tick is no longer hard-capped at one speak.
func TestHarness_SameTickDedup_RejectsVerbatimRepeatAcrossRounds(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	const line = `A room for the night, four coins.`
	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "speak", `{"text":"`+line+`"}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "speak", `{"text":"`+line+`"}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, spokeLog := newSpeakDedupHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done (model ended with done() after the repeat was blocked)", result.TerminalStatus)
	}
	if got := *spokeLog; len(got) != 1 {
		t.Errorf("utterances committed: got %d %q, want exactly 1 (verbatim repeat blocked)", len(got), got)
	}
	if !contains(result.ToolsSucceeded, "speak") {
		t.Errorf("ToolsSucceeded should include the first speak, got %v", result.ToolsSucceeded)
	}
	if !contains(result.ToolsFailedRejected, "speak") {
		t.Errorf("ToolsFailedRejected should include the blocked repeat, got %v", result.ToolsFailedRejected)
	}
	if result.IterationCount != 3 {
		t.Errorf("IterationCount: got %d, want 3 (speak, blocked repeat, done)", result.IterationCount)
	}
}

// A response that emits [speak X, speak X, done] commits the first, drops the
// second as a same-tick repeat before dispatch, and ends on the terminal.
func TestHarness_SameTickDedup_DropsVerbatimRepeatInSameBatch(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	const line = `Four coins for the room.`
	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{
		newToolCall("c1", 0, "speak", `{"text":"`+line+`"}`),
		newToolCall("c2", 1, "speak", `{"text":"`+line+`"}`),
		newToolCall("c3", 2, "done", `{}`),
	}}})
	h, spokeLog := newSpeakDedupHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if got := *spokeLog; len(got) != 1 {
		t.Errorf("utterances committed: got %d %q, want 1 (second dropped before dispatch)", len(got), got)
	}
	if !contains(result.ToolsFailedRejected, "speak") {
		t.Errorf("ToolsFailedRejected should include the dropped repeat, got %v", result.ToolsFailedRejected)
	}
	if result.IterationCount != 1 {
		t.Errorf("IterationCount: got %d, want 1", result.IterationCount)
	}
}

// Two DISTINCT lines in one batch both commit — the greet-then-answer two-beat
// that HOME-381's count cap wrongly cut. This is the behavior WORK-375 restores.
func TestHarness_SameTickDedup_AllowsDistinctFollowup(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{
		newToolCall("c1", 0, "speak", `{"text":"Good evening, Jefferey."}`),
		newToolCall("c2", 1, "speak", `{"text":"A room is four coins for the night."}`),
		newToolCall("c3", 2, "done", `{}`),
	}}})
	h, spokeLog := newSpeakDedupHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if got := *spokeLog; len(got) != 2 {
		t.Errorf("utterances committed: got %d %q, want 2 (distinct lines both allowed)", len(got), got)
	}
	if result.IterationCount != 1 {
		t.Errorf("IterationCount: got %d, want 1", result.IterationCount)
	}
}

// A bounced (failed-dispatch) speak does NOT enter the dedup set: only a
// successful commit is recorded, so the model may retry the SAME line and have
// it land. Mirrors the HOME-381 "rejected utterance doesn't burn the cap" case.
func TestHarness_SameTickDedup_BouncedSpeakNotRecorded(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	const line = `Welcome to the inn.`
	// Round 1: speak bounces (handler errors). Round 2: the SAME line lands. If
	// the bounce had poisoned the dedup set, round 2 would be rejected and never
	// reach the handler.
	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "speak", `{"text":"`+line+`"}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "speak", `{"text":"`+line+`"}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)

	r := NewRegistry()
	var spokeLog []string
	calls := 0
	speakFn := func(_ context.Context, in HandlerInput) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("bounced: nobody to address")
		}
		args := in.Args.(SpeakArgs)
		spokeLog = append(spokeLog, args.Text)
		return "[spoke: ok]", nil
	}
	if err := r.RegisterObservation("speak", speakSchema, DecodeSpeakArgs, speakFn); err != nil {
		t.Fatalf("register speak: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if calls != 2 {
		t.Errorf("speak handler calls: got %d, want 2 (retry must reach the handler — bounce did not poison the dedup set)", calls)
	}
	if got := spokeLog; len(got) != 1 || got[0] != line {
		t.Errorf("utterances committed: got %v, want [%q] (retry landed)", got, line)
	}
	if !contains(result.ToolsSucceeded, "speak") {
		t.Errorf("ToolsSucceeded should include the landed retry, got %v", result.ToolsSucceeded)
	}
	if !contains(result.ToolsFailedRejected, "speak") {
		t.Errorf("ToolsFailedRejected should include the bounce, got %v", result.ToolsFailedRejected)
	}
}

// Dedup is on the NORMALIZED text — case-folded, trimmed, inner-whitespace
// collapsed — so a repeat that differs only in case/spacing is still blocked.
func TestHarness_SameTickDedup_NormalizesCaseAndWhitespace(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "speak", `{"text":"Four Coins."}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "speak", `{"text":"four   coins."}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, spokeLog := newSpeakDedupHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if got := *spokeLog; len(got) != 1 {
		t.Errorf("utterances committed: got %d %q, want 1 (case/whitespace-different repeat blocked)", len(got), got)
	}
	if !contains(result.ToolsFailedRejected, "speak") {
		t.Errorf("ToolsFailedRejected should include the normalized repeat, got %v", result.ToolsFailedRejected)
	}
}

// ZBBS-HOME-402: the speak cap. commitResultContent ASKS the model to call
// done() after a speak; a weak model ignores it and re-pitches a REWORDED line
// the exact same-tick dedup can't catch (live: Josiah's 13 reworded greetings in
// 35s). After MaxSpeaksPerTick (default 2) DISTINCT speaks land, the harness ends
// the tick (budget_forced) — teeth for the nudge. Distinct lines are used so the
// WORK-375 verbatim dedup never fires; this isolates the cap.
func TestHarness_SpeakCap_EndsTickAfterMaxSpeaks(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	// Default MaxSpeaksPerTick is 2. Three distinct speaks, EACH IN ITS OWN
	// response (round); the third round never runs because the round-boundary cap
	// ends the tick after the second committed speak. (The multi-speak-in-one-
	// response case is locked by TestHarness_SpeakCap_MultiSpeakInOneResponseAllCommit.)
	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "speak", `{"text":"Good evening to you."}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "speak", `{"text":"What goods have you today?"}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "speak", `{"text":"A third distinct line."}`)}}},
	)
	h, spokeLog := newSpeakDedupHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusBudgetForced {
		t.Errorf("status: got %v, want BudgetForced (tick ended at the speak cap)", result.TerminalStatus)
	}
	if got := *spokeLog; len(got) != 2 {
		t.Errorf("utterances committed: got %d %q, want 2 (cap stops the third)", len(got), got)
	}
	if result.IterationCount != 2 {
		t.Errorf("IterationCount: got %d, want 2 (the third round never ran)", result.IterationCount)
	}
	if !result.BudgetHit {
		t.Error("BudgetHit should be true when the speak cap ends the tick")
	}
}

// The cap does NOT fire below the limit: a single speak then done() ends as Done
// with one utterance — a normal one-beat turn is untouched (ZBBS-HOME-402).
func TestHarness_SpeakCap_SingleSpeakUnaffected(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "speak", `{"text":"Good evening to you."}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "done", `{}`)}}},
	)
	h, spokeLog := newSpeakDedupHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done (one speak, under the cap)", result.TerminalStatus)
	}
	if got := *spokeLog; len(got) != 1 {
		t.Errorf("utterances committed: got %d %q, want 1", len(got), got)
	}
}

// Round-boundary semantics (ZBBS-HOME-402): the cap caps ROUNDS of speaking, not
// speaks within a single response. A single response emitting 3 distinct speaks
// (no done()) commits ALL three, THEN the tick ends at the round boundary as
// BudgetForced — bounded by MaxToolCallsPerResponse + the same-tick dedup. This
// locks the deliberate behavior (vs. a strict mid-batch cap, which would wrongly
// flip the legit greet-then-answer-then-done batch in
// TestHarness_SameTickDedup_AllowsDistinctFollowup from Done to BudgetForced).
func TestHarness_SpeakCap_MultiSpeakInOneResponseAllCommit(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{
		newToolCall("c1", 0, "speak", `{"text":"Good evening to you."}`),
		newToolCall("c2", 1, "speak", `{"text":"What goods have you today?"}`),
		newToolCall("c3", 2, "speak", `{"text":"A third distinct line."}`),
	}}})
	h, spokeLog := newSpeakDedupHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusBudgetForced {
		t.Errorf("status: got %v, want BudgetForced (tick ends at the round boundary after the cap)", result.TerminalStatus)
	}
	if got := *spokeLog; len(got) != 3 {
		t.Errorf("utterances committed: got %d %q, want 3 (whole response batch commits before the round-boundary cap)", len(got), got)
	}
	if result.IterationCount != 1 {
		t.Errorf("IterationCount: got %d, want 1 (single response)", result.IterationCount)
	}
}
