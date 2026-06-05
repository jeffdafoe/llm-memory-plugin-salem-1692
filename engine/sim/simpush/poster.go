package simpush

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// poster.go — the HTTP half of the daily push. Kept separate from the memapi
// LLM adapter (engine/sim/llm/memapi): that client is the chat/memory surface;
// this is a one-endpoint fire-and-forget poster with its own non-fatal status
// handling. Both authenticate as salem-engine with the same key.

// pushTimeout bounds a single conversation-day POST. The endpoint distills +
// saveNotes server-side but returns a small ack; 30s is generous headroom.
const pushTimeout = 30 * time.Second

// errSkippedNonSim and errSkippedUnknown mark the two contract-expected,
// non-fatal PostDay outcomes: the API rejects an agent that isn't in sim
// dream_mode (400) or doesn't know the agent at all (404). The engine pushes for
// every agentized actor and the API filters, so these are routine. They are
// returned (not logged here) so the dispatcher can fold them into a single
// per-day summary instead of one alarming "api 400" line per actor.
var (
	errSkippedNonSim  = errors.New("agent not in sim dream_mode")
	errSkippedUnknown = errors.New("agent unknown to API")
)

// pushEvent is one row in the POST body — the wire shape of a sim.SimDayEvent.
// The API narrates each by Kind (sim-conversation-distiller.js narrateEvent).
// Speaker labels the line so cross-actor overheard speech renders under the
// real speaker, not the day's target agent.
type pushEvent struct {
	At      time.Time      `json:"at"`
	Kind    string         `json:"kind"`
	Payload map[string]any `json:"payload"`
	Speaker string         `json:"speaker,omitempty"`
}

// pushBody is the /v1/sim/conversation-day request envelope.
type pushBody struct {
	Agent  string      `json:"agent"`
	Day    string      `json:"day"`
	Events []pushEvent `json:"events"`
}

// HTTPPoster POSTs day batches to llm-memory-api. Construct with NewHTTPPoster.
type HTTPPoster struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewHTTPPoster builds the poster. baseURL is the memory-api root (the path is
// appended here); apiKey is the salem-engine service key sent as a Bearer
// token. Panics on empty inputs (wiring bug, surfaced at startup).
func NewHTTPPoster(baseURL, apiKey string) *HTTPPoster {
	if baseURL == "" {
		panic("simpush: NewHTTPPoster requires a non-empty baseURL")
	}
	if apiKey == "" {
		panic("simpush: NewHTTPPoster requires a non-empty apiKey")
	}
	return &HTTPPoster{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: pushTimeout},
	}
}

// PostDay marshals and POSTs one (agent, day, events) batch.
//
// 400 (agent not dream_mode=sim) and 404 (agent unknown to the API) are
// contract-expected, non-fatal outcomes — the engine pushes for every agentized
// actor and the API filters — so they are returned as the errSkippedNonSim /
// errSkippedUnknown sentinels rather than logged here. The dispatcher recognizes
// them and folds them into a single per-day summary instead of one line per
// actor (a backlog boot would otherwise emit one alarming "api 400" line per
// non-dreaming actor for every caught-up day). Other non-2xx and transport
// failures are real errors, so the dispatcher leaves the day's cursor un-stamped
// and retries. Ported from v1's postSimDay.
func (p *HTTPPoster) PostDay(ctx context.Context, agent, day string, events []sim.SimDayEvent) error {
	body, err := json.Marshal(pushBody{
		Agent:  agent,
		Day:    day,
		Events: toWireEvents(events),
	})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	url := p.baseURL + "/v1/sim/conversation-day"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	// Cap the body read so a misbehaving response can't balloon engine memory;
	// 64KB is ample for the small JSON ack the API returns.
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	switch {
	case resp.StatusCode == http.StatusBadRequest:
		// Most common: agent isn't dream_mode=sim. Contract-expected; the
		// dispatcher counts these, it does not treat them as a failure.
		return errSkippedNonSim
	case resp.StatusCode == http.StatusNotFound:
		// Agent unknown to the API (e.g. a decorative NPC with no
		// agent_configuration row). Contract-expected.
		return errSkippedUnknown
	case resp.StatusCode >= 300:
		return fmt.Errorf("api %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// toWireEvents maps the engine-side day events to the wire shape. Returns a
// non-nil empty slice so the body marshals "events":[] not "events":null — the
// API rejects a null events array before its dream_mode short-circuit.
func toWireEvents(events []sim.SimDayEvent) []pushEvent {
	out := make([]pushEvent, 0, len(events))
	for _, e := range events {
		out = append(out, pushEvent{
			At:      e.At,
			Kind:    string(e.Kind),
			Payload: e.Payload,
			Speaker: e.Speaker,
		})
	}
	return out
}
