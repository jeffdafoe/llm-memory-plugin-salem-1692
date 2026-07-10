package memapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSearchMemory_SendsSlugPrefix verifies the LLM-356 slug_prefix reaches the
// wire when set (and is omitted when empty, per omitempty — covered by the other
// search tests passing "").
func TestSearchMemory_SendsSlugPrefix(t *testing.T) {
	var gotReq searchMemoryRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k")
	if _, err := c.SearchMemory(context.Background(), "salem-vendor", "smith", "anne-walker/", 5); err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	if gotReq.SlugPrefix != "anne-walker/" {
		t.Errorf("slug_prefix = %q, want anne-walker/", gotReq.SlugPrefix)
	}
}

// TestSaveNote verifies the /v1/documents/save request shape: path, bearer auth,
// upsert always true, metadata cognitive_type, and the note fields.
func TestSaveNote(t *testing.T) {
	var gotPath, gotAuth string
	var gotReq saveNoteRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		_, _ = io.WriteString(w, `{"id":"1","slug":"anne-walker/memory/2026-07-10-x"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-key")
	err := c.SaveNote(context.Background(), "salem-vendor", "anne-walker/memory/2026-07-10-x", "X", "## X\n\nbody", "episodic")
	if err != nil {
		t.Fatalf("SaveNote: %v", err)
	}
	if gotPath != "/v1/documents/save" {
		t.Errorf("path = %q, want /v1/documents/save", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("auth = %q, want Bearer test-key", gotAuth)
	}
	if !gotReq.Upsert {
		t.Error("upsert must be true for the memorize path")
	}
	if gotReq.Namespace != "salem-vendor" || gotReq.Slug != "anne-walker/memory/2026-07-10-x" || gotReq.Title != "X" {
		t.Errorf("request = %+v", gotReq)
	}
	if gotReq.Metadata["cognitive_type"] != "episodic" {
		t.Errorf("metadata cognitive_type = %v, want episodic", gotReq.Metadata["cognitive_type"])
	}
}

// TestSaveNote_NoCognitiveType_OmitsMetadata confirms an empty cognitiveType
// sends no metadata object (omitempty), rather than {"cognitive_type":""}.
func TestSaveNote_NoCognitiveType_OmitsMetadata(t *testing.T) {
	var rawBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &rawBody)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k")
	if err := c.SaveNote(context.Background(), "ns", "slug", "t", "c", ""); err != nil {
		t.Fatalf("SaveNote: %v", err)
	}
	if _, present := rawBody["metadata"]; present {
		t.Errorf("metadata should be omitted when cognitiveType is empty; body = %+v", rawBody)
	}
}

// TestListNotes verifies the /v1/documents/list request shape and that
// last_accessed decodes (null → zero time.Time).
func TestListNotes(t *testing.T) {
	var gotPath string
	var gotReq listNotesRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		_, _ = io.WriteString(w, `{"notes":[
            {"slug":"anne-walker/memory/a","created_at":"2026-07-01T00:00:00Z","updated_at":"2026-07-02T00:00:00Z","last_accessed":"2026-07-05T00:00:00Z"},
            {"slug":"anne-walker/memory/b","created_at":"2026-07-01T00:00:00Z","updated_at":"2026-07-01T00:00:00Z","last_accessed":null}
        ]}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k")
	notes, err := c.ListNotes(context.Background(), "salem-vendor", "anne-walker/memory/")
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if gotPath != "/v1/documents/list" {
		t.Errorf("path = %q, want /v1/documents/list", gotPath)
	}
	if gotReq.Namespace != "salem-vendor" || gotReq.Prefix != "anne-walker/memory/" {
		t.Errorf("request = %+v", gotReq)
	}
	if len(notes) != 2 {
		t.Fatalf("notes len = %d, want 2", len(notes))
	}
	if notes[0].LastAccessed.IsZero() {
		t.Error("note a last_accessed should decode to a non-zero time")
	}
	if !notes[1].LastAccessed.IsZero() {
		t.Error("note b last_accessed was null and should decode to the zero time")
	}
	// Freshness folds the three timestamps: note a's last_accessed (Jul 5) is
	// newest; note b's updated_at (Jul 1) is, since its last_accessed is zero.
	if !notes[1].Freshness().Before(notes[0].Freshness()) {
		t.Error("note b (never accessed) should be staler than note a (recently accessed)")
	}
}

// TestDeleteNote verifies the /v1/documents/delete request shape.
func TestDeleteNote(t *testing.T) {
	var gotPath string
	var gotReq deleteNoteRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotReq)
		_, _ = io.WriteString(w, `{"deleted":true}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "k")
	if err := c.DeleteNote(context.Background(), "salem-vendor", "anne-walker/memory/a"); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	if gotPath != "/v1/documents/delete" {
		t.Errorf("path = %q, want /v1/documents/delete", gotPath)
	}
	if gotReq.Namespace != "salem-vendor" || gotReq.Slug != "anne-walker/memory/a" {
		t.Errorf("request = %+v", gotReq)
	}
}

// TestSaveNote_HTTPError surfaces a non-2xx as an error (memorize maps it to an
// in-character result upstream).
func TestSaveNote_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k")
	if err := c.SaveNote(context.Background(), "ns", "s", "t", "c", "episodic"); err == nil {
		t.Fatal("expected an error on HTTP 500")
	}
}
