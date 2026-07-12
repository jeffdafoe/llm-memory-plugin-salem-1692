package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_bare_content_speak_test.go — LLM-378: a keeper never engages a
// customer who greeted them.
//
// Live root cause: a weak model under a heavy character prompt (a stateful
// keeper's ~6.7k soul) answers a conversational turn as bare assistant prose
// — "Lewis! Good to see you, friend…" — with NO speak tool call. Speech
// reaches the scene only through the speak commit, so before this fix the
// harness treated a content-only response as "the model is done thinking" and
// ended the tick, silently discarding the reply. The customer, whose scene
// never showed the keeper's words, waited forever.
//
// The fix gives the model exactly ONE reprompt per tick: on a content-only
// response with spoken substance, the harness appends the model's own line and
// a steer telling it to say the words through speak(), then loops once more so
// the model can re-emit CLEAN speech text. These tests use the same real-speak
// integration fixture as harness_speak_terminal_test.go.

// bareContentTurn scripts a content-only response — prose with no tool call,
// the exact shape a keeper produced when it narrated a reply instead of
// calling speak().
func bareContentTurn(text string) llm.ScriptedTurn {
	return llm.ScriptedTurn{Response: llm.Response{Content: text}}
}

// A bare-prose reply is not dropped: the harness reprompts once, the model
// re-emits the line through speak(), and that speak commit ends the tick. Two
// LLM rounds — the wasted "silent" round becomes a heard reply.
func TestHarness_BareContentReply_NudgedToSpeak(t *testing.T) {
	client := llm.NewFakeClient(
		bareContentTurn("*I straighten up behind the counter.* Lewis! Good to see you, friend."),
		callTurn("c1", "speak", `{"text":"Lewis! Good to see you, friend."}`),
	)
	f := newIntegrationFixture(t, newSpeakTerminalRegistry(t), client)
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
		t.Errorf("terminal_status: got %q, want \"success\" — the reprompted speak ends the tick", got)
	}

	reqs := client.Requests()
	if len(reqs) != 2 {
		t.Fatalf("LLM rounds: got %d, want 2 — a bare-prose reply must be reprompted, not dropped", len(reqs))
	}

	// The second round's transcript must carry the model's own bare line
	// followed by the steer, so the model re-says it through speak().
	msgs := reqs[1].Messages
	if len(msgs) < 2 {
		t.Fatalf("round 2 transcript too short: %d messages", len(msgs))
	}
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleUser || !strings.Contains(last.Content, "[not spoken]") {
		t.Errorf("round 2 last message: got role=%q content=%q, want a RoleUser steer containing \"[not spoken]\"", last.Role, last.Content)
	}
	prior := msgs[len(msgs)-2]
	if prior.Role != llm.RoleAssistant || !strings.Contains(prior.Content, "Lewis! Good to see you") {
		t.Errorf("round 2 second-to-last message: got role=%q, want the assistant's own bare-prose line echoed back", prior.Role)
	}
}

// The reprompt is one-shot: a model that answers with bare prose AGAIN after
// the steer is not chased further. Two bare-content rounds, then the tick ends
// as a plain content-only success — the third (speak) turn is never reached, so
// a stubborn model can't burn the whole round budget re-narrating.
func TestHarness_BareContentTwice_EndsAfterOneNudge(t *testing.T) {
	client := llm.NewFakeClient(
		bareContentTurn("Lewis! Good to see you, friend."),
		bareContentTurn("I said, good to see you!"),
		callTurn("c1", "speak", `{"text":"unreached"}`), // must NOT run — one nudge only
	)
	f := newIntegrationFixture(t, newSpeakTerminalRegistry(t), client)
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
		t.Errorf("terminal_status: got %q, want \"success\" — a second bare-content response ends the tick", got)
	}
	if n := len(client.Requests()); n != 2 {
		t.Errorf("LLM rounds: got %d, want 2 — exactly one reprompt, then the tick ends (the third scripted turn must never run)", n)
	}
}

// A genuinely empty content-only response is NOT reprompted — it is the model
// signalling it has nothing to say, the original "done thinking" semantics.
// One round, no steer, clean success.
func TestHarness_EmptyContentNoTool_EndsImmediately(t *testing.T) {
	client := llm.NewFakeClient(
		bareContentTurn("   "),                          // whitespace only — no spoken substance
		callTurn("c1", "speak", `{"text":"unreached"}`), // must NOT run
	)
	f := newIntegrationFixture(t, newSpeakTerminalRegistry(t), client)
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
		t.Errorf("terminal_status: got %q, want \"success\"", got)
	}
	if n := len(client.Requests()); n != 1 {
		t.Errorf("LLM rounds: got %d, want 1 — an empty content-only response ends the tick without a reprompt", n)
	}
}

// Budget edge: a bare-content reply that lands on the FINAL allowed round is
// NOT reprompted — there's no round left to spend on it, and nudging anyway
// would `continue` out of the exhausted loop into a misleading BudgetForced
// with the reply dropped. Instead the harness ends the tick cleanly as Success,
// preserving the pre-LLM-378 content-only semantics at the ceiling. Here a
// recall round (observation, doesn't consume the action budget) burns round 0
// so the bare content arrives on the last round of a 2-round ceiling.
func TestHarness_BareContentOnFinalRound_EndsSuccessNotBudgetForced(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		callTurn("c1", "recall", `{}`),                  // round 0: observation, loops on
		bareContentTurn("Lewis! Good to see you."),      // round 1 (final): no reprompt round left
		callTurn("c2", "speak", `{"text":"unreached"}`), // must NOT run
	)
	// IterationBudget 1 + MaxObservationRounds 1 → maxTotalRounds == 2, so the
	// recall (observation, budget-free) and the bare-content round exhaust the
	// ceiling with no reprompt round to spare.
	tr := newTestRegistry(t)
	h, err := NewHarness(HarnessConfig{Client: client, Registry: tr.r, IterationBudget: 1, MaxObservationRounds: 1})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusSuccess {
		t.Errorf("status: got %v, want Success — bare content on the final round ends cleanly, not BudgetForced", result.TerminalStatus)
	}
	if result.IterationCount != 2 {
		t.Errorf("IterationCount: got %d, want 2 (recall round then the final bare-content round)", result.IterationCount)
	}
	if n := len(client.Requests()); n != 2 {
		t.Errorf("LLM rounds: got %d, want 2 — no reprompt round is spent at the ceiling (the third scripted turn must never run)", n)
	}
}
