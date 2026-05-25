package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// fakeSearcher is a test double for llm.MemorySearcher. Records the last call
// args so tests can assert namespace scoping / limit / query truncation.
type fakeSearcher struct {
	hits     []llm.MemoryHit
	err      error
	calls    int
	gotNS    string
	gotQuery string
	gotLimit int
}

func (f *fakeSearcher) SearchMemory(_ context.Context, namespace, query string, limit int) ([]llm.MemoryHit, error) {
	f.calls++
	f.gotNS, f.gotQuery, f.gotLimit = namespace, query, limit
	return f.hits, f.err
}

func runRecall(t *testing.T, s llm.MemorySearcher, in HandlerInput) (string, error) {
	t.Helper()
	return makeRecallHandler(s)(context.Background(), in)
}

func TestRecall_Hits_Formatted(t *testing.T) {
	s := &fakeSearcher{hits: []llm.MemoryHit{
		{SourceFile: "people/bea", ChunkText: "Bea runs the bakery."},
		{SourceFile: "dreams/2026-05-01", ChunkText: "A dream of rain."},
	}}
	out, err := runRecall(t, s, HandlerInput{ActorID: "john", LLMMemoryAgent: "salem-john", Args: RecallArgs{Query: "who is bea"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(out, "You remember:") {
		t.Errorf("missing header; got %q", out)
	}
	for _, want := range []string{"people/bea", "Bea runs the bakery.", "dreams/2026-05-01", "A dream of rain."} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got %q", want, out)
		}
	}
}

func TestRecall_EmptyHits_NothingComesToMind(t *testing.T) {
	s := &fakeSearcher{hits: nil}
	out, err := runRecall(t, s, HandlerInput{LLMMemoryAgent: "salem-john", Args: RecallArgs{Query: "anything"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != recallNoMemoryText {
		t.Errorf("got %q, want %q", out, recallNoMemoryText)
	}
	if s.calls != 1 {
		t.Errorf("search calls = %d, want 1", s.calls)
	}
}

func TestRecall_EmptyQuery_NoSearch(t *testing.T) {
	s := &fakeSearcher{}
	out, err := runRecall(t, s, HandlerInput{LLMMemoryAgent: "salem-john", Args: RecallArgs{Query: "   "}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != recallNoQueryText {
		t.Errorf("got %q, want %q", out, recallNoQueryText)
	}
	if s.calls != 0 {
		t.Errorf("search should not be called for an empty query; calls = %d", s.calls)
	}
}

func TestRecall_NoNamespace_NoSearch(t *testing.T) {
	s := &fakeSearcher{hits: []llm.MemoryHit{{SourceFile: "x", ChunkText: "y"}}}
	out, err := runRecall(t, s, HandlerInput{ActorID: "deco", LLMMemoryAgent: "", Args: RecallArgs{Query: "who am i"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != recallFailedText {
		t.Errorf("got %q, want %q", out, recallFailedText)
	}
	if s.calls != 0 {
		t.Errorf("search must not run without a namespace; calls = %d", s.calls)
	}
}

func TestRecall_SearchError_GracefulText(t *testing.T) {
	s := &fakeSearcher{err: errors.New("boom")}
	out, err := runRecall(t, s, HandlerInput{LLMMemoryAgent: "salem-john", Args: RecallArgs{Query: "q"}})
	if err != nil {
		t.Fatalf("a search error must NOT surface as a handler error (v1 parity); got %v", err)
	}
	if out != recallFailedText {
		t.Errorf("got %q, want %q", out, recallFailedText)
	}
}

func TestRecall_SearchesActorNamespaceAtLimit(t *testing.T) {
	s := &fakeSearcher{}
	_, _ = runRecall(t, s, HandlerInput{LLMMemoryAgent: "salem-prudence", Args: RecallArgs{Query: "  remedies  "}})
	if s.gotNS != "salem-prudence" {
		t.Errorf("searched ns %q, want the actor's own namespace", s.gotNS)
	}
	if s.gotQuery != "remedies" {
		t.Errorf("query = %q, want trimmed %q", s.gotQuery, "remedies")
	}
	if s.gotLimit != recallResultLimit {
		t.Errorf("limit = %d, want %d", s.gotLimit, recallResultLimit)
	}
}

func TestRecall_QueryTruncatedToCap(t *testing.T) {
	s := &fakeSearcher{}
	long := strings.Repeat("か", recallQueryMaxChars+50) // multibyte: rune-truncation must not split
	_, err := runRecall(t, s, HandlerInput{LLMMemoryAgent: "salem-john", Args: RecallArgs{Query: long}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := utf8.RuneCountInString(s.gotQuery); got != recallQueryMaxChars {
		t.Errorf("truncated query rune count = %d, want %d", got, recallQueryMaxChars)
	}
	if !utf8.ValidString(s.gotQuery) {
		t.Error("truncation split a multibyte rune (invalid UTF-8)")
	}
}

func TestRecall_WrongArgsType_HandlerError(t *testing.T) {
	s := &fakeSearcher{}
	_, err := runRecall(t, s, HandlerInput{LLMMemoryAgent: "salem-john", Args: "not a RecallArgs"})
	if err == nil {
		t.Fatal("expected a handler error for an args-type mismatch (registration bug)")
	}
}

func TestDecodeRecallArgs(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
		query   string
	}{
		{"valid", `{"query":"who is bea"}`, false, "who is bea"},
		{"empty-query-ok", `{"query":""}`, false, ""}, // handler turns this into recallNoQueryText
		{"non-object", `"hello"`, true, ""},
		{"unknown-field", `{"query":"x","extra":1}`, true, ""},
		{"trailing-data", `{"query":"x"}{}`, true, ""},
		{"malformed", `{"query":}`, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := DecodeRecallArgs(json.RawMessage(c.raw))
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", c.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			args, ok := got.(RecallArgs)
			if !ok {
				t.Fatalf("decoded type %T, want RecallArgs", got)
			}
			if args.Query != c.query {
				t.Errorf("query = %q, want %q", args.Query, c.query)
			}
		})
	}
}

func TestFormatRecallHits_Empty(t *testing.T) {
	if got := formatRecallHits(nil); got != recallNoMemoryText {
		t.Errorf("got %q, want %q", got, recallNoMemoryText)
	}
}

func TestRegisterRecall(t *testing.T) {
	t.Run("nil-searcher-errors", func(t *testing.T) {
		r := NewRegistry()
		if err := RegisterRecall(r, nil); err == nil {
			t.Fatal("expected error for nil searcher")
		}
	})
	t.Run("registers-as-non-terminal-observation", func(t *testing.T) {
		r := NewRegistry()
		if err := RegisterRecall(r, &fakeSearcher{}); err != nil {
			t.Fatalf("RegisterRecall: %v", err)
		}
		e, ok := r.Lookup("recall")
		if !ok {
			t.Fatal("recall not registered")
		}
		if e.Class != ClassObservation {
			t.Errorf("class = %v, want ClassObservation", e.Class)
		}
		if e.TerminalPolicy != TerminalNever {
			t.Errorf("terminal policy = %v, want TerminalNever", e.TerminalPolicy)
		}
		if e.Availability != AvailabilityAvailable {
			t.Errorf("availability = %v, want AvailabilityAvailable", e.Availability)
		}
	})
}
