package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// denyAuth rejects every token — for the unauthenticated-WS-rejection test.
type denyAuth struct{}

func (denyAuth) Verify(string) VerifyResult { return VerifyResult{Reason: "invalid"} }

// fakeVerifyBackend emulates llm-memory-api's /v1/auth/verify. respond builds
// the JSON body; if calls is non-nil it counts requests (for cache assertions).
func fakeVerifyBackend(t *testing.T, respond func() map[string]any, calls *int32) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls != nil {
			atomic.AddInt32(calls, 1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(respond())
	}))
	t.Cleanup(ts.Close)
	return ts
}

func verifyResp(valid bool, realms []string, kind string) map[string]any {
	return map[string]any{"valid": valid, "agent": "tester", "realms": realms, "session_kind": kind}
}

func TestTokenVerifier_ValidSalem(t *testing.T) {
	ts := fakeVerifyBackend(t, func() map[string]any { return verifyResp(true, []string{"salem"}, "web") }, nil)
	res := NewTokenVerifier(ts.URL, time.Minute).Verify("tok")
	if !res.Valid {
		t.Fatalf("want valid, got reason=%q", res.Reason)
	}
	if res.User == nil || res.User.Username != "tester" || res.User.SessionKind != "web" {
		t.Errorf("user = %+v", res.User)
	}
}

func TestTokenVerifier_WrongRealmForbidden(t *testing.T) {
	ts := fakeVerifyBackend(t, func() map[string]any { return verifyResp(true, []string{"other"}, "web") }, nil)
	res := NewTokenVerifier(ts.URL, time.Minute).Verify("tok")
	if res.Valid || res.Reason != "realm" {
		t.Errorf("want realm reject, got valid=%v reason=%q", res.Valid, res.Reason)
	}
}

func TestTokenVerifier_Invalid(t *testing.T) {
	ts := fakeVerifyBackend(t, func() map[string]any { return verifyResp(false, nil, "") }, nil)
	if res := NewTokenVerifier(ts.URL, time.Minute).Verify("tok"); res.Valid || res.Reason != "invalid" {
		t.Errorf("want invalid, got %+v", res)
	}
}

func TestTokenVerifier_MissingToken(t *testing.T) {
	if res := NewTokenVerifier("http://unused.invalid", time.Minute).Verify(""); res.Valid || res.Reason != "missing" {
		t.Errorf("want missing, got %+v", res)
	}
}

func TestTokenVerifier_ServiceError(t *testing.T) {
	// A closed listener → connection refused → "service".
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := ts.URL
	ts.Close()
	if res := NewTokenVerifier(url, time.Minute).Verify("tok"); res.Valid || res.Reason != "service" {
		t.Errorf("want service, got %+v", res)
	}
}

func TestTokenVerifier_Non2xxRejected(t *testing.T) {
	// A non-2xx that happens to carry a success-shaped JSON body must NOT be
	// accepted — the status check makes this fail closed (→ service).
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(verifyResp(true, []string{"salem"}, "web"))
	}))
	t.Cleanup(ts.Close)
	if res := NewTokenVerifier(ts.URL, time.Minute).Verify("tok"); res.Valid || res.Reason != "service" {
		t.Errorf("non-2xx with valid-looking body: got %+v, want service reject", res)
	}
}

func TestTokenVerifier_PositiveResultCached(t *testing.T) {
	var calls int32
	ts := fakeVerifyBackend(t, func() map[string]any { return verifyResp(true, []string{"salem"}, "web") }, &calls)
	v := NewTokenVerifier(ts.URL, time.Minute)
	for i := 0; i < 3; i++ {
		if res := v.Verify("tok"); !res.Valid {
			t.Fatalf("verify %d invalid", i)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("backend hit %d times across 3 Verify calls, want 1 (positive cache)", got)
	}
}

func TestTokenVerifier_InvalidNotCached(t *testing.T) {
	var calls int32
	ts := fakeVerifyBackend(t, func() map[string]any { return verifyResp(false, nil, "") }, &calls)
	v := NewTokenVerifier(ts.URL, time.Minute)
	v.Verify("tok")
	v.Verify("tok")
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("invalid token hit backend %d times, want 2 (negatives not cached)", got)
	}
}

func TestRequireAuth_RejectsMissingAndInvalid(t *testing.T) {
	ts := fakeVerifyBackend(t, func() map[string]any { return verifyResp(false, nil, "") }, nil)
	srv := NewServer(seededWorld(t), NewTokenVerifier(ts.URL, time.Minute))

	// No Authorization header → missing → 401.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/village/world", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: code=%d, want 401", rec.Code)
	}

	// Present token the backend rejects → invalid → 401.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/village/world", nil)
	req2.Header.Set("Authorization", "Bearer bad")
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("bad token: code=%d, want 401", rec2.Code)
	}
}

func TestRequireAuth_WrongRealmForbidden(t *testing.T) {
	ts := fakeVerifyBackend(t, func() map[string]any { return verifyResp(true, []string{"other"}, "web") }, nil)
	srv := NewServer(seededWorld(t), NewTokenVerifier(ts.URL, time.Minute))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/village/world", nil)
	req.Header.Set("Authorization", "Bearer tok")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("wrong-realm token: code=%d, want 403", rec.Code)
	}
}

// TestEventsRejectsUnauthenticated covers the WS auth gate: an unauthenticated
// /events request is rejected with 401 BEFORE the upgrade — no upgrade, no
// register, no hello frame.
func TestEventsRejectsUnauthenticated(t *testing.T) {
	w := seededWorld(t)
	hub := NewHub(fixedTranslator)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx)

	srv := NewServer(w, denyAuth{})
	srv.SetEventsHub(hub)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/village/events?token=whatever"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		_ = conn.Close()
		t.Fatal("expected the WS dial to fail for an unauthenticated client")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want HTTP 401 before upgrade, got resp=%v", resp)
	}
}
