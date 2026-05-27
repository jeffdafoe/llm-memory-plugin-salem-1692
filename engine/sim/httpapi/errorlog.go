package httpapi

import (
	"bufio"
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// errorlog.go — server-observed HTTP error visibility. A response-capture
// middleware records every non-2xx the engine returns into a bounded in-memory
// ring AND logs it (so it also lands in the journal home can `journalctl`). The
// ring is dumped by the operator-gated umbilical /errors route, giving work +
// home remote visibility into client-facing failures — a 404 on a dead route, a
// 403 on pc/me, a 500 — WITHOUT SSH to the box.
//
// Deliberately server-observed ONLY: it records what the engine itself returned,
// never anything the browser self-reports. Client-submitted error strings are
// untrusted/spoofable and would poison the operator view, so there is no client
// beacon. Static-asset failures (sprite sheets etc.) are nginx's to log; this
// covers the engine's own API surface.
//
// In-memory + lossy-on-restart: transient diagnostics, no durability need, so no
// Postgres (see shared GUIDELINES). Safe for concurrent request goroutines.

// defaultErrorRingSize bounds the recent-error ring.
const defaultErrorRingSize = 256

// errorEntry is one non-2xx response the engine returned.
type errorEntry struct {
	Time   time.Time `json:"time"`
	Method string    `json:"method"`
	Path   string    `json:"path"`
	Status int       `json:"status"`
}

// errorRing is a fixed-size circular buffer of recent errorEntry, overwriting
// oldest-first once full. Mutex-guarded.
type errorRing struct {
	mu      sync.Mutex
	entries []errorEntry
	next    int
	full    bool
}

func newErrorRing(size int) *errorRing {
	if size <= 0 {
		size = defaultErrorRingSize
	}
	return &errorRing{entries: make([]errorEntry, size)}
}

func (r *errorRing) record(e errorEntry) {
	r.mu.Lock()
	r.entries[r.next] = e
	r.next = (r.next + 1) % len(r.entries)
	if r.next == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

// snapshot returns the recorded entries oldest→newest.
func (r *errorRing) snapshot() []errorEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]errorEntry, r.next)
		copy(out, r.entries[:r.next])
		return out
	}
	out := make([]errorEntry, 0, len(r.entries))
	out = append(out, r.entries[r.next:]...)
	out = append(out, r.entries[:r.next]...)
	return out
}

// statusRecorder wraps a ResponseWriter to remember the status code written.
// Defaults to 200 (a handler that writes a body without an explicit WriteHeader
// implies 200). Hijack is passed through so the WS /events upgrade still works.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status = http.StatusOK
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

// Hijack passes through to the underlying ResponseWriter so gorilla's WS upgrade
// (which needs http.Hijacker) keeps working through the wrapper.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := s.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, errors.New("httpapi: underlying ResponseWriter is not a Hijacker")
}

// withErrorCapture wraps the whole mux: it runs the request, then records + logs
// any non-2xx response. Runs OUTSIDE requireAuth, so it also catches no-route
// 404s and auth rejections — exactly the failures that are otherwise invisible
// without the box.
func (s *Server) withErrorCapture(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if rec.status >= 400 {
			s.errorLog.record(errorEntry{
				Time:   time.Now().UTC(),
				Method: r.Method,
				Path:   r.URL.Path,
				Status: rec.status,
			})
			log.Printf("httpapi: %d %s %s", rec.status, r.Method, r.URL.Path)
		}
	})
}

// handleUmbilicalErrors dumps the recent-error ring (oldest→newest). Operator-
// gated like the rest of the umbilical read surface.
func (s *Server) handleUmbilicalErrors(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.errorLog.snapshot())
}
