package memapi

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

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// DefaultTimeout matches v1's npcChatClient — wait=true blocks on the
// upstream LLM call, so a single Complete can legitimately sit for
// 30-60s. 90s gives headroom without indefinite hangs.
const DefaultTimeout = 90 * time.Second

// defaultPersistBackoffs is v1's 3-attempt schedule: first attempt
// immediate, then 200ms, then 600ms. Covers brief network drops and
// 5xx blips without growing the tail latency unboundedly.
var defaultPersistBackoffs = []time.Duration{0, 200 * time.Millisecond, 600 * time.Millisecond}

// Client is the memory-api HTTP adapter. Implements llm.Client and
// llm.ToolResultPersister.
//
// Holds the salem-engine API key — every Complete originates from
// salem-engine, only req.Model (the to_agents target) varies.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client

	// httpClientSupplied tracks whether httpClient came from
	// WithHTTPClient (true) or from the adapter's default
	// constructor path (false). WithTimeout reads this so it only
	// mutates the default — never a caller-owned http.Client. Makes
	// option ordering irrelevant (R1 finding #3).
	httpClientSupplied bool

	persistBackoffs []time.Duration
}

// Option configures a Client at construction time.
type Option func(*Client)

// WithHTTPClient replaces the default http.Client (90s timeout). Mainly
// for tests that need to swap in a server-side fixture's transport.
// Marks the client as caller-supplied so WithTimeout won't mutate it,
// regardless of option order.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
			c.httpClientSupplied = true
		}
	}
}

// WithTimeout overrides the default 90s HTTP timeout. Applies ONLY to
// the adapter's default http.Client — caller-supplied clients (via
// WithHTTPClient) are left alone, regardless of option order. Pass
// nothing to keep the default.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 && !c.httpClientSupplied {
			c.httpClient.Timeout = d
		}
	}
}

// WithPersistBackoffs overrides the retry schedule for
// PersistToolResults. Mainly for tests that want zero-delay retries.
// Pass a slice where the first element is the initial-attempt delay
// (typically 0) and subsequent elements are inter-attempt delays. The
// caller's slice is copied so later mutation can't change retry
// behavior post-construction. Empty slices are rejected (would
// silently drop persist — R1 finding #1).
func WithPersistBackoffs(backoffs []time.Duration) Option {
	return func(c *Client) {
		if len(backoffs) > 0 {
			c.persistBackoffs = append([]time.Duration(nil), backoffs...)
		}
	}
}

// NewClient constructs a memapi adapter. baseURL is the memory-api root
// (e.g. "https://memory.example.com" — the adapter appends /v1/chat/send
// itself). apiKey is the salem-engine service-account key passed as a
// Bearer token. Both are required; panics on empty.
//
// Apply options to tune timeout, swap the http.Client, or override the
// persist retry schedule.
func NewClient(baseURL, apiKey string, opts ...Option) *Client {
	if baseURL == "" {
		panic("memapi: NewClient requires a non-empty baseURL")
	}
	if apiKey == "" {
		panic("memapi: NewClient requires a non-empty apiKey")
	}
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: DefaultTimeout},
		// Copy the package-default slice so a Client's backoffs are
		// always its own — defense against accidental mutation of the
		// package-level default and against WithPersistBackoffs
		// aliasing (R1 finding #2).
		persistBackoffs: append([]time.Duration(nil), defaultPersistBackoffs...),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// --- wire types -----------------------------------------------------------

// chatRequest is the /v1/chat/send body. Mirrors v1's chatSendRequest
// minus the from_agent field (the API enforces from_agent ==
// authenticated agent for non-admin sessions).
type chatRequest struct {
	ToAgents        []string     `json:"to_agents"`
	Message         string       `json:"message,omitempty"`
	ToolsOffered    []apiTool    `json:"tools_offered,omitempty"`
	ToolCallResults []toolResult `json:"tool_call_results,omitempty"`
	PersistOnly     bool         `json:"persist_only,omitempty"`
	SceneID         string       `json:"scene_id,omitempty"`
	// ConversationID is the engine's narrative-beat scene id — the stable
	// grouping key the admin chat viewer collapses a whole exchange under
	// (distinct from the per-tick scene_id). Omitted when empty. ZBBS-HOME-397.
	ConversationID string `json:"conversation_id,omitempty"`
	// EphemeralContext (lean sim-history): per-tick affordances / world-state
	// the API attaches to the current turn but never persists. Sent on every
	// Complete; absent on persist-only calls (no perception).
	EphemeralContext string `json:"ephemeral_context,omitempty"`
	Wait             bool   `json:"wait"`
}

// apiTool is the neutral tool spec sent in tools_offered. parameters is
// passed through opaquely — the engine's tool registry produces the
// provider-shaped JSON schema and the adapter must not re-shape it.
type apiTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// toolResult is one engine-side answer to a tool the model called.
type toolResult struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// chatResponse is the /v1/chat/send response when wait=true. The route
// also returns from_agent / to_agents / sent_at, but the adapter only
// needs the reply.
type chatResponse struct {
	Reply *replyPayload `json:"reply"`
}

type replyPayload struct {
	Text      string        `json:"text"`
	ToolCalls []apiToolCall `json:"tool_calls"`
}

type apiToolCall struct {
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

// statusError is the internal HTTP-status-bearing error used to drive
// retry decisions in PersistToolResults. Translated to *llm.Error at
// the public boundary (Complete and PersistToolResults both wrap).
type statusError struct {
	status int
	body   string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("memapi: status %d: %s", e.status, truncate(e.body, 200))
}

// --- Complete -------------------------------------------------------------

// Complete implements llm.Client. Builds the chatRequest from the
// trailing turn in req.Messages (see package doc for the turn-extraction
// rule), POSTs to /v1/chat/send with wait=true, and maps the response
// into llm.Response.
func (c *Client) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if req.Model == "" {
		return llm.Response{}, &llm.Error{
			Class:   llm.ErrorMalformed,
			Message: "memapi: req.Model required (maps to to_agents)",
		}
	}

	message, results, err := extractTurn(req.Messages)
	if err != nil {
		return llm.Response{}, &llm.Error{
			Class:   llm.ErrorMalformed,
			Message: "memapi: extract turn: " + err.Error(),
		}
	}

	body, err := json.Marshal(chatRequest{
		ToAgents:         []string{req.Model},
		Message:          message,
		ToolsOffered:     toAPITools(req.Tools),
		ToolCallResults:  results,
		SceneID:          req.SceneID,
		ConversationID:   req.ConversationID,
		EphemeralContext: req.EphemeralContext,
		Wait:             true,
	})
	if err != nil {
		return llm.Response{}, &llm.Error{
			Class:   llm.ErrorMalformed,
			Message: "memapi: marshal request: " + err.Error(),
			Cause:   err,
		}
	}

	respBytes, err := c.post(ctx, "/v1/chat/send", body)
	if err != nil {
		return llm.Response{}, toLLMError(err)
	}

	return parseChatResponse(respBytes)
}

// --- PersistToolResults ---------------------------------------------------

// PersistToolResults implements llm.ToolResultPersister. Writes the
// tool-result rows to memory-api history without firing a follow-up
// LLM call. Used by the harness after a terminal-class tool fires —
// see package doc for the orphan-tool_use story.
//
// Retry: 5xx + transport failures retry on c.persistBackoffs; other
// 4xx (caller-side bug) bail immediately. ctx cancellation bails too.
func (c *Client) PersistToolResults(ctx context.Context, req llm.PersistRequest) error {
	if req.Model == "" {
		return &llm.Error{
			Class:   llm.ErrorMalformed,
			Message: "memapi: PersistRequest.Model required",
		}
	}
	if len(req.Results) == 0 {
		return &llm.Error{
			Class:   llm.ErrorMalformed,
			Message: "memapi: PersistRequest.Results must be non-empty",
		}
	}

	body, err := json.Marshal(chatRequest{
		ToAgents:        []string{req.Model},
		ToolCallResults: toWireResults(req.Results),
		PersistOnly:     true,
		SceneID:         req.SceneID,
		ConversationID:  req.ConversationID,
		Wait:            false,
	})
	if err != nil {
		return &llm.Error{
			Class:   llm.ErrorMalformed,
			Message: "memapi: marshal persist request: " + err.Error(),
			Cause:   err,
		}
	}

	var lastErr error
	for _, delay := range c.persistBackoffs {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctxErr(ctx)
			}
		}
		_, err := c.post(ctx, "/v1/chat/send", body)
		if err == nil {
			return nil
		}
		lastErr = err
		// 4xx other than 429 — bail.
		var se *statusError
		if errors.As(err, &se) && se.status >= 400 && se.status < 500 && se.status != 429 {
			return toLLMError(err)
		}
	}
	return toLLMError(lastErr)
}

// --- SearchMemory ---------------------------------------------------------

// searchMemoryRequest is the /v1/memory/search body. namespace scopes the
// search to a single agent's memory — recall passes the acting NPC's own
// namespace, never "*".
type searchMemoryRequest struct {
	Query     string `json:"query"`
	Namespace string `json:"namespace"`
	Limit     int    `json:"limit"`
}

// searchMemoryHitWire is one note-grouped result on the wire. Mirrors v1's
// searchMemoryHit (engine/agent_client.go). Decoded into llm.MemoryHit.
type searchMemoryHitWire struct {
	SourceFile string  `json:"source_file"`
	Heading    string  `json:"heading"`
	ChunkText  string  `json:"chunk_text"`
	Namespace  string  `json:"namespace"`
	Similarity float64 `json:"similarity"`
	ChunkCount int     `json:"chunk_count"`
}

type searchMemoryResponse struct {
	Results []searchMemoryHitWire `json:"results"`
}

// SearchMemory implements llm.MemorySearcher. POSTs /v1/memory/search scoped
// to a single namespace with the salem-engine API key — the port of v1's
// npcChatClient.searchMemory. The recall observation tool is the only caller
// today. An empty result set is returned as an empty slice, not an error.
//
// Errors carry no llm.Error classification (unlike Complete): recall's
// ObservationFn turns any error into an in-character "the memory wouldn't
// come" tool result, so the caller only needs to know success-vs-failure.
func (c *Client) SearchMemory(ctx context.Context, namespace, query string, limit int) ([]llm.MemoryHit, error) {
	body, err := json.Marshal(searchMemoryRequest{Query: query, Namespace: namespace, Limit: limit})
	if err != nil {
		return nil, fmt.Errorf("memapi: marshal search request: %w", err)
	}
	respBytes, err := c.post(ctx, "/v1/memory/search", body)
	if err != nil {
		return nil, fmt.Errorf("memapi: search: %w", err)
	}
	var resp searchMemoryResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("memapi: parse search response: %w", err)
	}
	hits := make([]llm.MemoryHit, 0, len(resp.Results))
	for _, h := range resp.Results {
		hits = append(hits, llm.MemoryHit{
			SourceFile: h.SourceFile,
			Heading:    h.Heading,
			ChunkText:  h.ChunkText,
			Namespace:  h.Namespace,
			Similarity: h.Similarity,
			ChunkCount: h.ChunkCount,
		})
	}
	return hits, nil
}

// --- FetchRateLimits ------------------------------------------------------

// RateLimit is one agent's effective rate-limit config as resolved by
// memory-api (LLM-156): the global config-table defaults merged with the
// agent's per-agent override. The engine paces per-agent tick emission to stay
// under Limit within WindowMS, so a shared VA never bursts into its CooldownMS
// lockout. Window/cooldown are milliseconds — the unit the limiter enforces, so
// the engine never paces against a rounded second.
type RateLimit struct {
	Limit      int
	WindowMS   int
	CooldownMS int
}

// rateLimitRequest / rateLimitResponse mirror POST /v1/agent/rate-limit.
type rateLimitRequest struct {
	Agents []string `json:"agents"`
}

type rateLimitWire struct {
	Limit      int `json:"limit"`
	WindowMS   int `json:"window_ms"`
	CooldownMS int `json:"cooldown_ms"`
}

type rateLimitResponse struct {
	Limits map[string]rateLimitWire `json:"limits"`
}

// FetchRateLimits resolves the effective per-agent rate limit for each VA slug
// via POST /v1/agent/rate-limit. Called once at engine startup so the reactor
// can pace per-agent tick emission under the cap memory-api enforces (LLM-156).
// Returns a map keyed by slug; slugs the server omits are simply absent (the
// caller leaves them ungated). An empty input returns an empty map, no call.
func (c *Client) FetchRateLimits(ctx context.Context, agents []string) (map[string]RateLimit, error) {
	if len(agents) == 0 {
		return map[string]RateLimit{}, nil
	}
	body, err := json.Marshal(rateLimitRequest{Agents: agents})
	if err != nil {
		return nil, fmt.Errorf("memapi: marshal rate-limit request: %w", err)
	}
	respBytes, err := c.post(ctx, "/v1/agent/rate-limit", body)
	if err != nil {
		return nil, fmt.Errorf("memapi: rate-limit: %w", err)
	}
	var resp rateLimitResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("memapi: parse rate-limit response: %w", err)
	}
	out := make(map[string]RateLimit, len(resp.Limits))
	for slug, e := range resp.Limits {
		out[slug] = RateLimit{
			Limit:      e.Limit,
			WindowMS:   e.WindowMS,
			CooldownMS: e.CooldownMS,
		}
	}
	return out, nil
}

// --- HTTP plumbing --------------------------------------------------------

// post issues a POST to baseURL+path with the given JSON body. Returns
// the response body on 2xx; an error otherwise. Errors are typed:
// statusError for non-2xx, context errors for ctx cancellation, or raw
// errors for transport failures. The caller wraps via toLLMError.
//
// path is parameterized so the chat endpoints (/v1/chat/send) and the
// memory-search endpoint (/v1/memory/search, ZBBS-WORK-321) share the same
// auth + status + transport handling.
func (c *Client) post(ctx context.Context, path string, body []byte) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		// http.Client.Do returns ctx errors as the cause inside a
		// url.Error — Classify walks errors.Is to find them, so we
		// just propagate and let the public boundary classify.
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	respBytes, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		// Surface body-read failures as transport — not malformed.
		// A partial 2xx body parsed by parseChatResponse would
		// otherwise be reported as Malformed when the real cause is
		// network/transport (R1 finding #4).
		return nil, fmt.Errorf("read response body: %w", readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &statusError{status: resp.StatusCode, body: string(respBytes)}
	}
	return respBytes, nil
}

// --- turn extraction ------------------------------------------------------

// extractTurn pulls the new-turn payload out of req.Messages. Returns
// either a non-empty message string (with empty results) or a non-empty
// results slice (with empty message). Errors only on malformed input
// (no user message, tool message missing call ID).
//
// Algorithm:
//
//  1. Walk back from the tail counting trailing tool messages. If
//     n > 0, those n tools are the new turn → tool_call_results.
//
//  2. Otherwise, find the LAST user message. Concatenate all
//     preceding system messages (in order) with "\n\n" separators
//     between them and the user content. That string is the
//     `message` field. salem-generic has blank startup_instructions
//     so the engine pushes the full prompt this way.
func extractTurn(messages []llm.Message) (message string, results []toolResult, err error) {
	if len(messages) == 0 {
		return "", nil, errors.New("messages is empty")
	}

	// Count trailing tool messages.
	trailing := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != llm.RoleTool {
			break
		}
		trailing++
	}

	if trailing > 0 {
		results = make([]toolResult, 0, trailing)
		for i := len(messages) - trailing; i < len(messages); i++ {
			m := messages[i]
			if m.ToolCallID == "" {
				return "", nil, fmt.Errorf("tool message at index %d missing ToolCallID", i)
			}
			results = append(results, toolResult{ID: m.ToolCallID, Content: m.Content})
		}
		return "", results, nil
	}

	// Find the last user message.
	lastUser := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llm.RoleUser {
			lastUser = i
			break
		}
	}
	if lastUser < 0 {
		return "", nil, errors.New("no user message in transcript")
	}

	// Collect preceding system messages (in original order) and the
	// last user message.
	parts := make([]string, 0, 2)
	for i := 0; i < lastUser; i++ {
		if messages[i].Role == llm.RoleSystem {
			parts = append(parts, messages[i].Content)
		}
	}
	parts = append(parts, messages[lastUser].Content)
	return strings.Join(parts, "\n\n"), nil, nil
}

// --- helpers --------------------------------------------------------------

// toAPITools maps the engine's ToolSpec slice to the API's apiTool
// slice. Schema bytes pass through opaquely as parameters.
func toAPITools(tools []llm.ToolSpec) []apiTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]apiTool, len(tools))
	for i, t := range tools {
		out[i] = apiTool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Schema,
		}
	}
	return out
}

// toWireResults maps the engine's ToolResult slice to the wire shape.
// Same struct fields; separated to keep public llm.ToolResult and
// wire-format toolResult independently evolvable.
func toWireResults(rs []llm.ToolResult) []toolResult {
	if len(rs) == 0 {
		return nil
	}
	out := make([]toolResult, len(rs))
	for i, r := range rs {
		out[i] = toolResult{ID: r.ID, Content: r.Content}
	}
	return out
}

// parseChatResponse unmarshals a 2xx body and maps it into llm.Response.
// Returns *llm.Error on parse failure or missing reply field.
func parseChatResponse(respBytes []byte) (llm.Response, error) {
	var out chatResponse
	if err := json.Unmarshal(respBytes, &out); err != nil {
		return llm.Response{}, &llm.Error{
			Class:   llm.ErrorMalformed,
			Message: fmt.Sprintf("memapi: parse response: %v (body=%q)", err, truncate(string(respBytes), 500)),
			Cause:   err,
		}
	}
	if out.Reply == nil {
		// wait=true should always return reply; if it doesn't, the
		// route's no-reply-pending path tripped (multi-recipient or
		// non-VA target) — caller-side bug.
		return llm.Response{}, &llm.Error{
			Class:   llm.ErrorMalformed,
			Message: fmt.Sprintf("memapi: response missing reply field (body=%q)", truncate(string(respBytes), 500)),
		}
	}

	response := llm.Response{Content: out.Reply.Text}
	for i, tc := range out.Reply.ToolCalls {
		// Normalize missing/null input to an empty object. memory-api
		// may emit `"input": null` (or omit it) for zero-arg tools;
		// most tool-arg parsers expect an object and choke on null
		// (R1 finding #5). The empty-object normalization is
		// downstream-friendly and the registry's DisallowUnknownFields
		// decode handles `{}` cleanly.
		input := tc.Input
		if input == nil {
			input = map[string]interface{}{}
		}
		args, err := json.Marshal(input)
		if err != nil {
			return llm.Response{}, &llm.Error{
				Class:   llm.ErrorMalformed,
				Message: fmt.Sprintf("memapi: remarshal tool_call[%d].input: %v", i, err),
				Cause:   err,
			}
		}
		response.ToolCalls = append(response.ToolCalls, llm.RawToolCall{
			ID:        tc.ID,
			Index:     i,
			Name:      tc.Name,
			Arguments: args,
		})
	}
	return response, nil
}

// toLLMError maps an internal post()-or-deeper error into a typed
// *llm.Error suitable for return at the public Complete /
// PersistToolResults boundary. Classification table per package doc.
func toLLMError(err error) error {
	if err == nil {
		return nil
	}
	// Already typed — pass through.
	var typed *llm.Error
	if errors.As(err, &typed) {
		return typed
	}
	// Status-bearing — split 429/4xx/5xx.
	var se *statusError
	if errors.As(err, &se) {
		class := llm.ErrorTransport
		if se.status == 429 {
			// memory-api rejects calls to a VA in rate-limit cooldown
			// with 429 (ZBBS-WORK-404). Not a caller-side bug and not
			// model output — classify honestly so telemetry doesn't
			// book cooldown windows as malformed.
			class = llm.ErrorRateLimited
		} else if se.status >= 400 && se.status < 500 {
			class = llm.ErrorMalformed
		}
		return &llm.Error{
			Class:   class,
			Message: se.Error(),
			Cause:   err,
		}
	}
	// Context cancellation.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &llm.Error{
			Class:   llm.ErrorContextCancelled,
			Message: "memapi: " + err.Error(),
			Cause:   err,
		}
	}
	// Catch-all — transport.
	return &llm.Error{
		Class:   llm.ErrorTransport,
		Message: "memapi: " + err.Error(),
		Cause:   err,
	}
}

// ctxErr wraps ctx.Err() in *llm.Error with ContextCancelled class.
// Used inside the persist retry loop where we explicitly observe
// ctx.Done() between attempts.
func ctxErr(ctx context.Context) error {
	return &llm.Error{
		Class:   llm.ErrorContextCancelled,
		Message: "memapi: ctx cancelled during persist retry",
		Cause:   ctx.Err(),
	}
}

// truncate returns s capped at n bytes. Used in error messages to keep
// arbitrary upstream response bodies from blowing up logs.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
