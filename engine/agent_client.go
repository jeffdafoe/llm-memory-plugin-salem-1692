package main

// HTTP client for llm-memory-api's /agent/tick endpoint.
//
// Sends perception or full message-history bodies and parses tool-call
// responses. The agent's API key comes from village_agent.llm_memory_api_key —
// same column the rest of the salem→llm-memory-api integration uses.
//
// Connection model: stateless. Each call is a fresh HTTP request with the
// agent's API key as a Bearer token. The /v1 middleware in llm-memory-api
// accepts API keys after MEM-2026-04-25 (commit c06640d) — sessions aren't
// needed for service-to-service callers.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// agentToolDef is the neutral tool spec (matches the providers/index.js opts.tools
// contract on the API side: { name, description, parameters }). Parameters is a
// JSON Schema. Marshaled directly into the request body.
type agentToolDef struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// agentMessage is the OpenAI-shape conversation entry. Role is one of
// "user" | "assistant" | "tool". Content holds plain text. Assistant messages
// MAY also carry ToolCalls (the LLM asking the engine to do something).
// Tool messages MUST carry ToolCallID matching the assistant's tool_call id.
type agentMessage struct {
	Role       string             `json:"role"`
	Content    string             `json:"content"`
	ToolCalls  []agentMessageCall `json:"tool_calls,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
}

// agentMessageCall is the OpenAI-shape tool-call entry inside an assistant
// message. Arguments is a JSON-string (per OpenAI's API), distinct from the
// parsed-input shape returned in /agent/tick responses.
type agentMessageCall struct {
	ID       string                  `json:"id"`
	Type     string                  `json:"type"` // always "function"
	Function agentMessageCallDetails `json:"function"`
}

type agentMessageCallDetails struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// agentTickRequest is the /agent/tick body. Exactly one of Perception or
// Messages must be set — the route rejects both.
type agentTickRequest struct {
	Perception string         `json:"perception,omitempty"`
	Messages   []agentMessage `json:"messages,omitempty"`
	Tools      []agentToolDef `json:"tools,omitempty"`
	System     string         `json:"system,omitempty"`
}

// agentToolCall is the parsed tool-call returned in /agent/tick responses.
// Distinct from agentMessageCall: input is already an object, not a string.
type agentToolCall struct {
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

// agentTickResponse is the /agent/tick response body.
type agentTickResponse struct {
	Agent     string          `json:"agent"`
	Text      string          `json:"text"`
	ToolCalls []agentToolCall `json:"tool_calls"`
	Usage     struct {
		InputTokens  int     `json:"input_tokens"`
		OutputTokens int     `json:"output_tokens"`
		Cost         float64 `json:"cost,omitempty"`
	} `json:"usage"`
	Cost float64 `json:"cost"`
}

// agentTickError lets callers distinguish rate-limit / cost-limit responses
// from generic failures so the engine can fall through to scheduler-only
// behavior gracefully.
type agentTickError struct {
	Status int
	Code   string
	Body   string
}

func (e *agentTickError) Error() string {
	return fmt.Sprintf("agent/tick %d %s: %s", e.Status, e.Code, e.Body)
}

// agentTickClient is reusable across calls — one client per engine instance.
// The HTTP client is shared (idempotent, thread-safe) and inherits the
// process-wide connection pool.
type agentTickClient struct {
	baseURL string
	http    *http.Client
}

func newAgentTickClient(baseURL string) *agentTickClient {
	return &agentTickClient{
		baseURL: baseURL,
		// 90s timeout fits inside the API-side 120s default and leaves room
		// for retries within a single tick budget if added later.
		http: &http.Client{Timeout: 90 * time.Second},
	}
}

// callTick sends the request and returns the parsed response. apiKey is the
// agent's plaintext API key from village_agent.llm_memory_api_key. ctx is
// honored for cancellation but not for the HTTP timeout — the http.Client
// timeout governs that independently.
func (c *agentTickClient) callTick(ctx context.Context, apiKey string, req agentTickRequest) (*agentTickResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal tick request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.baseURL, "/")+"/v1/agent/tick", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build tick request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("tick http: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Try to extract the API's structured error code; fall back to status.
		var errBody struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(respBytes, &errBody)
		return nil, &agentTickError{
			Status: resp.StatusCode,
			Code:   errBody.Error.Code,
			Body:   string(respBytes),
		}
	}

	var out agentTickResponse
	if err := json.Unmarshal(respBytes, &out); err != nil {
		return nil, fmt.Errorf("parse tick response: %w (body: %s)", err, string(respBytes))
	}
	return &out, nil
}
