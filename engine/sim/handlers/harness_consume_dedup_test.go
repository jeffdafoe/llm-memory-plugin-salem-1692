package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_consume_dedup_test.go — LLM-91: consume left the ZBBS-HOME-414 syntactic
// allowlist (genericCallKey) because a byte-identical repeat consume while still in
// need is PRODUCTIVE — it eats another unit and eases the need further. The
// syntactic guard wrongly rejected it as a no-op (live: Elizabeth Ellis ate one
// cheese, was still peckish, then bounced four identical consume-Cheese calls off
// `already_did_that` to the loop cap). consume now owns a result-aware guard: only
// a consume that EASED NO NEED (sim.ConsumeResult.EasedNeed == false — already
// sated; it still ate and wasted a unit, since consuming while full wastes a unit
// by design) is the senseless action, and only a REPEAT of that is blocked.
//
// The end-to-end no-op-records-then-blocks path needs a commit that SUCCEEDS on the
// world goroutine (to produce a ConsumeResult), which the handlers unit harness
// can't drive — sim.RunTickToolCommand rejects rootEventID > eventSeq and nothing
// here emits an event (same boundary the move_to/note commit-dispatch tests note,
// deferred to pool integration). So the guard's two halves are covered at the unit
// level by the consumeNoop / consumeItemKey predicates plus the regression below
// that the syntactic guard no longer blocks an identical consume.

// consumeItemKey is the normalized item key the LLM-91 guard records/looks up by.
func TestConsumeItemKey(t *testing.T) {
	if k, ok := consumeItemKey(&ValidatedCall{Name: "consume", DecodedArgs: ConsumeArgs{Item: "Cheese", Qty: 1}}); !ok || k != "cheese" {
		t.Errorf("consume Cheese: got (%q,%v), want (\"cheese\",true)", k, ok)
	}
	// Lowercase + inner-whitespace-collapse, so a re-eat with cosmetic
	// spacing/case drift still matches the recorded no-op.
	if k, ok := consumeItemKey(&ValidatedCall{Name: "consume", DecodedArgs: ConsumeArgs{Item: "  Sharp   Cheddar ", Qty: 1}}); !ok || k != "sharp cheddar" {
		t.Errorf("consume normalization: got (%q,%v), want (\"sharp cheddar\",true)", k, ok)
	}
	// Qty is NOT part of the key — once an item fed nothing, re-eating it at ANY
	// quantity is the senseless repeat.
	k1, _ := consumeItemKey(&ValidatedCall{Name: "consume", DecodedArgs: ConsumeArgs{Item: "Cheese", Qty: 1}})
	k2, _ := consumeItemKey(&ValidatedCall{Name: "consume", DecodedArgs: ConsumeArgs{Item: "Cheese", Qty: 9}})
	if k1 != k2 {
		t.Errorf("qty must not affect the key: %q vs %q", k1, k2)
	}
	// Not a consume.
	if _, ok := consumeItemKey(&ValidatedCall{Name: "speak", DecodedArgs: SpeakArgs{Text: "hi"}}); ok {
		t.Error("speak must not yield a consume key")
	}
	// Empty item after normalization.
	if _, ok := consumeItemKey(&ValidatedCall{Name: "consume", DecodedArgs: ConsumeArgs{Item: "   ", Qty: 1}}); ok {
		t.Error("blank item must yield ok=false")
	}
	// Wrong decoded-args type / nil safety.
	if _, ok := consumeItemKey(&ValidatedCall{Name: "consume", DecodedArgs: "not-consume-args"}); ok {
		t.Error("mismatched args type must yield ok=false")
	}
	if _, ok := consumeItemKey(nil); ok {
		t.Error("nil call must yield ok=false")
	}
}

// consumeNoop is the predicate dispatch uses to flag a consume that eased no
// need (the senseless-repeat signal the guard arms on). NOTE it keys on
// EasedNeed, not Consumed: a sated consume still eats and wastes a unit
// (Consumed >= 1) by design, so Consumed == 0 never happens (LLM-107).
func TestConsumeNoop(t *testing.T) {
	// Fully sated: the unit was eaten and wasted (Consumed==1), but no need moved.
	if !consumeNoop(sim.ConsumeResult{Kind: "cheese", Requested: 5, Consumed: 1, Kept: 4, EasedNeed: false}) {
		t.Error("EasedNeed==false must be a no-op (sated consume wasted a unit, eased nothing)")
	}
	// Productive: the consume eased a need.
	if consumeNoop(sim.ConsumeResult{Kind: "cheese", Requested: 5, Consumed: 1, Kept: 4, EasedNeed: true}) {
		t.Error("EasedNeed==true must NOT be a no-op")
	}
	// Non-consume result types and absent results are never no-op consumes.
	if consumeNoop("some-other-result") {
		t.Error("a non-ConsumeResult must not be a no-op consume")
	}
	if consumeNoop(nil) {
		t.Error("nil result must not be a no-op consume")
	}
}

// Regression: two identical consume calls in one tick BOTH reach dispatch — the
// syntactic ZBBS-HOME-414 guard no longer blocks the second (that was the bug).
// The fake consume command fails on the world goroutine (the unit harness can't
// land a successful commit), so neither call records a no-op result and neither
// is blocked by the result-aware guard either; the CommitFn logs once per dispatch,
// so a log length of 2 proves the round-2 identical consume was NOT rejected
// before dispatch. Under the old behavior (consume on the syntactic allowlist) this
// would have been 1.
func TestHarness_ConsumeRepeat_NotBlockedByGenericGuard(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()

	r := NewRegistry()
	var actLog []string
	consumeFn := func(in HandlerInput) (sim.Command, error) {
		actLog = append(actLog, "consume")
		return sim.Command{Fn: func(*sim.World) (any, error) { return nil, nil }}, nil
	}
	if err := r.RegisterCommit("consume", json.RawMessage(`{"type":"object"}`), passthroughDecode, consumeFn, false); err != nil {
		t.Fatalf("register consume: %v", err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}

	const args = `{"item":"Cheese","qty":1}`
	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c1", 0, "consume", args)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c2", 0, "consume", args)}}},
		llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall("c3", 0, "done", `{}`)}}},
	)
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if len(actLog) != 2 {
		t.Errorf("CommitFn invocations: got %d, want 2 — an identical consume repeat must reach dispatch now, not be blocked by the syntactic guard", len(actLog))
	}
}
