package memapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestSearchMemory verifies the /v1/memory/search request shape (path, bearer
// auth, body fields) and response decode into []llm.MemoryHit. ZBBS-WORK-321.
func TestSearchMemory(t *testing.T) {
	var gotPath, gotAuth string
	var gotReq searchMemoryRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"source_file":"people/bea","heading":"Bea","chunk_text":"runs the bakery","namespace":"salem-john","similarity":0.9,"chunk_count":2,"created_at":"2026-07-11T02:34:56.789Z"}]}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	hits, err := c.SearchMemory(context.Background(), "salem-john", "who is bea", "", 5)
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if gotPath != "/v1/memory/search" {
		t.Errorf("path = %q, want /v1/memory/search", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("auth = %q, want Bearer test-key", gotAuth)
	}
	if gotReq.Namespace != "salem-john" || gotReq.Query != "who is bea" || gotReq.Limit != 5 {
		t.Errorf("request = %+v", gotReq)
	}
	if len(hits) != 1 {
		t.Fatalf("hits len = %d, want 1", len(hits))
	}
	h := hits[0]
	if h.SourceFile != "people/bea" || h.ChunkText != "runs the bakery" || h.Namespace != "salem-john" || h.ChunkCount != 2 {
		t.Errorf("hit = %+v", h)
	}
	if want := time.Date(2026, 7, 11, 2, 34, 56, 789000000, time.UTC); !h.CreatedAt.Equal(want) {
		t.Errorf("CreatedAt = %v, want %v", h.CreatedAt, want)
	}
}

// TestSearchMemory_CreatedAtLiberalDecode — created_at is nice-to-have age
// context (LLM-390); a null, absent, or malformed value must decode to the
// zero time, never fail the whole search (the LLM-379 failure mode).
func TestSearchMemory_CreatedAtLiberalDecode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[
			{"source_file":"a","chunk_text":"x","namespace":"ns","similarity":0.9,"chunk_count":1,"created_at":null},
			{"source_file":"b","chunk_text":"y","namespace":"ns","similarity":0.8,"chunk_count":1},
			{"source_file":"c","chunk_text":"z","namespace":"ns","similarity":0.7,"chunk_count":1,"created_at":"not-a-timestamp"}
		]}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k")
	hits, err := c.SearchMemory(context.Background(), "ns", "q", "", 5)
	if err != nil {
		t.Fatalf("SearchMemory with odd created_at values: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("hits len = %d, want 3 (odd created_at must not fail the decode)", len(hits))
	}
	for i, h := range hits {
		if !h.CreatedAt.IsZero() {
			t.Errorf("hit %d CreatedAt = %v, want zero (unknown)", i, h.CreatedAt)
		}
	}
}

// TestSearchMemory_StringChunkCount — the memory-api serializes chunk_count as a
// quoted string in some responses; a plain int field failed the whole decode and
// every recall came back empty (LLM-379). flexInt must accept the string shape.
func TestSearchMemory_StringChunkCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"source_file":"people/bea","heading":"Bea","chunk_text":"runs the bakery","namespace":"salem-visitor","similarity":0.9,"chunk_count":"2"}]}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k")
	hits, err := c.SearchMemory(context.Background(), "salem-visitor", "who is bea", "", 5)
	if err != nil {
		t.Fatalf("SearchMemory with string chunk_count: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits len = %d, want 1 (string chunk_count must not fail the decode)", len(hits))
	}
	if hits[0].ChunkCount != 2 {
		t.Errorf("ChunkCount = %d, want 2 (parsed from the quoted string)", hits[0].ChunkCount)
	}
}

func TestSearchMemory_EmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k")
	hits, err := c.SearchMemory(context.Background(), "ns", "q", "", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("hits len = %d, want 0", len(hits))
	}
}

func TestSearchMemory_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k")
	if _, err := c.SearchMemory(context.Background(), "ns", "q", "", 5); err == nil {
		t.Fatal("expected an error on HTTP 500")
	}
}
