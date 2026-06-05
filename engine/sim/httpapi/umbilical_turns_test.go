package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/telemetry"
)

// turnsServer builds an umbilical-enabled Server whose /turns route proxies to
// upstreamURL, authenticating via permAuth{operatorPerms}.
func turnsServer(t *testing.T, upstreamURL string) http.Handler {
	t.Helper()
	srv := NewServer(seededWorld(t), permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8))
	srv.SetMemoryAPIBaseURL(upstreamURL)
	return srv.Handler()
}

// TestUmbilicalTurns_ProxiesAndForwardsToken: the happy path. The route forwards
// the operator's bearer token and a JSON body built from the query params to
// memory-api's /v1/sim/raw-turns, then relays the upstream status + body verbatim.
func TestUmbilicalTurns_ProxiesAndForwardsToken(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody rawTurnsUpstreamRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"turns":[{"id":42,"agent":"zbbs-ezekiel"}]}`)
	}))
	defer upstream.Close()

	h := turnsServer(t, upstream.URL)
	rec := req(t, h, "/api/village/umbilical/turns?scene=019e97d3-0000-7000-8000-000000000000&agent=zbbs-ezekiel&status=error&limit=3", "operator-tok")

	if rec.Code != http.StatusOK {
		t.Fatalf("turns = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Operator's token forwarded unchanged.
	if gotAuth != "Bearer operator-tok" {
		t.Errorf("forwarded Authorization = %q, want %q", gotAuth, "Bearer operator-tok")
	}
	// Hit the right upstream endpoint.
	if gotPath != turnsUpstreamPath {
		t.Errorf("upstream path = %q, want %q", gotPath, turnsUpstreamPath)
	}
	// Query params mapped into the upstream JSON body.
	if gotBody.SceneID != "019e97d3-0000-7000-8000-000000000000" || gotBody.Agent != "zbbs-ezekiel" {
		t.Errorf("upstream body scene/agent = %q/%q, want the query values", gotBody.SceneID, gotBody.Agent)
	}
	if gotBody.Status != "error" || gotBody.Limit != 3 {
		t.Errorf("upstream body status/limit = %q/%d, want error/3", gotBody.Status, gotBody.Limit)
	}
	// Upstream body relayed verbatim.
	if rec.Body.String() != `{"turns":[{"id":42,"agent":"zbbs-ezekiel"}]}` {
		t.Errorf("relayed body = %s, want the upstream JSON verbatim", rec.Body.String())
	}
}

// TestUmbilicalTurns_RelaysUpstreamStatus: a non-2xx from memory-api (e.g. a
// malformed scene_id 400) is passed straight through, not masked.
func TestUmbilicalTurns_RelaysUpstreamStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"BAD_REQUEST","message":"scene_id must be a UUID"}}`)
	}))
	defer upstream.Close()

	h := turnsServer(t, upstream.URL)
	rec := req(t, h, "/api/village/umbilical/turns?scene=not-a-uuid", "tok")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("turns = %d, want 400 (relayed); body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != `{"error":{"code":"BAD_REQUEST","message":"scene_id must be a UUID"}}` {
		t.Errorf("relayed error body = %s, want upstream verbatim", rec.Body.String())
	}
}

// TestUmbilicalTurns_NotConfigured: the route registers with the read surface
// even without an upstream, but can't serve → 503 (not a panic, not a 200).
func TestUmbilicalTurns_NotConfigured(t *testing.T) {
	srv := NewServer(seededWorld(t), permAuth{operatorPerms})
	srv.SetTelemetry(telemetry.New(8)) // umbilical on, but SetMemoryAPIBaseURL NOT called
	rec := req(t, srv.Handler(), "/api/village/umbilical/turns", "tok")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("turns with no upstream configured = %d, want 503", rec.Code)
	}
}

// TestUmbilicalTurns_UpstreamUnreachable: memory-api down → an honest 502 Bad
// Gateway, not a 500 or a hang. Point the base URL at a closed listener.
func TestUmbilicalTurns_UpstreamUnreachable(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // now refuses connections

	h := turnsServer(t, deadURL)
	rec := req(t, h, "/api/village/umbilical/turns?agent=salem-vendor", "tok")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("turns with upstream down = %d, want 502", rec.Code)
	}
}

// TestUmbilicalTurns_Gated: the route shares the read-surface gate — 404 when the
// umbilical is off, 403 for a non-operator, 401 with no token.
func TestUmbilicalTurns_Gated(t *testing.T) {
	const path = "/api/village/umbilical/turns"

	// Off by default (no telemetry) → 404 (the route isn't registered).
	off := NewServer(seededWorld(t), permAuth{operatorPerms}).Handler()
	if rec := req(t, off, path, "tok"); rec.Code != http.StatusNotFound {
		t.Errorf("turns umbilical-off = %d, want 404", rec.Code)
	}
	// Enabled but non-operator → 403; no token → 401. (Gate runs before the
	// upstream call, so no real memory-api is needed here.)
	nonOp := NewServer(seededWorld(t), permAuth{nil})
	nonOp.SetTelemetry(telemetry.New(4))
	nonOp.SetMemoryAPIBaseURL("http://127.0.0.1:1")
	hNonOp := nonOp.Handler()
	if rec := req(t, hNonOp, path, "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("turns non-operator = %d, want 403", rec.Code)
	}
	if rec := req(t, hNonOp, path, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("turns no token = %d, want 401", rec.Code)
	}
}
