package httpapi

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// clientlog.go — the client-reported error feed. The Godot client POSTs runtime
// failures (sprite decode, world_ready stall, WS parse, …) that NEITHER nginx
// NOR the engine can observe — pure browser-side events. They land in a bounded
// in-memory ring, dumped ONLY when an operator probes the umbilical
// (/umbilical/client-errors). Nothing consumes them automatically, and they are
// deliberately NOT written to the journal: this is UNTRUSTED, client-claimed
// data, kept strictly separate from the server-observed error ring (errorlog.go).
// It is a pull-only debug aid — never authoritative, never an input to a
// decision.
//
// The system is invite-only/closed, so abuse is a known, named, revocable user:
// each entry is stamped (by the engine, not the client) with the authed username
// AND source IP for attribution, and a light per-user rate cap keeps a buggy or
// hostile client from flooding the ring. In-memory + lossy-on-restart, so no
// Postgres (see shared GUIDELINES). Safe for concurrent use.

const (
	defaultClientErrorRingSize = 256
	maxClientErrorKindLen      = 64
	maxClientErrorMessageLen   = 1000
	clientLogMaxBody           = 4 << 10 // 4 KB request cap
	clientLogRateMax           = 60      // reports per user per window
	clientLogRateWindow        = time.Minute
)

// clientErrorEntry is one client-reported failure. Time/User/IP are stamped by
// the engine (trustworthy attribution); Kind/Message are client-claimed
// (untrusted — trimmed + length-capped, stored as opaque text).
type clientErrorEntry struct {
	Time    time.Time `json:"time"`
	User    string    `json:"user"`
	IP      string    `json:"ip"`
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
}

// clientErrorRing is a bounded circular buffer of recent client reports,
// overwriting oldest-first once full. Mutex-guarded. Kept distinct from the
// server-observed errorRing so untrusted client data never mixes with the
// engine's own truth.
type clientErrorRing struct {
	mu      sync.Mutex
	entries []clientErrorEntry
	next    int
	full    bool
}

func newClientErrorRing(size int) *clientErrorRing {
	if size <= 0 {
		size = defaultClientErrorRingSize
	}
	return &clientErrorRing{entries: make([]clientErrorEntry, size)}
}

func (r *clientErrorRing) record(e clientErrorEntry) {
	r.mu.Lock()
	r.entries[r.next] = e
	r.next = (r.next + 1) % len(r.entries)
	if r.next == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

// snapshot returns the recorded entries oldest→newest.
func (r *clientErrorRing) snapshot() []clientErrorEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]clientErrorEntry, r.next)
		copy(out, r.entries[:r.next])
		return out
	}
	out := make([]clientErrorEntry, 0, len(r.entries))
	out = append(out, r.entries[r.next:]...)
	out = append(out, r.entries[:r.next]...)
	return out
}

// clientLogRateLimiter is a light fixed-window per-user cap so one client can't
// flood the ring. In a closed/invite-only system the user is known + attributed,
// so this just bounds noise — it is not a security boundary.
type clientLogRateLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	seen   map[string]*rateWindow
}

type rateWindow struct {
	count int
	start time.Time
}

func newClientLogRateLimiter(max int, window time.Duration) *clientLogRateLimiter {
	return &clientLogRateLimiter{max: max, window: window, seen: make(map[string]*rateWindow)}
}

// allow reports whether user may record now, advancing their fixed window. The
// user set is tiny (closed system), so stale entries aren't pruned.
func (l *clientLogRateLimiter) allow(user string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.seen[user]
	if w == nil || now.Sub(w.start) >= l.window {
		l.seen[user] = &rateWindow{count: 1, start: now}
		return true
	}
	if w.count >= l.max {
		return false
	}
	w.count++
	return true
}

// handleClientLog records one client-reported failure. requireAuth has populated
// the session user; the engine stamps user + IP (never trusting the client for
// those) and stores the client-claimed kind/message trimmed + capped.
func (s *Server) handleClientLog(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	if !s.clientLogLimiter.allow(user.Username) {
		writeError(w, http.StatusTooManyRequests, "client-log rate limit exceeded")
		return
	}

	var in struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, clientLogMaxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid client-log body")
		return
	}

	kind := capRunes(strings.TrimSpace(in.Kind), maxClientErrorKindLen)
	if kind == "" {
		writeError(w, http.StatusBadRequest, "kind is required")
		return
	}
	msg := capRunes(strings.TrimSpace(in.Message), maxClientErrorMessageLen)

	s.clientLog.record(clientErrorEntry{
		Time:    time.Now().UTC(),
		User:    user.Username,
		IP:      clientIP(r),
		Kind:    kind,
		Message: msg,
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleUmbilicalClientErrors dumps the client-reported ring (oldest→newest).
// Operator-gated like the rest of the umbilical read surface. This is the ONLY
// place the untrusted feed surfaces — pull-only, on demand.
func (s *Server) handleUmbilicalClientErrors(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.clientLog.snapshot())
}

// clientIP returns the originating client IP. The engine sits behind nginx,
// which sets X-Real-IP to the real $remote_addr (site-village.conf.j2), so that
// header is authoritative here; fall back to the first X-Forwarded-For hop, then
// the raw RemoteAddr host.
func clientIP(r *http.Request) string {
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		return xr
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// capRunes truncates s to at most maxRunes runes (never splitting a multibyte
// character).
func capRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}
