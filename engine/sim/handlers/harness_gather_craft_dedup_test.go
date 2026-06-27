package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// harness_gather_craft_dedup_test.go — LLM-120: within-tick retry-spam guards for
// gather and craft. Both tools STARTED a within-tick loop in the wild — gather at
// a Blueberry Bush, craft×6 at the forge — because a repeat fell through to a
// domain error (gather "already busy") or a bare [ok] re-invite (craft), and the
// weak model re-fired to the iteration budget. Each is guarded by a name-only
// flag (gatheredThisTick / craftedThisTick) recorded ON SUCCESS, not by
// genericCallKey's byte-identical name+args key: gather's `qty` is vestigial
// (LLM-87) and craft's item resolves through aliases (LLM-113: Nail/nail/nails →
// one kind), so a byte-identical key would miss a drifted re-fire.
//
// Like the speak same-tick dedup (harness_dedup_test.go), the guards run
// identically for observation and commit dispatch, so these tests register the
// tool as an OBSERVATION tool decoding the real args — exercising the exact
// harness branches (the name-only guard + the success-block record) WITHOUT the
// causal-root scaffolding a commit dispatch needs (the rootEventID note in
// harness_test.go). The handler logs on success so a test can count how many
// calls actually landed.

// newObsDedupHarness builds a harness whose registry has an OBSERVATION tool
// `name` (using the real schema + decoder, logging on success via logArg) plus a
// terminal `done`.
func newObsDedupHarness(t *testing.T, client llm.Client, name string, schema json.RawMessage, decode func(json.RawMessage) (any, error), logArg func(any) string) (*Harness, *[]string) {
	t.Helper()
	r := NewRegistry()
	var log []string
	fn := func(_ context.Context, in HandlerInput) (string, error) {
		log = append(log, logArg(in.Args))
		return "[ok]", nil
	}
	if err := r.RegisterObservation(name, schema, decode, fn); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	if err := r.RegisterTerminal("done"); err != nil {
		t.Fatalf("register done: %v", err)
	}
	h, err := NewHarness(HarnessConfig{Client: client, Registry: r})
	if err != nil {
		t.Fatalf("NewHarness: %v", err)
	}
	return h, &log
}

// callTurn / doneTurn are one-call scripted-turn shorthands for these tests.
func callTurn(id, name, args string) llm.ScriptedTurn {
	return llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall(id, 0, name, args)}}}
}

func doneTurn(id string) llm.ScriptedTurn {
	return llm.ScriptedTurn{Response: llm.Response{ToolCalls: []llm.RawToolCall{newToolCall(id, 0, "done", `{}`)}}}
}

func gatherLogArg(any) string { return "gather" }

func craftLogArg(a any) string {
	if args, ok := a.(CraftArgs); ok {
		return args.Item
	}
	return ""
}

// A second gather this tick is rejected before dispatch — only the first lands.
func TestHarness_GatherRepeat_BlockedSameTick(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()
	client := llm.NewFakeClient(
		callTurn("g1", "gather", `{}`),
		callTurn("g2", "gather", `{}`),
		doneTurn("d1"),
	)
	h, log := newObsDedupHarness(t, client, "gather", gatherSchema, DecodeGatherArgs, gatherLogArg)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if len(*log) != 1 {
		t.Errorf("gather landings: got %d, want 1 — the second gather must be bounced before dispatch", len(*log))
	}
	if !contains(result.ToolsFailedRejected, "gather") {
		t.Errorf("ToolsFailedRejected should include the blocked repeat, got %v", result.ToolsFailedRejected)
	}
}

// The gather guard is name-only, so a qty-drifted re-fire is STILL blocked — qty
// is vestigial (LLM-87), so genericCallKey's byte-identical key would wrongly let
// `{"qty":5}` through after a bare `{}`. This is why gather isn't on that list.
func TestHarness_GatherRepeat_QtyDriftStillBlocked(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()
	client := llm.NewFakeClient(
		callTurn("g1", "gather", `{}`),
		callTurn("g2", "gather", `{"qty":5}`),
		doneTurn("d1"),
	)
	h, log := newObsDedupHarness(t, client, "gather", gatherSchema, DecodeGatherArgs, gatherLogArg)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if len(*log) != 1 {
		t.Errorf("gather landings: got %d, want 1 — a qty-drifted gather repeat must still be bounced", len(*log))
	}
}

// A bounced first gather is NOT recorded (record-on-success): the model may retry
// and have it land. This is the finding-1 property — a gather that failed for a
// reason that could change (within the harness's model) is not wrongly blocked.
func TestHarness_GatherBounced_NotRecorded(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()
	client := llm.NewFakeClient(
		callTurn("g1", "gather", `{}`),
		callTurn("g2", "gather", `{}`),
		doneTurn("d1"),
	)
	r := NewRegistry()
	var log []string
	calls := 0
	gatherFn := func(_ context.Context, in HandlerInput) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("bounced: not at a source")
		}
		log = append(log, "gather")
		return "[ok]", nil
	}
	if err := r.RegisterObservation("gather", gatherSchema, DecodeGatherArgs, gatherFn); err != nil {
		t.Fatalf("register gather: %v", err)
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
		t.Errorf("gather handler calls: got %d, want 2 — the retry must reach the handler (a bounced gather is not recorded)", calls)
	}
	if len(log) != 1 {
		t.Errorf("gather landings: got %d, want 1 (the retry landed)", len(log))
	}
}

// A second craft for the SAME good is rejected before dispatch — only the first
// lands.
func TestHarness_CraftRepeatSameItem_BlockedSameTick(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()
	client := llm.NewFakeClient(
		callTurn("c1", "produce", `{"item":"Nail"}`),
		callTurn("c2", "produce", `{"item":"Nail"}`),
		doneTurn("d1"),
	)
	h, log := newObsDedupHarness(t, client, "produce", craftSchema, DecodeCraftArgs, craftLogArg)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if len(*log) != 1 {
		t.Errorf("craft landings: got %d, want 1 — the second craft must be bounced before dispatch", len(*log))
	}
	if !contains(result.ToolsFailedRejected, "produce") {
		t.Errorf("ToolsFailedRejected should include the blocked repeat, got %v", result.ToolsFailedRejected)
	}
}

// Alias drift is STILL blocked: "Nail" then "nails" resolve to one kind
// (resolveItemKind, LLM-113), so the name-only craft guard catches the second
// even though its raw args differ — the gap a byte-identical genericCallKey key
// would miss.
func TestHarness_CraftAliasDrift_BlockedSameTick(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()
	client := llm.NewFakeClient(
		callTurn("c1", "produce", `{"item":"Nail"}`),
		callTurn("c2", "produce", `{"item":"nails"}`),
		doneTurn("d1"),
	)
	h, log := newObsDedupHarness(t, client, "produce", craftSchema, DecodeCraftArgs, craftLogArg)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if len(*log) != 1 {
		t.Errorf("craft landings: got %d, want 1 — an alias-drifted craft repeat must still be bounced", len(*log))
	}
}

// A craft for a DIFFERENT good is also blocked once a focus is chosen: a crafter
// forges one good at a time, so within-tick refocus only thrashes; the choice is
// reconsidered next tick. One production-focus choice per tick.
func TestHarness_CraftDifferentItem_BlockedSameTick(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()
	client := llm.NewFakeClient(
		callTurn("c1", "produce", `{"item":"Nail"}`),
		callTurn("c2", "produce", `{"item":"Skillet"}`),
		doneTurn("d1"),
	)
	h, log := newObsDedupHarness(t, client, "produce", craftSchema, DecodeCraftArgs, craftLogArg)

	result := h.RunTick(context.Background(), w, newTestJob("attempt-A", nil))

	if result.TerminalStatus != sim.TickStatusDone {
		t.Errorf("status: got %v, want Done", result.TerminalStatus)
	}
	if len(*log) != 1 {
		t.Errorf("craft landings: got %d, want 1 — a second craft this tick is blocked regardless of item", len(*log))
	}
}

// A bounced first craft (an unmakeable good) is NOT recorded: the model may
// re-call with a valid good and have it land — a failed focus choice is
// correctable within the tick.
func TestHarness_CraftBounced_NotRecorded(t *testing.T) {
	w, cancel := newHarnessWorld(t, "attempt-A")
	defer cancel()
	client := llm.NewFakeClient(
		callTurn("c1", "produce", `{"item":"Widget"}`),
		callTurn("c2", "produce", `{"item":"Nail"}`),
		doneTurn("d1"),
	)
	r := NewRegistry()
	var log []string
	calls := 0
	craftFn := func(_ context.Context, in HandlerInput) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("you don't make widget at your workplace")
		}
		log = append(log, craftLogArg(in.Args))
		return "[ok]", nil
	}
	if err := r.RegisterObservation("produce", craftSchema, DecodeCraftArgs, craftFn); err != nil {
		t.Fatalf("register craft: %v", err)
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
		t.Errorf("craft handler calls: got %d, want 2 — the retry with a valid good must reach the handler", calls)
	}
	if len(log) != 1 || log[0] != "Nail" {
		t.Errorf("craft landings: got %v, want [Nail] (the corrected craft landed)", log)
	}
}
