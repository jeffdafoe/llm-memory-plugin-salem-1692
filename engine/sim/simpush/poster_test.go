package simpush

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// poster_test.go — the HTTP half against an httptest server: request shape +
// auth header, the empty-events "[]" guard, and the non-fatal-vs-fatal status
// split.

// capture records what the test server received.
type capture struct {
	method string
	path   string
	auth   string
	body   []byte
}

// posterServer stands up an httptest server returning status, recording the
// request into *capture. Returns a poster pointed at it.
func posterServer(t *testing.T, status int, cap *capture) *HTTPPoster {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.method = r.Method
		cap.path = r.URL.Path
		cap.auth = r.Header.Get("Authorization")
		cap.body, _ = readAll(r)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return NewHTTPPoster(srv.URL, "engine-key-123")
}

func readAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}

// TestPostDay_Success: a 2xx posts to the right path with the bearer token and
// a well-formed body, and the populated event maps onto the wire shape.
func TestPostDay_Success(t *testing.T) {
	cap := &capture{}
	p := posterServer(t, http.StatusOK, cap)

	at := time.Date(2026, 6, 3, 9, 30, 0, 0, time.UTC)
	events := []sim.SimDayEvent{{
		At:      at,
		Kind:    sim.ActionTypePaid,
		Payload: map[string]any{"recipient": "Bob", "amount": float64(3)},
		Speaker: "Ezekiel",
	}}
	if err := p.PostDay(context.Background(), "salem-ezekiel", "2026-06-03", events); err != nil {
		t.Fatalf("PostDay: %v", err)
	}

	if cap.method != http.MethodPost {
		t.Errorf("method = %q, want POST", cap.method)
	}
	if cap.path != "/v1/sim/conversation-day" {
		t.Errorf("path = %q, want /v1/sim/conversation-day", cap.path)
	}
	if cap.auth != "Bearer engine-key-123" {
		t.Errorf("auth = %q, want Bearer engine-key-123", cap.auth)
	}

	var body struct {
		Agent  string `json:"agent"`
		Day    string `json:"day"`
		Events []struct {
			At      time.Time      `json:"at"`
			Kind    string         `json:"kind"`
			Payload map[string]any `json:"payload"`
			Speaker string         `json:"speaker"`
		} `json:"events"`
	}
	if err := json.Unmarshal(cap.body, &body); err != nil {
		t.Fatalf("decode body: %v (raw=%s)", err, cap.body)
	}
	if body.Agent != "salem-ezekiel" || body.Day != "2026-06-03" {
		t.Errorf("envelope = {agent:%q day:%q}, want salem-ezekiel/2026-06-03", body.Agent, body.Day)
	}
	if len(body.Events) != 1 {
		t.Fatalf("events len = %d, want 1", len(body.Events))
	}
	e := body.Events[0]
	if e.Kind != "paid" || e.Speaker != "Ezekiel" || e.Payload["recipient"] != "Bob" || !e.At.Equal(at) {
		t.Errorf("event = %+v, want kind=paid speaker=Ezekiel recipient=Bob at=%s", e, at)
	}
}

// TestPostDay_EmptyEventsIsArray: no events serializes as "events":[] (not
// null), which the API requires.
func TestPostDay_EmptyEventsIsArray(t *testing.T) {
	cap := &capture{}
	p := posterServer(t, http.StatusOK, cap)

	if err := p.PostDay(context.Background(), "salem-john", "2026-06-03", nil); err != nil {
		t.Fatalf("PostDay: %v", err)
	}
	if !bytes.Contains(cap.body, []byte(`"events":[]`)) {
		t.Errorf("body = %s, want events serialized as []", cap.body)
	}
}

// TestPostDay_NonFatalStatuses: 400 (non-sim) and 404 (unknown agent) are
// contract-expected — returned as the recognizable sentinels (not nil, not a
// hard error) so the dispatcher folds them into its per-day skip summary.
func TestPostDay_NonFatalStatuses(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusBadRequest, errSkippedNonSim},
		{http.StatusNotFound, errSkippedUnknown},
	}
	for _, tc := range cases {
		cap := &capture{}
		p := posterServer(t, tc.status, cap)
		if err := p.PostDay(context.Background(), "salem-x", "2026-06-03", nil); !errors.Is(err, tc.want) {
			t.Errorf("status %d: PostDay returned %v, want %v", tc.status, err, tc.want)
		}
	}
}

// TestPostDay_FatalStatus: a 5xx is a real error so the dispatcher leaves the
// cursor un-stamped and retries.
func TestPostDay_FatalStatus(t *testing.T) {
	cap := &capture{}
	p := posterServer(t, http.StatusInternalServerError, cap)
	if err := p.PostDay(context.Background(), "salem-x", "2026-06-03", nil); err == nil {
		t.Error("PostDay on 500 = nil, want an error")
	}
}
