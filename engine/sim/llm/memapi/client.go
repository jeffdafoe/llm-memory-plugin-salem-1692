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

	persistBackoffs []time.Duration
}

// Option configures a Client at construction time.
type Option func(*Client)

// WithHTTPClient replaces the default http.Client (90s timeout). Mainly
// for tests that need to swap in a server-side fixture's transport.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithTimeout overrides the default 90s HTTP timeout. Applied to the
// adapter's internal http.Client; ignored if WithHTTPClient also set a
// caller-supplied client.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if c.httpClient != nil && d > 0 {
			c.httpClient.Timeout = d
		}
	}
}

// WithPersistBackoffs overrides the retry schedule for
// PersistToolResults. Mainly for tests that want zero-delay retries.
// Pass a slice where the first element is the initial-attempt delay
// (typically 0) and subsequent elements are inter-attempt delays.
func WithPersistBackoffs(backoffs []time.Duration) Option {
	return func(c *Client) {
		if backoffs != nil {
			c.persistBackoffs = backoffs
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
		baseURL:         strings.TrimRight(baseURL, "/"),
		apiKey:          apiKey,
		httpClient:      &http.Client{Timeout: DefaultTimeout},
		persistBackoffs: defaultPersistBackoffs,
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
	Wait            bool         `json:"wait"`
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
		ToAgents:        []string{req.Model},
		Message:         message,
		ToolsOffered:    toAPITools(req.Tools),
		ToolCallResults: results,
		SceneID:         req.SceneID,
		Wait:            true,
	})
	if err != nil {
		return llm.Response{}, &llm.Error{
			Class:   llm.ErrorMalformed,
			Message: "memapi: marshal request: " + err.Error(),
			Cause:   err,
		}
	}

	respBytes, err := c.post(ctx, body)
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
		_, err := c.post(ctx, body)
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

// --- HTTP plumbing --------------------------------------------------------

// post issues a POST to /v1/chat/send with the given JSON body. Returns
// the response body on 2xx; an error otherwise. Errors are typed:
// statusError for non-2xx, context errors for ctx cancellation, or raw
// errors for transport failures. The caller wraps via toLLMError.
func (c *Client) post(ctx context.Context, body []byte) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/chat/send", bytes.NewReader(body))
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

	respBytes, _ := io.ReadAll(resp.Body)
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
		args, err := json.Marshal(tc.Input)
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
	// Status-bearing — split 4xx/5xx.
	var se *statusError
	if errors.As(err, &se) {
		class := llm.ErrorTransport
		if se.status >= 400 && se.status < 500 {
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
