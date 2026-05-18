package llm

import (
	"encoding/json"
)

// Standard role labels for Message.Role. Use these constants rather than
// string literals — the Client adapter switches on them when translating
// to the provider's native message shape.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Request is one LLM completion call. The harness in engine/sim/handlers
// builds it and passes it to Client.Complete.
//
// Messages is the FULL transcript for the current iteration — the harness
// owns the slice and appends to it across iterations within a tick. The
// Client does NOT mutate or persist it; each Complete call is stateless
// from the Client's perspective.
type Request struct {
	// Messages is the full transcript for this iteration. Initial
	// iteration: [system, user(rendered prompt)]. Subsequent iterations
	// append the prior assistant message (with ToolCalls populated) plus
	// one "tool" Message per provider call ID from that assistant message.
	Messages []Message

	// Tools is the list of advertised tool specs (registry entries where
	// Availability == Available). Disabled tools are NOT included — the
	// model never sees them. May be empty if the harness wants a
	// tool-free completion (rare, but legal).
	Tools []ToolSpec

	// Temperature and MaxTokens are provider model parameters carried
	// through. Zero values mean "use the adapter's default" — interpretation
	// is the adapter's job, not the harness's.
	Temperature float64
	MaxTokens   int

	// Model is the provider's model identifier (e.g. "claude-sonnet-4-6").
	// Empty when the adapter should use its configured default.
	Model string

	// SceneID, when set, identifies the conversation scope for this call.
	// Adapters that support history scoping (memory-api: filters
	// chat_messages by scene_id when loading history for the next VA
	// dispatch) thread this through; adapters that don't ignore it.
	//
	// Single-call cascade consumers (atmosphere, noticeboard) mint a
	// fresh UUID per Complete to isolate one-shot completions from prior
	// runs. Multi-iteration callers (the per-tick harness) mint once at
	// tick start and reuse across iterations so tool_call → tool_result
	// pairs thread within the same scene.
	SceneID string
}

// ToolResult is one engine-side answer to a tool call the model emitted
// in a prior assistant message. Used by ToolResultPersister to write
// orphaned tool-result rows to history without firing a follow-up LLM
// call (see ToolResultPersister doc for why this matters).
type ToolResult struct {
	// ID matches a prior assistant RawToolCall.ID. The adapter writes
	// one history row per ID so the next assistant turn sees all of
	// them in transcript order.
	ID string

	// Content is the result string the model would have seen as the
	// "tool" message body. Same content the harness already appended to
	// its local transcript; this just persists it provider-side.
	Content string
}

// PersistRequest is the input to ToolResultPersister.PersistToolResults.
// Model + SceneID are scoped the same way Request's are; Results is the
// batch from the assistant message that ended the tick.
type PersistRequest struct {
	// Model routes the persist call to the right VA slug (the API uses
	// it to attribute the rows to the same conversation pair the prior
	// assistant message wrote into). Required.
	Model string

	// SceneID scopes the persisted rows the same way Request.SceneID
	// scopes Complete. Should match the SceneID used on the prior
	// Complete that produced the tool_calls being persisted.
	SceneID string

	// Results is the per-call list, ordered to match the model's emit
	// order. Must be non-empty.
	Results []ToolResult
}

// Message is one role-tagged entry in the transcript. Field population
// depends on Role:
//
//   - "system" — Content only
//   - "user" — Content only
//   - "assistant" — Content (may be empty) + ToolCalls (may be empty)
//   - "tool" — Content + ToolCallID (matches a RawToolCall.ID from the
//     prior assistant message)
//
// The Client adapter translates these to the provider's native message
// shape. The harness produces them in this shape and the adapter handles
// any provider-specific re-shaping.
type Message struct {
	Role    string
	Content string

	// ToolCalls is the tool calls emitted by an assistant message. Empty
	// unless Role == "assistant".
	ToolCalls []RawToolCall

	// ToolCallID matches a RawToolCall.ID from the prior assistant
	// message — every "tool" message in the transcript MUST set this so
	// the provider can attribute the result to the right call.
	ToolCallID string
}

// ToolSpec is one advertised tool — what the model sees in its tool list.
// Schema is the raw JSON schema bytes the provider expects (Anthropic's
// "input_schema", OpenAI's "function.parameters"); the registry produces
// it and the Client adapter passes it through opaquely. The Client does
// NOT validate arguments against it — that's the registry's job (3-stage
// parse/validate ownership; see package doc).
type ToolSpec struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// RawToolCall is one tool call as the model emitted it. Arguments is
// opaque to the Client — provider-format decoded into bytes, but NOT
// validated against any tool schema (that's the registry's job).
//
// ID is the opaque provider call ID (Anthropic's "tool_use_id", OpenAI's
// "tool_call_id"). The harness uses it as the ToolCallID on the
// subsequent "tool" message that returns the result, which is how the
// provider attributes multi-call results under native transcript
// continuation (PR 3d transcript model, §6).
//
// Index is the within-response position of this call (0-based). It is a
// secondary disambiguator — providers should make IDs unique per
// response, but Index gives the harness a stable order to surface
// failures against in case an ID is missing or duplicated.
type RawToolCall struct {
	ID        string
	Index     int
	Name      string
	Arguments json.RawMessage
}

// Response is one LLM completion result. The Client populates all fields
// it can; token counts and StopReason are best-effort and may be zero or
// empty depending on the adapter.
type Response struct {
	// Content is the assistant's textual content. May be empty for
	// tool-only responses; the harness must not assume content presence.
	Content string

	// ToolCalls are ordered as emitted by the provider. Length 0 means
	// the model returned a content-only response (no tool calls); the
	// harness should treat that as a tick-ending event since there's
	// nothing actionable to dispatch.
	ToolCalls []RawToolCall

	// StopReason is the provider's stop reason. Verbatim from the adapter
	// — no normalization. Treat unknown values as "ended-for-some-reason"
	// rather than failing the tick.
	StopReason string

	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}
