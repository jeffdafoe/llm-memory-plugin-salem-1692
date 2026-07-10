package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// savedNote records one SaveNote call for assertions.
type savedNote struct {
	namespace, slug, title, content, cognitiveType string
}

// fakeWriter is a test double for llm.MemoryWriter. Records saves/deletes and
// returns a scripted list, so tests can assert slug derivation, content shape,
// and prune behavior.
type fakeWriter struct {
	saves      []savedNote
	saveErr    error
	listResult []llm.NoteMeta
	listErr    error
	gotListNS  string
	gotListPre string
	deleteErr  error
	deleted    []string
}

func (f *fakeWriter) SaveNote(_ context.Context, namespace, slug, title, content, cognitiveType string) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saves = append(f.saves, savedNote{namespace, slug, title, content, cognitiveType})
	return nil
}

func (f *fakeWriter) ListNotes(_ context.Context, namespace, slugPrefix string) ([]llm.NoteMeta, error) {
	f.gotListNS, f.gotListPre = namespace, slugPrefix
	return f.listResult, f.listErr
}

func (f *fakeWriter) DeleteNote(_ context.Context, namespace, slug string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, slug)
	return nil
}

func runMemorize(t *testing.T, w llm.MemoryWriter, in HandlerInput) (string, error) {
	t.Helper()
	return makeMemorizeHandler(w)(context.Background(), in)
}

// baseMemorizeInput is a well-formed shared-VA memorize call (Anne Walker's
// partition), used as the common case tests vary from.
func baseMemorizeInput(args MemorizeArgs) HandlerInput {
	return HandlerInput{
		ActorID:            "anne",
		LLMMemoryAgent:     "salem-vendor",
		MemorySlugPrefix:   "anne-walker/",
		MemoryHasPartition: true,
		MemoryDateStamp:    "2026-07-10",
		Args:               args,
	}
}

func TestMemorize_SharedVA_SavesUnderPartition(t *testing.T) {
	w := &fakeWriter{}
	out, err := runMemorize(t, w, baseMemorizeInput(MemorizeArgs{Topic: "The blacksmith's name", Body: "The smith is called Amos."}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(w.saves) != 1 {
		t.Fatalf("expected 1 save, got %d", len(w.saves))
	}
	s := w.saves[0]
	if s.namespace != "salem-vendor" {
		t.Errorf("namespace = %q, want salem-vendor", s.namespace)
	}
	wantSlug := "anne-walker/memory/2026-07-10-the-blacksmith-s-name"
	if s.slug != wantSlug {
		t.Errorf("slug = %q, want %q", s.slug, wantSlug)
	}
	if s.title != "The blacksmith's name" {
		t.Errorf("title = %q, want the topic", s.title)
	}
	if !strings.HasPrefix(s.content, "## The blacksmith's name\n\n") {
		t.Errorf("content should lead with the topic heading; got %q", s.content)
	}
	if !strings.Contains(s.content, "The smith is called Amos.") {
		t.Errorf("content missing the body; got %q", s.content)
	}
	if s.cognitiveType != memoryCognitiveType {
		t.Errorf("cognitiveType = %q, want %q", s.cognitiveType, memoryCognitiveType)
	}
	if !strings.Contains(out, "The blacksmith's name") {
		t.Errorf("result should name the memorized topic; got %q", out)
	}
}

func TestMemorize_StatefulNPC_NoPartitionPrefix(t *testing.T) {
	// A dedicated-VA NPC has an empty partition prefix — memory lives at
	// "memory/…" within its own namespace.
	w := &fakeWriter{}
	in := HandlerInput{
		ActorID:            "josiah",
		LLMMemoryAgent:     "zbbs-josiah-thorne",
		MemoryHasPartition: true, // dedicated VA: partition is its whole namespace, empty prefix
		MemoryDateStamp:    "2026-07-10",
		Args:               MemorizeArgs{Topic: "Well location", Body: "The well is past the meeting house."},
	}
	if _, err := runMemorize(t, w, in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(w.saves) != 1 {
		t.Fatalf("expected 1 save, got %d", len(w.saves))
	}
	if want := "memory/2026-07-10-well-location"; w.saves[0].slug != want {
		t.Errorf("slug = %q, want %q", w.saves[0].slug, want)
	}
}

func TestMemorize_SameTopicSameDay_SameSlug(t *testing.T) {
	// Determinism is what makes upsert a revision: re-memorizing the same topic
	// the same day resolves to the same slug.
	a := memorizeSlug("anne-walker/", "2026-07-10", "The Blacksmith's Name")
	b := memorizeSlug("anne-walker/", "2026-07-10", "the blacksmith's name")
	if a == "" || a != b {
		t.Errorf("same topic/day must yield one slug: %q vs %q", a, b)
	}
}

func TestMemorize_EmptyFields_InCharacter(t *testing.T) {
	w := &fakeWriter{}
	for _, args := range []MemorizeArgs{
		{Topic: "", Body: "something"},
		{Topic: "something", Body: "   "},
		{Topic: "!!!", Body: "body"}, // topic slugifies to empty
	} {
		out, err := runMemorize(t, w, baseMemorizeInput(args))
		if err != nil {
			t.Fatalf("args %+v: unexpected error: %v", args, err)
		}
		if out != memorizeNoInputText {
			t.Errorf("args %+v: got %q, want the no-input string", args, out)
		}
	}
	if len(w.saves) != 0 {
		t.Errorf("no save should happen for empty input; got %d", len(w.saves))
	}
}

func TestMemorize_NoNamespace_InCharacter(t *testing.T) {
	w := &fakeWriter{}
	in := baseMemorizeInput(MemorizeArgs{Topic: "x", Body: "y"})
	in.LLMMemoryAgent = ""
	out, err := runMemorize(t, w, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != memorizeNoAgentText {
		t.Errorf("got %q, want the no-agent string", out)
	}
}

func TestMemorize_NoPartition_RefusesWrite(t *testing.T) {
	// A shared-VA actor whose name won't slugify has a non-empty namespace but no
	// partition (empty prefix, hasPartition=false). memorize must refuse rather
	// than pool the write into the shared namespace root — the gate is
	// advertising-only, so the handler is the real control.
	w := &fakeWriter{}
	in := HandlerInput{
		ActorID:            "nameless",
		LLMMemoryAgent:     "salem-vendor",
		MemorySlugPrefix:   "",
		MemoryHasPartition: false,
		MemoryDateStamp:    "2026-07-10",
		Args:               MemorizeArgs{Topic: "x", Body: "y"},
	}
	out, err := runMemorize(t, w, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != memorizeNoAgentText {
		t.Errorf("got %q, want the no-agent string", out)
	}
	if len(w.saves) != 0 {
		t.Errorf("no write may happen without a partition; got %d saves", len(w.saves))
	}
}

func TestMemorize_NoDateStamp_InCharacter(t *testing.T) {
	w := &fakeWriter{}
	in := baseMemorizeInput(MemorizeArgs{Topic: "x", Body: "y"})
	in.MemoryDateStamp = ""
	out, err := runMemorize(t, w, in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != memorizeFailedText {
		t.Errorf("got %q, want the failed string", out)
	}
}

func TestMemorize_SaveError_InCharacter_NoLeak(t *testing.T) {
	w := &fakeWriter{saveErr: errors.New("memapi: save note: 500 boom")}
	out, err := runMemorize(t, w, baseMemorizeInput(MemorizeArgs{Topic: "x", Body: "y"}))
	if err != nil {
		t.Fatalf("a save failure must be an in-character result, not a handler error: %v", err)
	}
	if out != memorizeFailedText {
		t.Errorf("got %q, want the failed string", out)
	}
	if strings.Contains(out, "500") || strings.Contains(out, "memapi") {
		t.Errorf("transport detail leaked into the model-facing result: %q", out)
	}
}

func TestMemorize_ArgsTypeMismatch_IsHandlerError(t *testing.T) {
	w := &fakeWriter{}
	in := baseMemorizeInput(MemorizeArgs{})
	in.Args = "not a MemorizeArgs"
	if _, err := runMemorize(t, w, in); err == nil {
		t.Error("a wrong args type is a registration bug and must return a handler error")
	}
}

func TestMemorize_PrunesStalestOverCap(t *testing.T) {
	// Build one more than the cap; the two stalest (by Freshness) must be deleted
	// after the save, and the prune must list the actor's memory prefix only.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	notes := make([]llm.NoteMeta, 0, memoryNoteCap+2)
	for i := 0; i < memoryNoteCap+2; i++ {
		// Freshness ascends with i via last_accessed; i=0,1 are the two stalest.
		notes = append(notes, llm.NoteMeta{
			Slug:         fmt.Sprintf("anne-walker/memory/note-%02d", i),
			CreatedAt:    base,
			UpdatedAt:    base,
			LastAccessed: base.Add(time.Duration(i) * time.Hour),
		})
	}
	w := &fakeWriter{listResult: notes}
	if _, err := runMemorize(t, w, baseMemorizeInput(MemorizeArgs{Topic: "fresh", Body: "just saved"})); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.gotListPre != "anne-walker/memory/" {
		t.Errorf("prune listed prefix %q, want anne-walker/memory/", w.gotListPre)
	}
	wantDeleted := []string{"anne-walker/memory/note-00", "anne-walker/memory/note-01"}
	if len(w.deleted) != 2 {
		t.Fatalf("expected 2 deletions over cap, got %d (%v)", len(w.deleted), w.deleted)
	}
	got := map[string]bool{w.deleted[0]: true, w.deleted[1]: true}
	for _, want := range wantDeleted {
		if !got[want] {
			t.Errorf("expected stalest %q to be pruned; deleted %v", want, w.deleted)
		}
	}
}

func TestMemorize_PruneListFailure_StillSucceeds(t *testing.T) {
	// The memory is already saved; a prune failure must not fail the tool.
	w := &fakeWriter{listErr: errors.New("list down")}
	out, err := runMemorize(t, w, baseMemorizeInput(MemorizeArgs{Topic: "x", Body: "y"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "x") {
		t.Errorf("save should still report success despite prune failure; got %q", out)
	}
	if len(w.saves) != 1 {
		t.Errorf("the save must have happened; got %d", len(w.saves))
	}
}

func TestDecodeMemorizeArgs(t *testing.T) {
	if _, err := DecodeMemorizeArgs(json.RawMessage(`{"topic":"t","body":"b"}`)); err != nil {
		t.Errorf("valid args rejected: %v", err)
	}
	if _, err := DecodeMemorizeArgs(json.RawMessage(`["t","b"]`)); err == nil {
		t.Error("non-object args must be rejected")
	}
	if _, err := DecodeMemorizeArgs(json.RawMessage(`{"topic":"t","body":"b","x":1}`)); err == nil {
		t.Error("unknown field must be rejected")
	}
	if _, err := DecodeMemorizeArgs(json.RawMessage(`{"topic":"t","body":"b"} trailing`)); err == nil {
		t.Error("trailing data must be rejected")
	}
}
