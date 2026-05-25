package memapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
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
		_, _ = io.WriteString(w, `{"results":[{"source_file":"people/bea","heading":"Bea","chunk_text":"runs the bakery","namespace":"salem-john","similarity":0.9,"chunk_count":2}]}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	hits, err := c.SearchMemory(context.Background(), "salem-john", "who is bea", 5)
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
}

func TestSearchMemory_EmptyResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k")
	hits, err := c.SearchMemory(context.Background(), "ns", "q", 5)
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
	if _, err := c.SearchMemory(context.Background(), "ns", "q", 5); err == nil {
		t.Fatal("expected an error on HTTP 500")
	}
}
