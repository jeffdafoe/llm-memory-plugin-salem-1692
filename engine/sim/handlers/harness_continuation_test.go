package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_continuation_test.go — ZBBS-HOME-411: the post-speak body-swap. After
// the actor's first committed speak this tick, the harness stops sending the full
// per-tick perception furniture (affordances + the act-now coda) as the recency-
// dominant EphemeralContext and swaps to the lean continuation body, so a model
// that has already spoken reads a stop-biased decision instead of the cues that
// prime a re-pitch (live: Hannah Boggs re-pitching the same room three times in
// one tick on 2026-06-05, with the WORK-374/375 fixes already in place). HOME-402's
// speak cap stays as the backstop; this removes the prompt pressure that makes the
// cap fire. The swap is asserted via the per-round Request.EphemeralContext the
// FakeClient records.

// After the first committed speak, round 2's EphemeralContext is the lean
// continuation body, not round 1's full furniture.
func TestHarness_PostSpeakBodySwap_SwapsEphemeralAfterFirstSpeak(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "speak", `{"text":"Good evening to you."}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "done", `{}`)}}},
	)
	h, _ := newSpeakDedupHarness(t, client)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))
	if result.TerminalStatus != sim.TickStatusDone {
		t.Fatalf("status: got %v, want Done", result.TerminalStatus)
	}

	reqs := client.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: got %d, want 2 (speak round, done round)", len(reqs))
	}
	r0, r1 := reqs[0].EphemeralContext, reqs[1].EphemeralContext
	if r0 == r1 {
		t.Fatal("EphemeralContext must change after the first speak (round 1 full furniture → round 2 continuation body)")
	}
	// Round 2 is the stop-biased continuation body.
	for _, want := range []string{"already spoken", "re-pitch"} {
		if !strings.Contains(r1, want) {
			t.Errorf("round 2 EphemeralContext must be the continuation body (contain %q), got:\n%s", want, r1)
		}
	}
	// Round 1 is the full furniture, NOT the continuation body.
	if strings.Contains(r0, "already spoken") {
		t.Errorf("round 1 EphemeralContext must be the full furniture, not the continuation body, got:\n%s", r0)
	}
}

// A bounced (failed-dispatch) speak does NOT trigger the swap: the swap is keyed
// on a SUCCESSFUL committed speak, mirroring the dedup "a bounce doesn't poison
// the set" contract. Round 1 bounces, round 2 is done(); round 2 must still carry
// the full furniture.
func TestHarness_PostSpeakBodySwap_NoSwapOnBouncedSpeak(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "speak", `{"text":"Welcome to the inn."}`)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "done", `{}`)}}},
	)

	r := NewRegistry()
	speakFn := func(_ context.Context, _ HandlerInput) (string, error) {
		return "", errors.New("bounced: nobody to address")
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

	if result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil)); result.TerminalStatus != sim.TickStatusDone {
		t.Fatalf("status: got %v, want Done", result.TerminalStatus)
	}

	reqs := client.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests: got %d, want 2", len(reqs))
	}
	if strings.Contains(reqs[1].EphemeralContext, "already spoken") {
		t.Errorf("a bounced speak must NOT trigger the body-swap; round 2 should still be the full furniture, got:\n%s", reqs[1].EphemeralContext)
	}
}
