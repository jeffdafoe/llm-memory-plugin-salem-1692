package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

func postBody(t *testing.T, h http.Handler, path, token, body string, hdrs map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdrs {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// A client report is stored with engine-stamped user + IP (never client-supplied)
// and surfaces via the operator-gated umbilical feed.
func TestClientLog_RecordAndRead(t *testing.T) {
	h := umbilicalServer(t, operatorPerms, telemetry.New(8))

	rec := postBody(t, h, "/api/village/client-log", "tok",
		`{"kind":"npc_sheet_decode_failed","message":"npc/woman_A_v00.png"}`,
		map[string]string{"X-Real-IP": "203.0.113.7"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("client-log POST = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}

	read := req(t, h, "/api/village/umbilical/client-errors", "tok")
	if read.Code != http.StatusOK {
		t.Fatalf("client-errors = %d, want 200; body=%s", read.Code, read.Body.String())
	}
	var entries []clientErrorEntry
	if err := json.Unmarshal(read.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	e := entries[0]
	if e.Kind != "npc_sheet_decode_failed" || e.Message != "npc/woman_A_v00.png" {
		t.Errorf("kind/message = %q/%q", e.Kind, e.Message)
	}
	// User + IP are engine-stamped: "op" from permAuth, IP from X-Real-IP.
	if e.User != "op" {
		t.Errorf("user = %q, want op (engine-stamped from session)", e.User)
	}
	if e.IP != "203.0.113.7" {
		t.Errorf("ip = %q, want 203.0.113.7 (from X-Real-IP)", e.IP)
	}
}

// A report with no kind is rejected.
func TestClientLog_KindRequired(t *testing.T) {
	h := umbilicalServer(t, operatorPerms, telemetry.New(8))
	rec := postBody(t, h, "/api/village/client-log", "tok", `{"message":"x"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing kind = %d, want 400", rec.Code)
	}
}

// The read feed is operator-gated like the rest of the umbilical surface.
func TestClientLog_ReadOperatorGated(t *testing.T) {
	h := umbilicalServer(t, nil, telemetry.New(8)) // authed, no plugins/administer
	if rec := req(t, h, "/api/village/umbilical/client-errors", "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("client-errors as non-operator = %d, want 403", rec.Code)
	}
}

// The per-user fixed-window cap allows up to max, then denies, and is isolated
// per user.
func TestClientLogRateLimiter(t *testing.T) {
	l := newClientLogRateLimiter(2, time.Minute)
	if !l.allow("u") || !l.allow("u") {
		t.Fatal("first two reports for a user should be allowed")
	}
	if l.allow("u") {
		t.Error("third report within the window should be denied")
	}
	if !l.allow("other") {
		t.Error("a different user must not be affected by u's count")
	}
}
