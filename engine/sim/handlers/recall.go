package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
)

// recall.go — production recall observation tool, ZBBS-WORK-321 (port of v1's
// recall verb, engine/agent_tick.go).
//
// recall is the memory-grounded "try to remember" verb: it semantic-searches
// the acting NPC's OWN llm-memory namespace (notes, dreams, impressions) and
// returns the top hits as a tool-result text block the model reads on its next
// harness iteration. It is an OBSERVATION (ClassObservation) — it never ends
// the tick and never mutates the world; the only side effect is a read against
// llm-memory-api.
//
// Namespace scoping: recall searches in.LLMMemoryAgent (the acting actor's own
// namespace, resolved by the harness from the tick snapshot), never "*". An
// NPC can only remember its own memory.
//
// Failure handling mirrors v1's resolveRecall: every failure mode (empty
// query, no namespace, search error, no hits) returns an in-character text
// string as the tool RESULT, not a handler error — so the model reads a
// graceful "the memory wouldn't come" rather than a "[error: handler_failed]"
// label. The only error return is an args-type mismatch, which is a
// registration bug, not a runtime condition.

const (
	// recallResultLimit caps how many note-grouped hits recall returns.
	// v1 parity (engine/agent_tick.go).
	recallResultLimit = 5
	// recallQueryMaxChars caps the query length before search. v1 parity.
	// Truncation is by rune (v1 byte-sliced, which could split a UTF-8
	// rune); the cap is a cost guard, so trimming a few extra runes is fine.
	recallQueryMaxChars = 500
)

// recall in-character result strings — ported from v1 resolveRecall /
// formatRecallHits so the model sees the same prose it learned around.
const (
	recallNoQueryText  = "You tried to remember something but couldn't form the question."
	recallFailedText   = "You tried to recall but the memory wouldn't come."
	recallNoMemoryText = "Nothing comes to mind."
)

// RecallArgs is the decoded shape of the recall tool's single argument.
type RecallArgs struct {
	Query string `json:"query"`
}

// recallSchema is the JSON Schema bytes shipped to the LLM provider. Single
// required string `query`. Description ported verbatim from v1.
var recallSchema = json.RawMessage(`{
    "type": "object",
    "properties": {
        "query": {
            "type": "string",
            "minLength": 1,
            "description": "What you're trying to remember."
        }
    },
    "required": ["query"],
    "additionalProperties": false
}`)

// recallDescription is the tool description advertised to the model — verbatim
// from v1 (engine/agent_tick.go).
const recallDescription = "Try to remember something — search your past notes, dreams, and impressions for anything relevant. Use this when you want to recall what you know about a person, place, or event. Phrase the query in your own words."

// DecodeRecallArgs parses the raw tool-call arguments into a RecallArgs.
// Rejects non-object payloads, unknown fields, and trailing data — same
// pattern as DecodeTakeBreakArgs. An empty/whitespace query is NOT rejected
// here: the handler turns it into the in-character recallNoQueryText result,
// matching v1 (resolveRecall returns prose, not an error, for an empty query).
func DecodeRecallArgs(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, decodeErrf("recall: arguments must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var args RecallArgs
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("recall: malformed arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, decodeErrf("recall: trailing data after JSON object")
		}
		return nil, fmt.Errorf("recall: malformed trailing data: %w", err)
	}
	return args, nil
}

// formatRecallHits renders the search hits as the tool-result text block.
// v2-simplified vs v1: recall searches a SINGLE namespace (the actor's own),
// so v1's per-hit "[namespace display name]" label (which required a
// slug→name cache refreshed every tick) is dropped — every hit is from the
// actor's own memory. Empty hits → recallNoMemoryText.
func formatRecallHits(hits []llm.MemoryHit) string {
	if len(hits) == 0 {
		return recallNoMemoryText
	}
	var b strings.Builder
	b.WriteString("You remember:\n\n")
	for _, h := range hits {
		fmt.Fprintf(&b, "— %s —\n%s\n\n", h.SourceFile, h.ChunkText)
	}
	return strings.TrimRight(b.String(), "\n")
}

// makeRecallHandler builds the recall ObservationFn closed over searcher. It
// runs OFF the world goroutine (worker pool); searcher.SearchMemory honors
// ctx. See the file-level doc for the in-character failure-handling contract.
func makeRecallHandler(searcher llm.MemorySearcher) ObservationFn {
	return func(ctx context.Context, in HandlerInput) (string, error) {
		args, ok := in.Args.(RecallArgs)
		if !ok {
			// Registration bug (decode produced the wrong type) — surface
			// as a handler error, not in-character prose.
			return "", fmt.Errorf("recall: handler received unexpected args type %T", in.Args)
		}
		query := strings.TrimSpace(args.Query)
		if query == "" {
			return recallNoQueryText, nil
		}
		if utf8.RuneCountInString(query) > recallQueryMaxChars {
			query = string([]rune(query)[:recallQueryMaxChars])
		}
		if strings.TrimSpace(in.LLMMemoryAgent) == "" {
			// No VA namespace to search (decorative / unbacked actor). v1
			// logged + returned the in-character failure string.
			log.Printf("handlers: recall for actor %q: no llm-memory namespace", in.ActorID)
			return recallFailedText, nil
		}
		hits, err := searcher.SearchMemory(ctx, in.LLMMemoryAgent, query, recallResultLimit)
		if err != nil {
			// Detailed error to the log; in-character string to the model
			// (don't leak API/transport detail into the LLM transcript).
			log.Printf("handlers: recall for actor %q (ns %q): search: %v", in.ActorID, in.LLMMemoryAgent, err)
			return recallFailedText, nil
		}
		return formatRecallHits(hits), nil
	}
}
