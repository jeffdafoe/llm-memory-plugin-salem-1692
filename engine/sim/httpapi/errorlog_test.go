package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// The error ring captures the engine's own non-2xx responses; the umbilical
// /errors route dumps them. A no-route 404 (e.g. the dead v1 /api/me a stale
// client hits) must show up; a 2xx must not.
func TestUmbilicalErrors_RecordsNon2xx(t *testing.T) {
	h := umbilicalServer(t, operatorPerms, telemetry.New(8))

	// 404 — a route the engine doesn't serve (the original login-bug shape).
	if rec := req(t, h, "/api/me", "tok"); rec.Code != http.StatusNotFound {
		t.Fatalf("/api/me = %d, want 404", rec.Code)
	}
	// 200 — a real read; must NOT be recorded.
	if rec := req(t, h, "/api/village/world", "tok"); rec.Code != http.StatusOK {
		t.Fatalf("/api/village/world = %d, want 200", rec.Code)
	}

	rec := req(t, h, "/api/village/umbilical/errors", "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("umbilical/errors = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var entries []errorEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var saw404, saw200 bool
	for _, e := range entries {
		if e.Path == "/api/me" && e.Status == http.StatusNotFound {
			saw404 = true
		}
		if e.Path == "/api/village/world" {
			saw200 = true
		}
	}
	if !saw404 {
		t.Errorf("expected the /api/me 404 to be recorded; got %+v", entries)
	}
	if saw200 {
		t.Errorf("a 2xx (/api/village/world) must not be recorded; got %+v", entries)
	}
}

// The error ring is operator-gated like the rest of the umbilical surface.
func TestUmbilicalErrors_OperatorGated(t *testing.T) {
	h := umbilicalServer(t, nil, telemetry.New(8)) // authed, no plugins/administer
	if rec := req(t, h, "/api/village/umbilical/errors", "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("errors as non-operator = %d, want 403", rec.Code)
	}
}
