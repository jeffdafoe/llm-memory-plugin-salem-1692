// Package llm is the provider-neutral LLM client interface used by the
// agent-tick execution pipeline (Phase 2 PR 3d). It exposes the types the
// harness loop (engine/sim/handlers) hands to and receives from a provider
// — Request, Response, Message, ToolSpec, RawToolCall — and the Client
// interface a real or faked implementation satisfies.
//
// # Provider-neutral
//
// This package has NO HTTP, NO provider SDK dependencies. The real HTTP
// adapter that translates these types to Anthropic / OpenAI / etc. and
// back lives in the cutover layer, NOT here. PR 3d ships only the types
// and a FakeClient that returns scripted responses for deterministic
// pipeline tests.
//
// # 3-stage parse/validate ownership
//
// The Client owns provider-format decode and gross byte limits — nothing
// more. It treats RawToolCall.Arguments as opaque json.RawMessage. The
// tool registry (in engine/sim/handlers) validates against per-tool
// schemas and produces ValidatedCalls; the harness (also in handlers)
// owns loop policy — budgets, routing, terminal evaluation, stale checks,
// and CompleteReactorTick. See the PR 3d design note's "Parse/validate
// ownership" subsection for the boundary contract.
//
// # Transcript model
//
// Each tick uses one fresh provider transcript. Within a tick, iterations
// continue the SAME transcript: after each Complete the harness appends
// the assistant message (with ToolCalls populated) plus one "tool"
// Message per provider call ID, then calls Complete again. Perception is
// built ONCE per tick — no re-rendering between iterations. See the PR 3d
// design note §6 transcript model.
//
// # Error classification
//
// Every Complete error must be classifiable into one of the ErrorClass
// values: the harness's CompleteReactorTick policy table reads
// TickResult.LLMErrorClass to decide TerminalStatus. Adapters wrap their
// failures in *Error; the harness reads them via Classify. An error that
// classifies as ErrorUnknown is itself a bug to surface — extend Classify
// rather than silently coercing to a known class.
package llm
