package cascade

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// narration_expansion_test.go — driver-side tests for the narration
// expansion cascade (ZBBS-WORK-399): one full expansion cycle against
// the FakeClient, the reply-contract rejections, duplicate dropping,
// the durable-first persist gate, and the parse helpers. The substrate
// (draw counting, threshold nudge, apply/merge) is covered in
// engine/sim/narration_pool_test.go.

// narrationFakeSink records Append calls and optionally fails them.
type narrationFakeSink struct {
	appends []narrationAppend
	err     error
}

type narrationAppend struct {
	PoolKey     string
	Phrases     []string
	GeneratedBy string
}

func (s *narrationFakeSink) Append(_ context.Context, poolKey string, phrases []string, generatedBy string) error {
	if s.err != nil {
		return s.err
	}
	s.appends = append(s.appends, narrationAppend{PoolKey: poolKey, Phrases: append([]string(nil), phrases...), GeneratedBy: generatedBy})
	return nil
}

// buildNarrationDriverWorld loads a world, installs sink BEFORE starting
// the world goroutine (the setter contract: before Run, or from a
// Command), and runs it until the returned stop func.
func buildNarrationDriverWorld(t *testing.T, sink sim.NarrationExpansionSink) (*sim.World, func()) {
	t.Helper()
	repo, _ := mem.NewRepository()
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	w.SetNarrationExpansionSink(sink)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, func() {
		cancel()
		<-done
	}
}

// fetchNarrationContext round-trips the pool snapshot through the world.
func fetchNarrationContext(t *testing.T, w *sim.World, key string) sim.NarrationExpansionContext {
	t.Helper()
	res, err := w.SendContext(context.Background(), sim.FetchNarrationExpansionContext(key))
	if err != nil {
		t.Fatalf("fetch %q: %v", key, err)
	}
	return res.(sim.NarrationExpansionContext)
}

// retireReplyJSON builds a contract-correct reply with n distinct novel
// lines for the token-less retire pool.
func retireReplyJSON(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("\"Goodnight to you all — my bed calls, number %d.\"", i+1)
	}
	return `{"phrases": [` + strings.Join(lines, ", ") + `]}`
}

func TestRunOneNarrationExpansion_HappyPath(t *testing.T) {
	sink := &narrationFakeSink{}
	w, stop := buildNarrationDriverWorld(t, sink)
	defer stop()

	before := fetchNarrationContext(t, w, sim.NarrationKeyNPCRetire)
	client := llm.NewFakeClient(llm.ScriptedTurn{
		Response: llm.Response{Content: retireReplyJSON(before.Wanted)},
	})

	runOneNarrationExpansion(context.Background(), w, client, sim.NarrationKeyNPCRetire)

	after := fetchNarrationContext(t, w, sim.NarrationKeyNPCRetire)
	if got, want := len(after.Phrases), len(before.Phrases)+before.Wanted; got != want {
		t.Errorf("pool grew to %d lines, want %d", got, want)
	}
	if len(sink.appends) != 1 {
		t.Fatalf("sink got %d appends, want 1", len(sink.appends))
	}
	if sink.appends[0].PoolKey != sim.NarrationKeyNPCRetire {
		t.Errorf("sink pool key = %q", sink.appends[0].PoolKey)
	}
	if len(sink.appends[0].Phrases) != before.Wanted {
		t.Errorf("sink got %d phrases, want %d", len(sink.appends[0].Phrases), before.Wanted)
	}
	if sink.appends[0].GeneratedBy != narrationExpansionLLMModel {
		t.Errorf("generated_by = %q, want %q", sink.appends[0].GeneratedBy, narrationExpansionLLMModel)
	}

	// The one request must self-frame: model routing, fresh scene, no
	// tools, and a prompt carrying the existing lines + the count.
	reqs := client.Requests()
	if len(reqs) != 1 {
		t.Fatalf("FakeClient saw %d requests, want 1", len(reqs))
	}
	req := reqs[0]
	if req.Model != narrationExpansionLLMModel {
		t.Errorf("Model = %q", req.Model)
	}
	if req.SceneID == "" {
		t.Error("SceneID empty — expansion must isolate its scene")
	}
	if len(req.Tools) != 0 {
		t.Errorf("Tools = %d, want none", len(req.Tools))
	}
	prompt := req.Messages[0].Content
	if !strings.Contains(prompt, before.Phrases[0]) {
		t.Error("prompt missing the existing pool lines")
	}
	if !strings.Contains(prompt, fmt.Sprintf("exactly %d new lines", before.Wanted)) {
		t.Error("prompt missing the exact count instruction")
	}
	if !strings.Contains(prompt, "Do not use any {token} placeholders.") {
		t.Error("token-less pool prompt missing the no-token rule")
	}
}

func TestRunOneNarrationExpansion_RejectsContractViolations(t *testing.T) {
	cases := []struct {
		name  string
		reply string
	}{
		{"wrong count", retireReplyJSON(sim.NarrationExpansionBatchSize - 1)},
		{"not json", "Here are five lovely phrases for you!"},
		{"unknown field", `{"phrases": ["A line.", "B line.", "C line.", "D line.", "E line."], "note": "hi"}`},
		{"forbidden token", `{"phrases": ["Goodnight, {customer}.", "B line.", "C line.", "D line.", "E line."]}`},
		{"in-batch duplicate", `{"phrases": ["Same line.", "same line.", "C line.", "D line.", "E line."]}`},
		{"trailing JSON object", retireReplyJSON(sim.NarrationExpansionBatchSize) + ` {"phrases": []}`},
		{"trailing prose", retireReplyJSON(sim.NarrationExpansionBatchSize) + "\nThere you go — five fresh lines!"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sink := &narrationFakeSink{}
			w, stop := buildNarrationDriverWorld(t, sink)
			defer stop()

			before := fetchNarrationContext(t, w, sim.NarrationKeyNPCRetire)
			client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{Content: tc.reply}})

			runOneNarrationExpansion(context.Background(), w, client, sim.NarrationKeyNPCRetire)

			after := fetchNarrationContext(t, w, sim.NarrationKeyNPCRetire)
			if len(after.Phrases) != len(before.Phrases) {
				t.Errorf("rejected reply still grew the pool: %d → %d", len(before.Phrases), len(after.Phrases))
			}
			if len(sink.appends) != 0 {
				t.Errorf("rejected reply reached the sink: %v", sink.appends)
			}
		})
	}
}

func TestRunOneNarrationExpansion_DropsPoolDuplicates(t *testing.T) {
	sink := &narrationFakeSink{}
	w, stop := buildNarrationDriverWorld(t, sink)
	defer stop()

	before := fetchNarrationContext(t, w, sim.NarrationKeyNPCRetire)
	// Contract-valid batch where two lines already exist in the pool —
	// dropped, not rejected; the three novel lines land.
	reply := fmt.Sprintf(`{"phrases": [%q, %q, "Novel line one.", "Novel line two.", "Novel line three."]}`,
		before.Phrases[0], strings.ToUpper(before.Phrases[1]))
	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{Content: reply}})

	runOneNarrationExpansion(context.Background(), w, client, sim.NarrationKeyNPCRetire)

	after := fetchNarrationContext(t, w, sim.NarrationKeyNPCRetire)
	if got, want := len(after.Phrases), len(before.Phrases)+3; got != want {
		t.Errorf("pool grew to %d, want %d (3 novel)", got, want)
	}
	if len(sink.appends) != 1 || len(sink.appends[0].Phrases) != 3 {
		t.Errorf("sink appends = %+v, want one append of 3", sink.appends)
	}
}

func TestRunOneNarrationExpansion_PersistFailureDiscards(t *testing.T) {
	sink := &narrationFakeSink{err: errors.New("pg down")}
	w, stop := buildNarrationDriverWorld(t, sink)
	defer stop()

	before := fetchNarrationContext(t, w, sim.NarrationKeyNPCRetire)
	client := llm.NewFakeClient(llm.ScriptedTurn{
		Response: llm.Response{Content: retireReplyJSON(before.Wanted)},
	})

	runOneNarrationExpansion(context.Background(), w, client, sim.NarrationKeyNPCRetire)

	after := fetchNarrationContext(t, w, sim.NarrationKeyNPCRetire)
	if len(after.Phrases) != len(before.Phrases) {
		t.Errorf("persist failure still grew the live pool: %d → %d (durable-first violated)", len(before.Phrases), len(after.Phrases))
	}
}

func TestRunOneNarrationExpansion_LLMErrorLeavesPoolUntouched(t *testing.T) {
	sink := &narrationFakeSink{}
	w, stop := buildNarrationDriverWorld(t, sink)
	defer stop()

	before := fetchNarrationContext(t, w, sim.NarrationKeyNPCRetire)
	client := llm.NewFakeClient(llm.ScriptedTurn{
		Err: &llm.Error{Class: llm.ErrorTransport, Message: "boom"},
	})

	runOneNarrationExpansion(context.Background(), w, client, sim.NarrationKeyNPCRetire)

	after := fetchNarrationContext(t, w, sim.NarrationKeyNPCRetire)
	if len(after.Phrases) != len(before.Phrases) || len(sink.appends) != 0 {
		t.Error("LLM failure must leave the pool and sink untouched")
	}
}

func TestParseNarrationExpansionReply_FenceTolerance(t *testing.T) {
	nctx := sim.NarrationExpansionContext{Wanted: 2}
	fenced := "```json\n{\"phrases\": [\"Line the first.\", \"Line the second.\"]}\n```"
	phrases, reason := parseNarrationExpansionReply(fenced, nctx)
	if reason != "" {
		t.Fatalf("fenced contract reply rejected: %s", reason)
	}
	if len(phrases) != 2 {
		t.Errorf("got %d phrases", len(phrases))
	}
}

func TestStripCodeFence(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"no fence", `{"a":1}`, `{"a":1}`},
		{"plain fence", "```\n{\"a\":1}\n```", `{"a":1}`},
		{"json fence", "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"unterminated fence", "```json\n{\"a\":1}", "```json\n{\"a\":1}"},
		{"fence with trailer", "```json\n{\"a\":1}\n``` extra", "```json\n{\"a\":1}\n``` extra"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripCodeFence(tc.in); got != tc.want {
				t.Errorf("stripCodeFence(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRegisterNarrationExpansion_NilGuards(t *testing.T) {
	w, stop := buildNarrationDriverWorld(t, &narrationFakeSink{})
	defer stop()
	assertPanics := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: want panic", name)
			}
		}()
		fn()
	}
	assertPanics("nil world", func() {
		RegisterNarrationExpansion(context.Background(), nil, llm.NewFakeClient())
	})
	assertPanics("nil client", func() {
		RegisterNarrationExpansion(context.Background(), w, nil)
	})
}
