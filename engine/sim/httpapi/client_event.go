package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// client_event.go — LLM-880. The client event beacon: client-only events the
// Godot client reports for statistics (the figure at the treeline, LLM-844),
// which the engine never sees otherwise. POST /api/village/client-event writes
// one row per report straight to Postgres; GET /umbilical/client-events reads
// counts and recent rows back for an operator.
//
// It differs from the client error feed (clientlog.go) on purpose: errors are a
// short-lived debug ring, but an event that happens a few times a season would
// never survive the engine's many daily restarts in memory, so these rows are
// durable. The two never share a route, a store or a read.
//
// Untrusted, client-claimed data. The engine stamps the time, the username and
// the caller's PC; the client chooses only an allowlisted kind and a small
// detail object. Nothing in the engine reads these rows to make a decision.

const (
	clientEventMaxBody    = 4 << 10 // 4 KB request cap
	clientEventMaxDetail  = 1 << 10 // 1 KB cap on the re-encoded detail object
	clientEventRateMax    = 30      // reports per user per window
	clientEventRateWindow = time.Minute
	// clientEventMaxNumber bounds a detail number's magnitude. Go accepts numbers
	// Postgres numeric cannot hold (1e1000000), and a tiny token like 1e100000
	// reads back as a 100 KB digit string.
	clientEventMaxNumber = 1e15
)

// clientEventKinds is the allowlist of kinds the write route accepts. Adding a
// kind is a code change, so a buggy or hostile client cannot fill the table with
// made-up kinds. The read route takes any kind, so retired kinds stay readable.
var clientEventKinds = map[string]struct{}{
	// LLM-844: the figure at the treeline.
	"treeline_figure_shown":   {},
	"treeline_figure_gone":    {},
	"treeline_figure_skipped": {},
}

// ClientEventStore writes and reads client_event rows. *pg.ClientEventsRepo
// satisfies it; the narrow seam keeps httpapi off the pg package, like
// SettlementStore.
type ClientEventStore interface {
	InsertClientEvent(ctx context.Context, ev sim.ClientEvent) error
	LoadClientEvents(ctx context.Context, filter sim.ClientEventFilter, limit int) ([]sim.ClientEvent, error)
	CountClientEvents(ctx context.Context, filter sim.ClientEventFilter) ([]sim.ClientEventDayCount, error)
}

// handleClientEvent records one client-reported event. requireAuth has set the
// session user. 503 when no store is wired, 429 over the rate cap, 400 on a bad
// body, an unknown kind, or a detail that is not a small JSON object.
func (s *Server) handleClientEvent(w http.ResponseWriter, r *http.Request) {
	if s.clientEvents == nil {
		writeError(w, http.StatusServiceUnavailable, "client event store not configured")
		return
	}
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	if !s.clientEventLimiter.allow(user.Username) {
		writeError(w, http.StatusTooManyRequests, "client-event rate limit exceeded")
		return
	}

	var in struct {
		Kind   string          `json:"kind"`
		Detail json.RawMessage `json:"detail"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, clientEventMaxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid client-event body")
		return
	}
	// One Decode reads only the first JSON value. Read on to EOF so trailing
	// documents or garbage are refused and the body cap covers the whole body.
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid client-event body")
		return
	}

	kind := strings.TrimSpace(in.Kind)
	if _, ok := clientEventKinds[kind]; !ok {
		writeError(w, http.StatusBadRequest, "unknown client event kind")
		return
	}
	detail, ok := clientEventDetail(in.Detail)
	if !ok {
		writeError(w, http.StatusBadRequest, "detail must be a flat JSON object of strings, numbers, booleans or nulls, at most 1 KB")
		return
	}

	ev := sim.ClientEvent{
		At:       time.Now().UTC(),
		Username: user.Username,
		Kind:     kind,
		Detail:   detail,
	}
	if snap := s.world.Published(); snap != nil {
		if id, _, found := findPCSnapshotByLogin(snap, user.Username); found {
			ev.ActorID = id
		}
	}
	if err := s.clientEvents.InsertClientEvent(r.Context(), ev); err != nil {
		writeError(w, http.StatusInternalServerError, "store client event")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// clientEventDetail returns the detail re-encoded by the engine, or "{}" when it
// is absent or null. The detail must be a flat object whose values are strings,
// finite numbers within clientEventMaxNumber, booleans or nulls, with no NUL in
// any key or string, and at most clientEventMaxDetail bytes once re-encoded.
// Valid JSON is not always valid jsonb (Postgres refuses \u0000, an unpaired
// surrogate, or a number past numeric's range), so the engine never stores the
// client's bytes: decoding replaces an unpaired surrogate with U+FFFD, and the
// checks here refuse the rest as a 400 instead of a 500 at insert.
func clientEventDetail(raw json.RawMessage) ([]byte, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return []byte("{}"), true
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, false
	}
	for k, v := range obj {
		if strings.ContainsRune(k, 0) {
			return nil, false
		}
		switch val := v.(type) {
		case string:
			if strings.ContainsRune(val, 0) {
				return nil, false
			}
		case json.Number:
			f, err := val.Float64()
			if err != nil || math.IsNaN(f) || math.Abs(f) > clientEventMaxNumber {
				return nil, false
			}
			// Store the parsed value, not the client's token: 1e-1000000 parses
			// to 0 here but is past numeric's scale as written.
			obj[k] = f
		case bool, nil:
		default:
			// Nested objects and arrays: the detail is flat.
			return nil, false
		}
	}
	out, err := json.Marshal(obj)
	if err != nil || len(out) > clientEventMaxDetail {
		return nil, false
	}
	return out, true
}

// ClientEventDayCountDTO is one kind's count on one UTC day.
type ClientEventDayCountDTO struct {
	Day   string `json:"day"` // YYYY-MM-DD, UTC
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// ClientEventDTO is one stored event on the wire.
type ClientEventDTO struct {
	At       time.Time       `json:"at"`
	Username string          `json:"username"`
	ActorID  string          `json:"actor_id,omitempty"`
	Kind     string          `json:"kind"`
	Detail   json.RawMessage `json:"detail"`
}

// UmbilicalClientEventsDTO is the GET /umbilical/client-events response: counts
// by UTC day and kind over the whole window, plus the most recent rows. has_more
// is true only when the rows hit the cap; the counts are never capped.
type UmbilicalClientEventsDTO struct {
	ContractVersion int                      `json:"contract_version"`
	Counts          []ClientEventDayCountDTO `json:"counts"`
	Returned        int                      `json:"returned"`
	HasMore         bool                     `json:"has_more"`
	Events          []ClientEventDTO         `json:"events"`
}

// handleUmbilicalClientEvents serves counts and recent client events. Optional
// query params: kind, since / until (RFC3339, at window [since, until)), limit
// (same parse + cap as /actions). 400 on an unparseable since/until; 503 when no
// store is wired.
func (s *Server) handleUmbilicalClientEvents(w http.ResponseWriter, r *http.Request) {
	if s.clientEvents == nil {
		writeError(w, http.StatusServiceUnavailable, "client event store not configured")
		return
	}
	q := r.URL.Query()
	filter := sim.ClientEventFilter{Kind: strings.TrimSpace(q.Get("kind"))}
	if raw := q.Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be RFC3339")
			return
		}
		filter.Since = t
	}
	if raw := q.Get("until"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "until must be RFC3339")
			return
		}
		filter.Until = t
	}

	counts, err := s.clientEvents.CountClientEvents(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "count client events")
		return
	}
	limit := parseActionsLimit(q.Get("limit"))
	rows, err := s.clientEvents.LoadClientEvents(r.Context(), filter, limit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load client events")
		return
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	out := UmbilicalClientEventsDTO{
		ContractVersion: ContractVersion,
		Counts:          make([]ClientEventDayCountDTO, 0, len(counts)),
		Returned:        len(rows),
		HasMore:         hasMore,
		Events:          make([]ClientEventDTO, 0, len(rows)),
	}
	for _, c := range counts {
		out.Counts = append(out.Counts, ClientEventDayCountDTO{
			Day:   c.Day.UTC().Format("2006-01-02"),
			Kind:  c.Kind,
			Count: c.Count,
		})
	}
	for _, ev := range rows {
		detail := json.RawMessage(ev.Detail)
		if len(detail) == 0 {
			detail = json.RawMessage("{}")
		}
		out.Events = append(out.Events, ClientEventDTO{
			At:       ev.At,
			Username: ev.Username,
			ActorID:  string(ev.ActorID),
			Kind:     ev.Kind,
			Detail:   detail,
		})
	}
	writeJSON(w, out)
}
