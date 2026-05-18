// Package memapi is the production HTTP adapter that satisfies
// engine/sim/llm.Client (and the optional ToolResultPersister) against
// llm-memory-api's /v1/chat/send endpoint.
//
// # Why this exists
//
// All LLM access in salem goes through llm-memory-api — never direct to
// a provider. The engine identifies as the `salem-engine` actor (its
// own service-account API key, distinct from any developer-agent
// credentials). memory-api routes the request to the right VA slug
// (req.Model → to_agents), translates the neutral tool spec to the
// provider's native shape, and writes chat_messages rows so admin
// dashboards can replay the conversation.
//
// # Stateless from the caller, stateful on the wire
//
// The llm.Client contract is "every Complete call is stateless from
// the Client's perspective" — the harness owns the full transcript.
// memory-api, by contrast, is stateful per (from_agent, to_agent[,
// scene_id]): chat_messages rows persist and are loaded as history on
// the next dispatch.
//
// The adapter bridges this by extracting only the NEW turn from
// req.Messages on every call:
//
//   - Trailing run of tool messages → tool_call_results
//     (one per call ID the assistant emitted in its prior reply).
//   - Otherwise → take the last user message; if a system message
//     precedes it, concatenate with "\n\n" between them and send as
//     the `message` field. salem-generic has blank
//     startup_instructions, so callers push the full prompt inline.
//
// SceneID scoping is the isolation mechanism: cascade consumers
// (atmosphere, noticeboard) mint a fresh UUID per call so each
// completion is its own conversation. Multi-iteration callers (the
// harness) mint once at tick start and reuse, so tool_call →
// tool_result pairs thread within one scene.
//
// # Persist-only path
//
// When the harness's terminal-class tool fires (done() / unknown),
// the assistant message that contained the terminal tool_call has
// already been written to chat_messages by the prior Complete — but
// the matching tool_result has not (no follow-up Complete on terminal
// end). That leaves an orphan tool_use in history which corrupts the
// next tool-use call against the same VA (Anthropic 400 "tool_use
// without tool_result"). v1 closed this with persist_only=true,
// wait=false; v2 exposes the same mechanism via the optional
// llm.ToolResultPersister interface and implements it here with v1's
// 3-attempt exponential backoff (200ms / 600ms) — 5xx + transport
// failures retry, other 4xx bail.
//
// # Error classification
//
//	5xx, Do() failure       → llm.ErrorTransport
//	ctx.Err()               → llm.ErrorContextCancelled
//	4xx                     → llm.ErrorMalformed (caller-side bug)
//	parse fail / missing    → llm.ErrorMalformed
//	reply field
//
// ErrorProviderRefusal and ErrorTooLarge are not mapped today —
// memory-api doesn't surface them as distinct status codes. If a
// provider-refusal class needs handling later, the API would have to
// expose it in the error body.
package memapi
